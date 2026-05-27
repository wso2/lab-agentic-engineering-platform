package webhook

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"gorm.io/gorm"

	"github.com/wso2/asdlc/asdlc-service/services"
)

// TraitSyncWatcher is the convergence safety net for Phase 2 of the api-
// platform-integration plan (docs/design/api-platform-integration.md §6
// Phase 2). It runs on a 10 s ticker and, for every (orgID, projectID,
// componentName) tuple with an existing task record, invokes
// services.TraitSyncService.SyncComponentTraits — which is idempotent +
// convergent so re-running it is safe.
//
// Why this matters:
//   - The dispatch path emits the trait at CreateComponent time, but a
//     write-write race with a concurrent design PUT (or a transient OC
//     500 on the per-RB PATCH) can leave the cluster temporarily out of
//     sync. The watcher closes that window within one tick.
//   - The plan also calls for a bidirectional `trait_sync drift` metric
//     (`direction=missing_protection` and `direction=stale_protection`).
//     For now we log when a reconcile would change state, leaving the
//     Prometheus counter integration as a follow-up. The reconcile path
//     itself is the only writer of `traitEnvironmentConfigs.<inst>` so
//     observability already lives in the BFF log stream:
//     `trait_sync: reconciled` events.
//
// Per-component retry budget: when SyncComponentTraits returns a non-nil
// error for the same (orgID, projectID, componentName) for 5 consecutive
// sweeps, the watcher pauses that component for 5 min. This stops a
// single pathologically broken design from pinning the watcher's budget
// on every sweep. Recovery is automatic — the pause clears, the watcher
// retries, and the failure counter starts over.
type TraitSyncWatcher struct {
	db          *gorm.DB
	traitSync   *services.TraitSyncService
	tokenInject func(ctx context.Context) context.Context
	tick        time.Duration

	// failureBudget — max consecutive failures before pausing a tuple.
	failureBudget int
	// pauseFor — how long to skip a tuple after exhausting the budget.
	pauseFor time.Duration

	muFailures sync.Mutex
	failures   map[string]*tupleFailure
}

type tupleFailure struct {
	consecutive int
	pausedUntil time.Time
}

// NewTraitSyncWatcher builds a watcher. tokenInject is optional — when
// non-nil it adds the BFF's service-credential token to outbound OC
// calls.
func NewTraitSyncWatcher(
	db *gorm.DB,
	traitSync *services.TraitSyncService,
	tokenInject func(ctx context.Context) context.Context,
) *TraitSyncWatcher {
	return &TraitSyncWatcher{
		db:            db,
		traitSync:     traitSync,
		tokenInject:   tokenInject,
		tick:          10 * time.Second,
		failureBudget: 5,
		pauseFor:      5 * time.Minute,
		failures:      make(map[string]*tupleFailure),
	}
}

// Run blocks until ctx is cancelled. Spawned as a goroutine from main.
func (w *TraitSyncWatcher) Run(ctx context.Context) {
	if w.traitSync == nil {
		slog.InfoContext(ctx, "trait_sync watcher: traitSync nil; not starting")
		return
	}
	ticker := time.NewTicker(w.tick)
	defer ticker.Stop()
	slog.InfoContext(ctx, "trait_sync watcher started",
		"tick", w.tick,
		"failureBudget", w.failureBudget,
		"pauseFor", w.pauseFor,
	)
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			w.sweep(ctx)
		}
	}
}

func (w *TraitSyncWatcher) sweep(ctx context.Context) {
	if w.tokenInject != nil {
		ctx = w.tokenInject(ctx)
	}

	// Enumerate distinct (orgID, projectID, componentName) tuples from
	// the task table. Every dispatched component leaves at least one
	// row, so this catches every project that has an OC Component CR
	// the watcher could reconcile. Projects with a design but no
	// dispatch yet are deliberately skipped — there's nothing to drift.
	type tuple struct {
		OrgID         string
		ProjectID     string
		ComponentName string
	}
	var tuples []tuple
	if err := w.db.WithContext(ctx).Raw(`
		SELECT DISTINCT org_id, project_id, component_name
		FROM component_tasks
		WHERE org_id <> '' AND project_id <> '' AND component_name <> ''
	`).Scan(&tuples).Error; err != nil {
		slog.WarnContext(ctx, "trait_sync watcher: failed to enumerate tuples", "error", err)
		return
	}

	for _, t := range tuples {
		// SyncComponentTraits expects the k8s-shaped component name —
		// services.toK8sName isn't exported, so the watcher mirrors the
		// transform the dispatch path uses. component_tasks rows carry
		// the user-friendly name; we lower / strip to match.
		k8sName := services.ToK8sName(t.ComponentName)
		w.reconcileOne(ctx, t.OrgID, t.ProjectID, k8sName)
	}
}

func (w *TraitSyncWatcher) reconcileOne(ctx context.Context, orgID, projectID, componentName string) {
	key := orgID + "/" + projectID + "/" + componentName
	if w.isPaused(key) {
		return
	}
	if err := w.traitSync.SyncComponentTraits(ctx, orgID, projectID, componentName); err != nil {
		w.recordFailure(ctx, key, err)
		return
	}
	w.clearFailure(key)
}

func (w *TraitSyncWatcher) isPaused(key string) bool {
	w.muFailures.Lock()
	defer w.muFailures.Unlock()
	state, ok := w.failures[key]
	if !ok {
		return false
	}
	if state.pausedUntil.IsZero() {
		return false
	}
	if time.Now().Before(state.pausedUntil) {
		return true
	}
	// Pause window expired — clear so the next sweep retries.
	delete(w.failures, key)
	return false
}

func (w *TraitSyncWatcher) recordFailure(ctx context.Context, key string, err error) {
	w.muFailures.Lock()
	state, ok := w.failures[key]
	if !ok {
		state = &tupleFailure{}
		w.failures[key] = state
	}
	state.consecutive++
	exhausted := state.consecutive >= w.failureBudget
	if exhausted {
		state.pausedUntil = time.Now().Add(w.pauseFor)
	}
	w.muFailures.Unlock()

	if exhausted {
		slog.WarnContext(ctx, "trait_sync watcher: pausing tuple after consecutive failures",
			"tuple", key,
			"consecutive", state.consecutive,
			"pauseFor", w.pauseFor,
			"error", err,
		)
	} else {
		slog.DebugContext(ctx, "trait_sync watcher: reconcile failed; will retry next tick",
			"tuple", key,
			"consecutive", state.consecutive,
			"error", err,
		)
	}
}

func (w *TraitSyncWatcher) clearFailure(key string) {
	w.muFailures.Lock()
	defer w.muFailures.Unlock()
	delete(w.failures, key)
}
