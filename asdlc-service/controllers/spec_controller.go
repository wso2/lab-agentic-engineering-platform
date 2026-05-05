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

type SpecController interface {
	GetSpec(w http.ResponseWriter, r *http.Request)
	UpdateSpec(w http.ResponseWriter, r *http.Request)
	SaveAndProceed(w http.ResponseWriter, r *http.Request)
	GenerateSpec(w http.ResponseWriter, r *http.Request)
	DiscardChanges(w http.ResponseWriter, r *http.Request)
	GetSpecAtVersion(w http.ResponseWriter, r *http.Request)
	ListSpecVersions(w http.ResponseWriter, r *http.Request)
	GetSpecWireframe(w http.ResponseWriter, r *http.Request)
	GenerateSpecWireframe(w http.ResponseWriter, r *http.Request)
}

type specController struct {
	service services.SpecService
}

func NewSpecController(service services.SpecService) SpecController {
	return &specController{service: service}
}

func (c *specController) GetSpec(w http.ResponseWriter, r *http.Request) {
	org := r.PathValue("orgHandle")
	project := r.PathValue("projectName")
	if !requireOrgHandle(w, org) || !requireProjectName(w, project) {
		return
	}

	spec, err := c.service.GetSpec(r.Context(), org, project)
	if err != nil {
		slog.ErrorContext(r.Context(), "get spec failed", "error", err, "org", org, "project", project)
		utils.WriteErrorResponse(w, http.StatusInternalServerError, "failed to get spec")
		return
	}

	if spec == nil {
		utils.WriteSuccessResponse(w, http.StatusOK, nil)
		return
	}
	utils.WriteSuccessResponse(w, http.StatusOK, spec)
}

