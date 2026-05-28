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

// RequirementsChatController handles the chat SSE stream, the per-turn
// undo endpoint, and the per-file session-baseline endpoints (View
// original / Accept / Revert). Auth + path-validation runs through the
// same helpers as the rest of the requirements API surface.
type RequirementsChatController interface {
	StreamChat(w http.ResponseWriter, r *http.Request)
	UndoTurn(w http.ResponseWriter, r *http.Request)
	GetBaselineFile(w http.ResponseWriter, r *http.Request)
	RevertBaselineFile(w http.ResponseWriter, r *http.Request)
	DropBaseline(w http.ResponseWriter, r *http.Request)
}

type requirementsChatController struct {
	service services.RequirementsChatService
}

func NewRequirementsChatController(service services.RequirementsChatService) RequirementsChatController {
	return &requirementsChatController{service: service}
}

func (c *requirementsChatController) StreamChat(w http.ResponseWriter, r *http.Request) {
	org := r.PathValue("orgHandle")
	project := r.PathValue("projectName")
	if !requireOrgHandle(w, org) || !requireProjectName(w, project) {
		return
	}
	var body services.ChatTurnRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		utils.WriteErrorResponse(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if body.Message == "" {
		utils.WriteErrorResponse(w, http.StatusBadRequest, "message is required")
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

	if err := c.service.StreamChat(r.Context(), org, project, body, w, flusher.Flush); err != nil {
		slog.ErrorContext(r.Context(), "requirements chat stream failed",
			"org", org, "project", project, "error", err)
		errFrame, _ := json.Marshal(map[string]any{
			"type":      "error",
			"errorText": err.Error(),
		})
		fmt.Fprintf(w, "data: %s\n\n", errFrame)
		flusher.Flush()
	}
}

// GetBaselineFile returns the content of a single requirement file as
// captured in the chat session's baseline snapshot. Used by "View
// original" in the chat-modified banner.
func (c *requirementsChatController) GetBaselineFile(w http.ResponseWriter, r *http.Request) {
	org := r.PathValue("orgHandle")
	project := r.PathValue("projectName")
	baseline := r.PathValue("baselineId")
	filename := r.PathValue("name")
	if !requireOrgHandle(w, org) || !requireProjectName(w, project) || baseline == "" || filename == "" {
		utils.WriteErrorResponse(w, http.StatusBadRequest, "baselineId and filename are required")
		return
	}
	res, err := c.service.GetSessionBaselineFile(r.Context(), org, project, baseline, filename)
	if err != nil {
		if errors.Is(err, services.ErrArtifactNotFound) {
			utils.WriteErrorResponse(w, http.StatusNotFound, "baseline snapshot not found")
			return
		}
		slog.ErrorContext(r.Context(), "get baseline file failed",
			"org", org, "project", project, "baseline", baseline, "filename", filename, "error", err)
		utils.WriteErrorResponse(w, http.StatusInternalServerError, err.Error())
		return
	}
	utils.WriteSuccessResponse(w, http.StatusOK, res)
}

// RevertBaselineFile rewrites a single requirement file back to the
// content captured in the session baseline (or deletes it if the file
// didn't exist at baseline). Runs under the requirements dir lock.
func (c *requirementsChatController) RevertBaselineFile(w http.ResponseWriter, r *http.Request) {
	org := r.PathValue("orgHandle")
	project := r.PathValue("projectName")
	baseline := r.PathValue("baselineId")
	filename := r.PathValue("name")
	if !requireOrgHandle(w, org) || !requireProjectName(w, project) || baseline == "" || filename == "" {
		utils.WriteErrorResponse(w, http.StatusBadRequest, "baselineId and filename are required")
		return
	}
	if err := c.service.RevertFileToBaseline(r.Context(), org, project, baseline, filename); err != nil {
		if errors.Is(err, services.RequirementsDirLockBusy) {
			utils.WriteErrorResponse(w, http.StatusConflict, "chat_in_progress")
			return
		}
		if errors.Is(err, services.ErrArtifactNotFound) {
			utils.WriteErrorResponse(w, http.StatusNotFound, "baseline snapshot not found")
			return
		}
		slog.ErrorContext(r.Context(), "revert baseline file failed",
			"org", org, "project", project, "baseline", baseline, "filename", filename, "error", err)
		utils.WriteErrorResponse(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// DropBaseline deletes the session-baseline snapshot blob. Idempotent —
// 204 even if the snapshot did not exist. Called by the console after the
// last modified file in a session is Accepted.
func (c *requirementsChatController) DropBaseline(w http.ResponseWriter, r *http.Request) {
	org := r.PathValue("orgHandle")
	project := r.PathValue("projectName")
	baseline := r.PathValue("baselineId")
	if !requireOrgHandle(w, org) || !requireProjectName(w, project) || baseline == "" {
		utils.WriteErrorResponse(w, http.StatusBadRequest, "baselineId is required")
		return
	}
	if err := c.service.DropSessionBaseline(r.Context(), org, project, baseline); err != nil {
		slog.ErrorContext(r.Context(), "drop baseline failed",
			"org", org, "project", project, "baseline", baseline, "error", err)
		utils.WriteErrorResponse(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (c *requirementsChatController) UndoTurn(w http.ResponseWriter, r *http.Request) {
	org := r.PathValue("orgHandle")
	project := r.PathValue("projectName")
	turn := r.PathValue("turnId")
	if !requireOrgHandle(w, org) || !requireProjectName(w, project) || turn == "" {
		utils.WriteErrorResponse(w, http.StatusBadRequest, "turnId is required")
		return
	}
	files, err := c.service.UndoTurn(r.Context(), org, project, turn)
	if err != nil {
		slog.ErrorContext(r.Context(), "requirements chat undo failed",
			"org", org, "project", project, "turn", turn, "error", err)
		utils.WriteErrorResponse(w, http.StatusInternalServerError, err.Error())
		return
	}
	utils.WriteSuccessResponse(w, http.StatusOK, map[string]any{
		"files": files,
	})
}
