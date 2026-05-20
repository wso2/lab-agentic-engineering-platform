package openchoreo

import (
	"context"
	"fmt"
	"net/http"

	"github.com/wso2/asdlc/asdlc-service/clients/openchoreo/gen"
	"github.com/wso2/asdlc/asdlc-service/models"
)

//go:generate go run github.com/matryer/moq@v0.7.1 -rm -fmt goimports -pkg mocks -out mocks/namespace_client_mock.go . NamespaceClient

// NamespaceClient defines operations for managing OpenChoreo namespaces. An
// OC namespace *is* an ASDLC organization — there is no separate Organization
// CRD. The BFF's local Organization table just side-cars a UUID per namespace.
type NamespaceClient interface {
	ListNamespaces(ctx context.Context) ([]models.OrganizationView, error)
	GetNamespace(ctx context.Context, name string) (*models.OrganizationView, error)
}

type namespaceClient struct {
	oc *gen.ClientWithResponses
}

func NewNamespaceClient(cfg Config) NamespaceClient {
	oc, err := newGenClient(cfg)
	if err != nil {
		panic(fmt.Errorf("init openchoreo namespace client: %w", err))
	}
	return &namespaceClient{oc: oc}
}

func (c *namespaceClient) ListNamespaces(ctx context.Context) ([]models.OrganizationView, error) {
	resp, err := c.oc.ListNamespacesWithResponse(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to list namespaces: %w", err)
	}
	if resp.StatusCode() != http.StatusOK || resp.JSON200 == nil {
		return nil, handleErrorResponse(resp.StatusCode(), ErrorResponses{
			JSON400: resp.JSON400,
			JSON401: resp.JSON401,
			JSON403: resp.JSON403,
			JSON500: resp.JSON500,
		})
	}
	items := make([]models.OrganizationView, len(resp.JSON200.Items))
	for i, n := range resp.JSON200.Items {
		items[i] = namespaceToView(n)
	}
	return items, nil
}

// GetNamespace returns the OC namespace named `name`. OC's namespace API
// only surfaces namespaces labelled `openchoreo.dev/control-plane=true`;
// any other K8s namespace returns 404. The BFF uses this to verify the
// caller's tenant has been provisioned (by `platform-api-service` in
// hosted, or by `seed-admin-org.sh` locally).
func (c *namespaceClient) GetNamespace(ctx context.Context, name string) (*models.OrganizationView, error) {
	resp, err := c.oc.GetNamespaceWithResponse(ctx, name)
	if err != nil {
		return nil, fmt.Errorf("failed to get namespace: %w", err)
	}
	if resp.StatusCode() != http.StatusOK || resp.JSON200 == nil {
		return nil, handleErrorResponse(resp.StatusCode(), ErrorResponses{
			JSON401: resp.JSON401,
			JSON403: resp.JSON403,
			JSON404: resp.JSON404,
			JSON500: resp.JSON500,
		})
	}
	v := namespaceToView(*resp.JSON200)
	return &v, nil
}

func namespaceToView(n gen.Namespace) models.OrganizationView {
	var phase string
	if n.Status != nil && n.Status.Phase != nil {
		phase = string(*n.Status.Phase)
	}
	return models.OrganizationView{
		Name:        n.Metadata.Name,
		DisplayName: annotation(n.Metadata.Annotations, AnnotationKeyDisplayName),
		Description: annotation(n.Metadata.Annotations, AnnotationKeyDescription),
		Status:      phase,
	}
}
