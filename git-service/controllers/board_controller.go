package controllers

import (
	"encoding/json"
	"log/slog"
	"net/http"

	"github.com/wso2/asdlc/git-service/services"
	"github.com/wso2/asdlc/git-service/utils"
)

// BoardController handles HTTP requests for project board operations.
type BoardController interface {
	GetBoard(w http.ResponseWriter, r *http.Request)
	MoveIssueToStatus(w http.ResponseWriter, r *http.Request)
}

type boardController struct {
	service services.BoardService
}

func NewBoardController(service services.BoardService) BoardController {
	return &boardController{service: service}
}

func (c *boardController) GetBoard(w http.ResponseWriter, r *http.Request) {
	projectID := r.PathValue("projectId")

	result, err := c.service.GetBoard(r.Context(), projectID)
	if err != nil {
		slog.ErrorContext(r.Context(), "get board failed", "error", err, "project", projectID)
		utils.WriteErrorResponse(w, http.StatusInternalServerError, "failed to get board")
		return
	}

	utils.WriteSuccessResponse(w, http.StatusOK, result)
}

func (c *boardController) MoveIssueToStatus(w http.ResponseWriter, r *http.Request) {
	projectID := r.PathValue("projectId")

	var req struct {
		IssueURL     string `json:"issueUrl"`
		TargetStatus string `json:"targetStatus"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		utils.WriteErrorResponse(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.IssueURL == "" || req.TargetStatus == "" {
		utils.WriteErrorResponse(w, http.StatusBadRequest, "issueUrl and targetStatus are required")
		return
	}

	if err := c.service.MoveIssueToStatus(r.Context(), projectID, req.IssueURL, req.TargetStatus); err != nil {
		slog.ErrorContext(r.Context(), "move board item failed", "error", err, "project", projectID, "issueUrl", req.IssueURL)
		utils.WriteErrorResponse(w, http.StatusInternalServerError, "failed to move board item")
		return
	}

	utils.WriteSuccessResponse(w, http.StatusOK, map[string]string{"status": "ok"})
}
