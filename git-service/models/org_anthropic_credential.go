package models

import "time"

// OrgAnthropicCredential is the per-org Anthropic API key metadata row. The
// encrypted key bytes themselves live in `org_secrets(oc_org_id, key="anthropic/key")`
// alongside the GitHub PAT — same `dbStore` (Postgres + AES-256-GCM) plumbing,
// different `key` value. This table stores only non-secret projection fields.
//
// See docs/design/anthropic-key-dual-token.md §4.2.
type OrgAnthropicCredential struct {
	OcOrgID         string     `gorm:"primaryKey;type:text" json:"ocOrgId"`
	KeyPrefix       string     `gorm:"type:text;not null;column:key_prefix" json:"keyPrefix"`
	KeyLast4        string     `gorm:"type:text;not null;column:key_last4" json:"keyLast4"`
	Status          string     `gorm:"type:text;not null;default:active;column:status" json:"status"`
	ConnectedAt     time.Time  `gorm:"column:connected_at;not null;default:now()" json:"connectedAt"`
	LastValidatedAt *time.Time `gorm:"column:last_validated_at" json:"lastValidatedAt,omitempty"`
	ValidationError *string    `gorm:"type:text;column:validation_error" json:"validationError,omitempty"`
}

func (OrgAnthropicCredential) TableName() string { return "org_anthropic_credentials" }
