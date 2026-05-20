package models

import (
	"time"
)

// OrganizationIDPProfile is the per-org IDP configuration row backing
// Phase 3 of docs/design/api-platform-integration.md (per-org Thunder
// publisher client). One row per OC organisation.
//
//	v1 — every row has Kind="platform" + Thunder issuer/jwks_url, with
//	     PublisherClientID + PublisherSecretRef populated lazily on
//	     first protected-component deploy.
//	v2 — Kind ∈ {"asgardeo","custom"} adds support for BYO-IDP. The
//	     console org-settings page becomes editable in Phase 7.
//
// The OrgID field is the OC-side org handle (not a UUID) — matches
// every other place the BFF identifies orgs.
type OrganizationIDPProfile struct {
	ID                  string    `gorm:"primaryKey;type:uuid;default:gen_random_uuid()" json:"id"`
	OrgID               string    `gorm:"column:org_id;not null;uniqueIndex:one_profile_per_org" json:"orgId"`
	Kind                string    `gorm:"not null" json:"kind"` // platform | asgardeo | custom
	Issuer              string    `gorm:"not null" json:"issuer"`
	JWKSURL             string    `gorm:"column:jwks_url;not null" json:"jwksUrl"`
	AdminCredsSecretRef   string    `gorm:"column:admin_creds_secret_ref" json:"adminCredsSecretRef,omitempty"`
	PublisherClientID     string    `gorm:"column:publisher_client_id" json:"publisherClientId,omitempty"`
	// PublisherClientSecret is the live secret used by the BFF when it
	// needs to mint per-org publisher tokens or hand the secret out to
	// user-app pods. v1 stores plaintext in PostgreSQL; v2 moves to
	// OpenBao behind PublisherSecretRef (which stays nil until then).
	// The JSON `-` tag keeps it off the wire — callers must use a
	// purpose-built endpoint to fetch it.
	PublisherClientSecret string    `gorm:"column:publisher_client_secret" json:"-"`
	PublisherSecretRef    string    `gorm:"column:publisher_secret_ref" json:"publisherSecretRef,omitempty"`
	CreatedAt           time.Time `gorm:"column:created_at" json:"createdAt"`
	UpdatedAt           time.Time `gorm:"column:updated_at" json:"updatedAt"`
}

// TableName pins the GORM table name (the auto-pluraliser would
// produce `organization_idp_profiles` already, but we make it explicit
// to survive any future model package reshuffles).
func (OrganizationIDPProfile) TableName() string { return "organization_idp_profiles" }

// IDPAuditEvent is one row in the append-only audit log of
// publisher-lifecycle operations. Used by the console "Audit" view
// (Phase 4) and by incident response when investigating compromised
// org credentials.
type IDPAuditEvent struct {
	ID           int64     `gorm:"primaryKey;autoIncrement" json:"id"`
	OrgID        string    `gorm:"column:org_id;not null;index:idx_idp_audit_events_org_occurred,priority:1" json:"orgId"`
	Action       string    `gorm:"not null" json:"action"` // ensure_publisher | revoke_publisher | regenerate_secret
	Actor        string    `gorm:"not null" json:"actor"`  // user email / service principal
	OccurredAt   time.Time `gorm:"column:occurred_at;index:idx_idp_audit_events_org_occurred,priority:2,sort:desc" json:"occurredAt"`
	BeforeState  []byte    `gorm:"column:before_state;type:jsonb" json:"beforeState,omitempty"`
	AfterState   []byte    `gorm:"column:after_state;type:jsonb" json:"afterState,omitempty"`
	ErrorMessage string    `gorm:"column:error_message" json:"errorMessage,omitempty"`
}

func (IDPAuditEvent) TableName() string { return "idp_audit_events" }

// IDPAuditAction string constants. Centralised so the audit-log writers
// can't drift on spelling.
const (
	IDPAuditEnsurePublisher    = "ensure_publisher"
	IDPAuditRevokePublisher    = "revoke_publisher"
	IDPAuditRegenerateSecret   = "regenerate_secret"
	IDPAuditUpdateProfile      = "update_profile"
)
