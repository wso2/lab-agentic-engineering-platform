package services

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
	"strings"
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
	// MarkVerificationFailed transitions a task in_progress →
	// verification_failed when the agent reports its pre-PR integration
	// check failed. The PR stays a draft; the operator reviews the
	// diagnostic from the issue comment and clicks Retry (RetryTask).
	// Idempotent: subsequent calls on an already-verification_failed
	// task are absorbed (logged as a late event, no error).
	MarkVerificationFailed(ctx context.Context, taskID, diagnostic string) error
	// RetryTask transitions verification_failed → in_progress, clears
	// DispatchedAt + LastCodingAgentRunName, and re-dispatches the task
	// so a fresh WorkflowRun is created with a freshly minted bearer.
	// Returns the resulting DispatchResult.
	RetryTask(ctx context.Context, taskID string) (DispatchResult, error)
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
	projector     TaskStateProjector
	gitServiceURL string // URL the agent pod uses to reach git-service; cross-namespace FQDN in cluster
	platformURL   string // URL the agent pod uses to call the BFF F3c verification-failed callback
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
	projector TaskStateProjector,
	gitServiceURL string,
	platformURL string,
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
		projector:     projector,
		gitServiceURL: gitServiceURL,
		platformURL:   platformURL,
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

	// F2: deploy-gating. Build a {componentName → status} index for
	// dependsOn resolution. A task is dispatchable only when every task
	// it dependsOn (by component name) has reached `deployed`. This is
	// per-batch — DependsOnComponents lists names that map 1:1 to tasks
	// in the same batch (validated at persist time in task_stream.go).
	statusByComponent := make(map[string]string, len(tasks))
	for _, t := range tasks {
		statusByComponent[t.ComponentName] = t.Status
	}

	var results []DispatchResult
	for i := range tasks {
		task := &tasks[i]
		if task.Status == string(models.TaskStatusPendingDeps) {
			if !depsAllDeployed(task, statusByComponent) {
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

		if !depsAllDeployed(task, statusByComponent) {
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

// depsAllDeployed returns true when every component name listed in
// DependsOnComponents corresponds to a task whose Status == deployed.
// Unknown component names return false (fail closed; the persist-time
// validator in task_stream.go is the upstream guard).
func depsAllDeployed(task *models.ComponentTask, statusByComponent map[string]string) bool {
	for _, depComponent := range task.DependsOnComponents {
		st, ok := statusByComponent[depComponent]
		if !ok {
			return false
		}
		if st != string(models.TaskStatusDeployed) {
			return false
		}
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
	// F3a — resolve URLs for every component this task depends on so the
	// agent can bake them into the artifact and verify them before opening
	// the PR. Under F2 deploy-gating, every dep is `deployed` at this point
	// so ListDeployments must return a non-empty external URL — if any URL
	// is empty, the deps invariant is broken (probably a missing
	// `visibility: external` on the provider's spec.endpoints) and we
	// fail loudly rather than dispatching a task that will fail to verify.
	depEndpoints, err := s.resolveDependencyEndpoints(ctx, task)
	if err != nil {
		s.markFailed(ctx, task, fmt.Sprintf("resolve dependency endpoints: %v", err))
		return failResult(res, task.ErrorMessage)
	}
	prompt := buildAgentPrompt(task, depEndpoints)
	slog.InfoContext(ctx, "dispatched with dep endpoints",
		"task", task.ID,
		"component", task.ComponentName,
		"deps", depEndpoints,
	)
	runName, err := s.wfRunService.TriggerCodingAgent(ctx, CodingAgentTrigger{
		Task:          task,
		RepoURL:       repoInfo.RepoURL,
		IdentityName:  identity.Name,
		IdentityEmail: identity.Email,
		IdentityLogin: identity.Login,
		Prompt:        prompt,
		Bearer:        bearer,
		GitServiceURL: s.gitServiceURL,
		PlatformURL:   s.platformURL,
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

// MarkVerificationFailed (F3c) transitions a task from in_progress to
// verification_failed under the projector's per-task lock. The
// diagnostic is persisted to ErrorMessage so it surfaces on the board
// card alongside the Retry button.
func (s *dispatchService) MarkVerificationFailed(ctx context.Context, taskID, diagnostic string) error {
	if s.projector == nil {
		return fmt.Errorf("verification-failed: projector not configured")
	}
	// Trim very long diagnostics; ErrorMessage is operator-visible.
	if len(diagnostic) > 4000 {
		diagnostic = diagnostic[:4000] + "…(truncated)"
	}
	if err := s.projector.ApplyBuildResult(ctx, taskID, TaskEventVerificationFailed, diagnostic); err != nil {
		return fmt.Errorf("apply verification-failed: %w", err)
	}
	slog.InfoContext(ctx, "task marked verification_failed",
		"task", taskID, "diagnostic", diagnostic)
	return nil
}

// RetryTask (F3c) is the operator-driven retry path for a task in
// `verification_failed`. It:
//
//  1. Transitions verification_failed → in_progress via the projector
//     (TaskEventRetry).
//  2. Clears DispatchedAt + LastCodingAgentRunName + ErrorMessage so a
//     fresh WorkflowRun is created.
//  3. Calls dispatchOne to mint a new bearer + trigger a new agent pod
//     against the same component / issue / branch.
//
// The PR (if any) stays a draft; the new agent run pushes additional
// commits to the same feature branch. Idempotent on the retry trigger
// — calling twice in close succession re-applies the transition (the
// second call hits ErrInvalidTransition, treated as a no-op by the
// projector) but only the first dispatchOne wins on DispatchedAt.
func (s *dispatchService) RetryTask(ctx context.Context, taskID string) (DispatchResult, error) {
	if s.projector == nil {
		return DispatchResult{}, fmt.Errorf("retry: projector not configured")
	}
	if err := s.projector.ApplyBuildResult(ctx, taskID, TaskEventRetry, ""); err != nil {
		return DispatchResult{}, fmt.Errorf("apply retry: %w", err)
	}
	// Load fresh and clear the dispatch idempotency fields so the next
	// trigger creates a new WorkflowRun.
	task, err := s.taskRepo.GetByID(ctx, taskID)
	if err != nil {
		return DispatchResult{}, fmt.Errorf("retry: load task: %w", err)
	}
	if task == nil {
		return DispatchResult{}, fmt.Errorf("retry: task not found")
	}
	task.DispatchedAt = nil
	task.LastCodingAgentRunName = ""
	task.ErrorMessage = ""
	// Transition above already wrote Status=in_progress; persist the
	// idempotency-field clears alongside it.
	if err := s.taskRepo.Update(ctx, task); err != nil {
		return DispatchResult{}, fmt.Errorf("retry: clear dispatch fields: %w", err)
	}
	// Re-dispatch — mirrors the DispatchTasks dispatchOne path. We don't
	// reuse DispatchTasks because that batches across the project and
	// would skip our just-cleared task (it's in_progress, not pending).
	repoInfo, err := s.gitClient.GetRepo(ctx, task.OrgID, task.ProjectID)
	if err != nil {
		return DispatchResult{}, fmt.Errorf("retry: get repo: %w", err)
	}
	if repoInfo == nil {
		return DispatchResult{}, fmt.Errorf("retry: project repo not provisioned")
	}
	if repoInfo.DefaultBranch == "" {
		repoInfo.DefaultBranch = "main"
	}
	identity, err := s.gitClient.GetCredentialIdentity(ctx, task.OrgID)
	if err != nil {
		return DispatchResult{}, fmt.Errorf("retry: get identity: %w", err)
	}
	// dispatchOne doesn't gate on Status (the gating lives in
	// DispatchTasks); it triggers a fresh WorkflowRun and persists
	// DispatchedAt + LastCodingAgentRunName + Status=in_progress on success.
	res := s.dispatchOne(ctx, task, repoInfo, identity)
	slog.InfoContext(ctx, "task retried after verification_failed",
		"task", taskID, "status", res.Status)
	return res, nil
}

// DependencyEndpoint is one row in the agent prompt's "Dependency
// endpoints" section: an upstream component name + its resolved external
// URL. Sorted by Component for determinism (so the same task produces the
// same prompt bytes — matters for prompt caching and test snapshots).
type DependencyEndpoint struct {
	Component string
	URL       string
}

// buildAgentPrompt returns the user prompt given to the Claude agent. The
// full task context lives in the GitHub issue body
// (services/issue_body.go); the prompt points the agent at the issue and
// tells them how to link their PR back to the task. When the task has
// resolved dependency endpoints (F3a — under F2 deploy-gating, the URLs
// are guaranteed present at this point), they are listed in a stable,
// sorted block so the agent can bake them into the artifact (e.g.
// VITE_<UPSTREAM>_URL for Vite/React) and curl-verify before opening
// its PR. The asdlc skill loaded in the runner image carries the rest
// of the workflow.
func buildAgentPrompt(task *models.ComponentTask, deps []DependencyEndpoint) string {
	var sb strings.Builder
	sb.WriteString("Work on this GitHub issue: ")
	sb.WriteString(task.IssueURL)
	sb.WriteString("\n\n")
	if len(deps) > 0 {
		// Sort by component name for deterministic output. Mutating a
		// caller-owned slice would be a surprise — copy first.
		sorted := append([]DependencyEndpoint(nil), deps...)
		sort.Slice(sorted, func(i, j int) bool { return sorted[i].Component < sorted[j].Component })
		sb.WriteString("## Dependency endpoints (resolved at dispatch)\n")
		for _, d := range sorted {
			sb.WriteString(fmt.Sprintf("- %s: %s\n", d.Component, d.URL))
		}
		sb.WriteString("\n")
		sb.WriteString("Each URL above points to a live, deployed upstream component. ")
		sb.WriteString("Bake each one into this component as a build-time constant ")
		sb.WriteString("(for Vite/React: `.env` → `VITE_<UPSTREAM>_URL`, read via ")
		sb.WriteString("`import.meta.env.VITE_<UPSTREAM>_URL`; for other stacks, use ")
		sb.WriteString("the idiomatic build-time-constant mechanism). Do NOT add a ")
		sb.WriteString("`dependencies.endpoints` block to `workload.yaml` — consumer-side ")
		sb.WriteString("runtime URL injection is not used in this platform for v1.\n\n")
		sb.WriteString("Before marking your PR ready-for-review, run the integration ")
		sb.WriteString("verification step from the `asdlc` skill against each ")
		sb.WriteString("Dependency endpoint above. If verification fails, keep the ")
		sb.WriteString("PR a draft and follow the skill's recovery steps.\n\n")
	}
	sb.WriteString(fmt.Sprintf(
		"You are at the project repo root, on its default branch. Create your "+
			"own feature branch, implement the task, and open a PR whose body "+
			"includes the literal text `Closes #%d` so the platform links the "+
			"PR back to this task.",
		task.IssueNumber,
	))
	return sb.String()
}

// resolveDependencyEndpoints turns the task's DependsOnComponents list into
// a slice of (component, url) pairs by calling ComponentService.ListDeployments
// — the same path that powers the Deploy page (single source of truth). Under
// F2 deploy-gating every dep is `deployed` at dispatch time, so each
// ListDeployments call MUST return a non-empty external URL. An empty URL
// means the provider component is missing `visibility: external` on its
// `spec.endpoints` — that is the §1.3 invariant breaking. Fail loudly here.
func (s *dispatchService) resolveDependencyEndpoints(
	ctx context.Context,
	task *models.ComponentTask,
) ([]DependencyEndpoint, error) {
	if len(task.DependsOnComponents) == 0 || s.componentSvc == nil {
		return nil, nil
	}
	if s.tokenInject != nil {
		ctx = s.tokenInject(ctx)
	}
	out := make([]DependencyEndpoint, 0, len(task.DependsOnComponents))
	for _, depComponent := range task.DependsOnComponents {
		ocName := toK8sName(depComponent)
		list, err := s.componentSvc.ListDeployments(ctx, task.OrgID, task.ProjectID, ocName)
		if err != nil {
			return nil, fmt.Errorf("list deployments for %q: %w", depComponent, err)
		}
		url := firstExternalURL(list)
		if url == "" {
			return nil, fmt.Errorf(
				"dep %q has no external URL — confirm the provider's `workload.yaml` spec.endpoints declares `visibility: external` (see docs/design/cross-component-wiring-gaps.md §1.3)",
				depComponent,
			)
		}
		out = append(out, DependencyEndpoint{Component: depComponent, URL: url})
	}
	return out, nil
}

func firstExternalURL(list *models.DeploymentList) string {
	if list == nil {
		return ""
	}
	for _, d := range list.Items {
		if d.EndpointURL != "" {
			return d.EndpointURL
		}
	}
	return ""
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
