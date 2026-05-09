package openchoreo

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	"github.com/wso2/asdlc/asdlc-service/clients/httpx"
	"github.com/wso2/asdlc/asdlc-service/clients/oauth"
	"github.com/wso2/asdlc/asdlc-service/clients/requests"
)

// SecretRefClient creates the OC SecretReference CR that the build pod
// resolves through external-secrets to fetch the per-repo build token from
// OpenBao. Phase 2 PR C — replaces the legacy `CreateGitSecret` (an OC
// `GitSecret`) with the SecretReference + OpenBao + ExternalSecret chain.
//
// The CR is namespaced (`{ocOrgId}`) and idempotent on `(namespace, name)`.
// Re-creating an existing CR returns 409 which we treat as success.
type SecretRefClient interface {
	// EnsureSecretReference creates a SecretReference CR. Idempotent on
	// (namespace, name): a 409 from OC's API is treated as success.
	//
	// vaultPath is the OpenBao KV v2 logical path (e.g.
	// "secret/asdlc/{ocOrgId}/git/{repoSlug}"); ExternalSecret resolves it
	// at pod start and the build pod reads the resulting K8s Secret as a
	// `kubernetes.io/basic-auth` (username=x-access-token, password=<token>).
	EnsureSecretReference(ctx context.Context, namespace, name, vaultPath string) error
}

type secretRefClient struct {
	clientBase
}

// NewSecretRefClient builds the client. Uses the same OC API base URL,
// host header, and service-token provider as the component client.
func NewSecretRefClient(baseURL, hostHeader string, tokenProvider *oauth.TokenProvider) SecretRefClient {
	return &secretRefClient{
		clientBase: clientBase{
			baseURL:       baseURL,
			hostHeader:    hostHeader,
			httpClient:    &http.Client{Transport: httpx.WrapTransport(nil)},
			tokenProvider: tokenProvider,
		},
	}
}

func (c *secretRefClient) secretReferencesURL(namespace string) string {
	return fmt.Sprintf("%s/api/v1/namespaces/%s/secretreferences", c.baseURL, namespace)
}

// secretReference is the K8s-shaped CR body. Mirrors the live CRD schema —
// `spec.template` accepts only `type` and `metadata`, NOT `data`, so the
// data shape is constrained: one entry per Secret key, each pulled from
// OpenBao directly. For git-credentials the build pipeline only needs the
// `password` field (the GitHub token); HTTPS auth ignores the username
// for token-based bearer use.
//
//   spec:
//     refreshInterval: 30s
//     data:
//       - secretKey: password
//         remoteRef: { key: secret/data/asdlc/{ocOrgId}/git/{repoSlug}, property: value }
//     template:
//       type: kubernetes.io/basic-auth
//
// Mirrors the legacy GitSecret-derived shape (see existing
// phase2-prb-app-test-git-pat for reference). Compatible with the
// dockerfile-builder workflow's git-clone step.
type secretReference struct {
	APIVersion string              `json:"apiVersion"`
	Kind       string              `json:"kind"`
	Metadata   secretReferenceMeta `json:"metadata"`
	Spec       secretReferenceSpec `json:"spec"`
}

type secretReferenceMeta struct {
	Name      string            `json:"name"`
	Namespace string            `json:"namespace"`
	Labels    map[string]string `json:"labels,omitempty"`
}

type secretReferenceSpec struct {
	RefreshInterval string               `json:"refreshInterval"`
	Data            []secretRefDataEntry `json:"data"`
	Template        secretRefTemplate    `json:"template"`
}

type secretRefDataEntry struct {
	SecretKey string             `json:"secretKey"`
	RemoteRef secretRefRemoteRef `json:"remoteRef"`
}

type secretRefRemoteRef struct {
	Key      string `json:"key"`
	Property string `json:"property,omitempty"`
}

type secretRefTemplate struct {
	Type     string            `json:"type"`
	Metadata *secretRefTplMeta `json:"metadata,omitempty"`
}

type secretRefTplMeta struct {
	Labels map[string]string `json:"labels,omitempty"`
}

func (c *secretRefClient) EnsureSecretReference(ctx context.Context, namespace, name, vaultPath string) error {
	ns := namespace
	body := secretReference{
		APIVersion: "openchoreo.dev/v1alpha1",
		Kind:       "SecretReference",
		Metadata: secretReferenceMeta{
			Name:      name,
			Namespace: ns,
			Labels: map[string]string{
				"managed-by":                       "asdlc",
				"kubernetes.io/secret-type":        "basic-auth",
				"openchoreo.dev/secret-type":       "git-credentials",
				"openchoreo.dev/workflow-plane-kind": "ClusterWorkflowPlane",
				"openchoreo.dev/workflow-plane-name": "default",
			},
		},
		Spec: secretReferenceSpec{
			RefreshInterval: "30s",
			Data: []secretRefDataEntry{
				{
					SecretKey: "password",
					RemoteRef: secretRefRemoteRef{
						Key:      vaultPath,
						Property: "value",
					},
				},
			},
			Template: secretRefTemplate{
				Type: "kubernetes.io/basic-auth",
			},
		},
	}

	httpReq := c.newRequest(ctx, "openchoreo.EnsureSecretReference", http.MethodPost, c.secretReferencesURL(ns))
	httpReq.SetJSON(body)

	if err := c.send(ctx, httpReq, nil, http.StatusCreated); err != nil {
		var httpErr *requests.HttpError
		if errors.As(err, &httpErr) && httpErr.StatusCode == http.StatusConflict {
			return nil
		}
		return fmt.Errorf("ensure secretreference %s/%s: %w", ns, name, err)
	}
	return nil
}
