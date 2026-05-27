// Phase 3 — Thunder org UUID alignment.
//
// The BFF historically generated `organizations.uuid` via `uuid.New()`
// at backfill time, decoupled from Thunder's `ouId`. SM-API derives
// per-org namespaces (`wc-<orgUUID8>-<orgHash8>`) from the JWT's
// `ouId` claim — so the BFF's locally-generated UUID never matched
// the NS SM-API actually writes into, and the new dispatch path
// (which needs to compute the same NS to find the materialized
// Secret) was structurally broken.
//
// This migration adds a nullable `thunder_org_uuid` column so the BFF
// can persist Thunder's authoritative ouId alongside the local PK
// without an FK-cascade migration. The orgensure middleware fills it
// lazily on first authed request; SMAPIWriter reads it from the JWT
// context directly; dispatcher reads it from the row.
package migrations

import (
	"context"
	"fmt"

	"gorm.io/gorm"
)

func RunPhase3ThunderOrgUUID(ctx context.Context, db *gorm.DB) error {
	stmt := `DO $$ BEGIN
		   IF EXISTS (SELECT FROM information_schema.tables
		              WHERE table_schema='public' AND table_name='organizations') THEN
		     ALTER TABLE organizations
		       ADD COLUMN IF NOT EXISTS thunder_org_uuid UUID;
		     CREATE INDEX IF NOT EXISTS idx_organizations_thunder_org_uuid
		       ON organizations(thunder_org_uuid)
		       WHERE thunder_org_uuid IS NOT NULL;
		   END IF;
		 END $$`
	if err := db.WithContext(ctx).Exec(stmt).Error; err != nil {
		return fmt.Errorf("phase3_thunder_org_uuid: %w", err)
	}
	return nil
}
