package services

// Save flow implementations for artifact-store v2 (V1 scope).
//
// Replaces the `git commit + push + tag` flow inside SaveDesign and
// SaveRequirements with GitHub API calls (Contents API + Git Data API +
// Refs/Tags API). The working-tree clone remains the source of truth for
// draft content in V1 — Postgres drafts are V2.
//
// See docs/design/artifact-store-v2.md §0 for the V1 scope and §8 for the
// save flow shape.

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/wso2/asdlc/asdlc-service/models"
	"github.com/wso2/asdlc/asdlc-service/internal/credentials"
)

// saveDesignViaAPI implements §8.2 (Git Data API path) for V1: the working
// tree under `specs/design/` is the draft surface (root `design.md` plus
// per-component `design.md` / `openapi.yaml`). We compute the changeset
// against local-clone HEAD, apply it over current main via blob+tree+commit,
// then create the next `v<N>-<M>` annotated tag.
//
// Mirrors saveRequirementsViaAPI's shape — the only differences are the
// directory walked, the precondition (a v<N> tag must exist), and the tag
// naming scheme.
func (s *artifactService) saveDesignViaAPI(
	ctx context.Context,
	repoRecord *models.GitRepository,
	clonePath string,
	commitMessage string,
) (*DesignSaveResult, error) {
	owner, repo := models.OwnerRepoFromURL(repoRecord.RepoURL)
	if owner == "" || repo == "" {
		return nil, fmt.Errorf("cannot derive owner/repo from RepoURL %q", repoRecord.RepoURL)
	}
	cred, err := s.gitOps.resolver.Resolve(ctx, repoRecord.OrgID)
	if err != nil {
		return nil, fmt.Errorf("resolve credential: %w", err)
	}

	// 1. Validate the root design document exists. An empty `specs/design/`
	// can't be saved — at minimum the top-level `design.md` must be present.
	rootAbs := filepath.Join(clonePath, DesignDir, designRootFile)
	if _, err := os.Stat(rootAbs); err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("%w: %s/%s missing — generate design before saving",
				ErrArtifactPathInvalid, DesignDir, designRootFile)
		}
		return nil, fmt.Errorf("stat %s: %w", rootAbs, err)
	}

	// 2. Compute the changeset: (working tree) vs (local HEAD)
	// status codes: A=added, M=modified, D=deleted, R=renamed (treated as D+A)
	changes, err := diffWorkingTreeAgainstHEAD(ctx, clonePath, DesignDir)
	if err != nil {
		return nil, fmt.Errorf("diff against HEAD: %w", err)
	}

	// 3. Tag list — drives the parent-N precondition, unchanged-detection,
	// and next-revision naming.
	tags, err := s.fetchGitHubTags(ctx, owner, repo, cred)
	if err != nil {
		return nil, fmt.Errorf("list tags: %w", err)
	}
	parentN := latestRequirementsVersion(tags)
	if parentN == 0 {
		return nil, ErrNoRequirementsBaseline
	}

	// 4. If working tree matches local HEAD exactly (no changes), report the
	// latest v<parentN>-<M> as "unchanged". If no design tag exists yet for
	// this parent N, fall through and tag main's current HEAD as v<N>-1.
	if len(changes) == 0 {
		if latestM := latestDesignRevision(tags, parentN); latestM > 0 {
			return &DesignSaveResult{
				Status:              "unchanged",
				Tag:                 designTagFor(parentN, latestM),
				RequirementsVersion: parentN,
				DesignRevision:      latestM,
			}, nil
		}
	}

	// 5. Resolve current main commit + tree.
	mainCommitSHA, err := s.gitOps.gitHub.GetRef(ctx, owner, repo, cred, "heads/"+repoRecord.DefaultBranch)
	if err != nil {
		return nil, fmt.Errorf("get ref main: %w", err)
	}
	mainCommit, err := s.gitOps.gitHub.GetCommit(ctx, owner, repo, cred, mainCommitSHA)
	if err != nil {
		return nil, fmt.Errorf("get commit %s: %w", mainCommitSHA, err)
	}

	// 6. Build tree entries from the changeset.
	//    Add/Modify → upload blob + entry with sha
	//    Delete     → entry with sha:null (skipped if path isn't on main — see §8.3 step 1)
	mainTree, err := s.gitOps.gitHub.GetTree(ctx, owner, repo, cred, mainCommit.TreeSHA, true)
	if err != nil {
		return nil, fmt.Errorf("get tree %s: %w", mainCommit.TreeSHA, err)
	}
	mainPaths := make(map[string]TreeEntryResult, len(mainTree.Entries))
	for _, e := range mainTree.Entries {
		mainPaths[e.Path] = e
	}

	var entries []TreeEntry
	for _, ch := range changes {
		repoPath := filepath.ToSlash(filepath.Join(DesignDir, ch.Name))
		switch ch.Status {
		case "A", "M", "T":
			data, rerr := os.ReadFile(filepath.Join(clonePath, DesignDir, ch.Name))
			if rerr != nil {
				return nil, fmt.Errorf("read working tree %s: %w", ch.Name, rerr)
			}
			blobSHA, berr := s.gitOps.gitHub.CreateBlob(ctx, owner, repo, cred, data)
			if berr != nil {
				return nil, fmt.Errorf("create blob %s: %w", ch.Name, berr)
			}
			entries = append(entries, TreeEntry{
				Path: repoPath,
				Mode: "100644",
				Type: "blob",
				SHA:  blobSHA,
			})
		case "D":
			// No-op tombstone filter (§8.3 step 1): skip deletes for paths
			// absent on main — GitHub would 422 otherwise.
			if _, ok := mainPaths[repoPath]; !ok {
				continue
			}
			entries = append(entries, TreeEntry{
				Path: repoPath,
				Mode: "100644",
				Type: "blob",
				// SHA empty → wire-serialised as `sha: null` by CreateTree.
			})
		default:
			// R (rename) — diffWorkingTreeAgainstHEAD expands these into D+A
			// upstream, so we should not see R here.
			return nil, fmt.Errorf("unexpected diff status %q for %s", ch.Status, ch.Name)
		}
	}

	if len(entries) == 0 {
		// All changes were no-op tombstones — same outcome as len(changes)==0.
		if latestM := latestDesignRevision(tags, parentN); latestM > 0 {
			return &DesignSaveResult{
				Status:              "unchanged",
				Tag:                 designTagFor(parentN, latestM),
				RequirementsVersion: parentN,
				DesignRevision:      latestM,
			}, nil
		}
		// No effective changes and no design tag yet — fall through and tag
		// main's current HEAD as v<parentN>-1.
	}

	// 7. CreateTree / CreateCommit / UpdateRef under CAS retry.
	bucketKey := repoRecord.OrgID + ":" + repoRecord.ProjectID
	author, committer := s.gitOps.resolveSaveIdentities(cred)

	var newCommitSHA string
	if len(entries) > 0 {
		err = retryOnCASConflict(ctx, bucketKey, func() error {
			// On retry, re-fetch ref/commit/tree so base_tree is fresh.
			freshMain, ferr := s.gitOps.gitHub.GetRef(ctx, owner, repo, cred, "heads/"+repoRecord.DefaultBranch)
			if ferr != nil {
				return fmt.Errorf("refresh main: %w", ferr)
			}
			freshCommit, ferr := s.gitOps.gitHub.GetCommit(ctx, owner, repo, cred, freshMain)
			if ferr != nil {
				return fmt.Errorf("refresh commit: %w", ferr)
			}
			mainCommitSHA = freshMain
			mainCommit = freshCommit
			treeSHA, terr := s.gitOps.gitHub.CreateTree(ctx, owner, repo, cred, freshCommit.TreeSHA, entries)
			if terr != nil {
				return fmt.Errorf("create tree: %w", terr)
			}
			commitSHA, cerr := s.gitOps.gitHub.CreateCommit(ctx, owner, repo, cred, CreateCommitRequest{
				Message:   commitMessage,
				TreeSHA:   treeSHA,
				Parents:   []string{freshMain},
				Author:    author,
				Committer: committer,
			})
			if cerr != nil {
				return fmt.Errorf("create commit: %w", cerr)
			}
			if uerr := s.gitOps.gitHub.UpdateRef(ctx, owner, repo, cred, "heads/"+repoRecord.DefaultBranch, commitSHA, false); uerr != nil {
				return uerr
			}
			newCommitSHA = commitSHA
			return nil
		})
		if err != nil {
			if errors.Is(err, ErrConflictBudgetExhausted) {
				return nil, fmt.Errorf("save design: %w", err)
			}
			return nil, fmt.Errorf("commit + update ref: %w", err)
		}
	} else {
		// First-ever design tag with nothing to commit — point at main.
		newCommitSHA = mainCommitSHA
	}

	// 8. Annotated tag with collision retry. The tag may move past parentN if
	// external tags were claimed concurrently.
	nextRev, tagName := nextDesignTag(tags, parentN)
	tagBody := fmt.Sprintf("Design v%d-%d", parentN, nextRev)
	if commitMessage != "" && commitMessage != "Update design" {
		tagBody = fmt.Sprintf("%s\n\n%s", tagBody, commitMessage)
	}
	if err := s.createAnnotatedTagViaAPI(ctx, owner, repo, cred, &tags, &nextRev, &tagName, tagBody, newCommitSHA, parentN, "design"); err != nil {
		return nil, fmt.Errorf("create tag: %w", err)
	}

	// 9. Best-effort sync local clone so subsequent reads see what we just
	// saved. Failures are logged but don't fail the save.
	if err := s.gitOps.bestEffortPullDefaultBranch(ctx, repoRecord); err != nil {
		slog.WarnContext(ctx, "best-effort pull after design save failed",
			"project", repoRecord.ProjectID, "error", err)
	}

	slog.InfoContext(ctx, "design saved + tagged via api",
		"project", repoRecord.ProjectID,
		"tag", tagName,
		"commit", newCommitSHA)

	return &DesignSaveResult{
		Status:              "approved",
		Tag:                 tagName,
		RequirementsVersion: parentN,
		DesignRevision:      nextRev,
		CommitHash:          newCommitSHA,
	}, nil
}

