package models

import (
	"strings"
	"testing"
)

func TestSlugForURL(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"https://github.com/asdlc-repos/phase2-prc-app-test037.git", "asdlc-repos-phase2-prc-app-test037"},
		{"https://github.com/asdlc-repos/phase2-prc-app-test037", "asdlc-repos-phase2-prc-app-test037"},
		{"https://github.com/Owner/MixedCaseRepo", "owner-mixedcaserepo"},
		{"https://github.com/asdlc-repos/repo.git/", "asdlc-repos-repo"},
		// Non-GitHub URL — empty
		{"https://gitlab.com/asdlc/repo.git", ""},
	}
	for _, c := range cases {
		got := SlugForURL(c.in)
		if got != c.want {
			t.Errorf("SlugForURL(%q) = %q; want %q", c.in, got, c.want)
		}
	}
}

func TestSecretRefNameFor(t *testing.T) {
	// Short — straight join.
	if got := SecretRefNameFor("default", "asdlc-repos-myrepo"); got != "git-default-asdlc-repos-myrepo" {
		t.Errorf("short: got %q", got)
	}

	// Over 63 chars — must trim with hash suffix and end in hex.
	longSlug := strings.Repeat("a", 80)
	got := SecretRefNameFor("default", longSlug)
	if len(got) > 63 {
		t.Errorf("long: name length %d > 63; got %q", len(got), got)
	}
	if !strings.HasPrefix(got, "git-default-") {
		t.Errorf("long: missing prefix; got %q", got)
	}
	// Last 8 chars are hex.
	suffix := got[len(got)-8:]
	for _, r := range suffix {
		if !(r >= '0' && r <= '9' || r >= 'a' && r <= 'f') {
			t.Errorf("long: non-hex suffix %q; got %q", suffix, got)
			break
		}
	}
}
