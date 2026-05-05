package services

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/wso2/asdlc/git-service/models"
	"github.com/wso2/asdlc/git-service/pkg/credentials"
)

// fakeRepoRepo is a minimal in-memory RepoRepository for the mint-build tests.
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
func (f *fakeRepoRepo) Create(context.Context, *models.GitRepository) error            { return nil }
func (f *fakeRepoRepo) Update(context.Context, *models.GitRepository) error            { return nil }
func (f *fakeRepoRepo) Delete(context.Context, string) error                           { return nil }
func (f *fakeRepoRepo) DeleteAll(context.Context) error                                { return nil }

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

// fakeStore records Put calls and surfaces a fixed error.
type fakeStore struct {
	puts map[string][]byte // key = ocOrgID + ":" + key
	err  error
}

func (s *fakeStore) Get(context.Context, string, string) ([]byte, error)    { return nil, nil }
func (s *fakeStore) Delete(context.Context, string, string) error           { return nil }
func (s *fakeStore) Put(_ context.Context, ocOrgID, key string, value []byte) error {
	if s.err != nil {
		return s.err
	}
	if s.puts == nil {
		s.puts = map[string][]byte{}
	}
	s.puts[ocOrgID+":"+key] = value
	return nil
}

func TestMintBuildToken_Happy(t *testing.T) {
	secretRef := models.SecretRefNameFor("default", "asdlc-repos-myrepo")
	repo := &models.GitRepository{
		OrgID:           "default",
		ProjectID:       "p1",
		RepoSlug:        "asdlc-repos-myrepo",
		OcSecretRefName: &secretRef,
	}
	repos := &fakeRepoRepo{rows: map[string]*models.GitRepository{"default/asdlc-repos-myrepo": repo}}
	expiry := time.Now().Add(1 * time.Hour)
	res := &fakeResolver{cred: &fakeCred{token: "ghs_abc123", exp: expiry}}
	store := &fakeStore{}

	svc := NewBuildCredentialsService(repos, res, store)
	mint, err := svc.MintBuildToken(context.Background(), "default", "asdlc-repos-myrepo")
	if err != nil {
		t.Fatalf("MintBuildToken: %v", err)
	}
	if mint.SecretRefName != secretRef {
		t.Errorf("SecretRefName = %q; want %q", mint.SecretRefName, secretRef)
	}
	if !mint.ExpiresAt.Equal(expiry) {
		t.Errorf("ExpiresAt mismatch")
	}
	if string(store.puts["default:git/asdlc-repos-myrepo"]) != "ghs_abc123" {
		t.Errorf("OpenBao Put missing or wrong value: %v", store.puts)
	}
}

func TestMintBuildToken_RepoNotInOrg(t *testing.T) {
	repos := &fakeRepoRepo{rows: map[string]*models.GitRepository{}}
	svc := NewBuildCredentialsService(repos, &fakeResolver{}, &fakeStore{})
	_, err := svc.MintBuildToken(context.Background(), "default", "missing-slug")
	if !errors.Is(err, ErrRepoNotInOrg) {
		t.Errorf("got %v; want ErrRepoNotInOrg", err)
	}
}

func TestMintBuildToken_OrgDisconnected(t *testing.T) {
	secretRef := models.SecretRefNameFor("default", "slug")
	repos := &fakeRepoRepo{rows: map[string]*models.GitRepository{
		"default/slug": {OrgID: "default", RepoSlug: "slug", OcSecretRefName: &secretRef},
	}}
	res := &fakeResolver{err: &credentials.OrgNotActiveError{OcOrgID: "default", Status: "disconnected"}}
	svc := NewBuildCredentialsService(repos, res, &fakeStore{})
	_, err := svc.MintBuildToken(context.Background(), "default", "slug")
	if !errors.Is(err, ErrOrgDisconnected) {
		t.Errorf("got %v; want ErrOrgDisconnected", err)
	}
}

func TestMintBuildToken_OpenBaoUnavailable(t *testing.T) {
	secretRef := models.SecretRefNameFor("default", "slug")
	repos := &fakeRepoRepo{rows: map[string]*models.GitRepository{
		"default/slug": {OrgID: "default", RepoSlug: "slug", OcSecretRefName: &secretRef},
	}}
	res := &fakeResolver{cred: &fakeCred{token: "t", exp: time.Now().Add(time.Hour)}}
	store := &fakeStore{err: errors.New("openbao down")}
	svc := NewBuildCredentialsService(repos, res, store)
	_, err := svc.MintBuildToken(context.Background(), "default", "slug")
	if !errors.Is(err, ErrOpenBaoUnavailable) {
		t.Errorf("got %v; want ErrOpenBaoUnavailable", err)
	}
}
