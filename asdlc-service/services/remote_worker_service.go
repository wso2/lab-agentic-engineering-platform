package services

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/wso2/asdlc/asdlc-service/clients/gitservice"
	"github.com/wso2/asdlc/asdlc-service/clients/remoteworker"
	"github.com/wso2/asdlc/asdlc-service/models"
	"github.com/wso2/asdlc/asdlc-service/repositories"
)

// DispatchResult represents the outcome of dispatching a single task.
type DispatchResult struct {
	TaskID         string `json:"taskId"`
	ComponentName  string `json:"componentName"`
	WorkspacePath  string `json:"workspacePath"`
	BranchName     string `json:"branchName"`
	PullRequestURL string `json:"pullRequestUrl,omitempty"`
	Status         string `json:"status"`
	Error          string `json:"error,omitempty"`
}

// RemoteWorkerService orchestrates dispatching pending tasks to the
// remote-worker.
//
// Phase 0 dispatch is GitHub-native: per task we ensure issue + feature
// branch + draft PR exist on GitHub, then ask the remote-worker to clone
// the branch into a per-task workspace and spawn a Claude CLI. The agent
// uses git + gh directly via /credentials/refresh; no MCP.
//
// Each step is idempotent on its persisted column (§12.1) so re-dispatch
// is safe across crashes:
//   1. Task row exists (created at GenerateTasks time)
//   2. IssueNumber set       → skip create-issue
//   3. BranchName set        → skip create-branch
//   4. PullRequestNumber set → skip create-PR
//   5. DispatchedAt set      → skip remote-worker call
type RemoteWorkerService interface {
	DispatchTasks(ctx context.Context, orgID, projectID string) ([]DispatchResult, error)
}

type remoteWorkerService struct {
	taskRepo          repositories.TaskRepository
	workerClient      remoteworker.Client
	gitClient         gitservice.Client
	componentSvc      ComponentService
	store             *ArtifactStore
	taskTokens        *TaskTokenManager
	tokenInject       func(ctx context.Context) context.Context
	gitServiceHostURL string
}

func NewRemoteWorkerService(
	taskRepo repositories.TaskRepository,
	workerClient remoteworker.Client,
	gitClient gitservice.Client,
	componentSvc ComponentService,
	store *ArtifactStore,
	taskTokens *TaskTokenManager,
	tokenInject func(ctx context.Context) context.Context,
	gitServiceHostURL string,
) RemoteWorkerService {
	return &remoteWorkerService{
		taskRepo:          taskRepo,
		workerClient:      workerClient,
		gitClient:         gitClient,
		componentSvc:      componentSvc,
		store:             store,
		taskTokens:        taskTokens,
		tokenInject:       tokenInject,
		gitServiceHostURL: gitServiceHostURL,
	}
}

func (s *remoteWorkerService) DispatchTasks(ctx context.Context, orgID, projectID string) ([]DispatchResult, error) {
	tasks, err := s.taskRepo.ListByProjectID(ctx, orgID, projectID)
	if err != nil {
		return nil, fmt.Errorf("list tasks: %w", err)
	}

	// Resolve the project's repo + the org's credential identity once for
	// the whole batch. Phase 2 PR C — the legacy GetCredentials bridge is
	// gone; identity comes from /internal/credentials/orgs/{ocOrgId}/identity
	// (App: bot identity; PAT: PAT owner) and the repo's URL/default branch
	// from /api/v1/repos/{projectId}. The build token is provisioned per
	// WorkflowRun by the BFF's mint-build call, not at dispatch.
	repoInfo, err := s.gitClient.GetRepo(ctx, orgID, projectID)
	if err != nil {
		return nil, fmt.Errorf("get repo: %w", err)
	}
	if repoInfo == nil {
		return nil, fmt.Errorf("project repo not provisioned")
	}
	if repoInfo.DefaultBranch == "" {
		repoInfo.DefaultBranch = "main"
	}
	identity, err := s.gitClient.GetCredentialIdentity(ctx, orgID)
	if err != nil {
		return nil, fmt.Errorf("get credential identity: %w", err)
	}

	// Build a {title → status} index for dependsOn resolution. Multi-task-
	// per-component (tech-lead revamp §12) introduces base-commit ordering:
	// a task is dispatchable only when every task it dependsOn (by title)
	// has merged. Un-merged deps → status pending_deps; the task waits for
	// the merge webhook (handlers.go::PullRequestClosed) to re-evaluate.
	statusByTitle := make(map[string]string, len(tasks))
	for _, t := range tasks {
		statusByTitle[t.Title] = t.Status
	}

	var results []DispatchResult
	for i := range tasks {
		task := &tasks[i]
		if task.Status == string(models.TaskStatusPendingDeps) {
			// Re-check: maybe a sibling merged in the meantime.
			if !depsAllMerged(task, statusByTitle) {
				continue
			}
			task.Status = string(models.TaskStatusPending)
			if err := s.taskRepo.Update(ctx, task); err != nil {
				slog.WarnContext(ctx, "clear pending_deps", "task", task.ID, "error", err)
				continue
			}
		}
		if task.Status != string(models.TaskStatusPending) {
			continue
		}

		// Gate: any un-merged dependsOn → pending_deps; revisit on merge webhook.
		if !depsAllMerged(task, statusByTitle) {
			task.Status = string(models.TaskStatusPendingDeps)
			if err := s.taskRepo.Update(ctx, task); err != nil {
				slog.WarnContext(ctx, "set pending_deps", "task", task.ID, "error", err)
			}
			continue
		}

		res := s.dispatchOne(ctx, task, repoInfo, identity)
		results = append(results, res)
	}

	return results, nil
}

