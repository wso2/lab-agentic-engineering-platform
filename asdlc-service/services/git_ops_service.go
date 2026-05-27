package services

import (
	"bytes"
	"context"
	cryptorand "crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/wso2/asdlc/asdlc-service/models"
	"github.com/wso2/asdlc/asdlc-service/internal/credentials"
	"github.com/wso2/asdlc/asdlc-service/repositories"
)

// cryptoRandRead is exposed as a func var so tests can substitute a
// deterministic source. Real callers use crypto/rand.Read.
var cryptoRandRead = cryptorand.Read

// CommitRequest describes a git commit operation.
type CommitRequest struct {
	Message     string   `json:"message"`
	Files       []string `json:"files"`
	Directory   string   `json:"directory,omitempty"` // stage all changes under this dir
	AuthorName  string   `json:"authorName"`
	AuthorEmail string   `json:"authorEmail"`
}

// CommitResult is the result of a commit.
type CommitResult struct {
	CommitHash     string   `json:"commitHash"`
	Message        string   `json:"message"`
	FilesCommitted []string `json:"filesCommitted"`
}

// RepoStatus describes the git working tree status.
type RepoStatus struct {
	Branch  string   `json:"branch"`
	Clean   bool     `json:"clean"`
	Changes []string `json:"changes"`
}

// CreateTagRequest describes a git tag operation.
type CreateTagRequest struct {
	TagName string `json:"tagName"`
	Message string `json:"message"`
}

// TagResult is the result of creating a tag.
type TagResult struct {
	TagName    string `json:"tagName"`
	CommitHash string `json:"commitHash"`
}

// TagInfo describes a git tag.
type TagInfo struct {
	Name       string `json:"name"`
	CommitHash string `json:"commitHash"`
	Message    string `json:"message,omitempty"`
}

// GitOpsService handles git operations (commit, push, pull, status, tags).
type GitOpsService interface {
	Commit(ctx context.Context, projectID string, req CommitRequest) (*CommitResult, error)
	Push(ctx context.Context, projectID string, branch string) error
	Pull(ctx context.Context, projectID string, branch string) error
	Status(ctx context.Context, projectID string) (*RepoStatus, error)
	CreateTag(ctx context.Context, projectID string, req CreateTagRequest) (*TagResult, error)
	ListTags(ctx context.Context, projectID string, prefix string) ([]TagInfo, error)
	GetFileAtTag(ctx context.Context, projectID string, tag string, filePath string) (string, error)

	// Startup lifecycle (PR 1):
	//   - CleanupOrphanTmpClones removes leftover .tmpclone-* dirs from a
	//     prior crash mid-clone.
	//   - PreWarmClones ensures every ready repo's clone is on disk before
	//     the first request arrives, so cold-start traffic doesn't pay the
	//     multi-minute clone latency under the per-project mutex.
	CleanupOrphanTmpClones()
	PreWarmClones(ctx context.Context, workers int) (warmed, failed int)
}

type gitOpsService struct {
	repo         repositories.RepoRepository
	resolver     credentials.Resolver
	repoBasePath string
	locks        sync.Map // per-project mutex
	// gitHub is the GitHubClient injected for artifact-store v2 (V1) save flows.
	// May be nil in legacy test wiring that doesn't exercise SaveDesign /
	// SaveRequirements; the save flows nil-check before dereferencing.
	gitHub GitHubClient
}

// NewGitOpsService builds the service. The optional `github` arg is the
// GitHub HTTP client used by the artifact-store v2 save flow. Pass nil from
// tests that don't exercise save paths.
func NewGitOpsService(repo repositories.RepoRepository, resolver credentials.Resolver, repoBasePath string, github GitHubClient) GitOpsService {
	return &gitOpsService{repo: repo, resolver: resolver, repoBasePath: repoBasePath, gitHub: github}
}

func (s *gitOpsService) getRepoLock(projectID string) *sync.Mutex {
	val, _ := s.locks.LoadOrStore(projectID, &sync.Mutex{})
	return val.(*sync.Mutex)
}

