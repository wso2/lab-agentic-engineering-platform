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
	// DispatchTaskBuild fires an OC WorkflowRun for one specific task's
	// component at the given SHA, skipping the design.json path filter.
	// Used by the PullRequestClosed rendezvous handler (the task's own
	// merge — by definition this push will include code under its
	// component, so the filter is redundant). Idempotent on
	// (task.LastBuildSHA, task.LastBuildRunName).
	DispatchTaskBuild(ctx context.Context, task *models.ComponentTask, sha string) (runName string, err error)
	// RetryAuthFailedBuild — Phase 2 PR D §9.3. Mints a fresh build token
	// and recreates the task's WorkflowRun for the same SHA without
	// advancing LastBuildSHA. Used by the build watcher when classifyRun
	// returns TaskEventBuildAuthRetryExhausted-pending (i.e., the
	// classifier detected a git_clone_failed_auth that is still inside
	// the retry budget). Returns the new run name; caller persists it
	// onto the task row.
	RetryAuthFailedBuild(ctx context.Context, task *models.ComponentTask) (runName string, err error)
	// TriggerCodingAgent creates a WorkflowRun of ClusterWorkflow
	// `app-factory-coding-agent` for the per-task ephemeral pod that runs the
	// Claude Agent SDK. The pod clones the project's repo at its default
	// branch; the agent itself creates the feature branch and opens the PR
	// with `Closes #<issueNumber>`. The dispatch caller has already created
	// the GitHub issue and resolved the credential identity.
	TriggerCodingAgent(ctx context.Context, params CodingAgentTrigger) (runName string, err error)
}

// CodingAgentTrigger is the input to WorkflowRunService.TriggerCodingAgent.
// Built by the dispatch path from the ComponentTask plus the resolved repo
// info, identity, and prompt.
type CodingAgentTrigger struct {
	Task          *models.ComponentTask
	RepoURL       string
	IdentityName  string
	IdentityEmail string
	IdentityLogin string
	Prompt        string
	Bearer        string
	GitServiceURL string
	// PlatformURL is the BFF base URL the runner pod uses for the F3c
	// verification-failed callback. Empty disables that callback (the
	// runner posts the diagnostic on the GitHub issue regardless).
	PlatformURL string
	// AnthropicSecretRef is the name of the K8s Secret in workflows-<orgID>
	// carrying the per-org Anthropic API key. Materialised by git-service
	// in the dispatch pre-flight (see ApplyAnthropicWPSecret). The
	// ClusterWorkflow wires it into the pod's env via secretKeyRef.
	AnthropicSecretRef string
}

// TriggeredRun is the (component, runName) pair returned per build created.
type TriggeredRun struct {
	ComponentName string
	RunName       string
	TaskID        string
}

// TaskStateProjector is the subset of webhook.Projector that WorkflowRunService
// needs to atomically transition task state alongside build dispatch. Kept as
// an interface here so `services` doesn't depend on `services/webhook`
// (webhook already depends on services for the state machine).
type TaskStateProjector interface {
	// MarkBuilding records LastBuildSHA + LastBuildRunName and atomically
	// transitions merged → building under the per-task lock.
	MarkBuilding(ctx context.Context, taskID, sha, runName string) error
	// ApplyBuildResult applies a non-PR lifecycle event (e.g.
	// TaskEventBuildPathMismatch for merged → failed) with an optional
	// error message, under the per-task lock.
	ApplyBuildResult(ctx context.Context, taskID string, event TaskEvent, errMsg string) error
}

