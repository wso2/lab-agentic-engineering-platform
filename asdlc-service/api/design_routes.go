package api

import (
	"net/http"

	"github.com/wso2/asdlc/asdlc-service/controllers"
)

func registerDesignRoutes(mux *http.ServeMux, c controllers.DesignController) {
	// Assembled Design view (used by cell diagram + downstream code).
	mux.HandleFunc("GET /api/v1/organizations/{orgHandle}/projects/{projectName}/design", c.GetDesign)

	// Multi-file bundle view (used by the Explorer architecture page).
	mux.HandleFunc("GET /api/v1/organizations/{orgHandle}/projects/{projectName}/design/bundle", c.GetDesignBundle)
	mux.HandleFunc("PUT /api/v1/organizations/{orgHandle}/projects/{projectName}/design/files/{path...}", c.UpdateDesignFile)
	mux.HandleFunc("DELETE /api/v1/organizations/{orgHandle}/projects/{projectName}/design/files/{path...}", c.DeleteDesignFile)
	mux.HandleFunc("DELETE /api/v1/organizations/{orgHandle}/projects/{projectName}/design/components/{componentName}", c.DeleteComponent)

	// Whole-design generation (architect agent).
	mux.HandleFunc("POST /api/v1/organizations/{orgHandle}/projects/{projectName}/design/generate", c.GenerateDesign)

	// Save / discard / versions.
	mux.HandleFunc("POST /api/v1/organizations/{orgHandle}/projects/{projectName}/design/save", c.SaveAndProceed)
	mux.HandleFunc("POST /api/v1/organizations/{orgHandle}/projects/{projectName}/design/discard", c.DiscardChanges)
	mux.HandleFunc("GET /api/v1/organizations/{orgHandle}/projects/{projectName}/design/versions", c.ListDesignVersions)
	mux.HandleFunc("GET /api/v1/organizations/{orgHandle}/projects/{projectName}/design/versions/{tag}", c.GetDesignAtTag)
	mux.HandleFunc("GET /api/v1/organizations/{orgHandle}/projects/{projectName}/design/versions/{tag}/bundle", c.GetDesignBundleAtTag)
}
