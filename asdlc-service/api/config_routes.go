package api

import (
	"net/http"

	"github.com/wso2/asdlc/asdlc-service/controllers"
)

func registerConfigRoutes(mux *http.ServeMux, c controllers.ConfigController) {
	prefix := "/api/v1/organizations/{orgHandle}/projects/{projectName}/components/{componentName}/configs"
	mux.HandleFunc("GET "+prefix, c.GetConfig)
	mux.HandleFunc("PUT "+prefix, c.UpdateConfig)
}
