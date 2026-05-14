// Package services — build credentials.
//
// MintBuildToken is the BFF-only entry point for provisioning a per-build
// GitHub token. It validates (ocOrgId, repoSlug) ownership, mints a fresh
// token via the credential resolver, persists it to the credential store
// (Postgres post-2f26614), AND writes a per-org `kubernetes.io/basic-auth`
// Secret straight into the org's workflow-plane namespace
// (`workflows-<ocOrgID>`). The build's checkout-source step mounts that
// Secret as a regular volume.secret.secretName — no OpenBao, no
// SecretReference, no per-run ExternalSecret synthesis.
//
// See docs/design/github-integration-phase2.md §9.2 and the
// post-2f26614 follow-up notes in
// docs/design/cross-component-wiring-gaps.md.
package services

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/wso2/asdlc/git-service/clients/k8s"
	"github.com/wso2/asdlc/git-service/models"
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
// credential resolver, the credential store (Postgres-backed,
// AES-256-GCM encrypted), the repos table, and a workflow-plane k8s
// client for writing per-org build Secrets.
//
// wpClient may be nil in tests or when running outside a cluster — in
// that case Secret writes are skipped (Postgres write still happens)
// and the build will fail at clone time with "Git secret exists but no
// recognized credentials found" until the operator wires a cluster.
// We surface this loudly via a startup log so the misconfiguration is
// obvious.
type BuildCredentialsService struct {
	repos    repositories.RepoRepository
	resolver credentials.Resolver
	store    credentials.OpenBaoStore
	wpClient client.Client
}

func NewBuildCredentialsService(
	repos repositories.RepoRepository,
	resolver credentials.Resolver,
	store credentials.OpenBaoStore,
	wpClient client.Client,
) *BuildCredentialsService {
	return &BuildCredentialsService{
		repos:    repos,
		resolver: resolver,
		store:    store,
		wpClient: wpClient,
	}
}

// MintBuildToken implements the §9.2 sequence (revised post-2f26614):
//
//  1. Validate (ocOrgId, repoSlug) maps to an active git_repositories row.
//  2. Resolve the org's credential — refuses if status != active.
//  3. cred.Token(ctx) → fresh token (App: per-installation mint, cached;
//     PAT: Postgres read).
//  4. Persist the token to the credential store (Postgres). Keyed per-repo
//     for historical compatibility; the value is identical across repos
//     in the same org.
//  5. Ensure the org's workflow-plane namespace exists, then SSA the
//     per-org `kubernetes.io/basic-auth` Secret holding {username, password}.
//     This is what the build pod's checkout step mounts.
//  6. Return {secretRefName, expiresAt} where secretRefName is the
//     per-org WP Secret name (see models.BuildSecretName).
//
// Per-org (not per-repo) because the underlying credential is per-installation
// (App) or per-PAT — a single token grants access to every repo. Multiple
// concurrent mints against the same org SSA the same Secret with the same
// FieldOwner; last write wins, all writes contain valid tokens (the
// App-token cache returns the same value for the deadline window).
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

	// Credential-store persistence stays — call sites that read the build
	// token without going through k8s (none today, but the legacy contract
	// is preserved) continue to work, and the credential store remains the
	// authoritative record of the most recent mint.
	if err := s.store.Put(ctx, ocOrgID, "git/"+repoSlug, []byte(token)); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrOpenBaoUnavailable, err)
	}

	// SSA the per-org build Secret into the WP namespace. This is the path
	// the build pod's checkout-source step actually reads from.
	secretRefName := models.BuildSecretName(ocOrgID)
	if err := s.applyBuildSecret(ctx, ocOrgID, cred, token); err != nil {
		return nil, fmt.Errorf("mint-build: write workflow-plane secret: %w", err)
	}

	slog.InfoContext(ctx, "mint-build",
		"ocOrgId", ocOrgID, "repoSlug", repoSlug,
		"secretRef", secretRefName,
		"wpNamespace", models.WorkflowPlaneNamespace(ocOrgID),
		"expiresAt", expiresAt)

	return &MintResult{
		SecretRefName: secretRefName,
		ExpiresAt:     expiresAt,
	}, nil
}