// saveRequirementsViaAPI implements §8.3 (Git Data API path) for V1. The
// working tree under `specs/requirements/` is the draft surface; we compute
// the delta against the local clone's HEAD (which reflects what we last
// saved), then apply that delta over current main via the Git Data API.
//
// The local-HEAD-vs-working-tree delta gives us tombstones for files the
// user deleted (so V1 preserves today's delete UX). Files on remote main
// that we never touched are carried forward by `base_tree=current main tree`.
func (s *artifactService) saveRequirementsViaAPI(
	ctx context.Context,
	repoRecord *models.GitRepository,
	clonePath string,
	commitMessage string,
) (*RequirementsSaveResult, error) {
	owner, repo := models.OwnerRepoFromURL(repoRecord.RepoURL)
	if owner == "" || repo == "" {
		return nil, fmt.Errorf("cannot derive owner/repo from RepoURL %q", repoRecord.RepoURL)
	}
	cred, err := s.gitOps.resolver.Resolve(ctx, repoRecord.OrgID)
	if err != nil {
		return nil, fmt.Errorf("resolve credential: %w", err)
	}

	// 1. Validate the main requirements file exists.
	mainAbs := filepath.Join(clonePath, RequirementsDir, requirementsMainFile)
	if _, err := os.Stat(mainAbs); err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("%w: %s/%s missing — populate requirements before saving",
				ErrArtifactPathInvalid, RequirementsDir, requirementsMainFile)
		}
		return nil, fmt.Errorf("stat %s: %w", mainAbs, err)
	}

	// 2. Compute the changeset: (working tree) vs (local HEAD)
	// status codes: A=added, M=modified, D=deleted, R=renamed (treated as D+A)
	changes, err := diffWorkingTreeAgainstHEAD(ctx, clonePath, RequirementsDir)
	if err != nil {
		return nil, fmt.Errorf("diff against HEAD: %w", err)
	}

	// 3. Tags first — we need them for unchanged-detection and tag naming.
	tags, err := s.fetchGitHubTags(ctx, owner, repo, cred)
	if err != nil {
		return nil, fmt.Errorf("list tags: %w", err)
	}

	// 4. If working tree matches local HEAD exactly (no changes), return the
	// latest v<N> tag as "unchanged" (or empty if none).
	if len(changes) == 0 {
		if latest := latestRequirementsTag(tags); latest != "" {
			return &RequirementsSaveResult{
				Status:  "unchanged",
				Tag:     latest,
				Version: latestRequirementsVersion(tags),
			}, nil
		}
		// No changes AND no tag — surface as a "create the first tag" save below.
	}

	// 5. Resolve current main commit + tree.
	mainCommitSHA, err := s.gitOps.gitHub.GetRef(ctx, owner, repo, cred, "heads/"+repoRecord.DefaultBranch)
	if err != nil {
		return nil, fmt.Errorf("get ref main: %w", err)
	}
	mainCommit, err := s.gitOps.gitHub.GetCommit(ctx, owner, repo, cred, mainCommitSHA)
	if err != nil {
		return nil, fmt.Errorf("get commit %s: %w", mainCommitSHA, err)
	}

	// 6. Build tree entries from the changeset.
	//    Add/Modify → upload blob + entry with sha
	//    Delete     → entry with sha:null (skipped if path isn't on main — see §8.3 step 1)
	mainTree, err := s.gitOps.gitHub.GetTree(ctx, owner, repo, cred, mainCommit.TreeSHA, true)
	if err != nil {
		return nil, fmt.Errorf("get tree %s: %w", mainCommit.TreeSHA, err)
	}
	mainPaths := make(map[string]TreeEntryResult, len(mainTree.Entries))
	for _, e := range mainTree.Entries {
		mainPaths[e.Path] = e
	}

	var entries []TreeEntry
	for _, ch := range changes {
		repoPath := filepath.ToSlash(filepath.Join(RequirementsDir, ch.Name))
		switch ch.Status {
		case "A", "M", "T":
			data, rerr := os.ReadFile(filepath.Join(clonePath, RequirementsDir, ch.Name))
			if rerr != nil {
				return nil, fmt.Errorf("read working tree %s: %w", ch.Name, rerr)
			}
			blobSHA, berr := s.gitOps.gitHub.CreateBlob(ctx, owner, repo, cred, data)
			if berr != nil {
				return nil, fmt.Errorf("create blob %s: %w", ch.Name, berr)
			}
			entries = append(entries, TreeEntry{
				Path: repoPath,
				Mode: "100644",
				Type: "blob",
				SHA:  blobSHA,
			})
		case "D":
			// No-op tombstone filter (§8.3 step 1): skip deletes for paths
			// absent on main — GitHub would 422 otherwise.
			if _, ok := mainPaths[repoPath]; !ok {
				continue
			}
			entries = append(entries, TreeEntry{
				Path: repoPath,
				Mode: "100644",
				Type: "blob",
				// SHA empty → wire-serialised as `sha: null` by CreateTree.
			})
		default:
			// R (rename) — treat as D for old + A for new.
			// `diffWorkingTreeAgainstHEAD` expands R rows into a D + A pair
			// upstream, so we should not see R here.
			return nil, fmt.Errorf("unexpected diff status %q for %s", ch.Status, ch.Name)
		}
	}

	if len(entries) == 0 {
		// All changes were no-op tombstones (deleting files not on main).
		// Same outcome as len(changes)==0 — return latest tag as unchanged.
		if latest := latestRequirementsTag(tags); latest != "" {
			return &RequirementsSaveResult{
				Status:  "unchanged",
				Tag:     latest,
				Version: latestRequirementsVersion(tags),
			}, nil
		}
		// Truly empty save with no tags — fall through and tag main as v1.
	}

	// 7. CreateTree / CreateCommit / UpdateRef under CAS retry.
	bucketKey := repoRecord.OrgID + ":" + repoRecord.ProjectID
	author, committer := s.gitOps.resolveSaveIdentities(cred)

	var newCommitSHA string
	if len(entries) > 0 {
		err = retryOnCASConflict(ctx, bucketKey, func() error {
			// On retry, re-fetch ref/commit/tree so base_tree is fresh.
			freshMain, ferr := s.gitOps.gitHub.GetRef(ctx, owner, repo, cred, "heads/"+repoRecord.DefaultBranch)
			if ferr != nil {
				return fmt.Errorf("refresh main: %w", ferr)
			}
			freshCommit, ferr := s.gitOps.gitHub.GetCommit(ctx, owner, repo, cred, freshMain)
			if ferr != nil {
				return fmt.Errorf("refresh commit: %w", ferr)
			}
			mainCommitSHA = freshMain
			mainCommit = freshCommit
			treeSHA, terr := s.gitOps.gitHub.CreateTree(ctx, owner, repo, cred, freshCommit.TreeSHA, entries)
			if terr != nil {
				return fmt.Errorf("create tree: %w", terr)
			}
			commitMsg := commitMessage
			if commitMsg == "" {
				commitMsg = "Update requirements"
			}
			commitSHA, cerr := s.gitOps.gitHub.CreateCommit(ctx, owner, repo, cred, CreateCommitRequest{
				Message:   commitMsg,
				TreeSHA:   treeSHA,
				Parents:   []string{freshMain},
				Author:    author,
				Committer: committer,
			})
			if cerr != nil {
				return fmt.Errorf("create commit: %w", cerr)
			}
			if uerr := s.gitOps.gitHub.UpdateRef(ctx, owner, repo, cred, "heads/"+repoRecord.DefaultBranch, commitSHA, false); uerr != nil {
				return uerr
			}
			newCommitSHA = commitSHA
			return nil
		})
		if err != nil {
			if errors.Is(err, ErrConflictBudgetExhausted) {
				return nil, fmt.Errorf("save requirements: %w", err)
			}
			return nil, fmt.Errorf("commit + update ref: %w", err)
		}
	} else {
		// First-ever tag on a fresh repo with no `specs/requirements/` changes.
		newCommitSHA = mainCommitSHA
	}

	// 8. Annotated tag with collision retry.
	nextN, tagName := nextRequirementsTag(tags)
	tagBody := fmt.Sprintf("Requirements v%d", nextN)
	if commitMessage != "" && commitMessage != "Update requirements" {
		tagBody = fmt.Sprintf("%s\n\n%s", tagBody, commitMessage)
	}
	if err := s.createAnnotatedTagViaAPI(ctx, owner, repo, cred, &tags, &nextN, &tagName, tagBody, newCommitSHA, 0, "requirements"); err != nil {
		return nil, fmt.Errorf("create tag: %w", err)
	}

	// 9. Best-effort sync local clone so subsequent reads see what we just
	// saved. Failures are logged but don't fail the save (the user's bytes
	// are on remote already).
	if err := s.gitOps.bestEffortPullDefaultBranch(ctx, repoRecord); err != nil {
		slog.WarnContext(ctx, "post-save pull failed (continuing)",
			"project", repoRecord.ProjectID, "error", err)
	}

	slog.InfoContext(ctx, "requirements saved + tagged via api",
		"project", repoRecord.ProjectID,
		"tag", tagName,
		"commit", newCommitSHA,
		"entries", len(entries))

	return &RequirementsSaveResult{
		Status:     "approved",
		Tag:        tagName,
		Version:    nextN,
		CommitHash: newCommitSHA,
	}, nil
}