// depsAllMerged returns true when every task title listed in TaskDependsOn
// has status=merged|building|deployed (i.e. work landed on main). Empty
// dependsOn returns true. Unknown titles (referenced dep not in this
// project) are treated as merged — we can't block on something we don't
// own. Match titles case-sensitively (the planner emits exact titles).
func depsAllMerged(task *models.ComponentTask, statusByTitle map[string]string) bool {
	for _, depTitle := range task.TaskDependsOn {
		st, ok := statusByTitle[depTitle]
		if !ok {
			continue
		}
		switch st {
		case string(models.TaskStatusMerged),
			string(models.TaskStatusBuilding),
			string(models.TaskStatusDeployed):
			continue
		}
		return false
	}
	return true
}

// dispatchOne drives the §12.1 idempotency contract for a single task.
func (s *remoteWorkerService) dispatchOne(
	ctx context.Context,
	task *models.ComponentTask,
	repoInfo *gitservice.RepoInfo,
	identity *gitservice.IdentityProjection,
) DispatchResult {
	defaultBranch := repoInfo.DefaultBranch
	res := DispatchResult{TaskID: task.ID, ComponentName: task.ComponentName}

	// Step: ensure GitHub issue. GenerateTasks already attempts this — if it
	// failed earlier the issue is missing here, and dispatch must refuse.
	if task.IssueNumber == 0 || task.IssueURL == "" {
		s.markFailed(ctx, task, "no GitHub issue on task — generation must precede dispatch")
		return failResult(res, task.ErrorMessage)
	}

	// Step: ensure feature branch.
	branchName := task.BranchName
	if branchName == "" {
		branchName = computeBranchName(task)
	}
	if _, err := s.gitClient.CreateBranch(ctx, task.OrgID, task.ProjectID, branchName, defaultBranch); err != nil {
		s.markFailed(ctx, task, fmt.Sprintf("create branch %s: %v", branchName, err))
		return failResult(res, task.ErrorMessage)
	}
	if task.BranchName == "" {
		task.BranchName = branchName
		if err := s.taskRepo.Update(ctx, task); err != nil {
			s.markFailed(ctx, task, fmt.Sprintf("persist branch: %v", err))
			return failResult(res, task.ErrorMessage)
		}
	}

	// Step: seed a placeholder commit on the feature branch. GitHub rejects
	// draft PRs whose head and base are at the same SHA; we drop a small
	// .asdlc/task.json marker on the branch so the PR has something to
	// represent. Idempotent on (path, content) — re-dispatch is a no-op.
	seedPayload := fmt.Sprintf(`{"taskId":"%s","componentName":"%s","issueNumber":%d}`+"\n",
		task.ID, task.ComponentName, task.IssueNumber)
	seedMsg := fmt.Sprintf("chore: seed task %s for %s", task.ID, task.ComponentName)
	if err := s.gitClient.SeedBranchCommit(ctx, task.OrgID, task.ProjectID, branchName, ".asdlc/task.json", seedMsg, seedPayload); err != nil {
		s.markFailed(ctx, task, fmt.Sprintf("seed branch commit: %v", err))
		return failResult(res, task.ErrorMessage)
	}

	// Step: ensure draft PR.
	if task.PullRequestNumber == 0 {
		prTitle := fmt.Sprintf("[%s] %s", task.ComponentName, task.Title)
		prBody := fmt.Sprintf("Implementation for component **%s**.\n\nCloses #%d", task.ComponentName, task.IssueNumber)
		pr, err := s.gitClient.CreateDraftPR(ctx, task.OrgID, task.ProjectID, &gitservice.CreateDraftPRRequest{
			Title: prTitle,
			Body:  prBody,
			Head:  branchName,
			Base:  defaultBranch,
		})
		if err != nil {
			s.markFailed(ctx, task, fmt.Sprintf("create draft PR: %v", err))
			return failResult(res, task.ErrorMessage)
		}
		task.PullRequestNumber = pr.Number
		task.PullRequestURL = pr.URL
		if err := s.taskRepo.Update(ctx, task); err != nil {
			s.markFailed(ctx, task, fmt.Sprintf("persist PR: %v", err))
			return failResult(res, task.ErrorMessage)
		}
	}

	// Step: ensure OC SecretReference + Component exist. The SecretReference
	// CR was created at repo provision (project_service.go) — at dispatch we
	// only ensure the Component is wired to that name. Idempotent on
	// (ocOrgId, project, componentName); repeat-dispatch is a no-op.
	// Component is created with AutoBuild=false — builds are driven by
	// BFF-created WorkflowRuns from the push handler.
	if s.componentSvc != nil {
		if err := s.ensureOCComponent(ctx, task, repoInfo); err != nil {
			s.markFailed(ctx, task, fmt.Sprintf("ensure OC component: %v", err))
			return failResult(res, task.ErrorMessage)
		}
	}

	// Step: issue per-task Task JWT (RS256, 24h). Re-issued on every dispatch
	// attempt so re-dispatch always carries a fresh token. git-service verifies
	// it via JWKS at /credentials/refresh — no shared secret on the wire.
	if s.taskTokens == nil {
		s.markFailed(ctx, task, "task token manager not configured")
		return failResult(res, task.ErrorMessage)
	}
	bearer, err := s.taskTokens.Issue(task.ID, task.OrgID, task.ProjectID)
	if err != nil {
		s.markFailed(ctx, task, fmt.Sprintf("issue task jwt: %v", err))
		return failResult(res, task.ErrorMessage)
	}

	// Step: call remote-worker. The host-side worker provisions the workspace
	// and spawns Claude. Identity is the org's credential identity (App: bot
	// identity; PAT: PAT owner) returned by GetCredentialIdentity.
	workerReq := &remoteworker.DispatchRequest{
		TaskID:        task.ID,
		OrgID:         task.OrgID,
		ProjectID:     task.ProjectID,
		ComponentName: task.ComponentName,
		BranchName:    branchName,
		RepoURL:       repoInfo.RepoURL,
		Bearer:        bearer,
		Identity: remoteworker.IdentityPayload{
			Name:  identity.Name,
			Email: identity.Email,
			Login: identity.Login,
		},
		GitServiceURL: s.gitServiceHostURL,
		Prompt:        buildAgentPrompt(task),
	}
	resp, err := s.workerClient.Dispatch(ctx, workerReq)
	if err != nil {
		s.markFailed(ctx, task, fmt.Sprintf("remote-worker dispatch: %v", err))
		return failResult(res, task.ErrorMessage)
	}
	if resp.Status == "failed" {
		s.markFailed(ctx, task, fmt.Sprintf("remote-worker error: %s", resp.Error))
		return failResult(res, task.ErrorMessage)
	}

	// Step: mark dispatched + in_progress. The DispatchedAt write happens
	// after the worker confirms the workspace was provisioned (§12.1).
	now := time.Now()
	task.DispatchedAt = &now
	task.WorkspacePath = resp.WorkspacePath
	task.Status = string(models.TaskStatusInProgress)
	if err := s.taskRepo.Update(ctx, task); err != nil {
		slog.ErrorContext(ctx, "failed to update task after dispatch",
			"task", task.ID, "error", err)
	}

	slog.InfoContext(ctx, "task dispatched",
		"task", task.ID, "component", task.ComponentName,
		"branch", branchName, "pr", task.PullRequestNumber, "workspace", resp.WorkspacePath)

	res.WorkspacePath = resp.WorkspacePath
	res.BranchName = branchName
	res.PullRequestURL = task.PullRequestURL
	res.Status = "running"
	return res
}

