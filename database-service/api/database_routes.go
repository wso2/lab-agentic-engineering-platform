package api

import (
	"net/http"

	"github.com/wso2/asdlc/database-service/controllers"
)

func registerDatabaseRoutes(mux *http.ServeMux, ctrl controllers.DatabaseController, reg controllers.RegistryController) {
	mux.HandleFunc("POST /api/v1/databases/provision", ctrl.ProvisionDatabase)
	mux.HandleFunc("POST /api/v1/databases/test-connection", ctrl.TestConnection)

	// BFF registry endpoints
	mux.HandleFunc("POST /api/v1/databases/register", reg.RegisterDatabase)
	mux.HandleFunc("GET /api/v1/databases", reg.ListDatabases)
	mux.HandleFunc("PATCH /api/v1/databases/{referenceID}/status", reg.UpdateDatabaseStatus)
}
