package services

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/wso2/asdlc/git-service/models"
	"github.com/wso2/asdlc/git-service/repositories"
)

// ----- Errors -----

var (
	// ErrArtifactNotFound is returned when the requested artifact (working-tree
	// file or tagged version) does not exist. Maps to 404 at the handler.
	ErrArtifactNotFound = errors.New("artifact not found")
	// ErrArtifactPathInvalid is returned for path traversal / illegal-shape
	// inputs. Maps to 400.
	ErrArtifactPathInvalid = errors.New("invalid artifact path")
	// ErrIfMatchFailed is returned by PutFile when the supplied If-Match sha
	// does not equal the current working-tree blob sha. Maps to 412.
	ErrIfMatchFailed = errors.New("if-match precondition failed")
	// ErrNoVersionToDiscard is returned by Discard when no tag exists for the
	// artifact type. Maps to 404 (matches BFF's prior "no saved version to
	// revert to" semantics).
	ErrNoVersionToDiscard = errors.New("no saved version to revert to")
	// ErrConcurrentTagWrite is returned when `git tag -a` fails because the
	// tag already exists locally with a different commit (some other actor
	// wrote it between step 0 self-heal and step 6 create). Maps to 409.
	ErrConcurrentTagWrite = errors.New("tag created concurrently by another writer")
)

// ----- Artifact types -----

// ArtifactType selects the artifact + its tag prefix + its commit-staging
// rules. The wire string (used in the URL path) is the same as the constant
// value.
type ArtifactType string

const (
	ArtifactSpec       ArtifactType = "spec"
	ArtifactDesign     ArtifactType = "design"
	ArtifactWireframes ArtifactType = "wireframes"
)

// artifactDef bundles the per-type settings: where the file lives in the
// working tree, what tag prefix tracks its versions, and (for spec) which
// extra paths are staged on save.
type artifactDef struct {
	relPath      string   // working-tree file path, e.g. ".asdlc/spec.md"
	tagPrefix    string   // "spec-v" / "design-v"
	commitMsg    string   // tag annotation description
	extraStage   []string // extra paths to `git add` alongside the primary file (spec save → wireframes dir)
}

func defFor(t ArtifactType) (artifactDef, bool) {
	switch t {
	case ArtifactSpec:
		return artifactDef{
			relPath:    ".asdlc/spec.md",
			tagPrefix:  specTagPrefix,
			commitMsg:  "Specification",
			extraStage: []string{".asdlc/wireframes"}, // pre-PR-0 bug: stage wireframes alongside spec
		}, true
	case ArtifactDesign:
		return artifactDef{
			relPath:   ".asdlc/design.json",
			tagPrefix: designTagPrefix,
			commitMsg: "Architecture design",
		}, true
	}
	return artifactDef{}, false
}

// ----- Wire shapes -----

// SaveRequest is the body of POST /artifacts/{type}/save.
type SaveRequest struct {
	Content string  `json:"content"` // required — caller must be the writer of the bytes
	Message string  `json:"message,omitempty"`
	Lineage Lineage `json:"lineage,omitempty"`
}

// SaveResult is the response body of POST /artifacts/{type}/save.
type SaveResult struct {
	Status     string  `json:"status"` // "approved" | "unchanged"
	Version    int     `json:"version"`
	Tag        string  `json:"tag"`
	CommitHash string  `json:"commitHash,omitempty"`
	Lineage    Lineage `json:"lineage"`
}

// FileResult is the response shape for GET /artifacts/{type} and friends.
type FileResult struct {
	Content string `json:"content"`
	SHA     string `json:"sha"`
}

// PutResult is the response shape for PUT /artifacts/{type}.
type PutResult struct {
	SHA string `json:"sha"`
}

// VersionFileResult is GET /artifacts/{type}/versions/{tag}.
type VersionFileResult struct {
	Content string  `json:"content"`
	Lineage Lineage `json:"lineage"`
}

// WireframeEntry is one row of the wireframes listing.
type WireframeEntry struct {
	Name string `json:"name"`
	Size int64  `json:"size"`
}

// ----- Service -----

