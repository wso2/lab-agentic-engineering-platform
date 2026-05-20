// Package migrations holds one-shot SQL migrations that GORM AutoMigrate
// cannot express (column drops, data resets, structural rewrites).
//
// Phase 0 is destructive in dev: the legacy four-status fields on
// component_tasks (GitStatus / OCStatus / BuildStatus / DeployStatus /
// ErrorStage) are dropped, the table is truncated, and the new lifecycle
// (Status, BranchName, PullRequestNumber, MergeCommitSHA, …) takes over.
//
// The migration refuses to run unless DEPLOYMENT_TIER=dev so it cannot be
// triggered against a real environment by accident.
package migrations

import (
	"fmt"
	"log/slog"

	"gorm.io/gorm"
)

// RunPhase0 executes the Phase 0 destructive migration.
//
// It is idempotent: each statement uses IF EXISTS / IF NOT EXISTS guards so
// re-running is safe. The TRUNCATE is the only step that throws away rows,
// and it only runs when at least one of the legacy columns was actually
// dropped — so a freshly-bootstrapped DB doesn't lose data on second boot.
func RunPhase0(db *gorm.DB, deploymentTier string) error {
	if deploymentTier != "dev" {
		slog.Info("phase0 migration skipped — DEPLOYMENT_TIER is not dev",
			"tier", deploymentTier)
		return nil
	}

	type columnExists struct {
		Exists bool
	}

	// Detect whether any legacy column is still present. Truncate only when
	// at least one is — otherwise we're past the migration on a clean DB.
	var legacyPresent columnExists
	if err := db.Raw(`
		SELECT EXISTS (
			SELECT 1 FROM information_schema.columns
			WHERE table_name = 'component_tasks'
			  AND column_name IN
			      ('git_status','oc_status','build_status','deploy_status','error_stage')
		) AS exists
	`).Scan(&legacyPresent).Error; err != nil {
		return fmt.Errorf("phase0: check legacy columns: %w", err)
	}

	if legacyPresent.Exists {
		slog.Warn("phase0 migration: legacy columns detected, truncating component_tasks")
		if err := db.Exec(`TRUNCATE TABLE component_tasks`).Error; err != nil {
			return fmt.Errorf("phase0: truncate component_tasks: %w", err)
		}
		drops := []string{
			"ALTER TABLE component_tasks DROP COLUMN IF EXISTS git_status",
			"ALTER TABLE component_tasks DROP COLUMN IF EXISTS oc_status",
			"ALTER TABLE component_tasks DROP COLUMN IF EXISTS build_status",
			"ALTER TABLE component_tasks DROP COLUMN IF EXISTS deploy_status",
			"ALTER TABLE component_tasks DROP COLUMN IF EXISTS error_stage",
		}
		for _, stmt := range drops {
			if err := db.Exec(stmt).Error; err != nil {
				return fmt.Errorf("phase0: %s: %w", stmt, err)
			}
		}
		slog.Info("phase0 migration: legacy columns dropped, component_tasks truncated")
	}

	// project_default_pushes was a cross-handler rendezvous table between the
	// push and pull_request.closed webhook handlers. The build-dispatch path
	// now runs off `pr.closed merged=true` only, so the table is unused.
	// Drop it on first run so the schema stays clean.
	if err := db.Exec(`DROP TABLE IF EXISTS project_default_pushes`).Error; err != nil {
		return fmt.Errorf("phase0: drop project_default_pushes: %w", err)
	}

	return nil
}

// SeedPlatformOrgCredential was the Phase 0 platform-PAT seed. PR A of
// Phase 2 retires the kind='platform-pat' row entirely; the credential
// record relocates to git-service Postgres and seeds itself there. This
// function is intentionally absent — see RunPhase2PRA.
