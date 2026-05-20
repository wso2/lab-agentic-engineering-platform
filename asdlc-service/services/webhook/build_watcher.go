package webhook

import (
	"context"
	"log/slog"
	"strings"
	"time"

	"gorm.io/gorm"

	"github.com/wso2/asdlc/asdlc-service/clients/openchoreo"
	"github.com/wso2/asdlc/asdlc-service/models"
	"github.com/wso2/asdlc/asdlc-service/services"
)

// BuildWatcher polls OC for the status of in-flight builds and applies
// build.{succeeded,failed} via the projector.
//
// Why polling instead of per-build goroutines: a goroutine that owns a
// single build dies on BFF restart, leaving the task stuck in `building`.
// A periodic sweep is restart-safe by construction — the next tick picks
// up every `building` task with no in-memory state. Multi-replica safe via
// FOR UPDATE SKIP LOCKED so two BFF replicas don't double-poll the same
// task.
//
// Cadence is 10 s — agent-manager precedent (clients/openchoreosvc/client/
// builds.go uses similar timing) and a fine starting point per
// github-integration-phase0.md §15 q2.
//
// Phase 2 PR D §9.3 — git_clone_failed_auth retry budget. When classifyRun
// detects a git-clone auth failure, the watcher mints a fresh build token
// via WorkflowRunService.RetryAuthFailedBuild and recreates the run for the
// same SHA up to authRetryBudget times. Budget exhaustion → terminal
// failed with cause "build.auth_retry_exceeded".
type BuildWatcher struct {
	db          *gorm.DB
	ocClient    openchoreo.ComponentClient
	projector   *Projector
	tokenInject func(ctx context.Context) context.Context
	wfService   services.WorkflowRunService
	tick        time.Duration
	authBudget  int
}

func NewBuildWatcher(
	db *gorm.DB,
	ocClient openchoreo.ComponentClient,
	projector *Projector,
	tokenInject func(ctx context.Context) context.Context,
	wfService services.WorkflowRunService,
	authBudget int,
) *BuildWatcher {
	if authBudget <= 0 {
		authBudget = 3 // phase2.md §9.3
	}
	return &BuildWatcher{
		db:          db,
		ocClient:    ocClient,
		projector:   projector,
		tokenInject: tokenInject,
		wfService:   wfService,
		tick:        10 * time.Second,
		authBudget:  authBudget,
	}
}

// Run blocks until ctx is cancelled, polling at the configured cadence.
// Spawned as a goroutine from main.
func (w *BuildWatcher) Run(ctx context.Context) {
	ticker := time.NewTicker(w.tick)
	defer ticker.Stop()
	slog.InfoContext(ctx, "build watcher started", "tick", w.tick, "authBudget", w.authBudget)
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			w.sweep(ctx)
		}
	}
}

func (w *BuildWatcher) sweep(ctx context.Context) {
	if w.tokenInject != nil {
		ctx = w.tokenInject(ctx)
	}

	// Acquire a batch of `building` tasks under FOR UPDATE SKIP LOCKED so
	// concurrent BFF replicas split the work without coordination.
	var batch []models.ComponentTask
	err := w.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		return tx.Raw(`
			SELECT * FROM component_tasks
			WHERE status = ? AND last_build_run_name <> ''
			ORDER BY last_event_at NULLS FIRST
			LIMIT 50
			FOR UPDATE SKIP LOCKED
		`, string(models.TaskStatusBuilding)).
			Scan(&batch).Error
	})
	if err != nil {
		slog.ErrorContext(ctx, "build watcher: select batch failed", "error", err)
		return
	}
	if len(batch) == 0 {
		return
	}

	for i := range batch {
		t := &batch[i]
		run, err := w.ocClient.GetWorkflowRun(ctx, t.OrgID, t.LastBuildRunName)
		if err != nil {
			// Not fatal for the sweep — try again on the next tick.
			slog.WarnContext(ctx, "build watcher: get run failed",
				"task", t.ID, "run", t.LastBuildRunName, "error", err)
			continue
		}
		event, errMsg, authFailure := classifyRun(run)
		if event == "" && !authFailure {
			continue // still pending or running — leave for next tick
		}

		// Phase 2 PR D §9.3 — git-clone auth-failure retry path.
		// On classification, increment the budget counter; if still
		// within budget, mint a fresh token and recreate the run for
		// the same SHA. On exhaustion, transition to failed with the
		// dedicated cause.
		if authFailure && w.wfService != nil {
			if t.BuildAuthRetryCount < w.authBudget {
				w.handleAuthRetry(ctx, t)
				continue
			}
			// Budget exhausted — terminal failure.
			if err := w.projector.ApplyBuildResult(ctx, t.ID, services.TaskEventBuildAuthRetryExhausted, "build auth retry budget exceeded"); err != nil {
				slog.ErrorContext(ctx, "build watcher: apply auth-budget exhaustion failed",
					"task", t.ID, "error", err)
			} else {
				slog.WarnContext(ctx, "build watcher: auth retry budget exhausted",
					"task", t.ID, "attempts", t.BuildAuthRetryCount, "budget", w.authBudget)
			}
			continue
		}
		if event == "" {
			continue
		}
		if err := w.projector.ApplyBuildResult(ctx, t.ID, event, errMsg); err != nil {
			slog.ErrorContext(ctx, "build watcher: apply result failed",
				"task", t.ID, "event", event, "error", err)
			continue
		}
	}
}

