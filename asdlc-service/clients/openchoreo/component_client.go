package openchoreo

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/wso2/asdlc/asdlc-service/clients/httpx"
	"github.com/wso2/asdlc/asdlc-service/clients/oauth"
	"github.com/wso2/asdlc/asdlc-service/clients/requests"
	"github.com/wso2/asdlc/asdlc-service/models"
)

// ComponentClient defines operations for managing OpenChoreo components.
// Every method that names a component takes the user-friendly componentName
// plus the projectName that scopes it. The client applies ScopedComponentName
// internally so callers never deal with the prefixed k8s name.
type ComponentClient interface {
	ListComponents(ctx context.Context, orgName, projectName string, limit int, cursor string) (*models.ComponentList, error)
	GetComponent(ctx context.Context, orgName, projectName, componentName string) (*models.Component, error)
	CreateComponent(ctx context.Context, orgName, projectName string, req *models.CreateComponentRequest) (*models.Component, error)

	// Workload + Release (deploy flow)
	CreateWorkload(ctx context.Context, orgName string, req *models.CreateWorkloadRequest) error
	CreateComponentRelease(ctx context.Context, params *models.CreateReleaseParams) error
	CreateReleaseBinding(ctx context.Context, orgName, projectName, componentName, environment, releaseName string) error
	ListDeployments(ctx context.Context, orgName, projectName, componentName string) (*models.DeploymentList, error)

	// Build (workflow runs)
	TriggerBuild(ctx context.Context, orgName, projectName, componentName string) (*models.WorkflowRun, error)
	// TriggerBuildAtCommit creates a WorkflowRun pinned to commitSHA via
	// params.repository.revision.commit. Mirrors agent-manager's pattern at
	// agent-manager-service/clients/openchoreosvc/client/builds.go:71-85.
	TriggerBuildAtCommit(ctx context.Context, orgName, projectName, componentName, commitSHA string) (*models.WorkflowRun, error)
	// TriggerCodingAgent creates a WorkflowRun of ClusterWorkflow
	// `app-factory-coding-agent` for the per-task ephemeral pod that runs the
	// Claude Agent SDK against the task's feature branch. Replaces the legacy
	// HTTP POST to remote-worker /dispatch. The label
	// `app-factory.openchoreo.dev/coding-agent-task` carries the taskId so
	// the BFF watcher can correlate runs back to the task.
	TriggerCodingAgent(ctx context.Context, params CodingAgentParams) (*models.WorkflowRun, error)
	ListWorkflowRuns(ctx context.Context, orgName, projectName, componentName string, limit int, cursor string) (*models.WorkflowRunList, error)
	GetWorkflowRun(ctx context.Context, orgName, runName string) (*models.WorkflowRun, error)
}

// CodingAgentParams is the input to TriggerCodingAgent. Mirrors the schema
// of `app-factory-coding-agent` ClusterWorkflow. All fields are required.
type CodingAgentParams struct {
	OrgName       string
	ProjectName   string
	ComponentName string
	TaskID        string
	BranchName    string
	Prompt        string
	CorrelationID string
	RepoURL       string
	IdentityName  string
	IdentityEmail string
	IdentityLogin string
	Bearer        string
	GitServiceURL string
}

type componentClient struct {
	clientBase
}

func NewComponentClient(baseURL, hostHeader string, tokenProvider *oauth.TokenProvider, namespaceOverride string) ComponentClient {
	return &componentClient{
		clientBase: clientBase{
			baseURL:       baseURL,
			hostHeader:    hostHeader,
			httpClient:    &http.Client{Transport: httpx.WrapTransport(nil)},
			tokenProvider: tokenProvider,
			nsMap:         parseNamespaceOverride(namespaceOverride),
		},
	}
}

// -- URL builders ------------------------------------------------------------

func (c *componentClient) componentsURL(namespace string) string {
	return fmt.Sprintf("%s/api/v1/namespaces/%s/components", c.baseURL, namespace)
}

