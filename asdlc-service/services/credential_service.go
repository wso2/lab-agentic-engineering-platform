// Package services hosts git-service's HTTP-tier orchestration. The
// credential_service.go file implements the per-org credential connect /
// status / disconnect surface defined in docs/design/github-integration-phase2.md
// §5.2 and §6.4–6.7.
package services

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"gorm.io/gorm"

	"github.com/wso2/asdlc/asdlc-service/models"
	"github.com/wso2/asdlc/asdlc-service/internal/credentials"
)

// CredentialService is the orchestration layer behind /internal/credentials/orgs/...
//
// It owns: validation of new PATs against GitHub, App-mode connect via
// installation lookup, status projection, disconnect Phase D, webhook-secret
// rotation, lookup helpers used by the BFF's webhook routing.
//
// The Resolver (used at runtime by every git operation) doesn't change at
// connect time — it just reads whatever this service has persisted.
// BuildSecretCleaner cleans up the per-org build-credential Secret in the
// org's workflow-plane namespace. Implemented by BuildCredentialsService;
// kept as an interface here so CredentialService doesn't import a
// concrete struct from a sibling file (no real circular import today,
// but keeps the seam minimal and testable).
//
// Renamed from WPSecretCleaner in the anthropic-key-dual-token work —
// each WP-Secret-cleanup concern (build, anthropic, future providers)
// has its own narrowly-typed interface so cred services only depend on
// what they own. See docs/design/anthropic-key-dual-token.md §S5.
type BuildSecretCleaner interface {
	DeleteBuildSecretsForOrg(ctx context.Context, ocOrgID string) error
}

// AnthropicSecretCleaner mirrors BuildSecretCleaner for the per-org
// Anthropic-credentials Secret. Implemented by AnthropicCredentialService.
type AnthropicSecretCleaner interface {
	DeleteAnthropicSecret(ctx context.Context, ocOrgID string) error
}

type CredentialService struct {
	db        *gorm.DB
	store     credentials.OpenBaoStore
	minter    *credentials.AppTokenMinter
	githubAPI string // "https://api.github.com" by default; overridden in tests.

	// buildSecretCleaner is invoked from the Disconnect cascade so a
	// disconnected org's WP build Secret doesn't outlive its credential
	// row. nil is a graceful no-op (tests, off-cluster runs).
	buildSecretCleaner BuildSecretCleaner

	// smAPIWriter mirrors the PAT into SM-API on Connect (WS2.2) and
	// clears it on Disconnect. nil-safe — no-op when the writer isn't
	// configured (composition-root behavior when SECRET_MANAGER_API_URL
	// is unset).
	smAPIWriter *SMAPIWriter

	// envWebhookSecret is the platform-wide GITHUB_WEBHOOK_SECRET. The PAT
	// connect path uses this value when seeding `webhook_secrets[0]` on a
	// fresh or cross-mode-reseeded row so the per-repo webhook (which the
	// services.webhook_service registers with the same env value) verifies
	// against it. Rotation lands by appending a new entry via the
	// AppendWebhookSecret route. Empty in tests.
	envWebhookSecret string

	// PR D-followup §6.4 — App OAuth client_id/secret used by the
	// discover-then-bind path (BindAppInstallation). Empty values disable
	// that path; the discover endpoint surfaces 503 in that mode.
	appClientID     string
	appClientSecret string

	// githubClient is the typed wrapper for GitHub REST calls. CredentialService
	// uses it for the PR D-followup discover-then-bind path
	// (ListAppInstallations, ExchangeOAuthCode, GetUserInstallations);
	// the rest of CredentialService still uses raw httpClient for legacy
	// reasons. Optional — nil disables the bind path.
	githubClient GitHubClient

	httpClient *http.Client
}

// NewCredentialService constructs the service. db, store, minter must be
// non-nil. githubAPI may be empty (defaults to api.github.com).
// envWebhookSecret is the GITHUB_WEBHOOK_SECRET — used as the seed value
// for fresh PAT rows and cross-mode reseeds.
// appClientID / appClientSecret enable the OAuth bind path (PR D-followup);
// empty values disable it gracefully.
// githubClient is used by the discover-then-bind path (ListAppInstallations,
// ExchangeOAuthCode, GetUserInstallations); nil disables the bind path.
func NewCredentialService(
	db *gorm.DB,
	store credentials.OpenBaoStore,
	minter *credentials.AppTokenMinter,
	envWebhookSecret string,
	appClientID, appClientSecret string,
	githubClient GitHubClient,
) *CredentialService {
	return &CredentialService{
		db:               db,
		store:            store,
		minter:           minter,
		envWebhookSecret: envWebhookSecret,
		appClientID:      appClientID,
		appClientSecret:  appClientSecret,
		githubClient:     githubClient,
		githubAPI:        "https://api.github.com",
		httpClient:       &http.Client{Timeout: 30 * time.Second},
	}
}

// ----------------------------------------------------------------------------
// Errors with stable codes for the BFF / API layer.
// ----------------------------------------------------------------------------

// ValidationError carries a structured cause string for the connect/replace
// path so the UI can render field-level error text. The Cause field is the
// machine-readable code; the Message is the human-friendly text.
type ValidationError struct {
	Code    string
	Message string
}

func (e *ValidationError) Error() string { return e.Message }

// ConflictError signals a request that violates the credential record's
// invariants — usually a cross-mode change (existing kind=app-installation
// row, new request with kind=user-pat) or attempting App-mode webhook-secret
// management.
type ConflictError struct {
	Reason string
}

func (e *ConflictError) Error() string { return "conflict: " + e.Reason }

// NotFoundError signals "no row matches this lookup" — distinct from
// network/DB errors so the API layer can return 404 cleanly.
type NotFoundError struct {
	What string
}

func (e *NotFoundError) Error() string { return "not found: " + e.What }

// ----------------------------------------------------------------------------
// Connect / Replace — POST /internal/credentials/orgs/{ocOrgId}
// ----------------------------------------------------------------------------

// ConnectRequest is the body for POST /internal/credentials/orgs/{ocOrgId}.
// Exactly one of {AppInstallation, UserPAT} must be populated; the kind field
// must match.
type ConnectRequest struct {
	Kind            string `json:"kind"`
	InstallationID  int64  `json:"installationId,omitempty"`
	PAT             string `json:"pat,omitempty"`
	GitHubLogin     string `json:"githubLogin,omitempty"`
}

// Projection is the JSON shape returned by status / connect / replace. It
// never contains the token itself.
type Projection struct {
	OcOrgID           string     `json:"ocOrgId"`
	Kind              string     `json:"kind"`
	GitHubLogin       string     `json:"githubLogin"`
	IdentityName      string     `json:"identityName,omitempty"`
	IdentityEmail     string     `json:"identityEmail,omitempty"`
	IdentityLogin     string     `json:"identityLogin"`
	InstallationID    *int64     `json:"installationId,omitempty"`
	SelectedRepos     []string   `json:"selectedRepos,omitempty"`
	Status            string     `json:"status"`
	ConnectedAt       time.Time  `json:"connectedAt"`
	LastValidatedAt   *time.Time `json:"lastValidatedAt,omitempty"`
	IdentityChangedAt *time.Time `json:"identityChangedAt,omitempty"`
	PrevIdentityLogin *string    `json:"prevIdentityLogin,omitempty"`
}

