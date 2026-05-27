package services

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"errors"
	"strings"
	"testing"
)

func TestValidateSkillName(t *testing.T) {
	cases := []struct {
		name    string
		in      string
		wantErr string // "" = expect ok; otherwise the issue Code
	}{
		{"ok simple", "payments", ""},
		{"ok kebab", "payments-pci-handling", ""},
		{"empty", "", "NAME_REQUIRED"},
		{"uppercase", "Payments", "NAME_INVALID"},
		{"leading hyphen", "-payments", "NAME_INVALID"},
		{"trailing hyphen", "payments-", "NAME_INVALID"},
		{"double hyphen", "pay--ments", "NAME_INVALID"},
		{"underscore", "pay_ments", "NAME_INVALID"},
		{"reserved asdlc", "asdlc", "NAME_RESERVED"},
		{"reserved prefix builtin", "builtin-foo", "NAME_RESERVED"},
		{"reserved prefix custom", "custom-foo", "NAME_RESERVED"},
		{"reserved prefix imported", "imported-foo", "NAME_RESERVED"},
		{"too long", strings.Repeat("a", maxCustomNameLen+1), "NAME_TOO_LONG"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := validateSkillName(tc.in)
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("expected ok, got %v", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("expected error %s, got nil", tc.wantErr)
			}
			if err.Issues[0].Code != tc.wantErr {
				t.Fatalf("expected code %s, got %s", tc.wantErr, err.Issues[0].Code)
			}
		})
	}
}

const validSkillMD = `---
name: payments-pci-handling
description: PCI-DSS logging requirements for components that touch card data.
metadata:
  asdlc.version: "2"
---

# Payments PCI Handling

## What this skill does
Logs PAN access.
`

func TestParseAndValidateSkillMD(t *testing.T) {
	t.Run("valid", func(t *testing.T) {
		fm, body, err := parseAndValidateSkillMD(validSkillMD, nil)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if fm.Name != "payments-pci-handling" {
			t.Fatalf("name = %q", fm.Name)
		}
		if !strings.Contains(body, "Logs PAN access") {
			t.Fatalf("body missing content: %q", body)
		}
	})

	t.Run("empty", func(t *testing.T) {
		_, _, err := parseAndValidateSkillMD("   ", nil)
		assertIssueCode(t, err, "SKILL_MD_REQUIRED")
	})

	t.Run("no frontmatter", func(t *testing.T) {
		_, _, err := parseAndValidateSkillMD("# Just a heading\n", nil)
		assertIssueCode(t, err, "FRONTMATTER_INVALID")
	})

	t.Run("bad reference path", func(t *testing.T) {
		_, _, err := parseAndValidateSkillMD(validSkillMD, map[string]string{"scripts/run.sh": "echo hi"})
		assertIssueCode(t, err, "REFERENCE_PATH_INVALID")
	})

	t.Run("reference traversal", func(t *testing.T) {
		_, _, err := parseAndValidateSkillMD(validSkillMD, map[string]string{"references/../x.md": "x"})
		assertIssueCode(t, err, "REFERENCE_PATH_INVALID")
	})

	t.Run("oversize", func(t *testing.T) {
		big := validSkillMD + strings.Repeat("x", maxSkillBytes)
		_, _, err := parseAndValidateSkillMD(big, nil)
		assertIssueCode(t, err, "SIZE_EXCEEDED")
	})
}

func TestVersionFromMetadata(t *testing.T) {
	cases := []struct {
		name string
		md   string
		want int
	}{
		{"flat dotted key string", "metadata:\n  asdlc.version: \"2\"\n", 2},
		{"flat dotted key int", "metadata:\n  asdlc.version: 3\n", 3},
		{"nested form", "metadata:\n  asdlc:\n    version: \"4\"\n", 4},
		{"absent defaults to 1", "metadata:\n  other: x\n", 1},
		{"no metadata defaults to 1", "", 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			full := "---\nname: x\ndescription: d\n" + tc.md + "---\n\n# X\n"
			fm, _, err := parseSkillMD(full)
			if err != nil {
				t.Fatalf("parse: %v", err)
			}
			if got := versionFromMetadata(fm); got != tc.want {
				t.Fatalf("version = %d, want %d", got, tc.want)
			}
		})
	}
}

