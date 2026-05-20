package middleware

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/wso2/asdlc/git-service/models"
	"github.com/wso2/asdlc/git-service/repositories"
)

// fakeRepoRepo is the minimum surface RequireOrgScope needs.
type fakeRepoRepo struct {
	rows  map[string]*models.GitRepository // key = orgId+"/"+projectId
	fail  error
}

func (f *fakeRepoRepo) GetByProjectID(context.Context, string) (*models.GitRepository, error) {
	return nil, nil
}
func (f *fakeRepoRepo) GetByOrgAndProjectID(_ context.Context, orgID, projectID string) (*models.GitRepository, error) {
	if f.fail != nil {
		return nil, f.fail
	}
	return f.rows[orgID+"/"+projectID], nil
}
func (f *fakeRepoRepo) GetByOrgAndSlug(context.Context, string, string) (*models.GitRepository, error) {
	return nil, nil
}
func (f *fakeRepoRepo) ListAllReady(context.Context) ([]models.GitRepository, error) {
	return nil, nil
}
func (f *fakeRepoRepo) Create(context.Context, *models.GitRepository) error { return nil }
func (f *fakeRepoRepo) Update(context.Context, *models.GitRepository) error { return nil }
func (f *fakeRepoRepo) Delete(context.Context, string) error                { return nil }
func (f *fakeRepoRepo) DeleteAll(context.Context) error                     { return nil }

// Compile-time check that fakeRepoRepo satisfies the interface — if the
// interface ever grows a method, this fails the build instead of skipping
// tests at runtime.
var _ repositories.RepoRepository = (*fakeRepoRepo)(nil)

// newOrgScopeMux mounts the middleware on a tiny handler exposed at
// /api/v1/repos/{orgId}/{projectId}/probe so tests can drive it via real
// HTTP semantics (path-param extraction, status codes, no body leakage).
func newOrgScopeMux(repos repositories.RepoRepository, captured **models.GitRepository) http.Handler {
	mux := http.NewServeMux()
	mw := RequireOrgScope(repos)
	mux.Handle("GET /api/v1/repos/{orgId}/{projectId}/probe", mw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if captured != nil {
			*captured = ScopedRepo(r.Context())
		}
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"ok":true}`)) //nolint:errcheck
	})))
	return mux
}

func TestOrgScope_ValidPair_PassesThroughAndAttachesRepo(t *testing.T) {
	repo := &models.GitRepository{OrgID: "org-a", ProjectID: "proj-1"}
	repos := &fakeRepoRepo{rows: map[string]*models.GitRepository{"org-a/proj-1": repo}}
	var captured *models.GitRepository
	srv := httptest.NewServer(newOrgScopeMux(repos, &captured))
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/api/v1/repos/org-a/proj-1/probe")
	if err != nil {
		t.Fatalf("GET probe: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if captured == nil {
		t.Fatal("ScopedRepo() returned nil; expected the looked-up row")
	}
	if captured.OrgID != "org-a" || captured.ProjectID != "proj-1" {
		t.Errorf("ScopedRepo = %+v, want org-a/proj-1", captured)
	}
}

func TestOrgScope_WrongOrg_Returns404WithoutLeakingExistence(t *testing.T) {
	// Repo lives under org-b. Caller hits /repos/org-a/proj-1/probe.
	// Lookup with (org-a, proj-1) returns nil → 404.
	// Same response for (org-x, doesnt-exist) — the test below confirms it.
	repos := &fakeRepoRepo{rows: map[string]*models.GitRepository{
		"org-b/proj-1": {OrgID: "org-b", ProjectID: "proj-1"},
	}}
	srv := httptest.NewServer(newOrgScopeMux(repos, nil))
	defer srv.Close()

	wrongOrg, err := http.Get(srv.URL + "/api/v1/repos/org-a/proj-1/probe")
	if err != nil {
		t.Fatalf("GET wrong-org: %v", err)
	}
	wrongOrgBody := readBody(t, wrongOrg)
	wrongOrg.Body.Close()
	if wrongOrg.StatusCode != http.StatusNotFound {
		t.Errorf("wrong-org status = %d, want 404", wrongOrg.StatusCode)
	}

	notFound, err := http.Get(srv.URL + "/api/v1/repos/org-x/no-such-project/probe")
	if err != nil {
		t.Fatalf("GET not-found: %v", err)
	}
	notFoundBody := readBody(t, notFound)
	notFound.Body.Close()
	if notFound.StatusCode != http.StatusNotFound {
		t.Errorf("not-found status = %d, want 404", notFound.StatusCode)
	}

	// Existence-leak check: the two responses must be byte-identical so an
	// attacker can't tell "wrong org" from "no such project".
	if wrongOrgBody != notFoundBody {
		t.Errorf("response bodies differ between wrong-org and not-found:\n  wrong-org:  %q\n  not-found:  %q",
			wrongOrgBody, notFoundBody)
	}
}

func TestOrgScope_MalformedSlug_Returns400(t *testing.T) {
	repos := &fakeRepoRepo{}
	srv := httptest.NewServer(newOrgScopeMux(repos, nil))
	defer srv.Close()

	cases := []struct {
		name string
		path string
	}{
		// Note: leading dots and slashes break path matching itself, so we
		// pick patterns the mux *will* route to the handler — uppercase and
		// underscore — both rejected by the slug regex.
		{"uppercase orgId", "/api/v1/repos/OrgA/proj-1/probe"},
		{"underscore in projectId", "/api/v1/repos/org-a/proj_1/probe"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resp, err := http.Get(srv.URL + tc.path)
			if err != nil {
				t.Fatalf("GET: %v", err)
			}
			resp.Body.Close()
			if resp.StatusCode != http.StatusBadRequest {
				t.Errorf("status = %d, want 400", resp.StatusCode)
			}
		})
	}
}

func TestOrgScope_LookupError_Returns500(t *testing.T) {
	repos := &fakeRepoRepo{fail: errors.New("db down")}
	srv := httptest.NewServer(newOrgScopeMux(repos, nil))
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/api/v1/repos/org-a/proj-1/probe")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", resp.StatusCode)
	}
}

func readBody(t *testing.T, resp *http.Response) string {
	t.Helper()
	var sb strings.Builder
	buf := make([]byte, 4096)
	for {
		n, err := resp.Body.Read(buf)
		if n > 0 {
			sb.Write(buf[:n])
		}
		if err != nil {
			break
		}
	}
	return sb.String()
}
