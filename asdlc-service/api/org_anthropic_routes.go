package api

import (
	"net/http"

	"github.com/wso2/asdlc/asdlc-service/controllers"
)

// registerOrgAnthropicRoutes wires the per-org Anthropic settings surface.
// Inherits the JWT middleware that protects every other org-scoped route.
//
// See docs/design/anthropic-key-dual-token.md.
func registerOrgAnthropicRoutes(mux *http.ServeMux, c controllers.OrgAnthropicController) {
	mux.HandleFunc("POST /api/v1/organizations/{orgHandle}/anthropic", c.Connect)
	mux.HandleFunc("GET /api/v1/organizations/{orgHandle}/anthropic", c.GetStatus)
	mux.HandleFunc("DELETE /api/v1/organizations/{orgHandle}/anthropic", c.Disconnect)
}
