package services

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/wso2/asdlc/asdlc-service/models"
	"github.com/wso2/asdlc/asdlc-service/internal/credentials"
)

// fakeRepoRepo is a minimal in-memory RepoRepository for the
// stage-build-secret tests.
type fakeRepoRepo struct {
	rows map[string]*models.GitRepository // key = ocOrgID + "/" + repoSlug
}

func (f *fakeRepoRepo) GetByProjectID(ctx context.Context, projectID string) (*models.GitRepository, error) {
	return nil, nil
}
func (f *fakeRepoRepo) GetByOrgAndProjectID(ctx context.Context, ocOrgID, projectID string) (*models.GitRepository, error) {
	return nil, nil
}
func (f *fakeRepoRepo) ListAllReady(context.Context) ([]models.GitRepository, error) {
	return nil, nil
}
func (f *fakeRepoRepo) GetByOrgAndSlug(ctx context.Context, ocOrgID, repoSlug string) (*models.GitRepository, error) {
	return f.rows[ocOrgID+"/"+repoSlug], nil
}
func (f *fakeRepoRepo) Create(context.Context, *models.GitRepository) error { return nil }
func (f *fakeRepoRepo) Update(context.Context, *models.GitRepository) error { return nil }
func (f *fakeRepoRepo) Delete(context.Context, string) error                { return nil }
func (f *fakeRepoRepo) DeleteAll(context.Context) error                     { return nil }

// fakeResolver dispatches a fixed Credential or returns a fixed error.
type fakeResolver struct {
	cred credentials.Credential
	err  error
}

func (f *fakeResolver) Resolve(ctx context.Context, ocOrgID string) (credentials.Credential, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.cred, nil
}

// fakeCred returns a constant token + expiry.
type fakeCred struct {
	token string
	exp   time.Time
	err   error
}

func (c *fakeCred) Token(context.Context) (string, time.Time, error) {
	return c.token, c.exp, c.err
}
func (c *fakeCred) Identity() credentials.Identity { return credentials.Identity{} }
func (c *fakeCred) RepoOwner() string              { return "" }
func (c *fakeCred) WebhookStrategy() credentials.WebhookStrategy {
	return credentials.WebhookPerRepo
}

const testRunName = "default-greeting-api-1731538100123"

func TestStageBuildSecret_Happy(t *testing.T) {
	repo := &models.GitRepository{
		OrgID:     "default",
		ProjectID: "p1",
		RepoSlug:  "asdlc-repos-myrepo",
	}
	repos := &fakeRepoRepo{rows: map[string]*models.GitRepository{"default/asdlc-repos-myrepo": repo}}
	res := &fakeResolver{cred: &fakeCred{token: "ghs_abc123", exp: time.Now().Add(time.Hour)}}

	// wpClient=nil: SSA is skipped (with a warn) but the method should still
	// return the expected name. Production wires a real controller-runtime
	// client via NewInClusterClient.
	svc := NewBuildCredentialsService(repos, res, nil)
	got, err := svc.StageBuildSecret(context.Background(), "default", "asdlc-repos-myrepo", testRunName)
	if err != nil {
		t.Fatalf("StageBuildSecret: %v", err)
	}
	want := models.BuildSecretNameFor(testRunName)
	if got.SecretName != want {
		t.Errorf("SecretName = %q; want %q", got.SecretName, want)
	}
}

func TestStageBuildSecret_RepoNotInOrg(t *testing.T) {
	repos := &fakeRepoRepo{rows: map[string]*models.GitRepository{}}
	svc := NewBuildCredentialsService(repos, &fakeResolver{}, nil)
	_, err := svc.StageBuildSecret(context.Background(), "default", "missing-slug", testRunName)
	if !errors.Is(err, ErrRepoNotInOrg) {
		t.Errorf("got %v; want ErrRepoNotInOrg", err)
	}
}

func TestStageBuildSecret_OrgDisconnected(t *testing.T) {
	repos := &fakeRepoRepo{rows: map[string]*models.GitRepository{
		"default/slug": {OrgID: "default", RepoSlug: "slug"},
	}}
	res := &fakeResolver{err: &credentials.OrgNotActiveError{OcOrgID: "default", Status: "disconnected"}}
	svc := NewBuildCredentialsService(repos, res, nil)
	_, err := svc.StageBuildSecret(context.Background(), "default", "slug", testRunName)
	if !errors.Is(err, ErrOrgDisconnected) {
		t.Errorf("got %v; want ErrOrgDisconnected", err)
	}
}

func TestStageBuildSecret_MissingArgs(t *testing.T) {
	svc := NewBuildCredentialsService(&fakeRepoRepo{}, &fakeResolver{}, nil)
	for _, tc := range []struct{ org, slug, wrn string }{
		{"", "slug", testRunName},
		{"default", "", testRunName},
		{"default", "slug", ""},
	} {
		if _, err := svc.StageBuildSecret(context.Background(), tc.org, tc.slug, tc.wrn); err == nil {
			t.Errorf("StageBuildSecret(%q,%q,%q): expected error", tc.org, tc.slug, tc.wrn)
		}
	}
}