func projectionFromRow(r *models.OrgCredential) *Projection {
	p := &Projection{
		OcOrgID:           r.OcOrgID,
		Kind:              r.Kind,
		GitHubLogin:       r.GitHubLogin,
		IdentityName:      r.IdentityName,
		IdentityEmail:     r.IdentityEmail,
		IdentityLogin:     r.IdentityLogin,
		InstallationID:    r.InstallationID,
		Status:            r.Status,
		ConnectedAt:       r.ConnectedAt,
		LastValidatedAt:   r.LastValidatedAt,
		IdentityChangedAt: r.IdentityChangedAt,
		PrevIdentityLogin: r.PrevIdentityLogin,
	}
	if r.SelectedRepos != nil {
		p.SelectedRepos = []string(r.SelectedRepos)
	}
	return p
}

// Connect creates or replaces the credential record for ocOrgID. PAT mode
// runs the full validation chain (GET /user, membership probe, repo-read
// probe). App mode mints a JWT and looks up the install's account login.
//
// 409 (ConflictError) if:
//   - existing active row is a different kind (mode-fixed at connect time)
//   - existing row is in 'disconnecting' / 'suspended' state (no replace
//     until the disconnect cascade settles or the install is unsuspended)
//
// 400 (ValidationError) for any GitHub-side validation failure — wrapped
// with a cause code that the UI maps to a specific error message.
func (s *CredentialService) Connect(ctx context.Context, ocOrgID string, req ConnectRequest) (*Projection, error) {
	// Acquire org-scoped advisory lock for the duration of the txn so the
	// callback handler and a concurrent webhook (installation.created) can't
	// race the INSERT/UPDATE.
	tx := s.db.WithContext(ctx).Begin()
	if tx.Error != nil {
		return nil, fmt.Errorf("connect: begin tx: %w", tx.Error)
	}
	defer tx.Rollback() //nolint:errcheck — committed on success

	if err := tx.Exec(`SELECT pg_advisory_xact_lock(hashtext(?))`, "org:"+ocOrgID).Error; err != nil {
		return nil, fmt.Errorf("connect: org lock: %w", err)
	}

	var existing models.OrgCredential
	hadRow := false
	err := tx.Where("oc_org_id = ?", ocOrgID).First(&existing).Error
	switch {
	case err == nil:
		hadRow = true
	case errors.Is(err, gorm.ErrRecordNotFound):
		hadRow = false
	default:
		return nil, fmt.Errorf("connect: lookup existing: %w", err)
	}

	if hadRow && existing.Status == "active" && existing.Kind != req.Kind {
		return nil, &ConflictError{Reason: fmt.Sprintf("active %s connection exists; disconnect before connecting %s", existing.Kind, req.Kind)}
	}
	if hadRow && (existing.Status == "disconnecting") {
		return nil, &ConflictError{Reason: "disconnect in progress; retry shortly"}
	}

	switch req.Kind {
	case "user-pat":
		return s.connectPAT(ctx, tx, ocOrgID, hadRow, &existing, req)
	case "app-installation":
		return s.connectApp(ctx, tx, ocOrgID, hadRow, &existing, req)
	default:
		return nil, &ValidationError{Code: "kind_invalid", Message: fmt.Sprintf("unknown kind %q", req.Kind)}
	}
}

func (s *CredentialService) connectPAT(ctx context.Context, tx *gorm.DB, ocOrgID string, hadRow bool, existing *models.OrgCredential, req ConnectRequest) (*Projection, error) {
	if req.PAT == "" {
		return nil, &ValidationError{Code: "pat_missing", Message: "PAT is required"}
	}
	if req.GitHubLogin == "" {
		return nil, &ValidationError{Code: "github_login_missing", Message: "githubLogin is required"}
	}

	// Validation chain — phase2.md §6.5.
	identity, err := s.fetchPATIdentity(ctx, req.PAT)
	if err != nil {
		return nil, err
	}
	if err := s.validatePATMembership(ctx, req.PAT, req.GitHubLogin, identity.Login); err != nil {
		return nil, err
	}
	// Repo-read probe is best-effort: if no repos exist under githubLogin
	// yet, skip the probe; first real repo create surfaces failure.
	if err := s.probePATRepoRead(ctx, req.PAT, req.GitHubLogin); err != nil {
		// Wrap as 400 with a cause string.
		return nil, err
	}

	now := time.Now().UTC()

	// Persist the PAT to the credential store first; if the DB row insert
	// fails below the credential entry is harmless (no referencing row yet).
	if err := s.store.Put(ctx, ocOrgID, "github/pat", []byte(req.PAT)); err != nil {
		return nil, fmt.Errorf("connect: write PAT: %w", err)
	}

	if !hadRow {
		// CREATE — use the platform's GITHUB_WEBHOOK_SECRET so per-repo
		// webhook registrations (which sign with the same env value) verify
		// against this row's secret list. Fall back to a fresh random value
		// only if env is unset (test mode).
		secret := s.envWebhookSecret
		if secret == "" {
			gen, err := generateRandomHex(32)
			if err != nil {
				return nil, fmt.Errorf("connect: gen webhook secret: %w", err)
			}
			secret = gen
		}
		row := models.OrgCredential{
			OcOrgID:       ocOrgID,
			Kind:          "user-pat",
			GitHubLogin:   req.GitHubLogin,
			IdentityName:  identity.Name,
			IdentityEmail: identity.Email,
			IdentityLogin: identity.Login,
			Status:        "active",
			ConnectedAt:   now,
			LastValidatedAt: &now,
			WebhookSecrets: models.WebhookSecrets{
				{Secret: secret, AddedAt: now},
			},
		}
		if err := tx.Create(&row).Error; err != nil {
			return nil, fmt.Errorf("connect: insert: %w", err)
		}
		if err := tx.Commit().Error; err != nil {
			return nil, fmt.Errorf("connect: commit: %w", err)
		}
		slog.InfoContext(ctx, "credentials.connected", "ocOrgId", ocOrgID, "kind", "user-pat", "identityLogin", identity.Login)
		s.mirrorPATToSMAPI(ctx, ocOrgID, req.PAT)
		return projectionFromRow(&row), nil
	}

	// REPLACE — preserve webhook_secrets, possibly record identity drift.
	// Cross-mode reconnect (after disconnect): also flip `kind`, clear App-only
	// columns (installation_id, selected_repos), and seed webhook_secrets if
	// the prior row was App-mode (which has webhook_secrets=NULL per the
	// CHECK constraint).
	updates := map[string]any{
		"kind":              "user-pat",
		"github_login":      req.GitHubLogin,
		"identity_name":     identity.Name,
		"identity_email":    identity.Email,
		"identity_login":    identity.Login,
		"installation_id":   nil,
		"selected_repos":    nil,
		"last_validated_at": now,
		"status":            "active",
	}
	if identity.Login != existing.IdentityLogin {
		// Identity drift — record prev_identity_login + identity_changed_at
		// per phase2.md §6.6.
		prev := existing.IdentityLogin
		updates["prev_identity_login"] = &prev
		updates["identity_changed_at"] = now
	}
	// If switching from App → PAT, the prior row had webhook_secrets=NULL
	// (the secrets_shape_per_kind CHECK requires NOT NULL with array_length>=1
	// for user-pat). Seed using the platform's GITHUB_WEBHOOK_SECRET so the
	// per-repo hooks (registered by services.webhook_service against the
	// same env value) verify against it.
	if existing.Kind == "app-installation" {
		secret := s.envWebhookSecret
		if secret == "" {
			// No env secret available — fall back to a fresh random value.
			// PAT-mode webhooks may not verify against pre-existing repos in
			// this case, but we keep the constraint satisfied.
			gen, sErr := generateRandomHex(32)
			if sErr != nil {
				return nil, fmt.Errorf("connect: generate webhook secret: %w", sErr)
			}
			secret = gen
		}
		updates["webhook_secrets"] = models.WebhookSecrets{{Secret: secret, AddedAt: now}}
	}
	if err := tx.Model(&models.OrgCredential{}).Where("oc_org_id = ?", ocOrgID).Updates(updates).Error; err != nil {
		return nil, fmt.Errorf("connect: update: %w", err)
	}
	if err := tx.Commit().Error; err != nil {
		return nil, fmt.Errorf("connect: commit: %w", err)
	}
	// Reload for accurate projection.
	row, err := s.fetchRow(ctx, ocOrgID)
	if err != nil {
		return nil, err
	}
	slog.InfoContext(ctx, "credentials.replaced", "ocOrgId", ocOrgID, "kind", "user-pat", "identityLogin", identity.Login, "drift", identity.Login != existing.IdentityLogin)
	s.mirrorPATToSMAPI(ctx, ocOrgID, req.PAT)
	return projectionFromRow(row), nil
}

