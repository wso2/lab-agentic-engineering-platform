package services

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/wso2/asdlc/asdlc-service/clients/gitservice"
	"github.com/wso2/asdlc/asdlc-service/models"
	"github.com/wso2/asdlc/asdlc-service/repositories"
)

// DispatchResult represents the outcome of dispatching a single task.
// BranchName / PullRequestURL are populated later by the
// pull_request.opened webhook handler when the agent opens its PR — they
// are not known at dispatch time anymore.
type DispatchResult struct {
	TaskID        string `json:"taskId"`
	ComponentName string `json:"componentName"`
	RunName       string `json:"runName,omitempty"`
	Status        string `json:"status"`
	Error         string `json:"error,omitempty"`
}

// DispatchService orchestrates dispatching pending tasks. Per task it:
//
//  1. Verifies a GitHub issue exists (created at task generation).
//  2. Ensures the OC Component exists (with AutoBuild=false).
//  3. Mints a fresh per-task RS256 JWT.
//  4. Creates a WorkflowRun of ClusterWorkflow `app-factory-coding-agent`
//     via WorkflowRunService.TriggerCodingAgent. The Argo pod clones
//     the project repo on its default branch and runs the Claude Agent
//     SDK with the asdlc skill loaded; the agent itself creates the
//     feature branch and opens the PR with `Closes #<issue>` so the
//     webhook handler can link the PR back to the task.
//
// Idempotency: dispatch is gated on `DispatchedAt` — once set, re-dispatch
// is a no-op. The agent owns branch+PR creation, and the
// pull_request.opened webhook persists `BranchName` and
// `PullRequestNumber` once the agent opens its PR.
type DispatchService interface {
	DispatchTasks(ctx context.Context, orgID, projectID string) ([]DispatchResult, error)
}

type dispatchService struct {
	taskRepo      repositories.TaskRepository
	gitClient     gitservice.Client
	componentSvc  ComponentService
	configSvc     ConfigService
	store         *ArtifactStore
	taskTokens    *TaskTokenManager
	tokenInject   func(ctx context.Context) context.Context
	wfRunService  WorkflowRunService
	gitServiceURL string // URL the agent pod uses to reach git-service; cross-namespace FQDN in cluster
}

func NewDispatchService(
	taskRepo repositories.TaskRepository,
	gitClient gitservice.Client,
	componentSvc ComponentService,
	configSvc ConfigService,
	store *ArtifactStore,
	taskTokens *TaskTokenManager,
	tokenInject func(ctx context.Context) context.Context,
	wfRunService WorkflowRunService,
	gitServiceURL string,
) DispatchService {
	return &dispatchService{
		taskRepo:      taskRepo,
		gitClient:     gitClient,
		componentSvc:  componentSvc,
		configSvc:     configSvc,
		store:         store,
		taskTokens:    taskTokens,
		tokenInject:   tokenInject,
		wfRunService:  wfRunService,
		gitServiceURL: gitServiceURL,
	}
}