// resolveToken returns the GitHub token for the org owning the given repo.
// All on-disk git operations (clone/fetch/push) use this to authenticate.
func (s *gitOpsService) resolveToken(ctx context.Context, gitRepo *models.GitRepository) (string, credentials.Identity, error) {
	cred, err := s.resolver.Resolve(ctx, gitRepo.OrgID)
	if err != nil {
		return "", credentials.Identity{}, fmt.Errorf("resolve credential: %w", err)
	}
	token, _, err := cred.Token(ctx)
	if err != nil {
		return "", credentials.Identity{}, fmt.Errorf("token: %w", err)
	}
	return token, cred.Identity(), nil
}

// ErrCloneCorrupt is returned when the working tree exists with content but
// `.git` is missing/corrupt. Refusing to destructively re-clone in this
// state is the PR 1 hardening: under the old code path we'd `RemoveAll` the
// directory and silently lose any unsaved spec/design content. Now we
// surface the state to the operator (503 + runbook) instead.
var ErrCloneCorrupt = errors.New("clone has content but .git is missing or corrupt")

// ensureCloneReady verifies that the clone directory exists on disk. If the
// stored clone path is stale (e.g. REPO_BASE_PATH changed between runs) it
// updates the path. If the .git dir is absent, this performs a
// non-destructive re-clone: clone into a sibling `.tmpclone-<ts>` dir, then
// atomically rename over the existing path. If the existing path holds
// content but no .git (corruption), refuse to nuke it and return
// ErrCloneCorrupt.
func (s *gitOpsService) ensureCloneReady(ctx context.Context, repoRecord *models.GitRepository) error {
	expectedPath := filepath.Join(s.repoBasePath, repoRecord.OrgID, repoRecord.ProjectID)

	// If the stored clone path doesn't match the current base path, update it.
	if repoRecord.ClonePath != expectedPath {
		slog.InfoContext(ctx, "clone path changed, updating",
			"project", repoRecord.ProjectID,
			"old", repoRecord.ClonePath,
			"new", expectedPath)
		repoRecord.ClonePath = expectedPath
	}

	// Check if the .git directory exists at the expected path.
	if _, err := os.Stat(filepath.Join(repoRecord.ClonePath, ".git")); err == nil {
		return nil // clone exists and looks valid
	}

	// .git is missing. Before re-cloning, refuse to delete a non-empty
	// working tree — that's the data-loss guard. If the directory exists
	// and has content (anything other than an empty dir or non-existent),
	// surface ErrCloneCorrupt to the caller.
	if hasContent(repoRecord.ClonePath) {
		slog.ErrorContext(ctx, "clone directory has content but no .git; refusing destructive re-clone",
			"project", repoRecord.ProjectID, "path", repoRecord.ClonePath)
		return ErrCloneCorrupt
	}

	slog.InfoContext(ctx, "clone directory missing, re-cloning",
		"project", repoRecord.ProjectID, "path", repoRecord.ClonePath)

	if err := s.cloneIntoPath(ctx, repoRecord); err != nil {
		return err
	}

	// Update branch and path in the DB.
	repoRecord.DefaultBranch = detectDefaultBranch(repoRecord.ClonePath)
	repoRecord.Status = "ready"
	repoRecord.ErrorMessage = ""
	if err := s.repo.Update(ctx, repoRecord); err != nil {
		slog.ErrorContext(ctx, "failed to update repo after re-clone", "error", err)
	}

	slog.InfoContext(ctx, "re-clone completed", "project", repoRecord.ProjectID)
	return nil
}

// hasContent returns true if path exists and has at least one entry. An
// empty dir or non-existent path is "no content" (safe to re-clone into).
func hasContent(path string) bool {
	entries, err := os.ReadDir(path)
	if err != nil {
		return false // doesn't exist or unreadable → not "content we'd lose"
	}
	return len(entries) > 0
}