// ----- Helpers -----

// fetchGitHubTags resolves the project's tags via ListMatchingRefs and
// translates them into the local TagInfo shape so the existing
// `latestRequirementsVersion` / `nextDesignTag` helpers (artifact_versioning.go)
// can consume them.
func (s *artifactService) fetchGitHubTags(ctx context.Context, owner, repo string, cred credentials.Credential) ([]TagInfo, error) {
	refs, err := s.gitOps.gitHub.ListMatchingRefs(ctx, owner, repo, cred, "tags/v")
	if err != nil {
		return nil, err
	}
	out := make([]TagInfo, 0, len(refs))
	for _, r := range refs {
		// "refs/tags/v1" -> "v1"
		name := strings.TrimPrefix(r.Ref, "refs/tags/")
		if name == "" || name == r.Ref {
			continue
		}
		out = append(out, TagInfo{
			Name:       name,
			CommitHash: r.SHA,
			// Message is not surfaced by ListMatchingRefs; left empty.
			// Existing version helpers only use Name (and Tag/CommitHash for output).
		})
	}
	return out, nil
}

// blobSHAOnMain returns the blob SHA of `path` on main, or "" if the file
// doesn't exist there (404). Other errors are returned wrapped.
func blobSHAOnMain(ctx context.Context, gh GitHubClient, owner, repo string, cred credentials.Credential, path string) (string, error) {
	res, err := gh.GetContents(ctx, owner, repo, cred, path, "main")
	if err != nil {
		var httpErr *HTTPStatusError
		if errors.As(err, &httpErr) && httpErr.StatusCode == 404 {
			return "", nil
		}
		return "", err
	}
	return res.BlobSHA, nil
}

