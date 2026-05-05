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
	CreateNamespace(ctx context.Context, name, displayName, description string) (*models.OrganizationView, error)
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

func buildCreateNamespaceBody(name, displayName, description string) ocNamespace {
	body := ocNamespace{
		Metadata: ocObjectMeta{
			Name: name,
		},
	}
	if displayName != "" || description != "" {
		body.Metadata.Annotations = map[string]string{}
		if displayName != "" {
			body.Metadata.Annotations["openchoreo.dev/display-name"] = displayName
		}
		if description != "" {
			body.Metadata.Annotations["openchoreo.dev/description"] = description
		}
	}
	return body
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

func (c *namespaceClient) CreateNamespace(ctx context.Context, name, displayName, description string) (*models.OrganizationView, error) {
	req := c.newRequest(ctx, "openchoreo.CreateNamespace", http.MethodPost, c.namespacesURL())
	req.SetJSON(buildCreateNamespaceBody(name, displayName, description))

	var raw ocNamespace
	if err := c.send(ctx, req, &raw, http.StatusCreated); err != nil {
		return nil, fmt.Errorf("create namespace: %w", err)
	}
	v := normalizeNamespace(raw)
	return &v, nil
}