// cloneIntoPath does the git clone with PR 1 semantics: clone into a sibling
// `.tmpclone-<random>` directory, then atomically rename it onto the
// expected path. Any existing empty directory at the path is removed first
// so the rename can land. The temp dir is also cleaned on error so retries
// don't accumulate orphans.
func (s *gitOpsService) cloneIntoPath(ctx context.Context, repoRecord *models.GitRepository) error {
	parent := filepath.Dir(repoRecord.ClonePath)
	if err := os.MkdirAll(parent, 0o755); err != nil {
		return fmt.Errorf("create parent dir: %w", err)
	}

	// Sibling tmpclone path. Random suffix so concurrent retries on the
	// same project don't collide if one is still running. cleanupOrphanTmpClones
	// at startup handles any survivors.
	suffix := make([]byte, 6)
	if _, err := cryptoRandRead(suffix); err != nil {
		return fmt.Errorf("random suffix: %w", err)
	}
	tmpPath := filepath.Join(parent, ".tmpclone-"+repoRecord.ProjectID+"-"+hex.EncodeToString(suffix))
	defer os.RemoveAll(tmpPath) //nolint:errcheck — cleanup best-effort on success too

	token, _, err := s.resolveToken(ctx, repoRecord)
	if err != nil {
		return err
	}
	askPassScript, err := createAskPassScript(token)
	if err != nil {
		return fmt.Errorf("create askpass script: %w", err)
	}
	defer os.Remove(askPassScript)

	cloneCtx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()

	cmd := exec.CommandContext(cloneCtx, "git", "clone", repoRecord.RepoURL, tmpPath)
	cmd.Env = append(os.Environ(),
		"GIT_ASKPASS="+askPassScript,
		"GIT_TERMINAL_PROMPT=0",
	)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("re-clone failed: %s", stderr.String())
	}

	// Drop any pre-existing empty directory so the rename can land.
	if entries, err := os.ReadDir(repoRecord.ClonePath); err == nil && len(entries) == 0 {
		_ = os.Remove(repoRecord.ClonePath)
	}

	if err := os.Rename(tmpPath, repoRecord.ClonePath); err != nil {
		return fmt.Errorf("rename tmpclone into place: %w", err)
	}
	return nil
}

// CleanupOrphanTmpClones removes any leftover `.tmpclone-*` directories
// under repoBasePath. Called once at server startup before pre-warm so a
// previous crash mid-clone doesn't leave the disk littered.
func (s *gitOpsService) CleanupOrphanTmpClones() {
	orgs, err := os.ReadDir(s.repoBasePath)
	if err != nil {
		return
	}
	for _, org := range orgs {
		if !org.IsDir() {
			continue
		}
		orgDir := filepath.Join(s.repoBasePath, org.Name())
		entries, err := os.ReadDir(orgDir)
		if err != nil {
			continue
		}
		for _, e := range entries {
			if strings.HasPrefix(e.Name(), ".tmpclone-") {
				path := filepath.Join(orgDir, e.Name())
				if err := os.RemoveAll(path); err == nil {
					slog.Info("cleaned orphan tmpclone", "path", path)
				}
			}
		}
	}
}

// PreWarmClones reads every git_repositories row and ensures each clone is
// on disk. Concurrency-bounded by `workers`; failures are surfaced via
// per-row warning logs (caller's gauges already track pending/failed
// counts). Called once at server startup so cold-start traffic doesn't pay
// the multi-minute clone latency under the per-project mutex.
func (s *gitOpsService) PreWarmClones(ctx context.Context, workers int) (warmed, failed int) {
	repos, err := s.allRepos(ctx)
	if err != nil {
		slog.Error("pre-warm: list repos failed", "error", err)
		return 0, 0
	}
	if workers <= 0 {
		workers = 1
	}
	sem := make(chan struct{}, workers)
	var wg sync.WaitGroup
	var mu sync.Mutex
	for i := range repos {
		repo := &repos[i]
		if repo.Status != "ready" {
			continue
		}
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer wg.Done()
			defer func() { <-sem }()

			lock := s.getRepoLock(repo.ProjectID)
			lock.Lock()
			defer lock.Unlock()

			if err := s.ensureCloneReady(ctx, repo); err != nil {
				slog.Warn("pre-warm: ensure clone failed",
					"project", repo.ProjectID, "org", repo.OrgID, "error", err)
				mu.Lock()
				failed++
				mu.Unlock()
				return
			}
			mu.Lock()
			warmed++
			mu.Unlock()
		}()
	}
	wg.Wait()
	return warmed, failed
}

// allRepos paginates through git_repositories. The repo layer doesn't have
// a "list everything" method (intentionally — every other path is keyed on
// a specific project), so we go around it with a direct GORM query for
// pre-warm only.
func (s *gitOpsService) allRepos(ctx context.Context) ([]models.GitRepository, error) {
	type lister interface {
		ListAllReady(ctx context.Context) ([]models.GitRepository, error)
	}
	if l, ok := s.repo.(lister); ok {
		return l.ListAllReady(ctx)
	}
	return nil, nil
}