func (s *dispatchService) DispatchTasks(ctx context.Context, orgID, projectID string) ([]DispatchResult, error) {
	tasks, err := s.taskRepo.ListByProjectID(ctx, orgID, projectID)
	if err != nil {
		return nil, fmt.Errorf("list tasks: %w", err)
	}

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
	// has merged.
	statusByTitle := make(map[string]string, len(tasks))
	for _, t := range tasks {
		statusByTitle[t.Title] = t.Status
	}

	var results []DispatchResult
	for i := range tasks {
		task := &tasks[i]
		if task.Status == string(models.TaskStatusPendingDeps) {
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
// has status=merged|building|deployed (i.e. work landed on main).
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

// dispatchOne drives the idempotency contract for a single task.
func (s *dispatchService) dispatchOne(
	ctx context.Context,
	task *models.ComponentTask,
	repoInfo *gitservice.RepoInfo,
	identity *gitservice.IdentityProjection,
) DispatchResult {
	res := DispatchResult{TaskID: task.ID, ComponentName: task.ComponentName}

	if task.IssueNumber == 0 || task.IssueURL == "" {
		s.markFailed(ctx, task, "no GitHub issue on task — generation must precede dispatch")
		return failResult(res, task.ErrorMessage)
	}

	if s.componentSvc != nil {
		if err := s.ensureOCComponent(ctx, task, repoInfo); err != nil {
			s.markFailed(ctx, task, fmt.Sprintf("ensure OC component: %v", err))
			return failResult(res, task.ErrorMessage)
		}
	}

	if s.taskTokens == nil {
		s.markFailed(ctx, task, "task token manager not configured")
		return failResult(res, task.ErrorMessage)
	}
	bearer, err := s.taskTokens.Issue(task.ID, task.OrgID, task.ProjectID)
	if err != nil {
		s.markFailed(ctx, task, fmt.Sprintf("issue task jwt: %v", err))
		return failResult(res, task.ErrorMessage)
	}

	if s.wfRunService == nil {
		s.markFailed(ctx, task, "workflow run service not configured")
		return failResult(res, task.ErrorMessage)
	}
	runName, err := s.wfRunService.TriggerCodingAgent(ctx, CodingAgentTrigger{
		Task:          task,
		RepoURL:       repoInfo.RepoURL,
		IdentityName:  identity.Name,
		IdentityEmail: identity.Email,
		IdentityLogin: identity.Login,
		Prompt:        buildAgentPrompt(task),
		Bearer:        bearer,
		GitServiceURL: s.gitServiceURL,
	})
	if err != nil {
		s.markFailed(ctx, task, fmt.Sprintf("trigger coding-agent: %v", err))
		return failResult(res, task.ErrorMessage)
	}

	now := time.Now()
	task.DispatchedAt = &now
	task.LastCodingAgentRunName = runName
	task.Status = string(models.TaskStatusInProgress)
	if err := s.taskRepo.Update(ctx, task); err != nil {
		slog.ErrorContext(ctx, "failed to update task after dispatch",
			"task", task.ID, "error", err)
	}

	slog.InfoContext(ctx, "task dispatched",
		"task", task.ID, "component", task.ComponentName, "run", runName)

	res.RunName = runName
	res.Status = "running"
	return res
}

func failResult(r DispatchResult, msg string) DispatchResult {
	r.Status = "failed"
	r.Error = msg
	return r
}

func (s *dispatchService) markFailed(ctx context.Context, task *models.ComponentTask, msg string) {
	task.Status = string(models.TaskStatusFailed)
	task.ErrorMessage = msg
	if err := s.taskRepo.Update(ctx, task); err != nil {
		slog.ErrorContext(ctx, "failed to mark task failed", "task", task.ID, "error", err)
	}
	slog.ErrorContext(ctx, "dispatch step failed", "task", task.ID, "error", msg)
}

// buildAgentPrompt returns the user prompt given to the Claude agent. The
// full task context lives in the GitHub issue body
// (services/issue_body.go); the prompt points the agent at the issue and
// tells them how to link their PR back to the task. The asdlc skill
// loaded in the runner image carries the rest of the workflow.
func buildAgentPrompt(task *models.ComponentTask) string {
	return fmt.Sprintf(
		"Work on this GitHub issue: %s\n\n"+
			"You are at the project repo root, on its default branch. Create your "+
			"own feature branch, implement the task, and open a PR whose body "+
			"includes the literal text `Closes #%d` so the platform links the "+
			"PR back to this task.",
		task.IssueURL, task.IssueNumber,
	)
}

// ensureOCComponent creates the OC Component (one per task component) needed
// for the build to fire when the merge push arrives. AutoBuild=false — every
// build is driven by the BFF's push-webhook handler creating a WorkflowRun
// pinned to the merge SHA. AutoDeploy=true — OC's Component controller
// watches the Workload the build's generate-workload-cr step posts and
// creates the ReleaseBinding into the first environment of the project's
// DeploymentPipeline (development) with empty ComponentTypeEnvironmentConfigs;
// schema defaults on the `service` ClusterComponentType supply replicas,
// resources, and imagePullPolicy.
func (s *dispatchService) ensureOCComponent(
	ctx context.Context,
	task *models.ComponentTask,
	repoInfo *gitservice.RepoInfo,
) error {
	if s.tokenInject != nil {
		ctx = s.tokenInject(ctx)
	}
	componentName := toK8sName(task.ComponentName)

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

	description := task.Title
	if task.Rationale != "" {
		description = task.Title + " — " + task.Rationale
	}

	// Stamp the current per-component env vars onto the Component's
	// workflow parameters so the build's generate-workload-cr step writes
	// them into the Workload CR. autoDeploy then carries them into the
	// ReleaseBinding. Env-var changes after first dispatch flow in via
	// componentEnvVarsUpdated below.
	envVars := s.workflowEnvVars(ctx, task.OrgID, task.ProjectID, componentName)

	_, err = s.componentSvc.CreateComponent(ctx, task.OrgID, task.ProjectID, &models.CreateComponentRequest{
		Name:        componentName,
		DisplayName: task.ComponentName,
		Description: description,
		Type:        ocEntrypoint(comp.ComponentType),
		AutoBuild:   false,
		AutoDeploy:  true,
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
				EnvironmentVariables: envVars,
			},
		},
	})
	if err != nil {
		return fmt.Errorf("create component: %w", err)
	}
	return nil
}

// workflowEnvVars reads the stored env vars for this component and shapes
// them into the WorkflowEnvVarRef slice that the dockerfile-builder
// ClusterWorkflow's `environmentVariables` schema expects. Returns nil
// when nothing is configured or the lookup fails — the build workflow's
// `default: []` covers that case.
func (s *dispatchService) workflowEnvVars(ctx context.Context, orgID, projectID, componentName string) []models.WorkflowEnvVarRef {
	if s.configSvc == nil {
		return nil
	}
	stored, err := s.configSvc.GetEnvVarsForDeploy(ctx, orgID, projectID, componentName)
	if err != nil {
		slog.WarnContext(ctx, "fetch env vars for component workflow params; proceeding without",
			"org", orgID, "project", projectID, "component", componentName, "error", err)
		return nil
	}
	if len(stored) == 0 {
		return nil
	}
	out := make([]models.WorkflowEnvVarRef, 0, len(stored))
	for _, ev := range stored {
		out = append(out, models.WorkflowEnvVarRef{Key: ev.Key, Value: ev.Value})
	}
	return out
}
