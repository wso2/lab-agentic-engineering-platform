package api

import (
	"net/http"

	"github.com/wso2/asdlc/asdlc-service/controllers"
)

func registerComponentRoutes(mux *http.ServeMux, c controllers.ComponentController) {
	prefix := "/api/v1/organizations/{orgHandle}/projects/{projectName}/components"

	mux.HandleFunc("GET "+prefix, c.ListComponents)
	mux.HandleFunc("GET "+prefix+"/{componentName}", c.GetComponent)

	// Build operations
	mux.HandleFunc("POST "+prefix+"/{componentName}/builds", c.TriggerBuild)
	mux.HandleFunc("GET "+prefix+"/{componentName}/builds", c.ListBuilds)
	mux.HandleFunc("GET "+prefix+"/{componentName}/builds/{buildName}", c.GetBuildStatus)
	mux.HandleFunc("GET "+prefix+"/{componentName}/builds/{buildName}/logs", c.GetBuildLogs)

	// Deploy operations — the deploy chain is driven entirely by OC's
	// Component controller (AutoDeploy=true) once the build's
	// generate-workload-cr step posts a Workload, so the BFF only exposes
	// the read path. The list reflects whatever OC has materialised into
	// ReleaseBindings for this component.
	mux.HandleFunc("GET "+prefix+"/{componentName}/deployments", c.ListDeployments)

	// OpenAPI spec (drives the Test tab). Spec is read from .asdlc/design.json
	// — service components have a guaranteed full OpenAPI 3.0 doc; non-service
	// components return 409 so the UI can render a typed empty state.
	mux.HandleFunc("GET "+prefix+"/{componentName}/openapi", c.GetComponentOpenAPI)

	// Test-proxy. The Test tab's swagger-ui can't hit the deployed endpoint
	// directly because the console + endpoint live on different subdomains
	// (CORS). This endpoint forwards an HTTP request from the browser to the
	// component's known ReleaseBinding endpoint URL and streams the response
	// back. The target is supplied in `X-Asdlc-Target-Url`; the SSRF guard
	// rejects anything outside the component's known endpoints.
	mux.HandleFunc("POST "+prefix+"/{componentName}/test-proxy", c.TestProxy)
}
