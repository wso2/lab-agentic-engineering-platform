package controllers

import (
	"log/slog"
	"net/http"

	"github.com/wso2/asdlc/asdlc-service/services"
	"github.com/wso2/asdlc/asdlc-service/utils"
)

type BoardController interface {
	GetBoard(w http.ResponseWriter, r *http.Request)
}

type boardController struct {
	service services.BoardService
}

func NewBoardController(service services.BoardService) BoardController {
	return &boardController{service: service}
}

func (c *boardController) GetBoard(w http.ResponseWriter, r *http.Request) {
	org := r.PathValue("orgHandle")
	project := r.PathValue("projectName")

	board, err := c.service.GetBoard(r.Context(), org, project)
	if err != nil {
		slog.ErrorContext(r.Context(), "get board failed", "error", err, "org", org, "project", project)
		utils.WriteErrorResponse(w, http.StatusInternalServerError, "failed to get board")
		return
	}

	utils.WriteSuccessResponse(w, http.StatusOK, board)
}
