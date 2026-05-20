package migrations

import (
	"fmt"
	"log/slog"

	"gorm.io/gorm"
)

// RunPhase2PRD applies the BFF schema additions for PR D of
// docs/design/github-integration-phase2.md.
//
// Adds two columns to component_tasks:
//
//   - cause TEXT NULL — populated by the projector on every terminal
//     transition (org.disconnected, repo.unselected, build.failed,
//     build.auth_retry_exceeded, build.deployed, etc). Existing terminal
//     rows backfill with 'legacy' so the ReachReconciliationBanner's
//     "since=24h cause=repo.unselected" filter does not retroactively
//     surface them.
//   - build_auth_retry_count INT NOT NULL DEFAULT 0 — per-task counter
//     for the build watcher's git_clone_failed_auth retry budget (§9.3).
//     Resets at re-dispatch (new task ID).
//
// Idempotent — re-running is a no-op once both columns exist.
func RunPhase2PRD(db *gorm.DB) error {
	stmts := []struct {
		check string
		ddl   string
		desc  string
	}{
		{
			check: `SELECT EXISTS (
				SELECT 1 FROM information_schema.columns
				WHERE table_schema='public' AND table_name='component_tasks' AND column_name='cause'
			)`,
			ddl:  `ALTER TABLE component_tasks ADD COLUMN cause TEXT NULL`,
			desc: "add cause column",
		},
		{
			check: `SELECT EXISTS (
				SELECT 1 FROM information_schema.columns
				WHERE table_schema='public' AND table_name='component_tasks' AND column_name='build_auth_retry_count'
			)`,
			ddl:  `ALTER TABLE component_tasks ADD COLUMN build_auth_retry_count INT NOT NULL DEFAULT 0`,
			desc: "add build_auth_retry_count column",
		},
	}
	for _, s := range stmts {
		var exists struct{ Exists bool }
		if err := db.Raw(s.check).Scan(&exists).Error; err != nil {
			return fmt.Errorf("phase2_prd %s: detect: %w", s.desc, err)
		}
		if exists.Exists {
			continue
		}
		if err := db.Exec(s.ddl).Error; err != nil {
			return fmt.Errorf("phase2_prd %s: %w", s.desc, err)
		}
		slog.Info("phase2_prd migration: applied", "step", s.desc)
	}

	// Backfill cause='legacy' for terminal rows that pre-date the column.
	// The banner queries filter on cause=<specific>, so a NULL or 'legacy'
	// row never surfaces under any cause-specific lens. New transitions
	// write the precise cause; reads that don't filter on cause continue
	// to work either way.
	res := db.Exec(`
		UPDATE component_tasks
		SET cause = 'legacy'
		WHERE cause IS NULL
		  AND status IN ('deployed','rejected','failed','abandoned')
	`)
	if res.Error != nil {
		return fmt.Errorf("phase2_prd: backfill cause: %w", res.Error)
	}
	if res.RowsAffected > 0 {
		slog.Info("phase2_prd migration: backfilled cause='legacy'", "rows", res.RowsAffected)
	}

	return nil
}
