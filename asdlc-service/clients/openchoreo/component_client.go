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
	// UpdateComponentWorkflowEnvVars writes per-component env vars onto each
	// of the component's ReleaseBindings at
	// `spec.workloadOverrides.container.env`. Per-env (one RB per
	// environment) so OC's controller renders the values straight into the
	// pod spec on the next reconcile — no rebuild required, matching how
	// PE-managed components (app-factory-api, agent-manager-service, etc.)
	// carry their env. ReleaseBindings are listed by component label and
	// each is updated independently; if no RBs exist yet (pre-first-deploy)
	// the call is a soft no-op and the caller is expected to retry once
	// the first build has produced RBs.
	UpdateComponentWorkflowEnvVars(ctx context.Context, orgName, projectName, componentName string, envVars []models.WorkflowEnvVarRef) error

	// UpdateComponentWorkflowFiles writes per-component literal files onto
	// each of the component's ReleaseBindings at
	// `spec.workloadOverrides.container.files`. Used by the runtime-config
	// pipeline to drop `env-config.js` (and any other literal file) into
	// the pod via an OC-rendered ConfigMap mounted at the declared
	// mountPath — no rebuild needed. As with UpdateComponentWorkflowEnvVars,
	// when no ReleaseBindings exist yet the call is a soft no-op and the
	// caller is expected to retry after the first build produces RBs.
	UpdateComponentWorkflowFiles(ctx context.Context, orgName, projectName, componentName string, files []models.WorkflowFileVar) error

	// DeleteComponent removes the Component CR. OC's controller GCs the
	// chain (Component → ReleaseBinding → RenderedRelease → Deployment /
	// Service / HTTPRoute) via k8s ownerReferences. NOTE: trait-emitted
	// resources (Backend, RestApi) DO NOT carry owner refs back to the
	// Component (the canonical `api-configuration` trait template's
	// `creates` block omits them — see deployments/manifests/api-platform/
	// api-configuration-trait.yaml). Cascade is therefore PARTIAL; the
	// dp-namespace may retain orphaned Backend/RestApi resources. The
	// caller (designService.DeleteComponent) emits an audit log entry
	// reflecting that gap and a follow-up sweep is required to clean
	// them up. Returns ErrComponentNotFound when the Component does not
	// exist (idempotent — 404 is treated as success).
	DeleteComponent(ctx context.Context, orgName, projectName, componentName string) error

	// UpdateComponentTraits replaces `spec.traits` on an existing Component
	// with the supplied slice. Passing an empty slice clears traits.
	// Returns ErrComponentNotFound when the Component does not exist (the
	// caller decides whether to recreate or no-op). Used by trait_sync.go
	// when a user toggles `api.security` on `design.md` after first deploy.
	UpdateComponentTraits(ctx context.Context, orgName, projectName, componentName string, traits []models.ComponentTrait) error

	// UpdateComponentTraitEnvironmentConfigs writes per-environment trait
	// configs onto each of the component's ReleaseBindings at
	// `spec.traitEnvironmentConfigs`. Configs is keyed by trait instance
	// name; the value is the parameters block (e.g. `{"jwtAuth": {"enabled": true}}`).
	// Passing an empty map clears the field. When no RBs exist yet (pre-
	// first-deploy) the call is a soft no-op — the caller retries via the
	// trait-sync watcher once the deploy chain catches up.
	UpdateComponentTraitEnvironmentConfigs(ctx context.Context, orgName, projectName, componentName string, configs map[string]map[string]interface{}) error

	// Deploy (read-only — auto-deploy on the Component drives the chain)
	ListDeployments(ctx context.Context, orgName, projectName, componentName string) (*models.DeploymentList, error)

	// Build (workflow runs). `runName` is the WorkflowRun metadata.name; if
	// empty the OC client auto-generates one via NewBuildRunName. Callers
	// that need to know the name ahead of time (so they can stage a
	// per-WorkflowRun build Secret) MUST pass it.
	TriggerBuild(ctx context.Context, orgName, projectName, componentName, runName string) (*models.WorkflowRun, error)
	// TriggerBuildAtCommit creates a WorkflowRun pinned to commitSHA via
	// params.repository.revision.commit. Mirrors agent-manager's pattern at
	// agent-manager-service/clients/openchoreosvc/client/builds.go:71-85.
	// See TriggerBuild for the `runName` contract.
	TriggerBuildAtCommit(ctx context.Context, orgName, projectName, componentName, commitSHA, runName string) (*models.WorkflowRun, error)
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
	// AnthropicSecretRef is the name of the per-org K8s Secret in
	// workflows-<OrgName> carrying ANTHROPIC_API_KEY. Materialised by
	// git-service in the dispatch pre-flight (see
	// gitservice.Client.ApplyAnthropicWPSecret). The ClusterWorkflow wires
	// it into the pod via `parameters.anthropic.secretRef` →
	// `secretKeyRef.name`. See docs/design/anthropic-key-dual-token.md §5.
	AnthropicSecretRef string
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

