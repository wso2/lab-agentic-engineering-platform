// Package services — Anthropic credential service.
//
// AnthropicCredentialService owns the per-org Anthropic API key surface:
//
//   - Connect / Status / Disconnect (POST/GET/DELETE /internal/credentials/orgs/{org}/anthropic)
//   - EffectiveKey (GET .../anthropic/effective-key) — returns the org key
//     or the platform fallback, used by agents-service per-call
//   - ApplyWPSecret (POST .../anthropic/apply-wp-secret) — refreshes the
//     per-org K8s Secret in workflows-<ocOrgID> with the freshest value
//     from `org_secrets`. Same model as MintBuildToken's per-dispatch
//     SSA — see build_credentials_service.go.
//
// Secret bytes live in the same `org_secrets` (Postgres + AES-256-GCM)
// table as the GitHub PAT, keyed by `anthropic/key`. The metadata
// (prefix / last4 / status / connected_at / last_validated_at) lives in
// the new `org_anthropic_credentials` table.
//
// See docs/design/anthropic-key-dual-token.md.
package services

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"gorm.io/gorm"

	"github.com/wso2/asdlc/asdlc-service/clients/k8s"
	"github.com/wso2/asdlc/asdlc-service/models"
	"github.com/wso2/asdlc/asdlc-service/internal/credentials"
)

// AnthropicCredentialService — see package doc.
type AnthropicCredentialService struct {
	db            *gorm.DB
	store         credentials.OpenBaoStore
	wpClient      client.Client
	platformKey   string
	anthropicAPI  string // "https://api.anthropic.com" by default; overridden in tests
	invalidator   AgentsCacheInvalidator
	httpClient    *http.Client

	// smAPIWriter mirrors the key into SM-API on Connect (WS2.2). nil-safe.
	smAPIWriter *SMAPIWriter
}

// WithSMAPIWriter injects the SM-API writer; chainable. nil disables
// the mirror — the legacy org_secrets path remains authoritative.
func (s *AnthropicCredentialService) WithSMAPIWriter(w *SMAPIWriter) *AnthropicCredentialService {
	s.smAPIWriter = w
	return s
}

// AgentsCacheInvalidator is the in-process hook that broadcasts a
// `Connect / Disconnect` event to agents-service so its 5-min effective-
// key LRU drops the orgId immediately. Implementations live next to the
// service wiring in `cmd/git-service/main.go`. nil-safe — calls are
// best-effort.
type AgentsCacheInvalidator interface {
	Invalidate(ctx context.Context, ocOrgID string) error
}

// NewAnthropicCredentialService wires the service. db, store must be
// non-nil; platformKey may be empty (no fallback); wpClient may be nil
// (off-cluster degraded mode — same shape as BuildCredentialsService).
func NewAnthropicCredentialService(
	db *gorm.DB,
	store credentials.OpenBaoStore,
	wpClient client.Client,
	platformKey string,
	invalidator AgentsCacheInvalidator,
) *AnthropicCredentialService {
	return &AnthropicCredentialService{
		db:           db,
		store:        store,
		wpClient:     wpClient,
		platformKey:  platformKey,
		anthropicAPI: "https://api.anthropic.com",
		invalidator:  invalidator,
		httpClient:   &http.Client{Timeout: 15 * time.Second},
	}
}

// ----------------------------------------------------------------------------
// Errors
// ----------------------------------------------------------------------------

// ErrAnthropicKeyRequired signals that no per-org key is configured and
// the caller specifically required one (dispatch path). Distinct from
// returning the platform fallback. Wrap with status 422 at the API edge.
var ErrAnthropicKeyRequired = errors.New("anthropic: org key required")

// ----------------------------------------------------------------------------
// Projection — what the API + console see
// ----------------------------------------------------------------------------

type AnthropicProjection struct {
	OcOrgID         string     `json:"ocOrgId"`
	KeyPrefix       string     `json:"keyPrefix"`
	KeyLast4        string     `json:"keyLast4"`
	Status          string     `json:"status"`
	ConnectedAt     time.Time  `json:"connectedAt"`
	LastValidatedAt *time.Time `json:"lastValidatedAt,omitempty"`
	ValidationError *string    `json:"validationError,omitempty"`
}

func projectionFromAnthropicRow(r *models.OrgAnthropicCredential) *AnthropicProjection {
	return &AnthropicProjection{
		OcOrgID:         r.OcOrgID,
		KeyPrefix:       r.KeyPrefix,
		KeyLast4:        r.KeyLast4,
		Status:          r.Status,
		ConnectedAt:     r.ConnectedAt,
		LastValidatedAt: r.LastValidatedAt,
		ValidationError: r.ValidationError,
	}
}

