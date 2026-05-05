// Package services — build credentials.
//
// MintBuildToken is the BFF-only entry point for provisioning a per-build
// GitHub token. It validates (ocOrgId, repoSlug) ownership, mints a fresh
// token via the credential resolver, writes the token to OpenBao at
// `secret/asdlc/{ocOrgId}/git/{repoSlug}`, and returns only the SecretReference
// name + expiry. The BFF never sees the token — the build pod resolves the
// SecretReference CR through external-secrets at pod start.
//
// See docs/design/github-integration-phase2.md §9.2.
package services

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/wso2/asdlc/git-service/pkg/credentials"
	"github.com/wso2/asdlc/git-service/repositories"
)

// MintResult is the response shape returned to the BFF — secretRef name +
// the token's expiry. The token itself never crosses the boundary.
type MintResult struct {
	SecretRefName string    `json:"secretRefName"`
	ExpiresAt     time.Time `json:"expiresAt"`
}

// Errors with stable codes the API layer maps to phase2.md §5.2 status codes:
//
//   - ErrRepoNotInOrg       → 404 (the (ocOrgId, repoSlug) tuple doesn't match
//                            an active repo — server-side ownership fence)
//   - ErrOrgDisconnected    → 409 (credential row is suspended or disconnected)
//   - ErrOpenBaoUnavailable → 503 (OpenBao Put failed transiently)
//
// 429 (App rate-limit) is deferred to PR D's build-watcher auth-failure
// classifier; transient mint failures fall through as 500-class.
var (
	ErrRepoNotInOrg       = errors.New("mint-build: repo not in org")
	ErrOrgDisconnected    = errors.New("mint-build: org disconnected")
	ErrOpenBaoUnavailable = errors.New("mint-build: openbao unavailable")
)

// BuildCredentialsService orchestrates per-build token minting. Uses the
// credential resolver, the OpenBaoStore wrapper, and the repos table.
type BuildCredentialsService struct {
	repos    repositories.RepoRepository
	resolver credentials.Resolver
	store    credentials.OpenBaoStore
}

func NewBuildCredentialsService(
	repos repositories.RepoRepository,
	resolver credentials.Resolver,
	store credentials.OpenBaoStore,
) *BuildCredentialsService {
	return &BuildCredentialsService{
		repos:    repos,
		resolver: resolver,
		store:    store,
	}
}

// MintBuildToken implements the §9.2 sequence:
//
//  1. Validate (ocOrgId, repoSlug) maps to an active git_repositories row.
//  2. Resolve the org's credential — refuses if status != active.
//  3. cred.Token(ctx) → fresh token (App: per-installation mint, cached;
//     PAT: OpenBao read).
//  4. Write the token to OpenBao at secret/asdlc/{ocOrgId}/git/{repoSlug}.
//  5. Return {secretRefName, expiresAt} only.
//
// The OpenBao path is overwritten on every mint; concurrent mints against
// the same repo are safe because tokens are interchangeable within their
// TTL (phase2.md §9.4). The App-token cache + singleflight collapse the
// upstream mints; the OpenBao writes themselves are individual but cheap.
func (s *BuildCredentialsService) MintBuildToken(ctx context.Context, ocOrgID, repoSlug string) (*MintResult, error) {
	if ocOrgID == "" || repoSlug == "" {
		return nil, fmt.Errorf("mint-build: ocOrgId and repoSlug are required")
	}

	repo, err := s.repos.GetByOrgAndSlug(ctx, ocOrgID, repoSlug)
	if err != nil {
		return nil, fmt.Errorf("mint-build: lookup repo: %w", err)
	}
	if repo == nil {
		return nil, ErrRepoNotInOrg
	}
	if repo.OcSecretRefName == nil || *repo.OcSecretRefName == "" {
		// Defensive: repo predates PR C and the column wasn't backfilled.
		// Caller's lazy-backfill path should have populated this; if it
		// hasn't, fail closed rather than minting against an unknown CR.
		return nil, fmt.Errorf("mint-build: repo %s/%s has no secret-ref name", ocOrgID, repoSlug)
	}

	cred, err := s.resolver.Resolve(ctx, ocOrgID)
	if err != nil {
		// Resolver returns OrgNotActiveError (suspended/disconnected) and
		// OrgNotFoundError (no row). The mint-build endpoint pre-validates
		// (ocOrgId, repoSlug) against git_repositories, so a missing org
		// row at this point means the credential was disconnected after
		// the repo was provisioned — surface as ErrOrgDisconnected (409).
		var notActive *credentials.OrgNotActiveError
		var notFound *credentials.OrgNotFoundError
		if errors.As(err, &notActive) || errors.As(err, &notFound) {
			return nil, fmt.Errorf("%w: %v", ErrOrgDisconnected, err)
		}
		return nil, fmt.Errorf("mint-build: resolve credential: %w", err)
	}

	token, expiresAt, err := cred.Token(ctx)
	if err != nil {
		return nil, classifyMintErr(err)
	}

	if err := s.store.Put(ctx, ocOrgID, "git/"+repoSlug, []byte(token)); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrOpenBaoUnavailable, err)
	}

	slog.InfoContext(ctx, "mint-build",
		"ocOrgId", ocOrgID, "repoSlug", repoSlug, "secretRef", *repo.OcSecretRefName,
		"expiresAt", expiresAt)

	return &MintResult{
		SecretRefName: *repo.OcSecretRefName,
		ExpiresAt:     expiresAt,
	}, nil
}

// classifyMintErr maps credential-package errors onto the BuildCredentialsService
// stable error set. OpenBao read failures (PAT mode after disconnect-GC) are
// treated as ErrOrgDisconnected so the BFF can mark the task abandoned.
//
// App rate-limit detection is deferred to PR D (the build-watcher
// auth-failure classifier). For now, all other token errors fall through
// as 500-class transients.
func classifyMintErr(err error) error {
	if errors.Is(err, credentials.ErrSecretNotFound) {
		return fmt.Errorf("%w: %v", ErrOrgDisconnected, err)
	}
	return fmt.Errorf("mint-build: token: %w", err)
}
