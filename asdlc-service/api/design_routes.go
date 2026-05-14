package api

import (
	"net/http"

	"github.com/wso2/asdlc/asdlc-service/controllers"
)

func registerDesignRoutes(mux *http.ServeMux, c controllers.DesignController) {
	mux.HandleFunc("GET /api/v1/organizations/{orgHandle}/projects/{projectName}/design", c.GetDesign)
	mux.HandleFunc("POST /api/v1/organizations/{orgHandle}/projects/{projectName}/design/generate", c.GenerateDesign)
	mux.HandleFunc("POST /api/v1/organizations/{orgHandle}/projects/{projectName}/design/save", c.SaveAndProceed)
	mux.HandleFunc("POST /api/v1/organizations/{orgHandle}/projects/{projectName}/design/discard", c.DiscardChanges)
	mux.HandleFunc("GET /api/v1/organizations/{orgHandle}/projects/{projectName}/design/versions", c.ListDesignVersions)
	mux.HandleFunc("GET /api/v1/organizations/{orgHandle}/projects/{projectName}/design/versions/{tag}", c.GetDesignAtTag)
}
