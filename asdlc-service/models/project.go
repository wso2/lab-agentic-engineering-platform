package models

type Project struct {
	UID                string `json:"uid,omitempty"`
	Name               string `json:"name"`
	NamespaceName      string `json:"namespaceName,omitempty"`
	DisplayName        string `json:"displayName,omitempty"`
	Description        string `json:"description,omitempty"`
	DeploymentPipeline string `json:"deploymentPipeline,omitempty"`
	CreatedAt          string `json:"createdAt,omitempty"`
	Status             string `json:"status,omitempty"`
}

type ProjectList struct {
	Items []Project `json:"items"`
}

type CreateProjectRequest struct {
	Name               string `json:"name"`
	DisplayName        string `json:"displayName,omitempty"`
	Description        string `json:"description,omitempty"`
	DeploymentPipeline string `json:"deploymentPipeline,omitempty"`
}

// ProjectStatus represents the computed SDLC phase and artifact states.
type ProjectStatus struct {
	Phase        string `json:"phase"`        // "no-repo", "repo-cloning", "prompt", "spec", "architecture", "tasks", "components"
	RepoStatus   string `json:"repoStatus"`   // "", "pending", "cloning", "ready", "error"
	RepoURL      string `json:"repoUrl"`
	HasSpec      bool   `json:"hasSpec"`
	HasDesign    bool   `json:"hasDesign"`
	HasTasks     bool   `json:"hasTasks"`
	SpecStatus   string `json:"specStatus"`   // "", "draft", "approved"
	DesignStatus string `json:"designStatus"` // "", "draft", "approved"
}
