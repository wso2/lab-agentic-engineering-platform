package models

import (
	"database/sql/driver"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// OrgCredential is the per-org GitHub credential record. Phase 2 PR A
// relocates this from BFF Postgres to git-service Postgres — credentials
// live in the same service that holds them (evolution-doc §3.3).
//
// One row per OC org. Mode is fixed at connect time (kind ∈
// {app-installation, user-pat}). The Phase 0 single-tenant
// kind='platform-pat' row is retired and not represented here.
//
// CHECK constraints (secrets_shape_per_kind, app_fields) live in raw SQL
// in the migration — GORM does not model them well.
type OrgCredential struct {
	OcOrgID            string         `gorm:"primaryKey;type:text" json:"ocOrgId"`
	Kind               string         `gorm:"type:text;not null;column:kind" json:"kind"`
	GitHubLogin        string         `gorm:"type:text;not null;column:github_login" json:"githubLogin"`
	IdentityName       string         `gorm:"type:text;not null;column:identity_name" json:"-"`
	IdentityEmail      string         `gorm:"type:text;not null;column:identity_email" json:"-"`
	IdentityLogin      string         `gorm:"type:text;not null;column:identity_login" json:"identityLogin"`
	InstallationID     *int64         `gorm:"column:installation_id" json:"installationId,omitempty"`
	SelectedRepos      JSONStringList `gorm:"type:jsonb;column:selected_repos" json:"selectedRepos,omitempty"`
	PATSecretRef       *string        `gorm:"type:text;column:pat_secret_ref" json:"-"`
	WebhookSecrets     WebhookSecrets `gorm:"type:jsonb;column:webhook_secrets" json:"-"`
	Status             string         `gorm:"type:text;not null;default:active;column:status" json:"status"`
	ConnectedAt        time.Time      `gorm:"column:connected_at;not null;default:now()" json:"connectedAt"`
	LastValidatedAt    *time.Time     `gorm:"column:last_validated_at" json:"lastValidatedAt,omitempty"`
	IdentityChangedAt  *time.Time     `gorm:"column:identity_changed_at" json:"identityChangedAt,omitempty"`
	PrevIdentityLogin  *string        `gorm:"type:text;column:prev_identity_login" json:"prevIdentityLogin,omitempty"`

	// WS2.2 — SM-API triplet + write timestamp. See OrgAnthropicCredential
	// for lifecycle. Four columns stamped atomically in the Connect tx.
	SMAPISecretRefName *string    `gorm:"type:text;column:sm_api_secret_ref_name" json:"-"`
	SMAPIKVPath        *string    `gorm:"type:text;column:sm_api_kv_path" json:"-"`
	SMAPIProperty      *string    `gorm:"type:text;column:sm_api_property" json:"-"`
	SMAPIWrittenAt     *time.Time `gorm:"column:sm_api_written_at" json:"-"`
}

// TableName pins the underlying table to org_credentials. Without this
// override, GORM would pluralise the struct name to "org_credentials"
// — which is what we want, but explicit is better than implicit when
// other services have known table names.
func (OrgCredential) TableName() string { return "org_credentials" }

// Identity convenience accessor — packages the three identity fields.
func (c *OrgCredential) AsIdentity() (name, email, login string) {
	return c.IdentityName, c.IdentityEmail, c.IdentityLogin
}

// JSONStringList is a JSONB column holding []string — used for
// selected_repos in App mode. nil/empty is stored as JSON null.
type JSONStringList []string

func (l JSONStringList) Value() (driver.Value, error) {
	if l == nil {
		return nil, nil
	}
	return json.Marshal([]string(l))
}

func (l *JSONStringList) Scan(value any) error {
	if value == nil {
		*l = nil
		return nil
	}
	var b []byte
	switch v := value.(type) {
	case []byte:
		b = v
	case string:
		b = []byte(v)
	default:
		return fmt.Errorf("JSONStringList.Scan: unsupported type %T", value)
	}
	if len(b) == 0 || string(b) == "null" {
		*l = nil
		return nil
	}
	var s []string
	if err := json.Unmarshal(b, &s); err != nil {
		return err
	}
	*l = s
	return nil
}

// WebhookSecretEntry is one entry in the webhook_secrets JSONB list. The
// list shape (rather than scalar) is what enables N-of-M rotation per
// evolution-doc §7.6.
type WebhookSecretEntry struct {
	Secret  string    `json:"secret"`
	AddedAt time.Time `json:"added_at"`
}

// WebhookSecrets is a JSONB-backed list of WebhookSecretEntry. nil/empty
// is stored as JSON null (used by App-mode rows where the list lives
// platform-wide at _platform/github/app/webhook_secret rather than on
// the row).
type WebhookSecrets []WebhookSecretEntry

func (w WebhookSecrets) Value() (driver.Value, error) {
	if w == nil {
		return nil, nil
	}
	return json.Marshal([]WebhookSecretEntry(w))
}

func (w *WebhookSecrets) Scan(value any) error {
	if value == nil {
		*w = nil
		return nil
	}
	var b []byte
	switch v := value.(type) {
	case []byte:
		b = v
	case string:
		b = []byte(v)
	default:
		return errors.New("WebhookSecrets.Scan: unsupported source type")
	}
	if len(b) == 0 || string(b) == "null" {
		*w = nil
		return nil
	}
	var s []WebhookSecretEntry
	if err := json.Unmarshal(b, &s); err != nil {
		return err
	}
	*w = s
	return nil
}