// UpdateComponentWorkflowEnvVars lists the component's ReleaseBindings
// and writes the env vars onto each one at
// `spec.workloadOverrides.container.env`. One RB per environment — OC's
// controller renders the value into the pod spec on the next reconcile,
// so changing env vars no longer requires a rebuild.
//
// When no ReleaseBindings exist yet (the first build hasn't produced
// one), the call is a soft no-op: the caller is expected to retry after
// a successful deploy. An empty `envVars` slice clears any previously
// set env block on each binding.
func (c *componentClient) UpdateComponentWorkflowEnvVars(ctx context.Context, orgName, projectName, componentName string, envVars []models.WorkflowEnvVarRef) error {
	scopedComp := ScopedComponentName(projectName, componentName)
	componentQ := gen.ComponentQueryParam(scopedComp)
	listResp, err := c.oc.ListReleaseBindingsWithResponse(ctx, orgName, &gen.ListReleaseBindingsParams{
		Component: &componentQ,
	})
	if err != nil {
		return fmt.Errorf("failed to list release bindings for env-var update: %w", err)
	}
	if listResp.StatusCode() != http.StatusOK || listResp.JSON200 == nil {
		return handleErrorResponse(listResp.StatusCode(), ErrorResponses{
			JSON401: listResp.JSON401,
			JSON403: listResp.JSON403,
			JSON500: listResp.JSON500,
		})
	}

	rbs := listResp.JSON200.Items
	if len(rbs) == 0 {
		// First build hasn't produced a ReleaseBinding yet — nothing to
		// patch. The caller retries once the deploy chain catches up.
		return nil
	}

	envList := workflowEnvVarRefsToGen(envVars)
	for _, rb := range rbs {
		if rb.Spec == nil {
			rb.Spec = &gen.ReleaseBindingSpec{}
		}
		if rb.Spec.WorkloadOverrides == nil {
			rb.Spec.WorkloadOverrides = &gen.WorkloadOverrides{}
		}
		if rb.Spec.WorkloadOverrides.Container == nil {
			rb.Spec.WorkloadOverrides.Container = &gen.ContainerOverride{}
		}
		rb.Spec.WorkloadOverrides.Container.Env = envList

		updResp, uerr := c.oc.UpdateReleaseBindingWithResponse(ctx, orgName, gen.ReleaseBindingNameParam(rb.Metadata.Name), gen.UpdateReleaseBindingJSONRequestBody(rb))
		if uerr != nil {
			return fmt.Errorf("failed to update release binding %s: %w", rb.Metadata.Name, uerr)
		}
		if updResp.StatusCode() != http.StatusOK && updResp.StatusCode() != http.StatusCreated {
			return handleErrorResponse(updResp.StatusCode(), ErrorResponses{
				JSON400: updResp.JSON400,
				JSON401: updResp.JSON401,
				JSON403: updResp.JSON403,
				JSON404: updResp.JSON404,
				JSON500: updResp.JSON500,
			})
		}
	}
	return nil
}

