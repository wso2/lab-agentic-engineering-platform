package services

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	"github.com/wso2/asdlc/git-service/models"
	"github.com/wso2/asdlc/git-service/pkg/credentials"
	"github.com/wso2/asdlc/git-service/repositories"
)

// IssueService creates and lists GitHub issues on project repositories.
type IssueService interface {
	CreateIssue(ctx context.Context, projectID string, req CreateIssueRequest) (*IssueResult, error)
	ListIssues(ctx context.Context, projectID string, labels []string) ([]IssueInfo, error)
	// CloseIssue closes the issue, optionally posting a closing comment first.
	CloseIssue(ctx context.Context, projectID string, number int, comment string) error
	// CommentIssue posts a comment on the issue.
	CommentIssue(ctx context.Context, projectID string, number int, body string) error
	// EditIssueBody replaces the issue's body. Used by the tech-lead detail
	// phase to write the LLM-authored body after the placeholder issue was
	// created.
	EditIssueBody(ctx context.Context, projectID string, number int, body string) error
}

type issueService struct {
	repo     repositories.RepoRepository
	github   GitHubClient
	githubV2 GitHubV2Client
	resolver credentials.Resolver
	pat      string
}

func NewIssueService(repo repositories.RepoRepository, github GitHubClient, githubV2 GitHubV2Client, resolver credentials.Resolver, pat string) IssueService {
	return &issueService{
		repo:     repo,
		github:   github,
		githubV2: githubV2,
		resolver: resolver,
		pat:      pat,
	}
}

func (s *issueService) CreateIssue(ctx context.Context, projectID string, req CreateIssueRequest) (*IssueResult, error) {
	if strings.TrimSpace(req.Title) == "" {
		return nil, fmt.Errorf("title is required")
	}

	owner, repoName, cred, err := s.resolveRepoAndCredential(ctx, projectID)
	if err != nil {
		return nil, err
	}

	// Ensure all requested labels exist in the repo before creating the issue.
	// GitHub silently drops labels that don't exist, so we create them up-front.
	for _, label := range req.Labels {
		color := labelColor(label)
		if ensureErr := s.github.EnsureLabel(ctx, owner, repoName, cred, label, color); ensureErr != nil {
			// Non-fatal: log and continue; the issue will be created without the missing label.
			slog.WarnContext(ctx, "ensure github label failed", "label", label, "error", ensureErr)
		}
	}

	issue, err := s.github.CreateIssue(ctx, owner, repoName, cred, req)
	if err != nil {
		return nil, err
	}

	gitRepo, repoErr := s.repo.GetByProjectID(ctx, projectID)
	if repoErr == nil && gitRepo != nil {
		if gitRepo.GithubProjectID == "" {
			if boardID, err := s.ensureBoard(ctx, gitRepo, owner, repoName); err == nil {
				gitRepo.GithubProjectID = boardID
				if updateErr := s.repo.Update(ctx, gitRepo); updateErr != nil {
					slog.WarnContext(ctx, "failed to persist github project id after lazy creation", "project", projectID, "error", updateErr)
				}
			}
		}
		s.addIssueToProject(ctx, gitRepo.GithubProjectID, issue)
	}

	return issue, nil
}

func (s *issueService) ensureBoard(ctx context.Context, gitRepo *models.GitRepository, owner, repoName string) (string, error) {
	orgNodeID, err := s.githubV2.GetOrgID(ctx, owner, s.pat)
	if err != nil {
		return "", fmt.Errorf("resolve org id during lazy board create: %w", err)
	}

	githubProjectID, err := s.githubV2.CreateGitHubV2Project(ctx, orgNodeID, s.pat, gitRepo.ProjectID)
	if err != nil {
		return "", fmt.Errorf("create github project board: %w", err)
	}

	if linkErr := s.githubV2.LinkProjectToRepository(ctx, githubProjectID, owner, repoName, s.pat); linkErr != nil {
		slog.WarnContext(ctx, "failed to link lazy-created board to repository", "project", gitRepo.ProjectID, "error", linkErr)
	}

	slog.InfoContext(ctx, "lazy-created github project board", "project", gitRepo.ProjectID, "boardId", githubProjectID)
	return githubProjectID, nil
}

