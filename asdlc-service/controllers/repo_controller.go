package controllers

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"

	"github.com/wso2/asdlc/asdlc-service/utils/validate"
	"github.com/wso2/asdlc/asdlc-service/services"
	"github.com/wso2/asdlc/asdlc-service/utils"
)

// RepoController handles HTTP requests for repository management.
type RepoController interface {
	CreateRepo(w http.ResponseWriter, r *http.Request)
	GetRepo(w http.ResponseWriter, r *http.Request)
	DeleteRepo(w http.ResponseWriter, r *http.Request)
}

type repoController struct {
	service services.RepoService
}

func NewRepoController(service services.RepoService) RepoController {
	return &repoController{service: service}
}

type createRepoRequest struct {
	OrgID       string `json:"orgId"`
	ProjectID   string `json:"projectId"`
	ProjectName string `json:"projectName"`
}

func (c *repoController) CreateRepo(w http.ResponseWriter, r *http.Request) {
	var req createRepoRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		utils.WriteErrorResponse(w, http.StatusBadRequest, "invalid request body")
		return
	}

	if req.ProjectName == "" {
		utils.WriteErrorResponse(w, http.StatusBadRequest, "projectName is required")
		return
	}
	if err := validate.Slug(req.OrgID); err != nil {
		utils.WriteErrorResponse(w, http.StatusBadRequest, "orgId: "+err.Error())
		return
	}
	if err := validate.Slug(req.ProjectID); err != nil {
		utils.WriteErrorResponse(w, http.StatusBadRequest, "projectId: "+err.Error())
		return
	}

	repo, err := c.service.CreateRepo(r.Context(), req.OrgID, req.ProjectID, req.ProjectName)
	if err != nil {
		slog.ErrorContext(r.Context(), "create repo failed", "error", err)
		utils.WriteErrorResponse(w, http.StatusInternalServerError, "failed to create repository")
		return
	}

	// 200 on idempotent return-existing, 201 on first create. The handler
	// can't distinguish without an extra signal, so we return 200 either way
	// — the body already conveys the same row shape.
	utils.WriteSuccessResponse(w, http.StatusOK, repo)
}

func (c *repoController) GetRepo(w http.ResponseWriter, r *http.Request) {
	projectID := r.PathValue("projectId")
	if err := validate.Slug(projectID); err != nil {
		utils.WriteErrorResponse(w, http.StatusBadRequest, "projectId: "+err.Error())
		return
	}

	repo, err := c.service.GetRepo(r.Context(), projectID)
	if err != nil {
		if errors.Is(err, services.ErrRepoNotFound) {
			utils.WriteErrorResponse(w, http.StatusNotFound, "repository not found")
			return
		}
		slog.ErrorContext(r.Context(), "get repo failed", "error", err)
		utils.WriteErrorResponse(w, http.StatusInternalServerError, "failed to get repository")
		return
	}

	utils.WriteSuccessResponse(w, http.StatusOK, repo)
}

func (c *repoController) DeleteRepo(w http.ResponseWriter, r *http.Request) {
	projectID := r.PathValue("projectId")
	if err := validate.Slug(projectID); err != nil {
		utils.WriteErrorResponse(w, http.StatusBadRequest, "projectId: "+err.Error())
		return
	}

	if err := c.service.DeleteRepo(r.Context(), projectID); err != nil {
		if errors.Is(err, services.ErrRepoNotFound) {
			utils.WriteErrorResponse(w, http.StatusNotFound, "repository not found")
			return
		}
		slog.ErrorContext(r.Context(), "delete repo failed", "error", err)
		utils.WriteErrorResponse(w, http.StatusInternalServerError, "failed to delete repository")
		return
	}

	utils.WriteSuccessResponse(w, http.StatusNoContent, nil)
}
