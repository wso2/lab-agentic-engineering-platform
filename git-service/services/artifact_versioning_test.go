package services

import (
	"reflect"
	"testing"
)

func TestParseRequirementsTag(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want int
		ok   bool
	}{
		{"v1", "v1", 1, true},
		{"v17", "v17", 17, true},
		{"design tag rejected", "v1-2", 0, false},
		{"empty", "", 0, false},
		{"prefix only", "v", 0, false},
		{"non-numeric", "vX", 0, false},
		{"zero", "v0", 0, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := parseRequirementsTag(tc.in)
			if got != tc.want || ok != tc.ok {
				t.Errorf("parseRequirementsTag(%q) = (%d, %v), want (%d, %v)",
					tc.in, got, ok, tc.want, tc.ok)
			}
		})
	}
}

func TestParseDesignTag(t *testing.T) {
	cases := []struct {
		name    string
		in      string
		wantN   int
		wantR   int
		wantOK  bool
	}{
		{"v1-1", "v1-1", 1, 1, true},
		{"v3-12", "v3-12", 3, 12, true},
		{"requirements tag rejected", "v2", 0, 0, false},
		{"missing rev", "v1-", 0, 0, false},
		{"missing parent", "v-1", 0, 0, false},
		{"trailing junk", "v1-1x", 0, 0, false},
		{"zero rev", "v1-0", 0, 0, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			n, r, ok := parseDesignTag(tc.in)
			if n != tc.wantN || r != tc.wantR || ok != tc.wantOK {
				t.Errorf("parseDesignTag(%q) = (%d, %d, %v), want (%d, %d, %v)",
					tc.in, n, r, ok, tc.wantN, tc.wantR, tc.wantOK)
			}
		})
	}
}

func TestNextRequirementsTag(t *testing.T) {
	cases := []struct {
		name    string
		tags    []TagInfo
		wantN   int
		wantTag string
	}{
		{"empty", nil, 1, "v1"},
		{"single", []TagInfo{{Name: "v1"}}, 2, "v2"},
		{"gappy", []TagInfo{{Name: "v1"}, {Name: "v3"}}, 4, "v4"},
		{"design tags ignored", []TagInfo{{Name: "v2"}, {Name: "v9-3"}}, 3, "v3"},
		{"non-matching ignored", []TagInfo{{Name: "release-1"}, {Name: "v2"}}, 3, "v3"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			n, tag := nextRequirementsTag(tc.tags)
			if n != tc.wantN || tag != tc.wantTag {
				t.Errorf("nextRequirementsTag = (%d, %q), want (%d, %q)",
					n, tag, tc.wantN, tc.wantTag)
			}
		})
	}
}

func TestNextDesignTag(t *testing.T) {
	cases := []struct {
		name      string
		tags      []TagInfo
		parent    int
		wantR     int
		wantTag   string
	}{
		{"empty", nil, 1, 1, "v1-1"},
		{"existing parent", []TagInfo{{Name: "v1-1"}, {Name: "v1-2"}}, 1, 3, "v1-3"},
		{"different parent isolated", []TagInfo{{Name: "v1-5"}, {Name: "v2-1"}}, 2, 2, "v2-2"},
		{"requirements tags ignored", []TagInfo{{Name: "v1"}, {Name: "v2"}}, 2, 1, "v2-1"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r, tag := nextDesignTag(tc.tags, tc.parent)
			if r != tc.wantR || tag != tc.wantTag {
				t.Errorf("nextDesignTag(_, %d) = (%d, %q), want (%d, %q)",
					tc.parent, r, tag, tc.wantR, tc.wantTag)
			}
		})
	}
}

