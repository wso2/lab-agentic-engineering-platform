package api

import (
	"net/http"

	"github.com/wso2/asdlc/asdlc-service/controllers"
)

// registerOrganizationRoutes wires the unscoped /api/v1/organizations
// endpoints. The list+create pair is the only org-level surface; per-org
// settings stay under /api/v1/organizations/{orgHandle}/...
func registerOrganizationRoutes(mux *http.ServeMux, c controllers.OrganizationController) {
	mux.HandleFunc("GET /api/v1/organizations", c.ListOrganizations)
	mux.HandleFunc("POST /api/v1/organizations", c.CreateOrganization)
}
