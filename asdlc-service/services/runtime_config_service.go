package services

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sort"
	"strings"

	"github.com/wso2/asdlc/asdlc-service/clients/openchoreo"
	"github.com/wso2/asdlc/asdlc-service/clients/thundersvc"
	"github.com/wso2/asdlc/asdlc-service/models"
)

// RuntimeConfigService emits the per-web-app `env-config.js` file onto
// each ReleaseBinding's `workloadOverrides.container.files`. The SPA's
// `index.html` loads `/env-config.js` synchronously before its bundle,
// so the values land on `window._env_` before any React module runs.
//
// The BFF plays the platform-engineer role here — the coding agent
// never sees the upstream URL, the OIDC client_id, or any redirect URI.
// One image runs unchanged in every environment; per-env values arrive
// at ReleaseBinding time.
type RuntimeConfigService struct {
	componentClient openchoreo.ComponentClient
	store           *ArtifactStore
	// thunderAdmin, when non-nil, is used to declare the per-project
	// OAuth client on the first SPA emission in a project. The returned
	// client_id is the project name verbatim — the SPA reads it from
	// `window._env_.THUNDER_CLIENT_ID` to drive PKCE.
	thunderAdmin thundersvc.Client
	// platformIDPIssuer is the public Thunder URL (e.g.
	// `http://thunder.openchoreo.localhost:8080`). Written into every
	// SPA's `THUNDER_URL`.
	platformIDPIssuer string
	// platformIDPScopes is the default OAuth scope string (e.g.
	// `openid profile email`). Written into every SPA's `THUNDER_SCOPES`.
	platformIDPScopes string
}

func NewRuntimeConfigService(componentClient openchoreo.ComponentClient, store *ArtifactStore) *RuntimeConfigService {
	return &RuntimeConfigService{
		componentClient:   componentClient,
		store:             store,
		platformIDPScopes: "openid profile email",
	}
}

// SetThunderAdmin wires the Thunder admin client. When set, the first
// SPA emission in a project declares the per-project OAuth client and
// later emissions merge the SPA's external URL into its redirectUris.
func (s *RuntimeConfigService) SetThunderAdmin(t thundersvc.Client) {
	if s == nil {
		return
	}
	s.thunderAdmin = t
}

// SetPlatformIDP wires the cluster-wide Thunder URL + default scopes.
// These are written into every SPA's window._env_ as THUNDER_URL +
// THUNDER_SCOPES so the bundle can drive PKCE against the same IDP the
// gateway validates JWTs from.
func (s *RuntimeConfigService) SetPlatformIDP(issuer, scopes string) {
	if s == nil {
		return
	}
	if issuer != "" {
		s.platformIDPIssuer = issuer
	}
	if scopes != "" {
		s.platformIDPScopes = scopes
	}
}

