package migrations

import (
	"context"
	"fmt"
	"log/slog"

	"gorm.io/gorm"
)

// RunPerOrgSecretName collapses the per-repo `oc_secret_ref_name` column
// to the post-2f26614 per-org shape (`git-<ocOrgID>`). Every active
// git_repositories row in an org references the same per-org build secret
// (see models.BuildSecretName / docs/design/cross-component-wiring-gaps.md
// follow-up).
//
// Backfill rule: for each (org_id, project_id) row whose oc_secret_ref_name
// is non-NULL, overwrite to `git-<lower(org_id)>`. NULL rows stay NULL —
// the first MintBuildToken call after this migration re-populates them via
// the standard provisioning path.
//
// Idempotent: re-running is a no-op once every row already has the
// canonical per-org value.
func RunPerOrgSecretName(ctx context.Context, db *gorm.DB) error {
	var exists bool
	if err := db.WithContext(ctx).Raw(
		`SELECT EXISTS (SELECT FROM information_schema.tables WHERE table_schema='public' AND table_name='git_repositories')`,
	).Scan(&exists).Error; err != nil {
		return fmt.Errorf("per_org_secret_name: check table existence: %w", err)
	}
	if !exists {
		return nil
	}

	res := db.WithContext(ctx).Exec(`
		UPDATE git_repositories
		   SET oc_secret_ref_name = 'git-' || lower(org_id)
		 WHERE oc_secret_ref_name IS NOT NULL
		   AND oc_secret_ref_name <> 'git-' || lower(org_id)
	`)
	if res.Error != nil {
		return fmt.Errorf("per_org_secret_name: backfill: %w", res.Error)
	}
	if res.RowsAffected > 0 {
		slog.Info("per_org_secret_name migration: collapsed oc_secret_ref_name to per-org shape",
			"rows", res.RowsAffected)
	}
	return nil
}
