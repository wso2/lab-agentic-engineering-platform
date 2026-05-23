package services

import (
	"context"
	"errors"
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
	// traitSync, when non-nil, is invoked after CreateComponent to
	// reconcile per-environment trait configs (the part CreateComponent
	// can't pre-stamp because RBs are created asynchronously by OC's
	// autoDeploy controller). Set via WithTraitSync. Optional in tests.
	traitSync *TraitSyncService
	// runtimeConfig, when non-nil, writes `env-config.js` onto each
	// web-app's ReleaseBindings after CreateComponent. The SPA loads
	// the file synchronously before its bundle so `window._env_` is
	// populated before any React module runs — no rebuild needed when
	// per-env values change. Wired via SetRuntimeConfig.
	runtimeConfig *RuntimeConfigService
}

// DispatchServiceWithTraitSync surfaces the trait_sync setter without
// polluting the public DispatchService interface (parallels the
// DesignServiceWithTaskHook pattern in design_service.go).
type DispatchServiceWithTraitSync interface {
	DispatchService
	SetTraitSync(traitSync *TraitSyncService)
}

// SetRuntimeConfig installs the env-config.js emitter that writes
// per-env values onto each web-app's ReleaseBindings. Call after
// NewDispatchService in production wiring.
func (s *dispatchService) SetRuntimeConfig(r *RuntimeConfigService) {
	s.runtimeConfig = r
}

