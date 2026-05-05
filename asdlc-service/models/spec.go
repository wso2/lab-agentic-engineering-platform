package models

type Spec struct {
	ProjectID         string            `json:"projectId"`
	Content           string            `json:"content"`
	Status            string            `json:"status"`
	Version           int               `json:"version"`
	Versions          []ArtifactVersion `json:"versions,omitempty"`
	HasUnsavedChanges bool              `json:"hasUnsavedChanges"`
}
