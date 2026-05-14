package services

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/wso2/asdlc/git-service/models"
	"github.com/wso2/asdlc/git-service/pkg/credentials"
	"github.com/wso2/asdlc/git-service/repositories"
)

// RepoService manages git repository lifecycle (create, get, delete).
type RepoService interface {
	CreateRepo(ctx context.Context, orgID, projectID, projectName string) (*models.GitRepository, error)
	GetRepo(ctx context.Context, projectID string) (*models.GitRepository, error)
	// SetWebhookID is called by the webhook registration service after a hook
	// is provisioned for the repo on GitHub. Stored alongside the repo record
	// so cleanup can deregister.
	SetWebhookID(ctx context.Context, projectID string, hookID int64) error
	DeleteRepo(ctx context.Context, projectID string) error
	DeleteAll(ctx context.Context) error
	SetGithubProjectID(ctx context.Context, projectID, githubProjectID string) error
}

type repoService struct {
	repo         repositories.RepoRepository
	github       GitHubClient
	resolver     credentials.Resolver
	repoVis      string
	repoBasePath string
}

func NewRepoService(
	repo repositories.RepoRepository,
	github GitHubClient,
	resolver credentials.Resolver,
	repoVisibility, repoBasePath string,
) RepoService {
	return &repoService{
		repo:         repo,
		github:       github,
		resolver:     resolver,
		repoVis:      repoVisibility,
		repoBasePath: repoBasePath,
	}
}

func (s *repoService) CreateRepo(ctx context.Context, orgID, projectID, projectName string) (*models.GitRepository, error) {
	slog.InfoContext(ctx, "creating repository", "org", orgID, "project", projectID, "name", projectName)
	if orgID == "" {
		return nil, fmt.Errorf("orgID is required")
	}

	// Idempotent on (ocOrgId, project): a repeat-create returns the existing
	// row instead of erroring. Repo provisioning is the entry-point for many
	// flows (project creation, retry, drift fix), all of which should be safe
	// to retry. See evolution-doc §7.1 and phase0 §1.11.
	existing, err := s.repo.GetByProjectID(ctx, projectID)
	if err != nil {
		return nil, fmt.Errorf("check existing repo: %w", err)
	}
	if existing != nil {
		slog.InfoContext(ctx, "repo already provisioned for project; returning existing row",
			"projectId", projectID, "orgId", orgID)
		return existing, nil
	}

	cred, err := s.resolver.Resolve(ctx, orgID)
	if err != nil {
		return nil, fmt.Errorf("resolve credential for org %q: %w", orgID, err)
	}

	slug := slugifyProjectName(projectName)
	description := fmt.Sprintf("ASDLC project %s", projectName)

	repoName, cloneURL, err := s.createGitHubRepoWithRetry(ctx, cred, slug, description)
	if err != nil {
		return nil, fmt.Errorf("create github repo: %w", err)
	}

	clonePath := filepath.Join(s.repoBasePath, orgID, projectID)

	// Compute the per-repo slug from the GitHub clone URL — used by
	// StageBuildSecret to validate (ocOrgId, repoSlug) ownership. The
	// build credential itself is now pre-staged per WorkflowRun directly
	// as a K8s Secret in workflows-<ocOrgID> (see
	// docs/design/build-credential-injection.md), so no SecretReference
	// name is computed here; OcSecretRefName is left nil on new rows.
	repoSlug := models.SlugForURL(cloneURL)

	gitRepo := &models.GitRepository{
		OrgID:     orgID,
		ProjectID: projectID,
		RepoURL:   cloneURL,
		ClonePath: clonePath,
		Status:    "cloning",
		RepoSlug:  repoSlug,
	}

	if err := s.repo.Create(ctx, gitRepo); err != nil {
		return nil, fmt.Errorf("create repo record: %w", err)
	}

	slog.InfoContext(ctx, "created platform repo",
		"owner", cred.RepoOwner(), "name", repoName, "project", projectID, "org", orgID)

	go s.performClone(orgID, projectID, cloneURL, clonePath)

	return gitRepo, nil
}