func (c *componentClient) componentURL(namespace, componentName string) string {
	return fmt.Sprintf("%s/api/v1/namespaces/%s/components/%s", c.baseURL, namespace, componentName)
}

func (c *componentClient) workloadsURL(namespace string) string {
	return fmt.Sprintf("%s/api/v1/namespaces/%s/workloads", c.baseURL, namespace)
}

func (c *componentClient) workloadURL(namespace, workloadName string) string {
	return fmt.Sprintf("%s/api/v1/namespaces/%s/workloads/%s", c.baseURL, namespace, workloadName)
}

func (c *componentClient) componentReleasesURL(namespace string) string {
	return fmt.Sprintf("%s/api/v1/namespaces/%s/componentreleases", c.baseURL, namespace)
}

func (c *componentClient) releaseBindingsURL(namespace string) string {
	return fmt.Sprintf("%s/api/v1/namespaces/%s/releasebindings", c.baseURL, namespace)
}

func (c *componentClient) workflowRunsURL(namespace string) string {
	return fmt.Sprintf("%s/api/v1/namespaces/%s/workflowruns", c.baseURL, namespace)
}

func (c *componentClient) workflowRunURL(namespace, runName string) string {
	return fmt.Sprintf("%s/api/v1/namespaces/%s/workflowruns/%s", c.baseURL, namespace, runName)
}

// -- Normalize helpers -------------------------------------------------------

func normalizeComponent(comp ocComponent) models.Component {
	ann := comp.Metadata.Annotations
	labels := comp.Metadata.Labels

	var displayName, description string
	if ann != nil {
		displayName = ann["openchoreo.dev/display-name"]
		description = ann["openchoreo.dev/description"]
	}

	var projectName string
	if comp.Spec.Owner != nil {
		projectName = comp.Spec.Owner.ProjectName
	}
	if projectName == "" && labels != nil {
		projectName = labels["openchoreo.dev/project-name"]
	}

	var componentType string
	if comp.Spec.ComponentType != nil {
		componentType = comp.Spec.ComponentType.Name
	}
	if componentType == "" && labels != nil {
		componentType = labels["openchoreo.dev/component-type"]
	}

	return models.Component{
		UID:         comp.Metadata.UID,
		Name:        FriendlyComponentName(comp.Metadata.Name, projectName),
		ProjectName: projectName,
		DisplayName: displayName,
		Description: description,
		Type:        componentType,
		AutoDeploy:  comp.Spec.AutoDeploy,
		AutoBuild:   comp.Spec.AutoBuild,
		CreatedAt:   comp.Metadata.CreationTimestamp,
		Status:      latestConditionReason(comp.Status.Conditions),
	}
}

func normalizeWorkflowRun(run ocWorkflowRun) models.WorkflowRun {
	labels := run.Metadata.Labels
	var componentName, projectName string
	if labels != nil {
		componentName = labels["openchoreo.dev/component"]
		projectName = labels["openchoreo.dev/project"]
	}

	status := "Pending"
	for _, cond := range run.Status.Conditions {
		if cond.Type == "WorkflowCompleted" && cond.Reason != "" {
			status = cond.Reason
			break
		}
		if cond.Type == "WorkflowRunning" && cond.Status == "True" {
			status = "Running"
		}
	}

	var image, commit string
	tasks := make([]models.WorkflowRunTask, 0, len(run.Status.Tasks))
	for _, task := range run.Status.Tasks {
		flat := models.WorkflowRunTask{Name: task.Name, Outputs: map[string]string{}}
		if task.Outputs != nil {
			for _, p := range task.Outputs.Parameters {
				flat.Outputs[p.Name] = p.Value
			}
		}
		tasks = append(tasks, flat)
		// Convenience extracts retained for backward compat with the
		// existing component_service callers that read Image / Commit
		// directly off the flat WorkflowRun.
		if task.Outputs != nil {
			switch task.Name {
			case "publish-image":
				if v, ok := flat.Outputs["image"]; ok {
					image = v
				}
			case "checkout-source":
				if v, ok := flat.Outputs["git-revision"]; ok {
					commit = v
				}
			}
		}
	}

	return models.WorkflowRun{
		Name:          run.Metadata.Name,
		Status:        status,
		StartedAt:     run.Metadata.CreationTimestamp,
		ComponentName: FriendlyComponentName(componentName, projectName),
		ProjectName:   projectName,
		Image:         image,
		Commit:        commit,
		Tasks:         tasks,
	}
}