func TestLatestRequirementsTag(t *testing.T) {
	cases := []struct {
		name string
		tags []TagInfo
		want string
	}{
		{"empty", nil, ""},
		{"only design tags", []TagInfo{{Name: "v1-1"}, {Name: "v2-3"}}, ""},
		{"mixed", []TagInfo{{Name: "v1"}, {Name: "v3"}, {Name: "v2-9"}}, "v3"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := latestRequirementsTag(tc.tags); got != tc.want {
				t.Errorf("latestRequirementsTag = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestLatestDesignTag(t *testing.T) {
	cases := []struct {
		name string
		tags []TagInfo
		want string
	}{
		{"empty", nil, ""},
		{"only requirements tags", []TagInfo{{Name: "v1"}, {Name: "v2"}}, ""},
		{"highest parent wins", []TagInfo{{Name: "v1-9"}, {Name: "v2-1"}, {Name: "v2-3"}}, "v2-3"},
		{"highest revision within parent", []TagInfo{{Name: "v3-1"}, {Name: "v3-7"}, {Name: "v3-4"}}, "v3-7"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := latestDesignTag(tc.tags); got != tc.want {
				t.Errorf("latestDesignTag = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestTagsToRequirementsVersions_DescendingByVersion(t *testing.T) {
	tags := []TagInfo{
		{Name: "v1", CommitHash: "h1", Message: "first"},
		{Name: "v3", CommitHash: "h3", Message: "third"},
		{Name: "v2", CommitHash: "h2", Message: "second"},
		{Name: "v9-1", CommitHash: "ignored", Message: "design"},
	}
	got := tagsToRequirementsVersions(tags)
	if len(got) != 3 {
		t.Fatalf("expected 3 entries, got %d", len(got))
	}
	if got[0].Version != 3 || got[1].Version != 2 || got[2].Version != 1 {
		t.Errorf("not sorted descending: %+v", got)
	}
}

func TestTagsToDesignVersions_DescendingByParentThenRevision(t *testing.T) {
	tags := []TagInfo{
		{Name: "v1-1", CommitHash: "h1", Message: "d1"},
		{Name: "v2-3", CommitHash: "h2", Message: "d2"},
		{Name: "v2-1", CommitHash: "h3", Message: "d3"},
		{Name: "v1-2", CommitHash: "h4", Message: "d4"},
		{Name: "v3", CommitHash: "ignored", Message: "req"},
	}
	got := tagsToDesignVersions(tags)
	if len(got) != 4 {
		t.Fatalf("expected 4 entries, got %d", len(got))
	}
	wantOrder := []string{"v2-3", "v2-1", "v1-2", "v1-1"}
	for i, w := range wantOrder {
		if got[i].Tag != w {
			t.Errorf("got[%d].Tag = %q, want %q (full: %+v)", i, got[i].Tag, w, got)
		}
	}
}

func TestValidateRelPath(t *testing.T) {
	cases := []struct {
		name    string
		in      string
		wantErr bool
	}{
		{"requirements file", ".asdlc/requirements/requirements.md", false},
		{"requirements other", ".asdlc/requirements/functional-requirements.md", false},
		{"design root", ".asdlc/design/design.md", false},
		{"design component", ".asdlc/design/components/user-api/design.md", false},
		{"design openapi", ".asdlc/design/components/user-api/openapi.yaml", false},

		{"empty", "", true},
		{"absolute", "/etc/passwd", true},
		{"traversal-up", "..", true},
		{"traversal-mid", ".asdlc/../etc/passwd", true},
		{"non-asdlc", "src/main.go", true},
		{"non-canonical-trailing-slash", ".asdlc/requirements/foo.md/", true},
		{"non-canonical-double-slash", ".asdlc//requirements/foo.md", true},
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

func TestValidateRequirementFilename(t *testing.T) {
	cases := []struct {
		name    string
		in      string
		wantErr bool
	}{
		{"main", "requirements.md", false},
		{"functional", "functional-requirements.md", false},
		{"upper-MD", "Requirements.MD", false},
		{"excalidraw", "wireframes.excalidraw", false},
		{"excalidraw-suffixed", "domain-model-2.excalidraw", false},
		{"excalidraw-upper", "Diagram.EXCALIDRAW", false},

		{"empty", "", true},
		{"with slash", "sub/file.md", true},
		{"with backslash", "sub\\file.md", true},
		{"dot", ".", true},
		{"dotdot", "..", true},
		{"no extension", "requirements", true},
		{"wrong extension", "requirements.txt", true},
		{"excalidraw-json", "diagram.excalidraw.json", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := validateRequirementFilename(tc.in)
			if tc.wantErr && err == nil {
				t.Errorf("validateRequirementFilename(%q) = nil, want error", tc.in)
			}
			if !tc.wantErr && err != nil {
				t.Errorf("validateRequirementFilename(%q) = %v, want nil", tc.in, err)
			}
		})
	}
}

func TestRequirementFilePath_Join(t *testing.T) {
	got, err := RequirementFilePath("foo.md")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := ".asdlc/requirements/foo.md"
	if got != want {
		t.Errorf("RequirementFilePath = %q, want %q", got, want)
	}
}

// Sanity-check the regex helpers don't match degenerate inputs that string
// scanning could mishandle.
func TestTagRegexes_Sanity(t *testing.T) {
	cases := []struct {
		in       string
		isReq    bool
		isDesign bool
	}{
		{"v1", true, false},
		{"v1-1", false, true},
		{"v0", false, false},      // zero rejected
		{"v1-0", false, false},    // zero rejected
		{"v1-1-1", false, false},  // extra segment rejected
		{"v 1", false, false},
		{"V1", false, false}, // case-sensitive
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			_, isReq := parseRequirementsTag(tc.in)
			_, _, isDesign := parseDesignTag(tc.in)
			if isReq != tc.isReq || isDesign != tc.isDesign {
				t.Errorf("(%q) req=%v design=%v, want req=%v design=%v",
					tc.in, isReq, isDesign, tc.isReq, tc.isDesign)
			}
		})
	}
}

// Compile-time sanity: ensure VersionInfo wire shapes round-trip via
// reflect.DeepEqual on construction (no clever defaults sneaking in).
func TestVersionInfo_ZeroValue(t *testing.T) {
	var r RequirementsVersionInfo
	if !reflect.DeepEqual(r, RequirementsVersionInfo{}) {
		t.Errorf("zero RequirementsVersionInfo not equal to itself: %+v", r)
	}
	var d DesignVersionInfo
	if !reflect.DeepEqual(d, DesignVersionInfo{}) {
		t.Errorf("zero DesignVersionInfo not equal to itself: %+v", d)
	}
}
