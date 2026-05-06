package migrations

import (
	"fmt"
	"log/slog"

	"gorm.io/gorm"
)

// RunPhase4CodingAgent adds the `last_coding_agent_run_name` column to
// component_tasks. Mirrors the existing LastBuildRunName column but tracks
// the per-task WorkflowRun of ClusterWorkflow `app-factory-coding-agent`
// (the new ephemeral-pod path that replaces the long-lived remote-worker).
//
// Idempotent — re-running is a no-op once the column exists.
func RunPhase4CodingAgent(db *gorm.DB) error {
	const check = `SELECT EXISTS (
		SELECT 1 FROM information_schema.columns
		WHERE table_schema='public' AND table_name='component_tasks' AND column_name='last_coding_agent_run_name'
	)`
	var exists struct{ Exists bool }
	if err := db.Raw(check).Scan(&exists).Error; err != nil {
		return fmt.Errorf("phase4_coding_agent: detect column: %w", err)
	}
	if exists.Exists {
		return nil
	}
	if err := db.Exec(`ALTER TABLE component_tasks ADD COLUMN last_coding_agent_run_name TEXT NOT NULL DEFAULT ''`).Error; err != nil {
		return fmt.Errorf("phase4_coding_agent: add column: %w", err)
	}
	slog.Info("phase4_coding_agent migration: added last_coding_agent_run_name column")
	return nil
}