func (s *gitOpsService) Commit(ctx context.Context, projectID string, req CommitRequest) (*CommitResult, error) {
	mu := s.getRepoLock(projectID)
	mu.Lock()
	defer mu.Unlock()

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

	if err := s.ensureCloneReady(ctx, repoRecord); err != nil {
		return nil, fmt.Errorf("ensure clone: %w", err)
	}

	// Default committer identity to the credential's identity. Caller can
	// override for spec/design tag flows that want a specific author.
	_, identity, err := s.resolveToken(ctx, repoRecord)
	if err != nil {
		return nil, err
	}

	clonePath := repoRecord.ClonePath

	// Stage files
	for _, file := range req.Files {
		if err := runGit(ctx, clonePath, "add", file); err != nil {
			return nil, fmt.Errorf("git add %s: %w", file, err)
		}
	}

	// If no specific files, stage by directory or all
	if len(req.Files) == 0 {
		stageTarget := "-A"
		if req.Directory != "" {
			stageTarget = req.Directory
		}
		if err := runGit(ctx, clonePath, "add", stageTarget); err != nil {
			return nil, fmt.Errorf("git add %s: %w", stageTarget, err)
		}
	}

	// Commit
	authorName := req.AuthorName
	authorEmail := req.AuthorEmail
	if authorName == "" {
		authorName = identity.Name
	}
	if authorEmail == "" {
		authorEmail = identity.Email
	}
	args := []string{"commit", "-m", req.Message}
	if authorName != "" && authorEmail != "" {
		args = append(args, fmt.Sprintf("--author=%s <%s>", authorName, authorEmail))
	}

	commitCmd := exec.CommandContext(ctx, "git", args...)
	commitCmd.Dir = clonePath
	commitCmd.Env = append(os.Environ(),
		"GIT_COMMITTER_NAME="+authorName,
		"GIT_COMMITTER_EMAIL="+authorEmail,
	)
	var commitStderr bytes.Buffer
	commitCmd.Stderr = &commitStderr
	if err := commitCmd.Run(); err != nil {
		errMsg := commitStderr.String()
		// Check if nothing to commit
		if strings.Contains(errMsg, "nothing to commit") {
			return &CommitResult{
				Message:        req.Message,
				FilesCommitted: []string{},
			}, nil
		}
		return nil, fmt.Errorf("git commit: %s: %w", errMsg, err)
	}

	// Get commit hash
	hash, err := runGitOutput(ctx, clonePath, "rev-parse", "HEAD")
	if err != nil {
		return nil, fmt.Errorf("get commit hash: %w", err)
	}

	return &CommitResult{
		CommitHash:     strings.TrimSpace(hash),
		Message:        req.Message,
		FilesCommitted: req.Files,
	}, nil
}

func (s *gitOpsService) Push(ctx context.Context, projectID string, branch string) error {
	mu := s.getRepoLock(projectID)
	mu.Lock()
	defer mu.Unlock()

	repoRecord, err := s.repo.GetByProjectID(ctx, projectID)
	if err != nil {
		return fmt.Errorf("get repo: %w", err)
	}
	if repoRecord == nil {
		return ErrRepoNotFound
	}
	if repoRecord.Status != "ready" {
		return ErrRepoNotReady
	}

	if err := s.ensureCloneReady(ctx, repoRecord); err != nil {
		return fmt.Errorf("ensure clone: %w", err)
	}

	if branch == "" {
		branch = repoRecord.DefaultBranch
	}

	token, _, err := s.resolveToken(ctx, repoRecord)
	if err != nil {
		return err
	}
	askPassScript, err := createAskPassScript(token)
	if err != nil {
		return fmt.Errorf("create askpass script: %w", err)
	}
	defer os.Remove(askPassScript)

	cmd := exec.CommandContext(ctx, "git", "push", "origin", branch)
	cmd.Dir = repoRecord.ClonePath
	cmd.Env = append(os.Environ(),
		"GIT_ASKPASS="+askPassScript,
		"GIT_TERMINAL_PROMPT=0",
	)

	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		errMsg := stderr.String()
		if strings.Contains(errMsg, "rejected") || strings.Contains(errMsg, "non-fast-forward") {
			return fmt.Errorf("%w: %s", ErrPushConflict, errMsg)
		}
		if strings.Contains(errMsg, "Authentication") {
			return fmt.Errorf("%w: %s", ErrAuthFailed, errMsg)
		}
		return fmt.Errorf("git push: %s", errMsg)
	}

	return nil
}

