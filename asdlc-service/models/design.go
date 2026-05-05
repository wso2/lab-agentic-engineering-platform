package models

// DesignComponent describes a single component within a design.
// This matches the structured output schema from the AI Agent SDK.
type DesignComponent struct {
	Name                       string   `json:"name"`
	ComponentType              string   `json:"componentType"`
	Language                   string   `json:"language"`
	DependsOn                  []string `json:"dependsOn"`
	Entrypoint                 string   `json:"entrypoint"`
	Buildpack                  string   `json:"buildpack"`
	AppPath                    string   `json:"appPath"`
	OpenAPISpec                string   `json:"openAPISpec"`
	ComponentAgentInstructions string   `json:"componentAgentInstructions"`
	WireframePath              string   `json:"wireframePath,omitempty"`
}

// DesignComponents is a slice of DesignComponent.
type DesignComponents []DesignComponent

// Requirements is a slice of requirement strings.
type Requirements []string

type Design struct {
	ProjectID         string           `json:"projectId"`
	OrgID             string           `json:"-"`
	Overview          string           `json:"overview"`
	Requirements      Requirements     `json:"requirements"`
	Components        DesignComponents `json:"components"`
	Status            string           `json:"status"`
	Version           int              `json:"version"`
	Versions          []ArtifactVersion `json:"versions,omitempty"`
	HasUnsavedChanges bool             `json:"hasUnsavedChanges"`
	SourceSpec        string           `json:"sourceSpec,omitempty"`
}
