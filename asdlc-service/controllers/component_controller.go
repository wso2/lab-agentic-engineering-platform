package controllers

import (
	"errors"
	"log/slog"
	"net/http"

	"github.com/wso2/asdlc/asdlc-service/services"
	"github.com/wso2/asdlc/asdlc-service/utils"
)

// ComponentController handles HTTP requests for component operations.
type ComponentController interface {
	ListComponents(w http.ResponseWriter, r *http.Request)
	GetComponent(w http.ResponseWriter, r *http.Request)
	TriggerBuild(w http.ResponseWriter, r *http.Request)
	ListBuilds(w http.ResponseWriter, r *http.Request)
	GetBuildStatus(w http.ResponseWriter, r *http.Request)
	GetBuildLogs(w http.ResponseWriter, r *http.Request)
	ListDeployments(w http.ResponseWriter, r *http.Request)
}

type componentController struct {
	service     services.ComponentService
	taskService services.TaskService
}

func NewComponentController(service services.ComponentService, taskService services.TaskService) ComponentController {
	return &componentController{service: service, taskService: taskService}
}

func (c *componentController) ListComponents(w http.ResponseWriter, r *http.Request) {
	org := r.PathValue("orgHandle")
	projectName := r.PathValue("projectName")
	if !requireOrgHandle(w, org) || !requireProjectName(w, projectName) {
		return
	}

	list, err := c.service.ListComponents(r.Context(), org, projectName, 100, "")
	if err != nil {
		if errors.Is(err, services.ErrUnauthorized) {
			utils.WriteErrorResponse(w, http.StatusUnauthorized, "invalid or expired token")
			return
		}
		slog.ErrorContext(r.Context(), "list components failed", "error", err, "org", org, "project", projectName)
		utils.WriteErrorResponse(w, http.StatusInternalServerError, "failed to list components")
		return
	}

	utils.WriteSuccessResponse(w, http.StatusOK, list)
}

func (c *componentController) GetComponent(w http.ResponseWriter, r *http.Request) {
	org := r.PathValue("orgHandle")
	projectName := r.PathValue("projectName")
	componentName := r.PathValue("componentName")
	if !requireOrgHandle(w, org) || !requireProjectName(w, projectName) || !requireComponentName(w, componentName) {
		return
	}

	comp, err := c.service.GetComponent(r.Context(), org, projectName, componentName)
	if err != nil {
		if errors.Is(err, services.ErrComponentNotFound) {
			utils.WriteErrorResponse(w, http.StatusNotFound, "component not found")
			return
		}
		if errors.Is(err, services.ErrUnauthorized) {
			utils.WriteErrorResponse(w, http.StatusUnauthorized, "invalid or expired token")
			return
		}
		slog.ErrorContext(r.Context(), "get component failed", "error", err, "org", org, "component", componentName)
		utils.WriteErrorResponse(w, http.StatusInternalServerError, "failed to get component")
		return
	}

	utils.WriteSuccessResponse(w, http.StatusOK, comp)
}

func (c *componentController) TriggerBuild(w http.ResponseWriter, r *http.Request) {
	org := r.PathValue("orgHandle")
	projectName := r.PathValue("projectName")
	componentName := r.PathValue("componentName")
	if !requireOrgHandle(w, org) || !requireProjectName(w, projectName) || !requireComponentName(w, componentName) {
		return
	}

	run, err := c.service.TriggerBuild(r.Context(), org, projectName, componentName)
	if err != nil {
		if errors.Is(err, services.ErrUnauthorized) {
			utils.WriteErrorResponse(w, http.StatusUnauthorized, "invalid or expired token")
			return
		}
		slog.ErrorContext(r.Context(), "trigger build failed", "error", err, "org", org, "component", componentName)
		utils.WriteErrorResponse(w, http.StatusInternalServerError, "failed to trigger build")
		return
	}

	utils.WriteSuccessResponse(w, http.StatusCreated, run)
}

