package models

// Tasks is the response structure for fetching generated tasks.
type Tasks struct {
	ProjectID         string          `json:"projectId"`
	OrgID             string          `json:"-"`
	Tasks             []ComponentTask `json:"tasks"`
	Status            string          `json:"status"`
	Version           int             `json:"version"`
	Versions          []ArtifactVersion `json:"versions,omitempty"`
	HasUnsavedChanges bool            `json:"hasUnsavedChanges"`
	SourceDesign      string          `json:"sourceDesign,omitempty"`
}
