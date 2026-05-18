package services

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"

	"github.com/wso2/asdlc/asdlc-service/clients/openchoreo"
	"github.com/wso2/asdlc/asdlc-service/models"
)

// TraitSyncService is the single shared emitter for `api-configuration`
// trait state on a Component CR + its per-environment ReleaseBindings.
// See docs/design/api-platform-integration.md section 6 (Phase 2).
//
// Two write triggers call SyncComponentTraits:
//  1. Dispatch path (`dispatch_service.go`): after CreateComponent so a
//     newly-protected component lands with traits set immediately.
//  2. Design edit path (`design_service.UpdateDesignFile`): after the
//     user toggles `api.security` on `design.md` so the trait shape
//     propagates without waiting for the next dispatch.
//
// Concurrency: every call acquires a per-component mutex keyed by
// `(orgID, projectID, componentName)`. We use a `sync.Map` of `*sync.Mutex`
// — NOT `singleflight`. Singleflight coalesces duplicate calls (returns
// the in-flight call's result to later callers and skips their work),
// which is wrong here: a design PUT that lands while the dispatch path
// is mid-flight must trigger its own read after the dispatch finishes,
// not piggyback on the dispatch's stale read.
//
// The current implementation chooses the documented fallback path from
// the plan: protected components keep `autoDeploy: true`, accept the
// short first-deploy exposure window, and rely on this emitter +
// SyncAllProjectComponentTraits (drift sweep) for convergence. This was
// chosen over `autoDeploy: false` + BFF-managed RBs because the project→
// environment→RB binding logic in OC requires autoDeploy to drive
// initial RB creation; verifying we can short-circuit it is the
// prerequisite spike documented in §6 Phase 2 (R2). When OC adds first-
// class support for declarative RBs without autoDeploy, this code can
// flip to that path.
type TraitSyncService struct {
	componentClient openchoreo.ComponentClient
	store           *ArtifactStore
	// enabled gates the Phase 2 emit/reconcile path. When false the
	// service no-ops every method so dispatch + design PUT + watcher
	// behave like pre-Phase-2. Set at construction via the
	// FEATURE_EMIT_API_TRAIT config flag (config.Config.FeatureEmitAPITrait).
	enabled bool
	// idp, when non-nil, is invoked on every protected reconcile to
	// lazily ensure the org's Thunder publisher app exists. Failures
	// are logged but don't block the trait emit — the API stays
	// reachable, the org just lacks an outbound publisher identity
	// until a subsequent sync succeeds. Wired via SetIDPService.
	idp IDPService

	mu    sync.Mutex
	locks map[string]*sync.Mutex
}

// SetIDPService wires the per-org Thunder publisher provisioning hook.
// Optional — when not set the trait emit path skips the publisher
// EnsureOrgPublisher call entirely.
func (s *TraitSyncService) SetIDPService(idp IDPService) {
	if s == nil {
		return
	}
	s.idp = idp
}

func NewTraitSyncService(componentClient openchoreo.ComponentClient, store *ArtifactStore, enabled bool) *TraitSyncService {
	return &TraitSyncService{
		componentClient: componentClient,
		store:           store,
		enabled:         enabled,
		locks:           make(map[string]*sync.Mutex),
	}
}

// Enabled reports whether trait emission is gated on (FEATURE_EMIT_API_TRAIT).
// Exposed so callers (watcher init, controllers) can suppress related logging
// or metrics when the feature is off.
func (s *TraitSyncService) Enabled() bool {
	if s == nil {
		return false
	}
	return s.enabled
}

