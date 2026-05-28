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
//
// Deploy chain: the BFF used to drive Workload → ComponentRelease →
// ReleaseBinding from `DeployFromBuild` to work around an OC v1.0.0 bug
// where autoDeploy created bindings with empty environment configs and
// rendering failed on missing schema defaults. The deployed OC version
// (1.0.1-hotfix.1, pinned in wso2cloud-deployment) applies schema
// defaults from the ClusterComponentType on empty configs, so we set
// AutoDeploy=true on every Component (see dispatch_service.ensureOCComponent)
// and the build workflow's generate-workload-cr step is the only writer
// of the Workload CR. The BFF reads ReleaseBindings via ListDeployments.
type ComponentService interface {
	ListComponents(ctx context.Context, orgName, projectName string, limit int, cursor string) (*models.ComponentList, error)
	GetComponent(ctx context.Context, orgName, projectName, componentName string) (*models.Component, error)
	CreateComponent(ctx context.Context, orgName, projectName string, req *models.CreateComponentRequest) (*models.Component, error)
	UpdateWorkflowEnvVars(ctx context.Context, orgName, projectName, componentName string, envVars []models.WorkflowEnvVarRef) error

	// Deploy (read-only — autoDeploy on the Component drives the chain)
	ListDeployments(ctx context.Context, orgName, projectName, componentName string) (*models.DeploymentList, error)

	// OpenAPI for the Test tab. Reads the spec from
	// `specs/design/components/<name>/openapi.yaml`. The Test tab's
	// swagger-ui invokes the deployed endpoint directly; CORS is enabled
	// on the service ClusterComponentType's HTTPRoute.
	GetComponentOpenAPI(ctx context.Context, orgName, projectName, componentName string) (*models.ComponentOpenAPI, error)

	// Build (workflow runs)
	TriggerBuild(ctx context.Context, orgName, projectName, componentName string) (*models.WorkflowRun, error)
	ListBuilds(ctx context.Context, orgName, projectName, componentName string, limit int, cursor string) (*models.WorkflowRunList, error)
	GetBuildStatus(ctx context.Context, orgName, buildName string) (*models.WorkflowRun, error)
	GetBuildLogs(ctx context.Context, orgName, projectName, componentName, buildName string) (*models.BuildLogs, error)
}

type componentService struct {
	client        openchoreo.ComponentClient
	observClient  observability.Client
	artifactStore *ArtifactStore
	// repoSvc + buildCredSvc are used by TriggerBuild to pre-stage the
	// per-WorkflowRun build Secret. Optional — nil means "no staging"
	// (tests / unit-only flows).
	repoSvc       RepoService
	buildCredSvc  *BuildCredentialsService
}

// NewComponentService builds the component service. repoSvc + buildCredSvc
// may be nil in tests / unit-only flows; production wiring passes both so
// TriggerBuild can pre-stage the per-WorkflowRun build Secret.
func NewComponentService(client openchoreo.ComponentClient, observClient observability.Client, artifactStore *ArtifactStore, repoSvc RepoService, buildCredSvc *BuildCredentialsService) ComponentService {
	return &componentService{
		client:        client,
		observClient:  observClient,
		artifactStore: artifactStore,
		repoSvc:       repoSvc,
		buildCredSvc:  buildCredSvc,
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

// UpdateWorkflowEnvVars writes per-component env vars onto each of the
// component's ReleaseBindings (one per environment) at
// `spec.workloadOverrides.container.env`. OC's controller picks them up
// on the next reconcile — no rebuild required. When no ReleaseBindings
// exist yet (the user is editing env vars before first deploy) the
// underlying client returns nil and the caller is expected to retry
// after the first build has produced a binding.
func (s *componentService) UpdateWorkflowEnvVars(ctx context.Context, orgName, projectName, componentName string, envVars []models.WorkflowEnvVarRef) error {
	if err := s.client.UpdateComponentWorkflowEnvVars(ctx, orgName, projectName, componentName, envVars); err != nil {
		return translateComponentHTTPError(err)
	}
	return nil
}

// GetComponentOpenAPI reads the `specs/design/` tree via the ArtifactStore
// (assembling per-component design.md + openapi.yaml into the in-memory
// design) and returns the OpenAPI spec for the named component. The URL
// param is the k8s-shaped slug; we match it against toK8sName(design.Name)
// so callers can use the same identifier they use everywhere else (build,
// deploy, configs). Returns ErrComponentNotFound when no design exists or
// no component matches, ErrComponentNotService when the component exists
// but isn't a "service".
func (s *componentService) GetComponentOpenAPI(ctx context.Context, orgName, projectName, componentName string) (*models.ComponentOpenAPI, error) {
	if s.artifactStore == nil {
		return nil, fmt.Errorf("artifact store not configured")
	}
	design, err := s.artifactStore.ReadDesign(ctx, orgName, projectName)
	if err != nil {
		if IsNotFound(err) {
			return nil, ErrComponentNotFound
		}
		return nil, fmt.Errorf("read design: %w", err)
	}
	if design == nil {
		return nil, ErrComponentNotFound
	}
	for _, c := range design.Components {
		if toK8sName(c.Name) != componentName {
			continue
		}
		if c.ComponentType != "service" {
			return &models.ComponentOpenAPI{
				ComponentName: componentName,
				ComponentType: c.ComponentType,
			}, ErrComponentNotService
		}
		return &models.ComponentOpenAPI{
			ComponentName: componentName,
			ComponentType: c.ComponentType,
			Spec:          c.OpenAPISpec,
		}, nil
	}
	return nil, ErrComponentNotFound
}

func (s *componentService) ListDeployments(ctx context.Context, orgName, projectName, componentName string) (*models.DeploymentList, error) {
	list, err := s.client.ListDeployments(ctx, orgName, projectName, componentName)
	if err != nil {
		return nil, translateComponentHTTPError(err)
	}
	return list, nil
}

func (s *componentService) TriggerBuild(ctx context.Context, orgName, projectName, componentName string) (*models.WorkflowRun, error) {
	// Pre-stage the per-WorkflowRun build Secret in workflows-<orgID> so
	// the shared dockerfile-builder workflow's checkout-source mounts a
	// populated Secret (see docs/design/build-credential-injection.md).
	// Manual triggers from the console go through this path; the
	// webhook-driven dispatch path uses workflowRunService.dispatchBuild.
	runName := openchoreo.NewBuildRunName(projectName, componentName)
	if s.repoSvc != nil && s.buildCredSvc != nil {
		repo, err := s.repoSvc.GetRepo(ctx, projectName)
		switch {
		case err != nil:
			slog.WarnContext(ctx, "trigger-build: GetRepo failed; proceeding without staged Secret (build will fail at clone)",
				"orgName", orgName, "projectName", projectName, "error", err)
		case repo == nil || repo.RepoSlug == "":
			slog.WarnContext(ctx, "trigger-build: no repo / repoSlug; proceeding without staged Secret",
				"orgName", orgName, "projectName", projectName)
		default:
			if _, sErr := s.buildCredSvc.StageBuildSecret(ctx, orgName, repo.RepoSlug, runName); sErr != nil {
				return nil, fmt.Errorf("trigger-build: stage-build-secret: %w", sErr)
			}
		}
	}
	run, err := s.client.TriggerBuild(ctx, orgName, projectName, componentName, runName)
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