// ArtifactService is the new typed entry-point for the artifact endpoints
// added in PR 1. It composes with gitOpsService so they share the
// per-project mutex + clone-readiness machinery (both live in the same
// package, so unexported helpers are reachable).
type ArtifactService interface {
	GetFile(ctx context.Context, projectID, relPath string) (*FileResult, error)
	PutFile(ctx context.Context, projectID, relPath, content, ifMatch string) (*PutResult, error)
	ListWireframes(ctx context.Context, projectID string) ([]WireframeEntry, error)

	Save(ctx context.Context, projectID string, t ArtifactType, req SaveRequest) (*SaveResult, error)
	Discard(ctx context.Context, projectID string, t ArtifactType) (*FileResult, error)

	ListVersions(ctx context.Context, projectID string, t ArtifactType) ([]VersionInfo, error)
	GetVersion(ctx context.Context, projectID string, t ArtifactType, version int) (*VersionFileResult, error)
}

type artifactService struct {
	repo   repositories.RepoRepository
	gitOps *gitOpsService // shared lock map, ensureCloneReady, resolveToken
}

// NewArtifactService builds an ArtifactService that piggy-backs on the
// existing GitOpsService for shared infrastructure (locks, clone readiness,
// credential resolution). The concrete *gitOpsService is required because
// the artifact flow needs lock-acquisition and clone-management primitives
// that aren't on the GitOpsService interface — they're package-private.
func NewArtifactService(repo repositories.RepoRepository, gitOps GitOpsService) ArtifactService {
	concrete, ok := gitOps.(*gitOpsService)
	if !ok {
		panic("artifact service requires the concrete gitOpsService for shared lock + clone helpers")
	}
	return &artifactService{repo: repo, gitOps: concrete}
}

// ----- Path validation -----

