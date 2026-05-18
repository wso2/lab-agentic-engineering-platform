package services

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"gorm.io/gorm"

	"github.com/wso2/asdlc/asdlc-service/clients/thundersvc"
	"github.com/wso2/asdlc/asdlc-service/models"
)

// IDPService manages per-organisation IDP profiles + the matching
// Thunder publisher OAuth apps. Backs Phase 3 of
// docs/design/api-platform-integration.md.
//
// Lifecycle:
//   - First protected-component deploy in an org triggers
//     EnsureOrgPublisher. Idempotent — repeating returns the existing
//     row and skips the Thunder create call.
//   - Org delete (or explicit admin action) triggers RevokeOrgPublisher.
//   - Credential compromise / scheduled rotation triggers
//     RegenerateClientSecret.
//
// Every mutation appends an audit row to idp_audit_events so the
// console "Audit" tab and incident-response have a definitive trail.
type IDPService interface {
	// GetOrCreateProfile returns the org's IDP profile, creating a
	// default platform-kind row (kind=platform, issuer/jwksURL from
	// the platform IDP config) when none exists. The publisher_*
	// columns stay empty until EnsureOrgPublisher fires.
	GetOrCreateProfile(ctx context.Context, orgID string) (*models.OrganizationIDPProfile, error)

	// GetProfile returns the existing profile, or nil + nil error when
	// none exists yet.
	GetProfile(ctx context.Context, orgID string) (*models.OrganizationIDPProfile, error)

	// EnsureOrgPublisher creates (or returns the existing) Thunder
	// publisher OAuth app for the org, persists the credentials on
	// the profile row, and writes an audit-log entry. Returns the
	// canonical client_id (always present) and the client_secret
	// (only present on creation — empty when the app already existed
	// and the secret is already in the DB; use RegenerateClientSecret
	// to rotate if it was lost).
	EnsureOrgPublisher(ctx context.Context, orgID, actor string) (clientID, clientSecret string, created bool, err error)

	// RevokeOrgPublisher deletes the Thunder publisher app + clears
	// the profile row's publisher_* columns. Idempotent.
	RevokeOrgPublisher(ctx context.Context, orgID, actor string) (bool, error)

	// RegenerateClientSecret issues a fresh client_secret + updates the
	// profile row + writes an audit entry. Returns the new secret —
	// rotate any consumer pods after this call.
	RegenerateClientSecret(ctx context.Context, orgID, actor string) (string, error)

	// UpdateProfile changes the org's IDP kind / issuer / JWKS URL —
	// Phase 7 BYO-IDP editable picker. Switching kind invalidates any
	// existing publisher app (Thunder is a separate keymanager from
	// Asgardeo/custom OIDC), so the call cascades a RevokeOrgPublisher
	// against the previous kind's IDP. Audit-logged.
	//
	// Operator follow-up: after this call, the platform admin must
	// ensure the new IDP's keymanager is registered in
	// deployments/manifests/api-platform/gateway-config.yaml's
	// jwtauth_v1 block, then re-run setup-prerequisites. v2 will move
	// keymanager registration into the BFF (writeback to the ConfigMap
	// via the k8s API) but v1 keeps it as a manual ops step.
	UpdateProfile(ctx context.Context, orgID, actor string, req UpdateProfileRequest) (*models.OrganizationIDPProfile, error)
}

// UpdateProfileRequest is the input for IDPService.UpdateProfile.
// Empty fields leave the existing value unchanged.
type UpdateProfileRequest struct {
	Kind    string // "platform" | "asgardeo" | "custom"
	Issuer  string
	JWKSURL string
}

// PlatformIDPConfig is the cluster-level platform IDP defaults the BFF
// applies when seeding a new org profile. Loaded from env in main.go.
type PlatformIDPConfig struct {
	Issuer  string
	JWKSURL string
}

type idpService struct {
	db        *gorm.DB
	thunder   thundersvc.Client
	platform  PlatformIDPConfig
}

// NewIDPService builds the service. `thunder` may be nil in unit tests
// — the service rejects EnsureOrgPublisher / RevokeOrgPublisher /
// RegenerateClientSecret with ErrIDPThunderUnavailable when so. Read
// methods (GetProfile, GetOrCreateProfile) keep working.
func NewIDPService(db *gorm.DB, thunder thundersvc.Client, platform PlatformIDPConfig) IDPService {
	return &idpService{db: db, thunder: thunder, platform: platform}
}