// ----------------------------------------------------------------------------
// Connect / Replace
// ----------------------------------------------------------------------------

// AnthropicConnectRequest is the body for POST /internal/credentials/orgs/{org}/anthropic.
type AnthropicConnectRequest struct {
	APIKey string `json:"apiKey"`
}

// Connect validates the supplied key against Anthropic, persists it in
// `org_secrets` (AES-256-GCM), upserts the metadata row, and broadcasts
// a cache invalidate to agents-service. Idempotent under the org-scoped
// advisory lock — concurrent Connects produce one consistent row.
//
// Does NOT touch the workflow-plane namespace; the K8s Secret is materialised
// lazily on first dispatch via ApplyWPSecret.
func (s *AnthropicCredentialService) Connect(ctx context.Context, ocOrgID string, req AnthropicConnectRequest) (*AnthropicProjection, error) {
	key := strings.TrimSpace(req.APIKey)
	if key == "" {
		return nil, &ValidationError{Code: "anthropic_key_missing", Message: "apiKey is required"}
	}
	if !looksLikeAnthropicKey(key) {
		return nil, &ValidationError{Code: "anthropic_key_invalid", Message: "API key does not look like an Anthropic key (expected prefix 'sk-ant-')"}
	}

	if err := s.validateAnthropicKey(ctx, key); err != nil {
		return nil, err
	}

	now := time.Now().UTC()
	prefix, last4 := anthropicKeyPreview(key)

	tx := s.db.WithContext(ctx).Begin()
	if tx.Error != nil {
		return nil, fmt.Errorf("anthropic connect: begin tx: %w", tx.Error)
	}
	defer tx.Rollback() //nolint:errcheck

	if err := tx.Exec(`SELECT pg_advisory_xact_lock(hashtext(?))`, "org_anthropic:"+ocOrgID).Error; err != nil {
		return nil, fmt.Errorf("anthropic connect: lock: %w", err)
	}

	// Encrypted bytes — same KV store the GitHub PAT uses.
	if err := s.store.Put(ctx, ocOrgID, "anthropic/key", []byte(key)); err != nil {
		return nil, fmt.Errorf("anthropic connect: store put: %w", err)
	}

	row := models.OrgAnthropicCredential{
		OcOrgID:         ocOrgID,
		KeyPrefix:       prefix,
		KeyLast4:        last4,
		Status:          "active",
		ConnectedAt:     now,
		LastValidatedAt: &now,
		ValidationError: nil,
	}
	// Upsert via ON CONFLICT DO UPDATE so Replace is idempotent.
	if err := tx.Exec(`
		INSERT INTO org_anthropic_credentials
		    (oc_org_id, key_prefix, key_last4, status, connected_at, last_validated_at, validation_error)
		VALUES (?, ?, ?, ?, ?, ?, NULL)
		ON CONFLICT (oc_org_id) DO UPDATE
		  SET key_prefix         = EXCLUDED.key_prefix,
		      key_last4          = EXCLUDED.key_last4,
		      status             = EXCLUDED.status,
		      last_validated_at  = EXCLUDED.last_validated_at,
		      validation_error   = NULL`,
		row.OcOrgID, row.KeyPrefix, row.KeyLast4, row.Status, row.ConnectedAt, row.LastValidatedAt,
	).Error; err != nil {
		return nil, fmt.Errorf("anthropic connect: upsert: %w", err)
	}
	if err := tx.Commit().Error; err != nil {
		return nil, fmt.Errorf("anthropic connect: commit: %w", err)
	}

	// Best-effort cache invalidate. Failures are logged but don't fail the
	// connect — the 5-min TTL bounds staleness.
	s.broadcastInvalidate(ctx, ocOrgID)

	// WS2.2 — best-effort SM-API mirror. Same posture as
	// CredentialService.mirrorPATToSMAPI: legacy store stays authoritative
	// when SM-API is unavailable; the row's SM-API triplet stays NULL
	// until the next successful Connect.
	if s.smAPIWriter != nil && s.smAPIWriter.Enabled() {
		if _, err := s.smAPIWriter.WriteAnthropic(ctx, ocOrgID, key); err != nil {
			slog.WarnContext(ctx, "anthropic: SM-API mirror failed (legacy store still authoritative)",
				"ocOrgId", ocOrgID, "error", err)
		}
	}

	slog.InfoContext(ctx, "anthropic.connected", "ocOrgId", ocOrgID, "keyPrefix", prefix)
	return projectionFromAnthropicRow(&row), nil
}