// createAnnotatedTagViaAPI creates an annotated tag via the two-step
// (POST /git/tags → POST /git/refs) API. On a tag-collision 422 it refreshes
// the tag list and recomputes the next tag name in-place, then retries.
//
// `kind` is "design" or "requirements" — selects nextDesignTag vs
// nextRequirementsTag. For design, `parentN` is the parent requirements
// version; for requirements, `parentN` is ignored.
func (s *artifactService) createAnnotatedTagViaAPI(
	ctx context.Context,
	owner, repo string,
	cred credentials.Credential,
	tags *[]TagInfo,
	nextN *int,
	tagName *string,
	tagBody, commitSHA string,
	parentN int,
	kind string,
) error {
	author, _ := s.gitOps.resolveSaveIdentities(cred)
	return retryOnTagCollision(ctx, func() error {
		// Recompute the target name on each attempt so collisions push us forward.
		switch kind {
		case "design":
			refreshed, ferr := s.fetchGitHubTags(ctx, owner, repo, cred)
			if ferr == nil {
				*tags = refreshed
			}
			rev, name := nextDesignTag(*tags, parentN)
			*nextN, *tagName = rev, name
		case "requirements":
			refreshed, ferr := s.fetchGitHubTags(ctx, owner, repo, cred)
			if ferr == nil {
				*tags = refreshed
			}
			ver, name := nextRequirementsTag(*tags)
			*nextN, *tagName = ver, name
		}
		tagObjSHA, err := s.gitOps.gitHub.CreateTagObject(ctx, owner, repo, cred, CreateTagObjectRequest{
			Tag:     *tagName,
			Message: tagBody,
			Object:  commitSHA,
			Type:    "commit",
			Tagger:  author,
		})
		if err != nil {
			return fmt.Errorf("create tag object: %w", err)
		}
		if err := s.gitOps.gitHub.CreateTagRef(ctx, owner, repo, cred, *tagName, tagObjSHA); err != nil {
			return err // may be wrapped ErrTagAlreadyExists — retried
		}
		return nil
	})
}