// createGitHubRepoWithRetry rolls a fresh 3-digit suffix per attempt. Up to 5 retries on name conflict.
func (s *repoService) createGitHubRepoWithRetry(ctx context.Context, cred credentials.Credential, slug, description string) (repoName, cloneURL string, err error) {
	for attempt := 1; attempt <= 5; attempt++ {
		name := fmt.Sprintf("%s%03d", slug, rand.IntN(1000))
		cloneURL, err = s.github.CreateOrgRepo(ctx, cred, CreateOrgRepoRequest{
			Name:        name,
			Private:     strings.EqualFold(s.repoVis, "private"),
			AutoInit:    true,
			Description: description,
		})
		if err == nil {
			return name, cloneURL, nil
		}
		if !IsRepoNameConflict(err) {
			return "", "", err
		}
	}
	return "", "", fmt.Errorf("repo name for %q unavailable after 5 attempts: %w", slug, err)
}

func (s *repoService) GetRepo(ctx context.Context, projectID string) (*models.GitRepository, error) {
	repo, err := s.repo.GetByProjectID(ctx, projectID)
	if err != nil {
		return nil, fmt.Errorf("get repo: %w", err)
	}
	if repo == nil {
		return nil, ErrRepoNotFound
	}
	return repo, nil
}

func (s *repoService) SetWebhookID(ctx context.Context, projectID string, hookID int64) error {
	repo, err := s.repo.GetByProjectID(ctx, projectID)
	if err != nil {
		return fmt.Errorf("get repo: %w", err)
	}
	if repo == nil {
		return ErrRepoNotFound
	}
	id := hookID
	repo.WebhookID = &id
	return s.repo.Update(ctx, repo)
}

func (s *repoService) DeleteRepo(ctx context.Context, projectID string) error {
	repo, err := s.repo.GetByProjectID(ctx, projectID)
	if err != nil {
		return fmt.Errorf("get repo: %w", err)
	}
	if repo == nil {
		return ErrRepoNotFound
	}

	if repo.ClonePath != "" {
		if err := os.RemoveAll(repo.ClonePath); err != nil {
			slog.ErrorContext(ctx, "failed to remove clone directory", "path", repo.ClonePath, "error", err)
		}
	}

	if err := s.repo.Delete(ctx, projectID); err != nil {
		return fmt.Errorf("delete repo record: %w", err)
	}
	return nil
}

func (s *repoService) SetGithubProjectID(ctx context.Context, projectID, githubProjectID string) error {
	repo, err := s.repo.GetByProjectID(ctx, projectID)
	if err != nil {
		return fmt.Errorf("get repo: %w", err)
	}
	if repo == nil {
		return ErrRepoNotFound
	}
	repo.GithubProjectID = githubProjectID
	return s.repo.Update(ctx, repo)
}

func (s *repoService) DeleteAll(ctx context.Context) error {
	if err := os.RemoveAll(s.repoBasePath); err != nil {
		slog.ErrorContext(ctx, "failed to remove repo base path", "path", s.repoBasePath, "error", err)
	}
	if err := os.MkdirAll(s.repoBasePath, 0o755); err != nil {
		slog.ErrorContext(ctx, "failed to recreate repo base path", "path", s.repoBasePath, "error", err)
	}
	return s.repo.DeleteAll(ctx)
}

