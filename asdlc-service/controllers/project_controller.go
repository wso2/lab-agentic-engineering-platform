package controllers

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"

	"github.com/wso2/asdlc/asdlc-service/clients/requests"
	"github.com/wso2/asdlc/asdlc-service/models"
	"github.com/wso2/asdlc/asdlc-service/services"
	"github.com/wso2/asdlc/asdlc-service/utils"
)

// ProjectController handles HTTP requests for project operations.
type ProjectController interface {
	ListProjects(w http.ResponseWriter, r *http.Request)
	GetProject(w http.ResponseWriter, r *http.Request)
	CreateProject(w http.ResponseWriter, r *http.Request)
	DeleteProject(w http.ResponseWriter, r *http.Request)
	GetRepoStatus(w http.ResponseWriter, r *http.Request)
	GetProjectStatus(w http.ResponseWriter, r *http.Request)
}

type projectController struct {
	service services.ProjectService
}

func NewProjectController(service services.ProjectService) ProjectController {
	return &projectController{service: service}
}

func (c *projectController) ListProjects(w http.ResponseWriter, r *http.Request) {
	org := r.PathValue("orgHandle")
	if !requireOrgHandle(w, org) {
		return
	}
	cursor := r.URL.Query().Get("cursor")

	list, err := c.service.ListProjects(r.Context(), org, 100, cursor)
	if err != nil {
		if errors.Is(err, services.ErrUnauthorized) {
			utils.WriteErrorResponse(w, http.StatusUnauthorized, "invalid or expired token")
			return
		}
		slog.ErrorContext(r.Context(), "list projects failed", "error", err, "org", org)
		utils.WriteErrorResponse(w, http.StatusInternalServerError, "failed to list projects")
		return
	}

	utils.WriteSuccessResponse(w, http.StatusOK, list)
}

func (c *projectController) GetProject(w http.ResponseWriter, r *http.Request) {
	org := r.PathValue("orgHandle")
	projectName := r.PathValue("projectName")
	if !requireOrgHandle(w, org) || !requireProjectName(w, projectName) {
		return
	}

	project, err := c.service.GetProject(r.Context(), org, projectName)
	if err != nil {
		if errors.Is(err, services.ErrUnauthorized) {
			utils.WriteErrorResponse(w, http.StatusUnauthorized, "invalid or expired token")
			return
		}
		if errors.Is(err, services.ErrProjectNotFound) {
			utils.WriteErrorResponse(w, http.StatusNotFound, "project not found")
			return
		}
		slog.ErrorContext(r.Context(), "get project failed", "error", err, "org", org, "project", projectName)
		utils.WriteErrorResponse(w, http.StatusInternalServerError, "failed to get project")
		return
	}

	utils.WriteSuccessResponse(w, http.StatusOK, project)
}

func (c *projectController) CreateProject(w http.ResponseWriter, r *http.Request) {
	org := r.PathValue("orgHandle")
	if !requireOrgHandle(w, org) {
		return
	}

	var req models.CreateProjectRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		utils.WriteErrorResponse(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.Name == "" {
		utils.WriteErrorResponse(w, http.StatusBadRequest, "name is required")
		return
	}

	project, err := c.service.CreateProject(r.Context(), org, &req)
	if err != nil {
		if errors.Is(err, services.ErrUnauthorized) {
			utils.WriteErrorResponse(w, http.StatusUnauthorized, "invalid or expired token")
			return
		}
		var httpErr *requests.HttpError
		if errors.As(err, &httpErr) {
			slog.ErrorContext(r.Context(), "create project failed", "error", err, "org", org, "status", httpErr.StatusCode)
			utils.WriteErrorResponse(w, httpErr.StatusCode, httpErr.Body)
			return
		}
		slog.ErrorContext(r.Context(), "create project failed", "error", err, "org", org)
		utils.WriteErrorResponse(w, http.StatusInternalServerError, "failed to create project")
		return
	}

	utils.WriteSuccessResponse(w, http.StatusCreated, project)
}

func (c *projectController) DeleteProject(w http.ResponseWriter, r *http.Request) {
	org := r.PathValue("orgHandle")
	projectName := r.PathValue("projectName")
	if !requireOrgHandle(w, org) || !requireProjectName(w, projectName) {
		return
	}

	err := c.service.DeleteProject(r.Context(), org, projectName)
	if err != nil {
		if errors.Is(err, services.ErrUnauthorized) {
			utils.WriteErrorResponse(w, http.StatusUnauthorized, "invalid or expired token")
			return
		}
		if errors.Is(err, services.ErrForbidden) {
			utils.WriteErrorResponse(w, http.StatusForbidden, "insufficient permissions to delete this project")
			return
		}
		if errors.Is(err, services.ErrProjectNotFound) {
			utils.WriteErrorResponse(w, http.StatusNotFound, "project not found")
			return
		}
		slog.ErrorContext(r.Context(), "delete project failed", "error", err, "org", org, "project", projectName)
		utils.WriteErrorResponse(w, http.StatusInternalServerError, "failed to delete project")
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

func (c *projectController) GetRepoStatus(w http.ResponseWriter, r *http.Request) {
	org := r.PathValue("orgHandle")
	projectName := r.PathValue("projectName")
	if !requireOrgHandle(w, org) || !requireProjectName(w, projectName) {
		return
	}

	repo, err := c.service.GetRepoStatus(r.Context(), org, projectName)
	if err != nil {
		if errors.Is(err, services.ErrProjectNotFound) {
			utils.WriteErrorResponse(w, http.StatusNotFound, "repository not found")
			return
		}
		slog.ErrorContext(r.Context(), "get repo status failed", "error", err, "project", projectName)
		utils.WriteErrorResponse(w, http.StatusInternalServerError, "failed to get repo status")
		return
	}

	utils.WriteSuccessResponse(w, http.StatusOK, repo)
}

func (c *projectController) GetProjectStatus(w http.ResponseWriter, r *http.Request) {
	org := r.PathValue("orgHandle")
	projectName := r.PathValue("projectName")
	if !requireOrgHandle(w, org) || !requireProjectName(w, projectName) {
		return
	}

	status, err := c.service.GetProjectStatus(r.Context(), org, projectName)
	if err != nil {
		if errors.Is(err, services.ErrUnauthorized) {
			utils.WriteErrorResponse(w, http.StatusUnauthorized, "invalid or expired token")
			return
		}
		slog.ErrorContext(r.Context(), "get project status failed", "error", err, "org", org, "project", projectName)
		utils.WriteErrorResponse(w, http.StatusInternalServerError, "failed to get project status")
		return
	}

	utils.WriteSuccessResponse(w, http.StatusOK, status)
}