func (c *specController) UpdateSpec(w http.ResponseWriter, r *http.Request) {
	org := r.PathValue("orgHandle")
	project := r.PathValue("projectName")
	if !requireOrgHandle(w, org) || !requireProjectName(w, project) {
		return
	}

	var body struct {
		Content string `json:"content"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		utils.WriteErrorResponse(w, http.StatusBadRequest, "invalid request body")
		return
	}

	spec, err := c.service.UpdateSpec(r.Context(), org, project, body.Content)
	if err != nil {
		slog.ErrorContext(r.Context(), "update spec failed", "error", err, "org", org, "project", project)
		utils.WriteErrorResponse(w, http.StatusInternalServerError, "failed to update spec")
		return
	}

	utils.WriteSuccessResponse(w, http.StatusOK, spec)
}

func (c *specController) SaveAndProceed(w http.ResponseWriter, r *http.Request) {
	org := r.PathValue("orgHandle")
	project := r.PathValue("projectName")
	if !requireOrgHandle(w, org) || !requireProjectName(w, project) {
		return
	}

	spec, err := c.service.SaveAndProceed(r.Context(), org, project)
	if err != nil {
		if errors.Is(err, services.ErrSpecNotFound) {
			utils.WriteErrorResponse(w, http.StatusNotFound, "spec not found")
			return
		}
		if errors.Is(err, services.ErrSpecEmpty) {
			utils.WriteErrorResponse(w, http.StatusBadRequest, "spec content is empty")
			return
		}
		slog.ErrorContext(r.Context(), "save and proceed spec failed", "error", err, "org", org, "project", project)
		utils.WriteErrorResponse(w, http.StatusInternalServerError, "failed to save and proceed spec")
		return
	}

	utils.WriteSuccessResponse(w, http.StatusOK, spec)
}

func (c *specController) GenerateSpec(w http.ResponseWriter, r *http.Request) {
	org := r.PathValue("orgHandle")
	project := r.PathValue("projectName")
	if !requireOrgHandle(w, org) || !requireProjectName(w, project) {
		return
	}

	var body struct {
		Prompt string `json:"prompt"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		utils.WriteErrorResponse(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if body.Prompt == "" {
		utils.WriteErrorResponse(w, http.StatusBadRequest, "prompt is required")
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

	if err := c.service.StreamGenerateSpec(r.Context(), org, project, body.Prompt, w, flusher.Flush); err != nil {
		slog.ErrorContext(r.Context(), "generate spec failed", "error", err, "org", org, "project", project)
		// Headers already sent — surface the failure as a UI Message Stream error frame.
		errFrame, _ := json.Marshal(map[string]any{"type": "error", "errorText": err.Error()})
		fmt.Fprintf(w, "data: %s\n\n", errFrame)
		flusher.Flush()
	}
}

func (c *specController) DiscardChanges(w http.ResponseWriter, r *http.Request) {
	org := r.PathValue("orgHandle")
	project := r.PathValue("projectName")
	if !requireOrgHandle(w, org) || !requireProjectName(w, project) {
		return
	}

	spec, err := c.service.DiscardChanges(r.Context(), org, project)
	if err != nil {
		slog.ErrorContext(r.Context(), "discard spec changes failed", "error", err, "org", org, "project", project)
		utils.WriteErrorResponse(w, http.StatusInternalServerError, "failed to discard spec changes")
		return
	}

	utils.WriteSuccessResponse(w, http.StatusOK, spec)
}

func (c *specController) GetSpecAtVersion(w http.ResponseWriter, r *http.Request) {
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

	spec, err := c.service.GetSpecAtVersion(r.Context(), org, project, version)
	if err != nil {
		if errors.Is(err, services.ErrSpecNotFound) {
			utils.WriteErrorResponse(w, http.StatusNotFound, "spec not found")
			return
		}
		slog.ErrorContext(r.Context(), "get spec at version failed", "error", err, "org", org, "project", project, "version", version)
		utils.WriteErrorResponse(w, http.StatusInternalServerError, "failed to get spec at version")
		return
	}

	utils.WriteSuccessResponse(w, http.StatusOK, spec)
}

func (c *specController) GetSpecWireframe(w http.ResponseWriter, r *http.Request) {
	org := r.PathValue("orgHandle")
	project := r.PathValue("projectName")
	if !requireOrgHandle(w, org) || !requireProjectName(w, project) {
		return
	}

	content, err := c.service.GetSpecWireframe(r.Context(), org, project)
	if err != nil {
		if errors.Is(err, services.ErrSpecNotFound) {
			utils.WriteErrorResponse(w, http.StatusNotFound, "spec not found")
			return
		}
		if errors.Is(err, services.ErrWireframeNotGenerated) {
			utils.WriteSuccessResponse(w, http.StatusNotFound, map[string]string{"status": "not_generated"})
			return
		}
		if errors.Is(err, services.ErrWireframeGenerating) {
			utils.WriteSuccessResponse(w, http.StatusAccepted, map[string]string{"status": "generating"})
			return
		}
		slog.ErrorContext(r.Context(), "get spec wireframe failed", "error", err, "org", org, "project", project)
		utils.WriteErrorResponse(w, http.StatusInternalServerError, err.Error())
		return
	}

	w.Header().Set("Content-Type", "text/html")
	w.WriteHeader(http.StatusOK)
	w.Write([]byte(content)) //nolint:errcheck
}

func (c *specController) GenerateSpecWireframe(w http.ResponseWriter, r *http.Request) {
	org := r.PathValue("orgHandle")
	project := r.PathValue("projectName")
	if !requireOrgHandle(w, org) || !requireProjectName(w, project) {
		return
	}

	c.service.StartGenerateSpecWireframe(r.Context(), org, project)
	utils.WriteSuccessResponse(w, http.StatusAccepted, map[string]string{"status": "generating"})
}

func (c *specController) ListSpecVersions(w http.ResponseWriter, r *http.Request) {
	org := r.PathValue("orgHandle")
	project := r.PathValue("projectName")
	if !requireOrgHandle(w, org) || !requireProjectName(w, project) {
		return
	}

	versions, err := c.service.ListSpecVersions(r.Context(), org, project)
	if err != nil {
		slog.ErrorContext(r.Context(), "list spec versions failed", "error", err, "org", org, "project", project)
		utils.WriteErrorResponse(w, http.StatusInternalServerError, "failed to list spec versions")
		return
	}

	utils.WriteSuccessResponse(w, http.StatusOK, versions)
}
