package migrations

import (
	"context"
	"fmt"

	"gorm.io/gorm"
)

// RunOrgSecretsMigration creates the org_secrets table that stores per-org
// credentials (GitHub PATs, build tokens) in git-service's own Postgres DB.
// Replaces the previous OpenBao-backed store.
//
// Idempotent: safe to re-run on an already-migrated database.
func RunOrgSecretsMigration(ctx context.Context, db *gorm.DB) error {
	if err := db.WithContext(ctx).Exec(`
		CREATE TABLE IF NOT EXISTS org_secrets (
		  oc_org_id   TEXT        NOT NULL,
		  key         TEXT        NOT NULL,
		  value       TEXT        NOT NULL,
		  updated_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
		  PRIMARY KEY (oc_org_id, key)
		)`).Error; err != nil {
		return fmt.Errorf("org_secrets migration: %w", err)
	}
	return nil
}
