package services

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"gorm.io/gorm"

	"github.com/wso2/asdlc/asdlc-service/clients/secretmanagersvc"
	"github.com/wso2/asdlc/asdlc-service/middleware/jwtassertion"
	"github.com/wso2/asdlc/asdlc-service/models"
	"github.com/wso2/asdlc/asdlc-service/services/codingagent"
)

// vaultPathPrefix is the KV mount prefix SM-API writes user-app
// secrets under (matches SM-API's VAULT_PATH_PREFIX env, default
// "user-app-secrets" — see wso2cloud/backend/secret-manager-api/
// internal/vault/eso.go::VaultPath). Hardcoded here because the BFF
// must reconstruct the actual Vault path it stamps into the credential
// row's sm_api_kv_path column (read by the dispatcher's ExternalSecret).
// If SM-API's mount changes, both sides must change together.
const vaultPathPrefix = "user-app-secrets"

// SMAPIWriter is the small helper Connect flows call after the per-org
// credential row is upserted. It uploads the secret value to SM-API
// and stamps the resulting `{secretRefName, kvPath, property}` onto
// the row so dispatch (WS2.3) can mint per-run ExternalSecrets without
// a label-lookup.
//
// Failures are logged but do not break the Connect transaction — the
// legacy `org_secrets`-backed path keeps working until WS2.6 removes
// it. The "SM-API row was upserted but the triplet is missing" state
// surfaces in the next Connect attempt (overwrites the row cleanly).
type SMAPIWriter struct {
	client secretmanagersvc.SecretManagementClient
	db     *gorm.DB
}

// NewSMAPIWriter returns a no-op writer when client is nil (matches the
// composition-root behavior when SECRET_MANAGER_API_URL is unset).
func NewSMAPIWriter(client secretmanagersvc.SecretManagementClient, db *gorm.DB) *SMAPIWriter {
	return &SMAPIWriter{client: client, db: db}
}

// Enabled reports whether the writer is wired to a real SM-API client.
// Callers should branch on this to avoid no-op DB updates when the
// provider isn't configured.
func (w *SMAPIWriter) Enabled() bool {
	return w != nil && w.client != nil
}

// WriteAnthropic uploads the per-org Anthropic API key to SM-API and
// stamps the triplet onto `org_anthropic_credentials`. ctx must carry
// the inbound user JWT (the SM-API provider reads it via the
// jwtassertion middleware context helper).
//
// Returns the secretRefName for caller convenience; the DB has already
// been updated when the call returns nil.
func (w *SMAPIWriter) WriteAnthropic(ctx context.Context, ocOrgID string, apiKey string) (string, error) {
	if !w.Enabled() {
		return "", nil
	}
	if strings.TrimSpace(ocOrgID) == "" {
		return "", errors.New("sm-api writer: ocOrgID required")
	}
	if strings.TrimSpace(apiKey) == "" {
		return "", errors.New("sm-api writer: apiKey required")
	}
	loc := secretmanagersvc.SecretLocation{
		OrgName:    ocOrgID,
		EntityName: "anthropic",
		SecretKey:  secretmanagersvc.SecretKeyAPIKey,
	}
	secretRefName, err := w.client.CreateSecret(ctx, loc, map[string]string{
		secretmanagersvc.SecretKeyAPIKey: apiKey,
	})
	if err != nil {
		return "", fmt.Errorf("sm-api writer: anthropic upload: %w", err)
	}
	vaultKey, err := w.resolveVaultKey(ctx, secretRefName)
	if err != nil {
		return secretRefName, fmt.Errorf("sm-api writer: resolve anthropic vault key: %w", err)
	}
	prop := secretmanagersvc.SecretKeyAPIKey
	if err := w.db.WithContext(ctx).
		Model(&models.OrgAnthropicCredential{}).
		Where("oc_org_id = ?", ocOrgID).
		Updates(map[string]any{
			"sm_api_secret_ref_name": secretRefName,
			"sm_api_kv_path":         vaultKey,
			"sm_api_property":        prop,
		}).Error; err != nil {
		return secretRefName, fmt.Errorf("sm-api writer: stamp anthropic triplet: %w", err)
	}
	slog.InfoContext(ctx, "sm-api writer: anthropic key uploaded",
		"ocOrgId", ocOrgID,
		"secretRefName", secretRefName,
		"vaultKey", vaultKey)
	return secretRefName, nil
}

