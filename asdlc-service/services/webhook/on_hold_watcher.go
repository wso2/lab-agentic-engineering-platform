package webhook

import (
	"context"
	"log/slog"
	"time"

	"gorm.io/gorm"

	"github.com/wso2/asdlc/asdlc-service/models"
	"github.com/wso2/asdlc/asdlc-service/services"
)

// OnHoldWatcher retries dispatch for tasks that are on_hold because a
// dependency's external URL was not yet resolved by the OC ReleaseBinding
// controller at cascade time. The cascade fires immediately when a dep
// reaches `deployed`, but the ingress/gateway URL can take seconds to a
// minute to appear in ReleaseBinding.Status — causing a transient failure
// in resolveDependencyEndpoints. dispatchOne reverts those tasks to on_hold
// (rather than permanently failing them) and sets DispatchDeferredAt.
//
// This watcher sweeps every 10s, finds projects with on_hold tasks, and
// calls DispatchTasks — which re-evaluates the URL. If it's now available
// the task dispatches; if still missing and within the 2-minute deadline it
// stays on_hold for the next tick; after the deadline dispatchOne fails it.
//
// Multi-replica safe: FOR UPDATE SKIP LOCKED ensures two BFF replicas don't
// process the same project simultaneously.
type OnHoldWatcher struct {
	db       *gorm.DB
	dispatch services.DispatchService
	tick     time.Duration
}

func NewOnHoldWatcher(db *gorm.DB, dispatch services.DispatchService) *OnHoldWatcher {
	return &OnHoldWatcher{
		db:       db,
		dispatch: dispatch,
		tick:     10 * time.Second,
	}
}

// Run blocks until ctx is cancelled, polling at the configured cadence.
// Spawned as a goroutine from main.
func (w *OnHoldWatcher) Run(ctx context.Context) {
	ticker := time.NewTicker(w.tick)
	defer ticker.Stop()
	slog.InfoContext(ctx, "on_hold watcher started", "tick", w.tick)
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			w.sweep(ctx)
		}
	}
}

// projectKey is a deduplication key for (orgID, projectID) pairs.
type projectKey struct {
	orgID     string
	projectID string
}

func (w *OnHoldWatcher) sweep(ctx context.Context) {
	// Collect distinct (org_id, project_id) pairs with on_hold tasks whose
	// deps are all deployed — i.e., dispatch_deferred_at IS NOT NULL (these
	// are the timing-race cases, not tasks waiting on undeployed deps).
	// FOR UPDATE SKIP LOCKED prevents concurrent BFF replicas from
	// double-processing the same project.
	var rows []models.ComponentTask
	err := w.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		return tx.Raw(`
			SELECT org_id, project_id
			FROM component_tasks
			WHERE status = ? AND dispatch_deferred_at IS NOT NULL
			FOR UPDATE SKIP LOCKED
		`, string(models.TaskStatusOnHold)).Scan(&rows).Error
	})
	if err != nil {
		slog.ErrorContext(ctx, "on_hold watcher: select batch failed", "error", err)
		return
	}
	if len(rows) == 0 {
		return
	}

	seen := make(map[projectKey]struct{}, len(rows))
	for _, r := range rows {
		key := projectKey{orgID: r.OrgID, projectID: r.ProjectID}
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}

		results, err := w.dispatch.DispatchTasks(ctx, r.OrgID, r.ProjectID)
		if err != nil {
			slog.WarnContext(ctx, "on_hold watcher: DispatchTasks failed",
				"org", r.OrgID, "project", r.ProjectID, "error", err)
			continue
		}
		if len(results) > 0 {
			slog.InfoContext(ctx, "on_hold watcher: dispatched deferred tasks",
				"org", r.OrgID, "project", r.ProjectID, "count", len(results))
		}
	}
}