// ----------------------------------------------------------------------------
// Status
// ----------------------------------------------------------------------------

// Status returns the projection for ocOrgID. Returns NotFoundError when
// no row exists so the API edge can map to 404.
func (s *AnthropicCredentialService) Status(ctx context.Context, ocOrgID string) (*AnthropicProjection, error) {
	row, err := s.fetchRow(ctx, ocOrgID)
	if err != nil {
		return nil, err
	}
	return projectionFromAnthropicRow(row), nil
}

// ----------------------------------------------------------------------------
// Disconnect
// ----------------------------------------------------------------------------

// Disconnect removes the org's Anthropic key: deletes the encrypted bytes
// from `org_secrets`, drops the metadata row (status flip first, then
// delete via best-effort sweep is overkill for a single per-org credential),
// best-effort deletes the per-org WP Secret, and broadcasts cache invalidate.
//
// Idempotent: missing row is a no-op (200 → 204 at the API edge).
func (s *AnthropicCredentialService) Disconnect(ctx context.Context, ocOrgID string) error {
	tx := s.db.WithContext(ctx).Begin()
	if tx.Error != nil {
		return tx.Error
	}
	defer tx.Rollback() //nolint:errcheck

	if err := tx.Exec(`SELECT pg_advisory_xact_lock(hashtext(?))`, "org_anthropic:"+ocOrgID).Error; err != nil {
		return fmt.Errorf("anthropic disconnect: lock: %w", err)
	}

	// Delete the metadata row directly — the existing GitHub PAT flow flips
	// to `disconnected` for audit, but here we have nothing else referencing
	// the row (no installation_id, no webhook routing). Delete is cleaner.
	if err := tx.Exec(
		`DELETE FROM org_anthropic_credentials WHERE oc_org_id = ?`, ocOrgID,
	).Error; err != nil {
		return fmt.Errorf("anthropic disconnect: delete row: %w", err)
	}
	if err := tx.Commit().Error; err != nil {
		return fmt.Errorf("anthropic disconnect: commit: %w", err)
	}

	// Best-effort GC. Failures are logged, not surfaced.
	if err := s.store.Delete(ctx, ocOrgID, "anthropic/key"); err != nil {
		slog.WarnContext(ctx, "anthropic disconnect: store delete failed",
			"ocOrgId", ocOrgID, "error", err)
	}
	if err := s.DeleteAnthropicSecret(ctx, ocOrgID); err != nil {
		slog.WarnContext(ctx, "anthropic disconnect: wp secret delete failed",
			"ocOrgId", ocOrgID, "error", err)
	}
	s.broadcastInvalidate(ctx, ocOrgID)

	slog.InfoContext(ctx, "anthropic.disconnected", "ocOrgId", ocOrgID)
	return nil
}

// ----------------------------------------------------------------------------
// EffectiveKey
// ----------------------------------------------------------------------------

// EffectiveKeyResponse is the shape returned to agents-service.
type EffectiveKeyResponse struct {
	Source string `json:"source"` // "org" | "platform" | "none"
	Key    string `json:"key,omitempty"`
}

// EffectiveKey returns the org key when configured (and active), else the
// platform fallback. Returns { source: "none" } when neither is available
// — agents-service maps to 503.
func (s *AnthropicCredentialService) EffectiveKey(ctx context.Context, ocOrgID string) (*EffectiveKeyResponse, error) {
	row, err := s.fetchRow(ctx, ocOrgID)
	if err == nil && row.Status == "active" {
		key, getErr := s.store.Get(ctx, ocOrgID, "anthropic/key")
		if getErr == nil && len(key) > 0 {
			return &EffectiveKeyResponse{Source: "org", Key: string(key)}, nil
		}
		// Row says active but bytes are gone — log loudly and fall through
		// to platform (or "none").
		slog.WarnContext(ctx, "anthropic effective-key: row=active but org_secrets missing",
			"ocOrgId", ocOrgID, "error", getErr)
	}
	// `err != nil` falls through; either the row is absent (NotFoundError)
	// or row.Status != active. Either way, try the platform fallback.
	if s.platformKey != "" {
		return &EffectiveKeyResponse{Source: "platform", Key: s.platformKey}, nil
	}
	return &EffectiveKeyResponse{Source: "none"}, nil
}

// ----------------------------------------------------------------------------
// ApplyWPSecret
// ----------------------------------------------------------------------------

// ApplyWPSecretResult is returned to the dispatch caller — the K8s Secret
// name to thread into the WorkflowRun's `parameters.anthropic.secretRef`.
type ApplyWPSecretResult struct {
	SecretRefName string `json:"secretRefName"`
}