func (s *issueService) addIssueToProject(ctx context.Context, githubProjectID string, issue *IssueResult) {
	if issue.NodeID == "" || s.githubV2 == nil || githubProjectID == "" {
		slog.WarnContext(ctx, "skipping board add: missing project id or issue node id", "issue", issue.URL)
		return
	}
	if err := s.githubV2.AddIssueToProject(ctx, githubProjectID, issue.NodeID, s.pat); err != nil {
		slog.WarnContext(ctx, "failed to add issue to GitHub project board", "issue", issue.URL, "error", err)
	}
}

func (s *issueService) ListIssues(ctx context.Context, projectID string, labels []string) ([]IssueInfo, error) {
	owner, repoName, cred, err := s.resolveRepoAndCredential(ctx, projectID)
	if err != nil {
		return nil, err
	}
	return s.github.ListIssues(ctx, owner, repoName, cred, labels)
}

func (s *issueService) CloseIssue(ctx context.Context, projectID string, number int, comment string) error {
	owner, repoName, cred, err := s.resolveRepoAndCredential(ctx, projectID)
	if err != nil {
		return err
	}

	// Post the closing comment first (best-effort: log and continue on failure).
	if strings.TrimSpace(comment) != "" {
		if commentErr := s.github.CommentIssue(ctx, owner, repoName, cred, number, comment); commentErr != nil {
			slog.WarnContext(ctx, "failed to post closing comment", "project", projectID, "issue", number, "error", commentErr)
		}
	}

	return s.github.CloseIssue(ctx, owner, repoName, cred, number)
}

func (s *issueService) CommentIssue(ctx context.Context, projectID string, number int, body string) error {
	if strings.TrimSpace(body) == "" {
		return fmt.Errorf("comment body is required")
	}

	owner, repoName, cred, err := s.resolveRepoAndCredential(ctx, projectID)
	if err != nil {
		return err
	}

	return s.github.CommentIssue(ctx, owner, repoName, cred, number, body)
}

func (s *issueService) EditIssueBody(ctx context.Context, projectID string, number int, body string) error {
	if strings.TrimSpace(body) == "" {
		return fmt.Errorf("body is required")
	}
	owner, repoName, cred, err := s.resolveRepoAndCredential(ctx, projectID)
	if err != nil {
		return err
	}
	return s.github.EditIssueBody(ctx, owner, repoName, cred, number, body)
}

// resolveRepoAndCredential looks up the project's git repository, parses its
// owner/repo from the clone URL, and resolves the org's credential. Every
// GitHub-bound op routes through here — the multi-tenant invariant
// (operations parametrised by ocOrgID) is enforced at one place.
func (s *issueService) resolveRepoAndCredential(ctx context.Context, projectID string) (owner, repo string, cred credentials.Credential, err error) {
	gitRepo, err := s.repo.GetByProjectID(ctx, projectID)
	if err != nil {
		return "", "", nil, fmt.Errorf("get repo: %w", err)
	}
	if gitRepo == nil {
		return "", "", nil, ErrRepoNotFound
	}

	owner, repo, err = ParseOwnerRepo(gitRepo.RepoURL)
	if err != nil {
		return "", "", nil, fmt.Errorf("parse repo url %q: %w", gitRepo.RepoURL, err)
	}

	cred, err = s.resolver.Resolve(ctx, gitRepo.OrgID)
	if err != nil {
		return "", "", nil, fmt.Errorf("resolve credential for org %q: %w", gitRepo.OrgID, err)
	}
	return owner, repo, cred, nil
}

// ParseOwnerRepo extracts the "owner" and "repo" segments from a GitHub clone URL.
// Supports https://github.com/owner/repo.git and https://github.com/owner/repo forms.
func ParseOwnerRepo(cloneURL string) (owner, repo string, err error) {
	u := strings.TrimSpace(cloneURL)
	for _, prefix := range []string{"https://github.com/", "http://github.com/", "git@github.com:"} {
		if strings.HasPrefix(u, prefix) {
			u = strings.TrimPrefix(u, prefix)
			break
		}
	}
	u = strings.TrimSuffix(u, ".git")
	u = strings.Trim(u, "/")

	parts := strings.SplitN(u, "/", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", "", fmt.Errorf("not a github repo url")
	}
	return parts[0], parts[1], nil
}

// labelColor returns a hex color (without #) for well-known ASDLC labels,
// falling back to a neutral grey for anything else (e.g. phase-N labels).
func labelColor(name string) string {
	switch name {
	case "asdlc":
		return "0075ca" // blue
	case "implementation":
		return "7057ff" // purple
	case "pending":
		return "e4e669" // yellow
	default:
		return "ededed" // light grey for phase-N and other dynamic labels
	}
}
