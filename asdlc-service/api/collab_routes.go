package api

import (
	"net/http"

	"github.com/wso2/asdlc/asdlc-service/controllers"
)

func registerCollabRoutes(apiMux *http.ServeMux, mainMux *http.ServeMux, c controllers.CollabController) {
	// User-facing: protected by the JWT middleware via apiMux.
	apiMux.HandleFunc("GET /api/v1/organizations/{orgHandle}/projects/{projectName}/spec/collab-session", c.GetCollabSession)

	// Server-to-server: collab-server calls this with the user's Bearer JWT to
	// validate identity. Registered on mainMux so it bypasses the standard JWT
	// middleware; the controller validates the token inline.
	mainMux.HandleFunc("GET /api/v1/collab/validate", c.ValidateCollabAccess)
}
