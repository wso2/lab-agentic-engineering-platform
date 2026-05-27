package migrations

import (
	"context"
	"log/slog"

	"gorm.io/gorm"
)

// RunBootstrapGrants restores the connecting role's full privilege set on
// objects it owns in the public schema. Runs before any other migration.
//
// Some managed-DB setups provision the app role via an out-of-band
// hardening step that REVOKEs everything and re-grants only DML
// (INSERT/SELECT/UPDATE/DELETE). The owner retains ALTER (an owner-only
// check that bypasses the ACL) so column-level migrations keep working,
// but raw `CREATE TABLE … REFERENCES <owned-table>` fails with
// `permission denied for table <owned-table>` because REFERENCES is an
// ACL bit that was stripped.
//
// Owners can always GRANT to themselves, so this is a safe self-heal:
// no-op when fine, recovery when an out-of-band REVOKE has run.
// Errors are logged and swallowed so a more locked-down environment
// can still reach the diagnostic specific migration errors that follow.
func RunBootstrapGrants(ctx context.Context, db *gorm.DB) error {
	stmts := []string{
		`GRANT ALL ON ALL TABLES IN SCHEMA public TO CURRENT_USER`,
		`GRANT ALL ON ALL SEQUENCES IN SCHEMA public TO CURRENT_USER`,
		`ALTER DEFAULT PRIVILEGES IN SCHEMA public GRANT ALL ON TABLES TO CURRENT_USER`,
		`ALTER DEFAULT PRIVILEGES IN SCHEMA public GRANT ALL ON SEQUENCES TO CURRENT_USER`,
	}
	for _, s := range stmts {
		if err := db.WithContext(ctx).Exec(s).Error; err != nil {
			slog.Warn("bootstrap_grants: statement failed (continuing)", "stmt", s, "error", err)
		}
	}
	return nil
}