// ErrIDPThunderUnavailable means the Thunder admin client isn't wired
// (FEATURE_EMIT_API_TRAIT off, missing system credentials, etc).
// Callers in the dispatch / design-edit path treat this as
// non-fatal — protected components still deploy; per-org publisher
// provisioning is best-effort and the next dispatch tries again.
var ErrIDPThunderUnavailable = errors.New("idp_service: thunder admin client not configured")

func (s *idpService) GetProfile(ctx context.Context, orgID string) (*models.OrganizationIDPProfile, error) {
	if orgID == "" {
		return nil, fmt.Errorf("orgID required")
	}
	var profile models.OrganizationIDPProfile
	err := s.db.WithContext(ctx).Where("org_id = ?", orgID).First(&profile).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("idp_service.GetProfile: %w", err)
	}
	return &profile, nil
}

func (s *idpService) GetOrCreateProfile(ctx context.Context, orgID string) (*models.OrganizationIDPProfile, error) {
	if orgID == "" {
		return nil, fmt.Errorf("orgID required")
	}
	existing, err := s.GetProfile(ctx, orgID)
	if err != nil {
		return nil, err
	}
	if existing != nil {
		return existing, nil
	}

	profile := models.OrganizationIDPProfile{
		OrgID:     orgID,
		Kind:      "platform",
		Issuer:    s.platform.Issuer,
		JWKSURL:   s.platform.JWKSURL,
		CreatedAt: time.Now().UTC(),
		UpdatedAt: time.Now().UTC(),
	}
	if err := s.db.WithContext(ctx).Create(&profile).Error; err != nil {
		// Race: another goroutine may have created the row between our
		// SELECT and INSERT. Re-read.
		if again, gerr := s.GetProfile(ctx, orgID); gerr == nil && again != nil {
			return again, nil
		}
		return nil, fmt.Errorf("idp_service.GetOrCreateProfile: %w", err)
	}
	return &profile, nil
}

func (s *idpService) EnsureOrgPublisher(ctx context.Context, orgID, actor string) (string, string, bool, error) {
	if orgID == "" {
		return "", "", false, fmt.Errorf("orgID required")
	}
	if s.thunder == nil {
		return "", "", false, ErrIDPThunderUnavailable
	}

	profile, err := s.GetOrCreateProfile(ctx, orgID)
	if err != nil {
		return "", "", false, err
	}

	beforeJSON, _ := json.Marshal(profileSummary(profile))

	clientID, clientSecret, created, terr := s.thunder.EnsurePublisherApp(ctx, orgID)
	if terr != nil {
		s.audit(ctx, orgID, models.IDPAuditEnsurePublisher, actor, beforeJSON, nil, terr)
		return "", "", false, fmt.Errorf("idp_service.EnsureOrgPublisher: %w", terr)
	}

	// Persist clientId always; clientSecret only on creation (Thunder
	// doesn't expose it on subsequent reads).
	updates := map[string]interface{}{
		"publisher_client_id":  clientID,
		"publisher_secret_ref": secretRefPath(orgID), // logical path for v2 OpenBao migration
		"updated_at":           time.Now().UTC(),
	}
	if created && clientSecret != "" {
		updates["publisher_client_secret"] = clientSecret
	}
	if err := s.db.WithContext(ctx).Model(profile).
		Where("org_id = ?", orgID).
		Updates(updates).Error; err != nil {
		s.audit(ctx, orgID, models.IDPAuditEnsurePublisher, actor, beforeJSON, nil, err)
		return "", "", false, fmt.Errorf("idp_service.EnsureOrgPublisher persist: %w", err)
	}

	// Re-read for the audit "after" snapshot.
	after, _ := s.GetProfile(ctx, orgID)
	afterJSON, _ := json.Marshal(profileSummary(after))
	s.audit(ctx, orgID, models.IDPAuditEnsurePublisher, actor, beforeJSON, afterJSON, nil)

	slog.InfoContext(ctx, "idp_service: EnsureOrgPublisher",
		"orgID", orgID,
		"clientID", clientID,
		"created", created,
	)
	return clientID, clientSecret, created, nil
}

