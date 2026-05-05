package api

import (
	"net/http"

	"github.com/wso2/asdlc/git-service/controllers"
)

func registerBoardRoutes(mux *http.ServeMux, bc controllers.BoardController) {
	mux.HandleFunc("GET /api/v1/repos/{projectId}/board", bc.GetBoard)
	mux.HandleFunc("POST /api/v1/repos/{projectId}/board/move", bc.MoveIssueToStatus)
}
