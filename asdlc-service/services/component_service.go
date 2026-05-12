package services

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

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

	// OpenAPI for the Test tab. Reads the spec from .asdlc/design.json.
	GetComponentOpenAPI(ctx context.Context, orgName, projectName, componentName string) (*models.ComponentOpenAPI, error)

	// ProxyTestRequest forwards an HTTP request from the Test tab to the
	// component's live deployment endpoint, side-stepping browser CORS.
	// targetURL must start with the URL of one of the component's
	// ReleaseBinding endpoints; otherwise ErrInvalidTestTarget.
	ProxyTestRequest(ctx context.Context, orgName, projectName, componentName, targetURL, method string, headers http.Header, body io.Reader) (*http.Response, error)

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
}

func NewComponentService(client openchoreo.ComponentClient, observClient observability.Client, artifactStore *ArtifactStore) ComponentService {
	return &componentService{
		client:        client,
		observClient:  observClient,
		artifactStore: artifactStore,
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

// UpdateWorkflowEnvVars mirrors a per-component env-var edit onto the OC
// Component so the next build picks the new values up via the
// dockerfile-builder workflow's `environmentVariables` parameter. The
// underlying client returns nil when the Component doesn't yet exist
// (e.g. the user is editing env vars before first dispatch).
func (s *componentService) UpdateWorkflowEnvVars(ctx context.Context, orgName, projectName, componentName string, envVars []models.WorkflowEnvVarRef) error {
	if err := s.client.UpdateComponentWorkflowEnvVars(ctx, orgName, projectName, componentName, envVars); err != nil {
		return translateComponentHTTPError(err)
	}
	return nil
}

// GetComponentOpenAPI reads .asdlc/design.json via the ArtifactStore and
// returns the OpenAPI spec for the named component. The URL param is the
// k8s-shaped slug; we match it against toK8sName(design.Name) so callers
// can use the same identifier they use everywhere else (build, deploy,
// configs). Returns ErrComponentNotFound when design.json is missing or
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

// proxyHTTPClient is shared across requests so we benefit from connection
// reuse. 30s is plenty for a smoke-test invocation against an in-cluster
// endpoint; longer hangs almost always mean the user's app is broken.
var proxyHTTPClient = &http.Client{Timeout: 30 * time.Second}

// hopByHopHeaders are stripped from both inbound and outbound test-proxy
// requests per RFC 7230 §6.1.
var hopByHopHeaders = map[string]struct{}{
	"connection":          {},
	"keep-alive":          {},
	"proxy-authenticate":  {},
	"proxy-authorization": {},
	"te":                  {},
	"trailers":            {},
	"transfer-encoding":   {},
	"upgrade":             {},
}

func (s *componentService) ProxyTestRequest(ctx context.Context, orgName, projectName, componentName, targetURL, method string, headers http.Header, body io.Reader) (*http.Response, error) {
	if targetURL == "" {
		return nil, ErrInvalidTestTarget
	}
	parsed, err := url.Parse(targetURL)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return nil, ErrInvalidTestTarget
	}

	// Validate the target against the component's known deployment endpoints.
	// We require the target to share scheme+host+path-prefix with one of them
	// — that's the SSRF guardrail: the proxy can only reach a URL the
	// component has already published as a public endpoint.
	deployments, err := s.client.ListDeployments(ctx, orgName, projectName, componentName)
	if err != nil {
		return nil, translateComponentHTTPError(err)
	}
	if !endpointMatchesDeployment(targetURL, deployments) {
		return nil, ErrInvalidTestTarget
	}

	req, err := http.NewRequestWithContext(ctx, method, targetURL, body)
	if err != nil {
		return nil, fmt.Errorf("build proxy request: %w", err)
	}
	for k, vv := range headers {
		if _, hop := hopByHopHeaders[strings.ToLower(k)]; hop {
			continue
		}
		for _, v := range vv {
			req.Header.Add(k, v)
		}
	}

	resp, err := proxyHTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("proxy request: %w", err)
	}
	return resp, nil
}

// endpointMatchesDeployment is true if targetURL shares scheme + host + the
// path prefix of any deployment URL the component has registered. We accept
// any subpath (e.g. /health under the base) but reject host or scheme drift.
func endpointMatchesDeployment(targetURL string, deployments *models.DeploymentList) bool {
	if deployments == nil {
		return false
	}
	t, err := url.Parse(targetURL)
	if err != nil {
		return false
	}
	for _, d := range deployments.Items {
		if d.EndpointURL == "" {
			continue
		}
		base, err := url.Parse(d.EndpointURL)
		if err != nil {
			continue
		}
		if t.Scheme != base.Scheme || t.Host != base.Host {
			continue
		}
		// Strip trailing slash on both so /foo and /foo/ are equivalent.
		basePath := strings.TrimRight(base.Path, "/")
		tPath := strings.TrimRight(t.Path, "/")
		if basePath == "" || tPath == basePath || strings.HasPrefix(tPath, basePath+"/") {
			return true
		}
	}
	return false
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
