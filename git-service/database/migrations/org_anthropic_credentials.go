package migrations

import (
	"context"
	"fmt"

	"gorm.io/gorm"
)

// RunOrgAnthropicCredentialsMigration creates the org_anthropic_credentials
// table — metadata-only projection for per-org Anthropic keys. The encrypted
// key bytes live in `org_secrets(oc_org_id, key="anthropic/key")` (the same
// generic KV store the GitHub PAT uses). This table holds prefix + last4 for
// the UI, plus status / connected_at / last_validated_at / validation_error.
//
// Idempotent: safe to re-run on an already-migrated database.
func RunOrgAnthropicCredentialsMigration(ctx context.Context, db *gorm.DB) error {
	stmts := []string{
		`CREATE TABLE IF NOT EXISTS org_anthropic_credentials (
		   oc_org_id          TEXT PRIMARY KEY,
		   key_prefix         TEXT NOT NULL,
		   key_last4          TEXT NOT NULL,
		   status             TEXT NOT NULL DEFAULT 'active'
		                          CHECK (status IN ('active','invalid','disconnected')),
		   connected_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
		   last_validated_at  TIMESTAMPTZ,
		   validation_error   TEXT
		 )`,
	}
	for i, sql := range stmts {
		if err := db.WithContext(ctx).Exec(sql).Error; err != nil {
			return fmt.Errorf("org_anthropic_credentials migration step %d: %w", i+1, err)
		}
	}
	return nil
}