// UpdateComponentWorkflowFiles lists the component's ReleaseBindings and
// writes the literal files onto each one at
// `spec.workloadOverrides.container.files`. Per-env (one RB per
// environment) so OC's controller materialises a ConfigMap mounted at
// the declared mountPath on the next reconcile — no rebuild required.
//
// When no ReleaseBindings exist yet (the first build hasn't produced
// one), the call is a soft no-op: the caller is expected to retry after
// a successful deploy. An empty `files` slice clears any previously set
// files block on each binding.
func (c *componentClient) UpdateComponentWorkflowFiles(ctx context.Context, orgName, projectName, componentName string, files []models.WorkflowFileVar) error {
	scopedComp := ScopedComponentName(projectName, componentName)
	componentQ := gen.ComponentQueryParam(scopedComp)
	listResp, err := c.oc.ListReleaseBindingsWithResponse(ctx, orgName, &gen.ListReleaseBindingsParams{
		Component: &componentQ,
	})
	if err != nil {
		return fmt.Errorf("failed to list release bindings for file update: %w", err)
	}
	if listResp.StatusCode() != http.StatusOK || listResp.JSON200 == nil {
		return handleErrorResponse(listResp.StatusCode(), ErrorResponses{
			JSON401: listResp.JSON401,
			JSON403: listResp.JSON403,
			JSON500: listResp.JSON500,
		})
	}

	rbs := listResp.JSON200.Items
	if len(rbs) == 0 {
		// First build hasn't produced a ReleaseBinding yet — soft no-op.
		return nil
	}

	fileList := workflowFileVarsToGen(files)
	for _, rb := range rbs {
		if rb.Spec == nil {
			rb.Spec = &gen.ReleaseBindingSpec{}
		}
		if rb.Spec.WorkloadOverrides == nil {
			rb.Spec.WorkloadOverrides = &gen.WorkloadOverrides{}
		}
		if rb.Spec.WorkloadOverrides.Container == nil {
			rb.Spec.WorkloadOverrides.Container = &gen.ContainerOverride{}
		}
		rb.Spec.WorkloadOverrides.Container.Files = fileList

		updResp, uerr := c.oc.UpdateReleaseBindingWithResponse(ctx, orgName, gen.ReleaseBindingNameParam(rb.Metadata.Name), gen.UpdateReleaseBindingJSONRequestBody(rb))
		if uerr != nil {
			return fmt.Errorf("failed to update release binding %s files: %w", rb.Metadata.Name, uerr)
		}
		if updResp.StatusCode() != http.StatusOK && updResp.StatusCode() != http.StatusCreated {
			return handleErrorResponse(updResp.StatusCode(), ErrorResponses{
				JSON400: updResp.JSON400,
				JSON401: updResp.JSON401,
				JSON403: updResp.JSON403,
				JSON404: updResp.JSON404,
				JSON500: updResp.JSON500,
			})
		}
	}
	return nil
}

// workflowFileVarsToGen converts the BFF-internal file model into
// the gen.FileVar slice for ReleaseBinding workloadOverrides. An empty
// `files` returns a pointer to an empty slice so the server-side patch
// clears the field rather than leaving stale values in place.
func workflowFileVarsToGen(files []models.WorkflowFileVar) *[]gen.FileVar {
	out := make([]gen.FileVar, 0, len(files))
	for _, f := range files {
		v := f.Value
		out = append(out, gen.FileVar{
			Key:       f.Key,
			MountPath: f.MountPath,
			Value:     &v,
		})
	}
	return &out
}

// DeleteComponent issues DELETE against OC's Component endpoint. Returns
// nil on 200/204 OR 404 (idempotent — deleting a non-existent component
// is a success). OC's controller cascades the chain via k8s ownerRefs;
// trait-emitted Backend / RestApi resources are NOT covered by that
// cascade (see interface comment), so callers must surface that gap to
// the operator. The Component CR's RB list is GC'd by OC itself — the
// BFF doesn't need to delete each RB individually.
func (c *componentClient) DeleteComponent(ctx context.Context, orgName, projectName, componentName string) error {
	scopedComp := ScopedComponentName(projectName, componentName)
	resp, err := c.oc.DeleteComponentWithResponse(ctx, orgName, gen.ComponentNameParam(scopedComp))
	if err != nil {
		return fmt.Errorf("failed to delete component: %w", err)
	}
	switch resp.StatusCode() {
	case http.StatusOK, http.StatusNoContent, http.StatusAccepted:
		return nil
	case http.StatusNotFound:
		// Idempotent — caller's intent is satisfied.
		return nil
	}
	return handleErrorResponse(resp.StatusCode(), ErrorResponses{
		JSON401: resp.JSON401,
		JSON403: resp.JSON403,
		JSON404: resp.JSON404,
		JSON500: resp.JSON500,
	})
}

