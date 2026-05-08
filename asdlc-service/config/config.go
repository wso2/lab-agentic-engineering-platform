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

	Observability   ObservabilityConfig
	AgentsService   AgentsServiceConfig
	RemoteWorker    RemoteWorkerConfig
	ServiceAuth     ServiceAuthConfig
	GitService      GitServiceConfig
	DatabaseService DatabaseServiceConfig

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
	ServiceAuthRemoteWorker  ServiceAuthConfig
}

// RemoteWorkerConfig holds connection settings for the remote-worker service.
// BaseURL is optional; if empty, task dispatch via remote-worker is disabled.
type RemoteWorkerConfig struct {
	BaseURL string
	// GitServiceHostURL is the URL the remote-worker uses to reach git-service
	// for /credentials/refresh. In container mode this is an in-network DNS
	// name; in host mode it's the published localhost port.
	GitServiceHostURL string
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
// (AI SDK v6-based; BA, architect, task-generator, wireframe).
type AgentsServiceConfig struct {
	BaseURL string
}

// ObservabilityConfig holds connection settings for the observability service.
// BaseURL is optional; if empty, build log endpoints return an unavailable error.
type ObservabilityConfig struct {
	BaseURL string
}

// PlatformAPIConfig holds connection settings for the OpenChoreo platform API.
type PlatformAPIConfig struct {
	BaseURL    string
	HostHeader string
	// BuildRegistry is the Docker registry OC's dockerfile-builder pushes to.
	// Used to construct image refs at deploy time — OC does not surface them
	// in the WorkflowRun API.
	BuildRegistry string
	// OrgNamespaceOverride is a comma-separated list of orgHandle=namespace
	// pairs. When set, the OC client resolves org handles to the given
	// namespace instead of using the org handle directly. Example:
	//   admin=dp-wso2cloud-core-development-54e3d6ff
	// Needed when running under WSO2Cloud where K8s namespaces are
	// auto-generated and don't match org handles.
	OrgNamespaceOverride string
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
