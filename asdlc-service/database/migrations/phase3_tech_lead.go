package migrations

import (
	"fmt"
	"log/slog"

	"gorm.io/gorm"
)

// RunPhase3TechLead applies the schema delta for the tech-lead agent revamp
// (docs/design/tech-lead-agent.md §12).
//
// What changes:
//
//  1. ADD `body TEXT` (replaces `agent_instructions`).
//     Backfill `body` from `agent_instructions` on existing rows so dispatch
//     keeps working through the cutover.
//
//  2. DROP snapshot fields. Dispatch path now reads design fresh from
//     `.asdlc/design.json` via ArtifactStore on every dispatch — design
//     changes propagate without re-snapshotting per task. Dropped:
//     component_type, language, responsibilities, architecture_context,
//     key_considerations, api_contract, dependencies, open_api_spec,
//     app_path, buildpack, entrypoint, agent_instructions.
//
//  3. ADD batch + lineage fields:
//     rationale TEXT — one-sentence "why this task exists" from planner.
//     task_depends_on JSONB — titles of other tasks in the same batch this
//     task depends on; gates dispatch ordering.
//     batch_id UUID — append-only identifier for one tech-lead generation
//     run; rows in the same batch share source_*_version.
//     source_design_version / source_spec_version — design/spec tag at
//     generation time. Surfaces in the lineage chip; not used by dispatch.
//     body_sync_pending BOOLEAN — set when a GH issue body edit could not
//     be persisted; drained by a periodic reconciler.
//
//  4. CREATE INDEX idx_tasks_batch ON component_tasks(batch_id).
//
// Each step is guarded by an existence check in information_schema, so the
// migration is idempotent. Dropped columns are gated on existence so a re-run
// after a failed mid-migration retry doesn't error.
//
// Backfill ordering matters — `body` is created and populated BEFORE
// `agent_instructions` is dropped.
func RunPhase3TechLead(db *gorm.DB) error {
	// Step 1: add `body` and backfill from `agent_instructions` (if present).
	if err := addColumnIfMissing(db, "component_tasks", "body", `ALTER TABLE component_tasks ADD COLUMN body TEXT`); err != nil {
		return err
	}
	if hasColumn(db, "component_tasks", "agent_instructions") {
		res := db.Exec(`UPDATE component_tasks SET body = agent_instructions WHERE body IS NULL AND agent_instructions IS NOT NULL`)
		if res.Error != nil {
			return fmt.Errorf("phase3_tech_lead: backfill body: %w", res.Error)
		}
		if res.RowsAffected > 0 {
			slog.Info("phase3_tech_lead migration: backfilled body from agent_instructions", "rows", res.RowsAffected)
		}
	}

	// Step 2: drop snapshot columns. Order doesn't matter; each is independent.
	dropTargets := []string{
		"component_type",
		"language",
		"responsibilities",
		"architecture_context",
		"key_considerations",
		"api_contract",
		"dependencies",
		"open_api_spec",
		"app_path",
		"buildpack",
		"entrypoint",
		"agent_instructions",
	}
	for _, col := range dropTargets {
		if !hasColumn(db, "component_tasks", col) {
			continue
		}
		if err := db.Exec(fmt.Sprintf(`ALTER TABLE component_tasks DROP COLUMN %s`, col)).Error; err != nil {
			return fmt.Errorf("phase3_tech_lead: drop %s: %w", col, err)
		}
		slog.Info("phase3_tech_lead migration: dropped column", "column", col)
	}

	// Step 3: add new fields.
	adds := []struct {
		name string
		ddl  string
	}{
		{"rationale", `ALTER TABLE component_tasks ADD COLUMN rationale TEXT`},
		{"task_depends_on", `ALTER TABLE component_tasks ADD COLUMN task_depends_on JSONB DEFAULT '[]'::jsonb`},
		{"batch_id", `ALTER TABLE component_tasks ADD COLUMN batch_id UUID`},
		{"source_design_version", `ALTER TABLE component_tasks ADD COLUMN source_design_version TEXT`},
		{"source_spec_version", `ALTER TABLE component_tasks ADD COLUMN source_spec_version TEXT`},
		{"body_sync_pending", `ALTER TABLE component_tasks ADD COLUMN body_sync_pending BOOLEAN DEFAULT FALSE`},
	}
	for _, a := range adds {
		if err := addColumnIfMissing(db, "component_tasks", a.name, a.ddl); err != nil {
			return err
		}
	}

	// Step 4: index on batch_id.
	if err := db.Exec(`CREATE INDEX IF NOT EXISTS idx_tasks_batch ON component_tasks(batch_id)`).Error; err != nil {
		return fmt.Errorf("phase3_tech_lead: create idx_tasks_batch: %w", err)
	}

	return nil
}

func hasColumn(db *gorm.DB, table, column string) bool {
	var exists struct{ Exists bool }
	q := `SELECT EXISTS (
		SELECT 1 FROM information_schema.columns
		WHERE table_schema='public' AND table_name=$1 AND column_name=$2
	)`
	if err := db.Raw(q, table, column).Scan(&exists).Error; err != nil {
		// On error, conservatively assume present so we don't try to ADD
		// a column that already exists (Postgres errors on duplicate add).
		// The actual ADD path is also IF NOT EXISTS-guarded via this same
		// helper, so the worst case is a missed drop — caught next run.
		slog.Warn("phase3_tech_lead: hasColumn check failed", "table", table, "column", column, "error", err)
		return true
	}
	return exists.Exists
}

func addColumnIfMissing(db *gorm.DB, table, column, ddl string) error {
	if hasColumn(db, table, column) {
		return nil
	}
	if err := db.Exec(ddl).Error; err != nil {
		return fmt.Errorf("phase3_tech_lead: add %s.%s: %w", table, column, err)
	}
	slog.Info("phase3_tech_lead migration: added column", "table", table, "column", column)
	return nil
}