func failResult(r DispatchResult, msg string) DispatchResult {
	r.Status = "failed"
	r.Error = msg
	return r
}

func (s *remoteWorkerService) markFailed(ctx context.Context, task *models.ComponentTask, msg string) {
	task.Status = string(models.TaskStatusFailed)
	task.ErrorMessage = msg
	if err := s.taskRepo.Update(ctx, task); err != nil {
		slog.ErrorContext(ctx, "failed to mark task failed", "task", task.ID, "error", err)
	}
	slog.ErrorContext(ctx, "dispatch step failed", "task", task.ID, "error", msg)
}

// computeBranchName produces the deterministic feature-branch name:
// task/<slug(component-name)>-<short8(taskID)>. The slug + short ID combo
// is human-friendly while still unique per task.
func computeBranchName(task *models.ComponentTask) string {
	slug := strings.ToLower(task.ComponentName)
	slug = strings.NewReplacer(" ", "-", "_", "-", "/", "-").Replace(slug)
	slug = trimNonSlug(slug)
	if slug == "" {
		slug = "component"
	}
	short := task.ID
	if len(short) > 8 {
		short = short[:8]
	}
	short = strings.ReplaceAll(short, "-", "")
	return "task/" + slug + "-" + short
}

func trimNonSlug(s string) string {
	var b strings.Builder
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' {
			b.WriteRune(r)
		}
	}
	out := b.String()
	for strings.Contains(out, "--") {
		out = strings.ReplaceAll(out, "--", "-")
	}
	return strings.Trim(out, "-")
}

