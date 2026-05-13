package openchoreo

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/wso2/asdlc/asdlc-service/clients/openchoreo/gen"
	"github.com/wso2/asdlc/asdlc-service/models"
)

//go:generate go run github.com/matryer/moq@v0.7.1 -rm -fmt goimports -pkg mocks -out mocks/component_client_mock.go . ComponentClient

// ComponentClient defines operations for managing OpenChoreo components.
// Every method that names a component takes the user-friendly componentName
// plus the projectName that scopes it. The client applies ScopedComponentName
// internally so callers never deal with the prefixed k8s name.
//
// Deploy chain: with AutoDeploy=true set on the Component (see
// dispatch_service.ensureOCComponent), OC's Component controller owns the
// Workload → ComponentRelease → ReleaseBinding fan-out. The build
// workflow's `generate-workload-cr` step POSTs the Workload; the
// controller picks it up, hashes the spec, creates a ComponentRelease,
// and binds it into the project's first environment. The BFF only reads
// the result back via ListDeployments. Wrappers for the write side of
// that chain are deliberately absent — add them back per the roadmap
// when a real caller appears (e.g. per-env config overrides).
type ComponentClient interface {
	ListComponents(ctx context.Context, orgName, projectName string, limit int, cursor string) (*models.ComponentList, error)
	GetComponent(ctx context.Context, orgName, projectName, componentName string) (*models.Component, error)
	CreateComponent(ctx context.Context, orgName, projectName string, req *models.CreateComponentRequest) (*models.Component, error)
	// UpdateComponentWorkflowEnvVars rewrites the Component's
	// `spec.workflow.parameters.environmentVariables` array. This is the
	// signal the next build picks up: when the user edits per-component
	// env vars, the BFF mirrors the change onto the OC Component so the
	// next WorkflowRun (triggered by push or manual click) ships the
	// updated env vars through `generate-workload-cr` into the Workload
	// CR. Other workflow parameters (repository, docker, ...) are left
	// untouched.
	UpdateComponentWorkflowEnvVars(ctx context.Context, orgName, projectName, componentName string, envVars []models.WorkflowEnvVarRef) error

	// Deploy (read-only — auto-deploy on the Component drives the chain)
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
// The agent itself creates the feature branch and opens the PR (with
// `Closes #<issueNumber>` so the BFF webhook can link it back to the
// task), so no branch is plumbed through here.
type CodingAgentParams struct {
	OrgName       string
	ProjectName   string
	ComponentName string
	TaskID        string
	Prompt        string
	RepoURL       string
	IdentityName  string
	IdentityEmail string
	IdentityLogin string
	Bearer        string
	GitServiceURL string
	// PlatformURL is the BFF base URL the runner pod uses for the F3c
	// verification-failed callback. Passed through to the ClusterWorkflow
	// parameter `bff.platformUrl` → env var ASDLC_PLATFORM_URL in the pod.
	// Empty means the runner won't call the BFF (the diagnostic still
	// lands on the GitHub issue).
	PlatformURL string
}

type componentClient struct {
	oc *gen.ClientWithResponses
}

func NewComponentClient(cfg Config) ComponentClient {
	oc, err := newGenClient(cfg)
	if err != nil {
		panic(fmt.Errorf("init openchoreo component client: %w", err))
	}
	return &componentClient{oc: oc}
}

// -- Conversions -------------------------------------------------------------

func componentToModel(c gen.Component) models.Component {
	var projectName, componentType string
	var autoBuild, autoDeploy bool
	if c.Spec != nil {
		projectName = c.Spec.Owner.ProjectName
		componentType = c.Spec.ComponentType.Name
		if c.Spec.AutoBuild != nil {
			autoBuild = *c.Spec.AutoBuild
		}
		if c.Spec.AutoDeploy != nil {
			autoDeploy = *c.Spec.AutoDeploy
		}
	}
	if projectName == "" {
		projectName = label(c.Metadata.Labels, string(LabelKeyProjectName))
	}
	if componentType == "" {
		componentType = label(c.Metadata.Labels, string(LabelKeyComponentType))
	}

	var status string
	if c.Status != nil {
		status = latestConditionReason(c.Status.Conditions)
	}

	return models.Component{
		UID:         derefStr(c.Metadata.Uid),
		Name:        FriendlyComponentName(c.Metadata.Name, projectName),
		ProjectName: projectName,
		DisplayName: annotation(c.Metadata.Annotations, AnnotationKeyDisplayName),
		Description: annotation(c.Metadata.Annotations, AnnotationKeyDescription),
		Type:        componentType,
		AutoDeploy:  autoDeploy,
		AutoBuild:   autoBuild,
		CreatedAt:   derefTimeRFC3339(c.Metadata.CreationTimestamp),
		Status:      status,
	}
}

// workflowRunToModel mirrors the OC condition logic the hand-rolled client
// used: Reason of WorkflowCompleted wins; otherwise WorkflowRunning sets
// "Running"; default "Pending". `Completed` flips when WorkflowCompleted has
// Status=True — watchers gate terminal transitions on this, not on
// substring-matching the Status string.
func workflowRunToModel(run gen.WorkflowRun) models.WorkflowRun {
	var componentName, projectName string
	if run.Metadata.Labels != nil {
		componentName = label(run.Metadata.Labels, string(LabelKeyComponent))
		projectName = label(run.Metadata.Labels, string(LabelKeyProject))
	}

	status := "Pending"
	completed := false
	if run.Status != nil && run.Status.Conditions != nil {
		for _, cond := range *run.Status.Conditions {
			if cond.Type == WorkflowConditionCompleted {
				if cond.Status == "True" {
					completed = true
				}
				if cond.Reason != "" {
					status = cond.Reason
					break
				}
			}
			if cond.Type == WorkflowConditionRunning && cond.Status == "True" {
				status = "Running"
			}
		}
	}

	var tasks []models.WorkflowRunTask
	if run.Status != nil && run.Status.Tasks != nil {
		tasks = make([]models.WorkflowRunTask, 0, len(*run.Status.Tasks))
		for _, t := range *run.Status.Tasks {
			tasks = append(tasks, models.WorkflowRunTask{
				Name:        t.Name,
				Phase:       derefStr(t.Phase),
				Message:     derefStr(t.Message),
				StartedAt:   derefTimeRFC3339(t.StartedAt),
				CompletedAt: derefTimeRFC3339(t.CompletedAt),
			})
		}
	}

	return models.WorkflowRun{
		Name:          run.Metadata.Name,
		Status:        status,
		StartedAt:     derefTimeRFC3339(run.Metadata.CreationTimestamp),
		ComponentName: FriendlyComponentName(componentName, projectName),
		ProjectName:   projectName,
		Completed:     completed,
		Tasks:         tasks,
	}
}

// deploymentFromReleaseBinding pulls the first HTTP external URL from the
// binding's resolved endpoints. The gen surface flattened the legacy
// `externalURLs[http]` map into a typed `ExternalURLs.Http *EndpointURL`,
// so the lookup is direct.
func deploymentFromReleaseBinding(rb gen.ReleaseBinding) models.Deployment {
	var projectName, componentName, environment, releaseName string
	if rb.Spec != nil {
		projectName = rb.Spec.Owner.ProjectName
		componentName = rb.Spec.Owner.ComponentName
		environment = rb.Spec.Environment
		releaseName = derefStr(rb.Spec.ReleaseName)
	}

	var endpointURL string
	if rb.Status != nil && rb.Status.Endpoints != nil {
		for _, ep := range *rb.Status.Endpoints {
			if ep.ExternalURLs != nil && ep.ExternalURLs.Http != nil {
				endpointURL = formatEndpointURL(ep.ExternalURLs.Http)
				break
			}
		}
	}

	var status string
	if rb.Status != nil {
		status = latestConditionReason(rb.Status.Conditions)
	}

	return models.Deployment{
		Name:          rb.Metadata.Name,
		Environment:   environment,
		ReleaseName:   releaseName,
		ComponentName: FriendlyComponentName(componentName, projectName),
		EndpointURL:   endpointURL,
		CreatedAt:     derefTimeRFC3339(rb.Metadata.CreationTimestamp),
		Status:        status,
	}
}

// formatEndpointURL renders gen.EndpointURL as scheme://host:port/path. Path
// of "" or "/" yields a trailing "/" for stable display; matches the legacy
// format the UI consumed.
func formatEndpointURL(u *gen.EndpointURL) string {
	if u == nil {
		return ""
	}
	scheme := derefStr(u.Scheme)
	path := derefStr(u.Path)
	port := 0
	if u.Port != nil {
		port = int(*u.Port)
	}
	if path == "" || path == "/" {
		return fmt.Sprintf("%s://%s:%d/", scheme, u.Host, port)
	}
	return fmt.Sprintf("%s://%s:%d%s", scheme, u.Host, port, path)
}

// envVarsToGen converts model EnvVars to gen EnvVars (gen.EnvVar.Value is *string).
func envVarsToGen(envVars []models.EnvVar) []gen.EnvVar {
	out := make([]gen.EnvVar, len(envVars))
	for i, ev := range envVars {
		v := ev.Value
		out[i] = gen.EnvVar{Key: ev.Key, Value: &v}
	}
	return out
}

// -- Component CRUD ----------------------------------------------------------

func (c *componentClient) ListComponents(ctx context.Context, orgName, projectName string, limit int, cursor string) (*models.ComponentList, error) {
	params := &gen.ListComponentsParams{}
	sel := gen.LabelSelectorParam(fmt.Sprintf("%s=%s", string(LabelKeyProject), projectName))
	params.LabelSelector = &sel
	if limit > 0 {
		l := gen.LimitParam(limit)
		params.Limit = &l
	}
	if cursor != "" {
		cur := gen.CursorParam(cursor)
		params.Cursor = &cur
	}

	resp, err := c.oc.ListComponentsWithResponse(ctx, orgName, params)
	if err != nil {
		return nil, fmt.Errorf("failed to list components: %w", err)
	}
	if resp.StatusCode() != http.StatusOK || resp.JSON200 == nil {
		return nil, handleErrorResponse(resp.StatusCode(), ErrorResponses{
			JSON400: resp.JSON400,
			JSON401: resp.JSON401,
			JSON403: resp.JSON403,
			JSON500: resp.JSON500,
		})
	}

	items := make([]models.Component, len(resp.JSON200.Items))
	for i, comp := range resp.JSON200.Items {
		items[i] = componentToModel(comp)
	}
	return &models.ComponentList{Items: items}, nil
}

func (c *componentClient) GetComponent(ctx context.Context, orgName, projectName, componentName string) (*models.Component, error) {
	k8sName := ScopedComponentName(projectName, componentName)
	resp, err := c.oc.GetComponentWithResponse(ctx, orgName, k8sName)
	if err != nil {
		return nil, fmt.Errorf("failed to get component: %w", err)
	}
	if resp.StatusCode() != http.StatusOK || resp.JSON200 == nil {
		return nil, handleErrorResponse(resp.StatusCode(), ErrorResponses{
			JSON401: resp.JSON401,
			JSON403: resp.JSON403,
			JSON404: resp.JSON404,
			JSON500: resp.JSON500,
		})
	}
	comp := componentToModel(*resp.JSON200)
	return &comp, nil
}

func (c *componentClient) CreateComponent(ctx context.Context, orgName, projectName string, req *models.CreateComponentRequest) (*models.Component, error) {
	resp, err := c.oc.CreateComponentWithResponse(ctx, orgName, buildCreateComponentBody(projectName, req))
	if err != nil {
		return nil, fmt.Errorf("failed to create component: %w", err)
	}

	switch {
	case resp.StatusCode() == http.StatusCreated && resp.JSON201 != nil:
		comp := componentToModel(*resp.JSON201)
		return &comp, nil
	case resp.StatusCode() == http.StatusConflict:
		// Idempotent on (ocOrgId, project, componentName): a 409 means the
		// component already exists, so fetch and return it. Asserted at the
		// call boundary per phase0 §1.11.
		existing, gerr := c.GetComponent(ctx, orgName, projectName, req.Name)
		if gerr != nil {
			return nil, fmt.Errorf("create component returned conflict; refetch failed: %w", gerr)
		}
		return existing, nil
	}

	return nil, handleErrorResponse(resp.StatusCode(), ErrorResponses{
		JSON400: resp.JSON400,
		JSON401: resp.JSON401,
		JSON403: resp.JSON403,
		JSON409: resp.JSON409,
		JSON500: resp.JSON500,
	})
}

// UpdateComponentWorkflowEnvVars fetches the Component, rewrites its
// `spec.workflow.parameters.environmentVariables` array, and PUTs the
// whole Component back. We do a read-modify-write rather than blasting
// the entire spec from scratch because the build workflow's other
// parameters (repository, docker, ...) are owned by the dispatch path
// and we don't want to lose them.
//
// Returns nil on 404 — a missing Component means the user is editing
// env vars before the first dispatch, which is allowed: the values will
// be stamped onto the Component when ensureOCComponent fires (see
// dispatch_service.workflowEnvVars).
func (c *componentClient) UpdateComponentWorkflowEnvVars(ctx context.Context, orgName, projectName, componentName string, envVars []models.WorkflowEnvVarRef) error {
	scopedComp := ScopedComponentName(projectName, componentName)
	getResp, err := c.oc.GetComponentWithResponse(ctx, orgName, scopedComp)
	if err != nil {
		return fmt.Errorf("failed to get component for env-var update: %w", err)
	}
	if getResp.StatusCode() == http.StatusNotFound {
		return nil
	}
	if getResp.StatusCode() != http.StatusOK || getResp.JSON200 == nil {
		return handleErrorResponse(getResp.StatusCode(), ErrorResponses{
			JSON401: getResp.JSON401,
			JSON403: getResp.JSON403,
			JSON404: getResp.JSON404,
			JSON500: getResp.JSON500,
		})
	}

	body := *getResp.JSON200
	if body.Spec == nil {
		body.Spec = &gen.ComponentSpec{}
	}
	if body.Spec.Workflow == nil {
		// No workflow on this Component — env vars can't take effect.
		// Don't synthesize one here; the dispatch path is the only place
		// that knows the builder kind/name. Surface a soft error so the
		// caller can warn.
		return fmt.Errorf("component %s/%s has no workflow; env vars cannot be applied", projectName, componentName)
	}
	var params map[string]interface{}
	if body.Spec.Workflow.Parameters != nil {
		params = cloneParameterMap(*body.Spec.Workflow.Parameters)
	} else {
		params = map[string]interface{}{}
	}
	if list := workflowEnvVarsToList(envVars); list != nil {
		params["environmentVariables"] = list
	} else {
		delete(params, "environmentVariables")
	}
	body.Spec.Workflow.Parameters = &params

	updateResp, err := c.oc.UpdateComponentWithResponse(ctx, orgName, scopedComp, gen.UpdateComponentJSONRequestBody(body))
	if err != nil {
		return fmt.Errorf("failed to update component env vars: %w", err)
	}
	if updateResp.StatusCode() != http.StatusOK && updateResp.StatusCode() != http.StatusCreated {
		return handleErrorResponse(updateResp.StatusCode(), ErrorResponses{
			JSON400: updateResp.JSON400,
			JSON401: updateResp.JSON401,
			JSON403: updateResp.JSON403,
			JSON404: updateResp.JSON404,
			JSON500: updateResp.JSON500,
		})
	}
	return nil
}

// buildCreateComponentBody mirrors the legacy hand-rolled body verbatim.
// gen's ComponentSpec.{ComponentType,Owner} are inline anonymous structs;
// we materialize them with composite literals to stay clear of pointer
// gymnastics.
func buildCreateComponentBody(projectName string, req *models.CreateComponentRequest) gen.CreateComponentJSONRequestBody {
	ann := map[string]string{
		AnnotationKeyDisplayName: req.DisplayName,
		AnnotationKeyDescription: req.Description,
	}
	ctKind := gen.ComponentSpecComponentTypeKindClusterComponentType
	body := gen.Component{
		Metadata: gen.ObjectMeta{
			Name:        ScopedComponentName(projectName, req.Name),
			Annotations: &ann,
		},
		Spec: &gen.ComponentSpec{
			AutoBuild:  &req.AutoBuild,
			AutoDeploy: &req.AutoDeploy,
			Owner: struct {
				ProjectName string `json:"projectName"`
			}{ProjectName: projectName},
			ComponentType: struct {
				Kind *gen.ComponentSpecComponentTypeKind `json:"kind,omitempty"`
				Name string                              `json:"name"`
			}{
				Kind: &ctKind,
				Name: req.Type,
			},
		},
	}

	if req.Workflow != nil {
		wfKind := gen.ComponentWorkflowConfigKindClusterWorkflow
		if req.Workflow.Kind != "" {
			k := gen.ComponentWorkflowConfigKind(req.Workflow.Kind)
			wfKind = k
		}
		wf := &gen.ComponentWorkflowConfig{
			Kind: &wfKind,
			Name: req.Workflow.Name,
		}
		if params := workflowParametersToMap(req.Workflow.Parameters); params != nil {
			wf.Parameters = &params
		}
		body.Spec.Workflow = wf
	}
	return body
}

// workflowParametersToMap shapes our typed CreateComponentRequest.Workflow.Parameters
// into the dynamic `map[string]interface{}` gen uses. Returns nil when no
// fields are set so we don't leak an empty `parameters` object.
func workflowParametersToMap(p *models.ComponentWorkflowParameters) map[string]interface{} {
	if p == nil {
		return nil
	}
	out := map[string]interface{}{}
	if p.Repository != nil {
		repo := map[string]interface{}{}
		if p.Repository.URL != "" {
			repo["url"] = p.Repository.URL
		}
		if p.Repository.SecretRef != "" {
			repo["secretRef"] = p.Repository.SecretRef
		}
		if p.Repository.AppPath != "" {
			repo["appPath"] = p.Repository.AppPath
		}
		if p.Repository.Revision != nil {
			rev := map[string]interface{}{}
			if p.Repository.Revision.Branch != "" {
				rev["branch"] = p.Repository.Revision.Branch
			}
			if p.Repository.Revision.Commit != "" {
				rev["commit"] = p.Repository.Revision.Commit
			}
			if len(rev) > 0 {
				repo["revision"] = rev
			}
		}
		if len(repo) > 0 {
			out["repository"] = repo
		}
	}
	if p.Docker != nil {
		docker := map[string]interface{}{}
		if p.Docker.Context != "" {
			docker["context"] = p.Docker.Context
		}
		if p.Docker.FilePath != "" {
			docker["filePath"] = p.Docker.FilePath
		}
		if len(docker) > 0 {
			out["docker"] = docker
		}
	}
	if envVars := workflowEnvVarsToList(p.EnvironmentVariables); envVars != nil {
		out["environmentVariables"] = envVars
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// workflowEnvVarsToList shapes the typed env-var slice into the
// `[]map[string]interface{}` form OC's dockerfile-builder
// `environmentVariables` parameter expects. The build workflow's
// `generate-workload-cr` step splices this into the generated Workload's
// `spec.container.env`, so the auto-deployed pod picks the values up.
// Returns nil when there's nothing to inject so the JSON body stays
// `[]`-clean (the workflow's schema default).
func workflowEnvVarsToList(envVars []models.WorkflowEnvVarRef) []map[string]interface{} {
	if len(envVars) == 0 {
		return nil
	}
	out := make([]map[string]interface{}, 0, len(envVars))
	for _, ev := range envVars {
		entry := map[string]interface{}{"key": ev.Key}
		switch {
		case ev.ValueFrom != nil && ev.ValueFrom.SecretKeyRef != nil:
			entry["valueFrom"] = map[string]interface{}{
				"secretKeyRef": map[string]interface{}{
					"name": ev.ValueFrom.SecretKeyRef.Name,
					"key":  ev.ValueFrom.SecretKeyRef.Key,
				},
			}
		default:
			entry["value"] = ev.Value
		}
		out = append(out, entry)
	}
	return out
}

// -- Deployments (read-only) -------------------------------------------------

func (c *componentClient) ListDeployments(ctx context.Context, orgName, projectName, componentName string) (*models.DeploymentList, error) {
	scopedComp := ScopedComponentName(projectName, componentName)
	componentQ := gen.ComponentQueryParam(scopedComp)
	resp, err := c.oc.ListReleaseBindingsWithResponse(ctx, orgName, &gen.ListReleaseBindingsParams{
		Component: &componentQ,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to list release bindings: %w", err)
	}
	if resp.StatusCode() != http.StatusOK || resp.JSON200 == nil {
		return nil, handleErrorResponse(resp.StatusCode(), ErrorResponses{
			JSON401: resp.JSON401,
			JSON403: resp.JSON403,
			JSON500: resp.JSON500,
		})
	}

	items := make([]models.Deployment, len(resp.JSON200.Items))
	for i, rb := range resp.JSON200.Items {
		items[i] = deploymentFromReleaseBinding(rb)
	}
	return &models.DeploymentList{Items: items}, nil
}

// -- WorkflowRuns (builds + coding-agent) ------------------------------------

func (c *componentClient) TriggerBuild(ctx context.Context, orgName, projectName, componentName string) (*models.WorkflowRun, error) {
	return c.triggerBuildInner(ctx, orgName, projectName, componentName, "")
}

func (c *componentClient) TriggerBuildAtCommit(ctx context.Context, orgName, projectName, componentName, commitSHA string) (*models.WorkflowRun, error) {
	return c.triggerBuildInner(ctx, orgName, projectName, componentName, commitSHA)
}

// triggerBuildInner fetches the Component to grab its declared Workflow
// (kind+name+parameters) and POSTs a fresh WorkflowRun. When commitSHA is
// non-empty it's stamped onto `parameters.repository.revision.commit` so the
// build pod clones the exact merge SHA — webhook-driven builds set this
// from `pull_request.closed`'s merge_commit_sha.
func (c *componentClient) triggerBuildInner(ctx context.Context, orgName, projectName, componentName, commitSHA string) (*models.WorkflowRun, error) {
	scopedComp := ScopedComponentName(projectName, componentName)

	compResp, err := c.oc.GetComponentWithResponse(ctx, orgName, scopedComp)
	if err != nil {
		return nil, fmt.Errorf("failed to get component for build trigger: %w", err)
	}
	if compResp.StatusCode() != http.StatusOK || compResp.JSON200 == nil {
		return nil, handleErrorResponse(compResp.StatusCode(), ErrorResponses{
			JSON401: compResp.JSON401,
			JSON403: compResp.JSON403,
			JSON404: compResp.JSON404,
			JSON500: compResp.JSON500,
		})
	}

	wf := buildWorkflowFromComponent(compResp.JSON200, commitSHA)
	if wf.Name == "" {
		return nil, fmt.Errorf("trigger build: component %s/%s has no workflow configured", projectName, componentName)
	}

	runName := fmt.Sprintf("%s-%d", scopedComp, time.Now().UnixMilli())
	labels := map[string]string{
		string(LabelKeyComponent): scopedComp,
		string(LabelKeyProject):   projectName,
	}
	body := gen.CreateWorkflowRunJSONRequestBody{
		Metadata: gen.ObjectMeta{
			Name:   runName,
			Labels: &labels,
		},
		Spec: &gen.WorkflowRunSpec{Workflow: wf},
	}

	return c.createWorkflowRun(ctx, orgName, body, "trigger build")
}

// buildWorkflowFromComponent lifts the Component's declared Workflow into a
// WorkflowRunConfig, optionally injecting commitSHA on the parameters map.
// Parameters is shaped as `map[string]interface{}` end-to-end on the gen
// side; we deep-copy the slice keys we touch to avoid mutating shared maps
// returned by the cache layer (which would race with concurrent triggers).
func buildWorkflowFromComponent(comp *gen.Component, commitSHA string) gen.WorkflowRunConfig {
	if comp == nil || comp.Spec == nil || comp.Spec.Workflow == nil {
		return gen.WorkflowRunConfig{}
	}
	src := comp.Spec.Workflow
	runKind := gen.WorkflowRunConfigKindClusterWorkflow
	if src.Kind != nil {
		runKind = gen.WorkflowRunConfigKind(*src.Kind)
	}

	out := gen.WorkflowRunConfig{
		Kind: &runKind,
		Name: src.Name,
	}
	if src.Parameters == nil {
		return out
	}

	params := cloneParameterMap(*src.Parameters)
	if commitSHA != "" {
		injectCommitSHA(params, commitSHA)
	}
	out.Parameters = &params
	return out
}

// injectCommitSHA stamps params["repository"]["revision"]["commit"] = sha,
// materialising the nested maps if missing. The branch stays untouched so
// OC's clone path keeps working.
func injectCommitSHA(params map[string]interface{}, sha string) {
	repo, ok := params["repository"].(map[string]interface{})
	if !ok {
		repo = map[string]interface{}{}
		params["repository"] = repo
	}
	rev, ok := repo["revision"].(map[string]interface{})
	if !ok {
		rev = map[string]interface{}{}
		repo["revision"] = rev
	}
	rev["commit"] = sha
}

// cloneParameterMap deep-clones a map[string]interface{} value tree. Only
// handles the shapes we put into Workflow.Parameters (nested maps + scalars
// + string slices); other shapes pass through by reference.
func cloneParameterMap(in map[string]interface{}) map[string]interface{} {
	out := make(map[string]interface{}, len(in))
	for k, v := range in {
		switch tv := v.(type) {
		case map[string]interface{}:
			out[k] = cloneParameterMap(tv)
		case []string:
			cp := append([]string(nil), tv...)
			out[k] = cp
		default:
			out[k] = v
		}
	}
	return out
}

// TriggerCodingAgent creates a WorkflowRun of ClusterWorkflow
// `app-factory-coding-agent`. Each call creates a fresh run; idempotency is
// the caller's responsibility (see DispatchService.dispatchOne which gates
// on task.LastCodingAgentRunName + DispatchedAt).
//
// NOTE: deliberately NOT setting `openchoreo.dev/component` /
// `openchoreo.dev/project` labels. OC validates the
// ClusterWorkflow ↔ ClusterComponentType allowed-workflow pair when a
// WorkflowRun carries the `openchoreo.dev/component` label, which would
// reject `app-factory-coding-agent` because the user's component is
// `deployment/service` (allowed only the builder ClusterWorkflows). The
// agent pod has no need to be tied to the user's Component for OC's
// purposes — the project + component identifiers flow in via the
// `parameters.task.*` fields that the runner reads. The `app-factory.*`
// label catalog carries them for the BFF watcher instead.
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

	labels := map[string]string{
		string(LabelKeyAppFactoryCodingAgentTask): params.TaskID,
		string(LabelKeyAppFactoryProject):         params.ProjectName,
		string(LabelKeyAppFactoryComponent):       scopedComp,
	}

	wfKind := gen.WorkflowRunConfigKindClusterWorkflow
	parameters := codingAgentParameters(params)
	body := gen.CreateWorkflowRunJSONRequestBody{
		Metadata: gen.ObjectMeta{
			Name:   runName,
			Labels: &labels,
		},
		Spec: &gen.WorkflowRunSpec{
			Workflow: gen.WorkflowRunConfig{
				Kind:       &wfKind,
				Name:       "app-factory-coding-agent",
				Parameters: &parameters,
			},
		},
	}

	return c.createWorkflowRun(ctx, params.OrgName, body, "trigger coding-agent")
}

// codingAgentParameters builds the `parameters.*` map that the
// app-factory-coding-agent ClusterWorkflow's openAPIV3Schema expects. The
// runner image reads ASDLC_* env vars substituted from these keys.
func codingAgentParameters(p CodingAgentParams) map[string]interface{} {
	return map[string]interface{}{
		"task": map[string]interface{}{
			"id":            p.TaskID,
			"orgId":         p.OrgName,
			"projectId":     p.ProjectName,
			"componentName": p.ComponentName,
			"prompt":        p.Prompt,
		},
		"repository": map[string]interface{}{
			"url": p.RepoURL,
			"identity": map[string]interface{}{
				"name":  p.IdentityName,
				"email": p.IdentityEmail,
				"login": p.IdentityLogin,
			},
		},
		"bff": map[string]interface{}{
			"bearer":      p.Bearer,
			"platformUrl": p.PlatformURL,
		},
		"gitService": map[string]interface{}{
			"url": p.GitServiceURL,
		},
	}
}

// createWorkflowRun is the shared POST path for both trigger flows. opName
// goes into the network-error wrap to keep slog logs distinguishable
// (trigger build / trigger coding-agent).
func (c *componentClient) createWorkflowRun(ctx context.Context, orgName string, body gen.CreateWorkflowRunJSONRequestBody, opName string) (*models.WorkflowRun, error) {
	resp, err := c.oc.CreateWorkflowRunWithResponse(ctx, orgName, body)
	if err != nil {
		return nil, fmt.Errorf("failed to %s: %w", opName, err)
	}
	if resp.StatusCode() != http.StatusCreated && resp.StatusCode() != http.StatusOK {
		return nil, handleErrorResponse(resp.StatusCode(), ErrorResponses{
			JSON400: resp.JSON400,
			JSON401: resp.JSON401,
			JSON403: resp.JSON403,
			JSON404: resp.JSON404,
			JSON500: resp.JSON500,
		})
	}
	if resp.JSON201 == nil {
		return nil, fmt.Errorf("%s: empty WorkflowRun in response", opName)
	}
	run := workflowRunToModel(*resp.JSON201)
	return &run, nil
}

func (c *componentClient) ListWorkflowRuns(ctx context.Context, orgName, projectName, componentName string, limit int, cursor string) (*models.WorkflowRunList, error) {
	scopedComp := ScopedComponentName(projectName, componentName)
	sel := gen.LabelSelectorParam(fmt.Sprintf("%s=%s", string(LabelKeyComponent), scopedComp))
	params := &gen.ListWorkflowRunsParams{LabelSelector: &sel}
	if limit > 0 {
		l := gen.LimitParam(limit)
		params.Limit = &l
	}
	if cursor != "" {
		cur := gen.CursorParam(cursor)
		params.Cursor = &cur
	}

	resp, err := c.oc.ListWorkflowRunsWithResponse(ctx, orgName, params)
	if err != nil {
		return nil, fmt.Errorf("failed to list workflow runs: %w", err)
	}
	if resp.StatusCode() != http.StatusOK || resp.JSON200 == nil {
		return nil, handleErrorResponse(resp.StatusCode(), ErrorResponses{
			JSON400: resp.JSON400,
			JSON401: resp.JSON401,
			JSON403: resp.JSON403,
			JSON500: resp.JSON500,
		})
	}

	items := make([]models.WorkflowRun, len(resp.JSON200.Items))
	for i, run := range resp.JSON200.Items {
		items[i] = workflowRunToModel(run)
	}
	return &models.WorkflowRunList{Items: items}, nil
}

func (c *componentClient) GetWorkflowRun(ctx context.Context, orgName, runName string) (*models.WorkflowRun, error) {
	resp, err := c.oc.GetWorkflowRunWithResponse(ctx, orgName, runName)
	if err != nil {
		return nil, fmt.Errorf("failed to get workflow run: %w", err)
	}
	if resp.StatusCode() != http.StatusOK || resp.JSON200 == nil {
		return nil, handleErrorResponse(resp.StatusCode(), ErrorResponses{
			JSON401: resp.JSON401,
			JSON403: resp.JSON403,
			JSON404: resp.JSON404,
			JSON500: resp.JSON500,
		})
	}
	run := workflowRunToModel(*resp.JSON200)
	return &run, nil
}

