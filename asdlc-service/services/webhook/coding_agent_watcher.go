package webhook

import (
	"context"
	"log/slog"
	"time"

	"gorm.io/gorm"

	"github.com/wso2/asdlc/asdlc-service/clients/openchoreo"
	"github.com/wso2/asdlc/asdlc-service/models"
	"github.com/wso2/asdlc/asdlc-service/services"
)

// CodingAgentWatcher polls OC for the status of in-flight coding-agent
// WorkflowRuns and applies coding_agent.failed via the projector when a
// run terminates Failed/Error. Mirrors BuildWatcher's shape (10s sweep,
// FOR UPDATE SKIP LOCKED, multi-replica safe).
//
// Why polling: a goroutine that owns a single WorkflowRun dies on BFF
// restart, leaving the task stuck `in_progress`. A periodic sweep is
// restart-safe — the next tick picks up every `in_progress` task with
// LastCodingAgentRunName set.
//
// Success path is webhook-driven: the agent runs `gh pr ready` from inside
// the pod; that fires `pull_request:ready_for_review` which the projector
// maps to in_progress → ready_for_review. The watcher is purely the
// failure-detection complement to that path. Successful WorkflowRuns are
// observed but not acted on (the pod exited 0; the webhook will arrive
// shortly or has already arrived).
type CodingAgentWatcher struct {
	db          *gorm.DB
	ocClient    openchoreo.ComponentClient
	projector   *Projector
	tokenInject func(ctx context.Context) context.Context
	tick        time.Duration
}

func NewCodingAgentWatcher(
	db *gorm.DB,
	ocClient openchoreo.ComponentClient,
	projector *Projector,
	tokenInject func(ctx context.Context) context.Context,
) *CodingAgentWatcher {
	return &CodingAgentWatcher{
		db:          db,
		ocClient:    ocClient,
		projector:   projector,
		tokenInject: tokenInject,
		tick:        10 * time.Second,
	}
}

// Run blocks until ctx is cancelled, polling at the configured cadence.
func (w *CodingAgentWatcher) Run(ctx context.Context) {
	ticker := time.NewTicker(w.tick)
	defer ticker.Stop()
	slog.InfoContext(ctx, "coding-agent watcher started", "tick", w.tick)
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			w.sweep(ctx)
		}
	}
}

func (w *CodingAgentWatcher) sweep(ctx context.Context) {
	if w.tokenInject != nil {
		ctx = w.tokenInject(ctx)
	}

	var batch []models.ComponentTask
	err := w.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		return tx.Raw(`
			SELECT * FROM component_tasks
			WHERE status IN (?, ?) AND last_coding_agent_run_name <> ''
			ORDER BY last_event_at NULLS FIRST
			LIMIT 50
			FOR UPDATE SKIP LOCKED
		`, string(models.TaskStatusInProgress), string(models.TaskStatusTesting)).
			Scan(&batch).Error
	})
	if err != nil {
		slog.ErrorContext(ctx, "coding-agent watcher: select batch failed", "error", err)
		return
	}
	if len(batch) == 0 {
		return
	}

	for i := range batch {
		t := &batch[i]
		run, err := w.ocClient.GetWorkflowRun(ctx, t.OrgID, t.LastCodingAgentRunName)
		if err != nil {
			// Not fatal — try again next tick. Common during the brief
			// window between WorkflowRun create and the controller
			// materialising it.
			slog.DebugContext(ctx, "coding-agent watcher: get run failed",
				"task", t.ID, "run", t.LastCodingAgentRunName, "error", err)
			continue
		}
		event, errMsg := classifyCodingAgentRun(run)
		if event == "" {
			// Still in-flight, or succeeded (success rides the GitHub
			// pr.ready_for_review webhook).
			continue
		}
		if err := w.projector.ApplyBuildResult(ctx, t.ID, event, errMsg); err != nil {
			slog.ErrorContext(ctx, "coding-agent watcher: apply result failed",
				"task", t.ID, "event", event, "error", err)
			continue
		}
		slog.WarnContext(ctx, "coding-agent watcher: applied terminal failure",
			"task", t.ID, "run", t.LastCodingAgentRunName, "status", run.Status)
	}
}

// classifyCodingAgentRun maps OC WorkflowRun status to a TaskEvent. Terminal
// state is gated on `run.Completed` — the
// Status.Conditions[type=WorkflowCompleted, status=True] signal. Returns
// empty event for in-flight runs and for succeeded runs (success transitions
// via the GitHub webhook). Only terminal-failed maps to a transition.
func classifyCodingAgentRun(run *models.WorkflowRun) (event services.TaskEvent, errMsg string) {
	if run == nil || !run.Completed {
		return "", ""
	}
	if run.Status != openchoreo.ReasonWorkflowSucceeded {
		return services.TaskEventCodingAgentFailed, "coding-agent failed: " + run.Status
	}
	return "", ""
}
