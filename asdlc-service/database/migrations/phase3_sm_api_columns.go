package migrations

import (
	"context"
	"fmt"

	"gorm.io/gorm"
)

// RunPhase3SMAPIColumns adds the SM-API triplet columns to the per-org
// credential tables (WS2.2). Idempotent — re-running on a migrated DB
// is a no-op via ADD COLUMN IF NOT EXISTS.
//
// Per-row write semantics (filled by the Connect flow in WS2.2):
//
//   - sm_api_secret_ref_name — name returned by SM-API on POST /secrets;
//     the asdlc-side persists this so subsequent dispatch can mint an
//     ExternalSecret without re-resolving via label selector.
//   - sm_api_kv_path — KV path the SecretReference points at (passed
//     verbatim to ExternalSecret.spec.data[].remoteRef.key).
//   - sm_api_property — JSON field inside the KV value (passed to
//     ExternalSecret.spec.data[].remoteRef.property). For both Anthropic
//     and GitHub PAT this is always "api-key" today; persisting it
//     keeps the dispatch path free of magic strings.
//   - sm_api_written_at (org_credentials only) — Connect timestamp for
//     audit; useful for diffing against the GitHub side's
//     connected_at when triaging.
//
// Phase 1 leaves these nullable so existing rows from the legacy
// org_secrets-only flow keep working; the BFF tolerates NULL by
// short-circuiting the new dispatch path (legacy ClusterWorkflow still
// runs until WS2.6 deletes it).
func RunPhase3SMAPIColumns(ctx context.Context, db *gorm.DB) error {
	stmts := []string{
		// org_anthropic_credentials — added when the table itself exists
		// (created by RunOrgAnthropicCredentialsMigration earlier in the
		// migration chain).
		`DO $$ BEGIN
		   IF EXISTS (SELECT FROM information_schema.tables
		              WHERE table_schema='public' AND table_name='org_anthropic_credentials') THEN
		     ALTER TABLE org_anthropic_credentials
		       ADD COLUMN IF NOT EXISTS sm_api_secret_ref_name TEXT,
		       ADD COLUMN IF NOT EXISTS sm_api_kv_path         TEXT,
		       ADD COLUMN IF NOT EXISTS sm_api_property        TEXT;
		   END IF;
		 END $$`,
		`DO $$ BEGIN
		   IF EXISTS (SELECT FROM information_schema.tables
		              WHERE table_schema='public' AND table_name='org_credentials') THEN
		     ALTER TABLE org_credentials
		       ADD COLUMN IF NOT EXISTS sm_api_secret_ref_name TEXT,
		       ADD COLUMN IF NOT EXISTS sm_api_kv_path         TEXT,
		       ADD COLUMN IF NOT EXISTS sm_api_property        TEXT,
		       ADD COLUMN IF NOT EXISTS sm_api_written_at      TIMESTAMPTZ;
		   END IF;
		 END $$`,
	}
	for i, sql := range stmts {
		if err := db.WithContext(ctx).Exec(sql).Error; err != nil {
			return fmt.Errorf("phase3_sm_api_columns step %d: %w", i+1, err)
		}
	}
	return nil
}