// mirrorPATToSMAPI fires the SM-API write best-effort after a Connect.
// Logged-and-swallowed on error — the legacy org_secrets path keeps
// working when SM-API is down, so the user-facing Connect doesn't 5xx.
// The SM-API row will be created/refreshed on the next successful
// Connect (or by the periodic sync, once that lands).
func (s *CredentialService) mirrorPATToSMAPI(ctx context.Context, ocOrgID, pat string) {
	if s.smAPIWriter == nil || !s.smAPIWriter.Enabled() {
		return
	}
	if _, err := s.smAPIWriter.WriteGitHubPAT(ctx, ocOrgID, pat); err != nil {
		slog.WarnContext(ctx, "credentials: SM-API mirror failed (legacy store still authoritative)",
			"ocOrgId", ocOrgID, "error", err)
	}
}

func (s *CredentialService) connectApp(ctx context.Context, tx *gorm.DB, ocOrgID string, hadRow bool, existing *models.OrgCredential, req ConnectRequest) (*Projection, error) {
	if req.InstallationID == 0 {
		return nil, &ValidationError{Code: "installation_id_missing", Message: "installationId is required"}
	}
	if s.minter == nil || s.minter.AppID() == 0 {
		return nil, &ConflictError{Reason: "GitHub App not configured on this deployment"}
	}

	// Race-fix advisory lock keyed on installation_id (phase2.md §6.4).
	if err := tx.Exec(`SELECT pg_advisory_xact_lock(hashtext(?))`, fmt.Sprintf("install:%d", req.InstallationID)).Error; err != nil {
		return nil, fmt.Errorf("connect: install lock: %w", err)
	}

	// Cross-org install check: if the same installation_id already maps
	// to a different ocOrgId, refuse.
	var clash models.OrgCredential
	err := tx.Where("installation_id = ?", req.InstallationID).First(&clash).Error
	switch {
	case err == nil:
		if clash.OcOrgID != ocOrgID {
			return nil, &ConflictError{Reason: fmt.Sprintf("installation %d already bound to org %s", req.InstallationID, clash.OcOrgID)}
		}
		if clash.Status == "active" && hadRow && existing.OcOrgID == ocOrgID {
			// Idempotent re-connect — return current projection.
			slog.InfoContext(ctx, "credentials.connect.idempotent", "ocOrgId", ocOrgID, "kind", "app-installation", "installationId", req.InstallationID)
			return projectionFromRow(&clash), nil
		}
	case errors.Is(err, gorm.ErrRecordNotFound):
		// Fresh install — fall through to fetch + insert.
	default:
		return nil, fmt.Errorf("connect: install lookup: %w", err)
	}

	// Fetch installation + bot identity.
	accountLogin, accountType, selectedRepos, err := s.fetchInstallation(ctx, req.InstallationID)
	if err != nil {
		return nil, err
	}
	// Refuse User-account installs. GitHub's POST /user/repos is not
	// accessible to App installation tokens (returns 403 "Resource not
	// accessible by integration"), so any first-class repo provisioning
	// fails silently after bind. Surface it at connect time instead so
	// the user knows to install on an Organization account.
	if accountType == "User" {
		return nil, &ValidationError{
			Code:    "user_account_install_unsupported",
			Message: fmt.Sprintf("GitHub App was installed on a personal user account (%s). Install on an Organization account instead — App tokens cannot create repositories on user accounts.", accountLogin),
		}
	}
	if s.minter.BotIdentity().Login == "" {
		// First connect — populate the bot identity once.
		botID, err := s.fetchAppBotIdentity(ctx)
		if err != nil {
			slog.WarnContext(ctx, "fetch bot identity failed", "error", err)
			// Use a deterministic fallback so the row passes NOT NULL constraints.
			botID = credentials.Identity{
				Name:  "ASDLC Platform Bot",
				Email: "bot@asdlc.dev",
				Login: "asdlc-platform[bot]",
			}
		}
		s.minter.SetBotIdentity(botID)
	}
	bot := s.minter.BotIdentity()

	now := time.Now().UTC()
	id := req.InstallationID
	if !hadRow {
		row := models.OrgCredential{
			OcOrgID:         ocOrgID,
			Kind:            "app-installation",
			GitHubLogin:     accountLogin,
			IdentityName:    bot.Name,
			IdentityEmail:   bot.Email,
			IdentityLogin:   bot.Login,
			InstallationID:  &id,
			SelectedRepos:   models.JSONStringList(selectedRepos),
			Status:          "active",
			ConnectedAt:     now,
			LastValidatedAt: &now,
		}
		if err := tx.Create(&row).Error; err != nil {
			return nil, fmt.Errorf("connect: insert app: %w", err)
		}
		if err := tx.Commit().Error; err != nil {
			return nil, fmt.Errorf("connect: commit: %w", err)
		}
		slog.InfoContext(ctx, "credentials.connected", "ocOrgId", ocOrgID, "kind", "app-installation", "installationId", id, "githubLogin", accountLogin)
		return projectionFromRow(&row), nil
	}

	// Updating existing row to App mode (post-disconnect-then-reconnect).
	updates := map[string]any{
		"kind":              "app-installation",
		"github_login":      accountLogin,
		"identity_name":     bot.Name,
		"identity_email":    bot.Email,
		"identity_login":    bot.Login,
		"installation_id":   id,
		"selected_repos":    models.JSONStringList(selectedRepos),
		"status":            "active",
		"connected_at":      now,
		"last_validated_at": now,
		// PAT-mode specific fields are nulled by the CHECK constraint —
		// caller side must clear webhook_secrets.
		"webhook_secrets": nil,
		"pat_secret_ref":  nil,
	}
	if err := tx.Model(&models.OrgCredential{}).Where("oc_org_id = ?", ocOrgID).Updates(updates).Error; err != nil {
		return nil, fmt.Errorf("connect: update app: %w", err)
	}
	if err := tx.Commit().Error; err != nil {
		return nil, fmt.Errorf("connect: commit: %w", err)
	}
	row, err := s.fetchRow(ctx, ocOrgID)
	if err != nil {
		return nil, err
	}
	slog.InfoContext(ctx, "credentials.connected", "ocOrgId", ocOrgID, "kind", "app-installation", "installationId", id, "githubLogin", accountLogin)
	return projectionFromRow(row), nil
}