// SyncComponentTraits reconciles the OC Component CR + its ReleaseBindings
// against the desired state derived from `design.md`. Acquires the
// per-component mutex BEFORE reading design so a concurrent design edit
// doesn't write past us mid-PATCH.
//
// `componentName` is the user-friendly name (matches design.md component
// name); the OC client prefixes it with the project name internally.
//
// First-deploy race: when no ReleaseBindings exist yet for the component,
// the per-RB PATCH is a soft no-op (handled inside the OC client). The
// next dispatch — which is the only path that creates the Component CR
// with the trait already populated — closes that gap. The drift watcher
// catches anything that falls through.
//
// Errors are returned to the caller. Call sites in dispatch / design PUT
// log and continue (the design tree is the canonical source; the watcher
// will reconcile on the next sweep).
func (s *TraitSyncService) SyncComponentTraits(ctx context.Context, orgID, projectID, componentName string) error {
	if s == nil || !s.enabled {
		return nil
	}
	if orgID == "" || projectID == "" || componentName == "" {
		return fmt.Errorf("trait_sync: empty orgID/projectID/componentName")
	}

	mu := s.lockFor(orgID, projectID, componentName)
	mu.Lock()
	defer mu.Unlock()

	// Read design AFTER lock acquisition — never read before locking.
	// Otherwise a concurrent edit can write a newer version while we're
	// mid-PATCH with a stale read.
	design, err := s.store.ReadDesign(ctx, orgID, projectID)
	if err != nil {
		if IsNotFound(err) {
			// No design at all yet — nothing to reconcile. Reached from a
			// design PUT race where the controller hands us a stale path.
			return nil
		}
		return fmt.Errorf("trait_sync: read design: %w", err)
	}
	if design == nil {
		return nil
	}

	// Find the component in design by the k8s-shaped name (matches dispatch).
	var match *models.DesignComponent
	for i := range design.Components {
		if toK8sName(design.Components[i].Name) == componentName {
			match = &design.Components[i]
			break
		}
	}
	if match == nil {
		// Component is gone from design — caller (DeleteComponent path)
		// owns the OC cleanup. Trait sync has nothing to do.
		return nil
	}

	desiredEnabled := ResolveAPISecurityEnabled(*match)

	// Phase 3 — lazy provisioning of the org's Thunder publisher app.
	// First protected reconcile in an org creates `asdlc-publisher-<orgID>`
	// (idempotent on subsequent calls). Failures are non-fatal — the
	// trait still emits, the API stays reachable; the publisher will
	// be retried on the next reconcile or via the drift watcher.
	var issuers []string
	if desiredEnabled && s.idp != nil {
		if _, _, _, err := s.idp.EnsureOrgPublisher(ctx, orgID, "trait_sync"); err != nil {
			slog.WarnContext(ctx, "trait_sync: EnsureOrgPublisher failed; continuing",
				"orgID", orgID, "error", err)
		}
		// Phase 7 — when the org has a BYO-IDP (non-platform) profile,
		// pass its issuer into the trait so the RestApi pins JWT
		// validation to that issuer only. Platform-kind orgs keep
		// `issuers` empty (any cluster-configured keymanager).
		if profile, perr := s.idp.GetProfile(ctx, orgID); perr == nil && profile != nil {
			if profile.Kind != "" && profile.Kind != "platform" && profile.Issuer != "" {
				issuers = []string{profile.Issuer}
			}
		}
	}

	traits, configs := DesiredAPIConfigurationTraitWithIssuers(componentName, desiredEnabled, issuers)

	// Patch the Component CR's spec.traits. Skip when there's nothing to
	// change — but the OC client's GET-then-PUT is harmless so we always
	// fire to avoid bookkeeping drift between in-process state and OC.
	if err := s.componentClient.UpdateComponentTraits(ctx, orgID, projectID, componentName, traits); err != nil {
		return fmt.Errorf("trait_sync: update component traits: %w", err)
	}

	// Patch every existing ReleaseBinding's traitEnvironmentConfigs. The
	// OC client returns a soft no-op when none exist yet (first-deploy
	// race) — that's expected and the dispatch path creates the RB with
	// the right env config in place via the Component's autoDeploy
	// reconcile.
	if err := s.componentClient.UpdateComponentTraitEnvironmentConfigs(ctx, orgID, projectID, componentName, configs); err != nil {
		return fmt.Errorf("trait_sync: update trait env configs: %w", err)
	}

	slog.InfoContext(ctx, "trait_sync: reconciled",
		"orgID", orgID,
		"projectID", projectID,
		"componentName", componentName,
		"apiSecurityEnabled", desiredEnabled,
	)
	return nil
}

// DeleteComponentCascade deletes the OC Component CR via OC's REST API.
//
// Cleanup chain — end-to-end via OC:
//
//	Component  → owner ref → ComponentRelease
//	                        → owner ref → ReleaseBinding
//	                                     → owner ref → RenderedRelease
//	                                                  → finalizer (DataPlaneCleanupFinalizer)
//	                                                    iterates Status.Resources
//	                                                    and deletes every tracked
//	                                                    resource in the dp-namespace
//	                                                    — including the trait-
//	                                                    emitted Backend +
//	                                                    RestApi.
//
// The trait template's `creates` resources are tracked in
// RenderedRelease.Status.Resources by the OC controller at apply time,
// so the finalizer covers them even though they don't carry explicit
// ownerReferences (R3 from an earlier revision of the plan turned out
// to be unnecessary once we read OC's controller code — see
// renderedrelease/controller_finalize.go in the OC tree).
//
// Acquires the per-component mutex BEFORE issuing the OC call so a
// concurrent SyncComponentTraits (e.g. from a late design PUT) doesn't
// race with the deletion.
func (s *TraitSyncService) DeleteComponentCascade(ctx context.Context, orgID, projectID, componentName string) error {
	if s == nil {
		return nil
	}
	if orgID == "" || projectID == "" || componentName == "" {
		return fmt.Errorf("trait_sync: empty orgID/projectID/componentName")
	}

	mu := s.lockFor(orgID, projectID, componentName)
	mu.Lock()
	defer mu.Unlock()

	if err := s.componentClient.DeleteComponent(ctx, orgID, projectID, componentName); err != nil {
		return fmt.Errorf("trait_sync: delete component: %w", err)
	}

	if s.enabled {
		slog.InfoContext(ctx, "trait_sync: component deleted; OC RenderedRelease finalizer GCs trait resources",
			"orgID", orgID,
			"projectID", projectID,
			"componentName", componentName,
		)
	}
	return nil
}

