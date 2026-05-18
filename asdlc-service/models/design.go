package models

// DesignComponent describes a single component within a design.
// This matches the structured output schema from the AI Agent SDK.
type DesignComponent struct {
	Name                       string         `json:"name"`
	ComponentType              string         `json:"componentType"`
	Language                   string         `json:"language"`
	DependsOn                  []string       `json:"dependsOn"`
	Entrypoint                 string         `json:"entrypoint"`
	Buildpack                  string         `json:"buildpack"`
	AppPath                    string         `json:"appPath"`
	OpenAPISpec                string         `json:"openAPISpec"`
	ComponentAgentInstructions string         `json:"componentAgentInstructions"`
	Api                        *APISecurity   `json:"api,omitempty"`
	Auth                       *ComponentAuth `json:"auth,omitempty"`
}

// APISecurity carries the component's HTTP API security policy. Absent / nil
// ⇒ public (no AP hop). `Security: "required"` ⇒ AP enforces JWT validation.
// See docs/design/api-platform-integration.md section 5.1.
type APISecurity struct {
	Security string `json:"security,omitempty"`
}

// ComponentAuth carries the OIDC relying-party configuration for a web-app
// component. Only valid on componentType: "web-app". When present with
// Kind: "oidc-spa", the dispatch path posts a `## OIDC client provisioned`
// comment on the task's issue with the platform IDP's issuer / clientId /
// scopes so the coding agent bakes them into the SPA's workload.yaml.
// See docs/design/oauth-protected-webapp.md.
type ComponentAuth struct {
	Kind     string `json:"kind"`               // "oidc-spa"
	Upstream string `json:"upstream,omitempty"` // sibling service the SPA signs in to call
}

// DesignComponents is a slice of DesignComponent.
type DesignComponents []DesignComponent

type Design struct {
	ProjectID         string            `json:"projectId"`
	OrgID             string            `json:"-"`
	Overview          string            `json:"overview"`
	Components        DesignComponents  `json:"components"`
	Status            string            `json:"status"`
	Version           int               `json:"version"`
	Versions          []ArtifactVersion `json:"versions,omitempty"`
	HasUnsavedChanges bool              `json:"hasUnsavedChanges"`
	SourceSpec        string            `json:"sourceSpec,omitempty"`
}
