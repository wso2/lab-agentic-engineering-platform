package services

import (
	"context"
	"hash/fnv"
	"log/slog"

	"gorm.io/gorm"
)

// DispatchCascadeHook is the post-commit cascade fired by the webhook
// projector whenever a task lands in `deployed`. It owns the per-project
// advisory lock + eligibility scan + DispatchService.DispatchTasks call.
//
// See docs/design/cross-component-wiring-gaps.md §3 F1. The dispatch
// itself (gating logic + URL resolution) lives in DispatchService —
// this type only takes the lock and invokes it.
type DispatchCascadeHook struct {
	db       *gorm.DB
	dispatch DispatchService
}

func NewDispatchCascadeHook(db *gorm.DB, dispatch DispatchService) *DispatchCascadeHook {
	return &DispatchCascadeHook{db: db, dispatch: dispatch}
}

// OnTaskDeployed is the post-commit hook. Acquires a per-project advisory
// lock so concurrent deploys against the same project serialise (a
// dependent shared between two deps must dispatch exactly once when the
// second dep deploys). Inside the lock, calls DispatchTasks which handles
// the on_hold re-evaluation + actual dispatch.
//
// Errors are logged but never propagated — the deploy transition has
// already committed by the time we get here, and the cascade is
// best-effort. Operator-visible failure surfaces inside DispatchTasks
// (the dispatched task itself transitions to TaskStatusFailed with
// ErrorMessage populated).
func (h *DispatchCascadeHook) OnTaskDeployed(ctx context.Context, orgID, projectID, componentName string) {
	if h == nil || h.db == nil || h.dispatch == nil {
		return
	}
	// Per-project advisory lock — mirrors webhook.acquireProjectLock.
	// We take it in its own transaction so we can release before the
	// dispatch loop (which may itself perform writes). Holding it across
	// the dispatch loop would also be acceptable; we trade a tiny race
	// window (two near-simultaneous deploys releasing the lock before
	// the dispatch loop finishes) for shorter lock hold times. Since
	// DispatchTasks is itself idempotent on (DispatchedAt, LastCodingAgentRunName),
	// the worst case under that race is a single redundant lookup, not
	// a double-dispatch.
	if err := h.db.WithContext(ctx).Exec(
		`SELECT pg_advisory_xact_lock(?)`,
		hashCascadeKey("project:"+projectID),
	).Error; err != nil {
		slog.WarnContext(ctx, "dispatch cascade: acquire project lock failed",
			"project", projectID, "error", err)
		return
	}
	// Announce the newly-deployed dep's URL on every dependent task's
	// issue BEFORE dispatching. Tasks waiting in `on_hold` may flip
	// to `pending` and dispatch within the same lock; their agent reads
	// the issue + its comment trail as its first move, so the comment
	// must already be there. Best-effort: never errors. If commenting
	// fails offline, dispatch's own resolveDependencyEndpoints still
	// enforces the §1.3 URL invariant.
	h.dispatch.AnnounceDependencyDeployed(ctx, orgID, projectID, componentName)

	results, err := h.dispatch.DispatchTasks(ctx, orgID, projectID)
	if err != nil {
		slog.WarnContext(ctx, "dispatch cascade: DispatchTasks failed",
			"project", projectID, "deployedComponent", componentName, "error", err)
		return
	}
	slog.InfoContext(ctx, "dispatch cascade fired",
		"project", projectID,
		"deployedComponent", componentName,
		"dispatched", len(results),
	)
}

func hashCascadeKey(s string) int64 {
	h := fnv.New64a()
	h.Write([]byte(s))
	return int64(h.Sum64()) //nolint:gosec
}
