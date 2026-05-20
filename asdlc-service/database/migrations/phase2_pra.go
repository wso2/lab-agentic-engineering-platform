package migrations

import (
	"fmt"
	"log/slog"

	"gorm.io/gorm"
)

// RunPhase2PRA executes the BFF-side dev wipe for PR A of
// docs/design/github-integration-phase2.md.
//
// What this does (one-shot, idempotent):
//
//   - TRUNCATE component_tasks, webhook_deliveries, webhook_payloads,
//     git_repositories. The phase2_pra spec calls for clearing these
//     because they hold rows scoped to the legacy oc_org_id='platform'
//     and to projects whose webhook delivery / repo metadata won't match
//     the new per-org world. Re-create projects from the console after.
//
// The BFF intentionally does NOT touch org_credentials: that table is now
// owned by git-service. git-service's own RunPhase2PRA detects and drops
// any legacy BFF-shaped row before re-creating with the §4.1 schema.
//
// The detection is shape-based: we only run the wipe if a legacy
// BFF-shaped org_credentials table is present (i.e. Phase 0 state).
// Once absent, this is already-migrated and the truncates are skipped.
//
// Refuses to run unless DEPLOYMENT_TIER=dev. Production cutover is empty
// per evolution-doc §8 — there are no pre-Phase-2 projects to migrate.
func RunPhase2PRA(db *gorm.DB, deploymentTier string) error {
	if deploymentTier != "dev" {
		slog.Info("phase2_pra migration skipped — DEPLOYMENT_TIER is not dev",
			"tier", deploymentTier)
		return nil
	}

	// Detect a Phase 0 BFF-shaped org_credentials table (column
	// `git_hub_login` from GORM AutoMigrate of the old `GitHubLogin`
	// struct field). git-service's PR A uses `github_login` instead; if
	// we see the legacy column, this is a first-time migration.
	var legacy struct{ Exists bool }
	if err := db.Raw(`
		SELECT EXISTS (
			SELECT 1 FROM information_schema.columns
			WHERE table_schema = 'public'
			  AND table_name = 'org_credentials'
			  AND column_name = 'git_hub_login'
		) AS exists
	`).Scan(&legacy).Error; err != nil {
		return fmt.Errorf("phase2_pra: detect legacy shape: %w", err)
	}

	if !legacy.Exists {
		slog.Info("phase2_pra migration: already applied (no legacy org_credentials shape)")
		return nil
	}

	slog.Warn("phase2_pra migration: dev wipe — TRUNCATE component_tasks, webhook_deliveries, webhook_payloads, git_repositories")

	if err := db.Exec(`TRUNCATE TABLE git_repositories, component_tasks, webhook_deliveries, webhook_payloads RESTART IDENTITY CASCADE`).Error; err != nil {
		return fmt.Errorf("phase2_pra: truncate: %w", err)
	}

	slog.Info("phase2_pra migration: complete — credentials now live in git-service")
	return nil
}