// resolveSaveIdentities returns (author, committer) identities for the save.
//
// V1: committer = author = credential identity (same as today's CLI flow).
// V2 will split these per §11 (committer=bot, author=OC user).
func (s *gitOpsService) resolveSaveIdentities(cred credentials.Credential) (*GitIdentity, *GitIdentity) {
	id := cred.Identity()
	if id.Name == "" {
		id.Name = "ASDLC"
	}
	if id.Email == "" {
		id.Email = "noreply@asdlc.dev"
	}
	gi := &GitIdentity{Name: id.Name, Email: id.Email}
	return gi, gi
}

// bestEffortPullDefaultBranch advances the local clone's HEAD to remote main
// so the next save's `git status` against HEAD reflects current remote
// state. The working-tree files are LEFT ALONE — they're the user's draft
// surface in V1 and we just wrote them on remote as the save's commit. We
// also refresh the index so `git status` doesn't report every working-tree
// file as "modified vs new HEAD."
//
// Implementation note: we use `update-ref` + `read-tree` rather than
// `merge --ff-only` because the working tree contains untracked or
// otherwise-modified files that merge would refuse to overwrite.
func (s *gitOpsService) bestEffortPullDefaultBranch(ctx context.Context, repoRecord *models.GitRepository) error {
	authedEnv, _, cleanup, err := s.prepareAuthedEnv(ctx, repoRecord)
	if err != nil {
		return err
	}
	defer cleanup()
	clonePath := repoRecord.ClonePath
	branch := repoRecord.DefaultBranch

	// 1. Fetch the remote ref.
	cmd := exec.CommandContext(ctx, "git", "fetch", "origin", branch)
	cmd.Dir = clonePath
	cmd.Env = authedEnv
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("git fetch: %s", stderr.String())
	}

	// 2. Read the new remote SHA.
	originSHA, err := runGitOutput(ctx, clonePath, "rev-parse", "origin/"+branch)
	if err != nil {
		return fmt.Errorf("rev-parse origin/%s: %w", branch, err)
	}
	originSHA = strings.TrimSpace(originSHA)

	// 3. Move local branch ref to origin's tip without touching working tree.
	cmd = exec.CommandContext(ctx, "git", "update-ref", "refs/heads/"+branch, originSHA)
	cmd.Dir = clonePath
	stderr.Reset()
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("git update-ref refs/heads/%s: %s", branch, stderr.String())
	}

	// 4. Refresh the index to the new HEAD so `git status` against the
	// working tree shows only the user's local drafts as modified, not
	// every file that was renamed by the move.
	cmd = exec.CommandContext(ctx, "git", "read-tree", "HEAD")
	cmd.Dir = clonePath
	stderr.Reset()
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("git read-tree HEAD: %s", stderr.String())
	}
	return nil
}

