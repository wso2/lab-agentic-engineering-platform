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

// RequirementsController serves the multi-file requirements endpoints.
// Skill routing for per-file generation is performed BFF-side from the
// document-type registry passed in the generate request body.
type RequirementsController interface {
	GetRequirements(w http.ResponseWriter, r *http.Request)
	UpdateRequirementFile(w http.ResponseWriter, r *http.Request)
	DeleteRequirementFile(w http.ResponseWriter, r *http.Request)
	SaveAndProceed(w http.ResponseWriter, r *http.Request)
	DiscardChanges(w http.ResponseWriter, r *http.Request)
	GenerateRequirementFile(w http.ResponseWriter, r *http.Request)
	ListVersions(w http.ResponseWriter, r *http.Request)
	GetRequirementsAtVersion(w http.ResponseWriter, r *http.Request)
}

type requirementsController struct {
	service services.RequirementsService
}

func NewRequirementsController(service services.RequirementsService) RequirementsController {
	return &requirementsController{service: service}
}

func (c *requirementsController) GetRequirements(w http.ResponseWriter, r *http.Request) {
	org := r.PathValue("orgHandle")
	project := r.PathValue("projectName")
	if !requireOrgHandle(w, org) || !requireProjectName(w, project) {
		return
	}

	out, err := c.service.GetRequirements(r.Context(), org, project)
	if err != nil {
		slog.ErrorContext(r.Context(), "get requirements failed", "error", err, "org", org, "project", project)
		utils.WriteErrorResponse(w, http.StatusInternalServerError, "failed to get requirements")
		return
	}
	utils.WriteSuccessResponse(w, http.StatusOK, out)
}