func (s *repoService) performClone(orgID, projectID, repoURL, clonePath string) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	slog.Info("cloning repository", "project", projectID, "url", repoURL, "path", clonePath)

	if err := os.MkdirAll(filepath.Dir(clonePath), 0o755); err != nil {
		s.updateRepoStatus(projectID, "error", fmt.Sprintf("create directory: %v", err))
		return
	}

	cred, err := s.resolver.Resolve(ctx, orgID)
	if err != nil {
		s.updateRepoStatus(projectID, "error", fmt.Sprintf("resolve credential: %v", err))
		return
	}
	token, _, err := cred.Token(ctx)
	if err != nil {
		s.updateRepoStatus(projectID, "error", fmt.Sprintf("token: %v", err))
		return
	}

	askPassScript, err := createAskPassScript(token)
	if err != nil {
		s.updateRepoStatus(projectID, "error", fmt.Sprintf("create askpass script: %v", err))
		return
	}
	defer os.Remove(askPassScript)

	cmd := exec.CommandContext(ctx, "git", "clone", repoURL, clonePath)
	cmd.Env = append(os.Environ(),
		"GIT_ASKPASS="+askPassScript,
		"GIT_TERMINAL_PROMPT=0",
	)

	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		errMsg := stderr.String()
		if strings.Contains(errMsg, "Authentication") || strings.Contains(errMsg, "could not read Username") {
			s.updateRepoStatus(projectID, "error", "authentication failed — check platform PAT")
		} else {
			s.updateRepoStatus(projectID, "error", fmt.Sprintf("clone failed: %s", errMsg))
		}
		return
	}

	defaultBranch := detectDefaultBranch(clonePath)

	repo, err := s.repo.GetByProjectID(context.Background(), projectID)
	if err != nil || repo == nil {
		slog.Error("failed to update repo after clone", "project", projectID, "error", err)
		return
	}

	repo.Status = "ready"
	repo.DefaultBranch = defaultBranch
	if err := s.repo.Update(context.Background(), repo); err != nil {
		slog.Error("failed to update repo status", "project", projectID, "error", err)
	}

	slog.Info("repository cloned successfully", "project", projectID, "branch", defaultBranch)
}

func (s *repoService) updateRepoStatus(projectID, status, errorMsg string) {
	repo, err := s.repo.GetByProjectID(context.Background(), projectID)
	if err != nil || repo == nil {
		slog.Error("failed to find repo for status update", "project", projectID, "error", err)
		return
	}
	repo.Status = status
	repo.ErrorMessage = errorMsg
	if err := s.repo.Update(context.Background(), repo); err != nil {
		slog.Error("failed to update repo status", "project", projectID, "error", err)
	}
}

func createAskPassScript(pat string) (string, error) {
	f, err := os.CreateTemp("", "git-askpass-*.sh")
	if err != nil {
		return "", err
	}
	_, err = f.WriteString(fmt.Sprintf("#!/bin/sh\necho '%s'\n", pat))
	if err != nil {
		f.Close()
		os.Remove(f.Name())
		return "", err
	}
	f.Close()
	if err := os.Chmod(f.Name(), 0o700); err != nil {
		os.Remove(f.Name())
		return "", err
	}
	return f.Name(), nil
}

func detectDefaultBranch(repoPath string) string {
	cmd := exec.Command("git", "-C", repoPath, "rev-parse", "--abbrev-ref", "HEAD")
	out, err := cmd.Output()
	if err != nil {
		return "main"
	}
	branch := strings.TrimSpace(string(out))
	if branch == "" {
		return "main"
	}
	return branch
}

var repoSlugInvalid = regexp.MustCompile(`[^a-z0-9-]+`)

// slugifyProjectName produces the slug portion of a repo name. The 3-digit suffix is added by the caller.
func slugifyProjectName(projectName string) string {
	slug := strings.ToLower(projectName)
	slug = repoSlugInvalid.ReplaceAllString(slug, "-")
	slug = strings.Trim(slug, "-")
	for strings.Contains(slug, "--") {
		slug = strings.ReplaceAll(slug, "--", "-")
	}
	if len(slug) > 40 {
		slug = strings.TrimRight(slug[:40], "-")
	}
	if slug == "" {
		return "project"
	}
	return slug
}
