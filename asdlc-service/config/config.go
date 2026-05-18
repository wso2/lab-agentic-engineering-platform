package config

// Config holds all application configuration.
type Config struct {
	ServerHost string
	ServerPort int
	LogLevel   string

	PlatformAPI PlatformAPIConfig
	DatabaseURL string

	// Test mode — enables test-only endpoints like _test/reset.
	TestMode bool

	// DeploymentTier guards dev-only destructive migrations and seed paths.
	// Phase 0 used this for the platform-PAT startup gate (now retired in PR
	// A); Phase 2 PR A's BFF migration (RunPhase2PRA) refuses to run unless
	// tier=dev.
	DeploymentTier string

	// GitHubWebhookSecret is the HMAC key for inbound webhook validation
	// (one-shot, set per-org in production; one global value in dev).
	GitHubWebhookSecret string

	// OAuthStateSigningKey is the HS256 key used to sign the connect-state
	// JWT that rides the GitHub App OAuth `state` query param (CSRF
	// protection on the connect callback). Task JWTs use RS256 via
	// TaskTokenSigningKey; this key has no other use.
	OAuthStateSigningKey string

	// Phase 2 PR B — GitHub App connect surface.
	GithubAppSlug     string // App's URL slug, used in the install URL
	GithubAppClientID string // App's OAuth client_id; used to build the OAuth authorize URL
	// BFFPublicURL is the user-visible BFF base — used as the basis for
	// the App-mode redirect after callback (302 → console settings page).
	BFFPublicURL string

	// TaskTokenSigningKey is the PEM-encoded RSA private key used to sign
	// Task JWTs. The matching public key is published at /auth/external/jwks.json.
	TaskTokenSigningKey string
	// TaskTokenIssuer is the iss claim on issued Task JWTs (e.g. "asdlc-bff").
	TaskTokenIssuer string
	// TaskTokenAudience is the aud claim — fixed to "git-service" today, the
	// only verifier of Task JWTs.
	TaskTokenAudience string

	// Phase 2 PR D §9.3 — build watcher git_clone_failed_auth retry budget.
	// Default 3 attempts. Configurable via BUILD_AUTH_RETRY_BUDGET; tests
	// set to 0 to force exhaustion on the first auth failure.
	BuildAuthRetryBudget int

	// FeatureEmitAPITrait gates Phase 2 of the api-platform-integration
	// plan (docs/design/api-platform-integration.md §6 Phase 2). When
	// true: dispatch emits the `api-configuration` ClusterTrait based on
	// design.md `api.security`, design PUT triggers trait_sync, and the
	// drift watcher reconciles. When false: every code path behaves like
	// pre-Phase-2 — no trait emission, no watcher reconcile. Per §14
	// Rollout, default is on in dev / off in prod until a corpus of
	// existing components passes baseline-diff.
	FeatureEmitAPITrait bool

	// Phase 3 (api-platform-integration) — Thunder admin client config
	// for per-org publisher OAuth app lifecycle. Loaded from env vars
	// THUNDER_ADMIN_URL / THUNDER_SYSTEM_CLIENT_ID / THUNDER_SYSTEM_CLIENT_SECRET.
	// When ClientID is empty the BFF logs a warning and the IDP service
	// returns ErrIDPThunderUnavailable (non-fatal — protected components
	// still deploy, just without per-org publishers).
	ThunderAdmin ThunderAdminConfig

	// Platform IDP defaults seeded into organization_idp_profiles rows
	// on first access. Loaded from PLATFORM_IDP_ISSUER /
	// PLATFORM_IDP_JWKS_URL — should match the cluster's Thunder
	// keymanager in gateway-config.yaml.
	PlatformIDP PlatformIDPDefaults

	// UserAppsOIDC is the OIDC config the BFF hands to every user web-app
	// component with design.auth.kind == "oidc-spa". One shared OAuth
	// client (`ASDLC_USER_APPS` in Thunder, pre-seeded by setup-prerequisites)
	// is used for all user webapps in v1 — per-project clients are a v2
	// follow-up tracked in docs/design/oauth-protected-webapp.md.
	UserAppsOIDC UserAppsOIDCConfig

	Observability   ObservabilityConfig
	AgentsService   AgentsServiceConfig
	ServiceAuth     ServiceAuthConfig
	GitService      GitServiceConfig
	DatabaseService DatabaseServiceConfig

	// AgentGitServiceURL is the URL the coding-agent runner pod uses to reach
	// git-service for /credentials/refresh. The pod runs in the per-tenant
	// WorkflowPlane namespace (`workflows-<ouHandle>`), so this must be a
	// cross-namespace FQDN (e.g.
	// http://app-factory-git-service.<dp-ns>.svc.cluster.local:3300).
	// Falls back to GitService.BaseURL when empty.
	AgentGitServiceURL string

	// AgentPlatformURL is the URL the coding-agent runner pod uses to call
	// back to the BFF (F3c — POST /api/v1/tasks/{id}/verification-failed).
	// Reachable from the WorkflowPlane namespace; same cross-namespace FQDN
	// shape as AgentGitServiceURL. When empty, the runner skips the
	// verification-failed callback and only records the diagnostic on the
	// GitHub issue.
	AgentPlatformURL string

	// JWKS settings for inbound JWT verification — Thunder publishes the
	// User JWT and Service JWT signing key at JWKSURL; verifiers refresh
	// on kid miss. Issuer and audience configure RFC 7519 claim checks.
	JWKSURL                string
	JWTAllowedIssuer       string
	JWTAllowedAudience     string
	JWTResourceMetadataURL string

	// Per-target Service JWT clients used for outbound auth. Each one
	// corresponds to a distinct Thunder OAuth2 client whose audience is
	// pinned to the target service.
	ServiceAuthGitService    ServiceAuthConfig
	ServiceAuthAgentsService ServiceAuthConfig
}

