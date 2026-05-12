package openchoreo

import (
	"context"
	"fmt"
	"net/http"

	"github.com/wso2/asdlc/asdlc-service/clients/httpx"
	"github.com/wso2/asdlc/asdlc-service/clients/oauth"
	"github.com/wso2/asdlc/asdlc-service/models"
)

// ProjectClient defines operations for managing OpenChoreo projects.
type ProjectClient interface {
	ListProjects(ctx context.Context, orgName string, limit int, cursor string) (*models.ProjectList, error)
	GetProject(ctx context.Context, orgName, projectName string) (*models.Project, error)
	CreateProject(ctx context.Context, orgName string, req *models.CreateProjectRequest) (*models.Project, error)
	DeleteProject(ctx context.Context, orgName, projectName string) error
}

type projectClient struct {
	clientBase
}

func NewProjectClient(baseURL, hostHeader string, tokenProvider *oauth.TokenProvider, namespaceOverride string) ProjectClient {
	return &projectClient{
		clientBase: clientBase{
			baseURL:       baseURL,
			hostHeader:    hostHeader,
			httpClient:    &http.Client{Transport: httpx.WrapTransport(nil)},
			tokenProvider: tokenProvider,
			nsMap:         parseNamespaceOverride(namespaceOverride),
		},
	}
}

func (c *projectClient) projectsURL(namespace string) string {
	return fmt.Sprintf("%s/api/v1/namespaces/%s/projects", c.baseURL, namespace)
}

func (c *projectClient) projectURL(namespace, projectName string) string {
	return fmt.Sprintf("%s/api/v1/namespaces/%s/projects/%s", c.baseURL, namespace, projectName)
}

func normalizeProject(p ocProject) models.Project {
	ann := p.Metadata.Annotations
	var displayName, description string
	if ann != nil {
		displayName = ann["openchoreo.dev/display-name"]
		description = ann["openchoreo.dev/description"]
	}

	var deploymentPipeline string
	if p.Spec.DeploymentPipelineRef != nil {
		deploymentPipeline = p.Spec.DeploymentPipelineRef.Name
	}

	return models.Project{
		UID:                p.Metadata.UID,
		Name:               p.Metadata.Name,
		NamespaceName:      p.Metadata.Namespace,
		DisplayName:        displayName,
		Description:        description,
		DeploymentPipeline: deploymentPipeline,
		CreatedAt:          p.Metadata.CreationTimestamp,
		Status:             latestConditionReason(p.Status.Conditions),
	}
}

func buildCreateProjectBody(req *models.CreateProjectRequest) ocProject {
	body := ocProject{
		Metadata: ocObjectMeta{
			Name: req.Name,
		},
	}
	if req.DisplayName != "" || req.Description != "" {
		body.Metadata.Annotations = map[string]string{}
		if req.DisplayName != "" {
			body.Metadata.Annotations["openchoreo.dev/display-name"] = req.DisplayName
		}
		if req.Description != "" {
			body.Metadata.Annotations["openchoreo.dev/description"] = req.Description
		}
	}
	if req.DeploymentPipeline != "" {
		body.Spec.DeploymentPipelineRef = &ocRef{Name: req.DeploymentPipeline}
	}
	return body
}

func (c *projectClient) ListProjects(ctx context.Context, orgName string, limit int, cursor string) (*models.ProjectList, error) {
	ns := c.resolveNamespace(orgName)
	req := c.newRequest(ctx, "openchoreo.ListProjects", http.MethodGet, c.projectsURL(ns))
	if limit > 0 {
		req.SetQuery("limit", fmt.Sprintf("%d", limit))
	}
	if cursor != "" {
		req.SetQuery("cursor", cursor)
	}

	var raw ocProjectList
	if err := c.send(ctx, req, &raw, http.StatusOK); err != nil {
		return nil, fmt.Errorf("list projects: %w", err)
	}

	items := make([]models.Project, len(raw.Items))
	for i, p := range raw.Items {
		items[i] = normalizeProject(p)
	}
	return &models.ProjectList{Items: items}, nil
}

func (c *projectClient) GetProject(ctx context.Context, orgName, projectName string) (*models.Project, error) {
	ns := c.resolveNamespace(orgName)
	req := c.newRequest(ctx, "openchoreo.GetProject", http.MethodGet, c.projectURL(ns, projectName))

	var raw ocProject
	if err := c.send(ctx, req, &raw, http.StatusOK); err != nil {
		return nil, fmt.Errorf("get project: %w", err)
	}
	p := normalizeProject(raw)
	return &p, nil
}

func (c *projectClient) CreateProject(ctx context.Context, orgName string, body *models.CreateProjectRequest) (*models.Project, error) {
	ns := c.resolveNamespace(orgName)
	req := c.newRequest(ctx, "openchoreo.CreateProject", http.MethodPost, c.projectsURL(ns))
	req.SetJSON(buildCreateProjectBody(body))

	var raw ocProject
	if err := c.send(ctx, req, &raw, http.StatusCreated); err != nil {
		return nil, fmt.Errorf("create project: %w", err)
	}
	p := normalizeProject(raw)
	return &p, nil
}

func (c *projectClient) DeleteProject(ctx context.Context, orgName, projectName string) error {
	ns := c.resolveNamespace(orgName)
	req := c.newRequest(ctx, "openchoreo.DeleteProject", http.MethodDelete, c.projectURL(ns, projectName))

	if err := c.send(ctx, req, nil, http.StatusNoContent); err != nil {
		return fmt.Errorf("delete project: %w", err)
	}
	return nil
}
