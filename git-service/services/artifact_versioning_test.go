package services

import (
	"reflect"
	"testing"
)

func TestNextVersion(t *testing.T) {
	cases := []struct {
		name    string
		tags    []TagInfo
		prefix  string
		wantN   int
		wantTag string
	}{
		{"empty", nil, specTagPrefix, 1, "spec-v1"},
		{"single", []TagInfo{{Name: "spec-v1"}}, specTagPrefix, 2, "spec-v2"},
		{"gappy", []TagInfo{{Name: "spec-v1"}, {Name: "spec-v3"}}, specTagPrefix, 4, "spec-v4"},
		{"prefix-mixed", []TagInfo{{Name: "spec-v2"}, {Name: "design-v9"}}, specTagPrefix, 3, "spec-v3"},
		{"non-numeric ignored", []TagInfo{{Name: "spec-vfoo"}, {Name: "spec-v2"}}, specTagPrefix, 3, "spec-v3"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			n, tag := nextVersion(tc.tags, tc.prefix)
			if n != tc.wantN || tag != tc.wantTag {
				t.Errorf("nextVersion = (%d, %q), want (%d, %q)", n, tag, tc.wantN, tc.wantTag)
			}
		})
	}
}

func TestParseLineage(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want Lineage
	}{
		{"empty", "", Lineage{}},
		{"description-only", "Specification v3", Lineage{}},
		{"spec-only", "Architecture design v2\nsource-spec: spec-v5", Lineage{SourceSpec: "spec-v5"}},
		{"both", "Design v1\nsource-spec: spec-v2\nsource-design: design-v0", Lineage{SourceSpec: "spec-v2", SourceDesign: "design-v0"}},
		{"trailing whitespace", "x\nsource-spec:   spec-v7   ", Lineage{SourceSpec: "spec-v7"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := parseLineage(tc.in)
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("parseLineage(%q) = %+v, want %+v", tc.in, got, tc.want)
			}
		})
	}
}

func TestBuildLineageMessage_Roundtrip(t *testing.T) {
	cases := []Lineage{
		{},
		{SourceSpec: "spec-v3"},
		{SourceDesign: "design-v9"},
		{SourceSpec: "spec-v1", SourceDesign: "design-v2"},
	}
	for _, l := range cases {
		msg := buildLineageMessage("Description", l)
		got := parseLineage(msg)
		if !reflect.DeepEqual(got, l) {
			t.Errorf("roundtrip mismatch: built %q, parsed %+v, want %+v", msg, got, l)
		}
	}
}

func TestTagsToVersions_DescendingByVersion(t *testing.T) {
	tags := []TagInfo{
		{Name: "spec-v1", CommitHash: "h1", Message: "first"},
		{Name: "spec-v3", CommitHash: "h3", Message: "third\nsource-spec: spec-v2"},
		{Name: "spec-v2", CommitHash: "h2", Message: "second"},
		{Name: "design-v9", CommitHash: "ignored", Message: "wrong-prefix"},
	}
	got := tagsToVersions(tags, specTagPrefix)
	if len(got) != 3 {
		t.Fatalf("expected 3 entries, got %d", len(got))
	}
	if got[0].Version != 3 || got[1].Version != 2 || got[2].Version != 1 {
		t.Errorf("not sorted descending: %+v", got)
	}
	// Lineage parsed structurally on the v3 entry.
	if got[0].Lineage.SourceSpec != "spec-v2" {
		t.Errorf("v3 lineage SourceSpec = %q, want spec-v2", got[0].Lineage.SourceSpec)
	}
}

func TestValidateRelPath(t *testing.T) {
	cases := []struct {
		name    string
		in      string
		wantErr bool
	}{
		{"spec", ".asdlc/spec.md", false},
		{"design", ".asdlc/design.json", false},
		{"wireframe", ".asdlc/wireframes/foo.html", false},

		{"empty", "", true},
		{"absolute", "/etc/passwd", true},
		{"traversal-up", "..", true},
		{"traversal-mid", ".asdlc/../etc/passwd", true},
		{"non-asdlc", "src/main.go", true},
		{"non-canonical-trailing-slash", ".asdlc/spec.md/", true},
		{"non-canonical-double-slash", ".asdlc//spec.md", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := validateRelPath(tc.in)
			if tc.wantErr && err == nil {
				t.Errorf("validateRelPath(%q) = nil, want error", tc.in)
			}
			if !tc.wantErr && err != nil {
				t.Errorf("validateRelPath(%q) = %v, want nil", tc.in, err)
			}
		})
	}
}

func TestValidateWireframeName(t *testing.T) {
	cases := []struct {
		name    string
		in      string
		wantErr bool
	}{
		{"simple", "spec.html", false},
		{"hyphen", "page-1.html", false},
		{"underscore", "x_y.html", false},

		{"empty", "", true},
		{"slash", "a/b.html", true},
		{"traversal", "../etc/passwd", true},
		{"leading-dot", ".hidden", true},
		{"too-long", string(make([]byte, 200)), true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := validateWireframeName(tc.in)
			if tc.wantErr && err == nil {
				t.Errorf("validateWireframeName(%q) = nil, want error", tc.in)
			}
			if !tc.wantErr && err != nil {
				t.Errorf("validateWireframeName(%q) = %v, want nil", tc.in, err)
			}
		})
	}
}
