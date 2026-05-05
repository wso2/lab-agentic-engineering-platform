package services

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"gorm.io/gorm"

	"github.com/wso2/asdlc/asdlc-service/clients/gitservice"
	"github.com/wso2/asdlc/asdlc-service/clients/openchoreo"
	"github.com/wso2/asdlc/asdlc-service/models"
	"github.com/wso2/asdlc/asdlc-service/repositories"
)

// WorkflowRunService creates OC WorkflowRun CRs pinned to a specific commit
// SHA. This is what flips the BFF from "configure autoBuild=true and let OC
// react to its own webhook" to "we drive every build from our webhook
// handler with the merge SHA injected at trigger time."
//
// Mirrors agent-manager (clients/openchoreosvc/client/builds.go:71-85).
//
// Idempotency is at the (componentName, sha) level via ComponentTask.LastBuildSHA:
// if a task already has LastBuildSHA == sha, the trigger is skipped. This
// makes pessimistic-fallback rebuilds and admin re-pushes safe.
type WorkflowRunService interface {
	// TriggerForPush creates a WorkflowRun for every component in the project
	// whose AppPath matches at least one of changedPaths and whose merged
	// task's LastBuildSHA != sha. Returns the list of (componentName, runName)
	// pairs created.
	TriggerForPush(ctx context.Context, orgID, projectID, sha string, changedPaths []string) ([]TriggeredRun, error)
	// RetryAuthFailedBuild — Phase 2 PR D §9.3. Mints a fresh build token
	// and recreates the task's WorkflowRun for the same SHA without
	// advancing LastBuildSHA. Used by the build watcher when classifyRun
	// returns TaskEventBuildAuthRetryExhausted-pending (i.e., the
	// classifier detected a git_clone_failed_auth that is still inside
	// the retry budget). Returns the new run name; caller persists it
	// onto the task row.
	RetryAuthFailedBuild(ctx context.Context, task *models.ComponentTask) (runName string, err error)
}

// TriggeredRun is the (component, runName) pair returned per build created.
type TriggeredRun struct {
	ComponentName string
	RunName       string
	TaskID        string
}

type workflowRunService struct {
	db          *gorm.DB
	taskRepo    repositories.TaskRepository
	ocClient    openchoreo.ComponentClient
	gitClient   gitservice.Client
	store       *ArtifactStore
	configSvc   ConfigService
	tokenInject func(ctx context.Context) context.Context
}

// NewWorkflowRunService constructs the service. tokenInject lets the BFF
// inject the service-auth token for OC API calls; pass nil for tests.
// gitClient is the BFF's git-service client — used to mint a fresh GitHub
// token via /internal/credentials/orgs/{ocOrgId}/mint-build immediately
// before each WorkflowRun (phase2.md §9.2). store is used to look up
// per-component AppPath for the changed-path filter — tasks no longer
// snapshot AppPath, dispatch reads design fresh.
func NewWorkflowRunService(
	db *gorm.DB,
	taskRepo repositories.TaskRepository,
	ocClient openchoreo.ComponentClient,
	gitClient gitservice.Client,
	store *ArtifactStore,
	tokenInject func(ctx context.Context) context.Context,
) WorkflowRunService {
	return &workflowRunService{
		db:          db,
		taskRepo:    taskRepo,
		ocClient:    ocClient,
		gitClient:   gitClient,
		store:       store,
		tokenInject: tokenInject,
	}
}

