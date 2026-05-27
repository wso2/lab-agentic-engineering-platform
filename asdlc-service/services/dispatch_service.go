package services

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/wso2/asdlc/asdlc-service/clients/gitservice"
	"github.com/wso2/asdlc/asdlc-service/clients/thundersvc"
	"github.com/wso2/asdlc/asdlc-service/models"
	"github.com/wso2/asdlc/asdlc-service/repositories"
	"github.com/wso2/asdlc/asdlc-service/services/codingagent"
	"gorm.io/gorm"
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
	// AnnounceDependencyDeployed posts a `## Dependency endpoint resolved`
	// comment on the GitHub issue of every task in this project that lists
	// componentName in its DependsOnComponents and hasn't yet wrapped up
	// (status ∈ {pending, on_hold, in_progress}). Fired by the
	// cascade hook the moment a task lands `deployed`. Used by both
	// cluster-flow and local-flow agents — the comment is the single
	// source of truth for upstream URLs (the prompt no longer carries
	// them). Best-effort: per-task failures are logged but never bubble.
	AnnounceDependencyDeployed(ctx context.Context, orgID, projectID, componentName string)
	// RegisterUserAppRedirectURI ensures the just-deployed component's
	// external URL is registered as a redirect_uri on the user-apps
	// OAuth client in Thunder. No-op when the component is not a
	// web-app with auth.kind=oidc-spa, when the Thunder admin client
	// is not configured, or when the URL hasn't materialised yet (we
	// fail closed — manual register-after-deploy is always available).
	// Best-effort: never returns an error; per-call failures are logged.
	RegisterUserAppRedirectURI(ctx context.Context, orgID, projectID, componentName string)
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
	// userAppsOIDC, when ClientID is non-empty, drives the
	// `## OIDC client provisioned` comment posted on the issues of
	// web-app tasks whose design has auth.kind=oidc-spa. Wired via
	// SetUserAppsOIDC from main.go's config. Optional in tests.
	userAppsOIDC UserAppsOIDC
	// thunderAdmin, when non-nil, is used to register a deployed
	// user-webapp's external URL on the userAppsOIDC.ClientID app's
	// redirectUris set (see RegisterUserAppRedirectURI). Wired via
	// SetThunderAdminClient. Optional in tests — without it the
	// register step logs and skips, and the operator must add the URL
	// manually before browser sign-in works for that webapp.
	thunderAdmin thundersvc.Client

	// codingAgentDispatcher, when non-nil, is the WS2.3 proxy-based
	// dispatch path (NS + SA + ExternalSecret×2 + Job). When the
	// dispatcher is wired AND the per-org SM-API triplets are
	// populated, the new path runs and the legacy
	// wfRunService.TriggerCodingAgent is skipped. Both being absent
	// keeps the legacy ClusterWorkflow path live.
	codingAgentDispatcher *codingagent.Dispatcher

	// db backs the SM-API triplet lookup; nil disables the new
	// dispatch path even when codingAgentDispatcher is set. Wired by
	// main.go after WS2.3.
	db *gorm.DB

	// clusterSecretStore is the ESO ClusterSecretStore name the
	// per-run ExternalSecrets target. On DP this is `secretstore-read`;
	// local k3d reuses `default` (see deployments/docker-compose.yml).
	clusterSecretStore string

	// runnerImage is the docker image the per-run Job uses. Pinned by
	// the BFF from cfg.AgentRunnerImage.
	runnerImage string
}

// UserAppsOIDC is the BFF-side mirror of config.UserAppsOIDCConfig —
// declared here to keep the services package independent of config.
//
// InternalProxyPass is the URL the SPA's own nginx writes verbatim as
// the `proxy_pass` target for the same-origin `/oidc/` block. Must be
// reachable from a pod inside the cluster — see UserAppsOIDCConfig.
type UserAppsOIDC struct {
	Issuer            string
	ClientID          string
	Scopes            string
	InternalProxyPass string
}

// SetUserAppsOIDC installs the OIDC config the dispatch path hands to
// user web-apps via issue comments. Call after NewDispatchService.
func (s *dispatchService) SetUserAppsOIDC(cfg UserAppsOIDC) {
	s.userAppsOIDC = cfg
}

