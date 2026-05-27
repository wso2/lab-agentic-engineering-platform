package api

import (
	"net/http"

	"github.com/wso2/asdlc/asdlc-service/controllers"
)

func registerRepoBoardRoutes(mux *http.ServeMux, bc controllers.RepoBoardController) {
	if bc == nil {
		return
	}
	mux.HandleFunc("GET /api/v1/repos/{projectId}/board", bc.GetBoard)
	mux.HandleFunc("POST /api/v1/repos/{projectId}/board/move", bc.MoveIssueToStatus)
}