func (s *idpService) RevokeOrgPublisher(ctx context.Context, orgID, actor string) (bool, error) {
	if orgID == "" {
		return false, fmt.Errorf("orgID required")
	}
	if s.thunder == nil {
		return false, ErrIDPThunderUnavailable
	}
	profile, err := s.GetProfile(ctx, orgID)
	if err != nil {
		return false, err
	}
	if profile == nil || profile.PublisherClientID == "" {
		// Nothing to revoke.
		return false, nil
	}
	beforeJSON, _ := json.Marshal(profileSummary(profile))

	deleted, terr := s.thunder.DeletePublisherApp(ctx, orgID)
	if terr != nil {
		s.audit(ctx, orgID, models.IDPAuditRevokePublisher, actor, beforeJSON, nil, terr)
		return false, fmt.Errorf("idp_service.RevokeOrgPublisher: %w", terr)
	}

	if err := s.db.WithContext(ctx).Model(profile).
		Where("org_id = ?", orgID).
		Updates(map[string]interface{}{
			"publisher_client_id":     "",
			"publisher_client_secret": "",
			"publisher_secret_ref":    "",
			"updated_at":              time.Now().UTC(),
		}).Error; err != nil {
		s.audit(ctx, orgID, models.IDPAuditRevokePublisher, actor, beforeJSON, nil, err)
		return deleted, fmt.Errorf("idp_service.RevokeOrgPublisher persist: %w", err)
	}
	after, _ := s.GetProfile(ctx, orgID)
	afterJSON, _ := json.Marshal(profileSummary(after))
	s.audit(ctx, orgID, models.IDPAuditRevokePublisher, actor, beforeJSON, afterJSON, nil)

	slog.InfoContext(ctx, "idp_service: RevokeOrgPublisher",
		"orgID", orgID, "deleted", deleted)
	return deleted, nil
}

func (s *idpService) RegenerateClientSecret(ctx context.Context, orgID, actor string) (string, error) {
	if orgID == "" {
		return "", fmt.Errorf("orgID required")
	}
	if s.thunder == nil {
		return "", ErrIDPThunderUnavailable
	}
	profile, err := s.GetProfile(ctx, orgID)
	if err != nil {
		return "", err
	}
	if profile == nil || profile.PublisherClientID == "" {
		return "", fmt.Errorf("idp_service.RegenerateClientSecret: no publisher app for org %s", orgID)
	}
	beforeJSON, _ := json.Marshal(profileSummary(profile))

	newSecret, terr := s.thunder.RegenerateClientSecret(ctx, orgID)
	if terr != nil {
		s.audit(ctx, orgID, models.IDPAuditRegenerateSecret, actor, beforeJSON, nil, terr)
		return "", fmt.Errorf("idp_service.RegenerateClientSecret: %w", terr)
	}

	if err := s.db.WithContext(ctx).Model(profile).
		Where("org_id = ?", orgID).
		Updates(map[string]interface{}{
			"publisher_client_secret": newSecret,
			"updated_at":              time.Now().UTC(),
		}).Error; err != nil {
		s.audit(ctx, orgID, models.IDPAuditRegenerateSecret, actor, beforeJSON, nil, err)
		return "", fmt.Errorf("idp_service.RegenerateClientSecret persist: %w", err)
	}
	after, _ := s.GetProfile(ctx, orgID)
	afterJSON, _ := json.Marshal(profileSummary(after))
	s.audit(ctx, orgID, models.IDPAuditRegenerateSecret, actor, beforeJSON, afterJSON, nil)

	slog.InfoContext(ctx, "idp_service: RegenerateClientSecret", "orgID", orgID)
	return newSecret, nil
}