func (s *gitOpsService) Pull(ctx context.Context, projectID string, branch string) error {
	mu := s.getRepoLock(projectID)
	mu.Lock()
	defer mu.Unlock()

	repoRecord, err := s.repo.GetByProjectID(ctx, projectID)
	if err != nil {
		return fmt.Errorf("get repo: %w", err)
	}
	if repoRecord == nil {
		return ErrRepoNotFound
	}
	if repoRecord.Status != "ready" {
		return ErrRepoNotReady
	}

	if err := s.ensureCloneReady(ctx, repoRecord); err != nil {
		return fmt.Errorf("ensure clone: %w", err)
	}

	if branch == "" {
		branch = repoRecord.DefaultBranch
	}

	token, _, err := s.resolveToken(ctx, repoRecord)
	if err != nil {
		return err
	}
	askPassScript, err := createAskPassScript(token)
	if err != nil {
		return fmt.Errorf("create askpass script: %w", err)
	}
	defer os.Remove(askPassScript)

	cmd := exec.CommandContext(ctx, "git", "pull", "origin", branch)
	cmd.Dir = repoRecord.ClonePath
	cmd.Env = append(os.Environ(),
		"GIT_ASKPASS="+askPassScript,
		"GIT_TERMINAL_PROMPT=0",
	)

	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return fmt.Errorf("git pull: %s", stderr.String())
	}

	return nil
}

func (s *gitOpsService) Status(ctx context.Context, projectID string) (*RepoStatus, error) {
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

	if err := s.ensureCloneReady(ctx, repoRecord); err != nil {
		return nil, fmt.Errorf("ensure clone: %w", err)
	}

	clonePath := repoRecord.ClonePath

	// Get current branch
	branch, err := runGitOutput(ctx, clonePath, "rev-parse", "--abbrev-ref", "HEAD")
	if err != nil {
		return nil, fmt.Errorf("get branch: %w", err)
	}

	// Get status
	statusOutput, err := runGitOutput(ctx, clonePath, "status", "--porcelain")
	if err != nil {
		return nil, fmt.Errorf("git status: %w", err)
	}

	var changes []string
	if statusOutput != "" {
		changes = strings.Split(strings.TrimSpace(statusOutput), "\n")
	}

	return &RepoStatus{
		Branch:  strings.TrimSpace(branch),
		Clean:   len(changes) == 0,
		Changes: changes,
	}, nil
}

func (s *gitOpsService) CreateTag(ctx context.Context, projectID string, req CreateTagRequest) (*TagResult, error) {
	mu := s.getRepoLock(projectID)
	mu.Lock()
	defer mu.Unlock()

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

	if err := s.ensureCloneReady(ctx, repoRecord); err != nil {
		return nil, fmt.Errorf("ensure clone: %w", err)
	}

	clonePath := repoRecord.ClonePath

	token, identity, err := s.resolveToken(ctx, repoRecord)
	if err != nil {
		return nil, err
	}

	// Create annotated tag
	tagCmd := exec.CommandContext(ctx, "git", "tag", "-a", req.TagName, "-m", req.Message)
	tagCmd.Dir = clonePath
	tagCmd.Env = append(os.Environ(),
		"GIT_COMMITTER_NAME="+identity.Name,
		"GIT_COMMITTER_EMAIL="+identity.Email,
	)
	var tagStderr bytes.Buffer
	tagCmd.Stderr = &tagStderr
	if err := tagCmd.Run(); err != nil {
		errMsg := tagStderr.String()
		if strings.Contains(errMsg, "already exists") {
			return nil, fmt.Errorf("%w: %s", ErrTagAlreadyExists, req.TagName)
		}
		return nil, fmt.Errorf("git tag: %s: %w", errMsg, err)
	}

	// Push tag to remote.
	askPassScript, err := createAskPassScript(token)
	if err != nil {
		return nil, fmt.Errorf("create askpass script: %w", err)
	}
	defer os.Remove(askPassScript)

	cmd := exec.CommandContext(ctx, "git", "push", "origin", req.TagName)
	cmd.Dir = clonePath
	cmd.Env = append(os.Environ(),
		"GIT_ASKPASS="+askPassScript,
		"GIT_TERMINAL_PROMPT=0",
	)

	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		errMsg := stderr.String()
		if strings.Contains(errMsg, "Authentication") {
			return nil, fmt.Errorf("%w: %s", ErrAuthFailed, errMsg)
		}
		return nil, fmt.Errorf("git push tag: %s", errMsg)
	}

	// Get commit hash
	hash, err := runGitOutput(ctx, clonePath, "rev-parse", "HEAD")
	if err != nil {
		return nil, fmt.Errorf("get commit hash: %w", err)
	}

	slog.InfoContext(ctx, "tag created and pushed",
		"project", projectID, "tag", req.TagName)

	return &TagResult{
		TagName:    req.TagName,
		CommitHash: strings.TrimSpace(hash),
	}, nil
}