// buildAgentPrompt returns the user prompt given to the Claude agent. The full
// task context lives in the GitHub issue body (see services/issue_body.go);
// the prompt points the agent at the issue and tells them the working branch
// is already checked out.
func buildAgentPrompt(task *models.ComponentTask) string {
	if task.BranchName != "" {
		return fmt.Sprintf("Work on this GitHub issue: %s\n\nYour working branch %s is already checked out in this directory.",
			task.IssueURL, task.BranchName)
	}
	return fmt.Sprintf("Work on this GitHub issue: %s", task.IssueURL)
}

// ensureOCComponent creates the OC Component (one per task component)
// needed for the build to fire when the merge push arrives. The
// SecretReference CR backing the build credential was created at repo
// provision (project_service.go); the Component points at it via
// repoInfo.OcSecretRefName. AutoBuild is false — every build is driven by
// the BFF-created WorkflowRun in the push webhook handler, with the merge
// SHA pinned at params.repository.revision.commit.
//
// Phase 2 PR C — replaces the OC GitSecret + CreateGitSecret call. The
// SecretReference is repo-scoped (one per repo, shared across components),
// so dispatch only re-references it.
func (s *remoteWorkerService) ensureOCComponent(
	ctx context.Context,
	task *models.ComponentTask,
	repoInfo *gitservice.RepoInfo,
) error {
	if s.tokenInject != nil {
		ctx = s.tokenInject(ctx)
	}
	componentName := toK8sName(task.ComponentName)

	// Read component shape (appPath, componentType) fresh from design.json.
	// Tasks no longer snapshot these — design edits between generation and
	// dispatch propagate to OC component creation.
	comp, err := resolveDesignComponentVia(ctx, s.store, task)
	if err != nil {
		return fmt.Errorf("resolve component: %w", err)
	}

	dockerContext := comp.AppPath
	dockerFilePath := "Dockerfile"
	if dockerContext != "" {
		dockerFilePath = dockerContext + "/Dockerfile"
	} else {
		dockerContext = "."
	}

	if repoInfo == nil || repoInfo.OcSecretRefName == nil || *repoInfo.OcSecretRefName == "" {
		return fmt.Errorf("repo has no SecretReference name; project=%s", task.ProjectID)
	}
	secretRefName := *repoInfo.OcSecretRefName

	branch := repoInfo.DefaultBranch
	if branch == "" {
		branch = "main"
	}

	// Description used to be the per-task agentInstructions snapshot. The
	// task-level body is now in the GH issue; the OC Component description
	// stays at the level of "what is this thing" — title + rationale fits.
	description := task.Title
	if task.Rationale != "" {
		description = task.Title + " — " + task.Rationale
	}

	// AutoBuild=false: builds are driven by the push webhook handler
	// creating WorkflowRuns explicitly. AutoDeploy=false because OC v1.0.0's
	// auto-deploy path doesn't fill EnvironmentConfigs from schema defaults;
	// DeployFromBuild handles the full chain when invoked.
	_, err = s.componentSvc.CreateComponent(ctx, task.OrgID, task.ProjectID, &models.CreateComponentRequest{
		Name:        componentName,
		DisplayName: task.ComponentName,
		Description: description,
		Type:        ocEntrypoint(comp.ComponentType),
		AutoBuild:   false,
		AutoDeploy:  false,
		Workflow: &models.ComponentWorkflowSpec{
			Kind: "ClusterWorkflow",
			Name: "dockerfile-builder",
			Parameters: &models.ComponentWorkflowParameters{
				Repository: &models.WorkflowRepository{
					URL:       repoInfo.RepoURL,
					SecretRef: secretRefName,
					AppPath:   comp.AppPath,
					Revision:  &models.WorkflowRevision{Branch: branch},
				},
				Docker: &models.DockerParameters{
					Context:  dockerContext,
					FilePath: dockerFilePath,
				},
			},
		},
	})
	if err != nil {
		return fmt.Errorf("create component: %w", err)
	}
	return nil
}
