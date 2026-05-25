package api

import (
	"net/http"

	"github.com/wso2/asdlc/asdlc-service/controllers"
)

// registerSkillRoutes wires the org-scoped skills catalogue. Inherits the
// JWT + org-ensure middleware that protects every other org-scoped route.
//
// `POST .../skills/import` is more specific than `{name}` and is registered
// for POST only, so it never collides with the create / update / delete
// `{name}` patterns. See docs/design/skills-system.md > "REST API".
func registerSkillRoutes(mux *http.ServeMux, c controllers.SkillController) {
	mux.HandleFunc("GET /api/v1/organizations/{orgHandle}/skills", c.List)
	mux.HandleFunc("POST /api/v1/organizations/{orgHandle}/skills", c.Create)
	mux.HandleFunc("POST /api/v1/organizations/{orgHandle}/skills/import", c.Import)
	mux.HandleFunc("GET /api/v1/organizations/{orgHandle}/skills/{name}", c.Get)
	mux.HandleFunc("PUT /api/v1/organizations/{orgHandle}/skills/{name}", c.Update)
	mux.HandleFunc("DELETE /api/v1/organizations/{orgHandle}/skills/{name}", c.Delete)
}