// wireframeNameRE bounds wireframe filenames to a safe charset — no path
// separators, no `..`, no leading dot. Spec/design have fixed paths so this
// only matters for wireframes.
var wireframeNameRE = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._-]{0,127}$`)

const maxArtifactBytes = 5 << 20 // 5 MiB cap, matches design

// validateRelPath ensures relPath is under .asdlc/, has no .. segments, and
// after Clean still starts with .asdlc/. The caller has already validated
// the {orgId}/{projectId} portion via the org-scope middleware, so this is
// the last line of defense before disk I/O.
func validateRelPath(relPath string) error {
	if relPath == "" {
		return fmt.Errorf("%w: empty path", ErrArtifactPathInvalid)
	}
	clean := filepath.Clean(relPath)
	if clean != relPath {
		// Reject any input whose canonical form differs (catches ".//x", "x/", etc.)
		return fmt.Errorf("%w: non-canonical path %q", ErrArtifactPathInvalid, relPath)
	}
	if strings.HasPrefix(clean, "/") || strings.HasPrefix(clean, "..") {
		return fmt.Errorf("%w: must be repo-relative under .asdlc/", ErrArtifactPathInvalid)
	}
	parts := strings.Split(clean, string(filepath.Separator))
	if parts[0] != ".asdlc" {
		return fmt.Errorf("%w: only .asdlc/ paths are accessible via this API", ErrArtifactPathInvalid)
	}
	for _, p := range parts {
		if p == ".." {
			return fmt.Errorf("%w: traversal in path", ErrArtifactPathInvalid)
		}
	}
	return nil
}

func validateWireframeName(name string) error {
	if !wireframeNameRE.MatchString(name) {
		return fmt.Errorf("%w: wireframe name %q", ErrArtifactPathInvalid, name)
	}
	return nil
}

// ----- File ops -----

func (s *artifactService) GetFile(ctx context.Context, projectID, relPath string) (*FileResult, error) {
	if err := validateRelPath(relPath); err != nil {
		return nil, err
	}

	mu := s.gitOps.getRepoLock(projectID)
	mu.Lock()
	defer mu.Unlock()

	repoRecord, err := s.requireReadyRepo(ctx, projectID)
	if err != nil {
		return nil, err
	}
	if err := s.gitOps.ensureCloneReady(ctx, repoRecord); err != nil {
		return nil, fmt.Errorf("ensure clone: %w", err)
	}

	abs := filepath.Join(repoRecord.ClonePath, relPath)
	data, err := os.ReadFile(abs)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, ErrArtifactNotFound
		}
		return nil, fmt.Errorf("read %s: %w", relPath, err)
	}

	sha, err := blobSHAFor(ctx, repoRecord.ClonePath, data)
	if err != nil {
		// SHA is informational; surface the read but with empty sha.
		slog.WarnContext(ctx, "hash-object failed", "path", relPath, "error", err)
	}
	return &FileResult{Content: string(data), SHA: sha}, nil
}

func (s *artifactService) PutFile(ctx context.Context, projectID, relPath, content, ifMatch string) (*PutResult, error) {
	if err := validateRelPath(relPath); err != nil {
		return nil, err
	}
	if len(content) > maxArtifactBytes {
		return nil, fmt.Errorf("%w: content exceeds %d bytes", ErrArtifactPathInvalid, maxArtifactBytes)
	}

	mu := s.gitOps.getRepoLock(projectID)
	mu.Lock()
	defer mu.Unlock()

	repoRecord, err := s.requireReadyRepo(ctx, projectID)
	if err != nil {
		return nil, err
	}
	if err := s.gitOps.ensureCloneReady(ctx, repoRecord); err != nil {
		return nil, fmt.Errorf("ensure clone: %w", err)
	}

	abs := filepath.Join(repoRecord.ClonePath, relPath)

	if ifMatch != "" {
		current, err := os.ReadFile(abs)
		if err != nil && !os.IsNotExist(err) {
			return nil, fmt.Errorf("read for if-match: %w", err)
		}
		var currentSHA string
		if err == nil {
			currentSHA, _ = blobSHAFor(ctx, repoRecord.ClonePath, current)
		}
		if currentSHA != ifMatch {
			return nil, ErrIfMatchFailed
		}
	}

	if err := atomicWrite(abs, []byte(content)); err != nil {
		return nil, fmt.Errorf("write %s: %w", relPath, err)
	}

	sha, err := blobSHAFor(ctx, repoRecord.ClonePath, []byte(content))
	if err != nil {
		return nil, fmt.Errorf("hash-object: %w", err)
	}
	return &PutResult{SHA: sha}, nil
}

func (s *artifactService) ListWireframes(ctx context.Context, projectID string) ([]WireframeEntry, error) {
	mu := s.gitOps.getRepoLock(projectID)
	mu.Lock()
	defer mu.Unlock()

	repoRecord, err := s.requireReadyRepo(ctx, projectID)
	if err != nil {
		return nil, err
	}
	if err := s.gitOps.ensureCloneReady(ctx, repoRecord); err != nil {
		return nil, fmt.Errorf("ensure clone: %w", err)
	}

	dir := filepath.Join(repoRecord.ClonePath, ".asdlc", "wireframes")
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return []WireframeEntry{}, nil
		}
		return nil, fmt.Errorf("readdir wireframes: %w", err)
	}
	out := make([]WireframeEntry, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		out = append(out, WireframeEntry{Name: e.Name(), Size: info.Size()})
	}
	return out, nil
}

// ----- Save -----

// Save runs the full atomic save flow under a single mutex hold:
//
//   - Step 0: self-heal — push --tags so any local-only tag from a previous
//     run (commit landed remote, tag-push failed) lands on remote.
//   - Step 1: tmpfile + rename `content` into the typed file path.
//   - Step 2: stage the file plus any extras (spec also stages
//     `.asdlc/wireframes/`).
//   - Step 3: commit with author identity from the org's credential.
//   - Step 4: push commit to default branch.
//   - Step 5: fetch tags, list this artifact's prefix, decide if HEAD differs
//     from the latest tagged content. If unchanged → return status=unchanged.
//   - Step 6: create + push the next tag with structured lineage in the
//     annotation. On `tag already exists` → 409. On tag-push fail → delete
//     the local tag (so step 0 of the next save doesn't silently absorb it
//     into a no-op) and surface the error.
func (s *artifactService) Save(ctx context.Context, projectID string, t ArtifactType, req SaveRequest) (*SaveResult, error) {
	def, ok := defFor(t)
	if !ok {
		return nil, fmt.Errorf("%w: artifact type %q", ErrArtifactPathInvalid, t)
	}
	if req.Content == "" {
		return nil, fmt.Errorf("%w: content is required on save", ErrArtifactPathInvalid)
	}
	if len(req.Content) > maxArtifactBytes {
		return nil, fmt.Errorf("%w: content exceeds %d bytes", ErrArtifactPathInvalid, maxArtifactBytes)
	}

	mu := s.gitOps.getRepoLock(projectID)
	mu.Lock()
	defer mu.Unlock()

	repoRecord, err := s.requireReadyRepo(ctx, projectID)
	if err != nil {
		return nil, err
	}
	if err := s.gitOps.ensureCloneReady(ctx, repoRecord); err != nil {
		return nil, fmt.Errorf("ensure clone: %w", err)
	}
	clonePath := repoRecord.ClonePath

	token, identity, err := s.gitOps.resolveToken(ctx, repoRecord)
	if err != nil {
		return nil, err
	}
	askPass, err := createAskPassScript(token)
	if err != nil {
		return nil, fmt.Errorf("askpass: %w", err)
	}
	defer os.Remove(askPass)

	authedEnv := append(os.Environ(),
		"GIT_ASKPASS="+askPass,
		"GIT_TERMINAL_PROMPT=0",
	)

	// Step 0: self-heal previous tag-push failures. Best-effort — a network
	// blip here shouldn't block a fresh save.
	if err := pushAllTags(ctx, clonePath, authedEnv); err != nil {
		slog.WarnContext(ctx, "self-heal push --tags failed (continuing)",
			"project", projectID, "error", err)
	}

	// Step 1: write content atomically.
	abs := filepath.Join(clonePath, def.relPath)
	if err := atomicWrite(abs, []byte(req.Content)); err != nil {
		return nil, fmt.Errorf("write %s: %w", def.relPath, err)
	}

	// Step 2: stage primary file + extras.
	if err := runGit(ctx, clonePath, "add", def.relPath); err != nil {
		return nil, fmt.Errorf("git add %s: %w", def.relPath, err)
	}
	for _, extra := range def.extraStage {
		extraAbs := filepath.Join(clonePath, extra)
		if _, statErr := os.Stat(extraAbs); statErr == nil {
			if err := runGit(ctx, clonePath, "add", extra); err != nil {
				return nil, fmt.Errorf("git add %s: %w", extra, err)
			}
		}
	}

	// Step 3: commit (skip cleanly if nothing-to-commit).
	authorName := identity.Name
	authorEmail := identity.Email
	commitArgs := []string{"commit", "-m", req.Message}
	if req.Message == "" {
		commitArgs[2] = fmt.Sprintf("Update %s", def.commitMsg)
	}
	if authorName != "" && authorEmail != "" {
		commitArgs = append(commitArgs, fmt.Sprintf("--author=%s <%s>", authorName, authorEmail))
	}
	commitCmd := exec.CommandContext(ctx, "git", commitArgs...)
	commitCmd.Dir = clonePath
	commitCmd.Env = append(os.Environ(),
		"GIT_COMMITTER_NAME="+authorName,
		"GIT_COMMITTER_EMAIL="+authorEmail,
	)
	var commitStderr bytes.Buffer
	commitCmd.Stderr = &commitStderr
	commitErr := commitCmd.Run()
	if commitErr != nil {
		errMsg := commitStderr.String()
		if !strings.Contains(errMsg, "nothing to commit") {
			return nil, fmt.Errorf("git commit: %s: %w", errMsg, commitErr)
		}
		// Nothing-to-commit is OK — fall through to step 5 to figure out
		// whether the *current* HEAD is already tagged, in which case we
		// short-circuit unchanged.
	}

	// Step 4: push the commit (only if a new commit was created).
	if commitErr == nil {
		if err := runGitWithEnv(ctx, clonePath, authedEnv, "push", "origin", repoRecord.DefaultBranch); err != nil {
			return nil, fmt.Errorf("git push: %w", err)
		}
	}

	// Step 5: list tags + decide whether to bump version.
	if err := bestEffortFetchTags(ctx, clonePath, authedEnv); err != nil {
		slog.WarnContext(ctx, "fetch --tags failed (continuing with local view)",
			"project", projectID, "error", err)
	}
	tags, err := listTagsForPrefix(ctx, clonePath, def.tagPrefix)
	if err != nil {
		return nil, fmt.Errorf("list %s tags: %w", def.tagPrefix, err)
	}

	headHash, err := runGitOutput(ctx, clonePath, "rev-parse", "HEAD")
	if err != nil {
		return nil, fmt.Errorf("rev-parse HEAD: %w", err)
	}
	headHash = strings.TrimSpace(headHash)

	if existing := latestTagName(tags, def.tagPrefix); existing != "" {
		// If working-tree (now == HEAD after step 3) matches the latest
		// tagged content, return unchanged. Use git show to compare against
		// the tag's blob — same shape as the BFF used to do via GetFileAtTag.
		taggedContent, err := runGitOutput(ctx, clonePath, "show", existing+":"+def.relPath)
		if err == nil && strings.TrimSpace(taggedContent) == strings.TrimSpace(req.Content) {
			latestVer := latestTagVersion(tags, def.tagPrefix)
			var lineage Lineage
			for _, t := range tags {
				if t.Name == existing {
					lineage = parseLineage(t.Message)
					break
				}
			}
			return &SaveResult{
				Status:  "unchanged",
				Version: latestVer,
				Tag:     existing,
				Lineage: lineage,
			}, nil
		}
	}

	// Step 6: create + push next tag.
	nextVer, tagName := nextVersion(tags, def.tagPrefix)
	tagDesc := fmt.Sprintf("%s v%d", def.commitMsg, nextVer)
	tagBody := buildLineageMessage(tagDesc, req.Lineage)

	tagCmd := exec.CommandContext(ctx, "git", "tag", "-a", tagName, "-m", tagBody)
	tagCmd.Dir = clonePath
	tagCmd.Env = append(os.Environ(),
		"GIT_COMMITTER_NAME="+authorName,
		"GIT_COMMITTER_EMAIL="+authorEmail,
	)
	var tagStderr bytes.Buffer
	tagCmd.Stderr = &tagStderr
	if err := tagCmd.Run(); err != nil {
		errMsg := tagStderr.String()
		if strings.Contains(errMsg, "already exists") {
			// Concurrent write by another actor (manual ops or, in the
			// future, a multi-replica sibling). Don't delete — the tag
			// isn't ours.
			return nil, fmt.Errorf("%w: %s", ErrConcurrentTagWrite, tagName)
		}
		return nil, fmt.Errorf("git tag -a %s: %s: %w", tagName, errMsg, err)
	}

	if err := runGitWithEnv(ctx, clonePath, authedEnv, "push", "origin", tagName); err != nil {
		// Push failed. Delete the local tag so the next save's step 0
		// self-heal doesn't silently absorb the missing remote tag, and so
		// step 5's `latestTagName` lookup recomputes against the actual
		// shared state.
		if delErr := runGit(ctx, clonePath, "tag", "-d", tagName); delErr != nil {
			slog.ErrorContext(ctx, "failed to delete local tag after push failure",
				"project", projectID, "tag", tagName, "error", delErr)
		}
		return nil, fmt.Errorf("push tag %s: %w", tagName, err)
	}

	slog.InfoContext(ctx, "artifact saved + tagged",
		"project", projectID, "type", t, "tag", tagName, "head", headHash)

	return &SaveResult{
		Status:     "approved",
		Version:    nextVer,
		Tag:        tagName,
		CommitHash: headHash,
		Lineage:    req.Lineage,
	}, nil
}

// ----- Discard -----

// Discard reverts the working-tree file to its content at the latest tag.
// Returns ErrNoVersionToDiscard if no tag exists yet for this artifact type
// — the design pins this to 404 to match BFF's prior "no saved version to
// revert to" wording.
func (s *artifactService) Discard(ctx context.Context, projectID string, t ArtifactType) (*FileResult, error) {
	def, ok := defFor(t)
	if !ok {
		return nil, fmt.Errorf("%w: artifact type %q", ErrArtifactPathInvalid, t)
	}

	mu := s.gitOps.getRepoLock(projectID)
	mu.Lock()
	defer mu.Unlock()

	repoRecord, err := s.requireReadyRepo(ctx, projectID)
	if err != nil {
		return nil, err
	}
	if err := s.gitOps.ensureCloneReady(ctx, repoRecord); err != nil {
		return nil, fmt.Errorf("ensure clone: %w", err)
	}
	clonePath := repoRecord.ClonePath

	token, _, err := s.gitOps.resolveToken(ctx, repoRecord)
	if err != nil {
		return nil, err
	}
	askPass, err := createAskPassScript(token)
	if err != nil {
		return nil, fmt.Errorf("askpass: %w", err)
	}
	defer os.Remove(askPass)
	authedEnv := append(os.Environ(),
		"GIT_ASKPASS="+askPass,
		"GIT_TERMINAL_PROMPT=0",
	)

	if err := bestEffortFetchTags(ctx, clonePath, authedEnv); err != nil {
		slog.WarnContext(ctx, "discard: fetch --tags failed (continuing)",
			"project", projectID, "error", err)
	}
	tags, err := listTagsForPrefix(ctx, clonePath, def.tagPrefix)
	if err != nil {
		return nil, fmt.Errorf("list %s tags: %w", def.tagPrefix, err)
	}
	tagName := latestTagName(tags, def.tagPrefix)
	if tagName == "" {
		return nil, ErrNoVersionToDiscard
	}

	taggedContent, err := runGitOutput(ctx, clonePath, "show", tagName+":"+def.relPath)
	if err != nil {
		return nil, fmt.Errorf("git show %s:%s: %w", tagName, def.relPath, err)
	}

	abs := filepath.Join(clonePath, def.relPath)
	if err := atomicWrite(abs, []byte(taggedContent)); err != nil {
		return nil, fmt.Errorf("write %s: %w", def.relPath, err)
	}

	sha, _ := blobSHAFor(ctx, clonePath, []byte(taggedContent))
	return &FileResult{Content: taggedContent, SHA: sha}, nil
}

// ----- Versions -----

func (s *artifactService) ListVersions(ctx context.Context, projectID string, t ArtifactType) ([]VersionInfo, error) {
	def, ok := defFor(t)
	if !ok {
		return nil, fmt.Errorf("%w: artifact type %q", ErrArtifactPathInvalid, t)
	}

	mu := s.gitOps.getRepoLock(projectID)
	mu.Lock()
	defer mu.Unlock()

	repoRecord, err := s.requireReadyRepo(ctx, projectID)
	if err != nil {
		return nil, err
	}
	if err := s.gitOps.ensureCloneReady(ctx, repoRecord); err != nil {
		return nil, fmt.Errorf("ensure clone: %w", err)
	}
	clonePath := repoRecord.ClonePath

	token, _, err := s.gitOps.resolveToken(ctx, repoRecord)
	if err != nil {
		return nil, err
	}
	askPass, err := createAskPassScript(token)
	if err != nil {
		return nil, fmt.Errorf("askpass: %w", err)
	}
	defer os.Remove(askPass)
	authedEnv := append(os.Environ(),
		"GIT_ASKPASS="+askPass,
		"GIT_TERMINAL_PROMPT=0",
	)
	_ = bestEffortFetchTags(ctx, clonePath, authedEnv)

	tags, err := listTagsForPrefix(ctx, clonePath, def.tagPrefix)
	if err != nil {
		return nil, fmt.Errorf("list %s tags: %w", def.tagPrefix, err)
	}
	return tagsToVersions(tags, def.tagPrefix), nil
}

func (s *artifactService) GetVersion(ctx context.Context, projectID string, t ArtifactType, version int) (*VersionFileResult, error) {
	def, ok := defFor(t)
	if !ok {
		return nil, fmt.Errorf("%w: artifact type %q", ErrArtifactPathInvalid, t)
	}
	if version < 1 {
		return nil, fmt.Errorf("%w: version must be >= 1", ErrArtifactPathInvalid)
	}

	mu := s.gitOps.getRepoLock(projectID)
	mu.Lock()
	defer mu.Unlock()

	repoRecord, err := s.requireReadyRepo(ctx, projectID)
	if err != nil {
		return nil, err
	}
	if err := s.gitOps.ensureCloneReady(ctx, repoRecord); err != nil {
		return nil, fmt.Errorf("ensure clone: %w", err)
	}
	clonePath := repoRecord.ClonePath

	tagName := fmt.Sprintf("%s%d", def.tagPrefix, version)
	content, err := runGitOutput(ctx, clonePath, "show", tagName+":"+def.relPath)
	if err != nil {
		errMsg := err.Error()
		if strings.Contains(errMsg, "not a valid object") || strings.Contains(errMsg, "does not exist") {
			return nil, ErrArtifactNotFound
		}
		return nil, fmt.Errorf("git show %s:%s: %w", tagName, def.relPath, err)
	}

	// Lineage from tag annotation. Best-effort: missing message → empty
	// lineage rather than an error.
	msg, _ := runGitOutput(ctx, clonePath, "tag", "-l", tagName, "--format=%(contents)")
	return &VersionFileResult{
		Content: content,
		Lineage: parseLineage(strings.TrimSpace(msg)),
	}, nil
}

// ----- Helpers -----

func (s *artifactService) requireReadyRepo(ctx context.Context, projectID string) (*models.GitRepository, error) {
	repoRecord, err := s.repo.GetByProjectID(ctx, projectID)
	if err != nil {
		return nil, fmt.Errorf("get repo: %w", err)
	}
	if repoRecord == nil {
		return nil, ErrRepoNotFound
	}
	if repoRecord.Status != "ready" {
		return nil, ErrRepoNotReady
	}
	return repoRecord, nil
}

// atomicWrite writes data via a sibling temp file + rename so a partial
// write never leaves the target file truncated. Creates parent dirs as
// needed (e.g. .asdlc/wireframes/ before the first wireframe lands).
func atomicWrite(absPath string, data []byte) error {
	dir := filepath.Dir(absPath)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("mkdir %s: %w", dir, err)
	}
	suffix := make([]byte, 8)
	if _, err := rand.Read(suffix); err != nil {
		return err
	}
	tmp := filepath.Join(dir, ".tmp-"+filepath.Base(absPath)+"-"+hex.EncodeToString(suffix))
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, absPath); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

// blobSHAFor computes `git hash-object` for the given content. Stable
// across replica restarts (no mtime dependence) and independent of whether
// the file is currently staged or committed.
func blobSHAFor(ctx context.Context, clonePath string, data []byte) (string, error) {
	cmd := exec.CommandContext(ctx, "git", "hash-object", "--stdin")
	cmd.Dir = clonePath
	cmd.Stdin = bytes.NewReader(data)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("hash-object: %s: %w", stderr.String(), err)
	}
	return strings.TrimSpace(string(out)), nil
}

// pushAllTags is the step-0 self-heal: a previous save may have created and
// pushed a commit but failed to push its annotated tag. `git push --tags`
// uploads any local-only refs/tags. Best-effort — the caller logs a warning
// rather than failing the new save.
func pushAllTags(ctx context.Context, clonePath string, authedEnv []string) error {
	cmd := exec.CommandContext(ctx, "git", "push", "--tags", "origin")
	cmd.Dir = clonePath
	cmd.Env = authedEnv
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("push --tags: %s: %w", stderr.String(), err)
	}
	return nil
}

// bestEffortFetchTags refreshes our local view of remote tags. The caller
// logs a warning on failure rather than aborting — listing local-only tags
// is still useful, just possibly out of date.
func bestEffortFetchTags(ctx context.Context, clonePath string, authedEnv []string) error {
	cmd := exec.CommandContext(ctx, "git", "fetch", "--tags", "origin")
	cmd.Dir = clonePath
	cmd.Env = authedEnv
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("fetch --tags: %s", stderr.String())
	}
	return nil
}

// listTagsForPrefix shells `git tag -l <prefix>*` plus per-tag rev-list +
// annotation lookups. Mirrors the existing gitOpsService.ListTags shape so
// callers get the same TagInfo struct back.
func listTagsForPrefix(ctx context.Context, clonePath, prefix string) ([]TagInfo, error) {
	pattern := prefix + "*"
	output, err := runGitOutput(ctx, clonePath, "tag", "-l", pattern, "--sort=-version:refname")
	if err != nil {
		return nil, fmt.Errorf("git tag -l: %w", err)
	}
	output = strings.TrimSpace(output)
	if output == "" {
		return []TagInfo{}, nil
	}
	lines := strings.Split(output, "\n")
	tags := make([]TagInfo, 0, len(lines))
	for _, line := range lines {
		name := strings.TrimSpace(line)
		if name == "" {
			continue
		}
		hash, err := runGitOutput(ctx, clonePath, "rev-list", "-1", name)
		if err != nil {
			continue
		}
		msg, _ := runGitOutput(ctx, clonePath, "tag", "-l", name, "--format=%(contents)")
		tags = append(tags, TagInfo{
			Name:       name,
			CommitHash: strings.TrimSpace(hash),
			Message:    strings.TrimSpace(msg),
		})
	}
	return tags, nil
}

// runGitWithEnv is the explicit-env variant of runGit used when we need to
// pass GIT_ASKPASS for an authed remote operation. The default runGit uses
// the inherited environment, which doesn't include the ephemeral askpass
// path.
func runGitWithEnv(ctx context.Context, dir string, env []string, args ...string) error {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	cmd.Env = env
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%s: %w", strings.TrimSpace(stderr.String()), err)
	}
	return nil
}