func (s *TraitSyncService) lockFor(orgID, projectID, componentName string) *sync.Mutex {
	key := orgID + "/" + projectID + "/" + componentName
	s.mu.Lock()
	defer s.mu.Unlock()
	if mu, ok := s.locks[key]; ok {
		return mu
	}
	mu := &sync.Mutex{}
	s.locks[key] = mu
	return mu
}

// -- Pure helpers ------------------------------------------------------------

// APIConfigurationInstanceName returns the canonical trait instance name
// for the component's HTTP endpoint. Mirrors the POC manifests' naming
// (`<componentName>-http`) so on-cluster resources are predictable. The
// trait template uses this as the prefix for the generated Backend and
// RestApi resources (`<instanceName>-api-gw-backend`, `<instanceName>`).
func APIConfigurationInstanceName(componentName string) string {
	componentName = strings.TrimSpace(componentName)
	if componentName == "" {
		componentName = "component"
	}
	return componentName + "-http"
}

// DesiredAPIConfigurationTrait — convenience shim that calls
// DesiredAPIConfigurationTraitWithIssuers with no issuer pinning.
// Existing callers (tests, single-IDP code paths) get the same shape
// as before.
func DesiredAPIConfigurationTrait(componentName string, enabled bool) (traits []models.ComponentTrait, configs map[string]map[string]interface{}) {
	return DesiredAPIConfigurationTraitWithIssuers(componentName, enabled, nil)
}

// DesiredAPIConfigurationTraitWithIssuers returns the BFF-internal
// desired state for the `api-configuration` trait. When `enabled` is
// true, the trait is attached + jwtAuth is enabled in the per-env
// config with `issuers` pinned to the supplied list (empty ⇒ accept
// any cluster-configured keymanager, i.e. pre-Phase-7 behaviour). When
// `enabled` is false, the function returns nil + a tombstone entry to
// strip any previously-set config.
//
// `configs` is keyed by trait instance name; the value is the parameters
// block that lands at `ReleaseBinding.spec.traitEnvironmentConfigs[<inst>]`.
// The shape (cors / jwtAuth) matches the trait's environmentConfigSchema.
func DesiredAPIConfigurationTraitWithIssuers(componentName string, enabled bool, issuers []string) (traits []models.ComponentTrait, configs map[string]map[string]interface{}) {
	inst := APIConfigurationInstanceName(componentName)
	if !enabled {
		// Clear both: empty traits + empty config marks the instance for
		// removal in the OC client's merge logic.
		return nil, map[string]map[string]interface{}{
			inst: nil,
		}
	}
	traits = []models.ComponentTrait{{
		InstanceName: inst,
		Kind:         "ClusterTrait",
		Name:         "api-configuration",
		Parameters: map[string]interface{}{
			"endpointName": "http",
		},
	}}
	issuersIface := make([]interface{}, 0, len(issuers))
	for _, iss := range issuers {
		issuersIface = append(issuersIface, iss)
	}
	configs = map[string]map[string]interface{}{
		inst: {
			"cors": map[string]interface{}{
				"enabled": true,
			},
			"jwtAuth": map[string]interface{}{
				"enabled": true,
				// Phase 6 — the trait now emits `jwt-auth v1` which
				// accepts `issuers` + `audience` arrays. Empty issuers
				// mean "no per-RestApi filter; trust any cluster-
				// configured keymanager" (Phase 6 default). Phase 7
				// (BYO-IDP) populates these from the org's IDP profile
				// so each protected API only trusts its org's IDP.
				"issuers":  issuersIface,
				"audience": []interface{}{},
			},
		},
	}
	return traits, configs
}
