package services

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
	"gorm.io/gorm"
)

// References is the JSONB-backed map of optional reference filenames →
// body for a skill (e.g. `references/examples.md`). Uses GORM's
// serializer:json directive — see skillRow below.
type References map[string]string

// Skill is the resolved shape that flows from the `skills` table to the
// architect input, the tech-lead input, the runner pull endpoint, and
// the console. Mirrors the row schema 1:1 plus a few derived fields.
type Skill struct {
	OrgID         string            `json:"orgId"`
	Name          string            `json:"name"`
	Kind          string            `json:"kind"` // builtin | custom | imported
	Description   string            `json:"description"`
	SkillMD       string            `json:"skillMd"`
	References    map[string]string `json:"references"`
	Version       int               `json:"version"`
	ContentSHA    string            `json:"contentSha"`
	License       string            `json:"license,omitempty"`
	Compatibility string            `json:"compatibility,omitempty"`
	UpdatedAt     time.Time         `json:"updatedAt"`
}

// SkillSummary is the lightweight projection used in catalogue listings —
// no body, no references. Architect's "org skills" manifest renders from
// these (name + description only).
type SkillSummary struct {
	Name        string `json:"name"`
	Kind        string `json:"kind"`
	Version     int    `json:"version"`
	Description string `json:"description"`
	ContentSHA  string `json:"contentSha"`
	Editable    bool   `json:"editable"`
}

// SkillService is the single read/write surface for skills. Every consumer —
// architect input building, tech-lead input building, the runner's pull
// endpoint, the console — goes through here. The bootstrap step (see
// SkillBootstrap.Run) is the only writer for builtin rows.
type SkillService struct {
	db *gorm.DB
}

func NewSkillService(db *gorm.DB) *SkillService {
	return &SkillService{db: db}
}

// Resolve returns a single skill by name visible to the given org —
// the lookup is org-scoped (custom/imported for orgId) UNIONed with the
// global builtin set (org_id=''). When both exist, the org-scoped row
// wins (sorts after '' lexicographically).
func (s *SkillService) Resolve(ctx context.Context, orgID, name string) (*Skill, error) {
	if s == nil || s.db == nil {
		return nil, nil
	}
	var row skillRow
	err := s.db.WithContext(ctx).
		Table("skills").
		Where(`skill_name = ? AND org_id IN ('', ?)`, name, orgID).
		Order(`org_id DESC`).
		Limit(1).
		Take(&row).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, fmt.Errorf("skill resolve %q: %w", name, err)
	}
	return rowToSkill(row), nil
}

// List returns every skill visible to the org (builtins + this org's
// custom/imported) sorted by kind then name.
func (s *SkillService) List(ctx context.Context, orgID string) ([]Skill, error) {
	if s == nil || s.db == nil {
		return nil, nil
	}
	var rows []skillRow
	if err := s.db.WithContext(ctx).
		Table("skills").
		Where(`org_id IN ('', ?)`, orgID).
		Order(`kind, skill_name`).
		Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("skill list: %w", err)
	}
	out := make([]Skill, 0, len(rows))
	for _, r := range rows {
		out = append(out, *rowToSkill(r))
	}
	return out, nil
}

// ListSummaries is List() projected to (name, description, kind, ...).
// Used by the console listing and the architect's "org skills" manifest.
func (s *SkillService) ListSummaries(ctx context.Context, orgID string) ([]SkillSummary, error) {
	skills, err := s.List(ctx, orgID)
	if err != nil {
		return nil, err
	}
	out := make([]SkillSummary, 0, len(skills))
	for _, sk := range skills {
		out = append(out, SkillSummary{
			Name:        sk.Name,
			Kind:        sk.Kind,
			Version:     sk.Version,
			Description: sk.Description,
			ContentSHA:  sk.ContentSHA,
			Editable:    sk.Kind != "builtin",
		})
	}
	return out, nil
}

// ResolveMany fans Resolve over a list of names, preserving order.
// Skipped names (no row) are omitted; the caller may compare lengths
// to detect missing skills.
func (s *SkillService) ResolveMany(ctx context.Context, orgID string, names []string) ([]Skill, error) {
	out := make([]Skill, 0, len(names))
	for _, n := range names {
		sk, err := s.Resolve(ctx, orgID, n)
		if err != nil {
			return nil, err
		}
		if sk == nil {
			slog.WarnContext(ctx, "skill resolve missing", "orgID", orgID, "name", n)
			continue
		}
		out = append(out, *sk)
	}
	return out, nil
}

// MaterializedName is the prefixed identifier used in the per-task
// AgentSkills plugin tree (`builtin-api-management`, etc.). See
// docs/design/skills-system.md > "Materialisation".
func MaterializedName(kind, name string) string {
	return kind + "-" + name
}

// PrefixedID is the catalogue ID surfaced in snapshot rows + audit log.
func PrefixedID(kind, name string) string {
	return kind + "/" + name
}