func TestImportWarnings(t *testing.T) {
	t.Run("unsupported runtime", func(t *testing.T) {
		w := importWarnings(skillFrontmatter{Compatibility: "python>=3.11"})
		if len(w) != 1 || !strings.Contains(w[0], "python") {
			t.Fatalf("expected python warning, got %v", w)
		}
	})
	t.Run("allowed-tools ignored", func(t *testing.T) {
		w := importWarnings(skillFrontmatter{AllowedTools: []string{"Bash"}})
		if len(w) != 1 || !strings.Contains(w[0], "allowed_tools_ignored") {
			t.Fatalf("expected allowed-tools warning, got %v", w)
		}
	})
	t.Run("none", func(t *testing.T) {
		if w := importWarnings(skillFrontmatter{}); len(w) != 0 {
			t.Fatalf("expected no warnings, got %v", w)
		}
	})
}

// makeTarGz builds a gzip tarball from a name→content map. A trailing "/"
// in a key marks a directory entry.
func makeTarGz(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for name, content := range files {
		hdr := &tar.Header{Name: name, Mode: 0o644, Size: int64(len(content))}
		if strings.HasSuffix(name, "/") {
			hdr.Typeflag = tar.TypeDir
			hdr.Size = 0
		}
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatal(err)
		}
		if hdr.Typeflag != tar.TypeDir {
			if _, err := tw.Write([]byte(content)); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestExtractTarball(t *testing.T) {
	t.Run("valid with reference", func(t *testing.T) {
		tgz := makeTarGz(t, map[string]string{
			"my-skill/":                 "",
			"my-skill/SKILL.md":         validSkillMD,
			"my-skill/references/ex.md": "example",
			"my-skill/.DS_Store":        "junk",
		})
		top, md, refs, err := extractTarball(bytes.NewReader(tgz))
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if top != "my-skill" {
			t.Fatalf("topDir = %q", top)
		}
		if !strings.Contains(md, "payments-pci-handling") {
			t.Fatalf("SKILL.md not captured")
		}
		if refs["references/ex.md"] != "example" {
			t.Fatalf("reference not captured: %v", refs)
		}
	})

	t.Run("missing SKILL.md", func(t *testing.T) {
		tgz := makeTarGz(t, map[string]string{"my-skill/README.md": "x"})
		_, _, _, err := extractTarball(bytes.NewReader(tgz))
		// README.md is a disallowed file, caught before the missing-SKILL check.
		assertIssueCode(t, err, "DISALLOWED_FILE")
	})

	t.Run("disallowed scripts dir", func(t *testing.T) {
		tgz := makeTarGz(t, map[string]string{
			"my-skill/SKILL.md":     validSkillMD,
			"my-skill/scripts/x.sh": "echo",
		})
		_, _, _, err := extractTarball(bytes.NewReader(tgz))
		assertIssueCode(t, err, "DISALLOWED_FILE")
	})

	t.Run("multiple top dirs", func(t *testing.T) {
		tgz := makeTarGz(t, map[string]string{
			"a/SKILL.md": validSkillMD,
			"b/SKILL.md": validSkillMD,
		})
		_, _, _, err := extractTarball(bytes.NewReader(tgz))
		assertIssueCode(t, err, "MULTIPLE_TOP_DIRS")
	})

	t.Run("not gzip", func(t *testing.T) {
		_, _, _, err := extractTarball(strings.NewReader("not a gzip"))
		assertIssueCode(t, err, "TARBALL_INVALID")
	})
}

func assertIssueCode(t *testing.T, err error, code string) {
	t.Helper()
	if err == nil {
		t.Fatalf("expected error with code %s, got nil", code)
	}
	var verr *SkillValidationError
	if !errors.As(err, &verr) {
		t.Fatalf("expected *SkillValidationError, got %T: %v", err, err)
	}
	if verr.Issues[0].Code != code {
		t.Fatalf("expected code %s, got %s (%s)", code, verr.Issues[0].Code, verr.Issues[0].Message)
	}
}