// handleAuthRetry increments the retry counter, mints a fresh token,
// and recreates the WorkflowRun for the same SHA. Errors leave the
// counter incremented — the watcher's next sweep observes the same
// failed run, increments again, and eventually exhausts the budget.
// Successful retry advances LastBuildRunName so the watcher tracks the
// new run.
func (w *BuildWatcher) handleAuthRetry(ctx context.Context, t *models.ComponentTask) {
	newRun, err := w.wfService.RetryAuthFailedBuild(ctx, t)
	if err != nil {
		slog.ErrorContext(ctx, "build watcher: auth retry mint+trigger failed",
			"task", t.ID, "attempt", t.BuildAuthRetryCount+1, "error", err)
		// Increment anyway so a stuck mint failure exhausts the budget.
		_ = w.db.WithContext(ctx).Model(&models.ComponentTask{}).
			Where("id = ?", t.ID).
			Update("build_auth_retry_count", gorm.Expr("build_auth_retry_count + 1")).Error
		return
	}
	// Advance the run name + counter atomically.
	if err := w.db.WithContext(ctx).Model(&models.ComponentTask{}).
		Where("id = ?", t.ID).
		Updates(map[string]any{
			"build_auth_retry_count": gorm.Expr("build_auth_retry_count + 1"),
			"last_build_run_name":    newRun,
		}).Error; err != nil {
		slog.ErrorContext(ctx, "build watcher: persist retry state failed",
			"task", t.ID, "newRun", newRun, "error", err)
		return
	}
	slog.InfoContext(ctx, "build watcher: re-minted token + re-created build",
		"task", t.ID, "newRun", newRun, "attempt", t.BuildAuthRetryCount+1)
}

// authFailureMarkers are substring matches for the well-known
// git-clone-step failure outputs. A failed `checkout-source` task with
// any of these substrings in its outputs is classified as a transient
// auth failure and retried. Conservative — explicit substrings rather
// than regex — so an unrelated failure mode doesn't accidentally trigger
// the retry budget.
var authFailureMarkers = []string{
	"fatal: Authentication failed",
	"fatal: could not read Username",
	"could not read password",
	"unable to access ",       // git's HTTP 403 prefix
	"the requested URL returned error: 401",
	"the requested URL returned error: 403",
}

// classifyRun maps an OC WorkflowRun status to a TaskEvent. Terminal-state
// decision is gated on `run.Completed` — the canonical
// Status.Conditions[type=WorkflowCompleted, status=True] signal from the OC
// controller (controller_conditions.go:151-152). The Status string is the
// WorkflowCompleted condition's Reason (lifted by normalizeWorkflowRun);
// it is one of openchoreo.ReasonWorkflowSucceeded / ReasonWorkflowFailed
// when terminal.
//
// Phase 2 PR D §9.3 — when the run failed AND the checkout-source task's
// Phase ∈ {Failed, Error} with a Message matching an auth marker, returns
// event="" + authFailure=true so the watcher routes through the retry path
// instead of the terminal failed path.
func classifyRun(run *models.WorkflowRun) (event services.TaskEvent, errMsg string, authFailure bool) {
	if run == nil || !run.Completed {
		return "", "", false
	}
	if run.Status == openchoreo.ReasonWorkflowSucceeded {
		return services.TaskEventBuildSucceeded, "", false
	}
	// Anything not Succeeded once Completed=True is a failure.
	if isGitCloneAuthFailure(run) {
		return "", "", true
	}
	return services.TaskEventBuildFailed, "build failed: " + run.Status, false
}

// isGitCloneAuthFailure returns true when the failing checkout-source task
// matches a well-known git-clone auth marker. Drop-in replacement for the
// previous Outputs-iterating version per
// docs/design/auth-failure-classification.md §3 — OC's CRD does not expose
// per-task outputs, so the previous implementation was always false. We
// match on Tasks[].Phase ∈ {Failed, Error} on the `checkout-source` step
// AND a substring of Tasks[].Message. Fallback to OC `/logs?task=` for
// empty Messages is not yet wired (deferred — see §3.1 of the doc); when
// Message is empty we return false conservatively (a non-auth failure is
// safer than a runaway retry budget).
func isGitCloneAuthFailure(run *models.WorkflowRun) bool {
	for _, task := range run.Tasks {
		if task.Name != "checkout-source" {
			continue
		}
		if task.Phase != "Failed" && task.Phase != "Error" {
			continue
		}
		for _, marker := range authFailureMarkers {
			if strings.Contains(task.Message, marker) {
				return true
			}
		}
		return false
	}
	return false
}
