package migrations

import (
	"fmt"
	"log/slog"

	"gorm.io/gorm"
)

// RunPhase6DbTasks adds the `component_type` column to component_tasks.
func RunPhase6DbTasks(db *gorm.DB) error {
	if err := addColumnIfMissing(db, "component_tasks", "component_type",
		`ALTER TABLE component_tasks ADD COLUMN component_type TEXT NOT NULL DEFAULT ''`); err != nil {
		return fmt.Errorf("phase6_db_tasks: add component_type: %w", err)
	}
	slog.Info("phase6_db_tasks migration: component_type column ensured")
	return nil
}