// ApplyWPSecret reads the per-org key from `org_secrets`, decrypts it, and
// SSA-applies the per-org K8s Secret in `workflows-<ocOrgID>`. Returns
// ErrAnthropicKeyRequired when no org row exists or it's not active —
// the dispatch path maps to 422. Returns a wrapped error when the
// underlying SSA fails.
//
// Same model as `BuildCredentialsService.MintBuildToken` → `applyBuildSecret`:
// per-dispatch refresh, idempotent SSA with FieldOwner, no long-term K8s
// state ownership.
func (s *AnthropicCredentialService) ApplyWPSecret(ctx context.Context, ocOrgID string) (*ApplyWPSecretResult, error) {
	row, err := s.fetchRow(ctx, ocOrgID)
	if err != nil {
		return nil, ErrAnthropicKeyRequired
	}
	if row.Status != "active" {
		return nil, ErrAnthropicKeyRequired
	}

	key, err := s.store.Get(ctx, ocOrgID, "anthropic/key")
	if err != nil {
		// Row is active but the bytes are missing — refuse rather than
		// silently fall through.
		return nil, fmt.Errorf("anthropic apply-wp-secret: store get: %w", err)
	}
	if len(key) == 0 {
		return nil, ErrAnthropicKeyRequired
	}

	if err := s.applyAnthropicSecret(ctx, ocOrgID, key); err != nil {
		return nil, fmt.Errorf("anthropic apply-wp-secret: ssa: %w", err)
	}

	return &ApplyWPSecretResult{SecretRefName: models.AnthropicSecretName}, nil
}

// applyAnthropicSecret SSA-applies the per-org Opaque Secret carrying
// ANTHROPIC_API_KEY into workflows-<ocOrgID>. No-op (with a warn) when
// wpClient is nil — same degraded-mode behaviour as build_credentials_service.
func (s *AnthropicCredentialService) applyAnthropicSecret(ctx context.Context, ocOrgID string, key []byte) error {
	if s.wpClient == nil {
		slog.WarnContext(ctx, "anthropic apply-wp-secret: wp k8s client not configured — Secret write skipped",
			"ocOrgId", ocOrgID)
		return nil
	}

	ns := models.WorkflowPlaneNamespace(ocOrgID)
	secret := &corev1.Secret{
		TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "Secret"},
		ObjectMeta: metav1.ObjectMeta{
			Name:      models.AnthropicSecretName,
			Namespace: ns,
			Labels: map[string]string{
				"app.kubernetes.io/managed-by":           "app-factory-git-service",
				"app-factory.openchoreo.dev/oc-org-id":   ocOrgID,
				"app-factory.openchoreo.dev/secret-type": "anthropic-credentials",
			},
		},
		Type: corev1.SecretTypeOpaque,
		StringData: map[string]string{
			"ANTHROPIC_API_KEY": string(key),
		},
	}

	if err := s.wpClient.Patch(
		ctx, secret,
		client.Apply,
		client.ForceOwnership,
		client.FieldOwner(k8s.FieldOwner),
	); err != nil {
		return fmt.Errorf("ssa anthropic secret: %w", err)
	}
	return nil
}

// DeleteAnthropicSecret removes the per-org Anthropic Secret from
// workflows-<ocOrgID>. Idempotent — NotFound + nil wpClient are no-ops.
// Implements the AnthropicSecretCleaner interface.
func (s *AnthropicCredentialService) DeleteAnthropicSecret(ctx context.Context, ocOrgID string) error {
	if s.wpClient == nil {
		return nil
	}
	ns := models.WorkflowPlaneNamespace(ocOrgID)
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      models.AnthropicSecretName,
			Namespace: ns,
		},
	}
	if err := s.wpClient.Delete(ctx, secret); err != nil {
		if apierrors.IsNotFound(err) {
			return nil
		}
		return fmt.Errorf("delete anthropic secret %s/%s: %w", ns, secret.Name, err)
	}
	slog.InfoContext(ctx, "anthropic.deleted-wp-secret",
		"ocOrgId", ocOrgID, "namespace", ns, "secret", secret.Name)
	return nil
}

// ----------------------------------------------------------------------------
// helpers
// ----------------------------------------------------------------------------

func (s *AnthropicCredentialService) fetchRow(ctx context.Context, ocOrgID string) (*models.OrgAnthropicCredential, error) {
	var row models.OrgAnthropicCredential
	err := s.db.WithContext(ctx).Where("oc_org_id = ?", ocOrgID).First(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, &NotFoundError{What: fmt.Sprintf("org_anthropic_credentials.%s", ocOrgID)}
	}
	if err != nil {
		return nil, err
	}
	return &row, nil
}

