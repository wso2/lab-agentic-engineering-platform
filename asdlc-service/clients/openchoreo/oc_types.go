package openchoreo

// Internal K8s-style types used when communicating with the OpenChoreo API.
// These are not exposed outside this package — callers use the flat models
// from the models package.

// -- Common ------------------------------------------------------------------

type ocObjectMeta struct {
	Name              string            `json:"name,omitempty"`
	Namespace         string            `json:"namespace,omitempty"`
	UID               string            `json:"uid,omitempty"`
	CreationTimestamp string            `json:"creationTimestamp,omitempty"`
	Labels            map[string]string `json:"labels,omitempty"`
	Annotations       map[string]string `json:"annotations,omitempty"`
}

type ocCondition struct {
	Type   string `json:"type"`
	Status string `json:"status"`
	Reason string `json:"reason,omitempty"`
}

type ocStatus struct {
	Conditions []ocCondition `json:"conditions,omitempty"`
}

type ocRef struct {
	Name string `json:"name"`
}

func latestConditionReason(conditions []ocCondition) string {
	if len(conditions) == 0 {
		return ""
	}
	return conditions[len(conditions)-1].Reason
}

// -- Namespace ---------------------------------------------------------------

type ocNamespaceStatus struct {
	Phase string `json:"phase,omitempty"`
}

type ocNamespace struct {
	Metadata ocObjectMeta      `json:"metadata"`
	Status   ocNamespaceStatus `json:"status,omitempty"`
}

type ocNamespaceList struct {
	Items []ocNamespace `json:"items"`
}

// -- Project -----------------------------------------------------------------

type ocProjectSpec struct {
	DeploymentPipelineRef *ocRef `json:"deploymentPipelineRef,omitempty"`
}

type ocProject struct {
	Metadata ocObjectMeta  `json:"metadata"`
	Spec     ocProjectSpec `json:"spec"`
	Status   ocStatus      `json:"status,omitempty"`
}

type ocProjectList struct {
	Items []ocProject `json:"items"`
}

// -- Component ---------------------------------------------------------------

type ocComponentTypeRef struct {
	Kind string `json:"kind,omitempty"`
	Name string `json:"name"`
}

type ocOwner struct {
	ProjectName   string `json:"projectName"`
	ComponentName string `json:"componentName,omitempty"`
}

type ocWorkflowRevision struct {
	Branch string `json:"branch,omitempty"`
	Commit string `json:"commit,omitempty"`
}

// ocWorkflowIdentity is the {name,email,login} block used by ClusterWorkflows
// that need a git author identity passed in (coding-agent today).
type ocWorkflowIdentity struct {
	Name  string `json:"name"`
	Email string `json:"email"`
	Login string `json:"login,omitempty"`
}

type ocWorkflowRepository struct {
	URL       string              `json:"url,omitempty"`
	SecretRef string              `json:"secretRef,omitempty"`
	Revision  *ocWorkflowRevision `json:"revision,omitempty"`
	AppPath   string              `json:"appPath,omitempty"`
	// Identity is set only by the coding-agent ClusterWorkflow path. The
	// build path (dockerfile-builder etc.) leaves it nil — the field is
	// `omitempty` so the JSON shape stays unchanged for those callers.
	Identity *ocWorkflowIdentity `json:"identity,omitempty"`
}

type ocDockerParameters struct {
	Context  string `json:"context,omitempty"`
	FilePath string `json:"filePath,omitempty"`
}

// ocCodingAgentTask carries the per-task fields of the coding-agent
// ClusterWorkflow's `task` parameter object. Field names match the
// openAPIV3Schema in the YAML.
type ocCodingAgentTask struct {
	ID            string `json:"id"`
	OrgID         string `json:"orgId"`
	ProjectID     string `json:"projectId"`
	ComponentName string `json:"componentName"`
	BranchName    string `json:"branchName"`
	Prompt        string `json:"prompt"`
}

type ocCodingAgentBFF struct {
	Bearer string `json:"bearer"`
}

type ocCodingAgentGitService struct {
	URL string `json:"url"`
}

type ocWorkflowParameters struct {
	Repository *ocWorkflowRepository    `json:"repository,omitempty"`
	Docker     *ocDockerParameters      `json:"docker,omitempty"`
	Task       *ocCodingAgentTask       `json:"task,omitempty"`
	BFF        *ocCodingAgentBFF        `json:"bff,omitempty"`
	GitService *ocCodingAgentGitService `json:"gitService,omitempty"`
}

type ocWorkflow struct {
	Kind       string                `json:"kind,omitempty"`
	Name       string                `json:"name"`
	Parameters *ocWorkflowParameters `json:"parameters,omitempty"`
}

type ocComponentSpec struct {
	Owner         *ocOwner            `json:"owner,omitempty"`
	ComponentType *ocComponentTypeRef `json:"componentType,omitempty"`
	AutoDeploy    bool                `json:"autoDeploy,omitempty"`
	AutoBuild     bool                `json:"autoBuild,omitempty"`
	Workflow      *ocWorkflow         `json:"workflow,omitempty"`
}

type ocComponent struct {
	Metadata ocObjectMeta    `json:"metadata"`
	Spec     ocComponentSpec `json:"spec"`
	Status   ocStatus        `json:"status,omitempty"`
}

type ocComponentList struct {
	Items []ocComponent `json:"items"`
}

// -- WorkflowRun (builds) ----------------------------------------------------

