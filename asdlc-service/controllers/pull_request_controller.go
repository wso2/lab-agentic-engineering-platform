package controllers

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"

	"github.com/wso2/asdlc/asdlc-service/services"
	"github.com/wso2/asdlc/asdlc-service/utils"
)

// PullRequestController handles HTTP requests for pull request operations.
type PullRequestController interface {
	CreateDraftPR(w http.ResponseWriter, r *http.Request)
}

type pullRequestController struct {
	service services.PullRequestService
}

func NewPullRequestController(service services.PullRequestService) PullRequestController {
	return &pullRequestController{service: service}
}

func (c *pullRequestController) CreateDraftPR(w http.ResponseWriter, r *http.Request) {
	projectID := r.PathValue("projectId")
	if !requireProjectIDSlug(w, projectID) {
		return
	}

	var req services.CreateDraftPRRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		utils.WriteErrorResponse(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.Head == "" || req.Base == "" || req.Title == "" {
		utils.WriteErrorResponse(w, http.StatusBadRequest, "head, base, and title are required")
		return
	}

	result, err := c.service.CreateDraftPR(r.Context(), projectID, req)
	if err != nil {
		if errors.Is(err, services.ErrRepoNotFound) {
			utils.WriteErrorResponse(w, http.StatusNotFound, "repository not found")
			return
		}
		slog.ErrorContext(r.Context(), "create draft PR failed", "project", projectID, "error", err)
		utils.WriteErrorResponse(w, http.StatusInternalServerError, "failed to create draft PR")
		return
	}

	utils.WriteSuccessResponse(w, http.StatusCreated, result)
}
