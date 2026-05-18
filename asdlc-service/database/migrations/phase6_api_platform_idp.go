package migrations

import (
	"fmt"
	"log/slog"

	"gorm.io/gorm"
)

// RunPhase6APIPlatformIDP creates the two tables behind Phase 3 of
// docs/design/api-platform-integration.md (per-org Thunder publisher
// client lifecycle + audit trail):
//
//  1. organization_idp_profiles — one row per OC organisation, carrying
//     the IDP kind + issuer + JWKS URL + the per-org OAuth publisher
//     client_id and OpenBao secret path. v1 supports kind='platform'
//     only (Thunder); v2 adds 'asgardeo' and 'custom'. The unique
//     constraint enforces one profile per org.
//
//  2. idp_audit_events — append-only log of EnsurePublisher /
//     RevokePublisher / RegenerateClientSecret operations so org
//     admins can see rotation history and incident-response can
//     correlate compromise events.
//
// Idempotent — re-running checks for the tables before creating them.
func RunPhase6APIPlatformIDP(db *gorm.DB) error {
	if !hasTable(db, "organization_idp_profiles") {
		if err := db.Exec(`
			CREATE TABLE organization_idp_profiles (
				id                      UUID PRIMARY KEY DEFAULT gen_random_uuid(),
				org_id                  TEXT NOT NULL,
				kind                    TEXT NOT NULL CHECK (kind IN ('platform','asgardeo','custom')),
				issuer                  TEXT NOT NULL,
				jwks_url                TEXT NOT NULL,
				admin_creds_secret_ref  TEXT,
				publisher_client_id     TEXT,
				publisher_client_secret TEXT,
				publisher_secret_ref    TEXT,
				created_at              TIMESTAMPTZ NOT NULL DEFAULT now(),
				updated_at              TIMESTAMPTZ NOT NULL DEFAULT now(),
				CONSTRAINT one_profile_per_org UNIQUE (org_id)
			)
		`).Error; err != nil {
			return fmt.Errorf("phase6_api_platform_idp: create organization_idp_profiles: %w", err)
		}
		slog.Info("phase6_api_platform_idp migration: created table", "table", "organization_idp_profiles")
	}

	// publisher_client_secret was added after the initial table creation
	// — pre-existing dev databases need the column too. Idempotent.
	if !hasColumn(db, "organization_idp_profiles", "publisher_client_secret") {
		if err := db.Exec(`ALTER TABLE organization_idp_profiles ADD COLUMN publisher_client_secret TEXT`).Error; err != nil {
			return fmt.Errorf("phase6_api_platform_idp: add publisher_client_secret column: %w", err)
		}
		slog.Info("phase6_api_platform_idp migration: added column",
			"table", "organization_idp_profiles", "column", "publisher_client_secret")
	}

	// Ensure pgcrypto is available for gen_random_uuid(). No-op on
	// PostgreSQL ≥ 13 with the extension already enabled.
	if err := db.Exec(`CREATE EXTENSION IF NOT EXISTS pgcrypto`).Error; err != nil {
		// Best-effort — older deployments may run with limited
		// privileges. The CREATE TABLE above will have failed
		// already if gen_random_uuid() was unavailable.
		slog.Warn("phase6_api_platform_idp: pgcrypto extension toggle failed (likely already installed)", "error", err)
	}

	if !hasTable(db, "idp_audit_events") {
		if err := db.Exec(`
			CREATE TABLE idp_audit_events (
				id            BIGSERIAL PRIMARY KEY,
				org_id        TEXT NOT NULL,
				action        TEXT NOT NULL,
				actor         TEXT NOT NULL,
				occurred_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
				before_state  JSONB,
				after_state   JSONB,
				error_message TEXT
			)
		`).Error; err != nil {
			return fmt.Errorf("phase6_api_platform_idp: create idp_audit_events: %w", err)
		}
		if err := db.Exec(`CREATE INDEX idx_idp_audit_events_org_occurred ON idp_audit_events (org_id, occurred_at DESC)`).Error; err != nil {
			return fmt.Errorf("phase6_api_platform_idp: create idp_audit_events index: %w", err)
		}
		slog.Info("phase6_api_platform_idp migration: created table", "table", "idp_audit_events")
	}

	return nil
}

// hasTable returns true when the named relation exists in the current
// schema. Mirrors the hasColumn helper in phase3_tech_lead.go.
func hasTable(db *gorm.DB, table string) bool {
	var exists bool
	if err := db.Raw(`SELECT EXISTS (
		SELECT FROM information_schema.tables
		WHERE table_schema = 'public' AND table_name = ?
	)`, table).Scan(&exists).Error; err != nil {
		slog.Warn("phase6_api_platform_idp: hasTable check failed", "table", table, "error", err)
		return false
	}
	return exists
}
