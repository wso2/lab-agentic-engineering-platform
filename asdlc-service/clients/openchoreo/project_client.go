package openchoreo

import (
	"context"
	"fmt"
	"net/http"

	"github.com/wso2/asdlc/asdlc-service/clients/openchoreo/gen"
	"github.com/wso2/asdlc/asdlc-service/models"
)

//go:generate go run github.com/matryer/moq@v0.7.1 -rm -fmt goimports -pkg mocks -out mocks/project_client_mock.go . ProjectClient

// ProjectClient defines operations for managing OpenChoreo projects.
type ProjectClient interface {
	ListProjects(ctx context.Context, orgName string, limit int, cursor string) (*models.ProjectList, error)
	GetProject(ctx context.Context, orgName, projectName string) (*models.Project, error)
	CreateProject(ctx context.Context, orgName string, req *models.CreateProjectRequest) (*models.Project, error)
	DeleteProject(ctx context.Context, orgName, projectName string) error
}

type projectClient struct {
	oc *gen.ClientWithResponses
}

func NewProjectClient(cfg Config) ProjectClient {
	oc, err := newGenClient(cfg)
	if err != nil {
		panic(fmt.Errorf("init openchoreo project client: %w", err))
	}
	return &projectClient{oc: oc}
}

func (c *projectClient) ListProjects(ctx context.Context, orgName string, limit int, cursor string) (*models.ProjectList, error) {
	params := &gen.ListProjectsParams{}
	if limit > 0 {
		l := gen.LimitParam(limit)
		params.Limit = &l
	}
	if cursor != "" {
		cur := gen.CursorParam(cursor)
		params.Cursor = &cur
	}
	resp, err := c.oc.ListProjectsWithResponse(ctx, orgName, params)
	if err != nil {
		return nil, fmt.Errorf("failed to list projects: %w", err)
	}
	if resp.StatusCode() != http.StatusOK || resp.JSON200 == nil {
		return nil, handleErrorResponse(resp.StatusCode(), ErrorResponses{
			JSON400: resp.JSON400,
			JSON401: resp.JSON401,
			JSON403: resp.JSON403,
			JSON404: resp.JSON404,
			JSON500: resp.JSON500,
		})
	}
	items := make([]models.Project, len(resp.JSON200.Items))
	for i, p := range resp.JSON200.Items {
		items[i] = projectToModel(p)
	}
	return &models.ProjectList{Items: items}, nil
}

func (c *projectClient) GetProject(ctx context.Context, orgName, projectName string) (*models.Project, error) {
	resp, err := c.oc.GetProjectWithResponse(ctx, orgName, projectName)
	if err != nil {
		return nil, fmt.Errorf("failed to get project: %w", err)
	}
	if resp.StatusCode() != http.StatusOK || resp.JSON200 == nil {
		return nil, handleErrorResponse(resp.StatusCode(), ErrorResponses{
			JSON401: resp.JSON401,
			JSON403: resp.JSON403,
			JSON404: resp.JSON404,
			JSON500: resp.JSON500,
		})
	}
	p := projectToModel(*resp.JSON200)
	return &p, nil
}

func (c *projectClient) CreateProject(ctx context.Context, orgName string, body *models.CreateProjectRequest) (*models.Project, error) {
	resp, err := c.oc.CreateProjectWithResponse(ctx, orgName, buildCreateProjectBody(body))
	if err != nil {
		return nil, fmt.Errorf("failed to create project: %w", err)
	}
	// OC's POST returns 201 on success; tolerate 200 in case a future build flips to it.
	if (resp.StatusCode() != http.StatusCreated && resp.StatusCode() != http.StatusOK) || resp.JSON201 == nil {
		return nil, handleErrorResponse(resp.StatusCode(), ErrorResponses{
			JSON400: resp.JSON400,
			JSON401: resp.JSON401,
			JSON403: resp.JSON403,
			JSON409: resp.JSON409,
			JSON500: resp.JSON500,
		})
	}
	p := projectToModel(*resp.JSON201)
	return &p, nil
}

func (c *projectClient) DeleteProject(ctx context.Context, orgName, projectName string) error {
	resp, err := c.oc.DeleteProjectWithResponse(ctx, orgName, projectName)
	if err != nil {
		return fmt.Errorf("failed to delete project: %w", err)
	}
	if resp.StatusCode() != http.StatusNoContent && resp.StatusCode() != http.StatusOK {
		return handleErrorResponse(resp.StatusCode(), ErrorResponses{
			JSON401: resp.JSON401,
			JSON403: resp.JSON403,
			JSON404: resp.JSON404,
			JSON500: resp.JSON500,
		})
	}
	return nil
}

func projectToModel(p gen.Project) models.Project {
	var deploymentPipeline string
	if p.Spec != nil && p.Spec.DeploymentPipelineRef != nil {
		deploymentPipeline = p.Spec.DeploymentPipelineRef.Name
	}
	return models.Project{
		UID:                derefStr(p.Metadata.Uid),
		Name:               p.Metadata.Name,
		NamespaceName:      derefStr(p.Metadata.Namespace),
		DisplayName:        annotation(p.Metadata.Annotations, AnnotationKeyDisplayName),
		Description:        annotation(p.Metadata.Annotations, AnnotationKeyDescription),
		DeploymentPipeline: deploymentPipeline,
		CreatedAt:          derefTimeRFC3339(p.Metadata.CreationTimestamp),
		Status:             latestProjectStatusReason(p.Status),
	}
}

func buildCreateProjectBody(req *models.CreateProjectRequest) gen.CreateProjectJSONRequestBody {
	body := gen.Project{
		Metadata: gen.ObjectMeta{Name: req.Name},
	}
	if req.DisplayName != "" || req.Description != "" {
		ann := map[string]string{}
		if req.DisplayName != "" {
			ann[AnnotationKeyDisplayName] = req.DisplayName
		}
		if req.Description != "" {
			ann[AnnotationKeyDescription] = req.Description
		}
		body.Metadata.Annotations = &ann
	}
	if req.DeploymentPipeline != "" {
		body.Spec = &gen.ProjectSpec{
			DeploymentPipelineRef: &struct {
				Kind *gen.ProjectSpecDeploymentPipelineRefKind `json:"kind,omitempty"`
				Name string                                    `json:"name"`
			}{Name: req.DeploymentPipeline},
		}
	}
	return body
}

func latestProjectStatusReason(status *gen.ProjectStatus) string {
	if status == nil {
		return ""
	}
	return latestConditionReason(status.Conditions)
}