// skillRow is the GORM row shape — kept private so callers always go
// through rowToSkill for any post-processing.
type skillRow struct {
	OrgID         string     `gorm:"column:org_id"`
	SkillName     string     `gorm:"column:skill_name"`
	Kind          string     `gorm:"column:kind"`
	Description   string     `gorm:"column:description"`
	SkillMD       string     `gorm:"column:skill_md"`
	References    References `gorm:"column:references;type:jsonb;serializer:json"`
	Version       int        `gorm:"column:version"`
	ContentSHA    string     `gorm:"column:content_sha"`
	License       *string    `gorm:"column:license"`
	Compatibility *string    `gorm:"column:compatibility"`
	UpdatedAt     time.Time  `gorm:"column:updated_at"`
}

func rowToSkill(r skillRow) *Skill {
	refs := map[string]string(r.References)
	if refs == nil {
		refs = map[string]string{}
	}
	skill := &Skill{
		OrgID:       r.OrgID,
		Name:        r.SkillName,
		Kind:        r.Kind,
		Description: r.Description,
		SkillMD:     r.SkillMD,
		References:  refs,
		Version:     r.Version,
		ContentSHA:  r.ContentSHA,
		UpdatedAt:   r.UpdatedAt,
	}
	if r.License != nil {
		skill.License = *r.License
	}
	if r.Compatibility != nil {
		skill.Compatibility = *r.Compatibility
	}
	return skill
}

// ---- frontmatter parsing ----------------------------------------------------

// skillFrontmatter is the YAML frontmatter shape accepted on SKILL.md.
// Spec-clean AgentSkills: name, description, optional license,
// compatibility, allowed-tools. Platform extensions under metadata.asdlc.*
type skillFrontmatter struct {
	Name          string                 `yaml:"name"`
	Description   string                 `yaml:"description"`
	License       string                 `yaml:"license,omitempty"`
	Compatibility string                 `yaml:"compatibility,omitempty"`
	AllowedTools  any                    `yaml:"allowed-tools,omitempty"`
	Metadata      map[string]interface{} `yaml:"metadata,omitempty"`
}

// parseSkillMD splits frontmatter from body and decodes it. Returns the
// decoded frontmatter, the body, and any parse error.
func parseSkillMD(content string) (skillFrontmatter, string, error) {
	fm, body, err := splitFrontmatter(content)
	if err != nil {
		return skillFrontmatter{}, "", fmt.Errorf("split frontmatter: %w", err)
	}
	if fm == "" {
		return skillFrontmatter{}, "", fmt.Errorf("SKILL.md missing frontmatter")
	}
	var s skillFrontmatter
	if err := yaml.Unmarshal([]byte(fm), &s); err != nil {
		return skillFrontmatter{}, "", fmt.Errorf("decode frontmatter: %w", err)
	}
	if strings.TrimSpace(s.Name) == "" {
		return skillFrontmatter{}, "", fmt.Errorf("frontmatter missing name")
	}
	if strings.TrimSpace(s.Description) == "" {
		return skillFrontmatter{}, "", fmt.Errorf("frontmatter missing description")
	}
	return s, body, nil
}

// versionFromMetadata pulls metadata.asdlc.version out of frontmatter
// (stored as a string-as-int by the spec) and returns the integer
// version. Defaults to 1 when absent.
func versionFromMetadata(s skillFrontmatter) int {
	if s.Metadata == nil {
		return 1
	}
	// Flat dotted-key form — the documented AgentSkills string→string
	// representation: `metadata: { "asdlc.version": "2" }`. YAML does not
	// treat the dot as nesting, so this is a single literal key. This is
	// the form every bundled built-in uses.
	if v, ok := s.Metadata["asdlc.version"]; ok {
		return coerceVersion(v)
	}
	// Nested form — `metadata: { asdlc: { version: "2" } }`.
	if asdlcAny, ok := s.Metadata["asdlc"]; ok {
		if asdlcMap, ok := asdlcAny.(map[string]interface{}); ok {
			if verAny, ok := asdlcMap["version"]; ok {
				return coerceVersion(verAny)
			}
		}
	}
	return 1
}

// coerceVersion maps an int/float/string YAML scalar to a positive version
// integer, defaulting to 1.
func coerceVersion(v any) int {
	switch t := v.(type) {
	case int:
		if t > 0 {
			return t
		}
	case int64:
		if t > 0 {
			return int(t)
		}
	case float64:
		if t > 0 {
			return int(t)
		}
	case string:
		var n int
		_, _ = fmt.Sscanf(t, "%d", &n)
		if n > 0 {
			return n
		}
	}
	return 1
}

// contentSHA computes a deterministic hash over the canonical concat of
// the SKILL.md body + sorted reference filenames + their contents.
// Used by the bootstrap path to suppress no-op UPDATE writes.
func contentSHA(skillMD string, references map[string]string) string {
	h := sha256.New()
	h.Write([]byte(skillMD))
	keys := make([]string, 0, len(references))
	for k := range references {
		keys = append(keys, k)
	}
	// Sort
	for i := 1; i < len(keys); i++ {
		for j := i; j > 0 && keys[j-1] > keys[j]; j-- {
			keys[j-1], keys[j] = keys[j], keys[j-1]
		}
	}
	for _, k := range keys {
		h.Write([]byte{'\x00'})
		h.Write([]byte(k))
		h.Write([]byte{'\x00'})
		h.Write([]byte(references[k]))
	}
	return hex.EncodeToString(h.Sum(nil))
}

