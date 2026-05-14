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

func TestBuildSecretNameFor(t *testing.T) {
	// The Secret name must match what the upstream dockerfile-builder
	// ClusterWorkflow templates from `${metadata.workflowRunName}-git-secret`.
	cases := []struct {
		runName, want string
	}{
		{"default-greeting-api-1731538100123", "default-greeting-api-1731538100123-git-secret"},
		{"x", "x-git-secret"},
	}
	for _, c := range cases {
		if got := BuildSecretNameFor(c.runName); got != c.want {
			t.Errorf("BuildSecretNameFor(%q) = %q; want %q", c.runName, got, c.want)
		}
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
