package controllers

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"

	"github.com/wso2/asdlc/asdlc-service/services"
	"github.com/wso2/asdlc/asdlc-service/utils"
)

type DesignController interface {
	GetDesign(w http.ResponseWriter, r *http.Request)
	GetDesignBundle(w http.ResponseWriter, r *http.Request)
	GenerateDesign(w http.ResponseWriter, r *http.Request)
	UpdateDesignFile(w http.ResponseWriter, r *http.Request)
	DeleteDesignFile(w http.ResponseWriter, r *http.Request)
	DeleteComponent(w http.ResponseWriter, r *http.Request)
	SaveAndProceed(w http.ResponseWriter, r *http.Request)
	DiscardChanges(w http.ResponseWriter, r *http.Request)
	GetDesignAtTag(w http.ResponseWriter, r *http.Request)
	GetDesignBundleAtTag(w http.ResponseWriter, r *http.Request)
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
		if errors.Is(err, services.ErrSpecNotApproved) {
			utils.WriteErrorResponse(w, http.StatusConflict, "save requirements first — no v<N> baseline tag")
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

func (c *designController) GetDesignAtTag(w http.ResponseWriter, r *http.Request) {
	org := r.PathValue("orgHandle")
	project := r.PathValue("projectName")
	if !requireOrgHandle(w, org) || !requireProjectName(w, project) {
		return
	}
	tag := r.PathValue("tag")
	if tag == "" {
		utils.WriteErrorResponse(w, http.StatusBadRequest, "tag is required")
		return
	}

	design, err := c.service.GetDesignAtTag(r.Context(), org, project, tag)
	if err != nil {
		if errors.Is(err, services.ErrDesignNotFound) {
			utils.WriteErrorResponse(w, http.StatusNotFound, "design not found")
			return
		}
		slog.ErrorContext(r.Context(), "get design at tag failed", "error", err, "org", org, "project", project, "tag", tag)
		utils.WriteErrorResponse(w, http.StatusInternalServerError, "failed to get design at tag")
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

// GetDesignBundle returns the file map + assembled Design in one shot for
// the architecture page Explorer + cell diagram.
func (c *designController) GetDesignBundle(w http.ResponseWriter, r *http.Request) {
	org := r.PathValue("orgHandle")
	project := r.PathValue("projectName")
	if !requireOrgHandle(w, org) || !requireProjectName(w, project) {
		return
	}
	bundle, err := c.service.GetDesignBundle(r.Context(), org, project)
	if err != nil {
		slog.ErrorContext(r.Context(), "get design bundle failed", "error", err, "org", org, "project", project)
		utils.WriteErrorResponse(w, http.StatusInternalServerError, "failed to get design bundle")
		return
	}
	utils.WriteSuccessResponse(w, http.StatusOK, bundle)
}

// GetDesignBundleAtTag returns the file map + assembled Design at a tag.
func (c *designController) GetDesignBundleAtTag(w http.ResponseWriter, r *http.Request) {
	org := r.PathValue("orgHandle")
	project := r.PathValue("projectName")
	if !requireOrgHandle(w, org) || !requireProjectName(w, project) {
		return
	}
	tag := r.PathValue("tag")
	if tag == "" {
		utils.WriteErrorResponse(w, http.StatusBadRequest, "tag is required")
		return
	}
	bundle, err := c.service.GetDesignBundleAtTag(r.Context(), org, project, tag)
	if err != nil {
		if errors.Is(err, services.ErrDesignNotFound) {
			utils.WriteErrorResponse(w, http.StatusNotFound, "design not found")
			return
		}
		slog.ErrorContext(r.Context(), "get design bundle at tag failed", "error", err, "org", org, "project", project, "tag", tag)
		utils.WriteErrorResponse(w, http.StatusInternalServerError, "failed to get design bundle at tag")
		return
	}
	utils.WriteSuccessResponse(w, http.StatusOK, bundle)
}

// UpdateDesignFile writes a single file under specs/design/.
func (c *designController) UpdateDesignFile(w http.ResponseWriter, r *http.Request) {
	org := r.PathValue("orgHandle")
	project := r.PathValue("projectName")
	if !requireOrgHandle(w, org) || !requireProjectName(w, project) {
		return
	}
	subPath := r.PathValue("path")
	if subPath == "" {
		utils.WriteErrorResponse(w, http.StatusBadRequest, "path is required")
		return
	}
	var body struct {
		Content string `json:"content"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		utils.WriteErrorResponse(w, http.StatusBadRequest, "invalid request body")
		return
	}
	bundle, err := c.service.UpdateDesignFile(r.Context(), org, project, subPath, body.Content)
	if err != nil {
		slog.ErrorContext(r.Context(), "update design file failed", "error", err, "path", subPath)
		utils.WriteErrorResponse(w, http.StatusInternalServerError, "failed to update design file")
		return
	}
	utils.WriteSuccessResponse(w, http.StatusOK, bundle)
}

// DeleteDesignFile removes a single file under specs/design/. Refuses to
// delete the root design.md.
func (c *designController) DeleteDesignFile(w http.ResponseWriter, r *http.Request) {
	org := r.PathValue("orgHandle")
	project := r.PathValue("projectName")
	if !requireOrgHandle(w, org) || !requireProjectName(w, project) {
		return
	}
	subPath := r.PathValue("path")
	if subPath == "" {
		utils.WriteErrorResponse(w, http.StatusBadRequest, "path is required")
		return
	}
	bundle, err := c.service.DeleteDesignFile(r.Context(), org, project, subPath)
	if err != nil {
		slog.ErrorContext(r.Context(), "delete design file failed", "error", err, "path", subPath)
		utils.WriteErrorResponse(w, http.StatusInternalServerError, "failed to delete design file")
		return
	}
	utils.WriteSuccessResponse(w, http.StatusOK, bundle)
}

// DeleteComponent removes the entire components/<name>/ directory.
func (c *designController) DeleteComponent(w http.ResponseWriter, r *http.Request) {
	org := r.PathValue("orgHandle")
	project := r.PathValue("projectName")
	if !requireOrgHandle(w, org) || !requireProjectName(w, project) {
		return
	}
	name := r.PathValue("componentName")
	if name == "" {
		utils.WriteErrorResponse(w, http.StatusBadRequest, "componentName is required")
		return
	}
	bundle, err := c.service.DeleteComponent(r.Context(), org, project, name)
	if err != nil {
		slog.ErrorContext(r.Context(), "delete component failed", "error", err, "component", name)
		utils.WriteErrorResponse(w, http.StatusInternalServerError, "failed to delete component")
		return
	}
	utils.WriteSuccessResponse(w, http.StatusOK, bundle)
}

