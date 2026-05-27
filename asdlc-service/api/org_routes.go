package api

import (
	"net/http"

	"github.com/wso2/asdlc/asdlc-service/controllers"
)

func registerOrgRoutes(mux *http.ServeMux, pc controllers.GitProjectController) {
	if pc == nil {
		return
	}
	// GitHub project operations
	mux.HandleFunc("POST /api/v1/orgs", pc.InitProject)
}