func normalizeDeployment(rb ocReleaseBinding) models.Deployment {
	var projectName, componentName string
	if rb.Spec.Owner != nil {
		projectName = rb.Spec.Owner.ProjectName
		componentName = rb.Spec.Owner.ComponentName
	}

	// Extract external endpoint URL from ReleaseBinding status
	var endpointURL string
	for _, ep := range rb.Status.Endpoints {
		if httpURL, ok := ep.ExternalURLs["http"]; ok {
			endpointURL = fmt.Sprintf("%s://%s:%d%s", httpURL.Scheme, httpURL.Host, httpURL.Port, httpURL.Path)
			if httpURL.Path == "" || httpURL.Path == "/" {
				endpointURL = fmt.Sprintf("%s://%s:%d/", httpURL.Scheme, httpURL.Host, httpURL.Port)
			}
			break
		}
	}

	return models.Deployment{
		Name:          rb.Metadata.Name,
		Environment:   rb.Spec.Environment,
		ReleaseName:   rb.Spec.ReleaseName,
		ComponentName: FriendlyComponentName(componentName, projectName),
		EndpointURL:   endpointURL,
		CreatedAt:     rb.Metadata.CreationTimestamp,
		Status:        latestConditionReason(rb.Status.Conditions),
	}
}

// -- Component CRUD ----------------------------------------------------------

func (c *componentClient) ListComponents(ctx context.Context, orgName, projectName string, limit int, cursor string) (*models.ComponentList, error) {
	req := c.newRequest(ctx, "openchoreo.ListComponents", http.MethodGet, c.componentsURL(c.resolveNamespace(orgName)))
	req.SetQuery("labelSelector", fmt.Sprintf("openchoreo.dev/project=%s", projectName))
	if limit > 0 {
		req.SetQuery("limit", fmt.Sprintf("%d", limit))
	}
	if cursor != "" {
		req.SetQuery("cursor", cursor)
	}

	var raw ocComponentList
	if err := c.send(ctx, req, &raw, http.StatusOK); err != nil {
		return nil, fmt.Errorf("list components: %w", err)
	}

	items := make([]models.Component, len(raw.Items))
	for i, comp := range raw.Items {
		items[i] = normalizeComponent(comp)
	}
	return &models.ComponentList{Items: items}, nil
}

func (c *componentClient) GetComponent(ctx context.Context, orgName, projectName, componentName string) (*models.Component, error) {
	k8sName := ScopedComponentName(projectName, componentName)
	req := c.newRequest(ctx, "openchoreo.GetComponent", http.MethodGet, c.componentURL(c.resolveNamespace(orgName), k8sName))

	var raw ocComponent
	if err := c.send(ctx, req, &raw, http.StatusOK); err != nil {
		return nil, fmt.Errorf("get component: %w", err)
	}
	comp := normalizeComponent(raw)
	return &comp, nil
}