type workflowRunService struct {
	db          *gorm.DB
	taskRepo    repositories.TaskRepository
	ocClient    openchoreo.ComponentClient
	gitClient   gitservice.Client
	store       *ArtifactStore
	configSvc   ConfigService
	projector   TaskStateProjector
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
	projector TaskStateProjector,
	tokenInject func(ctx context.Context) context.Context,
) WorkflowRunService {
	return &workflowRunService{
		db:          db,
		taskRepo:    taskRepo,
		ocClient:    ocClient,
		gitClient:   gitClient,
		store:       store,
		projector:   projector,
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

		if t.LastBuildSHA == sha && t.LastBuildRunName != "" {
			continue // already built this exact SHA
		}
		appPath := appPathByComponent[key]
		isOwnMerge := t.MergeCommitSHA == sha
		if !pathsMatchComponent(appPath, changedPaths) {
			if isOwnMerge {
				// Contract violation: this push contains the task's own
				// merge commit, but no file in the push lives under the
				// component's appPath. Either the architect emitted an
				// appPath that doesn't match what the coding-agent
				// committed, or the agent strayed outside its component
				// folder. Fail loudly so the orphan is visible — the
				// previous silent skip stranded tasks in `building`.
				errMsg := fmt.Sprintf(
					"build dispatch skipped: appPath %q matched no file in merge %s (%d changed paths)",
					appPath, shortSHA(sha), len(changedPaths))
				slog.WarnContext(ctx, "build dispatch contract violation: own-merge path mismatch",
					"task", t.ID, "component", t.ComponentName,
					"appPath", appPath, "sha", sha,
					"changedPathsSample", samplePaths(changedPaths, 5))
				if s.projector != nil {
					if perr := s.projector.ApplyBuildResult(ctx, t.ID, TaskEventBuildPathMismatch, errMsg); perr != nil {
						slog.WarnContext(ctx, "mark path mismatch failed",
							"task", t.ID, "error", perr)
					}
				}
			} else {
				slog.DebugContext(ctx, "path filter skipped task",
					"task", t.ID, "component", t.ComponentName, "appPath", appPath)
			}
			continue
		}

		runName, err := s.dispatchBuild(ctx, t, orgID, projectID, repoSlug, sha)
		if err != nil {
			// dispatchBuild has already logged with appropriate detail.
			continue
		}
		runs = append(runs, TriggeredRun{
			ComponentName: t.ComponentName,
			RunName:       runName,
			TaskID:        t.ID,
		})
	}
	return runs, nil
}

// DispatchTaskBuild is the per-task entry point used by the
// PullRequestClosed rendezvous (push already arrived, task just got
// merged). It bypasses the changed-paths filter — for the task's own
// merge SHA, the path filter is redundant by construction.
//
// Idempotent: re-entrant calls with the same SHA and a populated
// LastBuildRunName return the existing name and dispatch nothing.
func (s *workflowRunService) DispatchTaskBuild(ctx context.Context, task *models.ComponentTask, sha string) (string, error) {
	if task == nil {
		return "", fmt.Errorf("dispatch task build: task is nil")
	}
	if s.tokenInject != nil {
		ctx = s.tokenInject(ctx)
	}
	if task.LastBuildSHA == sha && task.LastBuildRunName != "" {
		return task.LastBuildRunName, nil
	}

	var repoSlug string
	if s.gitClient != nil {
		repo, err := s.gitClient.GetRepo(ctx, task.OrgID, task.ProjectID)
		if err != nil {
			return "", fmt.Errorf("get repo for slug: %w", err)
		}
		if repo == nil || repo.RepoSlug == "" {
			return "", fmt.Errorf("project repo not provisioned for %s", task.ProjectID)
		}
		repoSlug = repo.RepoSlug
	}

	return s.dispatchBuild(ctx, task, task.OrgID, task.ProjectID, repoSlug, sha)
}