func (s *gitOpsService) ListTags(ctx context.Context, projectID string, prefix string) ([]TagInfo, error) {
	mu := s.getRepoLock(projectID)
	mu.Lock()
	defer mu.Unlock()

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

	if err := s.ensureCloneReady(ctx, repoRecord); err != nil {
		return nil, fmt.Errorf("ensure clone: %w", err)
	}

	clonePath := repoRecord.ClonePath

	// Fetch tags from remote first to ensure we have the latest.
	token, _, err := s.resolveToken(ctx, repoRecord)
	if err != nil {
		return nil, err
	}
	askPassScript, err := createAskPassScript(token)
	if err != nil {
		return nil, fmt.Errorf("create askpass script: %w", err)
	}
	defer os.Remove(askPassScript)

	fetchCmd := exec.CommandContext(ctx, "git", "fetch", "--tags", "origin")
	fetchCmd.Dir = clonePath
	fetchCmd.Env = append(os.Environ(),
		"GIT_ASKPASS="+askPassScript,
		"GIT_TERMINAL_PROMPT=0",
	)
	// Ignore fetch errors — we still list local tags
	_ = fetchCmd.Run()

	// List tags matching prefix, sorted by version
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
		// Retrieve the annotated tag message
		msg, _ := runGitOutput(ctx, clonePath, "tag", "-l", name, "--format=%(contents)")
		tags = append(tags, TagInfo{
			Name:       name,
			CommitHash: strings.TrimSpace(hash),
			Message:    strings.TrimSpace(msg),
		})
	}

	return tags, nil
}

func (s *gitOpsService) GetFileAtTag(ctx context.Context, projectID string, tag string, filePath string) (string, error) {
	mu := s.getRepoLock(projectID)
	mu.Lock()
	defer mu.Unlock()

	repoRecord, err := s.repo.GetByProjectID(ctx, projectID)
	if err != nil {
		return "", fmt.Errorf("get repo: %w", err)
	}
	if repoRecord == nil {
		return "", ErrRepoNotFound
	}
	if repoRecord.Status != "ready" {
		return "", ErrRepoNotReady
	}

	if err := s.ensureCloneReady(ctx, repoRecord); err != nil {
		return "", fmt.Errorf("ensure clone: %w", err)
	}

	clonePath := repoRecord.ClonePath

	// git show <tag>:<filePath>
	ref := fmt.Sprintf("%s:%s", tag, filePath)
	content, err := runGitOutput(ctx, clonePath, "show", ref)
	if err != nil {
		errMsg := fmt.Sprintf("%v", err)
		if strings.Contains(errMsg, "not a valid object") || strings.Contains(errMsg, "does not exist") {
			return "", fmt.Errorf("%w: %s", ErrTagNotFound, tag)
		}
		if strings.Contains(errMsg, "does not exist in") || strings.Contains(errMsg, "path") {
			return "", fmt.Errorf("%w: %s at %s", ErrFileNotFound, filePath, tag)
		}
		return "", fmt.Errorf("git show %s: %w", ref, err)
	}

	return content, nil
}

func runGit(ctx context.Context, dir string, args ...string) error {
	_, err := runGitOutput(ctx, dir, args...)
	return err
}

// runGitOutput runs a git command and returns its stdout. On failure, stderr
// is included in the returned error so caller logs show git's own diagnostic.
func runGitOutput(ctx context.Context, dir string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			return "", err
		}
		return "", fmt.Errorf("%s: %w", msg, err)
	}
	return string(out), nil
}
