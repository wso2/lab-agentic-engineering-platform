package openchoreo

import (
	"context"
	"fmt"
	"net/http"

	"github.com/wso2/asdlc/asdlc-service/clients/httpx"
	"github.com/wso2/asdlc/asdlc-service/clients/oauth"
	"github.com/wso2/asdlc/asdlc-service/models"
)

// NamespaceClient defines operations for managing OpenChoreo namespaces. An
// OC namespace *is* an ASDLC organization — there is no separate Organization
// CRD. The BFF's local Organization table just side-cars a UUID per namespace.
type NamespaceClient interface {
	ListNamespaces(ctx context.Context) ([]models.OrganizationView, error)
	GetNamespace(ctx context.Context, name string) (*models.OrganizationView, error)
}

type namespaceClient struct {
	clientBase
}

func NewNamespaceClient(baseURL, hostHeader string, tokenProvider *oauth.TokenProvider) NamespaceClient {
	return &namespaceClient{
		clientBase: clientBase{
			baseURL:       baseURL,
			hostHeader:    hostHeader,
			httpClient:    &http.Client{Transport: httpx.WrapTransport(nil)},
			tokenProvider: tokenProvider,
		},
	}
}

func (c *namespaceClient) namespacesURL() string {
	return fmt.Sprintf("%s/api/v1/namespaces", c.baseURL)
}

func (c *namespaceClient) namespaceURL(name string) string {
	return fmt.Sprintf("%s/api/v1/namespaces/%s", c.baseURL, name)
}

func normalizeNamespace(n ocNamespace) models.OrganizationView {
	ann := n.Metadata.Annotations
	var displayName, description string
	if ann != nil {
		displayName = ann["openchoreo.dev/display-name"]
		description = ann["openchoreo.dev/description"]
	}
	return models.OrganizationView{
		Name:        n.Metadata.Name,
		DisplayName: displayName,
		Description: description,
		Status:      n.Status.Phase,
	}
}

func (c *namespaceClient) ListNamespaces(ctx context.Context) ([]models.OrganizationView, error) {
	req := c.newRequest(ctx, "openchoreo.ListNamespaces", http.MethodGet, c.namespacesURL())

	var raw ocNamespaceList
	if err := c.send(ctx, req, &raw, http.StatusOK); err != nil {
		return nil, fmt.Errorf("list namespaces: %w", err)
	}

	items := make([]models.OrganizationView, len(raw.Items))
	for i, n := range raw.Items {
		items[i] = normalizeNamespace(n)
	}
	return items, nil
}

// GetNamespace returns the OC namespace named `name`. OC's namespace API
// only surfaces namespaces labelled `openchoreo.dev/control-plane=true`;
// any other K8s namespace returns 404. The BFF uses this to verify the
// caller's tenant has been provisioned (by `platform-api-service` in
// hosted, or by `seed-admin-org.sh` locally).
func (c *namespaceClient) GetNamespace(ctx context.Context, name string) (*models.OrganizationView, error) {
	req := c.newRequest(ctx, "openchoreo.GetNamespace", http.MethodGet, c.namespaceURL(name))

	var raw ocNamespace
	if err := c.send(ctx, req, &raw, http.StatusOK); err != nil {
		return nil, fmt.Errorf("get namespace: %w", err)
	}
	v := normalizeNamespace(raw)
	return &v, nil
}
