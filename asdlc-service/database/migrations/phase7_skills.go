package migrations

import (
	"fmt"
	"log/slog"

	"gorm.io/gorm"
)

// RunPhase7Skills creates the three tables behind the skills system
// (docs/design/skills-system.md):
//
//  1. skills — single source of truth for built-in, custom, and
//     imported skills. Builtins land here on BFF startup via
//     SkillBootstrap.Run() with org_id='' (empty string, global).
//
//  2. skill_audit_events — append-only audit log of every skill mutation
//     (create/update/delete/import + bootstrap-upsert/bootstrap-purge).
//
//  3. design_version_skill_snapshots — frozen bodies + references per
//     (project_id, design_version, skill_id) tuple at issue-creation
//     time, so in-flight tasks see the same skill content their
//     dispatching tech-lead used.
//
// Idempotent — every CREATE is gated on existence.
func RunPhase7Skills(db *gorm.DB) error {
	if !hasTable(db, "skills") {
		if err := db.Exec(`
			CREATE TABLE skills (
				org_id        VARCHAR(64)  NOT NULL DEFAULT '',
				skill_name    VARCHAR(64)  NOT NULL,
				kind          VARCHAR(32)  NOT NULL,
				description   TEXT         NOT NULL,
				skill_md      TEXT         NOT NULL,
				"references"  JSONB        NOT NULL DEFAULT '{}'::jsonb,
				version       INT          NOT NULL DEFAULT 1,
				content_sha   VARCHAR(64)  NOT NULL,
				license       TEXT,
				compatibility TEXT,
				created_at    TIMESTAMPTZ  NOT NULL DEFAULT now(),
				updated_at    TIMESTAMPTZ  NOT NULL DEFAULT now(),
				updated_by    VARCHAR(64),
				PRIMARY KEY (org_id, skill_name)
			)
		`).Error; err != nil {
			return fmt.Errorf("phase7_skills: create skills: %w", err)
		}
		if err := db.Exec(`CREATE INDEX skills_kind_idx ON skills (kind)`).Error; err != nil {
			return fmt.Errorf("phase7_skills: create skills_kind_idx: %w", err)
		}
		slog.Info("phase7_skills migration: created table", "table", "skills")
	}

	if !hasTable(db, "skill_audit_events") {
		if err := db.Exec(`
			CREATE TABLE skill_audit_events (
				id             BIGSERIAL PRIMARY KEY,
				org_id         VARCHAR(64) NOT NULL,
				skill_name     VARCHAR(64) NOT NULL,
				action         VARCHAR(32) NOT NULL,
				actor          VARCHAR(64) NOT NULL,
				before_state   JSONB,
				after_state    JSONB,
				occurred_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
				correlation_id VARCHAR(64)
			)
		`).Error; err != nil {
			return fmt.Errorf("phase7_skills: create skill_audit_events: %w", err)
		}
		if err := db.Exec(`CREATE INDEX skill_audit_events_org_skill_idx ON skill_audit_events (org_id, skill_name, occurred_at)`).Error; err != nil {
			return fmt.Errorf("phase7_skills: create skill_audit_events index: %w", err)
		}
		slog.Info("phase7_skills migration: created table", "table", "skill_audit_events")
	}

	if !hasTable(db, "design_version_skill_snapshots") {
		if err := db.Exec(`
			CREATE TABLE design_version_skill_snapshots (
				project_id        VARCHAR(64)  NOT NULL,
				design_version    VARCHAR(32)  NOT NULL,
				skill_id          VARCHAR(128) NOT NULL,
				materialized_name VARCHAR(96)  NOT NULL,
				kind              VARCHAR(32)  NOT NULL,
				skill_md          TEXT         NOT NULL,
				"references"      JSONB        NOT NULL DEFAULT '{}'::jsonb,
				created_at        TIMESTAMPTZ  NOT NULL DEFAULT now(),
				PRIMARY KEY (project_id, design_version, skill_id)
			)
		`).Error; err != nil {
			return fmt.Errorf("phase7_skills: create design_version_skill_snapshots: %w", err)
		}
		slog.Info("phase7_skills migration: created table", "table", "design_version_skill_snapshots")
	}

	return nil
}