// WriteGitHubPAT uploads a per-org GitHub PAT to SM-API and stamps the
// triplet (plus written_at) onto `org_credentials`. Same semantics as
// WriteAnthropic: errors are returned, ctx must carry the user JWT.
func (w *SMAPIWriter) WriteGitHubPAT(ctx context.Context, ocOrgID string, pat string) (string, error) {
	if !w.Enabled() {
		return "", nil
	}
	if strings.TrimSpace(ocOrgID) == "" {
		return "", errors.New("sm-api writer: ocOrgID required")
	}
	if strings.TrimSpace(pat) == "" {
		return "", errors.New("sm-api writer: pat required")
	}
	loc := secretmanagersvc.SecretLocation{
		OrgName:    ocOrgID,
		EntityName: "github-pat",
		SecretKey:  secretmanagersvc.SecretKeyAPIKey,
	}
	secretRefName, err := w.client.CreateSecret(ctx, loc, map[string]string{
		secretmanagersvc.SecretKeyAPIKey: pat,
	})
	if err != nil {
		return "", fmt.Errorf("sm-api writer: github-pat upload: %w", err)
	}
	vaultKey, err := w.resolveVaultKey(ctx, secretRefName)
	if err != nil {
		return secretRefName, fmt.Errorf("sm-api writer: resolve github-pat vault key: %w", err)
	}
	prop := secretmanagersvc.SecretKeyAPIKey
	now := time.Now().UTC()
	if err := w.db.WithContext(ctx).
		Model(&models.OrgCredential{}).
		Where("oc_org_id = ?", ocOrgID).
		Updates(map[string]any{
			"sm_api_secret_ref_name": secretRefName,
			"sm_api_kv_path":         vaultKey,
			"sm_api_property":        prop,
			"sm_api_written_at":      now,
		}).Error; err != nil {
		return secretRefName, fmt.Errorf("sm-api writer: stamp github-pat triplet: %w", err)
	}
	slog.InfoContext(ctx, "sm-api writer: github-pat uploaded",
		"ocOrgId", ocOrgID,
		"secretRefName", secretRefName,
		"vaultKey", vaultKey)
	return secretRefName, nil
}

// resolveVaultKey reconstructs the actual Vault KV key from the
// JWT's `ouId` claim — matches the shape SM-API derives server-side
// via vault.VaultPath() and stamps onto the SecretReference CR's
// spec.data[].remoteRef.key. The dispatcher pipes this verbatim into
// the per-run ExternalSecret.
//
// Pulling orgUUID from the JWT (not the DB) is deliberate: SM-API
// derives the NS from the JWT it just authenticated, so the BFF must
// use the same source-of-truth to compute a matching path. The BFF's
// local `organizations.uuid` is a random local PK and would diverge.
// Connect always runs in a request context with a verified user JWT.
func (w *SMAPIWriter) resolveVaultKey(ctx context.Context, secretRefName string) (string, error) {
	claims := jwtassertion.GetTokenClaims(ctx)
	if claims == nil || strings.TrimSpace(claims.OuId) == "" {
		return "", errors.New("no ouId claim in JWT context")
	}
	ns := codingagent.OrgBaseNamespace(claims.OuId)
	return vaultPathPrefix + "/" + ns + "/" + secretRefName, nil
}

// DeleteAnthropic best-effort removes the SM-API secret + clears the
// triplet on `org_anthropic_credentials`. Called by Disconnect; tolerates
// "already gone" responses (the underlying client returns nil on 404).
func (w *SMAPIWriter) DeleteAnthropic(ctx context.Context, ocOrgID string) error {
	if !w.Enabled() {
		return nil
	}
	loc := secretmanagersvc.SecretLocation{
		OrgName:    ocOrgID,
		EntityName: "anthropic",
		SecretKey:  secretmanagersvc.SecretKeyAPIKey,
	}
	var row models.OrgAnthropicCredential
	if err := w.db.WithContext(ctx).Where("oc_org_id = ?", ocOrgID).First(&row).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil
		}
		return fmt.Errorf("sm-api writer: load anthropic row: %w", err)
	}
	refName := ""
	if row.SMAPISecretRefName != nil {
		refName = *row.SMAPISecretRefName
	}
	if err := w.client.DeleteSecret(ctx, loc, refName); err != nil {
		return fmt.Errorf("sm-api writer: delete anthropic secret: %w", err)
	}
	return w.db.WithContext(ctx).
		Model(&models.OrgAnthropicCredential{}).
		Where("oc_org_id = ?", ocOrgID).
		Updates(map[string]any{
			"sm_api_secret_ref_name": nil,
			"sm_api_kv_path":         nil,
			"sm_api_property":        nil,
		}).Error
}

// DeleteGitHubPAT mirrors DeleteAnthropic on the GitHub side.
func (w *SMAPIWriter) DeleteGitHubPAT(ctx context.Context, ocOrgID string) error {
	if !w.Enabled() {
		return nil
	}
	loc := secretmanagersvc.SecretLocation{
		OrgName:    ocOrgID,
		EntityName: "github-pat",
		SecretKey:  secretmanagersvc.SecretKeyAPIKey,
	}
	var row models.OrgCredential
	if err := w.db.WithContext(ctx).Where("oc_org_id = ?", ocOrgID).First(&row).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil
		}
		return fmt.Errorf("sm-api writer: load github row: %w", err)
	}
	refName := ""
	if row.SMAPISecretRefName != nil {
		refName = *row.SMAPISecretRefName
	}
	if err := w.client.DeleteSecret(ctx, loc, refName); err != nil {
		return fmt.Errorf("sm-api writer: delete github-pat secret: %w", err)
	}
	return w.db.WithContext(ctx).
		Model(&models.OrgCredential{}).
		Where("oc_org_id = ?", ocOrgID).
		Updates(map[string]any{
			"sm_api_secret_ref_name": nil,
			"sm_api_kv_path":         nil,
			"sm_api_property":        nil,
			"sm_api_written_at":      nil,
		}).Error
}
