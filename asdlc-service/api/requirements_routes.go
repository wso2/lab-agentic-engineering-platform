package api

import (
	"net/http"

	"github.com/wso2/asdlc/asdlc-service/controllers"
)

func registerRequirementsRoutes(mux *http.ServeMux, c controllers.RequirementsController) {
	prefix := "/api/v1/organizations/{orgHandle}/projects/{projectName}/requirements"

	mux.HandleFunc("GET "+prefix, c.GetRequirements)
	mux.HandleFunc("PUT "+prefix+"/files/{name}", c.UpdateRequirementFile)
	mux.HandleFunc("DELETE "+prefix+"/files/{name}", c.DeleteRequirementFile)
	mux.HandleFunc("POST "+prefix+"/files/{name}/generate", c.GenerateRequirementFile)
	mux.HandleFunc("POST "+prefix+"/save", c.SaveAndProceed)
	mux.HandleFunc("POST "+prefix+"/discard", c.DiscardChanges)
	mux.HandleFunc("GET "+prefix+"/versions", c.ListVersions)
	mux.HandleFunc("GET "+prefix+"/versions/{tag}", c.GetRequirementsAtVersion)
}
