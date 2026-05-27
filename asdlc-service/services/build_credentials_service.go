// Package services — build credentials.
//
// StageBuildSecret is the BFF entry point for provisioning a per-build GitHub
// credential. The BFF generates the WorkflowRun name upfront and asks
// git-service to materialise a per-WorkflowRun `kubernetes.io/basic-auth`
// Secret named `<workflowRunName>-git-secret` in the org's workflow-plane
// namespace `workflows-<ocOrgID>`. The build pod's checkout-source step then
// mounts that Secret as a regular volume.secret.secretName — same name the
// upstream `dockerfile-builder` ClusterWorkflow templates from
// `${metadata.workflowRunName}-git-secret` (line 144 of the workflow).
//
// This sidesteps the SecretReference / per-run ExternalSecret synth entirely
// because the BFF POSTs the WorkflowRun with `parameters.repository.secretRef
// == ""` — OC's externalRefs resolver explicitly skips empty refs
// (openchoreo internal/controller/workflowrun/externalref.go:41-44) and the
// workflow's `git-secret` resource has `includeWhen: secretRef != ""`.
//
// Design doc: docs/design/build-credential-injection.md.
package services

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/wso2/asdlc/asdlc-service/clients/k8s"
	"github.com/wso2/asdlc/asdlc-service/models"
	"github.com/wso2/asdlc/asdlc-service/internal/credentials"
	"github.com/wso2/asdlc/asdlc-service/repositories"
)

// StageResult is the response shape returned to the BFF. The token itself
// never crosses the boundary; only the K8s Secret name the workflow will
// mount.
type StageResult struct {
	SecretName string `json:"secretName"`
}

// Errors with stable codes the API layer maps to phase2.md §5.2 status codes:
//
//   - ErrRepoNotInOrg    → 404 (the (ocOrgId, repoSlug) tuple doesn't match
//                          an active repo — server-side ownership fence)
//   - ErrOrgDisconnected → 409 (credential row is suspended or disconnected)
//
// Transient K8s SSA / credential-store failures fall through as 500-class.
var (
	ErrRepoNotInOrg    = errors.New("stage-build-secret: repo not in org")
	ErrOrgDisconnected = errors.New("stage-build-secret: org disconnected")
)

// Labels stamped on every per-WorkflowRun build Secret. Used by
// DeleteBuildSecretsForOrg and the (future) sweep loop to find Secrets to
// clean up. The `build-secret` value is the discriminator from
// AnthropicSecretName / other things git-service writes into WP namespaces.
const (
	LabelManagedBy   = "app.kubernetes.io/managed-by"
	LabelOrgID       = "app-factory.openchoreo.dev/oc-org-id"
	LabelSecretType  = "app-factory.openchoreo.dev/secret-type"
	BuildSecretLabel = "build-credentials"
)

// BuildCredentialsService stages per-WorkflowRun build Secrets in
// workflows-<ocOrgID>. It reads the credential from the resolver (which
// reads from `org_secrets` in postgres for PAT mode), packages it as a
// kubernetes.io/basic-auth Secret, and SSAs it into the WP namespace.
//
// wpClient may be nil in tests or when running outside a cluster — in
// that case Secret writes are skipped (with a loud warning) and the build
// will fail at clone time with a clearer NotFound error than a silent
// misroute. Production startup logs the configured state.
type BuildCredentialsService struct {
	repos    repositories.RepoRepository
	resolver credentials.Resolver
	wpClient client.Client
}

func NewBuildCredentialsService(
	repos repositories.RepoRepository,
	resolver credentials.Resolver,
	wpClient client.Client,
) *BuildCredentialsService {
	return &BuildCredentialsService{
		repos:    repos,
		resolver: resolver,
		wpClient: wpClient,
	}
}