func (c *componentClient) CreateComponent(ctx context.Context, orgName, projectName string, req *models.CreateComponentRequest) (*models.Component, error) {
	body := ocComponent{
		Metadata: ocObjectMeta{
			Name: ScopedComponentName(projectName, req.Name),
			Annotations: map[string]string{
				"openchoreo.dev/display-name": req.DisplayName,
				"openchoreo.dev/description":  req.Description,
			},
		},
		Spec: ocComponentSpec{
			Owner:         &ocOwner{ProjectName: projectName},
			ComponentType: &ocComponentTypeRef{Kind: "ClusterComponentType", Name: req.Type},
			AutoBuild:     req.AutoBuild,
			AutoDeploy:    req.AutoDeploy,
		},
	}

	if req.Workflow != nil {
		body.Spec.Workflow = &ocWorkflow{
			Kind: req.Workflow.Kind,
			Name: req.Workflow.Name,
		}
		if req.Workflow.Parameters != nil && req.Workflow.Parameters.Repository != nil {
			repo := req.Workflow.Parameters.Repository
			body.Spec.Workflow.Parameters = &ocWorkflowParameters{
				Repository: &ocWorkflowRepository{
					URL:       repo.URL,
					SecretRef: repo.SecretRef,
					AppPath:   repo.AppPath,
				},
			}
			if repo.Revision != nil {
				body.Spec.Workflow.Parameters.Repository.Revision = &ocWorkflowRevision{
					Branch: repo.Revision.Branch,
					Commit: repo.Revision.Commit,
				}
			}
		}
		if req.Workflow.Parameters != nil && req.Workflow.Parameters.Docker != nil {
			if body.Spec.Workflow.Parameters == nil {
				body.Spec.Workflow.Parameters = &ocWorkflowParameters{}
			}
			body.Spec.Workflow.Parameters.Docker = &ocDockerParameters{
				Context:  req.Workflow.Parameters.Docker.Context,
				FilePath: req.Workflow.Parameters.Docker.FilePath,
			}
		}
	}

	httpReq := c.newRequest(ctx, "openchoreo.CreateComponent", http.MethodPost, c.componentsURL(c.resolveNamespace(orgName)))
	httpReq.SetJSON(body)

	var raw ocComponent
	err := c.send(ctx, httpReq, &raw, http.StatusCreated)
	if err == nil {
		comp := normalizeComponent(raw)
		return &comp, nil
	}
	// Idempotent on (ocOrgId, project, componentName): a 409 means the component
	// already exists, so fetch and return it. This is asserted at the call
	// boundary per phase0 §1.11.
	var httpErr *requests.HttpError
	if errors.As(err, &httpErr) && httpErr.StatusCode == http.StatusConflict {
		existing, gerr := c.GetComponent(ctx, orgName, projectName, req.Name)
		if gerr != nil {
			return nil, fmt.Errorf("create component returned conflict; refetch failed: %w", gerr)
		}
		return existing, nil
	}
	return nil, fmt.Errorf("create component: %w", err)
}

// -- Workload ----------------------------------------------------------------

func (c *componentClient) CreateWorkload(ctx context.Context, orgName string, req *models.CreateWorkloadRequest) error {
	container := &ocContainer{
		Image: req.Image,
		Args:  req.Args,
	}
	if len(req.EnvVars) > 0 {
		container.Env = toOCEnvVars(req.EnvVars)
	}

	scopedComp := ScopedComponentName(req.ProjectName, req.ComponentName)
	body := ocWorkload{
		Metadata: ocObjectMeta{
			Name: scopedComp + "-workload",
		},
		Spec: ocWorkloadSpec{
			Owner: &ocOwner{
				ProjectName:   req.ProjectName,
				ComponentName: scopedComp,
			},
			Endpoints: map[string]ocEndpoint{
				"http": {
					Type:       "HTTP",
					Port:       req.Port,
					Visibility: []string{"external"},
				},
			},
			Container: container,
		},
	}

	// Upsert: POST, and on 409 Conflict fall back to PUT so the Workload's
	// container image can be refreshed on each deploy. Updating the Workload
	// is what drives OC to emit a new ComponentRelease + (with autoDeploy)
	// ReleaseBinding.
	postReq := c.newRequest(ctx, "openchoreo.CreateWorkload", http.MethodPost, c.workloadsURL(c.resolveNamespace(orgName)))
	postReq.SetJSON(body)
	err := c.send(ctx, postReq, nil, http.StatusCreated)
	if err == nil {
		return nil
	}
	var httpErr *requests.HttpError
	if !errors.As(err, &httpErr) || httpErr.StatusCode != http.StatusConflict {
		return fmt.Errorf("create workload: %w", err)
	}
	putReq := c.newRequest(ctx, "openchoreo.UpdateWorkload", http.MethodPut, c.workloadURL(c.resolveNamespace(orgName), body.Metadata.Name))
	putReq.SetJSON(body)
	if err := c.send(ctx, putReq, nil, http.StatusOK); err != nil {
		return fmt.Errorf("update workload: %w", err)
	}
	return nil
}