// UpdateComponentTraits replaces spec.traits on the named Component. GET-
// then-PUT to satisfy OC's full-object update semantics. Pass an empty
// slice to clear traits.
func (c *componentClient) UpdateComponentTraits(ctx context.Context, orgName, projectName, componentName string, traits []models.ComponentTrait) error {
	scopedComp := ScopedComponentName(projectName, componentName)
	getResp, err := c.oc.GetComponentWithResponse(ctx, orgName, scopedComp)
	if err != nil {
		return fmt.Errorf("failed to get component for traits update: %w", err)
	}
	if getResp.StatusCode() != http.StatusOK || getResp.JSON200 == nil {
		return handleErrorResponse(getResp.StatusCode(), ErrorResponses{
			JSON401: getResp.JSON401,
			JSON403: getResp.JSON403,
			JSON404: getResp.JSON404,
			JSON500: getResp.JSON500,
		})
	}
	comp := *getResp.JSON200
	if comp.Spec == nil {
		comp.Spec = &gen.ComponentSpec{}
	}
	comp.Spec.Traits = componentTraitsToGen(traits)

	updResp, err := c.oc.UpdateComponentWithResponse(ctx, orgName, gen.ComponentNameParam(scopedComp), gen.UpdateComponentJSONRequestBody(comp))
	if err != nil {
		return fmt.Errorf("failed to update component traits: %w", err)
	}
	if updResp.StatusCode() != http.StatusOK && updResp.StatusCode() != http.StatusCreated {
		return handleErrorResponse(updResp.StatusCode(), ErrorResponses{
			JSON400: updResp.JSON400,
			JSON401: updResp.JSON401,
			JSON403: updResp.JSON403,
			JSON404: updResp.JSON404,
			JSON500: updResp.JSON500,
		})
	}
	return nil
}

