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

type ComponentWorkflowParameters struct {
	Repository *WorkflowRepository `json:"repository,omitempty"`
	Docker     *DockerParameters   `json:"docker,omitempty"`
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

// -- Workload ----------------------------------------------------------------

type WorkloadEndpoint struct {
	Type       string   `json:"type,omitempty"`
	Port       int      `json:"port"`
	Visibility []string `json:"visibility,omitempty"`
}

type CreateWorkloadRequest struct {
	ComponentName string                      `json:"componentName"`
	ProjectName   string                      `json:"projectName"`
	Image         string                      `json:"image"`
	Port          int                         `json:"port"`
	Args          []string                    `json:"args,omitempty"`
	EnvVars       []EnvVar                    `json:"envVars,omitempty"`
	Endpoints     map[string]WorkloadEndpoint `json:"endpoints,omitempty"`
}

// CreateReleaseParams holds all parameters for creating a ComponentRelease.
type CreateReleaseParams struct {
	OrgName           string
	ProjectName       string
	ComponentName     string
	ReleaseName       string
	Image             string
	Port              int
	Args              []string
	EnvVars           []EnvVar
	ComponentTypeName string
}

// -- WorkflowRun (builds) ----------------------------------------------------

type WorkflowRun struct {
	Name          string `json:"name,omitempty"`
	Status        string `json:"status,omitempty"`
	StartedAt     string `json:"startedAt,omitempty"`
	ComponentName string `json:"componentName,omitempty"`
	ProjectName   string `json:"projectName,omitempty"`
	Image         string `json:"image,omitempty"`
	Commit        string `json:"commit,omitempty"`

	// Tasks preserves OC's per-task outputs from Status.Tasks[]. Phase 2
	// PR D §9.3 — the build watcher classifies git-clone auth failures by
	// inspecting the checkout-source task's failure outputs. Optional;
	// only populated when OC surfaces task-level outputs.
	Tasks []WorkflowRunTask `json:"tasks,omitempty"`
}

// WorkflowRunTask is the platform-side projection of OC's ocTask. The
// only consumer today (PR D) reads these to discriminate
// git_clone_failed_auth from other build failures, so the shape is
// minimal — Name + a flat key/value Outputs map.
type WorkflowRunTask struct {
	Name    string            `json:"name"`
	Outputs map[string]string `json:"outputs,omitempty"`
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

type DeployRequest struct {
	Environment string `json:"environment"`
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