// -- ComponentRelease --------------------------------------------------------

func (c *componentClient) CreateComponentRelease(ctx context.Context, params *models.CreateReleaseParams) error {
	ctName := params.ComponentTypeName
	if ctName == "" {
		ctName = "service"
	}

	// Fetch the ComponentType spec to embed in the release snapshot
	ctSpec := c.fetchComponentTypeSpec(ctx, params.OrgName, ctName)

	container := &ocContainer{
		Image: params.Image,
		Args:  params.Args,
	}
	if len(params.EnvVars) > 0 {
		container.Env = toOCEnvVars(params.EnvVars)
	}

	body := ocComponentRelease{
		Metadata: ocObjectMeta{
			Name: ScopedComponentName(params.ProjectName, params.ReleaseName),
		},
		Spec: ocComponentReleaseSpec{
			Owner: &ocOwner{
				ProjectName:   params.ProjectName,
				ComponentName: ScopedComponentName(params.ProjectName, params.ComponentName),
			},
			ComponentType: &ocComponentTypeSnapshot{
				Kind: "ClusterComponentType",
				Name: ctName,
				Spec: ctSpec,
			},
			Workload: &ocComponentReleaseWorkload{
				Endpoints: map[string]ocEndpoint{
					"http": {
						Type:       "HTTP",
						Port:       params.Port,
						Visibility: []string{"external"},
					},
				},
				Container: container,
			},
		},
	}

	httpReq := c.newRequest(ctx, "openchoreo.CreateComponentRelease", http.MethodPost, c.componentReleasesURL(c.resolveNamespace(params.OrgName)))
	httpReq.SetJSON(body)

	if err := c.send(ctx, httpReq, nil, http.StatusCreated); err != nil {
		return fmt.Errorf("create component release: %w", err)
	}
	return nil
}

// toOCEnvVars converts model EnvVars to OC API EnvVars.
func toOCEnvVars(envVars []models.EnvVar) []ocEnvVar {
	result := make([]ocEnvVar, len(envVars))
	for i, ev := range envVars {
		result[i] = ocEnvVar{Key: ev.Key, Value: ev.Value}
	}
	return result
}

