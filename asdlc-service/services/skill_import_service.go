package services

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"fmt"
	"io"
	"log/slog"
	"path"
	"strings"
	"time"

	"gorm.io/gorm"
)

// importMaxBytes caps the total decompressed payload we read from a
// tarball — a coarse zip-bomb guard on top of per-skill validation.
const importMaxBytes = 2 * 1024 * 1024

// unsupportedRuntimes are runtime tokens the runner image does NOT ship.
// A `compatibility` frontmatter mentioning one yields a warning (not a block).
var unsupportedRuntimes = []string{"python", "rust", "ruby", "java", "dotnet", "deno", "bun"}

// ImportResult is the response body for a successful import.
type ImportResult struct {
	Name          string   `json:"name"`
	Kind          string   `json:"kind"`
	License       string   `json:"license,omitempty"`
	Compatibility string   `json:"compatibility,omitempty"`
	Warnings      []string `json:"warnings"`
}

// SkillImportService decodes an AgentSkills tarball, validates it against
// our narrowed scope (SKILL.md + references/<file>.md only), and stores it
// as a kind=imported row. See docs/design/skills-system.md > "Validation
// rules" and "AgentSkills compatibility".
type SkillImportService struct {
	db     *gorm.DB
	skills *SkillService
}

func NewSkillImportService(db *gorm.DB, skills *SkillService) *SkillImportService {
	return &SkillImportService{db: db, skills: skills}
}

// Import reads a gzip-compressed tarball, validates it, and persists the
// skill. Returns a SkillValidationError for any structural/validation
// failure (nothing persists), or the import result with warnings.
func (s *SkillImportService) Import(ctx context.Context, orgID, actor string, r io.Reader) (*ImportResult, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("skill import service: not configured")
	}

	topDir, skillMD, refs, err := extractTarball(r)
	if err != nil {
		return nil, err
	}

	name := strings.TrimSpace(topDir)
	if issues := validateSkillName(name); issues != nil {
		return nil, issues
	}
	fm, _, err := parseAndValidateSkillMD(skillMD, refs)
	if err != nil {
		return nil, err
	}
	if fm.Name != name {
		return nil, validationErr("NAME_MISMATCH",
			fmt.Sprintf("frontmatter name %q must equal the tarball's top-level directory %q", fm.Name, name), "name")
	}

	existing, err := s.skills.Resolve(ctx, orgID, name)
	if err != nil {
		return nil, fmt.Errorf("collision check: %w", err)
	}
	if existing != nil {
		return nil, ErrSkillNameCollision
	}

	warnings := importWarnings(fm)

	refsMap := normalizeRefs(refs)
	sha := contentSHA(skillMD, refsMap)
	version := versionFromMetadata(fm)
	now := time.Now().UTC()
	refsJSON, _ := marshalRefs(refsMap)

	q := `
		INSERT INTO skills
			(org_id, skill_name, kind, description, skill_md, "references",
			 version, content_sha, license, compatibility, created_at, updated_at, updated_by)
		VALUES (?, ?, 'imported', ?, ?, ?::jsonb, ?, ?, NULLIF(?, ''), NULLIF(?, ''), ?, ?, ?)
	`
	if err := s.db.WithContext(ctx).Exec(q,
		orgID, name, strings.TrimSpace(fm.Description), skillMD, string(refsJSON),
		version, sha, fm.License, fm.Compatibility, now, now, actor,
	).Error; err != nil {
		return nil, fmt.Errorf("insert imported skill %q: %w", name, err)
	}
	s.audit(ctx, orgID, name, actor)
	slog.InfoContext(ctx, "skill imported", "orgID", orgID, "name", name, "actor", actor, "warnings", len(warnings))

	return &ImportResult{
		Name:          name,
		Kind:          "imported",
		License:       fm.License,
		Compatibility: fm.Compatibility,
		Warnings:      warnings,
	}, nil
}

// audit mirrors the mutation service's best-effort audit write.
func (s *SkillImportService) audit(ctx context.Context, orgID, name, actor string) {
	q := `
		INSERT INTO skill_audit_events
			(org_id, skill_name, action, actor, occurred_at)
		VALUES (?, ?, 'import', ?, NOW())
	`
	if err := s.db.WithContext(ctx).Exec(q, orgID, name, actor).Error; err != nil {
		slog.WarnContext(ctx, "skill audit write failed", "action", "import", "name", name, "error", err)
	}
}