// dispatchBuild is the shared inner path: mint token, trigger OC, mark
// building atomically. Callers handle filtering / iteration.
func (s *workflowRunService) dispatchBuild(
	ctx context.Context,
	task *models.ComponentTask,
	orgID, projectID, repoSlug, sha string,
) (string, error) {
	// Phase 2 PR C — mint a fresh GitHub token immediately before the
	// build pod's git clone. Errors classify per phase2.md §5.2:
	// 404 → skip, 409 → skip (disconnect race), 5xx → log + retry.
	if s.gitClient != nil && repoSlug != "" {
		if _, err := s.gitClient.MintBuildToken(ctx, orgID, repoSlug); err != nil {
			switch {
			case errors.Is(err, gitservice.ErrRepoNotInOrg):
				slog.WarnContext(ctx, "mint-build refused: repo/org mismatch",
					"orgId", orgID, "repoSlug", repoSlug, "task", task.ID)
				return "", err
			case errors.Is(err, gitservice.ErrOrgDisconnected):
				slog.WarnContext(ctx, "mint-build refused: org disconnected; skipping build",
					"orgId", orgID, "repoSlug", repoSlug, "task", task.ID)
				return "", err
			default:
				slog.ErrorContext(ctx, "mint-build transient",
					"orgId", orgID, "repoSlug", repoSlug, "task", task.ID, "error", err)
				return "", err
			}
		}
	}

	run, err := s.ocClient.TriggerBuildAtCommit(ctx, orgID, projectID, task.ComponentName, sha)
	if err != nil {
		slog.ErrorContext(ctx, "trigger build failed",
			"component", task.ComponentName, "sha", sha, "error", err)
		return "", err
	}

	// Atomic state + fields. MarkBuilding writes LastBuildSHA +
	// LastBuildRunName AND transitions merged → building in the same
	// transaction, so the task never sits in `building` without a run.
	if s.projector != nil {
		if perr := s.projector.MarkBuilding(ctx, task.ID, sha, run.Name); perr != nil {
			slog.WarnContext(ctx, "mark building failed", "task", task.ID, "run", run.Name, "error", perr)
		}
	} else {
		// Fallback for tests that don't wire a projector — keep the field
		// writes so callers still observe the dispatched state.
		task.LastBuildSHA = sha
		task.LastBuildRunName = run.Name
		if err := s.taskRepo.Update(ctx, task); err != nil {
			slog.WarnContext(ctx, "persist build run name failed", "task", task.ID, "error", err)
		}
	}
	return run.Name, nil
}

func shortSHA(sha string) string {
	if len(sha) <= 12 {
		return sha
	}
	return sha[:12]
}

func samplePaths(paths []string, n int) []string {
	if len(paths) <= n {
		return paths
	}
	return paths[:n]
}

// pathsMatchComponent returns true when at least one path matches the
// component's app path prefix. Empty appPath matches everything (root-level
// component). appPath is normalised by stripping a leading/trailing slash —
// the architect emits paths like "/greeting-api" while GitHub push payloads
// carry file paths like "greeting-api/main.go".
func pathsMatchComponent(appPath string, paths []string) bool {
	if len(paths) == 0 {
		// pessimistic: rebuild everything if we have no path data
		return true
	}
	prefix := strings.Trim(appPath, "/")
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
// TriggerCodingAgent creates a fresh WorkflowRun of ClusterWorkflow
// `app-factory-coding-agent`. Each call increments the attempt counter
// implicitly via time.Now(); idempotency is at the dispatch-service level
// (DispatchedAt + LastCodingAgentRunName).
func (s *workflowRunService) TriggerCodingAgent(ctx context.Context, p CodingAgentTrigger) (string, error) {
	if s.tokenInject != nil {
		ctx = s.tokenInject(ctx)
	}
	if p.Task == nil {
		return "", fmt.Errorf("trigger coding-agent: task is nil")
	}
	if p.Bearer == "" {
		return "", fmt.Errorf("trigger coding-agent: empty bearer for task %s", p.Task.ID)
	}
	if p.GitServiceURL == "" {
		return "", fmt.Errorf("trigger coding-agent: empty git-service URL for task %s", p.Task.ID)
	}
	if p.RepoURL == "" {
		return "", fmt.Errorf("trigger coding-agent: empty repo URL for task %s", p.Task.ID)
	}

	run, err := s.ocClient.TriggerCodingAgent(ctx, openchoreo.CodingAgentParams{
		OrgName:            p.Task.OrgID,
		ProjectName:        p.Task.ProjectID,
		ComponentName:      p.Task.ComponentName,
		TaskID:             p.Task.ID,
		Prompt:             p.Prompt,
		RepoURL:            p.RepoURL,
		IdentityName:       p.IdentityName,
		IdentityEmail:      p.IdentityEmail,
		IdentityLogin:      p.IdentityLogin,
		Bearer:             p.Bearer,
		GitServiceURL:      p.GitServiceURL,
		PlatformURL:        p.PlatformURL,
		AnthropicSecretRef: p.AnthropicSecretRef,
	})
	if err != nil {
		return "", fmt.Errorf("trigger coding-agent: %w", err)
	}
	return run.Name, nil
}

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