// EmitForComponent computes the env-config.js content for the named
// component and writes it onto each of the component's ReleaseBindings.
// No-op when the component is not a web-app.
//
// Idempotent + best-effort. The OC client returns a soft no-op when no
// ReleaseBindings exist yet — the cascade hook re-fires on every deploy
// in the project so the file lands after the first build catches up.
func (s *RuntimeConfigService) EmitForComponent(ctx context.Context, orgID, projectID, componentName string) error {
	if s == nil {
		return nil
	}
	if orgID == "" || projectID == "" || componentName == "" {
		return fmt.Errorf("runtime_config: empty orgID/projectID/componentName")
	}

	design, err := s.store.ReadDesign(ctx, orgID, projectID)
	if err != nil {
		if IsNotFound(err) {
			return nil
		}
		return fmt.Errorf("runtime_config: read design: %w", err)
	}
	if design == nil {
		return nil
	}

	var match *models.DesignComponent
	for i := range design.Components {
		if toK8sName(design.Components[i].Name) == componentName {
			match = &design.Components[i]
			break
		}
	}
	if match == nil || match.ComponentType != "web-app" {
		return nil
	}

	envValues, ready := s.buildEnvValues(ctx, orgID, projectID, match, design)
	if !ready {
		// One or more required keys couldn't be populated yet (transient
		// OC error, SPA URL not resolved, etc.). DO NOT write a partial
		// env-config.js — that would either blank previously valid keys
		// or ship a window._env_ that the SPA's src/env.ts throws on at
		// module load. The cascade hook re-fires on every deploy event,
		// so the next sibling deploy (or this SPA's own follow-up
		// reconcile) will retry.
		slog.InfoContext(ctx, "runtime_config: required keys not yet ready; deferring env-config.js write",
			"orgID", orgID,
			"projectID", projectID,
			"component", componentName,
			"keys", sortedKeys(envValues),
		)
		return nil
	}
	file := models.WorkflowFileVar{
		Key:       "env-config.js",
		MountPath: "/usr/share/nginx/html/",
		Value:     renderEnvConfigJS(envValues),
	}

	if err := s.componentClient.UpdateComponentWorkflowFiles(ctx, orgID, projectID, componentName, []models.WorkflowFileVar{file}); err != nil {
		return fmt.Errorf("runtime_config: update workflow files: %w", err)
	}

	slog.InfoContext(ctx, "emitting env-config.js",
		"orgID", orgID,
		"projectID", projectID,
		"component", componentName,
		"keys", sortedKeys(envValues),
	)
	return nil
}

// EmitForProjectSPAs re-emits env-config.js on every web-app component in
// the project. Called from the dispatch cascade so that when ANY component
// lands `deployed` (especially a sibling service whose external URL just
// resolved), every SPA picks up the new value in its ReleaseBinding without
// waiting for the SPA itself to re-deploy.
//
// Idempotent + best-effort: a failure on one component logs and continues.
func (s *RuntimeConfigService) EmitForProjectSPAs(ctx context.Context, orgID, projectID string) error {
	if s == nil {
		return nil
	}
	if orgID == "" || projectID == "" {
		return fmt.Errorf("runtime_config: empty orgID/projectID")
	}
	design, err := s.store.ReadDesign(ctx, orgID, projectID)
	if err != nil {
		if IsNotFound(err) {
			return nil
		}
		return fmt.Errorf("runtime_config: read design: %w", err)
	}
	if design == nil {
		return nil
	}
	for _, c := range design.Components {
		if c.ComponentType != "web-app" {
			continue
		}
		k8sName := toK8sName(c.Name)
		if err := s.EmitForComponent(ctx, orgID, projectID, k8sName); err != nil {
			slog.WarnContext(ctx, "runtime_config: per-SPA emit failed; continuing",
				"orgID", orgID,
				"projectID", projectID,
				"componentName", k8sName,
				"error", err,
			)
		}
	}
	return nil
}

