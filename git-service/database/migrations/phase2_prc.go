package migrations

import (
	"context"
	"fmt"

	"gorm.io/gorm"
)

// RunPhase2PRC applies the PR C schema:
//
//   - ALTER TABLE git_repositories ADD COLUMN repo_slug TEXT
//     (nullable initially; backfilled from repo_url; then NOT NULL via index)
//
// repo_slug is the SecretReference slug — `lower(replace(<owner>/<repo>, '/', '-'))`.
// Used for OpenBao path keying (`secret/asdlc/{ocOrgId}/git/{repoSlug}`) and
// the OC `SecretReference` CR name (`git-{ocOrgId}-{repoSlug}`).
//
// Idempotent. Backfill is best-effort: rows whose repo_url cannot be parsed
// as `https://github.com/<owner>/<repo>(.git)?` are left NULL and the
// dispatch path's lazy backfill handles them on first build trigger.
func RunPhase2PRC(ctx context.Context, db *gorm.DB) error {
	// On fresh deployments git_repositories may not exist yet — skip silently.
	var exists bool
	if err := db.WithContext(ctx).Raw(
		`SELECT EXISTS (SELECT FROM information_schema.tables WHERE table_schema='public' AND table_name='git_repositories')`,
	).Scan(&exists).Error; err != nil {
		return fmt.Errorf("phase2_prc: check table existence: %w", err)
	}
	if !exists {
		return nil
	}
	stmts := []string{
		`ALTER TABLE git_repositories ADD COLUMN IF NOT EXISTS repo_slug TEXT`,
		// Backfill repo_slug: extract owner/repo from repo_url, drop optional
		// .git suffix, replace '/' with '-'. The COALESCE keeps the existing
		// value if a previous run already populated it.
		`UPDATE git_repositories
		   SET repo_slug = COALESCE(
		     repo_slug,
		     lower(
		       replace(
		         regexp_replace(
		           substring(repo_url from 'github\.com/(.+)$'),
		           '\.git$', ''
		         ),
		         '/', '-'
		       )
		     )
		   )
		   WHERE repo_slug IS NULL OR repo_slug = ''`,
		// Legacy backfill of oc_secret_ref_name. The column is no longer
		// read by the application (build credentials are now pre-staged as
		// per-WorkflowRun K8s Secrets; see
		// docs/design/build-credential-injection.md), but kept populated
		// on legacy rows so older snapshots remain debuggable.
		`UPDATE git_repositories
		   SET oc_secret_ref_name = 'git-' || org_id || '-' || repo_slug
		   WHERE (oc_secret_ref_name IS NULL OR oc_secret_ref_name = '')
		     AND repo_slug IS NOT NULL
		     AND char_length('git-' || org_id || '-' || repo_slug) <= 63`,
		`CREATE INDEX IF NOT EXISTS ix_git_repositories_org_slug
		   ON git_repositories (org_id, repo_slug)`,
	}
	for i, sql := range stmts {
		if err := db.WithContext(ctx).Exec(sql).Error; err != nil {
			return fmt.Errorf("phase2_prc migration step %d: %w", i+1, err)
		}
	}
	return nil
}
