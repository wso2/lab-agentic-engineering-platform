package services

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

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
	// DispatchTaskBuild fires an OC WorkflowRun for one specific task's
	// component at the given SHA. Called from the `pull_request.closed
	// merged=true` webhook handler — the single source of truth for "code
	// just landed on main". Idempotent on (task.LastBuildSHA,
	// task.LastBuildRunName): a second call with the same SHA after the
	// first has committed is a no-op that returns the existing run name.
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

// dispatchBuild is the shared inner path: pre-stage the per-WorkflowRun
// build Secret in workflows-<orgID> (`<runName>-git-secret`), trigger OC,
// mark building atomically. Callers handle filtering / iteration. See
// docs/design/build-credential-injection.md.
func (s *workflowRunService) dispatchBuild(
	ctx context.Context,
	task *models.ComponentTask,
	orgID, projectID, repoSlug, sha string,
) (string, error) {
	// Generate the WorkflowRun name upfront so we can race-free stage the
	// per-build K8s Secret before the WorkflowRun spawns the Argo pod.
	runName := openchoreo.NewBuildRunName(projectID, task.ComponentName)

	// Stage the per-WorkflowRun build Secret in workflows-<orgID> with the
	// org's GitHub credential. Errors classify identically to the previous
	// mint-build surface (404 → skip, 409 → skip, 5xx → log + retry).
	if s.gitClient != nil && repoSlug != "" {
		if _, err := s.gitClient.StageBuildSecret(ctx, orgID, repoSlug, runName); err != nil {
			switch {
			case errors.Is(err, gitservice.ErrRepoNotInOrg):
				slog.WarnContext(ctx, "stage-build-secret refused: repo/org mismatch",
					"orgId", orgID, "repoSlug", repoSlug, "task", task.ID)
				return "", err
			case errors.Is(err, gitservice.ErrOrgDisconnected):
				slog.WarnContext(ctx, "stage-build-secret refused: org disconnected; skipping build",
					"orgId", orgID, "repoSlug", repoSlug, "task", task.ID)
				return "", err
			default:
				slog.ErrorContext(ctx, "stage-build-secret transient",
					"orgId", orgID, "repoSlug", repoSlug, "task", task.ID, "error", err)
				return "", err
			}
		}
	}

	run, err := s.ocClient.TriggerBuildAtCommit(ctx, orgID, projectID, task.ComponentName, sha, runName)
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
	runName := openchoreo.NewBuildRunName(task.ProjectID, task.ComponentName)
	if _, err := s.gitClient.StageBuildSecret(ctx, task.OrgID, repo.RepoSlug, runName); err != nil {
		switch {
		case errors.Is(err, gitservice.ErrRepoNotInOrg), errors.Is(err, gitservice.ErrOrgDisconnected):
			return "", err
		default:
			return "", fmt.Errorf("retry-auth-failed: stage-build-secret: %w", err)
		}
	}
	run, err := s.ocClient.TriggerBuildAtCommit(ctx, task.OrgID, task.ProjectID, task.ComponentName, task.LastBuildSHA, runName)
	if err != nil {
		return "", fmt.Errorf("retry-auth-failed: trigger build: %w", err)
	}
	return run.Name, nil
}