// buildEnvValues assembles the map that becomes `window._env_`.
//   - `API_BASE_URL` — first sibling service dep's external URL (the
//     conventional name for the primary backend).
//   - `<UPPER_SNAKE_NAME>_URL` — every dep, keyed by component name. Lets
//     a SPA with multiple backends address each one explicitly.
//   - `THUNDER_*` — OIDC config. Emitted when the webapp's design
//     declares `auth.kind: oidc-spa` (or Phase 2's `callerIdentity.mode:
//     end-user`). The BFF declares the per-project OAuth client in
//     Thunder lazily here; the agent never sees a client_id.
// buildEnvValues returns the map + a `ready` flag. The flag is false
// when a required key couldn't be populated yet (transient OC error,
// SPA URL not yet resolved, etc.). The caller must NOT write a
// partial env-config.js on `!ready` — see EmitForComponent.
func (s *RuntimeConfigService) buildEnvValues(ctx context.Context, orgID, projectID string, webapp *models.DesignComponent, design *DesignFile) (out map[string]interface{}, ready bool) {
	out = map[string]interface{}{}
	ready = true

	// Index sibling components by name for type lookup.
	byName := make(map[string]models.DesignComponent, len(design.Components))
	for _, c := range design.Components {
		byName[c.Name] = c
	}

	var firstServiceURL string
	for _, dep := range webapp.DependsOn {
		sibling, ok := byName[dep]
		if !ok {
			continue
		}
		// Skip non-service deps (peer webapps aren't called over HTTP).
		if sibling.ComponentType != "service" {
			continue
		}
		k8sName := toK8sName(dep)
		list, err := s.componentClient.ListDeployments(ctx, orgID, projectID, k8sName)
		if err != nil {
			// Transient OC failure on a required dep. Mark not-ready so
			// the caller skips the write and preserves the previously
			// valid env-config.js for the pod.
			slog.WarnContext(ctx, "runtime_config: ListDeployments error for dep; deferring",
				"projectID", projectID, "component", webapp.Name, "dep", dep, "error", err)
			ready = false
			continue
		}
		if list == nil {
			continue
		}
		url := ""
		for _, d := range list.Items {
			if d.EndpointURL != "" {
				url = strings.TrimRight(d.EndpointURL, "/")
				break
			}
		}
		if url == "" {
			// Dep not yet deployed — not an error, but we don't have a
			// URL for it. Defer rather than ship a window._env_ that
			// will throw at module load.
			ready = false
			continue
		}
		out[upperSnakeKey(dep)+"_URL"] = url
		if firstServiceURL == "" {
			firstServiceURL = url
		}
	}
	if firstServiceURL != "" {
		out["API_BASE_URL"] = firstServiceURL
	}

	// Layer THUNDER_* — OIDC config the SPA reads to drive PKCE.
	if oidcSPAEnabled(webapp) {
		ok := s.layerThunderKeys(ctx, orgID, projectID, webapp, out)
		if !ok {
			ready = false
		}
	}

	return out, ready
}

// oidcSPAEnabled returns true when the component should receive
// THUNDER_* keys in its env-config.js. The new `callerIdentity.mode`
// field is authoritative when present — if a migration set it to
// `service-account`, that's binding even if a stale legacy `auth.kind:
// oidc-spa` is still on disk. Only when CallerIdentity is absent
// entirely does the legacy `auth.kind` apply.
func oidcSPAEnabled(c *models.DesignComponent) bool {
	if c == nil || c.ComponentType != "web-app" {
		return false
	}
	if c.CallerIdentity != nil && c.CallerIdentity.Mode != "" {
		return c.CallerIdentity.Mode == "end-user"
	}
	if c.Auth != nil && c.Auth.Kind == "oidc-spa" {
		return true
	}
	return false
}

