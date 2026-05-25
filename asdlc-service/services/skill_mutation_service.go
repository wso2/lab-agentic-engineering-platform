package services

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"strings"
	"time"

	"gorm.io/gorm"
)

// Skill mutation sentinels — controllers map these to HTTP status codes.
var (
	// ErrSkillNotEditable is returned for PUT/DELETE against a builtin.
	ErrSkillNotEditable = errors.New("skill is read-only (builtin)")
	// ErrSkillNotFound is returned when a name maps to no editable row.
	ErrSkillNotFound = errors.New("skill not found")
	// ErrSkillNameCollision is returned when a create reuses a visible name.
	ErrSkillNameCollision = errors.New("skill name already in use")
	// ErrImportedSkillInUse is returned when an imported skill is referenced
	// by an in-flight task's snapshot.
	ErrImportedSkillInUse = errors.New("imported skill is referenced by in-flight tasks")
)

// maxSkillBytes caps total skill size (SKILL.md + references). Matches the
// design's 400 KB ceiling.
const maxSkillBytes = 400 * 1024

// reservedSkillNames cannot be used by custom/imported skills — `asdlc` is
// the base plugin's name.
var reservedSkillNames = map[string]bool{"asdlc": true}

// reservedSkillPrefixes are used at materialisation time; custom/imported
// names must not collide with them.
var reservedSkillPrefixes = []string{"builtin-", "custom-", "imported-"}

// skillNameRE is the AgentSkills kebab rule: lowercase alphanumeric
// segments joined by single hyphens; no leading/trailing/consecutive
// hyphen. Stricter than validate.Slug (which allows trailing/double
// hyphens) because the name is also the AgentSkills directory + frontmatter
// `name:`.
var skillNameRE = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)

// maxCustomNameLen leaves room for the 9-char materialisation prefix
// (`imported-`) within AgentSkills' 64-char ceiling.
const maxCustomNameLen = 55

// SkillValidationIssue mirrors the design's { code, message, path } shape.
type SkillValidationIssue struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Path    string `json:"path,omitempty"`
}

// SkillValidationError carries one or more structured issues. The
// controller renders it as 400 with the issues array; nothing persists
// when this is returned.
type SkillValidationError struct {
	Issues []SkillValidationIssue
}

func (e *SkillValidationError) Error() string {
	parts := make([]string, 0, len(e.Issues))
	for _, i := range e.Issues {
		parts = append(parts, i.Code+": "+i.Message)
	}
	return "skill validation failed: " + strings.Join(parts, "; ")
}

func validationErr(code, message, path string) *SkillValidationError {
	return &SkillValidationError{Issues: []SkillValidationIssue{{Code: code, Message: message, Path: path}}}
}

// CreateSkillInput is the POST body for a custom skill.
type CreateSkillInput struct {
	Name       string            `json:"name"`
	SkillMD    string            `json:"skillMd"`
	References map[string]string `json:"references"`
}

// UpdateSkillInput is the PUT body for a custom skill.
type UpdateSkillInput struct {
	SkillMD    string            `json:"skillMd"`
	References map[string]string `json:"references"`
}

// SkillMutationService owns the org-editable write surface: create/update/
// delete for custom skills, delete for imported skills, and the read-only
// guard for builtins. See docs/design/skills-system.md > "REST API".
type SkillMutationService struct {
	db     *gorm.DB
	skills *SkillService
}

func NewSkillMutationService(db *gorm.DB, skills *SkillService) *SkillMutationService {
	return &SkillMutationService{db: db, skills: skills}
}

// Create validates and inserts a new kind=custom skill for the org.
func (m *SkillMutationService) Create(ctx context.Context, orgID, actor string, in CreateSkillInput) (*Skill, error) {
	if m == nil || m.db == nil {
		return nil, fmt.Errorf("skill mutation service: not configured")
	}
	name := strings.TrimSpace(in.Name)
	if issues := validateSkillName(name); issues != nil {
		return nil, issues
	}
	fm, _, err := parseAndValidateSkillMD(in.SkillMD, in.References)
	if err != nil {
		return nil, err
	}
	if fm.Name != name {
		return nil, validationErr("NAME_MISMATCH",
			fmt.Sprintf("frontmatter name %q must equal the request name %q", fm.Name, name), "name")
	}

	// Collision: any builtin (org_id='') OR this org's existing row.
	existing, err := m.skills.Resolve(ctx, orgID, name)
	if err != nil {
		return nil, fmt.Errorf("collision check: %w", err)
	}
	if existing != nil {
		return nil, ErrSkillNameCollision
	}

	refs := normalizeRefs(in.References)
	sha := contentSHA(in.SkillMD, refs)
	version := versionFromMetadata(fm)
	now := time.Now().UTC()

	refsJSON, _ := marshalRefs(refs)
	q := `
		INSERT INTO skills
			(org_id, skill_name, kind, description, skill_md, "references",
			 version, content_sha, license, compatibility, created_at, updated_at, updated_by)
		VALUES (?, ?, 'custom', ?, ?, ?::jsonb, ?, ?, NULLIF(?, ''), NULLIF(?, ''), ?, ?, ?)
	`
	if err := m.db.WithContext(ctx).Exec(q,
		orgID, name, strings.TrimSpace(fm.Description), in.SkillMD, string(refsJSON),
		version, sha, fm.License, fm.Compatibility, now, now, actor,
	).Error; err != nil {
		return nil, fmt.Errorf("insert custom skill %q: %w", name, err)
	}
	m.audit(ctx, orgID, name, "create", actor)
	slog.InfoContext(ctx, "skill created", "orgID", orgID, "name", name, "actor", actor)
	return m.skills.Resolve(ctx, orgID, name)
}

