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
	db            *gorm.DB
	dispatch      DispatchService
	traitSync     *TraitSyncService
	runtimeConfig *RuntimeConfigService
}

func NewDispatchCascadeHook(db *gorm.DB, dispatch DispatchService) *DispatchCascadeHook {
	return &DispatchCascadeHook{db: db, dispatch: dispatch}
}

// SetTraitSync wires the trait sync service so the cascade can re-emit
// sibling-CORS origins on every protected API in the project when a SPA
// lands deployed. Optional — when nil the cascade skips the re-emit step.
func (h *DispatchCascadeHook) SetTraitSync(t *TraitSyncService) {
	if h == nil {
		return
	}
	h.traitSync = t
}

// SetRuntimeConfig wires the env-config.js emitter so the cascade can
// re-emit window._env_ values on every SPA in the project when any
// component lands deployed (sibling API URLs become available). Optional.
func (h *DispatchCascadeHook) SetRuntimeConfig(r *RuntimeConfigService) {
	if h == nil {
		return
	}
	h.runtimeConfig = r
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
	// Dependency URL handoff to consumer SPAs now flows through the
	// ReleaseBinding `env-config.js` (BFF emits per-env values into
	// workloadOverrides.container.files), so no GitHub issue comment is
	// posted here. dispatch's resolveDependencyEndpoints continues to
	// enforce the §1.3 URL invariant at dispatch time.

	// For OIDC-SPA web-apps the platform IDP redirect_uris are
	// registered by RuntimeConfigService.layerThunderKeys when the SPA
	// gets its env-config.js emitted below — no separate dispatch-side
	// Thunder call is needed.

	// Sibling-CORS re-emit: any dispatch in a project re-emits the
	// `cors.allowedOrigins` block on every protected API's ReleaseBinding
	// so freshly added SPAs are echoed back on preflight. Without this,
	// the first deploy of a new SPA cannot call the API cross-origin
	// until something else triggers a sync. Idempotent + best-effort.
	if h.traitSync != nil {
		if err := h.traitSync.SyncProjectAPITraits(ctx, orgID, projectID); err != nil {
			slog.WarnContext(ctx, "dispatch cascade: SyncProjectAPITraits failed",
				"project", projectID, "deployedComponent", componentName, "error", err)
		}
	}

	// Env-config.js re-emit: when a sibling service's external URL just
	// resolved, the depending SPAs need their `window._env_.API_BASE_URL`
	// refreshed. Re-emit env-config.js on every SPA in the project so
	// the next pod restart picks up the latest values. Idempotent +
	// best-effort.
	if h.runtimeConfig != nil {
		if err := h.runtimeConfig.EmitForProjectSPAs(ctx, orgID, projectID); err != nil {
			slog.WarnContext(ctx, "dispatch cascade: EmitForProjectSPAs failed",
				"project", projectID, "deployedComponent", componentName, "error", err)
		}
	}

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
