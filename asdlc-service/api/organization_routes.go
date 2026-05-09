package api

import (
	"net/http"

	"github.com/wso2/asdlc/asdlc-service/controllers"
)

// registerOrganizationRoutes wires the unscoped /api/v1/organizations
// endpoint. The BFF only lists orgs (the OC namespaces it can see); creating
// orgs is the platform's job, not the BFF's.
func registerOrganizationRoutes(mux *http.ServeMux, c controllers.OrganizationController) {
	mux.HandleFunc("GET /api/v1/organizations", c.ListOrganizations)
}