// Update rewrites an existing kind=custom skill. Returns ErrSkillNotEditable
// for builtins, ErrSkillNotFound when the name is not an editable custom row.
func (m *SkillMutationService) Update(ctx context.Context, orgID, actor, name string, in UpdateSkillInput) (*Skill, error) {
	if m == nil || m.db == nil {
		return nil, fmt.Errorf("skill mutation service: not configured")
	}
	existing, err := m.skills.Resolve(ctx, orgID, name)
	if err != nil {
		return nil, fmt.Errorf("resolve %q: %w", name, err)
	}
	if existing == nil {
		return nil, ErrSkillNotFound
	}
	if existing.Kind == "builtin" {
		return nil, ErrSkillNotEditable
	}
	if existing.Kind != "custom" {
		// imported skills are replaced via re-import, not PUT.
		return nil, ErrSkillNotFound
	}

	fm, _, err := parseAndValidateSkillMD(in.SkillMD, in.References)
	if err != nil {
		return nil, err
	}
	if fm.Name != name {
		return nil, validationErr("NAME_IMMUTABLE",
			"cannot rename a skill via update; frontmatter name must match the existing name", "name")
	}

	refs := normalizeRefs(in.References)
	sha := contentSHA(in.SkillMD, refs)
	version := versionFromMetadata(fm)
	now := time.Now().UTC()

	refsJSON, _ := marshalRefs(refs)
	q := `
		UPDATE skills
		   SET description = ?, skill_md = ?, "references" = ?::jsonb,
		       version = ?, content_sha = ?, updated_at = ?, updated_by = ?
		 WHERE org_id = ? AND skill_name = ? AND kind = 'custom'
	`
	res := m.db.WithContext(ctx).Exec(q,
		strings.TrimSpace(fm.Description), in.SkillMD, string(refsJSON),
		version, sha, now, actor, orgID, name,
	)
	if res.Error != nil {
		return nil, fmt.Errorf("update custom skill %q: %w", name, res.Error)
	}
	if res.RowsAffected == 0 {
		return nil, ErrSkillNotFound
	}
	m.audit(ctx, orgID, name, "update", actor)
	slog.InfoContext(ctx, "skill updated", "orgID", orgID, "name", name, "actor", actor)
	return m.skills.Resolve(ctx, orgID, name)
}

// Delete removes a custom or imported skill. Builtins return
// ErrSkillNotEditable; imported skills referenced by in-flight tasks
// return ErrImportedSkillInUse.
func (m *SkillMutationService) Delete(ctx context.Context, orgID, actor, name string) error {
	if m == nil || m.db == nil {
		return fmt.Errorf("skill mutation service: not configured")
	}
	existing, err := m.skills.Resolve(ctx, orgID, name)
	if err != nil {
		return fmt.Errorf("resolve %q: %w", name, err)
	}
	if existing == nil {
		return ErrSkillNotFound
	}
	if existing.Kind == "builtin" {
		return ErrSkillNotEditable
	}

	if existing.Kind == "imported" {
		inUse, err := m.importedSkillInUse(ctx, orgID, name)
		if err != nil {
			return fmt.Errorf("in-use check %q: %w", name, err)
		}
		if inUse {
			return ErrImportedSkillInUse
		}
	}

	res := m.db.WithContext(ctx).Exec(
		`DELETE FROM skills WHERE org_id = ? AND skill_name = ? AND kind <> 'builtin'`, orgID, name)
	if res.Error != nil {
		return fmt.Errorf("delete skill %q: %w", name, res.Error)
	}
	if res.RowsAffected == 0 {
		return ErrSkillNotFound
	}
	m.audit(ctx, orgID, name, "delete", actor)
	slog.InfoContext(ctx, "skill deleted", "orgID", orgID, "name", name, "actor", actor)
	return nil
}