// fetchComponentTypeSpec retrieves a ClusterComponentType spec for embedding in releases.
func (c *componentClient) fetchComponentTypeSpec(ctx context.Context, orgName string, ctName string) interface{} {
	url := fmt.Sprintf("%s/api/v1/clustercomponenttypes/%s", c.baseURL, ctName)
	req := c.newRequest(ctx, "openchoreo.GetComponentType", http.MethodGet, url)

	var raw struct {
		Spec interface{} `json:"spec"`
	}
	if err := c.send(ctx, req, &raw, http.StatusOK); err == nil && raw.Spec != nil {
		return raw.Spec
	}

	// Fallback: minimal ComponentType spec with env var support
	return map[string]interface{}{
		"workloadType": "deployment",
		"resources": []map[string]interface{}{
			{
				"id": "deployment",
				"template": map[string]interface{}{
					"apiVersion": "apps/v1",
					"kind":       "Deployment",
					"metadata":   map[string]interface{}{"name": "${metadata.componentName}", "namespace": "${metadata.namespace}", "labels": "${metadata.labels}"},
					"spec": map[string]interface{}{
						"replicas": json.Number("1"),
						"selector": map[string]interface{}{"matchLabels": "${metadata.podSelectors}"},
						"template": map[string]interface{}{
							"metadata": map[string]interface{}{"labels": "${metadata.podSelectors}"},
							"spec": map[string]interface{}{
								"containers": []map[string]interface{}{{
									"name":    "main",
									"image":   "${workload.container.image}",
									"env":     "${workload.container.env}",
									"envFrom": "${configurations.toContainerEnvFrom()}",
								}},
							},
						},
					},
				},
			},
			{
				"id": "service",
				"template": map[string]interface{}{
					"apiVersion": "v1",
					"kind":       "Service",
					"metadata":   map[string]interface{}{"name": "${metadata.componentName}", "namespace": "${metadata.namespace}"},
					"spec":       map[string]interface{}{"selector": "${metadata.podSelectors}", "ports": "${workload.toServicePorts()}"},
				},
			},
		},
	}
}

// -- ReleaseBinding (deploy) -------------------------------------------------

func (c *componentClient) CreateReleaseBinding(ctx context.Context, orgName, projectName, componentName, environment, releaseName string) error {
	scopedComp := ScopedComponentName(projectName, componentName)
	body := ocReleaseBinding{
		Metadata: ocObjectMeta{
			Name: fmt.Sprintf("%s-%s", scopedComp, environment),
		},
		Spec: ocReleaseBindingSpec{
			Owner: &ocOwner{
				ProjectName:   projectName,
				ComponentName: scopedComp,
			},
			Environment: environment,
			ReleaseName: ScopedComponentName(projectName, releaseName),
			ComponentTypeEnvironmentConfigs: map[string]interface{}{
				"replicas": 1,
				"resources": map[string]interface{}{
					"requests": map[string]interface{}{"cpu": "50m", "memory": "128Mi"},
					"limits":   map[string]interface{}{"cpu": "200m", "memory": "256Mi"},
				},
			},
		},
	}

	httpReq := c.newRequest(ctx, "openchoreo.CreateReleaseBinding", http.MethodPost, c.releaseBindingsURL(c.resolveNamespace(orgName)))
	httpReq.SetJSON(body)

	if err := c.send(ctx, httpReq, nil, http.StatusCreated); err != nil {
		return fmt.Errorf("create release binding: %w", err)
	}
	return nil
}

func (c *componentClient) ListDeployments(ctx context.Context, orgName, projectName, componentName string) (*models.DeploymentList, error) {
	httpReq := c.newRequest(ctx, "openchoreo.ListReleaseBindings", http.MethodGet, c.releaseBindingsURL(c.resolveNamespace(orgName)))
	httpReq.SetQuery("component", ScopedComponentName(projectName, componentName))

	var raw ocReleaseBindingList
	if err := c.send(ctx, httpReq, &raw, http.StatusOK); err != nil {
		return nil, fmt.Errorf("list release bindings: %w", err)
	}

	items := make([]models.Deployment, len(raw.Items))
	for i, rb := range raw.Items {
		items[i] = normalizeDeployment(rb)
	}
	return &models.DeploymentList{Items: items}, nil
}

// -- WorkflowRuns (builds) ---------------------------------------------------

func (c *componentClient) TriggerBuild(ctx context.Context, orgName, projectName, componentName string) (*models.WorkflowRun, error) {
	return c.triggerBuildInner(ctx, orgName, projectName, componentName, "")
}