// ThunderAdminConfig holds the asdlc-system-client OAuth2 credentials
// + base URL the BFF uses to manage Thunder applications (per-org
// publisher lifecycle). The same Thunder instance that fronts user
// PKCE login — see deployments/single-cluster/values-thunder.yaml's
// CONFIDENTIAL_APPS for the row that ships these credentials.
type ThunderAdminConfig struct {
	BaseURL      string
	ClientID     string
	ClientSecret string
}

// PlatformIDPDefaults are the issuer + JWKS URL of the cluster's
// platform IDP (Thunder in v1). Seeded into every new
// organization_idp_profiles row.
type PlatformIDPDefaults struct {
	Issuer  string
	JWKSURL string
}

// UserAppsOIDCConfig is the OIDC client config the BFF hands to user
// web-apps. Loaded from env vars USER_APPS_OIDC_ISSUER /
// USER_APPS_OIDC_CLIENT_ID / USER_APPS_OIDC_SCOPES /
// USER_APPS_OIDC_INTERNAL_PROXY_PASS. When ClientID is empty the
// dispatch path skips the `## OIDC client provisioned` comment and
// logs a warning — user apps with auth.kind=oidc-spa will deploy but
// fail at sign-in.
//
// InternalProxyPass is the URL the SPA's own nginx `/oidc/` block uses
// to proxy `POST /oidc/token` back to Thunder. It MUST be reachable
// from a pod inside the cluster (the public Issuer hostname isn't —
// `*.openchoreo.localhost` doesn't resolve from pod DNS). Default is
// the in-cluster Thunder Service FQDN + the `/oauth2/` path prefix.
type UserAppsOIDCConfig struct {
	Issuer            string
	ClientID          string
	Scopes            string // space-separated, e.g. "openid profile"
	InternalProxyPass string
}

// ServiceAuthConfig holds OAuth2 client_credentials settings for
// service-to-service authentication (e.g. BFF → OpenChoreo API).
type ServiceAuthConfig struct {
	TokenURL     string
	ClientID     string
	ClientSecret string
	HostHeader   string // Thunder Host header for k3d routing
}

// AgentsServiceConfig holds connection settings for the asdlc-agents-service
// (AI SDK v6-based; BA, architect, tech-lead).
type AgentsServiceConfig struct {
	BaseURL string
}

// ObservabilityConfig holds connection settings for the OpenChoreo Observer
// service. BaseURL is optional; if empty, the BFF returns 503
// progress_unavailable on the /progress/* endpoints. Auth fields drive the
// Thunder client_credentials flow used to read workflow-run logs.
type ObservabilityConfig struct {
	BaseURL string

	// OAuth client_credentials settings — wired to the platform-default
	// reader app `openchoreo-observer-resource-reader-client` on this
	// branch. Promoting to multi-tenant cloud should swap this for a
	// per-app registration (see task-execution-progress.md §5.4).
	TokenURL     string
	ClientID     string
	ClientSecret string
	HostHeader   string

}

// PlatformAPIConfig holds connection settings for the OpenChoreo platform API.
type PlatformAPIConfig struct {
	BaseURL    string
	HostHeader string
}

// GitServiceConfig holds connection settings for the git-service.
//
// PR 2 of the repo-storage-ownership refactor removed RepoBasePath: the
// BFF no longer mounts the repo working tree (git-service is the sole
// owner). All artifact reads/writes go over HTTP via BaseURL.
type GitServiceConfig struct {
	BaseURL string
}

// DatabaseServiceConfig holds connection settings for the database-service.
// BaseURL is optional; if empty, database provisioning is disabled.
type DatabaseServiceConfig struct {
	BaseURL string
}