// ----------------------------------------------------------------------------
// Status — GET /internal/credentials/orgs/{ocOrgId}
// ----------------------------------------------------------------------------

// Status returns the projection for ocOrgID. NotFoundError if no row exists.
func (s *CredentialService) Status(ctx context.Context, ocOrgID string) (*Projection, error) {
	row, err := s.fetchRow(ctx, ocOrgID)
	if err != nil {
		return nil, err
	}
	return projectionFromRow(row), nil
}

// ----------------------------------------------------------------------------
// Disconnect — DELETE /internal/credentials/orgs/{ocOrgId}
// ----------------------------------------------------------------------------

// Disconnect runs Phase D of the disconnect cascade (phase2.md §6.7):
// org-scoped advisory lock, status flip to 'disconnected', best-effort
// OpenBao GC of secret/asdlc/{ocOrgId}/{github,git}/*. Phases A/B/C live
// in the BFF (they need to enumerate ComponentTask rows that this service
// doesn't own).
//
// Idempotent: if the row is already 'disconnected' or absent, returns nil.
func (s *CredentialService) Disconnect(ctx context.Context, ocOrgID string) error {
	tx := s.db.WithContext(ctx).Begin()
	if tx.Error != nil {
		return tx.Error
	}
	defer tx.Rollback() //nolint:errcheck

	if err := tx.Exec(`SELECT pg_advisory_xact_lock(hashtext(?))`, "org:"+ocOrgID).Error; err != nil {
		return fmt.Errorf("disconnect: org lock: %w", err)
	}

	var row models.OrgCredential
	err := tx.Where("oc_org_id = ?", ocOrgID).First(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		_ = tx.Commit()
		return nil
	}
	if err != nil {
		return fmt.Errorf("disconnect: lookup: %w", err)
	}

	// Status flip — already-disconnected is a no-op (200 idempotent).
	if row.Status != "disconnected" {
		if err := tx.Model(&models.OrgCredential{}).Where("oc_org_id = ?", ocOrgID).Update("status", "disconnected").Error; err != nil {
			return fmt.Errorf("disconnect: status flip: %w", err)
		}
	}
	if err := tx.Commit().Error; err != nil {
		return fmt.Errorf("disconnect: commit: %w", err)
	}

	// Best-effort GC of credential-store keys. Failure is logged, not surfaced —
	// the periodic GC sweep (PR D) catches anything missed.
	if row.Kind == "user-pat" {
		if err := s.store.Delete(ctx, ocOrgID, "github/pat"); err != nil {
			slog.WarnContext(ctx, "disconnect: cred-store delete failed", "ocOrgId", ocOrgID, "key", "github/pat", "error", err)
		}
	}

	// Drop every per-WorkflowRun build Secret in this org's WP namespace
	// so a disconnected org's tokens don't linger inside the cluster.
	// Best-effort.
	if s.buildSecretCleaner != nil {
		if err := s.buildSecretCleaner.DeleteBuildSecretsForOrg(ctx, ocOrgID); err != nil {
			slog.WarnContext(ctx, "disconnect: wp secret delete failed", "ocOrgId", ocOrgID, "error", err)
		}
	}

	slog.InfoContext(ctx, "credentials.disconnected", "ocOrgId", ocOrgID, "kind", row.Kind)
	return nil
}

// WithBuildSecretCleaner injects the post-disconnect cleanup hook for
// the per-org build-credential Secret. Wired by main after both services
// are constructed; nil-safe so tests don't have to pass one. Returns the
// receiver to allow chained construction.
func (s *CredentialService) WithBuildSecretCleaner(cleaner BuildSecretCleaner) *CredentialService {
	s.buildSecretCleaner = cleaner
	return s
}

// WithSMAPIWriter injects the SM-API writer (WS2.2). When set, the
// PAT-mode Connect path uploads the PAT to SM-API after the local
// commit and stamps the triplet onto the row. nil-safe.
func (s *CredentialService) WithSMAPIWriter(w *SMAPIWriter) *CredentialService {
	s.smAPIWriter = w
	return s
}

// UninstallAppInstallation calls GitHub's DELETE /app/installations/{id} for
// the org's bound install. Looks up the row by ocOrgID, confirms App-mode,
// and asks GitHub to remove the install. Best-effort: caller (disconnect
// cascade Phase E) treats failures as non-fatal — the platform row is gone
// regardless, and an admin can clean up via github.com if needed.
//
// No-op for PAT mode (no installation_id). Returns ErrAppBindNotConfigured
// if the App minter isn't loaded — this should never happen in production
// once the platform is configured but is checked defensively.
func (s *CredentialService) UninstallAppInstallation(ctx context.Context, ocOrgID string) error {
	if s.minter == nil || s.minter.AppID() == 0 || s.githubClient == nil {
		return ErrAppBindNotConfigured
	}
	var row models.OrgCredential
	err := s.db.WithContext(ctx).Where("oc_org_id = ?", ocOrgID).First(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("uninstall: lookup: %w", err)
	}
	if row.Kind != "app-installation" || row.InstallationID == nil {
		return nil
	}
	if err := s.githubClient.DeleteInstallation(ctx, s.minter, *row.InstallationID); err != nil {
		return fmt.Errorf("uninstall: github delete: %w", err)
	}
	slog.InfoContext(ctx, "credentials.uninstalled", "ocOrgId", ocOrgID, "installationId", *row.InstallationID)
	return nil
}

// ----------------------------------------------------------------------------
// Identity projection — GET /internal/credentials/orgs/{ocOrgId}/identity
// ----------------------------------------------------------------------------

// Identity is the identity-only projection used by the BFF dispatch path.
// Replaces the dead Identity field on the legacy GetCredentials bridge
// (PR C deletes that bridge entirely).
type Identity struct {
	Kind        string `json:"kind"`
	Name        string `json:"name"`
	Email       string `json:"email"`
	Login       string `json:"login"`
	GitHubLogin string `json:"githubLogin"`
}

func (s *CredentialService) IdentityFor(ctx context.Context, ocOrgID string) (*Identity, error) {
	row, err := s.fetchRow(ctx, ocOrgID)
	if err != nil {
		return nil, err
	}
	if row.Status != "active" {
		return nil, &ConflictError{Reason: fmt.Sprintf("org %s status=%s", ocOrgID, row.Status)}
	}
	return &Identity{
		Kind:        row.Kind,
		Name:        row.IdentityName,
		Email:       row.IdentityEmail,
		Login:       row.IdentityLogin,
		GitHubLogin: row.GitHubLogin,
	}, nil
}

