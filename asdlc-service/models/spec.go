package models

// RequirementsBundle describes the multi-document requirements set stored at
// `specs/requirements/*.md`. The bundle is versioned together as `v<N>`
// tags; each save produces one new tag covering the whole directory.
type RequirementsBundle struct {
	ProjectID         string            `json:"projectId"`
	Files             map[string]string `json:"files"`
	Status            string            `json:"status"` // "draft" | "approved"
	Version           int               `json:"version,omitempty"`
	Versions          []ArtifactVersion `json:"versions,omitempty"`
	HasUnsavedChanges bool              `json:"hasUnsavedChanges,omitempty"`
}