// UpdateComponentTraitEnvironmentConfigs iterates the Component's
// ReleaseBindings and writes the supplied trait-instance keyed configs
// onto each one's `spec.traitEnvironmentConfigs`. Existing entries are
// preserved when not named in `configs` (the typical add-trait case
// shouldn't strip other traits' env configs). Pass a nil/empty value
// for an instance to clear that instance's config.
func (c *componentClient) UpdateComponentTraitEnvironmentConfigs(ctx context.Context, orgName, projectName, componentName string, configs map[string]map[string]interface{}) error {
	scopedComp := ScopedComponentName(projectName, componentName)
	componentQ := gen.ComponentQueryParam(scopedComp)
	listResp, err := c.oc.ListReleaseBindingsWithResponse(ctx, orgName, &gen.ListReleaseBindingsParams{
		Component: &componentQ,
	})
	if err != nil {
		return fmt.Errorf("failed to list release bindings for trait env config update: %w", err)
	}
	if listResp.StatusCode() != http.StatusOK || listResp.JSON200 == nil {
		return handleErrorResponse(listResp.StatusCode(), ErrorResponses{
			JSON401: listResp.JSON401,
			JSON403: listResp.JSON403,
			JSON500: listResp.JSON500,
		})
	}

	rbs := listResp.JSON200.Items
	if len(rbs) == 0 {
		// First build hasn't produced a ReleaseBinding yet — soft no-op.
		// The trait_sync watcher will retry once the deploy chain catches up.
		return nil
	}

	for _, rb := range rbs {
		if rb.Spec == nil {
			rb.Spec = &gen.ReleaseBindingSpec{}
		}
		// Merge: preserve any pre-existing instance keys we don't touch.
		var merged map[string]interface{}
		if rb.Spec.TraitEnvironmentConfigs != nil {
			merged = *rb.Spec.TraitEnvironmentConfigs
		} else {
			merged = map[string]interface{}{}
		}
		for inst, params := range configs {
			if len(params) == 0 {
				delete(merged, inst)
				continue
			}
			merged[inst] = cloneParameterMap(params)
		}
		rb.Spec.TraitEnvironmentConfigs = &merged

		updResp, uerr := c.oc.UpdateReleaseBindingWithResponse(ctx, orgName, gen.ReleaseBindingNameParam(rb.Metadata.Name), gen.UpdateReleaseBindingJSONRequestBody(rb))
		if uerr != nil {
			return fmt.Errorf("failed to update release binding %s trait env config: %w", rb.Metadata.Name, uerr)
		}
		if updResp.StatusCode() != http.StatusOK && updResp.StatusCode() != http.StatusCreated {
			return handleErrorResponse(updResp.StatusCode(), ErrorResponses{
				JSON400: updResp.JSON400,
				JSON401: updResp.JSON401,
				JSON403: updResp.JSON403,
				JSON404: updResp.JSON404,
				JSON500: updResp.JSON500,
			})
		}
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
	if traits := componentTraitsToGen(req.Traits); traits != nil {
		body.Spec.Traits = traits
	}
	return body
}

// componentTraitsToGen converts the BFF-internal slice into the gen shape.
// Returns nil for an empty input so we don't stamp an empty traits array
// onto Components without API security configured.
func componentTraitsToGen(traits []models.ComponentTrait) *[]gen.ComponentTrait {
	if len(traits) == 0 {
		return nil
	}
	out := make([]gen.ComponentTrait, 0, len(traits))
	for _, t := range traits {
		entry := gen.ComponentTrait{
			InstanceName: t.InstanceName,
			Name:         t.Name,
		}
		if t.Kind != "" {
			k := gen.ComponentTraitKind(t.Kind)
			entry.Kind = &k
		}
		if len(t.Parameters) > 0 {
			p := cloneParameterMap(t.Parameters)
			entry.Parameters = &p
		}
		out = append(out, entry)
	}
	return &out
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
	if len(out) == 0 {
		return nil
	}
	return out
}

// workflowEnvVarRefsToGen converts the BFF-internal env-var model into
// the gen.EnvVar slice that goes onto a ReleaseBinding's
// `spec.workloadOverrides.container.env`. An empty `envVars` returns a
// pointer to an empty slice so the server-side patch clears the field
// rather than leaving stale values in place.
func workflowEnvVarRefsToGen(envVars []models.WorkflowEnvVarRef) *[]gen.EnvVar {
	out := make([]gen.EnvVar, 0, len(envVars))
	for _, ev := range envVars {
		entry := gen.EnvVar{Key: ev.Key}
		if ev.ValueFrom != nil && ev.ValueFrom.SecretKeyRef != nil {
			name := ev.ValueFrom.SecretKeyRef.Name
			key := ev.ValueFrom.SecretKeyRef.Key
			entry.ValueFrom = &gen.EnvVarValueFrom{
				SecretKeyRef: &struct {
					Key  *string `json:"key,omitempty"`
					Name *string `json:"name,omitempty"`
				}{
					Key:  &key,
					Name: &name,
				},
			}
		} else {
			v := ev.Value
			entry.Value = &v
		}
		out = append(out, entry)
	}
	return &out
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

func (c *componentClient) TriggerBuild(ctx context.Context, orgName, projectName, componentName, runName string) (*models.WorkflowRun, error) {
	return c.triggerBuildInner(ctx, orgName, projectName, componentName, "", runName)
}

func (c *componentClient) TriggerBuildAtCommit(ctx context.Context, orgName, projectName, componentName, commitSHA, runName string) (*models.WorkflowRun, error) {
	return c.triggerBuildInner(ctx, orgName, projectName, componentName, commitSHA, runName)
}

// triggerBuildInner fetches the Component to grab its declared Workflow
// (kind+name+parameters) and POSTs a fresh WorkflowRun. When commitSHA is
// non-empty it's stamped onto `parameters.repository.revision.commit` so the
// build pod clones the exact merge SHA — webhook-driven builds set this
// from `pull_request.closed`'s merge_commit_sha.
//
// When runName is empty the BFF gets a fresh NewBuildRunName-shaped name —
// retained for tests / call sites that don't need to pre-stage anything.
// Production callers (dispatch path, console "Build" button) pass runName
// because they staged the per-WorkflowRun build Secret with that name
// upfront.
func (c *componentClient) triggerBuildInner(ctx context.Context, orgName, projectName, componentName, commitSHA, runName string) (*models.WorkflowRun, error) {
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

	if runName == "" {
		runName = NewBuildRunName(projectName, componentName)
	}
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
	// Force-blank repository.secretRef. App Factory delivers the per-build
	// credential by pre-staging a K8s Secret named
	// `<workflowRunName>-git-secret` in workflows-<orgID> (see
	// docs/design/build-credential-injection.md); the upstream
	// dockerfile-builder workflow's externalRefs lookup is skipped when
	// secretRef is empty (openchoreo internal/controller/workflowrun/
	// externalref.go:41-44) and its git-secret ExternalSecret resource is
	// gated on `secretRef != ""`. Legacy Components may still have a
	// non-empty secretRef stored from the SecretReference-era flow — wipe
	// it at trigger time so the workflow never tries to resolve it.
	blankRepoSecretRef(params)
	out.Parameters = &params
	return out
}

// blankRepoSecretRef sets params["repository"]["secretRef"] = "", creating
// the nested map if needed. No-op for components that never had a
// repository block.
func blankRepoSecretRef(params map[string]interface{}) {
	repo, ok := params["repository"].(map[string]interface{})
	if !ok {
		return
	}
	repo["secretRef"] = ""
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
		"anthropic": map[string]interface{}{
			"secretRef": p.AnthropicSecretRef,
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