func (s *AnthropicCredentialService) broadcastInvalidate(ctx context.Context, ocOrgID string) {
	if s.invalidator == nil {
		return
	}
	if err := s.invalidator.Invalidate(ctx, ocOrgID); err != nil {
		slog.WarnContext(ctx, "anthropic cache invalidate failed (best-effort)",
			"ocOrgId", ocOrgID, "error", err)
	}
}

// validateAnthropicKey probes Anthropic's /v1/messages with a minimal
// payload. 401 → ValidationError{anthropic_key_invalid}. 5xx → 503-shape
// transient. Other non-2xx are treated as transient by default.
//
// Anthropic's /v1/messages requires both `x-api-key` and `anthropic-version`
// headers; a malformed request returns 400 (which still proves the key is
// recognized). We send a single 1-token completion request that should
// either 200 OK or 401 Unauthorized.
func (s *AnthropicCredentialService) validateAnthropicKey(ctx context.Context, key string) error {
	body := []byte(`{
	  "model": "claude-haiku-4-5",
	  "max_tokens": 1,
	  "messages": [{"role":"user","content":"ping"}]
	}`)
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, s.anthropicAPI+"/v1/messages", bytes.NewReader(body))
	req.Header.Set("x-api-key", key)
	req.Header.Set("anthropic-version", "2023-06-01")
	req.Header.Set("content-type", "application/json")

	resp, err := s.httpClient.Do(req)
	if err != nil {
		return &ValidationError{Code: "anthropic_unreachable", Message: fmt.Sprintf("Anthropic API unreachable: %v", err)}
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
	switch resp.StatusCode {
	case http.StatusUnauthorized:
		return &ValidationError{Code: "anthropic_key_invalid", Message: "Anthropic rejected the key (401 Unauthorized)"}
	case http.StatusForbidden:
		return &ValidationError{Code: "anthropic_key_forbidden", Message: "Anthropic key lacks the required permissions"}
	case http.StatusOK, http.StatusBadRequest:
		// 200 = key valid; 400 = key recognized but request payload arguable
		// (e.g. unknown model). Either way the key is authenticated.
		return nil
	}
	return &ValidationError{
		Code:    "anthropic_unexpected_status",
		Message: fmt.Sprintf("Anthropic API returned %d: %s", resp.StatusCode, truncateForError(respBody)),
	}
}

func looksLikeAnthropicKey(k string) bool {
	return strings.HasPrefix(k, "sk-ant-") && len(k) >= 20
}

// anthropicKeyPreview returns the standard prefix + last-4 display
// shape used everywhere (`sk-ant-ap03-A1B2…XyZw`).
func anthropicKeyPreview(k string) (prefix, last4 string) {
	if len(k) < 20 {
		return k, ""
	}
	// `sk-ant-` + next 8 chars = stable prefix.
	prefix = k[:15]
	last4 = k[len(k)-4:]
	return
}

// noopInvalidator is the type used by tests when no broadcast is needed.
type noopInvalidator struct{}

func (noopInvalidator) Invalidate(context.Context, string) error { return nil }

// NoopAgentsCacheInvalidator returns an invalidator that does nothing.
// Useful in tests and in main.go before the agents-service URL is
// configured.
func NoopAgentsCacheInvalidator() AgentsCacheInvalidator { return noopInvalidator{} }

// HTTPAgentsCacheInvalidator builds an invalidator that POSTs to
// agents-service `/v1/internal/cache/invalidate`. authHeader, if non-empty,
// is sent verbatim as the `Authorization` header so the caller can supply
// either a Bearer token or a Service-JWT-bearing string assembled
// elsewhere. baseURL = "" returns a noop.
func HTTPAgentsCacheInvalidator(baseURL string, authHeader string) AgentsCacheInvalidator {
	if baseURL == "" {
		return noopInvalidator{}
	}
	return &httpInvalidator{baseURL: strings.TrimRight(baseURL, "/"), authHeader: authHeader, httpClient: &http.Client{Timeout: 5 * time.Second}}
}

type httpInvalidator struct {
	baseURL    string
	authHeader string
	httpClient *http.Client
}

func (h *httpInvalidator) Invalidate(ctx context.Context, ocOrgID string) error {
	body, _ := json.Marshal(map[string]string{"orgId": ocOrgID})
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, h.baseURL+"/v1/internal/cache/invalidate", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if h.authHeader != "" {
		req.Header.Set("Authorization", h.authHeader)
	}
	resp, err := h.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent && resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("invalidate: %d: %s", resp.StatusCode, truncateForError(b))
	}
	return nil
}
