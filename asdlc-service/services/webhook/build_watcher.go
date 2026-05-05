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

// classifyRun maps an OC WorkflowRun status to a TaskEvent. Mirrors the
// agent-manager precedence (Completed > Succeeded/Failed > Running). The
// OC normalize layer reduces the conditions list to a Status string —
// for completed runs that's the WorkflowCompleted condition's Reason
// (e.g. "WorkflowSucceeded", "WorkflowFailed"), for in-progress runs
// it's "Running", and on a fresh run with no conditions yet it's
// "Pending". Match all three families.
//
// Phase 2 PR D §9.3 — when the run failed AND the checkout-source task's
// outputs match an authFailureMarker, returns event="" + authFailure=true
// so the watcher routes through the retry path instead of the terminal
// failed path.
func classifyRun(run *models.WorkflowRun) (event services.TaskEvent, errMsg string, authFailure bool) {
	if run == nil {
		return "", "", false
	}
	s := strings.ToLower(run.Status)
	if strings.Contains(s, "succeeded") || strings.Contains(s, "completed") {
		return services.TaskEventBuildSucceeded, "", false
	}
	if strings.Contains(s, "failed") || strings.Contains(s, "error") {
		if isGitCloneAuthFailure(run) {
			return "", "", true
		}
		return services.TaskEventBuildFailed, "build failed: " + run.Status, false
	}
	return "", "", false
}

// isGitCloneAuthFailure returns true when the failed run's checkout-source
// task output (or any task output, as a fallback) carries one of the
// well-known git-clone auth failure substrings.
func isGitCloneAuthFailure(run *models.WorkflowRun) bool {
	for _, task := range run.Tasks {
		for _, val := range task.Outputs {
			for _, marker := range authFailureMarkers {
				if strings.Contains(val, marker) {
					return true
				}
			}
		}
	}
	return false
}