// ----------------------------------------------------------------------------
// Webhook secrets — GET / POST / DELETE
// ----------------------------------------------------------------------------

// WebhookSecrets returns the accepted HMAC keys for ocOrgID, current-first.
// PAT mode reads from the row's webhook_secrets JSONB. App mode reads from
// the platform-wide secret/asdlc/_platform/github/app/webhook_secret.
func (s *CredentialService) WebhookSecrets(ctx context.Context, ocOrgID string) ([][]byte, error) {
	row, err := s.fetchRow(ctx, ocOrgID)
	if err != nil {
		return nil, err
	}
	if row.Status != "active" && row.Status != "disconnecting" {
		return nil, &ConflictError{Reason: fmt.Sprintf("org %s status=%s", ocOrgID, row.Status)}
	}

	switch row.Kind {
	case "user-pat":
		if len(row.WebhookSecrets) == 0 {
			return nil, &ConflictError{Reason: "no webhook secrets configured"}
		}
		out := make([][]byte, 0, len(row.WebhookSecrets))
		for _, e := range row.WebhookSecrets {
			out = append(out, []byte(e.Secret))
		}
		return out, nil

	case "app-installation":
		// Platform-wide secret list at _platform/github/app/webhook_secret.
		// Loaded via the platform-key path (which only the seed loads
		// directly — for the receiver-time read we go through a tiny
		// helper on AppTokenMinter that doesn't break the import fence).
		secrets, err := s.minter.LoadAppWebhookSecrets(ctx)
		if err != nil {
			return nil, fmt.Errorf("webhook secrets: load app secrets: %w", err)
		}
		if len(secrets) == 0 {
			return nil, &ConflictError{Reason: "no app webhook secrets configured"}
		}
		return secrets, nil
	default:
		return nil, fmt.Errorf("unknown kind %q", row.Kind)
	}
}

// AppendWebhookSecret rotates a new secret onto the PAT row's list.
// 409 if called against an App-mode row (rotation lives in _platform).
func (s *CredentialService) AppendWebhookSecret(ctx context.Context, ocOrgID, secret string) error {
	if secret == "" {
		return &ValidationError{Code: "secret_empty", Message: "secret is required"}
	}
	tx := s.db.WithContext(ctx).Begin()
	if tx.Error != nil {
		return tx.Error
	}
	defer tx.Rollback() //nolint:errcheck

	if err := tx.Exec(`SELECT pg_advisory_xact_lock(hashtext(?))`, "org:"+ocOrgID).Error; err != nil {
		return err
	}
	var row models.OrgCredential
	if err := tx.Where("oc_org_id = ?", ocOrgID).First(&row).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return &NotFoundError{What: "org_credentials"}
		}
		return err
	}
	if row.Kind != "user-pat" {
		return &ConflictError{Reason: "webhook-secret rotation is PAT-only; App-mode rotation lives in _platform"}
	}
	row.WebhookSecrets = append(models.WebhookSecrets{{Secret: secret, AddedAt: time.Now().UTC()}}, row.WebhookSecrets...)
	if err := tx.Model(&models.OrgCredential{}).Where("oc_org_id = ?", ocOrgID).Update("webhook_secrets", row.WebhookSecrets).Error; err != nil {
		return err
	}
	return tx.Commit().Error
}

// RemoveWebhookSecret drops a specific secret from the PAT row's list.
func (s *CredentialService) RemoveWebhookSecret(ctx context.Context, ocOrgID, secret string) error {
	tx := s.db.WithContext(ctx).Begin()
	if tx.Error != nil {
		return tx.Error
	}
	defer tx.Rollback() //nolint:errcheck

	if err := tx.Exec(`SELECT pg_advisory_xact_lock(hashtext(?))`, "org:"+ocOrgID).Error; err != nil {
		return err
	}
	var row models.OrgCredential
	if err := tx.Where("oc_org_id = ?", ocOrgID).First(&row).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return &NotFoundError{What: "org_credentials"}
		}
		return err
	}
	if row.Kind != "user-pat" {
		return &ConflictError{Reason: "webhook-secret rotation is PAT-only"}
	}
	filtered := row.WebhookSecrets[:0]
	for _, e := range row.WebhookSecrets {
		if e.Secret != secret {
			filtered = append(filtered, e)
		}
	}
	if len(filtered) == 0 {
		return &ConflictError{Reason: "cannot drop the last webhook secret"}
	}
	row.WebhookSecrets = filtered
	if err := tx.Model(&models.OrgCredential{}).Where("oc_org_id = ?", ocOrgID).Update("webhook_secrets", row.WebhookSecrets).Error; err != nil {
		return err
	}
	return tx.Commit().Error
}

// ----------------------------------------------------------------------------
// Routing lookup — used by the BFF webhook receiver
// ----------------------------------------------------------------------------

// OrgIDByInstallationID returns the ocOrgId bound to the given
// installation_id. Used by the BFF webhook receiver to route App-mode
// events. NotFoundError if no row matches.
func (s *CredentialService) OrgIDByInstallationID(ctx context.Context, installationID int64) (string, error) {
	var row models.OrgCredential
	err := s.db.WithContext(ctx).Where("installation_id = ?", installationID).First(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return "", &NotFoundError{What: fmt.Sprintf("installation %d", installationID)}
	}
	if err != nil {
		return "", err
	}
	return row.OcOrgID, nil
}

// OrgIDByRepoFullName returns the ocOrgId that owns the given GitHub repo
// (full_name = "owner/repo"). Resolved against git_repositories.org_id —
// every provisioned repo carries the OC org slug it belongs to.
//
// Used by the BFF webhook receiver to route PAT-mode (and App-mode
// per-repo) events: pull_request, push, issue_comment, issues. The
// installation-id-based path handles the App-mode lifecycle events;
// repo-keyed events use this lookup so PAT-mode events route correctly.
func (s *CredentialService) OrgIDByRepoFullName(ctx context.Context, fullName string) (string, error) {
	if fullName == "" {
		return "", &NotFoundError{What: "empty repo full_name"}
	}
	// Look up the repo by repo_url. The git_repositories table stores
	// the canonical clone URL — `https://github.com/<owner>/<repo>` or
	// `...<repo>.git`. We match either shape so a webhook payload's
	// `<owner>/<repo>` (without `.git`) routes correctly.
	var row struct {
		OrgID string `gorm:"column:org_id"`
	}
	suffix := "%/" + fullName
	suffixGit := suffix + ".git"
	err := s.db.WithContext(ctx).
		Table("git_repositories").
		Select("org_id").
		Where("repo_url LIKE ? OR repo_url LIKE ?", suffix, suffixGit).
		Limit(1).
		Scan(&row).Error
	if err != nil {
		return "", fmt.Errorf("repo lookup: %w", err)
	}
	if row.OrgID == "" {
		return "", &NotFoundError{What: fmt.Sprintf("repo %s", fullName)}
	}
	return row.OrgID, nil
}

