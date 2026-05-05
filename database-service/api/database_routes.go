package api

import (
	"net/http"

	"github.com/wso2/asdlc/database-service/controllers"
)

func registerDatabaseRoutes(mux *http.ServeMux, ctrl controllers.DatabaseController) {
	mux.HandleFunc("POST /api/v1/databases/provision", ctrl.ProvisionDatabase)
	mux.HandleFunc("POST /api/v1/databases/test-connection", ctrl.TestConnection)
}