// UpdateProfile changes kind/issuer/JWKS URL for an org. When kind
// switches, the existing publisher app is revoked (so the next
// EnsureOrgPublisher creates a fresh one tied to the new IDP). When
// only issuer/JWKS URL change but kind stays the same, the publisher
// app is preserved.
func (s *idpService) UpdateProfile(ctx context.Context, orgID, actor string, req UpdateProfileRequest) (*models.OrganizationIDPProfile, error) {
	if orgID == "" {
		return nil, fmt.Errorf("orgID required")
	}
	if req.Kind != "" {
		switch req.Kind {
		case "platform", "asgardeo", "custom":
			// ok
		default:
			return nil, fmt.Errorf("invalid kind %q (must be platform|asgardeo|custom)", req.Kind)
		}
	}

	existing, err := s.GetOrCreateProfile(ctx, orgID)
	if err != nil {
		return nil, err
	}
	beforeJSON, _ := json.Marshal(profileSummary(existing))

	kindChanged := req.Kind != "" && req.Kind != existing.Kind

	updates := map[string]interface{}{
		"updated_at": time.Now().UTC(),
	}
	if req.Kind != "" {
		updates["kind"] = req.Kind
	}
	if req.Issuer != "" {
		updates["issuer"] = req.Issuer
	}
	if req.JWKSURL != "" {
		updates["jwks_url"] = req.JWKSURL
	}

	// Clear publisher state when kind switches — the existing publisher
	// app belongs to the previous IDP. Trying to reuse it across IDPs
	// breaks the trust chain (different issuer, different signing keys).
	if kindChanged {
		// Best-effort Thunder cleanup BEFORE clearing — only for the
		// platform→other transition. We skip the call when thunder
		// isn't configured (just clear the columns).
		if existing.Kind == "platform" && existing.PublisherClientID != "" && s.thunder != nil {
			if _, derr := s.thunder.DeletePublisherApp(ctx, orgID); derr != nil {
				slog.WarnContext(ctx, "idp_service.UpdateProfile: Thunder publisher cleanup failed (ignored)",
					"orgID", orgID, "error", derr)
			}
		}
		updates["publisher_client_id"] = ""
		updates["publisher_client_secret"] = ""
		updates["publisher_secret_ref"] = ""
	}

	if err := s.db.WithContext(ctx).Model(existing).
		Where("org_id = ?", orgID).
		Updates(updates).Error; err != nil {
		s.audit(ctx, orgID, models.IDPAuditUpdateProfile, actor, beforeJSON, nil, err)
		return nil, fmt.Errorf("idp_service.UpdateProfile persist: %w", err)
	}

	after, _ := s.GetProfile(ctx, orgID)
	afterJSON, _ := json.Marshal(profileSummary(after))
	s.audit(ctx, orgID, models.IDPAuditUpdateProfile, actor, beforeJSON, afterJSON, nil)
	slog.InfoContext(ctx, "idp_service: UpdateProfile",
		"orgID", orgID, "kindChanged", kindChanged,
		"newKind", req.Kind, "newIssuer", req.Issuer)
	return after, nil
}

// audit writes one row into idp_audit_events. Best-effort — a failed
// insert logs but doesn't propagate (the principal action already
// happened on Thunder / in the DB).
func (s *idpService) audit(ctx context.Context, orgID, action, actor string, before, after []byte, opErr error) {
	row := models.IDPAuditEvent{
		OrgID:      orgID,
		Action:     action,
		Actor:      coalesceActor(actor),
		OccurredAt: time.Now().UTC(),
		BeforeState: before,
		AfterState:  after,
	}
	if opErr != nil {
		row.ErrorMessage = opErr.Error()
	}
	if err := s.db.WithContext(ctx).Create(&row).Error; err != nil {
		slog.WarnContext(ctx, "idp_service: audit insert failed",
			"orgID", orgID, "action", action, "error", err)
	}
}

func coalesceActor(actor string) string {
	if actor == "" {
		return "system"
	}
	return actor
}

// profileSummary projects the row down to the audit-friendly fields —
// drops timestamps + db-internal id so the diff is purely about
// publisher state.
type profileSummaryFields struct {
	Kind              string `json:"kind"`
	Issuer            string `json:"issuer"`
	JWKSURL           string `json:"jwksUrl"`
	PublisherClientID string `json:"publisherClientId"`
	HasClientSecret   bool   `json:"hasClientSecret"`
}

func profileSummary(p *models.OrganizationIDPProfile) profileSummaryFields {
	if p == nil {
		return profileSummaryFields{}
	}
	return profileSummaryFields{
		Kind:              p.Kind,
		Issuer:            p.Issuer,
		JWKSURL:           p.JWKSURL,
		PublisherClientID: p.PublisherClientID,
		HasClientSecret:   p.PublisherClientSecret != "",
	}
}

// secretRefPath returns the logical OpenBao path the BFF would write
// the publisher secret to in a v2 deployment. v1 keeps the secret in
// PostgreSQL but persists this path on the profile so future migration
// can rewrite the row without schema changes.
func secretRefPath(orgID string) string {
	return "secret/asdlc/" + orgID + "/idp/publisher"
}
