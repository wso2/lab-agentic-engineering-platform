package services

import (
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"log/slog"
	"path"
	"strings"
	"time"

	"gorm.io/gorm"

	"github.com/wso2/asdlc/asdlc-service/skills"
)

// SkillBootstrap UPSERTs the bundled built-in SKILL.md files into the
// `skills` table on BFF startup, then prunes any built-in rows whose
// source files have been removed between releases.
//
// Idempotent: re-running on identical disk content is a no-op (the
// content_sha guard skips write when nothing changed).
//
// See docs/design/skills-system.md > "Bootstrap".
type SkillBootstrap struct {
	db *gorm.DB
}

func NewSkillBootstrap(db *gorm.DB) *SkillBootstrap {
	return &SkillBootstrap{db: db}
}

// Run reads each builtin/<name>/SKILL.md from the embedded FS, parses
// it, UPSERTs to `skills`, then DELETEs rows whose names are no longer
// bundled. Best-effort: failures log + bubble so main.go can choose to
// fail-start or warn.
func (b *SkillBootstrap) Run(ctx context.Context) error {
	if b == nil || b.db == nil {
		return fmt.Errorf("skill bootstrap: nil receiver")
	}

	entries, err := fs.ReadDir(skills.BuiltinFS, "builtin")
	if err != nil {
		return fmt.Errorf("skill bootstrap: read builtin dir: %w", err)
	}

	bundled := make([]string, 0, len(entries))
	now := time.Now().UTC()

	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		name := entry.Name()
		bundled = append(bundled, name)

		skillPath := path.Join("builtin", name, "SKILL.md")
		raw, err := fs.ReadFile(skills.BuiltinFS, skillPath)
		if err != nil {
			slog.WarnContext(ctx, "skill bootstrap: read failed", "name", name, "error", err)
			continue
		}

		fm, _, err := parseSkillMD(string(raw))
		if err != nil {
			slog.WarnContext(ctx, "skill bootstrap: parse failed", "name", name, "error", err)
			continue
		}
		if fm.Name != name {
			slog.WarnContext(ctx, "skill bootstrap: name mismatch",
				"dirName", name, "frontmatterName", fm.Name)
			continue
		}

		refs := References{} // v1 builtins ship no references
		sha := contentSHA(string(raw), refs)
		version := versionFromMetadata(fm)

		row := skillRow{
			OrgID:       "",
			SkillName:   name,
			Kind:        "builtin",
			Description: strings.TrimSpace(fm.Description),
			SkillMD:     string(raw),
			References:  refs,
			Version:     version,
			ContentSHA:  sha,
			UpdatedAt:   now,
		}

		if err := b.upsertBuiltin(ctx, row); err != nil {
			slog.WarnContext(ctx, "skill bootstrap: upsert failed", "name", name, "error", err)
			continue
		}
	}

	// Purge built-ins removed between releases.
	if len(bundled) > 0 {
		res := b.db.WithContext(ctx).
			Exec(`DELETE FROM skills WHERE kind = 'builtin' AND skill_name NOT IN (?)`, bundled)
		if res.Error != nil {
			return fmt.Errorf("skill bootstrap: purge removed: %w", res.Error)
		}
		if res.RowsAffected > 0 {
			slog.InfoContext(ctx, "skill bootstrap: purged removed builtins", "count", res.RowsAffected)
			b.audit(ctx, "", "*", "bootstrap-purge", res.RowsAffected, nil)
		}
	}

	slog.InfoContext(ctx, "skill bootstrap complete", "builtins", len(bundled))
	return nil
}

// upsertBuiltin writes the row only when content_sha differs from the
// stored row (or when no row exists). Postgres ON CONFLICT DO UPDATE
// is atomic — safe under concurrent BFF replicas seeding at the same
// moment.
func (b *SkillBootstrap) upsertBuiltin(ctx context.Context, row skillRow) error {
	// Use raw SQL because GORM's OnConflict doesn't play well with
	// gorm:"serializer:json" columns when we're embedding the value
	// inline (it tries to re-bind the type adapter on the conflict
	// branch). Direct INSERT...ON CONFLICT is clearer.
	refsJSON, err := marshalRefs(row.References)
	if err != nil {
		return fmt.Errorf("marshal refs: %w", err)
	}
	q := `
		INSERT INTO skills
			(org_id, skill_name, kind, description, skill_md, "references",
			 version, content_sha, created_at, updated_at, updated_by)
		VALUES (?, ?, 'builtin', ?, ?, ?::jsonb, ?, ?, ?, ?, 'system')
		ON CONFLICT (org_id, skill_name) DO UPDATE
			SET description = EXCLUDED.description,
			    skill_md    = EXCLUDED.skill_md,
			    "references" = EXCLUDED."references",
			    version     = EXCLUDED.version,
			    content_sha = EXCLUDED.content_sha,
			    updated_at  = EXCLUDED.updated_at,
			    updated_by  = 'system'
			WHERE skills.content_sha <> EXCLUDED.content_sha
	`
	if err := b.db.WithContext(ctx).Exec(q,
		row.OrgID, row.SkillName, row.Description, row.SkillMD,
		string(refsJSON), row.Version, row.ContentSHA, row.UpdatedAt, row.UpdatedAt,
	).Error; err != nil {
		return fmt.Errorf("upsert builtin %q: %w", row.SkillName, err)
	}
	return nil
}

// audit is a best-effort write to skill_audit_events. Failures log + ignore.
func (b *SkillBootstrap) audit(ctx context.Context, orgID, name, action string, count int64, after any) {
	q := `
		INSERT INTO skill_audit_events
			(org_id, skill_name, action, actor, after_state, occurred_at)
		VALUES (?, ?, ?, 'system', ?::jsonb, NOW())
	`
	afterJSON, _ := marshalRefs(References{"count": fmt.Sprintf("%d", count)})
	if err := b.db.WithContext(ctx).Exec(q, orgID, name, action, string(afterJSON)).Error; err != nil {
		slog.WarnContext(ctx, "skill audit write failed", "action", action, "error", err)
	}
}

func marshalRefs(r References) ([]byte, error) {
	if r == nil {
		r = References{}
	}
	return json.Marshal(map[string]string(r))
}
