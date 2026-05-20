package services

import (
	"context"
	"fmt"

	"github.com/wso2/asdlc/git-service/repositories"
)

// BranchService creates and manages feature branches on platform-managed
// repos via the GitHub Refs API. Branches are an attribute of a project
// (i.e. a repo) — caller-side ocOrgID enforcement happens via the resolver
// when the credential is fetched.
type BranchService interface {
	// CreateBranch creates a new branch from `fromRef` (a branch name; resolved
	// to its tip SHA at call time). Idempotent on the branch name: returns the
	// existing tip SHA if the ref already exists.
	CreateBranch(ctx context.Context, projectID, branch, fromRef string) (sha string, err error)
	// SeedBranchCommit creates or updates a single file on the given branch
	// to seed at least one commit on it. GitHub's PR API rejects PRs whose
	// head and base are at the same SHA, so a freshly-created task branch
	// needs a placeholder commit before a draft PR can be opened. Idempotent
	// on (path, content).
	SeedBranchCommit(ctx context.Context, projectID, branch, path, message string, content []byte) error
}

type branchService struct {
	repo   repositories.RepoRepository
	github GitHubClient
	// resolver is reused via the issueService.resolveRepoAndCredential helper —
	// we duplicate the resolution logic here to keep services orthogonal.
	issue *issueService
}

// NewBranchService wires a branch service against the existing GitHub client.
// The issueService dependency is the credential-resolution helper; we accept
// the concrete type to share resolveRepoAndCredential without exposing it on
// the public IssueService interface.
func NewBranchService(repo repositories.RepoRepository, github GitHubClient, issueSvc IssueService) BranchService {
	is, _ := issueSvc.(*issueService)
	return &branchService{repo: repo, github: github, issue: is}
}

func (s *branchService) CreateBranch(ctx context.Context, projectID, branch, fromRef string) (string, error) {
	if branch == "" {
		return "", fmt.Errorf("branch name is required")
	}
	if fromRef == "" {
		fromRef = "" // caller may pass empty to default to repo's default branch
	}

	owner, repoName, cred, err := s.issue.resolveRepoAndCredential(ctx, projectID)
	if err != nil {
		return "", err
	}

	// Resolve fromRef to a SHA. Empty means "use default branch".
	if fromRef == "" {
		gitRepo, gerr := s.repo.GetByProjectID(ctx, projectID)
		if gerr != nil {
			return "", fmt.Errorf("get repo: %w", gerr)
		}
		fromRef = gitRepo.DefaultBranch
		if fromRef == "" {
			fromRef = "main"
		}
	}
	sha, err := s.github.GetBranchSHA(ctx, owner, repoName, cred, fromRef)
	if err != nil {
		return "", fmt.Errorf("resolve fromRef %q: %w", fromRef, err)
	}

	return s.github.CreateBranch(ctx, owner, repoName, cred, branch, sha)
}

func (s *branchService) SeedBranchCommit(ctx context.Context, projectID, branch, path, message string, content []byte) error {
	if branch == "" || path == "" {
		return fmt.Errorf("branch and path are required")
	}
	owner, repoName, cred, err := s.issue.resolveRepoAndCredential(ctx, projectID)
	if err != nil {
		return err
	}
	return s.github.PutFileOnBranch(ctx, owner, repoName, cred, branch, path, message, content)
}
