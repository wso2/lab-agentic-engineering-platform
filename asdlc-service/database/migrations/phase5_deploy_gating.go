package migrations

import (
	"fmt"
	"log/slog"

	"gorm.io/gorm"
)

// RunPhase5DeployGating implements the F2 rename from the cross-component-
// wiring gap analysis (docs/design/cross-component-wiring-gaps.md):
//
//  1. RENAME component_tasks.task_depends_on → depends_on_components.
//     The old column held GitHub issue titles authored by the tech-lead LLM;
//     the new column holds component names sourced from .asdlc/design.json
//     (platform-authored, not LLM-authored). Persist-time validation in
//     services/task_stream.go::persistAndIssue refuses generation if the
//     name doesn't match design.Components[*].name.
//
//  2. The JSONB type and default carry over unchanged. Pre-existing rows
//     (always under DEPLOYMENT_TIER=dev for now) keep their value — local
//     fixtures will be regenerated on the next plan-stream run because
//     the new gating reads ComponentName, not title.
//
// Idempotent — re-running is a no-op once the rename has taken effect.
func RunPhase5DeployGating(db *gorm.DB) error {
	hasOld := hasColumn(db, "component_tasks", "task_depends_on")
	hasNew := hasColumn(db, "component_tasks", "depends_on_components")
	switch {
	case hasNew && !hasOld:
		// Already renamed.
		return nil
	case hasNew && hasOld:
		// Both columns exist — a previous interrupted run. Drop the old one;
		// the new one is authoritative.
		if err := db.Exec(`ALTER TABLE component_tasks DROP COLUMN task_depends_on`).Error; err != nil {
			return fmt.Errorf("phase5_deploy_gating: drop legacy column: %w", err)
		}
		slog.Info("phase5_deploy_gating migration: dropped stale task_depends_on column")
		return nil
	case hasOld && !hasNew:
		if err := db.Exec(`ALTER TABLE component_tasks RENAME COLUMN task_depends_on TO depends_on_components`).Error; err != nil {
			return fmt.Errorf("phase5_deploy_gating: rename column: %w", err)
		}
		slog.Info("phase5_deploy_gating migration: renamed task_depends_on → depends_on_components")
		return nil
	default:
		// Neither exists — fresh DB. Phase 3 should have added task_depends_on;
		// add the new one directly so the column is present for the model.
		if err := db.Exec(`ALTER TABLE component_tasks ADD COLUMN depends_on_components JSONB DEFAULT '[]'::jsonb`).Error; err != nil {
			return fmt.Errorf("phase5_deploy_gating: add new column: %w", err)
		}
		slog.Info("phase5_deploy_gating migration: added depends_on_components column")
		return nil
	}
}
