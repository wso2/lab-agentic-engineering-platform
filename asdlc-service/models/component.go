package models

// -- Component ---------------------------------------------------------------

type Component struct {
	UID         string `json:"uid,omitempty"`
	Name        string `json:"name"`
	ProjectName string `json:"projectName,omitempty"`
	DisplayName string `json:"displayName,omitempty"`
	Description string `json:"description,omitempty"`
	Type        string `json:"type,omitempty"`
	AutoDeploy  bool   `json:"autoDeploy,omitempty"`
	AutoBuild   bool   `json:"autoBuild,omitempty"`
	CreatedAt   string `json:"createdAt,omitempty"`
	Status      string `json:"status,omitempty"`
}

type ComponentList struct {
	Items []Component `json:"items"`
}

// -- Create Component --------------------------------------------------------

type WorkflowRevision struct {
	Branch string `json:"branch,omitempty"`
	Commit string `json:"commit,omitempty"`
}

type WorkflowRepository struct {
	URL       string            `json:"url,omitempty"`
	SecretRef string            `json:"secretRef,omitempty"`
	Revision  *WorkflowRevision `json:"revision,omitempty"`
	AppPath   string            `json:"appPath,omitempty"`
}

type DockerParameters struct {
	Context  string `json:"context,omitempty"`
	FilePath string `json:"filePath,omitempty"`
}

// WorkflowEnvVarRef is a Workload-style env entry passed into the
// dockerfile-builder ClusterWorkflow's `environmentVariables` parameter.
// The build's `generate-workload-cr` step splices these into
// `Workload.spec.container.env` so the auto-deployed pod picks them up.
// Either Value or ValueFrom must be set, not both.
type WorkflowEnvVarRef struct {
	Key       string                  `json:"key"`
	Value     string                  `json:"value,omitempty"`
	ValueFrom *WorkflowEnvVarValueRef `json:"valueFrom,omitempty"`
}

type WorkflowEnvVarValueRef struct {
	SecretKeyRef *WorkflowSecretKeyRef `json:"secretKeyRef,omitempty"`
}

type WorkflowSecretKeyRef struct {
	Name string `json:"name"`
	Key  string `json:"key"`
}

type ComponentWorkflowParameters struct {
	Repository           *WorkflowRepository `json:"repository,omitempty"`
	Docker               *DockerParameters   `json:"docker,omitempty"`
	EnvironmentVariables []WorkflowEnvVarRef `json:"environmentVariables,omitempty"`
}

type ComponentWorkflowSpec struct {
	Kind       string                       `json:"kind,omitempty"`
	Name       string                       `json:"name,omitempty"`
	Parameters *ComponentWorkflowParameters `json:"parameters,omitempty"`
}

type CreateComponentRequest struct {
	Name        string                 `json:"name"`
	DisplayName string                 `json:"displayName"`
	Description string                 `json:"description"`
	Type        string                 `json:"type"`
	AutoBuild   bool                   `json:"autoBuild,omitempty"`
	AutoDeploy  bool                   `json:"autoDeploy,omitempty"`
	Workflow    *ComponentWorkflowSpec `json:"workflow,omitempty"`
}

// -- WorkflowRun (builds) ----------------------------------------------------

type WorkflowRun struct {
	Name          string `json:"name,omitempty"`
	Status        string `json:"status,omitempty"`
	StartedAt     string `json:"startedAt,omitempty"`
	ComponentName string `json:"componentName,omitempty"`
	ProjectName   string `json:"projectName,omitempty"`

	// Completed mirrors Status.Conditions[type=WorkflowCompleted].Status=True
	// — the canonical OC signal that the controller is done with this run
	// (controller_conditions.go:151-152). Watchers gate terminal-state
	// transitions on this, not on substring-matching the Status string.
	Completed bool `json:"completed,omitempty"`

	// Tasks mirrors OC's WorkflowRun.Status.Tasks[] (CRD shape:
	// {Name, Phase, Message, StartedAt, CompletedAt}). Used by the build
	// watcher's auth-failure classifier and by the build-progress endpoint.
	Tasks []WorkflowRunTask `json:"tasks,omitempty"`
}

// WorkflowRunTask is the platform-side projection of OC's ocTask. Mirrors the
// upstream CRD field-for-field (api/v1alpha1/workflowrun_types.go:80-109).
type WorkflowRunTask struct {
	Name        string `json:"name"`
	Phase       string `json:"phase,omitempty"`
	Message     string `json:"message,omitempty"`
	StartedAt   string `json:"startedAt,omitempty"`
	CompletedAt string `json:"completedAt,omitempty"`
}

type WorkflowRunList struct {
	Items []WorkflowRun `json:"items"`
}

// -- Deployment (ReleaseBinding) ---------------------------------------------

type Deployment struct {
	Name          string `json:"name,omitempty"`
	Environment   string `json:"environment,omitempty"`
	ReleaseName   string `json:"releaseName,omitempty"`
	ComponentName string `json:"componentName,omitempty"`
	EndpointURL   string `json:"endpointUrl,omitempty"`
	CreatedAt     string `json:"createdAt,omitempty"`
	Status        string `json:"status,omitempty"`
}

type DeploymentList struct {
	Items []Deployment `json:"items"`
}

// -- ComponentOpenAPI (Test tab) ----------------------------------------------

// ComponentOpenAPI is the response shape returned by
// GET /api/v1/.../components/{name}/openapi. The spec is the raw YAML string
// from .asdlc/design.json (already canonicalised on write by
// openapi_normalize.go), shipped verbatim so the console's swagger-ui can
// parse it without an extra round-trip.
type ComponentOpenAPI struct {
	ComponentName string `json:"componentName"`
	ComponentType string `json:"componentType"`
	Spec          string `json:"spec"`
}

// -- Build Logs ---------------------------------------------------------------

type BuildLogEntry struct {
	Timestamp string `json:"timestamp,omitempty"`
	Log       string `json:"log"`
	Level     string `json:"level,omitempty"`
}

type BuildLogs struct {
	Logs       []BuildLogEntry `json:"logs"`
	TotalCount int             `json:"totalCount,omitempty"`
}