func (c *requirementsController) UpdateRequirementFile(w http.ResponseWriter, r *http.Request) {
	org := r.PathValue("orgHandle")
	project := r.PathValue("projectName")
	name := r.PathValue("name")
	if !requireOrgHandle(w, org) || !requireProjectName(w, project) || name == "" {
		utils.WriteErrorResponse(w, http.StatusBadRequest, "filename is required")
		return
	}
	var body struct {
		Content string `json:"content"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		utils.WriteErrorResponse(w, http.StatusBadRequest, "invalid request body")
		return
	}
	out, err := c.service.UpdateRequirementFile(r.Context(), org, project, name, body.Content)
	if err != nil {
		slog.ErrorContext(r.Context(), "update requirement file failed", "error", err, "org", org, "project", project, "name", name)
		utils.WriteErrorResponse(w, http.StatusInternalServerError, "failed to update requirement file")
		return
	}
	utils.WriteSuccessResponse(w, http.StatusOK, out)
}

func (c *requirementsController) DeleteRequirementFile(w http.ResponseWriter, r *http.Request) {
	org := r.PathValue("orgHandle")
	project := r.PathValue("projectName")
	name := r.PathValue("name")
	if !requireOrgHandle(w, org) || !requireProjectName(w, project) || name == "" {
		utils.WriteErrorResponse(w, http.StatusBadRequest, "filename is required")
		return
	}
	out, err := c.service.DeleteRequirementFile(r.Context(), org, project, name)
	if err != nil {
		slog.ErrorContext(r.Context(), "delete requirement file failed", "error", err, "org", org, "project", project, "name", name)
		utils.WriteErrorResponse(w, http.StatusInternalServerError, err.Error())
		return
	}
	utils.WriteSuccessResponse(w, http.StatusOK, out)
}

func (c *requirementsController) SaveAndProceed(w http.ResponseWriter, r *http.Request) {
	org := r.PathValue("orgHandle")
	project := r.PathValue("projectName")
	if !requireOrgHandle(w, org) || !requireProjectName(w, project) {
		return
	}
	out, err := c.service.SaveAndProceed(r.Context(), org, project)
	if err != nil {
		if errors.Is(err, services.ErrSpecNotFound) {
			utils.WriteErrorResponse(w, http.StatusNotFound, "requirements not found")
			return
		}
		slog.ErrorContext(r.Context(), "save requirements failed", "error", err, "org", org, "project", project)
		utils.WriteErrorResponse(w, http.StatusInternalServerError, "failed to save requirements")
		return
	}
	utils.WriteSuccessResponse(w, http.StatusOK, out)
}

func (c *requirementsController) DiscardChanges(w http.ResponseWriter, r *http.Request) {
	org := r.PathValue("orgHandle")
	project := r.PathValue("projectName")
	if !requireOrgHandle(w, org) || !requireProjectName(w, project) {
		return
	}
	out, err := c.service.DiscardChanges(r.Context(), org, project)
	if err != nil {
		slog.ErrorContext(r.Context(), "discard requirements failed", "error", err, "org", org, "project", project)
		utils.WriteErrorResponse(w, http.StatusInternalServerError, "failed to discard requirements")
		return
	}
	utils.WriteSuccessResponse(w, http.StatusOK, out)
}

// GenerateRequirementFile streams a document-generation skill into the
// named target file. The body specifies the skill ID, optional source
// filenames, and an optional user prompt (for bootstrap skills).
func (c *requirementsController) GenerateRequirementFile(w http.ResponseWriter, r *http.Request) {
	org := r.PathValue("orgHandle")
	project := r.PathValue("projectName")
	name := r.PathValue("name")
	if !requireOrgHandle(w, org) || !requireProjectName(w, project) || name == "" {
		utils.WriteErrorResponse(w, http.StatusBadRequest, "filename is required")
		return
	}
	var body struct {
		SkillID string   `json:"skillId"`
		Sources []string `json:"sources,omitempty"`
		Prompt  string   `json:"prompt,omitempty"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		utils.WriteErrorResponse(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if body.SkillID == "" {
		utils.WriteErrorResponse(w, http.StatusBadRequest, "skillId is required")
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

	if err := c.service.StreamGenerate(r.Context(), org, project, name, body.SkillID, body.Sources, body.Prompt, w, flusher.Flush); err != nil {
		slog.ErrorContext(r.Context(), "generate requirement file failed", "error", err, "org", org, "project", project, "name", name)
		errFrame, _ := json.Marshal(map[string]any{"type": "error", "errorText": err.Error()})
		fmt.Fprintf(w, "data: %s\n\n", errFrame)
		flusher.Flush()
	}
}

func (c *requirementsController) ListVersions(w http.ResponseWriter, r *http.Request) {
	org := r.PathValue("orgHandle")
	project := r.PathValue("projectName")
	if !requireOrgHandle(w, org) || !requireProjectName(w, project) {
		return
	}
	versions, err := c.service.ListVersions(r.Context(), org, project)
	if err != nil {
		slog.ErrorContext(r.Context(), "list requirements versions failed", "error", err, "org", org, "project", project)
		utils.WriteErrorResponse(w, http.StatusInternalServerError, "failed to list versions")
		return
	}
	utils.WriteSuccessResponse(w, http.StatusOK, versions)
}

func (c *requirementsController) GetRequirementsAtVersion(w http.ResponseWriter, r *http.Request) {
	org := r.PathValue("orgHandle")
	project := r.PathValue("projectName")
	tag := r.PathValue("tag")
	if !requireOrgHandle(w, org) || !requireProjectName(w, project) || tag == "" {
		utils.WriteErrorResponse(w, http.StatusBadRequest, "tag is required")
		return
	}
	out, err := c.service.GetRequirementsAtTag(r.Context(), org, project, tag)
	if err != nil {
		if errors.Is(err, services.ErrSpecNotFound) {
			utils.WriteErrorResponse(w, http.StatusNotFound, "requirements not found at tag")
			return
		}
		slog.ErrorContext(r.Context(), "get requirements at tag failed", "error", err, "org", org, "project", project, "tag", tag)
		utils.WriteErrorResponse(w, http.StatusInternalServerError, "failed to get requirements at tag")
		return
	}
	utils.WriteSuccessResponse(w, http.StatusOK, out)
}
