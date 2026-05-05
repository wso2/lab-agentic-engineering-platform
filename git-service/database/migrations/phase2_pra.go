// Package migrations holds raw-SQL migrations applied alongside GORM's
// AutoMigrate. CHECK constraints, partial unique indexes, and explicit
// ALTER TABLE shapes that GORM's struct-tag inference doesn't model
// well live here.
package migrations

import (
	"context"
	"fmt"

	"gorm.io/gorm"
)

// RunPhase2PRA applies the PR A schema:
//
//   - CREATE TABLE org_credentials with the §4.1 schema + CHECK constraints
//   - Two partial unique indexes (one OC org : one GitHub org property)
//   - ALTER TABLE git_repositories ADD oc_secret_ref_name (used by PR C's
//     SecretReference flow; nullable until then)
//
// Idempotent: re-running on a pre-migrated DB is a no-op.
//
// MUST run before GORM AutoMigrate so the raw-SQL column shapes (and CHECK
// constraints) are authoritative. If a legacy BFF-shaped table exists
// (with `git_hub_login` instead of `github_login`), this drops it first.
// The BFF's separate phase2_pra ran the dev wipe of transient tables.
func RunPhase2PRA(ctx context.Context, db *gorm.DB) error {
	// Detect a legacy table created by the Phase 0 BFF's GORM AutoMigrate —
	// it has `git_hub_login` instead of the §4.1 spec name `github_login`.
	// Drop and recreate cleanly.
	var legacyShape struct{ Exists bool }
	if err := db.WithContext(ctx).Raw(`
		SELECT EXISTS (
			SELECT 1 FROM information_schema.columns
			WHERE table_schema = 'public'
			  AND table_name = 'org_credentials'
			  AND column_name = 'git_hub_login'
		) AS exists
	`).Scan(&legacyShape).Error; err != nil {
		return fmt.Errorf("phase2_pra: detect legacy shape: %w", err)
	}
	if legacyShape.Exists {
		if err := db.WithContext(ctx).Exec(`DROP TABLE IF EXISTS org_credentials CASCADE`).Error; err != nil {
			return fmt.Errorf("phase2_pra: drop legacy org_credentials: %w", err)
		}
	}

	stmts := []string{
		`CREATE TABLE IF NOT EXISTS org_credentials (
		  oc_org_id           TEXT PRIMARY KEY,
		  kind                TEXT NOT NULL CHECK (kind IN ('app-installation', 'user-pat')),
		  github_login        TEXT NOT NULL,
		  identity_name       TEXT NOT NULL,
		  identity_email      TEXT NOT NULL,
		  identity_login      TEXT NOT NULL,
		  installation_id     BIGINT,
		  selected_repos      JSONB,
		  pat_secret_ref      TEXT,
		  webhook_secrets     JSONB,
		  status              TEXT NOT NULL DEFAULT 'active'
		                          CHECK (status IN ('active','suspended','disconnected')),
		  connected_at        TIMESTAMPTZ NOT NULL DEFAULT now(),
		  last_validated_at   TIMESTAMPTZ,
		  identity_changed_at TIMESTAMPTZ,
		  prev_identity_login TEXT,
		  CONSTRAINT secrets_shape_per_kind CHECK (
		    (kind = 'user-pat' AND webhook_secrets IS NOT NULL AND jsonb_array_length(webhook_secrets) >= 1)
		    OR (kind = 'app-installation' AND webhook_secrets IS NULL)
		  ),
		  CONSTRAINT app_fields CHECK (
		    (kind = 'user-pat' AND installation_id IS NULL AND selected_repos IS NULL)
		    OR (kind = 'app-installation' AND installation_id IS NOT NULL)
		  )
		)`,
		// One active credential row per (oc_org_id, github_login). The same
		// GitHub login can legitimately back credentials for multiple OC orgs
		// — e.g. a single platform PAT seeding the dev-tier orgs `default`
		// and `admin`, or an engineer who has access to two OC orgs in a
		// personal capacity. The earlier global-uniqueness index on
		// (github_login) blocked that legitimate case; drop it if it exists.
		`DROP INDEX IF EXISTS ux_org_credentials_github_login_active`,
		`CREATE UNIQUE INDEX IF NOT EXISTS ux_org_credentials_org_login_active
		   ON org_credentials (oc_org_id, github_login) WHERE status = 'active'`,
		`CREATE UNIQUE INDEX IF NOT EXISTS ux_org_credentials_installation_active
		   ON org_credentials (installation_id) WHERE status = 'active' AND installation_id IS NOT NULL`,
		// git_repositories may not exist yet on fresh deployments — skip the ALTER.
		`DO $$ BEGIN IF EXISTS (SELECT FROM information_schema.tables WHERE table_schema='public' AND table_name='git_repositories') THEN ALTER TABLE git_repositories ADD COLUMN IF NOT EXISTS oc_secret_ref_name TEXT; END IF; END $$`,
	}
	for i, sql := range stmts {
		if err := db.WithContext(ctx).Exec(sql).Error; err != nil {
			return fmt.Errorf("phase2_pra migration step %d: %w", i+1, err)
		}
	}
	return nil
}
