package gitservice

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestInitProjectComponents_DecodesWrappedResponse asserts the BFF client
// correctly unwraps the {"projectId": "<gh-node-id>", "repo": {…}} envelope
// returned by git-service's POST /api/v1/orgs (set by the org-neutrality
// refactor). The previous flat-decode masked the bug end-to-end: missing
// OcSecretRefName / RepoSlug → no SecretReference CR → every build stuck in
// WorkflowPending for hours.
func TestInitProjectComponents_DecodesWrappedResponse(t *testing.T) {
	wantSlug := "asdlc-repos-foo123"
	wantRefName := "git-default-asdlc-repos-foo123"
	wantURL := "https://github.com/asdlc-repos/foo123.git"

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/v1/orgs" {
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"projectId": "PVT_kwDOC0xxxx", // GitHub project node ID — should NOT land in RepoInfo.ProjectID
			"repo": map[string]any{
				"orgId":           "default",
				"projectId":       "test-proj",
				"repoUrl":         wantURL,
				"clonePath":       "/repos/default/test-proj",
				"defaultBranch":   "main",
				"status":          "cloning",
				"ocSecretRefName": wantRefName,
				"repoSlug":        wantSlug,
			},
		})
	}))
	defer srv.Close()

	c := NewClient(srv.URL, nil)
	info, err := c.InitProjectComponents(context.Background(), &CreateRepoRequest{
		OrgID: "default", ProjectID: "test-proj", ProjectName: "Test",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if info == nil {
		t.Fatal("expected non-nil RepoInfo")
	}
	if info.RepoSlug != wantSlug {
		t.Errorf("RepoSlug: got %q, want %q", info.RepoSlug, wantSlug)
	}
	if info.OcSecretRefName == nil || *info.OcSecretRefName != wantRefName {
		t.Errorf("OcSecretRefName: got %v, want %q", info.OcSecretRefName, wantRefName)
	}
	if info.RepoURL != wantURL {
		t.Errorf("RepoURL: got %q, want %q", info.RepoURL, wantURL)
	}
	// Critically, RepoInfo.ProjectID must come from repo.projectId — NOT the
	// top-level projectId (which is the GitHub node ID). A flat decode here
	// is exactly how the original bug presented.
	if info.ProjectID != "test-proj" {
		t.Errorf("ProjectID: got %q, want %q (top-level GH node id must NOT bleed through)", info.ProjectID, "test-proj")
	}
}

// TestInitProjectComponents_MissingRepoFieldErrors guards against the
// reverse drift: a future server change that drops the wrapper should make
// the BFF fail loudly, not silently pass a half-populated struct.
func TestInitProjectComponents_MissingRepoFieldErrors(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = io.WriteString(w, `{"projectId":"PVT_xxxx"}`)
	}))
	defer srv.Close()

	c := NewClient(srv.URL, nil)
	_, err := c.InitProjectComponents(context.Background(), &CreateRepoRequest{
		OrgID: "default", ProjectID: "test-proj", ProjectName: "Test",
	})
	if err == nil {
		t.Fatal("expected error when response body has no 'repo' field, got nil")
	}
	if !strings.Contains(err.Error(), "repo") {
		t.Errorf("error should mention missing 'repo'; got: %v", err)
	}
}
