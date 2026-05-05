package api

import (
	"net/http"

	"github.com/wso2/asdlc/asdlc-service/controllers"
)

func registerSpecRoutes(mux *http.ServeMux, c controllers.SpecController) {
	mux.HandleFunc("GET /api/v1/organizations/{orgHandle}/projects/{projectName}/spec", c.GetSpec)
	mux.HandleFunc("PUT /api/v1/organizations/{orgHandle}/projects/{projectName}/spec", c.UpdateSpec)
	mux.HandleFunc("POST /api/v1/organizations/{orgHandle}/projects/{projectName}/spec/save", c.SaveAndProceed)
	mux.HandleFunc("POST /api/v1/organizations/{orgHandle}/projects/{projectName}/spec/discard", c.DiscardChanges)
	mux.HandleFunc("POST /api/v1/organizations/{orgHandle}/projects/{projectName}/spec/generate", c.GenerateSpec)
	mux.HandleFunc("GET /api/v1/organizations/{orgHandle}/projects/{projectName}/spec/versions", c.ListSpecVersions)
	mux.HandleFunc("GET /api/v1/organizations/{orgHandle}/projects/{projectName}/spec/versions/{version}", c.GetSpecAtVersion)
	mux.HandleFunc("GET /api/v1/organizations/{orgHandle}/projects/{projectName}/spec/wireframe", c.GetSpecWireframe)
	mux.HandleFunc("POST /api/v1/organizations/{orgHandle}/projects/{projectName}/spec/wireframe/generate", c.GenerateSpecWireframe)
}
