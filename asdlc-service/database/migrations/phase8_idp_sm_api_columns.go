package migrations

import (
	"context"
	"fmt"

	"gorm.io/gorm"
)

// RunPhase8IDPSMAPIColumns adds the SM-API triplet columns to
// organization_idp_profiles (WS2.4). The publisher cc client_secret is
// mirrored to SM-API and materialised into the runner pod via a per-run
// ExternalSecret; the triplet here is read by the dispatcher to build that
// ExternalSecret without a label-selector lookup. Mirrors the per-row
// semantics in RunPhase3SMAPIColumns.
func RunPhase8IDPSMAPIColumns(ctx context.Context, db *gorm.DB) error {
	stmt := `DO $$ BEGIN
	   IF EXISTS (SELECT FROM information_schema.tables
	              WHERE table_schema='public' AND table_name='organization_idp_profiles') THEN
	     ALTER TABLE organization_idp_profiles
	       ADD COLUMN IF NOT EXISTS sm_api_secret_ref_name TEXT,
	       ADD COLUMN IF NOT EXISTS sm_api_kv_path         TEXT,
	       ADD COLUMN IF NOT EXISTS sm_api_property        TEXT,
	       ADD COLUMN IF NOT EXISTS sm_api_written_at      TIMESTAMPTZ;
	   END IF;
	 END $$`
	if err := db.WithContext(ctx).Exec(stmt).Error; err != nil {
		return fmt.Errorf("phase8_idp_sm_api_columns: %w", err)
	}
	return nil
}