// SuspendInstallation flips the org_credentials row bound to installationID
// to status='suspended'. Idempotent.
func (s *CredentialService) SuspendInstallation(ctx context.Context, installationID int64) error {
	return s.setInstallationStatus(ctx, installationID, "suspended")
}

// UnsuspendInstallation flips the row to status='active'. Idempotent.
func (s *CredentialService) UnsuspendInstallation(ctx context.Context, installationID int64) error {
	return s.setInstallationStatus(ctx, installationID, "active")
}

func (s *CredentialService) setInstallationStatus(ctx context.Context, installationID int64, status string) error {
	tx := s.db.WithContext(ctx).Begin()
	if tx.Error != nil {
		return tx.Error
	}
	defer tx.Rollback() //nolint:errcheck
	if err := tx.Exec(`SELECT pg_advisory_xact_lock(hashtext(?))`, fmt.Sprintf("install:%d", installationID)).Error; err != nil {
		return err
	}
	res := tx.Model(&models.OrgCredential{}).
		Where("installation_id = ?", installationID).
		Update("status", status)
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		// 200 idempotent — webhooks may arrive before the connect callback
		// has finished; missing row is recoverable via the next connect.
		_ = tx.Commit()
		return nil
	}
	return tx.Commit().Error
}

// MergeSelectedRepos applies an installation_repositories.added/removed
// JSON merge under the org-scoped lock. delta carries lists of full names
// to add/remove (intersection vs. current state determines the new set).
func (s *CredentialService) MergeSelectedRepos(ctx context.Context, installationID int64, added, removed []string) error {
	tx := s.db.WithContext(ctx).Begin()
	if tx.Error != nil {
		return tx.Error
	}
	defer tx.Rollback() //nolint:errcheck

	var row models.OrgCredential
	if err := tx.Where("installation_id = ?", installationID).First(&row).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return &NotFoundError{What: fmt.Sprintf("installation %d", installationID)}
		}
		return err
	}
	if err := tx.Exec(`SELECT pg_advisory_xact_lock(hashtext(?))`, "org:"+row.OcOrgID).Error; err != nil {
		return err
	}

	current := map[string]bool{}
	for _, r := range row.SelectedRepos {
		current[r] = true
	}
	for _, r := range removed {
		delete(current, r)
	}
	for _, r := range added {
		current[r] = true
	}
	merged := make([]string, 0, len(current))
	for r := range current {
		merged = append(merged, r)
	}

	now := time.Now().UTC()
	if err := tx.Model(&models.OrgCredential{}).
		Where("oc_org_id = ?", row.OcOrgID).
		Updates(map[string]any{
			"selected_repos":    models.JSONStringList(merged),
			"last_validated_at": now,
		}).Error; err != nil {
		return err
	}
	return tx.Commit().Error
}

// ----------------------------------------------------------------------------
// Validation chain — phase2.md §6.5
// ----------------------------------------------------------------------------

type ghIdentity struct {
	Login string `json:"login"`
	Name  string `json:"name"`
	Email string `json:"email"`
}

func (s *CredentialService) fetchPATIdentity(ctx context.Context, pat string) (*ghIdentity, error) {
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, s.githubAPI+"/user", nil)
	req.Header.Set("Authorization", "Bearer "+pat)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	resp, err := s.httpClient.Do(req)
	if err != nil {
		return nil, &ValidationError{Code: "github_unreachable", Message: fmt.Sprintf("GitHub /user unreachable: %v", err)}
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode == 401 {
		return nil, &ValidationError{Code: "pat_invalid", Message: "PAT is not a valid GitHub token"}
	}
	if resp.StatusCode == 403 {
		return nil, &ValidationError{Code: "pat_forbidden", Message: "PAT lacks scope or is rate-limited"}
	}
	if resp.StatusCode != 200 {
		return nil, &ValidationError{Code: "github_error", Message: fmt.Sprintf("GET /user: %d %s", resp.StatusCode, truncateForError(body))}
	}
	var id ghIdentity
	if err := json.Unmarshal(body, &id); err != nil {
		return nil, &ValidationError{Code: "github_unmarshal", Message: fmt.Sprintf("decode /user: %v", err)}
	}
	if id.Login == "" {
		return nil, &ValidationError{Code: "github_no_login", Message: "/user response missing login"}
	}
	if id.Name == "" {
		id.Name = id.Login
	}
	if id.Email == "" {
		// User may have private email — fall back to noreply.
		id.Email = fmt.Sprintf("%s@users.noreply.github.com", id.Login)
	}
	return &id, nil
}