// changeRow is one row of `git diff --name-status HEAD -- <dir>`.
type changeRow struct {
	Status string // A, M, D, T, R<num>
	Name   string // basename (filename within RequirementsDir)
}

// diffWorkingTreeAgainstHEAD lists the working-tree changes under `subdir`
// relative to the clone's local HEAD. Uses `git status --porcelain` which
// covers tracked-and-modified, tracked-and-deleted, and untracked files in
// one call. Renames are expanded into a delete + add pair so callers don't
// have to handle the rename status.
//
// status mapping (from `git status --porcelain -z`):
//   - "?? path"   → untracked → A
//   - " M path"   → modified (working tree) → M
//   - "M  path"   → modified (staged) → M
//   - " D path"   → deleted (working tree) → D
//   - "D  path"   → deleted (staged) → D
//   - "A  path"   → added (staged) → A
//   - "R  old\0new" → renamed (staged) → D(old) + A(new)
//
// Each row from the porcelain output is two chars of status + space + path,
// terminated by a NUL byte. Rename entries carry an additional NUL-separated
// "from" path AFTER the "to" path.
func diffWorkingTreeAgainstHEAD(ctx context.Context, clonePath, subdir string) ([]changeRow, error) {
	cmd := exec.CommandContext(ctx, "git", "status", "--porcelain=v1", "-z", "--untracked-files=all", "--", subdir)
	cmd.Dir = clonePath
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("git status: %s: %w", stderr.String(), err)
	}

	// Paths from `git status` are repo-root-relative (e.g.
	// `specs/design/components/foo/design.md`). Strip the `subdir/` prefix
	// so `Name` carries the path *within* subdir — including any directory
	// nesting — rather than just the basename. `filepath.Base` here would
	// silently drop `components/foo/` and collapse every nested file onto
	// its leaf name, which breaks the multi-file design layout.
	subdirPrefix := strings.TrimRight(subdir, "/") + "/"
	relName := func(p string) string { return strings.TrimPrefix(p, subdirPrefix) }

	out := stdout.Bytes()
	var rows []changeRow
	for len(out) > 0 {
		// Each entry: <XY> <space> <path> <NUL>
		// XY = two-char status field.
		if len(out) < 3 {
			break
		}
		x := out[0] // index/staged status
		y := out[1] // working-tree status
		// out[2] is a space separator
		out = out[3:]
		end := bytes.IndexByte(out, 0)
		if end < 0 {
			break
		}
		path := string(out[:end])
		out = out[end+1:]

		// Renames carry an additional path AFTER the primary path.
		var fromPath string
		if x == 'R' || y == 'R' {
			end := bytes.IndexByte(out, 0)
			if end < 0 {
				break
			}
			fromPath = string(out[:end])
			out = out[end+1:]
		}

		// Translate XY to our single-letter status.
		var s string
		switch {
		case x == '?' && y == '?':
			s = "A" // untracked → add
		case x == 'D' || y == 'D':
			s = "D"
		case x == 'A' && y != 'D':
			s = "A"
		case x == 'M' || y == 'M' || x == 'T' || y == 'T':
			s = "M"
		case x == 'R' || y == 'R':
			// Emit D(from) + A(to) and continue.
			rows = append(rows,
				changeRow{Status: "D", Name: relName(fromPath)},
				changeRow{Status: "A", Name: relName(path)},
			)
			continue
		default:
			// Unknown / ignored status (e.g. "!!").
			continue
		}
		rows = append(rows, changeRow{Status: s, Name: relName(path)})
	}
	return rows, nil
}

// gitHub is the GitHubClient pointer wired into gitOpsService at
// construction. Resolved here as a convenience accessor — the field name
// resolves to the artifact-store v2 (V1) injected client.
func (s *gitOpsService) GitHubClient() GitHubClient { return s.gitHub }