// TriggerBuildAtCommit pins the build to a specific commit SHA via
// params.repository.revision.commit, matching the agent-manager pattern
// (agent-manager-service/clients/openchoreosvc/client/builds.go). Phase 0
// uses this for webhook-driven builds — the SHA comes from the merging
// pull_request.closed event.
func (c *componentClient) TriggerBuildAtCommit(ctx context.Context, orgName, projectName, componentName, commitSHA string) (*models.WorkflowRun, error) {
	return c.triggerBuildInner(ctx, orgName, projectName, componentName, commitSHA)
}

func (c *componentClient) triggerBuildInner(ctx context.Context, orgName, projectName, componentName, commitSHA string) (*models.WorkflowRun, error) {
	scopedComp := ScopedComponentName(projectName, componentName)
	// Fetch the component to get its workflow config
	getReq := c.newRequest(ctx, "openchoreo.GetComponentForBuild", http.MethodGet, c.componentURL(c.resolveNamespace(orgName), scopedComp))
	var rawComp ocComponent
	if err := c.send(ctx, getReq, &rawComp, http.StatusOK); err != nil {
		return nil, fmt.Errorf("get component for build trigger: %w", err)
	}

	// Clone the workflow params so we don't mutate shared state if the OC
	// client cached the component anywhere upstream.
	workflow := rawComp.Spec.Workflow
	if commitSHA != "" && workflow != nil && workflow.Parameters != nil && workflow.Parameters.Repository != nil {
		// Inject SHA at params.repository.revision.commit. Branch stays as-is
		// so OC's clone path works.
		repoCopy := *workflow.Parameters.Repository
		var rev ocWorkflowRevision
		if repoCopy.Revision != nil {
			rev = *repoCopy.Revision
		}
		rev.Commit = commitSHA
		repoCopy.Revision = &rev
		paramsCopy := *workflow.Parameters
		paramsCopy.Repository = &repoCopy
		wfCopy := *workflow
		wfCopy.Parameters = &paramsCopy
		workflow = &wfCopy
	}

	runName := fmt.Sprintf("%s-%d", scopedComp, time.Now().UnixMilli())
	body := ocWorkflowRun{
		Metadata: ocObjectMeta{
			Name: runName,
			Labels: map[string]string{
				"openchoreo.dev/component": scopedComp,
				"openchoreo.dev/project":   projectName,
			},
		},
		Spec: ocWorkflowRunSpec{
			Workflow: workflow,
		},
	}

	httpReq := c.newRequest(ctx, "openchoreo.TriggerBuild", http.MethodPost, c.workflowRunsURL(c.resolveNamespace(orgName)))
	httpReq.SetJSON(body)

	var raw ocWorkflowRun
	if err := c.send(ctx, httpReq, &raw, http.StatusCreated); err != nil {
		return nil, fmt.Errorf("trigger build: %w", err)
	}
	run := normalizeWorkflowRun(raw)
	return &run, nil
}

