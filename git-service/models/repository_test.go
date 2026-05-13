package models

import (
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

func TestSecretRefNameFor_PerOrg(t *testing.T) {
	// Post-2f26614: the build credential is per-org (single token for
	// every repo the install/PAT can see); the repoSlug parameter is
	// retained for call-site compat but intentionally unused.
	cases := []struct {
		ocOrgID string
		// repoSlug intentionally varies — the result must NOT.
		repoSlugs []string
		want      string
	}{
		{"default", []string{"asdlc-repos-myrepo", "asdlc-repos-other", "very-long-slug-xx"}, "git-default"},
		{"Acme-Co", []string{"r1"}, "git-acme-co"}, // case-normalised
	}
	for _, c := range cases {
		for _, slug := range c.repoSlugs {
			got := SecretRefNameFor(c.ocOrgID, slug)
			if got != c.want {
				t.Errorf("SecretRefNameFor(%q, %q) = %q; want %q (per-org)",
					c.ocOrgID, slug, got, c.want)
			}
		}
	}
}

func TestBuildSecretName_MatchesSecretRefNameFor(t *testing.T) {
	// SecretRefNameFor is a thin wrapper over BuildSecretName. Lock in
	// the invariant so a future drift breaks loudly.
	if got, want := SecretRefNameFor("default", "anything"), BuildSecretName("default"); got != want {
		t.Errorf("SecretRefNameFor must delegate to BuildSecretName: got %q vs %q", got, want)
	}
}

func TestWorkflowPlaneNamespace(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"default", "workflows-default"},
		{"Acme-Co", "workflows-acme-co"}, // case-normalised
		{"  trimmed  ", "workflows-trimmed"},
	}
	for _, c := range cases {
		if got := WorkflowPlaneNamespace(c.in); got != c.want {
			t.Errorf("WorkflowPlaneNamespace(%q) = %q; want %q", c.in, got, c.want)
		}
	}
}
