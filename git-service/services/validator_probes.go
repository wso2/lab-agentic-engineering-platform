package services

import (
	"context"
	"errors"

	"github.com/wso2/asdlc/git-service/pkg/credentials"
)

// ValidatorProbes is the production credentials.ValidatorProbes that
// wraps the resolver, the GitHub client, the AppTokenMinter, and the
// CredentialService's identity-update helpers. Phase 2 PR D §6.10.
//
// Construction lives in cmd/git-service/main.go; the validator is wired
// after the credential service + github client + minter are all up.
type ValidatorProbes struct {
	credSvc      *CredentialService
	githubClient GitHubClient
	resolver     credentials.Resolver
	minter       *credentials.AppTokenMinter
}

// NewValidatorProbes constructs the probes adapter. All four
// dependencies must be non-nil; nil short-circuits the validator at
// construction so we don't half-fire later.
func NewValidatorProbes(credSvc *CredentialService, githubClient GitHubClient, resolver credentials.Resolver, minter *credentials.AppTokenMinter) *ValidatorProbes {
	return &ValidatorProbes{
		credSvc:      credSvc,
		githubClient: githubClient,
		resolver:     resolver,
		minter:       minter,
	}
}

// ListActiveRows projects org_credentials rows into the validator's
// schema-free shape.
func (p *ValidatorProbes) ListActiveRows(ctx context.Context) ([]credentials.ActiveRow, error) {
	rows, err := p.credSvc.ListActiveRows(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]credentials.ActiveRow, 0, len(rows))
	for i := range rows {
		r := rows[i]
		out = append(out, credentials.ActiveRow{
			OcOrgID:        r.OcOrgID,
			Kind:           r.Kind,
			GitHubLogin:    r.GitHubLogin,
			IdentityLogin:  r.IdentityLogin,
			InstallationID: r.InstallationID,
			Status:         r.Status,
		})
	}
	return out, nil
}

// ProbePAT performs GET /user using the row's resolved credential.
// Translates GitHub's HTTP status into the validator's signal vocabulary
// (Unauthorized triggers cascade; Transient defers to the next tick).
func (p *ValidatorProbes) ProbePAT(ctx context.Context, row credentials.ActiveRow) (login, name, email string, err error) {
	cred, err := p.resolver.Resolve(ctx, row.OcOrgID)
	if err != nil {
		return "", "", "", err
	}
	user, err := p.githubClient.GetUser(ctx, cred)
	if err != nil {
		switch {
		case IsHTTPStatus(err, 401), IsHTTPStatus(err, 403):
			return "", "", "", credentials.ErrCredentialUnauthorized
		case IsHTTPStatus(err, 404):
			return "", "", "", credentials.ErrCredentialUnauthorized
		}
		return "", "", "", credentials.ErrCredentialTransient
	}
	if user.Email == "" {
		user.Email = user.Login + "@users.noreply.github.com"
	}
	if user.Name == "" {
		user.Name = user.Login
	}
	return user.Login, user.Name, user.Email, nil
}

// ProbeApp calls GET /app/installations/{installationId}. Only valid for
// app-installation rows; PAT rows would carry InstallationID=nil and we
// skip them at the caller layer.
func (p *ValidatorProbes) ProbeApp(ctx context.Context, row credentials.ActiveRow) (string, error) {
	if row.InstallationID == nil {
		return "", errors.New("validator: app row missing installation_id")
	}
	info, err := p.githubClient.GetAppInstallation(ctx, p.minter, *row.InstallationID)
	if err != nil {
		switch {
		case IsHTTPStatus(err, 401), IsHTTPStatus(err, 404), IsHTTPStatus(err, 410):
			return "", credentials.ErrCredentialUnauthorized
		}
		return "", credentials.ErrCredentialTransient
	}
	return info.Account.Login, nil
}

// RecordIdentityFromGitHub delegates to the credential service so the
// drift columns are written under the row's database connection.
func (p *ValidatorProbes) RecordIdentityFromGitHub(ctx context.Context, ocOrgID, login, name, email string) (bool, error) {
	return p.credSvc.RecordIdentityFromGitHub(ctx, ocOrgID, login, name, email)
}

func (p *ValidatorProbes) UpdateGitHubLogin(ctx context.Context, ocOrgID, login string) error {
	return p.credSvc.UpdateGitHubLogin(ctx, ocOrgID, login)
}

func (p *ValidatorProbes) TouchValidatedAt(ctx context.Context, ocOrgID string) error {
	return p.credSvc.TouchValidatedAt(ctx, ocOrgID)
}
