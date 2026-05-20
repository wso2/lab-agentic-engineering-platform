package api

import (
	"net/http"

	"github.com/wso2/asdlc/asdlc-service/controllers"
)

func registerBoardRoutes(mux *http.ServeMux, c controllers.BoardController) {
	mux.HandleFunc("GET /api/v1/organizations/{orgHandle}/projects/{projectName}/board", c.GetBoard)
}