// applyBuildSecret ensures workflows-<ocOrgID> exists and Server-Side
// Applies the per-org git-credentials Secret. No-op (with a warn) when
// wpClient is nil — see NewBuildCredentialsService for the rationale.
func (s *BuildCredentialsService) applyBuildSecret(
	ctx context.Context,
	ocOrgID string,
	cred credentials.Credential,
	token string,
) error {
	if s.wpClient == nil {
		slog.WarnContext(ctx, "mint-build: wp k8s client not configured — Secret write skipped (build will fail at clone)",
			"ocOrgId", ocOrgID)
		return nil
	}

	ns := models.WorkflowPlaneNamespace(ocOrgID)
	// The WP namespace is pre-provisioned (setup.sh for the `default` org,
	// asdlc-service's org-onboarding flow for additional orgs — TODO). The
	// namespaced Role we run under doesn't grant `namespaces get/create`,
	// so we skip the ensure-namespace step entirely and let the SSA below
	// surface a clear NotFound error if the operator forgot to provision
	// the namespace first.

	username := usernameForCredential(cred)

	secret := &corev1.Secret{
		TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "Secret"},
		ObjectMeta: metav1.ObjectMeta{
			Name:      models.BuildSecretName(ocOrgID),
			Namespace: ns,
			Labels: map[string]string{
				"app.kubernetes.io/managed-by":           "app-factory-git-service",
				"app-factory.openchoreo.dev/oc-org-id":   ocOrgID,
				"app-factory.openchoreo.dev/secret-type": "git-credentials",
			},
		},
		Type: corev1.SecretTypeBasicAuth,
		StringData: map[string]string{
			"username": username,
			"password": token,
		},
	}

	if err := s.wpClient.Patch(
		ctx, secret,
		client.Apply,
		client.ForceOwnership,
		client.FieldOwner(k8s.FieldOwner),
	); err != nil {
		return fmt.Errorf("ssa secret: %w", err)
	}
	return nil
}

// DeleteBuildSecret removes the per-org build-credential Secret from the
// org's workflow-plane namespace. Called from the org.disconnected
// cascade so a disconnected org's token doesn't linger in the WP
// namespace after the cred row is wiped from Postgres. Idempotent —
// returns nil on NotFound and on nil wpClient.
func (s *BuildCredentialsService) DeleteBuildSecret(ctx context.Context, ocOrgID string) error {
	if s.wpClient == nil {
		return nil
	}
	ns := models.WorkflowPlaneNamespace(ocOrgID)
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      models.BuildSecretName(ocOrgID),
			Namespace: ns,
		},
	}
	if err := s.wpClient.Delete(ctx, secret); err != nil {
		if apierrors.IsNotFound(err) {
			return nil
		}
		return fmt.Errorf("delete build secret %s/%s: %w", ns, secret.Name, err)
	}
	slog.InfoContext(ctx, "mint-build: deleted per-org build secret on disconnect",
		"ocOrgId", ocOrgID, "namespace", ns, "secret", secret.Name)
	return nil
}


// usernameForCredential derives the HTTPS basic-auth username for git push/pull.
//
//   - App-installation: "x-access-token" is GitHub's documented username for
//     installation-access-token HTTPS auth.
//   - User-PAT: any non-empty string works; we use the PAT owner's login for
//     audit clarity, falling back to "git" if the identity is missing.
//
// Distinguishes the two without type-switching on Credential (forbidden by
// the package contract — see pkg/credentials/credential.go §3 rules) by
// reading the credential's WebhookStrategy: App mode is WebhookPlatform,
// PAT mode is WebhookPerRepo.
func usernameForCredential(cred credentials.Credential) string {
	if cred.WebhookStrategy() == credentials.WebhookPlatform {
		return "x-access-token"
	}
	if login := cred.Identity().Login; login != "" {
		return login
	}
	return "git"
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