// SetThunderAdminClient installs the Thunder REST client used to register
// per-webapp redirect URIs on the user-apps OAuth client. Call after
// NewDispatchService (in production) — when nil, RegisterUserAppRedirectURI
// is a logged no-op and the operator must register URIs by hand.
func (s *dispatchService) SetThunderAdminClient(c thundersvc.Client) {
	s.thunderAdmin = c
}

// WithCodingAgentDispatcher wires the WS2.3 proxy-based dispatch path.
// db is required for the SM-API triplet lookup; clusterSecretStore +
// runnerImage are pinned by the caller. Returns the receiver for
// chained construction.
func (s *dispatchService) WithCodingAgentDispatcher(d *codingagent.Dispatcher, db *gorm.DB, clusterSecretStore, runnerImage string) DispatchService {
	s.codingAgentDispatcher = d
	s.db = db
	s.clusterSecretStore = clusterSecretStore
	s.runnerImage = runnerImage
	return s
}

// DispatchServiceWithTraitSync surfaces the trait_sync setter without
// polluting the public DispatchService interface (parallels the
// DesignServiceWithTaskHook pattern in design_service.go).
type DispatchServiceWithTraitSync interface {
	DispatchService
	SetTraitSync(traitSync *TraitSyncService)
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
	// The resolved URLs are NOT passed through the prompt. The agent
	// receives them via `## Dependency endpoint resolved` comments on
	// its GitHub issue, posted by AnnounceDependencyDeployed when each
	// upstream landed `deployed`. Keeping the prompt thin makes the
	// cluster and local flows read from the same source.
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

	// WS2.3 — new dispatch path. When the proxy-based dispatcher is
	// wired AND the per-org SM-API triplets are populated, dispatch
	// goes through cluster-gateway-proxy (NS + SA + ExternalSecret×2
	// + Job) instead of the legacy ClusterWorkflow path. Fall back to
	// the legacy `wfRunService.TriggerCodingAgent` when the proxy
	// path's prerequisites aren't satisfied — keeps mixed dev
	// environments working until WS2.6 cuts over fully.
	var runName string
	used, runName, err := s.tryDispatchViaProxy(ctx, task, repoInfo.RepoURL, prompt, identity, bearer)
	if err != nil {
		s.markFailed(ctx, task, fmt.Sprintf("dispatch via proxy: %v", err))
		return failResult(res, task.ErrorMessage)
	}
	if !used {
		runName, err = s.wfRunService.TriggerCodingAgent(ctx, CodingAgentTrigger{
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

// tryDispatchViaProxy is WS2.3's proxy-based dispatch attempt. Returns
// (used=true, runName, nil) when dispatch succeeded; (used=false, "", nil)
// when prerequisites aren't met so the caller falls back to the legacy
// ClusterWorkflow path; (used=false, "", err) on actual failure.
//
// Prerequisites:
//   - codingAgentDispatcher wired (cluster-gateway-proxy client present);
//   - db wired (for the SM-API triplet lookup);
//   - runnerImage + clusterSecretStore configured;
//   - the org's anthropic + github credential rows carry the SM-API
//     triplet (populated by WS2.2's Connect flow);
//   - the BFF has an Organization row with the OrgUUID for the NS derivation.
//
// When any of these fail, the function returns used=false with nil
// error and the legacy path runs — operators see a single log line per
// dispatch noting the fallback reason.
func (s *dispatchService) tryDispatchViaProxy(
	ctx context.Context,
	task *models.ComponentTask,
	repoURL, prompt string,
	identity *gitservice.IdentityProjection,
	bearer string,
) (bool, string, error) {
	if s.codingAgentDispatcher == nil || s.db == nil {
		return false, "", nil
	}
	if s.runnerImage == "" || s.clusterSecretStore == "" {
		slog.WarnContext(ctx, "proxy dispatch: missing runnerImage or clusterSecretStore — falling back to legacy path",
			"task", task.ID)
		return false, "", nil
	}

	// SM-API triplets — fetched in one round-trip each from the
	// per-org credential rows. The Connect flow guarantees these are
	// stamped together (in the same tx as the encrypted blob), so a
	// half-populated row is not expected.
	var (
		anthropicRow models.OrgAnthropicCredential
		githubRow    models.OrgCredential
	)
	if err := s.db.WithContext(ctx).Where("oc_org_id = ?", task.OrgID).First(&anthropicRow).Error; err != nil {
		slog.InfoContext(ctx, "proxy dispatch: anthropic row missing; falling back",
			"task", task.ID, "ocOrgId", task.OrgID, "error", err)
		return false, "", nil
	}
	if err := s.db.WithContext(ctx).Where("oc_org_id = ?", task.OrgID).First(&githubRow).Error; err != nil {
		slog.InfoContext(ctx, "proxy dispatch: github row missing; falling back",
			"task", task.ID, "ocOrgId", task.OrgID, "error", err)
		return false, "", nil
	}
	if anthropicRow.SMAPIKVPath == nil || githubRow.SMAPIKVPath == nil {
		slog.InfoContext(ctx, "proxy dispatch: SM-API triplet missing on credential row(s); falling back",
			"task", task.ID,
			"anthropicMissing", anthropicRow.SMAPIKVPath == nil,
			"githubMissing", githubRow.SMAPIKVPath == nil)
		return false, "", nil
	}

	// OrgUUID lookup. The BFF Organization row carries the UUID the
	// NS derivation needs (`wc-<orgUUID8>-<orgHash8>-remote-worker`).
	orgUUID, err := s.lookupOrgUUID(ctx, task.OrgID)
	if err != nil {
		slog.InfoContext(ctx, "proxy dispatch: org UUID not found; falling back",
			"task", task.ID, "ocOrgId", task.OrgID, "error", err)
		return false, "", nil
	}

	runName := codingAgentRunName(task)
	job := codingagent.JobInputs{
		RunName:       runName,
		TaskID:        task.ID,
		OrgID:         task.OrgID,
		ProjectID:     task.ProjectID,
		ComponentName: task.ComponentName,
		RunnerImage:   s.runnerImage,
		RepoURL:       repoURL,
		Prompt:        prompt,
		IdentityName:  identity.Name,
		IdentityEmail: identity.Email,
		IdentityLogin: identity.Login,
		GitServiceURL: s.gitServiceURL,
		CallbackURL:   s.platformURL,
		// `ASDLC_BEARER` keeps the legacy per-task RS256 JWT path alive
		// on the new dispatcher. The runner's `oneshot.ts` validates
		// the env var at startup and uses it for /credentials/refresh
		// callbacks. WS2.4's eventual switch to Thunder
		// `client_credentials` (third per-run ExternalSecret =
		// ThunderClientSR) lets us drop this; for now both paths are
		// load-bearing and we mint the bearer here just like the
		// legacy `wfRunService.TriggerCodingAgent` call does above.
		Bearer: bearer,
	}

	rn, err := s.codingAgentDispatcher.Dispatch(ctx, codingagent.Inputs{
		OrgUUID:                orgUUID,
		Job:                    job,
		AnthropicSR:            codingagent.SecretRef{SecretRefName: derefStr(anthropicRow.SMAPISecretRefName), KVPath: derefStr(anthropicRow.SMAPIKVPath), Property: derefStr(anthropicRow.SMAPIProperty)},
		GitHubSR:               codingagent.SecretRef{SecretRefName: derefStr(githubRow.SMAPISecretRefName), KVPath: derefStr(githubRow.SMAPIKVPath), Property: derefStr(githubRow.SMAPIProperty)},
		ClusterSecretStoreName: s.clusterSecretStore,
	})
	if err != nil {
		return false, "", err
	}
	return true, rn, nil
}

func (s *dispatchService) lookupOrgUUID(ctx context.Context, ocOrgID string) (string, error) {
	var org models.Organization
	if err := s.db.WithContext(ctx).Where("name = ?", ocOrgID).First(&org).Error; err != nil {
		return "", err
	}
	// Prefer the Thunder-issued ouId persisted on `thunder_org_uuid`
	// (the authoritative UUID that SM-API also derives NS from).
	// Fall back to the local PK `uuid` only when the row predates the
	// orgensure lazy-fill — in that case the NS will mismatch and the
	// proxy path will silently fail, but we let it through so legacy
	// callers don't lose dispatch capability mid-rollout.
	if org.ThunderOrgUUID != nil && *org.ThunderOrgUUID != uuid.Nil {
		return org.ThunderOrgUUID.String(), nil
	}
	if org.UUID == uuid.Nil {
		return "", fmt.Errorf("organization %s has no UUID", ocOrgID)
	}
	slog.WarnContext(ctx, "dispatch: thunder_org_uuid missing on org row; falling back to local PK (NS derivation will likely mismatch SM-API)",
		"name", ocOrgID, "uuid", org.UUID.String())
	return org.UUID.String(), nil
}

// codingAgentRunName derives a deterministic run name from the task ID
// + a UTC minute bucket. Same task dispatched twice in the same minute
// reuses the run name (the Job is immutable so ApplyJob does a
// DELETE+POST, restarting the agent). Bucket is intentionally coarse
// so retries within a minute are idempotent.
func codingAgentRunName(task *models.ComponentTask) string {
	min := time.Now().UTC().Format("0601021504")
	shortID := task.ID
	if len(shortID) > 8 {
		shortID = shortID[:8]
	}
	name := fmt.Sprintf("ca-%s-%s", shortID, min)
	if len(name) > 63 {
		name = name[:63]
	}
	return name
}

func derefStr(s *string) string {
	if s == nil {
		return ""
	}
	return *s
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

// AnnounceDependencyDeployed posts a `## Dependency endpoint resolved`
// comment on every dependent task's issue. The comment trail on the issue
// is the single source of truth for upstream URLs — both cluster-flow
// agents (which would otherwise have to receive the URL inside a runtime
// prompt block) and local-flow agents (which only ever see the issue) read
// from the same place. The agent's skill instructs it to pick the most
// recent matching comment per upstream component, so a redeploy with a
// changed URL is self-healing on the next retry.
//
// Best-effort: never returns an error. Per-task failures (issue gone, git
// service unreachable, etc.) are logged and the loop continues; the
// deploy transition that triggered this call has already committed.
func (s *dispatchService) AnnounceDependencyDeployed(ctx context.Context, orgID, projectID, deployedComponent string) {
	if s.componentSvc == nil || s.gitClient == nil || s.taskRepo == nil {
		return
	}
	if s.tokenInject != nil {
		ctx = s.tokenInject(ctx)
	}

	url := s.resolveExternalURL(ctx, orgID, projectID, deployedComponent)
	if url == "" {
		// Don't bail loudly — the same condition is enforced at dispatch
		// time by resolveDependencyEndpoints, where it becomes a §1.3
		// invariant error. Here, with no consumer to fail, just log.
		slog.WarnContext(ctx, "announce dep deployed: no external URL",
			"project", projectID, "deployedComponent", deployedComponent)
		return
	}

	tasks, err := s.taskRepo.ListByProjectID(ctx, orgID, projectID)
	if err != nil {
		slog.WarnContext(ctx, "announce dep deployed: list tasks failed",
			"project", projectID, "error", err)
		return
	}

	body := buildDepEndpointComment(deployedComponent, url)
	posted := 0
	for i := range tasks {
		dependent := &tasks[i]
		if !shouldAnnounceTo(dependent, deployedComponent) {
			continue
		}
		if err := s.gitClient.CommentIssue(ctx, orgID, projectID, dependent.IssueNumber, body); err != nil {
			slog.WarnContext(ctx, "announce dep deployed: comment failed",
				"project", projectID,
				"deployedComponent", deployedComponent,
				"dependent", dependent.ID,
				"issue", dependent.IssueNumber,
				"error", err,
			)
			continue
		}
		posted++
	}
	slog.InfoContext(ctx, "announced dep deployed",
		"project", projectID,
		"deployedComponent", deployedComponent,
		"url", url,
		"dependentsCommented", posted,
	)
}

// RegisterUserAppRedirectURI is the cascade-time hook that registers a
// just-deployed OIDC-SPA web-app's external URL on the user-apps OAuth
// client in Thunder (the redirect_uris set the SPA's /oauth2/authorize
// call is checked against). Without this, browser sign-in fails with
// "Invalid redirect URI" on the first load of the new webapp.
//
// Idempotent on each URI (re-running on an unchanged design is free).
// Best-effort: never returns an error; failures log + skip.
func (s *dispatchService) RegisterUserAppRedirectURI(ctx context.Context, orgID, projectID, componentName string) {
	if s == nil || s.componentSvc == nil || s.store == nil || s.gitClient == nil {
		return
	}
	if s.thunderAdmin == nil {
		slog.DebugContext(ctx, "registerUserAppRedirect: thunder admin client not configured; skipping",
			"project", projectID, "component", componentName)
		return
	}
	if s.userAppsOIDC.ClientID == "" {
		slog.DebugContext(ctx, "registerUserAppRedirect: USER_APPS_OIDC_CLIENT_ID not configured; skipping",
			"project", projectID, "component", componentName)
		return
	}
	if s.tokenInject != nil {
		ctx = s.tokenInject(ctx)
	}
	design, err := s.store.ReadDesign(ctx, orgID, projectID)
	if err != nil || design == nil {
		slog.WarnContext(ctx, "registerUserAppRedirect: read design failed",
			"project", projectID, "component", componentName, "error", err)
		return
	}
	var comp *models.DesignComponent
	for i := range design.Components {
		if design.Components[i].Name == componentName {
			comp = &design.Components[i]
			break
		}
	}
	if comp == nil || comp.ComponentType != "web-app" {
		return
	}
	if comp.Auth == nil || comp.Auth.Kind != "oidc-spa" {
		return
	}
	url := s.resolveExternalURL(ctx, orgID, projectID, componentName)
	if url == "" {
		slog.WarnContext(ctx, "registerUserAppRedirect: no external URL for webapp; skipping",
			"project", projectID, "component", componentName)
		return
	}
	// Append both the origin and origin/callback. Different SPA stacks
	// pick different conventions (Asgardeo SDK uses the origin; the
	// canonical SKILL recipe in this repo uses origin/callback) — we
	// add both to be safe. EnsureRedirectURIs dedupes existing entries.
	// Strip a trailing slash from the origin first — firstExternalURL
	// often returns `http://host:port/` and naive concat yields
	// `http://host:port//callback`, which Thunder treats as a distinct
	// (non-matching) URI from the SPA's `${origin}/callback`.
	origin := strings.TrimRight(url, "/")
	uris := []string{origin, origin + "/callback"}
	added, err := s.thunderAdmin.EnsureRedirectURIs(ctx, s.userAppsOIDC.ClientID, uris)
	if err != nil {
		slog.WarnContext(ctx, "registerUserAppRedirect: thunder update failed",
			"project", projectID, "component", componentName,
			"clientId", s.userAppsOIDC.ClientID, "error", err)
		return
	}
	if added {
		slog.InfoContext(ctx, "registerUserAppRedirect: redirect URIs registered",
			"project", projectID, "component", componentName,
			"clientId", s.userAppsOIDC.ClientID, "uris", uris)
	} else {
		slog.DebugContext(ctx, "registerUserAppRedirect: redirect URIs already present (no-op)",
			"project", projectID, "component", componentName, "uris", uris)
	}
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

// shouldAnnounceTo returns true when the dependent task lists
// deployedComponent as one of its deps and is still in a state where the
// comment is useful — i.e. the agent hasn't yet started its PR's
// verification step. Once the task is past in_progress, its build is
// underway / done and a new dep URL no longer affects the artifact.
func shouldAnnounceTo(dependent *models.ComponentTask, deployedComponent string) bool {
	if dependent == nil || dependent.IssueNumber == 0 {
		return false
	}
	switch models.TaskStatus(dependent.Status) {
	case models.TaskStatusPending,
		models.TaskStatusOnHold,
		models.TaskStatusInProgress:
		// ok — agent hasn't yet opened a non-draft PR
	default:
		return false
	}
	for _, dep := range dependent.DependsOnComponents {
		if dep == deployedComponent {
			return true
		}
	}
	return false
}

// buildDepEndpointComment renders the markdown for a single dep-resolved
// comment. The format is structured enough for the asdlc skill to
// scan-and-reduce comments to "latest URL per upstream component", while
// still readable for a human browsing the issue.
func buildDepEndpointComment(component, url string) string {
	return fmt.Sprintf(
		"## Dependency endpoint resolved\n"+
			"\n"+
			"- **%s**: %s\n"+
			"\n"+
			"Posted by the platform when `%s` reached `deployed`. Bake this URL "+
			"into your component as a build-time constant (Vite/React: "+
			"`VITE_<UPSTREAM>_URL`; other stacks: the idiomatic equivalent). "+
			"If a later comment resolves the same component, use the most recent.",
		component, url, component,
	)
}

// DependencyEndpoint is one row in the legacy dep-prompt block. Kept for
// resolveDependencyEndpoints' return shape — used at dispatch time as a
// §1.3 invariant guard ("every dep this task lists has a non-empty
// external URL"). The actual URL handoff to the agent now goes through
// AnnounceDependencyDeployed → GitHub issue comments, not the prompt.
type DependencyEndpoint struct {
	Component string
	URL       string
}

// buildAgentPrompt returns the user prompt given to the Claude agent. The
// full task context lives in the GitHub issue body
// (services/issue_body.go); the prompt only points the agent at the issue
// and reminds it how to link the PR back. Dependency endpoint URLs come
// through `## Dependency endpoint resolved` comments on the issue, posted
// by AnnounceDependencyDeployed when each upstream lands `deployed` —
// not through this prompt. Keeping the prompt thin means the cluster
// flow (this prompt) and the local flow (no prompt; user types something
// like "work on issue #N") read URLs from the same place.
//
// The asdlc skill loaded in the runner image carries the rest of the
// workflow (read the issue + its comments, harvest dep URLs, bake them
// in, verify before PR, recovery).
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
	// Phase 2 — derive the `api-configuration` trait from design.md's
	// optional `api.security` block. nil/none ⇒ no trait, no AP hop;
	// `required` ⇒ trait attached with cors+jwtAuth enabled in every env.
	// See services/trait_sync.go for the canonical emitter.
	//
	// Gated on FEATURE_EMIT_API_TRAIT — when off, both `apiSecurityEnabled`
	// and `traits` stay zero-valued so the resulting Component is
	// bit-identical to the pre-Phase-2 baseline (covered by
	// tests/api/baseline_diff_test.go).
	var apiSecurityEnabled bool
	var traits []models.ComponentTrait
	if s.traitSync != nil && s.traitSync.Enabled() {
		apiSecurityEnabled = ResolveAPISecurityEnabled(*comp)
		traits, _ = DesiredAPIConfigurationTrait(componentName, apiSecurityEnabled)
	}

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

	// Best-effort: post the `## OIDC client provisioned` comment on the
	// issue for web-apps whose design has auth.kind=oidc-spa. The coding
	// agent reads this comment (per the asdlc SKILL's OIDC-SPA section)
	// and bakes the values into the SPA's workload.yaml env block.
	s.announceOIDCConfigIfApplicable(ctx, task, comp)

	return nil
}

// announceOIDCConfigIfApplicable posts a `## OIDC client provisioned`
// comment on the task's issue when (a) the component is a web-app, (b)
// its design has auth.kind = "oidc-spa", and (c) the BFF has
// USER_APPS_OIDC_CLIENT_ID configured. Idempotency: re-running dispatch
// re-posts the comment; the agent uses the most recent matching comment,
// so duplicates are harmless. Best-effort — failures log and don't bubble.
func (s *dispatchService) announceOIDCConfigIfApplicable(ctx context.Context, task *models.ComponentTask, comp *models.DesignComponent) {
	if comp == nil || comp.ComponentType != "web-app" {
		return
	}
	if comp.Auth == nil || comp.Auth.Kind != "oidc-spa" {
		return
	}
	if s.userAppsOIDC.ClientID == "" {
		slog.WarnContext(ctx, "announceOIDC: USER_APPS_OIDC_CLIENT_ID not configured; skipping comment",
			"task", task.ID, "component", task.ComponentName)
		return
	}
	if task.IssueNumber == 0 {
		return
	}
	body := buildOIDCCommentBody(s.userAppsOIDC)
	if err := s.gitClient.CommentIssue(ctx, task.OrgID, task.ProjectID, task.IssueNumber, body); err != nil {
		slog.WarnContext(ctx, "announceOIDC: comment failed",
			"task", task.ID, "component", task.ComponentName, "error", err)
		return
	}
	slog.InfoContext(ctx, "announceOIDC: comment posted",
		"task", task.ID, "component", task.ComponentName,
		"clientId", s.userAppsOIDC.ClientID, "issuer", s.userAppsOIDC.Issuer,
	)
}

// buildOIDCCommentBody renders the markdown for a `## OIDC client
// provisioned` comment. The format mirrors `## Dependency endpoint
// resolved` so the asdlc SKILL can scan-and-reduce both comment kinds
// the same way. Kept structured enough for the agent to parse
// confidently while staying readable for a human reviewer.
//
// `host` is derived from the Issuer URL (hostname:port stripped of scheme).
// It is needed by the SPA's nginx `/oidc/` same-origin proxy block, which
// sets `proxy_set_header Host ${OIDC_HOST}` so Thunder sees its own
// canonical hostname (kgateway routes by Host header).
func buildOIDCCommentBody(cfg UserAppsOIDC) string {
	host := cfg.Issuer
	if u, err := url.Parse(cfg.Issuer); err == nil && u.Host != "" {
		host = u.Host
	}
	return fmt.Sprintf(
		"## OIDC client provisioned\n"+
			"\n"+
			"- **issuer**: %s\n"+
			"- **clientId**: %s\n"+
			"- **scopes**: %s\n"+
			"- **host**: %s\n"+
			"- **internalProxyPass**: %s\n"+
			"\n"+
			"Bake the first four values into `<appPath>/.env` BEFORE "+
			"`npm run build` as `VITE_OIDC_ISSUER`, "+
			"`VITE_OIDC_CLIENT_ID`, `VITE_OIDC_SCOPES`, "+
			"`VITE_OIDC_HOST` (or the framework's equivalent "+
			"prefix — CRA `REACT_APP_*`, Next `NEXT_PUBLIC_*`). "+
			"Add `VITE_API_BASE_URL` from the upstream's "+
			"`## Dependency endpoint resolved` comment. Read them "+
			"via `import.meta.env.VITE_*` and throw at module "+
			"top-level on missing — no silent `?? ''` fallback. "+
			"ALSO write the `internalProxyPass` value above as the "+
			"literal `proxy_pass` target in `nginx/default.conf` for "+
			"the same-origin `/oidc/` proxy (it routes `/oidc/token` "+
			"to Thunder's `/oauth2/token`, bypassing a kgateway "+
			"CORS bug). The `internalProxyPass` is an in-cluster "+
			"Service FQDN — the public `issuer` hostname does NOT "+
			"resolve from inside a pod, so DO NOT use `${issuer}/oauth2/` "+
			"as the `proxy_pass` target (nginx will fail to start with "+
			"\"host not found in upstream\"). The redirect URI is "+
			"`window.location.origin + '/callback'`. DO NOT use "+
			"`workload.yaml` `configurations.env`, nginx "+
			"envsubst, `/etc/nginx/templates/`, `/env-config.js`, "+
			"or `window.__ENV__` — those runtime mechanisms are "+
			"deprecated. See the `asdlc` SKILL's OIDC-SPA section "+
			"for the reference `.env`, `nginx/default.conf`, and "+
			"`src/auth.ts`.",
		cfg.Issuer, cfg.ClientID, cfg.Scopes, host, cfg.InternalProxyPass,
	)
}