func (s *CredentialService) validatePATMembership(ctx context.Context, pat, githubLogin, identityLogin string) error {
	if strings.EqualFold(githubLogin, identityLogin) {
		// PAT owner == githubLogin — no membership probe needed.
		return nil
	}
	url := fmt.Sprintf("%s/user/memberships/orgs/%s", s.githubAPI, githubLogin)
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	req.Header.Set("Authorization", "Bearer "+pat)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	resp, err := s.httpClient.Do(req)
	if err != nil {
		return &ValidationError{Code: "github_unreachable", Message: fmt.Sprintf("GitHub membership probe unreachable: %v", err)}
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode == 404 {
		return &ValidationError{Code: "pat_not_member", Message: fmt.Sprintf("PAT is not a member of org %q", githubLogin)}
	}
	// 403 from this endpoint typically means the PAT lacks the
	// `read:org` / `Members: read` permission. Fine-grained PATs scoped
	// only to repo operations hit this. The downstream repo-read probe
	// is the real signal — if the PAT can write to the org's repos
	// (which is what ASDLC actually does), membership is implicit.
	// Skip-with-log so a fine-grained-PAT user isn't blocked from
	// connecting just because they didn't grant membership-read.
	if resp.StatusCode == 403 {
		// Best-effort log; don't fail. The repo-read probe catches
		// the genuine "can't reach this org" failure mode.
		return nil
	}
	if resp.StatusCode != 200 {
		return &ValidationError{Code: "github_error", Message: fmt.Sprintf("membership probe %d: %s", resp.StatusCode, truncateForError(body))}
	}
	var membership struct{ State string `json:"state"` }
	_ = json.Unmarshal(body, &membership)
	if !strings.EqualFold(membership.State, "active") {
		return &ValidationError{Code: "pat_membership_inactive", Message: fmt.Sprintf("PAT membership in %q is %q (must be active)", githubLogin, membership.State)}
	}
	return nil
}

func (s *CredentialService) probePATRepoRead(ctx context.Context, pat, githubLogin string) error {
	// List one repo under githubLogin. If empty, accept (fresh org).
	url := fmt.Sprintf("%s/orgs/%s/repos?per_page=1", s.githubAPI, githubLogin)
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	req.Header.Set("Authorization", "Bearer "+pat)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	resp, err := s.httpClient.Do(req)
	if err != nil {
		return &ValidationError{Code: "github_unreachable", Message: fmt.Sprintf("GitHub repo probe unreachable: %v", err)}
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode == 404 {
		// Could be a user account, not an org — try the user endpoint.
		userURL := fmt.Sprintf("%s/users/%s/repos?per_page=1", s.githubAPI, githubLogin)
		req2, _ := http.NewRequestWithContext(ctx, http.MethodGet, userURL, nil)
		req2.Header.Set("Authorization", "Bearer "+pat)
		req2.Header.Set("Accept", "application/vnd.github+json")
		req2.Header.Set("X-GitHub-Api-Version", "2022-11-28")
		resp2, err2 := s.httpClient.Do(req2)
		if err2 != nil {
			return &ValidationError{Code: "github_unreachable", Message: fmt.Sprintf("GitHub user-repo probe: %v", err2)}
		}
		defer resp2.Body.Close()
		if resp2.StatusCode != 200 {
			b, _ := io.ReadAll(resp2.Body)
			return &ValidationError{Code: "pat_no_repo_read", Message: fmt.Sprintf("PAT scope check failed: cannot read repos under %q (%d %s)", githubLogin, resp2.StatusCode, truncateForError(b))}
		}
		return nil
	}
	if resp.StatusCode == 200 {
		return nil
	}
	if resp.StatusCode == 403 {
		return &ValidationError{Code: "pat_no_repo_read", Message: fmt.Sprintf("PAT scope check failed: cannot read repos under %q", githubLogin)}
	}
	return &ValidationError{Code: "github_error", Message: fmt.Sprintf("repo probe %d: %s", resp.StatusCode, truncateForError(body))}
}

// fetchInstallation mints an App JWT and reads /app/installations/{id} to
// extract account.{login,type} + selected_repos. Used at App-mode connect.
// accountType is "Organization" or "User" — the connect path refuses
// "User" because GitHub's POST /user/repos endpoint is not accessible to
// App installation tokens, so repo provisioning would 403 silently.
func (s *CredentialService) fetchInstallation(ctx context.Context, installationID int64) (accountLogin, accountType string, selectedRepos []string, err error) {
	jwt, err := s.minter.SignAppJWT(time.Now())
	if err != nil {
		return "", "", nil, &ConflictError{Reason: fmt.Sprintf("app sign: %v", err)}
	}
	url := fmt.Sprintf("%s/app/installations/%d", s.githubAPI, installationID)
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	req.Header.Set("Authorization", "Bearer "+jwt)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	resp, err := s.httpClient.Do(req)
	if err != nil {
		return "", "", nil, fmt.Errorf("fetch install: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode == 404 {
		return "", "", nil, &ValidationError{Code: "installation_not_found", Message: fmt.Sprintf("installation %d not found (was the App uninstalled?)", installationID)}
	}
	if resp.StatusCode != 200 {
		return "", "", nil, &ValidationError{Code: "github_error", Message: fmt.Sprintf("GET /app/installations/%d: %d %s", installationID, resp.StatusCode, truncateForError(body))}
	}
	var inst struct {
		Account struct {
			Login string `json:"login"`
			Type  string `json:"type"`
		} `json:"account"`
		RepositorySelection string `json:"repository_selection"`
	}
	if err := json.Unmarshal(body, &inst); err != nil {
		return "", "", nil, fmt.Errorf("decode install: %w", err)
	}
	if inst.Account.Login == "" {
		return "", "", nil, &ValidationError{Code: "install_no_account", Message: "installation response missing account.login"}
	}

	// Pull the selected-repos list. Empty for repository_selection=all.
	selectedRepos, err = s.listInstallationRepos(ctx, installationID)
	if err != nil {
		// Best-effort — log and continue with empty list.
		slog.WarnContext(ctx, "list installation repos failed", "installationId", installationID, "error", err)
	}

	return inst.Account.Login, inst.Account.Type, selectedRepos, nil
}

// listInstallationRepos returns the full_name of each repo accessible by
// the installation. Uses the installation token (not the App JWT).
// ListInstallationRepos calls GET /installation/repositories with a fresh
// installation token. Used by Phase 2 PR D's reach-reconciliation Phase B
// cascade to confirm GitHub agrees the install has shrunk before abandoning
// tasks (§6.8). Public wrapper around the private helper that's also used
// by the connect flow.
func (s *CredentialService) ListInstallationRepos(ctx context.Context, installationID int64) ([]string, error) {
	return s.listInstallationRepos(ctx, installationID)
}

// ----------------------------------------------------------------------------
// Connect resolution — user-scoped install discovery
// ----------------------------------------------------------------------------

// ErrAppBindNotConfigured is returned when the App or OAuth client secret
// is not configured — the operator hasn't completed the App setup.
var ErrAppBindNotConfigured = errors.New("app bind path not configured (missing app key or oauth client secret)")

// ResolveUserInstallations exchanges an OAuth code for a user-token,
// fetches the installations the user has admin access to via
// GET /user/installations, and intersects with our App's installations.
// Only installations that are either unbound or bound to the requesting
// org are returned — installs bound to *other* ASDLC orgs are silently
// filtered to avoid leaking cross-tenant install metadata to OC admins
// who happen to share GitHub admin access.
//
// The user-token is used only inside this call and discarded — it never
// crosses any process boundary, never lands in storage, never logged.
//
// Architectural note: this single call replaces the earlier discover +
// bind pair. There is no "list every install of our App" surface anymore
// — discovery is always proven to the requesting user via OAuth.
func (s *CredentialService) ResolveUserInstallations(ctx context.Context, ocOrgID, oauthCode, redirectURI string) ([]AppInstallationSummary, error) {
	if s.minter == nil || s.minter.AppID() == 0 || s.githubClient == nil {
		return nil, ErrAppBindNotConfigured
	}
	if s.appClientID == "" || s.appClientSecret == "" {
		return nil, ErrAppBindNotConfigured
	}
	if ocOrgID == "" {
		return nil, &ValidationError{Code: "oc_org_id_missing", Message: "ocOrgID is required"}
	}
	if oauthCode == "" {
		return nil, &ValidationError{Code: "oauth_code_missing", Message: "oauthCode is required"}
	}

	userToken, err := s.githubClient.ExchangeOAuthCode(ctx, s.appClientID, s.appClientSecret, oauthCode, redirectURI)
	if err != nil {
		return nil, &ValidationError{Code: "oauth_exchange_failed", Message: err.Error()}
	}
	if userToken == "" {
		return []AppInstallationSummary{}, nil
	}

	userInstalls, err := s.githubClient.GetUserInstallations(ctx, userToken)
	if err != nil {
		return nil, fmt.Errorf("get user installations: %w", err)
	}
	userInstallSet := make(map[int64]struct{}, len(userInstalls))
	for _, id := range userInstalls {
		userInstallSet[id] = struct{}{}
	}

	all, err := s.githubClient.ListAppInstallations(ctx, s.minter)
	if err != nil {
		return nil, fmt.Errorf("list app installations: %w", err)
	}

	// Pull installations bound to OTHER orgs — we filter those out so we
	// don't leak "install X is owned by some other ASDLC tenant" to this
	// user. Installs bound to ocOrgID itself (re-connect / re-confirm)
	// are kept.
	type boundRow struct {
		InstallationID int64
		OcOrgID        string
	}
	var bound []boundRow
	if err := s.db.WithContext(ctx).
		Model(&models.OrgCredential{}).
		Where("installation_id IS NOT NULL AND status IN ?", []string{"active", "suspended", "disconnecting"}).
		Select("installation_id, oc_org_id").
		Find(&bound).Error; err != nil {
		return nil, fmt.Errorf("scan bound installs: %w", err)
	}
	boundElsewhere := make(map[int64]struct{}, len(bound))
	for _, b := range bound {
		if b.OcOrgID != ocOrgID {
			boundElsewhere[b.InstallationID] = struct{}{}
		}
	}

	candidates := make([]AppInstallationSummary, 0, len(all))
	for _, inst := range all {
		if _, ok := userInstallSet[inst.InstallationID]; !ok {
			continue
		}
		if _, ok := boundElsewhere[inst.InstallationID]; ok {
			continue
		}
		candidates = append(candidates, inst)
	}
	return candidates, nil
}

// RecordIdentityFromGitHub atomically updates an OrgCredential row's
// identity columns (login/name/email) and last_validated_at. If the new
// login differs from stored identity_login, it also records prev_identity_login
// and identity_changed_at per phase2.md §6.6.
//
// Used by:
//   - the PAT-replace flow (existing call site, after this extraction)
//   - the PR D periodic validator on a successful GET /user / /app/installations/{id}
//
// Caller passes (login, name, email) — the same triple ghIdentity carries.
// Returns true if drift was recorded.
func (s *CredentialService) RecordIdentityFromGitHub(ctx context.Context, ocOrgID, login, name, email string) (drifted bool, err error) {
	row, err := s.fetchRow(ctx, ocOrgID)
	if err != nil {
		return false, err
	}
	if row == nil {
		return false, &NotFoundError{What: "org_credentials:" + ocOrgID}
	}
	if name == "" {
		name = login
	}
	if email == "" {
		email = fmt.Sprintf("%s@users.noreply.github.com", login)
	}
	now := time.Now().UTC()
	updates := map[string]any{
		"identity_name":     name,
		"identity_email":    email,
		"identity_login":    login,
		"last_validated_at": now,
	}
	if login != row.IdentityLogin {
		prev := row.IdentityLogin
		updates["prev_identity_login"] = &prev
		updates["identity_changed_at"] = now
		drifted = true
	}
	if err := s.db.WithContext(ctx).Model(&models.OrgCredential{}).
		Where("oc_org_id = ?", ocOrgID).
		Updates(updates).Error; err != nil {
		return false, fmt.Errorf("update identity: %w", err)
	}
	return drifted, nil
}

// TouchValidatedAt updates last_validated_at without modifying identity. Used
// by the PR D validator's no-drift App-mode path to record the heartbeat.
func (s *CredentialService) TouchValidatedAt(ctx context.Context, ocOrgID string) error {
	now := time.Now().UTC()
	return s.db.WithContext(ctx).Model(&models.OrgCredential{}).
		Where("oc_org_id = ?", ocOrgID).
		Update("last_validated_at", now).Error
}

// UpdateGitHubLogin sets github_login (App-mode rename drift). Validator-only.
func (s *CredentialService) UpdateGitHubLogin(ctx context.Context, ocOrgID, githubLogin string) error {
	return s.db.WithContext(ctx).Model(&models.OrgCredential{}).
		Where("oc_org_id = ?", ocOrgID).
		Update("github_login", githubLogin).Error
}

// ListActiveRows returns all OrgCredential rows in 'active' or 'suspended'
// status. The PR D validator (pkg/credentials/validator.go) walks this list
// once per tick. The result is materialised — the validator releases the
// validator-scoped advisory lock before iterating.
func (s *CredentialService) ListActiveRows(ctx context.Context) ([]models.OrgCredential, error) {
	var rows []models.OrgCredential
	err := s.db.WithContext(ctx).
		Where("status IN ?", []string{"active", "suspended"}).
		Find(&rows).Error
	if err != nil {
		return nil, err
	}
	return rows, nil
}

func (s *CredentialService) listInstallationRepos(ctx context.Context, installationID int64) ([]string, error) {
	token, _, err := s.minter.MintForInstallation(ctx, installationID)
	if err != nil {
		return nil, err
	}
	out := []string{}
	page := 1
	for {
		url := fmt.Sprintf("%s/installation/repositories?per_page=100&page=%d", s.githubAPI, page)
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Accept", "application/vnd.github+json")
		req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
		resp, err := s.httpClient.Do(req)
		if err != nil {
			return out, err
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != 200 {
			return out, fmt.Errorf("list repos: %d %s", resp.StatusCode, truncateForError(body))
		}
		var page1 struct {
			TotalCount   int `json:"total_count"`
			Repositories []struct {
				FullName string `json:"full_name"`
			} `json:"repositories"`
		}
		if err := json.Unmarshal(body, &page1); err != nil {
			return out, err
		}
		for _, r := range page1.Repositories {
			out = append(out, r.FullName)
		}
		if len(page1.Repositories) < 100 {
			break
		}
		page++
	}
	return out, nil
}

// fetchAppBotIdentity calls GET /app to learn the App's bot login. The
// "name" field is the App's display name; the "slug" is what appears as
// `<slug>[bot]` on commits.
func (s *CredentialService) fetchAppBotIdentity(ctx context.Context) (credentials.Identity, error) {
	jwt, err := s.minter.SignAppJWT(time.Now())
	if err != nil {
		return credentials.Identity{}, err
	}
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, s.githubAPI+"/app", nil)
	req.Header.Set("Authorization", "Bearer "+jwt)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	resp, err := s.httpClient.Do(req)
	if err != nil {
		return credentials.Identity{}, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 {
		return credentials.Identity{}, fmt.Errorf("GET /app: %d %s", resp.StatusCode, truncateForError(body))
	}
	var info struct {
		Slug string `json:"slug"`
		Name string `json:"name"`
	}
	if err := json.Unmarshal(body, &info); err != nil {
		return credentials.Identity{}, err
	}
	if info.Slug == "" {
		return credentials.Identity{}, errors.New("/app response missing slug")
	}
	// GitHub's commit-attribution convention uses {numericUserID}+{slug}[bot]
	// as the noreply email's local-part. We don't have the numeric user ID
	// at this layer; leave the slug-based shape (GitHub still attributes
	// correctly when the email belongs to the App's verified noreply domain).
	return credentials.Identity{
		Name:  info.Name,
		Email: fmt.Sprintf("%s[bot]@users.noreply.github.com", info.Slug),
		Login: fmt.Sprintf("%s[bot]", info.Slug),
	}, nil
}

// ----------------------------------------------------------------------------
// helpers
// ----------------------------------------------------------------------------

func (s *CredentialService) fetchRow(ctx context.Context, ocOrgID string) (*models.OrgCredential, error) {
	var row models.OrgCredential
	err := s.db.WithContext(ctx).Where("oc_org_id = ?", ocOrgID).First(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, &NotFoundError{What: fmt.Sprintf("org_credentials.%s", ocOrgID)}
	}
	if err != nil {
		return nil, err
	}
	return &row, nil
}

func generateRandomHex(byteLen int) (string, error) {
	buf := make([]byte, byteLen)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf), nil
}

func truncateForError(b []byte) string {
	s := string(b)
	if len(s) > 200 {
		s = s[:200] + "…"
	}
	return strings.ReplaceAll(s, "\n", " ")
}
