package models

// DesignComponent describes a single component within a design.
// This matches the structured output schema from the AI Agent SDK.
type DesignComponent struct {
	Name                       string           `json:"name"`
	ComponentType              string           `json:"componentType"`
	Language                   string           `json:"language"`
	DependsOn                  []string         `json:"dependsOn"`
	Entrypoint                 string           `json:"entrypoint"`
	Buildpack                  string           `json:"buildpack"`
	AppPath                    string           `json:"appPath"`
	OpenAPISpec                string           `json:"openAPISpec"`
	ComponentAgentInstructions string           `json:"componentAgentInstructions"`
	Api                        *APISecurity     `json:"api,omitempty"`
	Auth                       *ComponentAuth   `json:"auth,omitempty"`
	CallerIdentity             *CallerIdentity  `json:"callerIdentity,omitempty"`
	ExposesAPI                 *ExposesAPI      `json:"exposesAPI,omitempty"`
	DependentApis              []DependentAPI   `json:"dependentApis,omitempty"`
}

// DependentAPI is an HTTP endpoint outside this project that a component
// consumes at runtime — a corporate directory, a payments processor, etc.
// Unlike `DependsOn` (which references sibling components built by this
// project), the URL here is fixed at design time. The cell diagram renders
// these outside the cell boundary, and the tech-lead carries the URL +
// description into the coding-agent's issue body.
type DependentAPI struct {
	Name           string `json:"name"`
	URL            string `json:"url"`
	Description    string `json:"description,omitempty"`
	Authentication string `json:"authentication,omitempty"`
}

// APISecurity carries the component's HTTP API security policy. Absent / nil
// ⇒ public (no AP hop). `Security: "required"` ⇒ AP enforces JWT validation.
//
// Deprecated: prefer ExposesAPI. Kept as a backwards-compat alias for
// older designs.
type APISecurity struct {
	Security string `json:"security,omitempty"`
}

// ExposesAPI is the Phase-2 replacement for APISecurity. Carried on
// service components only. `Auth: "end-user-required"` ⇒ gateway
// validates an end-user JWT and injects UserContext (default
// X-User-Id) before forwarding upstream.
type ExposesAPI struct {
	Managed     bool   `json:"managed,omitempty"`
	Auth        string `json:"auth,omitempty"`        // "end-user-required" | "service-required" | "none"
	UserContext string `json:"userContext,omitempty"` // injected header name
}

// CallerIdentity is the Phase-2 replacement for ComponentAuth on
// web-app components. `Mode: "end-user"` ⇒ the SPA performs OIDC
// Authorization Code + PKCE against the platform IDP and the BFF
// declares the per-project OAuth client lazily.
type CallerIdentity struct {
	Mode string `json:"mode,omitempty"` // "end-user" | "service-account" | "none"
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