// TriggerCodingAgent creates a WorkflowRun of ClusterWorkflow
// `app-factory-coding-agent`. Each call creates a fresh run; idempotency is
// the caller's responsibility (see DispatchService.dispatchOne which gates on
// task.LastCodingAgentRunName + DispatchedAt).
func (c *componentClient) TriggerCodingAgent(ctx context.Context, params CodingAgentParams) (*models.WorkflowRun, error) {
	scopedComp := ScopedComponentName(params.ProjectName, params.ComponentName)

	// Run name shape: coding-agent-<short-task>-<unixMs>. K8s names must be
	// ≤63 chars and start with a letter. Truncate the taskID to 8 to stay
	// safely inside the budget. The unixMs suffix makes re-dispatch unique.
	shortTask := params.TaskID
	if len(shortTask) > 8 {
		shortTask = shortTask[:8]
	}
	runName := fmt.Sprintf("coding-agent-%s-%d", shortTask, time.Now().UnixMilli())

	// NOTE: deliberately NOT setting `openchoreo.dev/component` / `openchoreo.dev/project`
	// labels. OC validates ClusterWorkflow → ClusterComponentType allowed-workflow
	// pairs when a WorkflowRun carries the `openchoreo.dev/component` label, which
	// would reject `app-factory-coding-agent` because the user's component is
	// `deployment/service` (allowed only the builder ClusterWorkflows). The agent
	// pod has no need to be tied to the user's Component for OC's purposes — the
	// project + component identifiers flow in via the `parameters.task.*` fields
	// that the runner reads.
	body := ocWorkflowRun{
		Metadata: ocObjectMeta{
			Name: runName,
			Labels: map[string]string{
				"app-factory.openchoreo.dev/coding-agent-task": params.TaskID,
				"app-factory.openchoreo.dev/project":           params.ProjectName,
				"app-factory.openchoreo.dev/component":         scopedComp,
			},
		},
		Spec: ocWorkflowRunSpec{
			Workflow: &ocWorkflow{
				Kind: "ClusterWorkflow",
				Name: "app-factory-coding-agent",
				Parameters: &ocWorkflowParameters{
					Task: &ocCodingAgentTask{
						ID:            params.TaskID,
						OrgID:         params.OrgName,
						ProjectID:     params.ProjectName,
						ComponentName: params.ComponentName,
						BranchName:    params.BranchName,
						Prompt:        params.Prompt,
						CorrelationID: params.CorrelationID,
					},
					Repository: &ocWorkflowRepository{
						URL: params.RepoURL,
						Identity: &ocWorkflowIdentity{
							Name:  params.IdentityName,
							Email: params.IdentityEmail,
							Login: params.IdentityLogin,
						},
					},
					BFF:        &ocCodingAgentBFF{Bearer: params.Bearer},
					GitService: &ocCodingAgentGitService{URL: params.GitServiceURL},
				},
			},
		},
	}

	httpReq := c.newRequest(ctx, "openchoreo.TriggerCodingAgent", http.MethodPost, c.workflowRunsURL(c.resolveNamespace(params.OrgName)))
	httpReq.SetJSON(body)

	var raw ocWorkflowRun
	if err := c.send(ctx, httpReq, &raw, http.StatusCreated); err != nil {
		return nil, fmt.Errorf("trigger coding-agent: %w", err)
	}
	run := normalizeWorkflowRun(raw)
	return &run, nil
}

func (c *componentClient) ListWorkflowRuns(ctx context.Context, orgName, projectName, componentName string, limit int, cursor string) (*models.WorkflowRunList, error) {
	httpReq := c.newRequest(ctx, "openchoreo.ListWorkflowRuns", http.MethodGet, c.workflowRunsURL(c.resolveNamespace(orgName)))
	httpReq.SetQuery("labelSelector", fmt.Sprintf("openchoreo.dev/component=%s", ScopedComponentName(projectName, componentName)))
	if limit > 0 {
		httpReq.SetQuery("limit", fmt.Sprintf("%d", limit))
	}
	if cursor != "" {
		httpReq.SetQuery("cursor", cursor)
	}

	var raw ocWorkflowRunList
	if err := c.send(ctx, httpReq, &raw, http.StatusOK); err != nil {
		return nil, fmt.Errorf("list workflow runs: %w", err)
	}

	items := make([]models.WorkflowRun, len(raw.Items))
	for i, run := range raw.Items {
		items[i] = normalizeWorkflowRun(run)
	}
	return &models.WorkflowRunList{Items: items}, nil
}

func (c *componentClient) GetWorkflowRun(ctx context.Context, orgName, runName string) (*models.WorkflowRun, error) {
	httpReq := c.newRequest(ctx, "openchoreo.GetWorkflowRun", http.MethodGet, c.workflowRunURL(c.resolveNamespace(orgName), runName))

	var raw ocWorkflowRun
	if err := c.send(ctx, httpReq, &raw, http.StatusOK); err != nil {
		return nil, fmt.Errorf("get workflow run: %w", err)
	}
	run := normalizeWorkflowRun(raw)
	return &run, nil
}