func (s *workflowRunService) TriggerForPush(ctx context.Context, orgID, projectID, sha string, changedPaths []string) ([]TriggeredRun, error) {
	if s.tokenInject != nil {
		ctx = s.tokenInject(ctx)
	}

	// Resolve the project's repo once — every component shares the same
	// (ocOrgId, repoSlug) tuple. The slug is the input to MintBuildToken.
	var repoSlug string
	if s.gitClient != nil {
		repo, err := s.gitClient.GetRepo(ctx, orgID, projectID)
		if err != nil {
			return nil, fmt.Errorf("get repo for slug: %w", err)
		}
		if repo == nil {
			return nil, fmt.Errorf("project repo not provisioned for %s", projectID)
		}
		repoSlug = repo.RepoSlug
		if repoSlug == "" {
			return nil, fmt.Errorf("repo %s has no repo_slug; rerun the migration or trigger lazy backfill", projectID)
		}
	}

	// All merged tasks for the project — these own the (component, sha) pair.
	var merged []models.ComponentTask
	if err := s.db.WithContext(ctx).
		Where("org_id = ? AND project_id = ?", orgID, projectID).
		Where("status IN ?", []string{
			string(models.TaskStatusMerged),
			string(models.TaskStatusBuilding),
			string(models.TaskStatusDeployed),
		}).
		Find(&merged).Error; err != nil {
		return nil, fmt.Errorf("scan tasks: %w", err)
	}

	// Decide which components to (re)build. AppPath empty means "matches any
	// path" — covers root-level monolithic components. AppPath is resolved
	// from the current design.json (tasks no longer snapshot it).
	appPathByComponent := make(map[string]string)
	if s.store != nil {
		if design, derr := s.store.ReadDesign(ctx, orgID, projectID); derr == nil && design != nil {
			for _, c := range design.Components {
				appPathByComponent[strings.ToLower(c.Name)] = c.AppPath
			}
		} else if derr != nil && !IsNotFound(derr) {
			slog.WarnContext(ctx, "design lookup failed for workflow trigger; treating all components as root-level",
				"orgId", orgID, "projectId", projectID, "error", derr)
		}
	}

	var runs []TriggeredRun
	seen := map[string]bool{}
	for i := range merged {
		t := &merged[i]
		key := strings.ToLower(t.ComponentName)
		if seen[key] {
			continue
		}
		seen[key] = true

		if t.LastBuildSHA == sha {
			continue // already built this exact SHA
		}
		appPath := appPathByComponent[key]
		if !pathsMatchComponent(appPath, changedPaths) {
			continue
		}

		// Phase 2 PR C — mint a fresh GitHub token immediately before the
		// build pod's git clone. Token is written to OpenBao at
		// secret/asdlc/{ocOrgId}/git/{repoSlug}; the Component's
		// SecretReference resolves it via external-secrets at pod start.
		// Errors classify per phase2.md §5.2: 404 → skip, 409 → skip
		// (orphan from a disconnect mid-trigger), 5xx → log + retry next push.
		if s.gitClient != nil && repoSlug != "" {
			if _, err := s.gitClient.MintBuildToken(ctx, orgID, repoSlug); err != nil {
				switch {
				case errors.Is(err, gitservice.ErrRepoNotInOrg):
					slog.WarnContext(ctx, "mint-build refused: repo/org mismatch",
						"orgId", orgID, "repoSlug", repoSlug, "task", t.ID)
					continue
				case errors.Is(err, gitservice.ErrOrgDisconnected):
					slog.WarnContext(ctx, "mint-build refused: org disconnected; skipping build",
						"orgId", orgID, "repoSlug", repoSlug, "task", t.ID)
					continue
				default:
					slog.ErrorContext(ctx, "mint-build transient",
						"orgId", orgID, "repoSlug", repoSlug, "task", t.ID, "error", err)
					continue
				}
			}
		}

		run, err := s.ocClient.TriggerBuildAtCommit(ctx, orgID, projectID, t.ComponentName, sha)
		if err != nil {
			slog.ErrorContext(ctx, "trigger build failed",
				"component", t.ComponentName, "sha", sha, "error", err)
			continue
		}

		// Persist LastBuildSHA + LastBuildRunName so the build watcher and
		// idempotency check both find the right state.
		t.LastBuildSHA = sha
		t.LastBuildRunName = run.Name
		if err := s.taskRepo.Update(ctx, t); err != nil {
			slog.WarnContext(ctx, "persist build run name failed", "task", t.ID, "error", err)
		}

		runs = append(runs, TriggeredRun{
			ComponentName: t.ComponentName,
			RunName:       run.Name,
			TaskID:        t.ID,
		})
	}
	return runs, nil
}

// pathsMatchComponent returns true when at least one path matches the
// component's app path prefix. Empty appPath matches everything (root-level
// component).
func pathsMatchComponent(appPath string, paths []string) bool {
	if len(paths) == 0 {
		// pessimistic: rebuild everything if we have no path data
		return true
	}
	prefix := strings.TrimSuffix(appPath, "/")
	if prefix == "" {
		return true
	}
	for _, p := range paths {
		if strings.HasPrefix(p, prefix+"/") || p == prefix {
			return true
		}
	}
	return false
}

// RetryAuthFailedBuild mints a fresh build token + creates a new
// WorkflowRun for the same commit SHA that the failed run targeted.
// LastBuildSHA stays unchanged; LastBuildRunName advances to the new
// run so the next watcher sweep tracks the retry rather than the dead
// original. Mint failures (org disconnected, repo not in org) abort the
// retry — the watcher will eventually exhaust the budget if the
// underlying problem persists.
func (s *workflowRunService) RetryAuthFailedBuild(ctx context.Context, task *models.ComponentTask) (string, error) {
	if s.tokenInject != nil {
		ctx = s.tokenInject(ctx)
	}
	if s.gitClient == nil {
		return "", fmt.Errorf("retry-auth-failed: git client not configured")
	}
	if task.LastBuildSHA == "" {
		return "", fmt.Errorf("retry-auth-failed: task has no LastBuildSHA")
	}
	repo, err := s.gitClient.GetRepo(ctx, task.OrgID, task.ProjectID)
	if err != nil {
		return "", fmt.Errorf("retry-auth-failed: get repo: %w", err)
	}
	if repo == nil || repo.RepoSlug == "" {
		return "", fmt.Errorf("retry-auth-failed: project %s has no repo slug", task.ProjectID)
	}
	if _, err := s.gitClient.MintBuildToken(ctx, task.OrgID, repo.RepoSlug); err != nil {
		switch {
		case errors.Is(err, gitservice.ErrRepoNotInOrg), errors.Is(err, gitservice.ErrOrgDisconnected):
			return "", err
		default:
			return "", fmt.Errorf("retry-auth-failed: mint-build: %w", err)
		}
	}
	run, err := s.ocClient.TriggerBuildAtCommit(ctx, task.OrgID, task.ProjectID, task.ComponentName, task.LastBuildSHA)
	if err != nil {
		return "", fmt.Errorf("retry-auth-failed: trigger build: %w", err)
	}
	return run.Name, nil
}