// importedSkillInUse reports whether any in-flight task in the org has a
// snapshot row referencing imported/<name>. In-flight = not yet
// merged/rejected/abandoned/deployed/failed.
func (m *SkillMutationService) importedSkillInUse(ctx context.Context, orgID, name string) (bool, error) {
	var count int64
	err := m.db.WithContext(ctx).Raw(
		`SELECT COUNT(*) FROM design_version_skill_snapshots s
		 JOIN component_tasks t
		   ON t.project_id = s.project_id AND t.source_design_version = s.design_version
		 WHERE s.skill_id = ? AND t.org_id = ?
		   AND t.status NOT IN ('merged','rejected','abandoned','deployed','failed')`,
		PrefixedID("imported", name), orgID,
	).Scan(&count).Error
	if err != nil {
		return false, err
	}
	return count > 0, nil
}

// audit is a best-effort write to skill_audit_events. Failures log + ignore.
func (m *SkillMutationService) audit(ctx context.Context, orgID, name, action, actor string) {
	q := `
		INSERT INTO skill_audit_events
			(org_id, skill_name, action, actor, occurred_at)
		VALUES (?, ?, ?, ?, NOW())
	`
	if err := m.db.WithContext(ctx).Exec(q, orgID, name, action, actor).Error; err != nil {
		slog.WarnContext(ctx, "skill audit write failed", "action", action, "name", name, "error", err)
	}
}

// ---- shared validation helpers ---------------------------------------------

// validateSkillName enforces the AgentSkills kebab rule, the custom-name
// length cap, and reserved-name rules.
func validateSkillName(name string) *SkillValidationError {
	if name == "" {
		return validationErr("NAME_REQUIRED", "name is required", "name")
	}
	if len(name) > maxCustomNameLen {
		return validationErr("NAME_TOO_LONG",
			fmt.Sprintf("name must be ≤ %d chars (leaves room for the materialisation prefix)", maxCustomNameLen), "name")
	}
	if !skillNameRE.MatchString(name) {
		return validationErr("NAME_INVALID",
			"name must be lowercase kebab-case: alphanumeric segments joined by single hyphens, no leading/trailing/double hyphen", "name")
	}
	if reservedSkillNames[name] {
		return validationErr("NAME_RESERVED", fmt.Sprintf("%q is a reserved name", name), "name")
	}
	for _, p := range reservedSkillPrefixes {
		if strings.HasPrefix(name, p) {
			return validationErr("NAME_RESERVED",
				fmt.Sprintf("name must not start with the reserved prefix %q", p), "name")
		}
	}
	return nil
}

// parseAndValidateSkillMD parses the frontmatter, enforces description
// length, reference-key shape, and total size. Returns the parsed
// frontmatter + body, or a SkillValidationError.
func parseAndValidateSkillMD(skillMD string, references map[string]string) (skillFrontmatter, string, error) {
	if strings.TrimSpace(skillMD) == "" {
		return skillFrontmatter{}, "", validationErr("SKILL_MD_REQUIRED", "skillMd is required", "skillMd")
	}
	fm, body, err := parseSkillMD(skillMD)
	if err != nil {
		return skillFrontmatter{}, "", validationErr("FRONTMATTER_INVALID", err.Error(), "skillMd")
	}
	if n := len(strings.TrimSpace(fm.Description)); n < 1 || n > 1024 {
		return skillFrontmatter{}, "", validationErr("DESCRIPTION_LENGTH",
			"description must be 1–1024 chars", "description")
	}
	total := len(skillMD)
	for path, content := range references {
		if !strings.HasPrefix(path, "references/") || !strings.HasSuffix(path, ".md") {
			return skillFrontmatter{}, "", validationErr("REFERENCE_PATH_INVALID",
				fmt.Sprintf("reference key %q must be of the form references/<file>.md", path), "references")
		}
		if strings.Contains(path, "..") || strings.Contains(path, "//") {
			return skillFrontmatter{}, "", validationErr("REFERENCE_PATH_INVALID",
				fmt.Sprintf("reference key %q must not contain path traversal", path), "references")
		}
		total += len(content)
	}
	if total > maxSkillBytes {
		return skillFrontmatter{}, "", validationErr("SIZE_EXCEEDED",
			fmt.Sprintf("total skill size %d bytes exceeds the %d-byte limit", total, maxSkillBytes), "")
	}
	return fm, body, nil
}

// normalizeRefs returns a non-nil References map from the raw input.
func normalizeRefs(in map[string]string) References {
	out := References{}
	for k, v := range in {
		out[k] = v
	}
	return out
}

// MarshalValidationIssues is a JSON helper for controllers rendering a
// SkillValidationError into the wire body.
func MarshalValidationIssues(e *SkillValidationError) []byte {
	b, _ := json.Marshal(map[string]any{"error": "Bad Request", "issues": e.Issues})
	return b
}