// WorkflowRun condition Reasons. Mirrors OC's
// internal/controller/workflowrun/controller_conditions.go reason constants.
// `normalizeWorkflowRun` lifts the WorkflowCompleted condition's Reason
// into models.WorkflowRun.Status; classifiers compare against these
// constants instead of substring-matching the reason string.
const (
	ReasonWorkflowSucceeded = "WorkflowSucceeded"
	ReasonWorkflowFailed    = "WorkflowFailed"
	ReasonWorkflowRunning   = "WorkflowRunning"
)

// ocTask is the OC CRD-canonical projection of WorkflowRun.Status.Tasks[i].
// Per openchoreo/api/v1alpha1/workflowrun_types.go:80-109, OC surfaces only
// {Name, Phase, Message, StartedAt, CompletedAt}. The previously-declared
// `Outputs` field never existed on the CRD and was always nil on the wire —
// see docs/design/auth-failure-classification.md.
type ocTask struct {
	Name        string  `json:"name"`
	Phase       string  `json:"phase,omitempty"`
	Message     string  `json:"message,omitempty"`
	StartedAt   string  `json:"startedAt,omitempty"`
	CompletedAt string  `json:"completedAt,omitempty"`
}

type ocWorkflowRunStatus struct {
	Conditions []ocCondition `json:"conditions,omitempty"`
	Tasks      []ocTask      `json:"tasks,omitempty"`
}

type ocWorkflowRunSpec struct {
	Workflow *ocWorkflow `json:"workflow,omitempty"`
}

type ocWorkflowRun struct {
	Metadata ocObjectMeta        `json:"metadata"`
	Spec     ocWorkflowRunSpec   `json:"spec"`
	Status   ocWorkflowRunStatus `json:"status,omitempty"`
}

type ocWorkflowRunList struct {
	Items []ocWorkflowRun `json:"items"`
}

// -- Workload ----------------------------------------------------------------

type ocEndpoint struct {
	Type       string   `json:"type,omitempty"`
	Port       int      `json:"port,omitempty"`
	Visibility []string `json:"visibility,omitempty"`
}

type ocEnvVar struct {
	Key   string `json:"key"`
	Value string `json:"value,omitempty"`
}

type ocContainer struct {
	Image string     `json:"image,omitempty"`
	Args  []string   `json:"args,omitempty"`
	Env   []ocEnvVar `json:"env,omitempty"`
}

type ocWorkloadSpec struct {
	Owner     *ocOwner              `json:"owner,omitempty"`
	Endpoints map[string]ocEndpoint `json:"endpoints,omitempty"`
	Container *ocContainer          `json:"container,omitempty"`
}

type ocWorkload struct {
	Metadata ocObjectMeta   `json:"metadata"`
	Spec     ocWorkloadSpec `json:"spec"`
	Status   ocStatus       `json:"status,omitempty"`
}

// -- ComponentRelease --------------------------------------------------------

type ocComponentTypeSnapshot struct {
	Kind string      `json:"kind,omitempty"`
	Name string      `json:"name,omitempty"`
	Spec interface{} `json:"spec,omitempty"`
}

type ocComponentReleaseWorkload struct {
	Endpoints map[string]ocEndpoint `json:"endpoints,omitempty"`
	Container *ocContainer          `json:"container,omitempty"`
}

type ocComponentReleaseSpec struct {
	Owner         *ocOwner                    `json:"owner,omitempty"`
	ComponentType *ocComponentTypeSnapshot    `json:"componentType,omitempty"`
	Workload      *ocComponentReleaseWorkload `json:"workload,omitempty"`
}

type ocComponentRelease struct {
	Metadata ocObjectMeta           `json:"metadata"`
	Spec     ocComponentReleaseSpec `json:"spec"`
	Status   ocStatus               `json:"status,omitempty"`
}

// -- HTTPRoute (read-only, for resolving endpoint URLs) ----------------------

type ocHTTPRouteRule struct {
	Matches []struct {
		Path struct {
			Value string `json:"value"`
		} `json:"path"`
	} `json:"matches,omitempty"`
}

type ocHTTPRouteSpec struct {
	Hostnames []string          `json:"hostnames,omitempty"`
	Rules     []ocHTTPRouteRule `json:"rules,omitempty"`
}

type ocHTTPRoute struct {
	Metadata ocObjectMeta    `json:"metadata"`
	Spec     ocHTTPRouteSpec `json:"spec"`
}

type ocHTTPRouteList struct {
	Items []ocHTTPRoute `json:"items"`
}

// -- ReleaseBinding ----------------------------------------------------------

type ocReleaseBindingSpec struct {
	Owner                           *ocOwner    `json:"owner,omitempty"`
	Environment                     string      `json:"environment,omitempty"`
	ReleaseName                     string      `json:"releaseName,omitempty"`
	ComponentTypeEnvironmentConfigs interface{} `json:"componentTypeEnvironmentConfigs,omitempty"`
}

type ocEndpointExternalURL struct {
	Scheme string `json:"scheme,omitempty"`
	Host   string `json:"host,omitempty"`
	Port   int    `json:"port,omitempty"`
	Path   string `json:"path,omitempty"`
}

type ocEndpointStatus struct {
	Name         string                           `json:"name,omitempty"`
	Type         string                           `json:"type,omitempty"`
	ExternalURLs map[string]ocEndpointExternalURL `json:"externalURLs,omitempty"`
}

type ocReleaseBindingStatus struct {
	Conditions []ocCondition      `json:"conditions,omitempty"`
	Endpoints  []ocEndpointStatus `json:"endpoints,omitempty"`
}

type ocReleaseBinding struct {
	Metadata ocObjectMeta           `json:"metadata"`
	Spec     ocReleaseBindingSpec   `json:"spec"`
	Status   ocReleaseBindingStatus `json:"status,omitempty"`
}

type ocReleaseBindingList struct {
	Items []ocReleaseBinding `json:"items"`
}