// SetTraitSync installs the shared trait_sync emitter. Call after
// NewDispatchService in production wiring.
func (s *dispatchService) SetTraitSync(traitSync *TraitSyncService) {
	s.traitSync = traitSync
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
		if task.Status == string(models.TaskStatusOnHold) {
			if !depsAllDeployed(task, statusByComponent) {
				continue
			}
			task.Status = string(models.TaskStatusPending)
			if err := s.taskRepo.Update(ctx, task); err != nil {
				slog.WarnContext(ctx, "clear on_hold", "task", task.ID, "error", err)
				continue
			}
		}
		if task.Status != string(models.TaskStatusPending) {
			continue
		}

		if !depsAllDeployed(task, statusByComponent) {
			task.Status = string(models.TaskStatusOnHold)
			if err := s.taskRepo.Update(ctx, task); err != nil {
				slog.WarnContext(ctx, "set on_hold", "task", task.ID, "error", err)
			}
			if task.IssueURL != "" {
				if err := s.gitClient.MoveIssueToStatus(ctx, task.ProjectID, task.IssueURL, "On Hold"); err != nil {
					slog.WarnContext(ctx, "failed to move board item to On Hold",
						"task", task.ID, "error", err)
				}
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
	// F3a — assert that every component this task depends on has a
	// non-empty external URL at dispatch time. Under F2 deploy-gating,
	// every dep is `deployed` at this point so ListDeployments must
	// return a non-empty external URL — if any URL is empty, the deps
	// invariant is broken (probably a missing `visibility: external` on
	// the provider's spec.endpoints) and we fail loudly rather than
	// dispatching a task that will fail to verify.
	//
	// The resolved URLs are NOT passed through the prompt. SPAs receive
	// them at runtime via `window._env_` (BFF writes per-env values into
	// `env-config.js` on each ReleaseBinding). Keeping the prompt thin
	// matches both cluster and local flows.
	depEndpoints, err := s.resolveDependencyEndpoints(ctx, task)
	if err != nil {
		const deferDeadline = 2 * time.Minute
		now := time.Now()
		if task.DispatchDeferredAt != nil && time.Since(*task.DispatchDeferredAt) > deferDeadline {
			// Deadline exceeded — not a timing race, genuine misconfiguration.
			s.markFailed(ctx, task, fmt.Sprintf("resolve dependency endpoints: %v", err))
			return failResult(res, task.ErrorMessage)
		}
		// First attempt or still within deadline — the OC ReleaseBinding
		// controller may not have resolved the external URL yet (timing race
		// between build WorkflowRun completion and ingress provisioning).
		// Revert to on_hold; the on_hold_watcher retries every 10s.
		if task.DispatchDeferredAt == nil {
			task.DispatchDeferredAt = &now
		}
		task.Status = string(models.TaskStatusOnHold)
		task.ErrorMessage = fmt.Sprintf("resolve dependency endpoints: %v", err)
		if err := s.taskRepo.Update(ctx, task); err != nil {
			slog.WarnContext(ctx, "dispatchOne: revert to on_hold failed", "task", task.ID, "error", err)
		}
		if task.IssueURL != "" {
			if err := s.gitClient.MoveIssueToStatus(ctx, task.ProjectID, task.IssueURL, "On Hold"); err != nil {
				slog.WarnContext(ctx, "dispatchOne: move board item to On Hold", "task", task.ID, "error", err)
			}
		}
		slog.WarnContext(ctx, "dispatch deferred: dep external URL not yet available",
			"task", task.ID, "deferredAt", task.DispatchDeferredAt, "deadline", deferDeadline)
		return failResult(res, task.ErrorMessage)
	}
	// URL resolved — clear the deferred timestamp from any prior attempts.
	task.DispatchDeferredAt = nil
	prompt := buildAgentPrompt(task)
	slog.InfoContext(ctx, "dispatched with dep endpoints",
		"task", task.ID,
		"component", task.ComponentName,
		"deps", depEndpoints,
	)

	// Per-dispatch pre-flight: ensure the org has an active Anthropic key,
	// then SSA-refresh the WP Secret. Returns ErrAnthropicKeyRequired when
	// the org row is missing or inactive; we surface that as a structured
	// failure rather than markFailed so the console can offer "configure
	// key" instead of "retry". See docs/design/anthropic-key-dual-token.md §6.2.
	anthropicRes, err := s.gitClient.ApplyAnthropicWPSecret(ctx, task.OrgID)
	if err != nil {
		if errors.Is(err, gitservice.ErrAnthropicKeyRequired) {
			s.markFailed(ctx, task, "anthropic_key_required: configure an Anthropic API key in org settings before dispatching the remote coding agent")
			res = failResult(res, task.ErrorMessage)
			res.Error = "anthropic_key_required"
			return res
		}
		s.markFailed(ctx, task, fmt.Sprintf("apply anthropic wp-secret: %v", err))
		return failResult(res, task.ErrorMessage)
	}

	runName, err := s.wfRunService.TriggerCodingAgent(ctx, CodingAgentTrigger{
		Task:               task,
		RepoURL:            repoInfo.RepoURL,
		IdentityName:       identity.Name,
		IdentityEmail:      identity.Email,
		IdentityLogin:      identity.Login,
		Prompt:             prompt,
		Bearer:             bearer,
		GitServiceURL:      s.gitServiceURL,
		PlatformURL:        s.platformURL,
		AnthropicSecretRef: anthropicRes.SecretRefName,
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

	// Move the GitHub Project board item to "In Progress" so the console
	// kanban reflects dispatch state immediately (GitHub does not do this
	// automatically on WorkflowRun creation).
	if err := s.gitClient.MoveIssueToStatus(ctx, task.ProjectID, task.IssueURL, "In Progress"); err != nil {
		slog.WarnContext(ctx, "failed to move board item to In Progress",
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
	// Sync the GitHub project board item so it surfaces in the Failed column.
	if task.IssueURL != "" {
		if err := s.gitClient.MoveIssueToStatus(ctx, task.ProjectID, task.IssueURL, "Failed"); err != nil {
			slog.WarnContext(ctx, "markFailed: move board item to Failed", "task", task.ID, "error", err)
		}
	}
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


// resolveExternalURL resolves a single component's first external URL, or
// "" if the component has no deployed endpoint with `visibility: external`.
// Mirrors resolveDependencyEndpoints' inner step but for a single
// component, since AnnounceDependencyDeployed doesn't need a per-task
// task object — only the component name that just deployed.
func (s *dispatchService) resolveExternalURL(ctx context.Context, orgID, projectID, componentName string) string {
	list, err := s.componentSvc.ListDeployments(ctx, orgID, projectID, toK8sName(componentName))
	if err != nil {
		slog.WarnContext(ctx, "announce dep deployed: list deployments failed",
			"project", projectID, "component", componentName, "error", err)
		return ""
	}
	return firstExternalURL(list)
}

// DependencyEndpoint is one row used by resolveDependencyEndpoints — the
// dispatch-time §1.3 invariant guard ("every dep this task lists has a
// non-empty external URL"). The URL handoff to the SPA now flows through
// ReleaseBinding `env-config.js` (BFF emits per-env values into
// workloadOverrides.container.files), not through GitHub issue comments
// or the prompt.
type DependencyEndpoint struct {
	Component string
	URL       string
}

// buildAgentPrompt returns the user prompt given to the Claude agent. The
// full task context lives in the GitHub issue body
// (services/issue_body.go); the prompt only points the agent at the issue
// and reminds it how to link the PR back. Dependency URLs reach the SPA
// at runtime via `window._env_` (written into `env-config.js` by the BFF
// at ReleaseBinding time) — not through prompts or issue comments.
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

	// Per docs/design/build-credential-injection.md: build credentials are
	// pre-staged per WorkflowRun as a K8s Secret in workflows-<orgID> by
	// git-service. The Component's `repository.secretRef` parameter stays
	// empty so the upstream dockerfile-builder ClusterWorkflow skips its
	// SecretReference / ExternalSecret synth path entirely.
	const secretRefName = ""
	if repoInfo == nil {
		return fmt.Errorf("repo info missing for project=%s", task.ProjectID)
	}

	branch := repoInfo.DefaultBranch
	if branch == "" {
		branch = "main"
	}

	description := task.Title
	if task.Rationale != "" {
		description = task.Title + " — " + task.Rationale
	}

	// Per-component env vars are no longer stamped onto the Component's
	// workflow parameters. They live on the per-environment ReleaseBindings
	// (spec.workloadOverrides.container.env). configService.UpdateEnvVars
	// writes them out via componentSvc.UpdateWorkflowEnvVars, which patches
	// each ReleaseBinding for this component. On first dispatch the
	// ReleaseBindings don't exist yet — OC creates them after autoDeploy
	// observes the build's Workload — so the next config save (or the
	// caller's post-dispatch reconcile) is what lands env vars into them.
	// Derive the `api-configuration` trait from design.md's optional
	// `exposesAPI.auth` block. nil/none ⇒ no trait, no AP hop;
	// `required` ⇒ trait attached with cors+jwtAuth enabled in every env.
	// See services/trait_sync.go for the canonical emitter.
	apiSecurityEnabled := ResolveAPISecurityEnabled(*comp)
	traits, _ := DesiredAPIConfigurationTrait(componentName, apiSecurityEnabled)

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
			},
		},
		Traits: traits,
	})
	if err != nil {
		return fmt.Errorf("create component: %w", err)
	}

	// Best-effort post-create sync. Idempotent — the Component already has
	// traits set via CreateComponent above; this call resolves the
	// per-environment ReleaseBinding configs once OC creates them. When no
	// RBs exist yet the call is a soft no-op (the trait_sync watcher will
	// catch up).
	if apiSecurityEnabled && s.traitSync != nil {
		if syncErr := s.traitSync.SyncComponentTraits(ctx, task.OrgID, task.ProjectID, componentName); syncErr != nil {
			slog.WarnContext(ctx, "ensureOCComponent: trait_sync best-effort failed",
				"orgID", task.OrgID,
				"projectID", task.ProjectID,
				"componentName", componentName,
				"error", syncErr,
			)
		}
	}

	// Web-apps only: emit env-config.js into each ReleaseBinding so the
	// SPA's `window._env_` is populated at request time. Idempotent — the
	// OC client soft no-ops when no RBs exist yet; the cascade re-fires
	// after the first deploy lands a binding.
	if comp.ComponentType == "web-app" && s.runtimeConfig != nil {
		if rcErr := s.runtimeConfig.EmitForComponent(ctx, task.OrgID, task.ProjectID, componentName); rcErr != nil {
			slog.WarnContext(ctx, "ensureOCComponent: runtime_config best-effort failed",
				"orgID", task.OrgID,
				"projectID", task.ProjectID,
				"componentName", componentName,
				"error", rcErr,
			)
		}
	}

	return nil
}
