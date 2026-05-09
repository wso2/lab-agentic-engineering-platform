package config

import "time"

// Config holds all application configuration.
type Config struct {
	ServerHost string
	ServerPort int
	LogLevel   string

	DatabaseURL  string
	RepoBasePath string

	// GitHub — platform repo provisioning. Each org connects via the
	// settings page (App or PAT mode); every code path resolves the
	// per-org credential through the resolver. No platform-wide PAT,
	// no platform-wide repo owner — those come from the per-org
	// credential surface (Credential.Token + Credential.RepoOwner).
	GitHubRepoVisibility string
	GitHubCommitterName  string // committer attribution on platform-driven commits/tags
	GitHubCommitterEmail string

	// Webhook delivery URL the platform registers on each repo. The single
	// piece of config shared by local and cloud — local points at a smee.io
	// channel forwarded by a host-side relay; cloud points directly at the
	// BFF's public ingress URL. See docs/design/webhook-delivery.md.
	WebhookDeliveryURL string

	// Webhook HMAC secret. Phase 0 is single-tenant (one secret platform-wide);
	// Phase 2 stores per-org secrets on the credential record.
	WebhookHMACSecret string

	// Test mode — enables test-only endpoints like _test/reset.
	TestMode bool

	// Phase 2 PR A — OpenBao + per-org credentials.
	//
	// DeploymentTier guards the dev-only seed path. Production deployments
	// connect via the (PR B) /internal/credentials/orgs/{ocOrgId} endpoint
	// instead of seeding from env.
	DeploymentTier string

	// OpenBao connection details. Token-based dev auth; production uses
	// kubernetes-auth (the policies are pre-seeded by the postStart hook
	// in OpenBao's helm values — no per-tenant ACL configuration needed).
	OpenBaoAddr  string
	OpenBaoToken string

	// Phase 2 PR B — GitHub App + internal credential routes.
	//
	// GitHubAppID is the App's numeric ID. Optional in dev; PR B's
	// app_platform seed writes it into OpenBao at
	// secret/asdlc/_platform/github/app/app_id alongside the private key.
	GitHubAppID string

	// GitHubAppClientID is the OAuth client ID associated with the App.
	GitHubAppClientID string

	// GitHubAppClientSecret is the OAuth client secret used by the App's
	// user-OAuth flow (PR D-followup §6.4 — the discover-then-bind path
	// that recovers from "App already installed but platform has no row"
	// dead-ends). Seeded into OpenBao at _platform/github/app/client_secret;
	// used by git-service to exchange OAuth codes for user tokens during
	// bind. Empty in dev → discover/bind endpoints return 503.
	GitHubAppClientSecret string

	// GitHubAppSlug is the App's URL-shaped name, used to build the install
	// URL: https://github.com/apps/{slug}/installations/new.
	GitHubAppSlug string

	// GitHubAppPrivateKeyPath points at the on-disk PEM the dev seed loads.
	// The path is read once at startup, contents are written into OpenBao,
	// and the file is never read again. Production deployments seed
	// OpenBao directly via the operational runbook.
	GitHubAppPrivateKeyPath string

	// Phase 2 PR D — periodic credential validator interval (§6.10).
	// Default 24h. Configurable via CREDENTIAL_VALIDATOR_INTERVAL for
	// E2E tests to force fast ticks.
	CredentialValidatorInterval time.Duration

	// JWKSURL is the Thunder JWKS endpoint used to verify User and Service
	// JWTs presented to /api/v1/repos/* and /internal/credentials/*.
	JWKSURL string
	// BFFJWKSURL is the BFF's JWKS endpoint (/auth/external/jwks.json) used
	// to verify Task JWTs on /api/v1/credentials/refresh.
	BFFJWKSURL string

	// JWTAllowedIssuer / Audience configure RFC 7519 claim checks for
	// inbound Service JWTs.
	JWTAllowedIssuer       string
	JWTAllowedAudience     string
	JWTResourceMetadataURL string

	// TaskJWTAllowedIssuer / Audience configure claim checks for inbound
	// Task JWTs (issuer = "asdlc-bff", audience = "git-service").
	TaskJWTAllowedIssuer   string
	TaskJWTAllowedAudience string
}
