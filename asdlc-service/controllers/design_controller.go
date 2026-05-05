package controllers

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"

	"github.com/wso2/asdlc/asdlc-service/services"
	"github.com/wso2/asdlc/asdlc-service/utils"
)

type DesignController interface {
	GetDesign(w http.ResponseWriter, r *http.Request)
	GenerateDesign(w http.ResponseWriter, r *http.Request)
	SaveAndProceed(w http.ResponseWriter, r *http.Request)
	DiscardChanges(w http.ResponseWriter, r *http.Request)
	GetDesignAtVersion(w http.ResponseWriter, r *http.Request)
	ListDesignVersions(w http.ResponseWriter, r *http.Request)
}

type designController struct {
	service services.DesignService
}

func NewDesignController(service services.DesignService) DesignController {
	return &designController{service: service}
}

func (c *designController) GetDesign(w http.ResponseWriter, r *http.Request) {
	org := r.PathValue("orgHandle")
	project := r.PathValue("projectName")
	if !requireOrgHandle(w, org) || !requireProjectName(w, project) {
		return
	}

	design, err := c.service.GetDesign(r.Context(), org, project)
	if err != nil {
		slog.ErrorContext(r.Context(), "get design failed", "error", err, "org", org, "project", project)
		utils.WriteErrorResponse(w, http.StatusInternalServerError, "failed to get design")
		return
	}

	if design == nil {
		utils.WriteSuccessResponse(w, http.StatusOK, nil)
		return
	}
	utils.WriteSuccessResponse(w, http.StatusOK, design)
}

func (c *designController) GenerateDesign(w http.ResponseWriter, r *http.Request) {
	org := r.PathValue("orgHandle")
	project := r.PathValue("projectName")
	if !requireOrgHandle(w, org) || !requireProjectName(w, project) {
		return
	}

	flusher, ok := w.(http.Flusher)
	if !ok {
		slog.ErrorContext(r.Context(), "response writer does not support flushing")
		utils.WriteErrorResponse(w, http.StatusInternalServerError, "streaming not supported")
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	w.Header().Set("x-vercel-ai-ui-message-stream", "v1")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	if err := c.service.StreamGenerateDesign(r.Context(), org, project, w, flusher.Flush); err != nil {
		slog.ErrorContext(r.Context(), "generate design failed", "error", err, "org", org, "project", project)
		// Headers already sent — surface the failure as a UI Message Stream error frame.
		errText := err.Error()
		switch {
		case errors.Is(err, services.ErrSpecNotFound):
			errText = "spec not found"
		case errors.Is(err, services.ErrSpecNotApproved):
			errText = "spec must be approved before generating a design"
		}
		errFrame, _ := json.Marshal(map[string]any{"type": "error", "errorText": errText})
		fmt.Fprintf(w, "data: %s\n\n", errFrame)
		flusher.Flush()
	}
}

func (c *designController) SaveAndProceed(w http.ResponseWriter, r *http.Request) {
	org := r.PathValue("orgHandle")
	project := r.PathValue("projectName")
	if !requireOrgHandle(w, org) || !requireProjectName(w, project) {
		return
	}

	design, err := c.service.SaveAndProceed(r.Context(), org, project)
	if err != nil {
		if errors.Is(err, services.ErrDesignNotFound) {
			utils.WriteErrorResponse(w, http.StatusNotFound, "design not found")
			return
		}
		slog.ErrorContext(r.Context(), "save and proceed design failed", "error", err, "org", org, "project", project)
		utils.WriteErrorResponse(w, http.StatusInternalServerError, "failed to save and proceed design")
		return
	}

	utils.WriteSuccessResponse(w, http.StatusOK, design)
}

func (c *designController) DiscardChanges(w http.ResponseWriter, r *http.Request) {
	org := r.PathValue("orgHandle")
	project := r.PathValue("projectName")
	if !requireOrgHandle(w, org) || !requireProjectName(w, project) {
		return
	}

	design, err := c.service.DiscardChanges(r.Context(), org, project)
	if err != nil {
		slog.ErrorContext(r.Context(), "discard design changes failed", "error", err, "org", org, "project", project)
		utils.WriteErrorResponse(w, http.StatusInternalServerError, "failed to discard design changes")
		return
	}

	utils.WriteSuccessResponse(w, http.StatusOK, design)
}

func (c *designController) GetDesignAtVersion(w http.ResponseWriter, r *http.Request) {
	org := r.PathValue("orgHandle")
	project := r.PathValue("projectName")
	if !requireOrgHandle(w, org) || !requireProjectName(w, project) {
		return
	}
	versionStr := r.PathValue("version")

	version, err := strconv.Atoi(versionStr)
	if err != nil {
		utils.WriteErrorResponse(w, http.StatusBadRequest, "invalid version number")
		return
	}

	design, err := c.service.GetDesignAtVersion(r.Context(), org, project, version)
	if err != nil {
		if errors.Is(err, services.ErrDesignNotFound) {
			utils.WriteErrorResponse(w, http.StatusNotFound, "design not found")
			return
		}
		slog.ErrorContext(r.Context(), "get design at version failed", "error", err, "org", org, "project", project, "version", version)
		utils.WriteErrorResponse(w, http.StatusInternalServerError, "failed to get design at version")
		return
	}

	utils.WriteSuccessResponse(w, http.StatusOK, design)
}

func (c *designController) ListDesignVersions(w http.ResponseWriter, r *http.Request) {
	org := r.PathValue("orgHandle")
	project := r.PathValue("projectName")
	if !requireOrgHandle(w, org) || !requireProjectName(w, project) {
		return
	}

	versions, err := c.service.ListDesignVersions(r.Context(), org, project)
	if err != nil {
		slog.ErrorContext(r.Context(), "list design versions failed", "error", err, "org", org, "project", project)
		utils.WriteErrorResponse(w, http.StatusInternalServerError, "failed to list design versions")
		return
	}

	utils.WriteSuccessResponse(w, http.StatusOK, versions)
}

