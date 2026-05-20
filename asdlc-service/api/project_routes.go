package api

import (
	"net/http"

	"github.com/wso2/asdlc/asdlc-service/controllers"
)

func registerProjectRoutes(mux *http.ServeMux, c controllers.ProjectController) {
	mux.HandleFunc("GET /api/v1/organizations/{orgHandle}/projects", c.ListProjects)
	mux.HandleFunc("POST /api/v1/organizations/{orgHandle}/projects", c.CreateProject)
	mux.HandleFunc("GET /api/v1/organizations/{orgHandle}/projects/{projectName}", c.GetProject)
	mux.HandleFunc("DELETE /api/v1/organizations/{orgHandle}/projects/{projectName}", c.DeleteProject)
	mux.HandleFunc("GET /api/v1/organizations/{orgHandle}/projects/{projectName}/repo", c.GetRepoStatus)
	mux.HandleFunc("GET /api/v1/organizations/{orgHandle}/projects/{projectName}/status", c.GetProjectStatus)
}