func (c *componentController) ListBuilds(w http.ResponseWriter, r *http.Request) {
	org := r.PathValue("orgHandle")
	projectName := r.PathValue("projectName")
	componentName := r.PathValue("componentName")
	if !requireOrgHandle(w, org) || !requireProjectName(w, projectName) || !requireComponentName(w, componentName) {
		return
	}

	list, err := c.service.ListBuilds(r.Context(), org, projectName, componentName, 20, "")
	if err != nil {
		if errors.Is(err, services.ErrUnauthorized) {
			utils.WriteErrorResponse(w, http.StatusUnauthorized, "invalid or expired token")
			return
		}
		slog.ErrorContext(r.Context(), "list builds failed", "error", err, "org", org, "component", componentName)
		utils.WriteErrorResponse(w, http.StatusInternalServerError, "failed to list builds")
		return
	}

	utils.WriteSuccessResponse(w, http.StatusOK, list)
}

func (c *componentController) GetBuildStatus(w http.ResponseWriter, r *http.Request) {
	org := r.PathValue("orgHandle")
	buildName := r.PathValue("buildName")
	if !requireOrgHandle(w, org) {
		return
	}
	// buildName is a k8s WorkflowRun name (lowercase DNS-label-shaped slug).
	if err := validateSlugParam(w, "buildName", buildName); err != nil {
		return
	}

	run, err := c.service.GetBuildStatus(r.Context(), org, buildName)
	if err != nil {
		if errors.Is(err, services.ErrComponentNotFound) {
			utils.WriteErrorResponse(w, http.StatusNotFound, "build not found")
			return
		}
		if errors.Is(err, services.ErrUnauthorized) {
			utils.WriteErrorResponse(w, http.StatusUnauthorized, "invalid or expired token")
			return
		}
		slog.ErrorContext(r.Context(), "get build status failed", "error", err, "org", org, "build", buildName)
		utils.WriteErrorResponse(w, http.StatusInternalServerError, "failed to get build status")
		return
	}

	utils.WriteSuccessResponse(w, http.StatusOK, run)
}

func (c *componentController) GetBuildLogs(w http.ResponseWriter, r *http.Request) {
	org := r.PathValue("orgHandle")
	projectName := r.PathValue("projectName")
	componentName := r.PathValue("componentName")
	buildName := r.PathValue("buildName")
	if !requireOrgHandle(w, org) || !requireProjectName(w, projectName) || !requireComponentName(w, componentName) {
		return
	}
	if err := validateSlugParam(w, "buildName", buildName); err != nil {
		return
	}

	logs, err := c.service.GetBuildLogs(r.Context(), org, projectName, componentName, buildName)
	if err != nil {
		if errors.Is(err, services.ErrUnauthorized) {
			utils.WriteErrorResponse(w, http.StatusUnauthorized, "invalid or expired token")
			return
		}
		if errors.Is(err, services.ErrLogsUnavailable) {
			utils.WriteErrorResponse(w, http.StatusServiceUnavailable, "build logs service not available")
			return
		}
		slog.ErrorContext(r.Context(), "get build logs failed",
			"error", err, "org", org, "project", projectName, "component", componentName, "build", buildName)
		utils.WriteErrorResponse(w, http.StatusInternalServerError, "failed to get build logs")
		return
	}

	utils.WriteSuccessResponse(w, http.StatusOK, logs)
}

func (c *componentController) ListDeployments(w http.ResponseWriter, r *http.Request) {
	org := r.PathValue("orgHandle")
	projectName := r.PathValue("projectName")
	componentName := r.PathValue("componentName")
	if !requireOrgHandle(w, org) || !requireProjectName(w, projectName) || !requireComponentName(w, componentName) {
		return
	}

	list, err := c.service.ListDeployments(r.Context(), org, projectName, componentName)
	if err != nil {
		if errors.Is(err, services.ErrUnauthorized) {
			utils.WriteErrorResponse(w, http.StatusUnauthorized, "invalid or expired token")
			return
		}
		slog.ErrorContext(r.Context(), "list deployments failed", "error", err, "org", org, "component", componentName)
		utils.WriteErrorResponse(w, http.StatusInternalServerError, "failed to list deployments")
		return
	}

	utils.WriteSuccessResponse(w, http.StatusOK, list)
}
