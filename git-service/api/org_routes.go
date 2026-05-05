package api

import (
	"net/http"

	"github.com/wso2/asdlc/git-service/controllers"
)

func registerOrgRoutes(mux *http.ServeMux, pc controllers.ProjectController) {
	// GitHub project operations
	mux.HandleFunc("POST /api/v1/orgs", pc.InitProject)
}
