package openchoreo

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	"github.com/wso2/asdlc/asdlc-service/clients/openchoreo/gen"
)

//go:generate go run github.com/matryer/moq@v0.7.1 -rm -fmt goimports -pkg mocks -out mocks/secretref_client_mock.go . SecretRefClient

// SecretRefClient creates the OC SecretReference CR that the build pod
// resolves through external-secrets to fetch the per-repo build token from
// OpenBao. Phase 2 PR C — replaces the legacy `CreateGitSecret` (an OC
// `GitSecret`) with the SecretReference + OpenBao + ExternalSecret chain.
//
// The CR is namespaced (`{ocOrgId}`) and idempotent on `(namespace, name)`.
// Re-creating an existing CR returns 409 which we treat as success.
type SecretRefClient interface {
	// EnsureSecretReference creates a SecretReference CR. Idempotent on
	// (namespace, name): a 409 from OC's API is treated as success — the
	// pre-existing CR satisfies the caller's "make sure this exists" contract.
	//
	// vaultPath is the OpenBao KV v2 logical path (e.g.
	// "secret/asdlc/{ocOrgId}/git/{repoSlug}"); ExternalSecret resolves it
	// at pod start and the build pod reads the resulting K8s Secret as a
	// `kubernetes.io/basic-auth` (username=x-access-token, password=<token>).
	EnsureSecretReference(ctx context.Context, namespace, name, vaultPath string) error
}

type secretRefClient struct {
	oc *gen.ClientWithResponses
}

func NewSecretRefClient(cfg Config) SecretRefClient {
	oc, err := newGenClient(cfg)
	if err != nil {
		panic(fmt.Errorf("init openchoreo secretref client: %w", err))
	}
	return &secretRefClient{oc: oc}
}

func (c *secretRefClient) EnsureSecretReference(ctx context.Context, namespace, name, vaultPath string) error {
	resp, err := c.oc.CreateSecretReferenceWithResponse(ctx, namespace, buildEnsureSecretReferenceBody(namespace, name, vaultPath))
	if err != nil {
		return fmt.Errorf("failed to create secret reference: %w", err)
	}

	switch resp.StatusCode() {
	case http.StatusCreated, http.StatusOK:
		return nil
	case http.StatusConflict:
		// Idempotent: pre-existing CR with the same (namespace, name) is fine.
		return nil
	}

	// Defensive: if the gen typed body lookup misses (unexpected status from
	// a flaky gateway, etc.) handleErrorResponse still classifies by raw
	// status — and ErrConflict still folds into success in case OC's API
	// surfaces a conflict outside the typed JSON409 path.
	classified := handleErrorResponse(resp.StatusCode(), ErrorResponses{
		JSON400: resp.JSON400,
		JSON401: resp.JSON401,
		JSON403: resp.JSON403,
		JSON409: resp.JSON409,
		JSON500: resp.JSON500,
	})
	if errors.Is(classified, ErrConflict) {
		return nil
	}
	return classified
}

// buildEnsureSecretReferenceBody returns the SecretReference body we POST.
// Mirrors the live CRD schema — `spec.template` accepts only `type` and
// `metadata`, NOT `data`, so the data shape is constrained: one entry per
// Secret key, each pulled from OpenBao directly. For git-credentials the
// build pipeline only needs the `password` field (the GitHub token); HTTPS
// auth ignores the username for token-based bearer use.
func buildEnsureSecretReferenceBody(namespace, name, vaultPath string) gen.CreateSecretReferenceJSONRequestBody {
	property := "value"
	refreshInterval := "30s"
	tplType := gen.KubernetesIobasicAuth
	labels := map[string]string{
		string(LabelKeyManagedBy):         LabelValueManagedBy,
		string(LabelKeySecretType):        LabelValueSecretTypeBasicAuth,
		string(LabelKeyOCSecretType):      LabelValueSecretTypeGitCreds,
		string(LabelKeyWorkflowPlaneKind): LabelValueClusterWorkflowKind,
		string(LabelKeyWorkflowPlaneName): LabelValueWorkflowPlaneName,
	}
	return gen.CreateSecretReferenceJSONRequestBody{
		Metadata: gen.ObjectMeta{
			Name:      name,
			Namespace: &namespace,
			Labels:    &labels,
		},
		Spec: &gen.SecretReferenceSpec{
			RefreshInterval: &refreshInterval,
			Data: []gen.SecretDataSource{
				{
					SecretKey: "password",
					RemoteRef: gen.RemoteReference{
						Key:      vaultPath,
						Property: &property,
					},
				},
			},
			Template: gen.SecretTemplate{
				Type: &tplType,
			},
		},
	}
}