// layerThunderKeys writes the OIDC client config into the env-config.js
// map. Idempotently declares the per-project Thunder OAuth client and
// merges the SPA's own external URL into its redirectUris.
//
// Returns false when a required input is missing (platform issuer not
// configured, SPA external URL not yet resolved, Thunder admin client
// reachable but EnsureProjectOAuthClient failed). The caller treats
// false as "defer the env-config.js write" so the SPA isn't shipped a
// window._env_ that will throw at module load.
func (s *RuntimeConfigService) layerThunderKeys(ctx context.Context, orgID, projectID string, webapp *models.DesignComponent, out map[string]interface{}) bool {
	if s.platformIDPIssuer == "" {
		slog.WarnContext(ctx, "runtime_config: platformIDPIssuer not configured; skipping THUNDER_*",
			"projectID", projectID, "component", webapp.Name)
		return false
	}

	// Compute the SPA's own external URL. Defer the THUNDER_* layer
	// until the SPA has materialised a public URL (the OC ReleaseBinding
	// status fills `Endpoints[].externalURLs` after the first reconcile).
	spaURL := s.componentExternalURL(ctx, orgID, projectID, webapp.Name)
	if spaURL == "" {
		slog.InfoContext(ctx, "runtime_config: SPA external URL not yet resolved; will retry on next cascade",
			"projectID", projectID, "component", webapp.Name)
		return false
	}
	spaOrigin := strings.TrimRight(spaURL, "/")
	redirectURI := spaOrigin + "/callback"

	// EnsureProjectOAuthClient is the source of truth for THUNDER_CLIENT_ID.
	// On error we DEFER rather than ship a clientID that Thunder doesn't
	// recognise — otherwise the SPA's /oauth2/authorize call returns
	// `invalid_client` with no useful diagnostic.
	if s.thunderAdmin == nil {
		slog.WarnContext(ctx, "runtime_config: thunderAdmin not wired; deferring THUNDER_*",
			"projectID", projectID, "component", webapp.Name)
		return false
	}
	gotID, _, err := s.thunderAdmin.EnsureProjectOAuthClient(ctx, projectID, []string{redirectURI})
	if err != nil {
		slog.WarnContext(ctx, "runtime_config: EnsureProjectOAuthClient failed; deferring",
			"projectID", projectID, "error", err)
		return false
	}
	if gotID == "" {
		slog.WarnContext(ctx, "runtime_config: EnsureProjectOAuthClient returned empty clientID; deferring",
			"projectID", projectID)
		return false
	}

	out["THUNDER_URL"] = s.platformIDPIssuer
	out["THUNDER_CLIENT_ID"] = gotID
	out["THUNDER_REDIRECT_URI"] = redirectURI
	out["THUNDER_SCOPES"] = s.platformIDPScopes
	out["THUNDER_AFTER_SIGN_IN_URL"] = spaOrigin
	return true
}

// componentExternalURL returns the first external URL OC has resolved
// for the named component, or "" when none is materialised yet.
func (s *RuntimeConfigService) componentExternalURL(ctx context.Context, orgID, projectID, componentName string) string {
	if s.componentClient == nil {
		return ""
	}
	k8sName := toK8sName(componentName)
	list, err := s.componentClient.ListDeployments(ctx, orgID, projectID, k8sName)
	if err != nil || list == nil {
		return ""
	}
	for _, d := range list.Items {
		if d.EndpointURL != "" {
			return d.EndpointURL
		}
	}
	return ""
}

// renderEnvConfigJS produces the literal JS the SPA's index.html loads
// synchronously before its bundle. Keys are sorted for byte-stable
// output so equality checks don't flap.
//
// Values that fail to marshal are emitted as `null` with a comment —
// silently dropping them would leave a trailing comma that aborts the
// SPA's <script> with a SyntaxError and blanks the page. `null` is at
// least a parseable value the SPA's typed env shim can throw on
// loudly.
func renderEnvConfigJS(values map[string]interface{}) string {
	var b strings.Builder
	b.WriteString("window._env_ = {\n")
	keys := sortedKeys(values)
	for i, k := range keys {
		raw, err := json.Marshal(values[k])
		if err != nil || len(raw) == 0 {
			raw = []byte("null")
		}
		b.WriteString("  ")
		// JS-side keys are bare identifiers — safe to emit unquoted since
		// upperSnakeKey returns only [A-Z0-9_].
		b.WriteString(k)
		b.WriteString(": ")
		b.Write(raw)
		if i < len(keys)-1 {
			b.WriteString(",")
		}
		b.WriteString("\n")
	}
	b.WriteString("};\n")
	return b.String()
}

func sortedKeys(m map[string]interface{}) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// upperSnakeKey converts a component name (kebab- or camelCase) into the
// upper-snake form used as a `window._env_` key prefix. Drops any chars
// outside [A-Za-z0-9_] so the result is a safe JS identifier.
func upperSnakeKey(name string) string {
	var b strings.Builder
	prevAlnum := false
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z':
			b.WriteRune(r - 'a' + 'A')
			prevAlnum = true
		case r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			b.WriteRune(r)
			prevAlnum = true
		case r == '-' || r == '_':
			if prevAlnum {
				b.WriteRune('_')
			}
			prevAlnum = false
		default:
			prevAlnum = false
		}
	}
	return strings.TrimRight(b.String(), "_")
}