// StageBuildSecret materialises a per-WorkflowRun K8s Secret carrying the
// org's GitHub credential, named to match the upstream
// `dockerfile-builder` workflow's expected default
// (`${metadata.workflowRunName}-git-secret`).
//
// Flow:
//
//  1. Validate (ocOrgId, repoSlug) maps to an active git_repositories row
//     — server-side ownership fence, identical to the prior MintBuildToken
//     surface.
//  2. Resolve the org's credential. Refuses if status != active.
//  3. cred.Token(ctx) → fresh token (App: per-installation mint, cached;
//     PAT: postgres read via `userPATCred`).
//  4. SSA `<workflowRunName>-git-secret` into workflows-<ocOrgID>:
//     - type: kubernetes.io/basic-auth
//     - data: {username, password}
//     - labels: managed-by + oc-org-id + secret-type=build-credentials
//
// Idempotent on retry (SSA with stable FieldOwner).
func (s *BuildCredentialsService) StageBuildSecret(
	ctx context.Context, ocOrgID, repoSlug, workflowRunName string,
) (*StageResult, error) {
	if ocOrgID == "" || repoSlug == "" || workflowRunName == "" {
		return nil, fmt.Errorf("stage-build-secret: ocOrgId, repoSlug, workflowRunName are required")
	}

	repo, err := s.repos.GetByOrgAndSlug(ctx, ocOrgID, repoSlug)
	if err != nil {
		return nil, fmt.Errorf("stage-build-secret: lookup repo: %w", err)
	}
	if repo == nil {
		return nil, ErrRepoNotInOrg
	}

	cred, err := s.resolver.Resolve(ctx, ocOrgID)
	if err != nil {
		var notActive *credentials.OrgNotActiveError
		var notFound *credentials.OrgNotFoundError
		if errors.As(err, &notActive) || errors.As(err, &notFound) {
			return nil, fmt.Errorf("%w: %v", ErrOrgDisconnected, err)
		}
		return nil, fmt.Errorf("stage-build-secret: resolve credential: %w", err)
	}

	token, _, err := cred.Token(ctx)
	if err != nil {
		return nil, classifyMintErr(err)
	}

	secretName := models.BuildSecretNameFor(workflowRunName)
	if err := s.applyBuildSecret(ctx, ocOrgID, secretName, cred, token); err != nil {
		return nil, fmt.Errorf("stage-build-secret: write WP secret: %w", err)
	}

	slog.InfoContext(ctx, "stage-build-secret",
		"ocOrgId", ocOrgID, "repoSlug", repoSlug,
		"workflowRunName", workflowRunName,
		"secretName", secretName,
		"wpNamespace", models.WorkflowPlaneNamespace(ocOrgID))

	return &StageResult{SecretName: secretName}, nil
}

// applyBuildSecret SSAs the per-WorkflowRun build Secret into the WP
// namespace. No-op (with warn) when wpClient is nil.
func (s *BuildCredentialsService) applyBuildSecret(
	ctx context.Context,
	ocOrgID, secretName string,
	cred credentials.Credential,
	token string,
) error {
	if s.wpClient == nil {
		slog.WarnContext(ctx, "stage-build-secret: wp k8s client not configured — Secret write skipped (build will fail at clone)",
			"ocOrgId", ocOrgID, "secretName", secretName)
		return nil
	}

	ns := models.WorkflowPlaneNamespace(ocOrgID)
	// The WP namespace is pre-provisioned by OC's project-onboarding flow.
	// If absent, the SSA below surfaces a clear NotFound to the operator.

	secret := &corev1.Secret{
		TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "Secret"},
		ObjectMeta: metav1.ObjectMeta{
			Name:      secretName,
			Namespace: ns,
			Labels: map[string]string{
				LabelManagedBy:  k8s.FieldOwner,
				LabelOrgID:      ocOrgID,
				LabelSecretType: BuildSecretLabel,
			},
		},
		Type: corev1.SecretTypeBasicAuth,
		StringData: map[string]string{
			"username": usernameForCredential(cred),
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

// DeleteBuildSecretsForOrg removes every per-WorkflowRun build Secret in
// the org's WP namespace. Called from the org.disconnected cascade so
// staged tokens for that org don't linger after the credential row is
// wiped. Idempotent — NotFound list / delete errors are returned as nil.
//
// Selector: managed-by=<FieldOwner> + secret-type=build-credentials. The
// org-id label is implicit in the WP namespace name; we don't filter on
// it (the namespace is per-org).
func (s *BuildCredentialsService) DeleteBuildSecretsForOrg(ctx context.Context, ocOrgID string) error {
	if s.wpClient == nil {
		return nil
	}
	ns := models.WorkflowPlaneNamespace(ocOrgID)
	if err := s.wpClient.DeleteAllOf(ctx, &corev1.Secret{},
		client.InNamespace(ns),
		client.MatchingLabels{
			LabelManagedBy:  k8s.FieldOwner,
			LabelSecretType: BuildSecretLabel,
		},
	); err != nil {
		if apierrors.IsNotFound(err) {
			return nil
		}
		return fmt.Errorf("delete build secrets in %s: %w", ns, err)
	}
	slog.InfoContext(ctx, "stage-build-secret: deleted org build secrets on disconnect",
		"ocOrgId", ocOrgID, "namespace", ns)
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

// classifyMintErr maps credential-package errors onto the
// BuildCredentialsService stable error set. ErrSecretNotFound (credential
// missing from store) is treated as ErrOrgDisconnected so the BFF can mark
// the task abandoned.
func classifyMintErr(err error) error {
	if errors.Is(err, credentials.ErrSecretNotFound) {
		return fmt.Errorf("%w: %v", ErrOrgDisconnected, err)
	}
	return fmt.Errorf("stage-build-secret: token: %w", err)
}
