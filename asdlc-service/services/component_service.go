package services

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"

	"github.com/wso2/asdlc/asdlc-service/clients/observability"
	"github.com/wso2/asdlc/asdlc-service/clients/openchoreo"
	"github.com/wso2/asdlc/asdlc-service/clients/requests"
	"github.com/wso2/asdlc/asdlc-service/models"
)

// ComponentService handles business logic for component operations.
// ComponentName parameters are the user-friendly name; the OC client prefixes
// with projectName internally (see ScopedComponentName) because OC components
// share a single k8s namespace across all projects in an org.
type ComponentService interface {
	ListComponents(ctx context.Context, orgName, projectName string, limit int, cursor string) (*models.ComponentList, error)
	GetComponent(ctx context.Context, orgName, projectName, componentName string) (*models.Component, error)
	CreateComponent(ctx context.Context, orgName, projectName string, req *models.CreateComponentRequest) (*models.Component, error)

	// Workload + Release (deploy flow)
	CreateWorkload(ctx context.Context, orgName string, req *models.CreateWorkloadRequest) error
	CreateComponentRelease(ctx context.Context, params *models.CreateReleaseParams) error
	Deploy(ctx context.Context, orgName, projectName, componentName, environment, releaseName string) error
	DeployFromBuild(ctx context.Context, orgName, projectName, componentName, environment string, port int) error
	ListDeployments(ctx context.Context, orgName, projectName, componentName string) (*models.DeploymentList, error)

	// Build (workflow runs)
	TriggerBuild(ctx context.Context, orgName, projectName, componentName string) (*models.WorkflowRun, error)
	ListBuilds(ctx context.Context, orgName, projectName, componentName string, limit int, cursor string) (*models.WorkflowRunList, error)
	GetBuildStatus(ctx context.Context, orgName, buildName string) (*models.WorkflowRun, error)
	GetBuildLogs(ctx context.Context, orgName, projectName, componentName, buildName string) (*models.BuildLogs, error)
}

type componentService struct {
	client        openchoreo.ComponentClient
	observClient  observability.Client
	configSvc     ConfigService
	buildRegistry string
}

func NewComponentService(client openchoreo.ComponentClient, observClient observability.Client, configSvc ConfigService, buildRegistry string) ComponentService {
	return &componentService{
		client:        client,
		observClient:  observClient,
		configSvc:     configSvc,
		buildRegistry: buildRegistry,
	}
}

func (s *componentService) ListComponents(ctx context.Context, orgName, projectName string, limit int, cursor string) (*models.ComponentList, error) {
	list, err := s.client.ListComponents(ctx, orgName, projectName, limit, cursor)
	if err != nil {
		return nil, translateComponentHTTPError(err)
	}
	return list, nil
}

func (s *componentService) GetComponent(ctx context.Context, orgName, projectName, componentName string) (*models.Component, error) {
	comp, err := s.client.GetComponent(ctx, orgName, projectName, componentName)
	if err != nil {
		return nil, translateComponentHTTPError(err)
	}
	return comp, nil
}

func (s *componentService) CreateComponent(ctx context.Context, orgName, projectName string, req *models.CreateComponentRequest) (*models.Component, error) {
	comp, err := s.client.CreateComponent(ctx, orgName, projectName, req)
	if err != nil {
		return nil, translateComponentHTTPError(err)
	}
	return comp, nil
}

func (s *componentService) CreateWorkload(ctx context.Context, orgName string, req *models.CreateWorkloadRequest) error {
	if err := s.client.CreateWorkload(ctx, orgName, req); err != nil {
		return translateComponentHTTPError(err)
	}
	return nil
}

func (s *componentService) CreateComponentRelease(ctx context.Context, params *models.CreateReleaseParams) error {
	if err := s.client.CreateComponentRelease(ctx, params); err != nil {
		return translateComponentHTTPError(err)
	}
	return nil
}

func (s *componentService) Deploy(ctx context.Context, orgName, projectName, componentName, environment, releaseName string) error {
	if err := s.client.CreateReleaseBinding(ctx, orgName, projectName, componentName, environment, releaseName); err != nil {
		return translateComponentHTTPError(err)
	}
	return nil
}