// extractTarball decodes a gzip+tar stream into (topDir, SKILL.md body,
// references map). Enforces: a single top-level directory; only SKILL.md +
// references/<file>.md; no symlinks/hardlinks/.. paths/AppleDouble entries.
func extractTarball(r io.Reader) (topDir, skillMD string, refs map[string]string, err error) {
	gz, gerr := gzip.NewReader(r)
	if gerr != nil {
		return "", "", nil, validationErr("TARBALL_INVALID", "not a valid gzip stream: "+gerr.Error(), "")
	}
	defer gz.Close()

	tr := tar.NewReader(gz)
	refs = map[string]string{}
	limited := &byteBudget{remaining: importMaxBytes}

	for {
		hdr, terr := tr.Next()
		if terr == io.EOF {
			break
		}
		if terr != nil {
			return "", "", nil, validationErr("TARBALL_INVALID", "read tar: "+terr.Error(), "")
		}

		// Reject anything that isn't a regular file or directory.
		switch hdr.Typeflag {
		case tar.TypeReg, tar.TypeDir:
			// ok
		case tar.TypeSymlink, tar.TypeLink:
			return "", "", nil, validationErr("UNSAFE_ENTRY",
				fmt.Sprintf("symlinks/hardlinks are not allowed (%q)", hdr.Name), "")
		default:
			return "", "", nil, validationErr("UNSAFE_ENTRY",
				fmt.Sprintf("unsupported tar entry type for %q", hdr.Name), "")
		}

		clean := path.Clean(hdr.Name)
		if clean == "." || clean == "/" {
			continue
		}
		if strings.HasPrefix(clean, "..") || strings.Contains(clean, "../") || path.IsAbs(clean) {
			return "", "", nil, validationErr("UNSAFE_ENTRY",
				fmt.Sprintf("path traversal in %q", hdr.Name), "")
		}
		// AppleDouble / macOS metadata.
		base := path.Base(clean)
		if strings.HasPrefix(base, "._") || clean == "__MACOSX" || strings.HasPrefix(clean, "__MACOSX/") || base == ".DS_Store" {
			continue
		}

		segments := strings.Split(clean, "/")
		if topDir == "" {
			topDir = segments[0]
		} else if segments[0] != topDir {
			return "", "", nil, validationErr("MULTIPLE_TOP_DIRS",
				"tarball must contain exactly one top-level directory", "")
		}

		if hdr.Typeflag == tar.TypeDir {
			continue
		}

		rel := strings.Join(segments[1:], "/") // path inside the top dir
		switch {
		case rel == "SKILL.md":
			b, rerr := limited.readAll(tr)
			if rerr != nil {
				return "", "", nil, rerr
			}
			skillMD = string(b)
		case strings.HasPrefix(rel, "references/") && strings.HasSuffix(rel, ".md"):
			b, rerr := limited.readAll(tr)
			if rerr != nil {
				return "", "", nil, rerr
			}
			refs[rel] = string(b)
		default:
			return "", "", nil, validationErr("DISALLOWED_FILE",
				fmt.Sprintf("only SKILL.md and references/<file>.md are allowed (found %q)", rel), "")
		}
	}

	if topDir == "" {
		return "", "", nil, validationErr("TARBALL_EMPTY", "tarball has no entries", "")
	}
	if skillMD == "" {
		return "", "", nil, validationErr("SKILL_MD_MISSING", "tarball is missing SKILL.md", "")
	}
	return topDir, skillMD, refs, nil
}

// byteBudget bounds total bytes read across all tar entries.
type byteBudget struct{ remaining int64 }

func (b *byteBudget) readAll(r io.Reader) ([]byte, error) {
	limited := io.LimitReader(r, b.remaining+1)
	data, err := io.ReadAll(limited)
	if err != nil {
		return nil, validationErr("TARBALL_INVALID", "read entry: "+err.Error(), "")
	}
	if int64(len(data)) > b.remaining {
		return nil, validationErr("SIZE_EXCEEDED",
			fmt.Sprintf("decompressed payload exceeds the %d-byte limit", importMaxBytes), "")
	}
	b.remaining -= int64(len(data))
	return data, nil
}

// importWarnings collects non-blocking advisories: unsupported runtime
// compatibility, and the ignored allowed-tools field.
func importWarnings(fm skillFrontmatter) []string {
	// Non-nil so JSON marshals as [] not null — the console reads
	// result.warnings.length directly.
	warnings := []string{}
	if c := strings.ToLower(strings.TrimSpace(fm.Compatibility)); c != "" {
		for _, rt := range unsupportedRuntimes {
			if strings.Contains(c, rt) {
				warnings = append(warnings, fmt.Sprintf("compatibility %q requires %q, which the runner image does not ship; the skill is stored but may not run as authored", fm.Compatibility, rt))
				break
			}
		}
	}
	if fm.AllowedTools != nil {
		warnings = append(warnings, "allowed-tools is preserved in metadata but not honoured by the runner in v1 (allowed_tools_ignored)")
	}
	return warnings
}