// DeployFromBuild orchestrates: verify a build succeeded → Workload →
// ComponentRelease → ReleaseBinding. We drive all three ourselves because OC
// v1.0.0's `autoDeploy` does create the CR + Binding when a Workload appears,
// but doesn't populate `componentTypeEnvironmentConfigs` from the
// ClusterComponentType schema defaults — rendering then fails with "no such
// key: resources". See Component with `AutoDeploy: false` in task_service.go.
//
// OC's WorkflowRun API also doesn't surface the built image (it drops Argo
// task outputs), so we construct the image ref from convention —
// ASDLC's dockerfile-builder always pushes to
// `<registry>/<orgNs>-<projectName>-<scopedName>:latest`. Override the registry
// host via OPENCHOREO_BUILD_REGISTRY.
//
// TODO(option B / proper fix): push all of this out of the BFF. OC's
// intended CI pipeline ends with a `generate-workload-cr` workflow step that
// creates the Workload itself, and a future OC version is expected to fill
// in schema defaults on autoDeploy-created bindings. Once both land, the BFF
// can drop DeployFromBuild entirely and rely on `autoDeploy: true` on the
// Component — create Component + trigger build, nothing else. Requires:
//   - Custom ClusterWorkflow extending containerfile-build with a curl step
//     that POSTs to `/api/v1/namespaces/{ns}/workloads` (OAuth via a
//     workflow-dedicated client like `openchoreo-workload-publisher-client`).
//   - OC upgrade that honors schema defaults on auto-created bindings OR
//     setting defaults via Environment.spec or Component.spec.parameters
//     (neither route works in v1.0.0 as of 2026-04).
// Reference: docs/platform-engineer-guide/workflows/workload-generation.md
// in the openchoreo docs repo.
func (s *componentService) DeployFromBuild(ctx context.Context, orgName, projectName, componentName, environment string, port int) error {
	builds, err := s.client.ListWorkflowRuns(ctx, orgName, projectName, componentName, 10, "")
	if err != nil {
		return fmt.Errorf("list builds: %w", translateComponentHTTPError(err))
	}

	hasSuccess := false
	for _, b := range builds.Items {
		if b.Status == "WorkflowSucceeded" || b.Status == "Succeeded" {
			hasSuccess = true
			break
		}
	}
	if !hasSuccess {
		return fmt.Errorf("no successful build found for component %s", componentName)
	}

	image := openchoreo.BuildImageRef(s.buildRegistry, orgName, projectName, componentName)
	slog.InfoContext(ctx, "deploying from build", "component", componentName, "image", image, "environment", environment, "port", port)

	var envVars []models.EnvVar
	if s.configSvc != nil {
		envVars, err = s.configSvc.GetEnvVarsForDeploy(ctx, orgName, projectName, componentName)
		if err != nil {
			slog.WarnContext(ctx, "failed to fetch env vars for deploy, proceeding without", "error", err)
		}
	}

	err = s.client.CreateWorkload(ctx, orgName, &models.CreateWorkloadRequest{
		ComponentName: componentName,
		ProjectName:   projectName,
		Image:         image,
		Port:          port,
		EnvVars:       envVars,
	})
	if err != nil {
		return fmt.Errorf("create workload: %w", translateComponentHTTPError(err))
	}

	releaseName := componentName + "-release-1"
	err = s.client.CreateComponentRelease(ctx, &models.CreateReleaseParams{
		OrgName:       orgName,
		ProjectName:   projectName,
		ComponentName: componentName,
		ReleaseName:   releaseName,
		Image:         image,
		Port:          port,
		EnvVars:       envVars,
	})
	if err != nil {
		var httpErr *requests.HttpError
		if !errors.As(err, &httpErr) || httpErr.StatusCode != http.StatusConflict {
			return fmt.Errorf("create release: %w", translateComponentHTTPError(err))
		}
	}

	err = s.client.CreateReleaseBinding(ctx, orgName, projectName, componentName, environment, releaseName)
	if err != nil {
		var httpErr *requests.HttpError
		if !errors.As(err, &httpErr) || httpErr.StatusCode != http.StatusConflict {
			return fmt.Errorf("create release binding: %w", translateComponentHTTPError(err))
		}
	}
	return nil
}

func (s *componentService) ListDeployments(ctx context.Context, orgName, projectName, componentName string) (*models.DeploymentList, error) {
	list, err := s.client.ListDeployments(ctx, orgName, projectName, componentName)
	if err != nil {
		return nil, translateComponentHTTPError(err)
	}
	return list, nil
}

func (s *componentService) TriggerBuild(ctx context.Context, orgName, projectName, componentName string) (*models.WorkflowRun, error) {
	run, err := s.client.TriggerBuild(ctx, orgName, projectName, componentName)
	if err != nil {
		return nil, translateComponentHTTPError(err)
	}
	return run, nil
}

func (s *componentService) ListBuilds(ctx context.Context, orgName, projectName, componentName string, limit int, cursor string) (*models.WorkflowRunList, error) {
	list, err := s.client.ListWorkflowRuns(ctx, orgName, projectName, componentName, limit, cursor)
	if err != nil {
		return nil, translateComponentHTTPError(err)
	}
	return list, nil
}

func (s *componentService) GetBuildStatus(ctx context.Context, orgName, buildName string) (*models.WorkflowRun, error) {
	run, err := s.client.GetWorkflowRun(ctx, orgName, buildName)
	if err != nil {
		return nil, translateComponentHTTPError(err)
	}
	return run, nil
}

func (s *componentService) GetBuildLogs(ctx context.Context, orgName, projectName, componentName, buildName string) (*models.BuildLogs, error) {
	if s.observClient == nil {
		return nil, ErrLogsUnavailable
	}
	logs, err := s.observClient.GetBuildLogs(ctx, orgName, projectName, componentName, buildName)
	if err != nil {
		return nil, fmt.Errorf("get build logs: %w", err)
	}
	return logs, nil
}

func translateComponentHTTPError(err error) error {
	if err == nil {
		return nil
	}
	var httpErr *requests.HttpError
	if errors.As(err, &httpErr) {
		switch httpErr.StatusCode {
		case http.StatusNotFound:
			return fmt.Errorf("%w: %s", ErrComponentNotFound, httpErr.Body)
		case http.StatusUnauthorized:
			return ErrUnauthorized
		}
	}
	return err
}
