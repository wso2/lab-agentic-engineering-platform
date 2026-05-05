package controllers

import (
	"errors"
	"log/slog"
	"net/http"

	"github.com/wso2/asdlc/git-service/services"
	"github.com/wso2/asdlc/git-service/utils"
)

// WebhookRegistrationController handles per-repo GitHub webhook registration.
//
// This is distinct from the BFF's inbound webhook receiver — this controller
// is what the BFF calls when provisioning a repo to install the hook on
// GitHub's side.
type WebhookRegistrationController interface {
	Register(w http.ResponseWriter, r *http.Request)
	Deregister(w http.ResponseWriter, r *http.Request)
}

type webhookRegistrationController struct {
	service services.WebhookService
}

func NewWebhookRegistrationController(service services.WebhookService) WebhookRegistrationController {
	return &webhookRegistrationController{service: service}
}

type registerWebhookResponse struct {
	HookID *int64 `json:"hookId,omitempty"`
	// Strategy reports which strategy the credential dispatched. "per-repo"
	// when a hook ID is returned; "platform" when the call was a no-op
	// (Phase 2 App-mode platform-wide delivery).
	Strategy string `json:"strategy"`
}

func (c *webhookRegistrationController) Register(w http.ResponseWriter, r *http.Request) {
	projectID := r.PathValue("projectId")
	if !requireProjectIDSlug(w, projectID) {
		return
	}

	hookID, err := c.service.Register(r.Context(), projectID)
	if err != nil {
		if errors.Is(err, services.ErrRepoNotFound) {
			utils.WriteErrorResponse(w, http.StatusNotFound, "repository not found")
			return
		}
		slog.ErrorContext(r.Context(), "register webhook failed", "project", projectID, "error", err)
		utils.WriteErrorResponse(w, http.StatusInternalServerError, "failed to register webhook")
		return
	}

	resp := registerWebhookResponse{HookID: hookID, Strategy: "per-repo"}
	if hookID == nil {
		resp.Strategy = "platform"
	}
	utils.WriteSuccessResponse(w, http.StatusCreated, resp)
}

func (c *webhookRegistrationController) Deregister(w http.ResponseWriter, r *http.Request) {
	projectID := r.PathValue("projectId")
	if !requireProjectIDSlug(w, projectID) {
		return
	}

	if err := c.service.Deregister(r.Context(), projectID); err != nil {
		if errors.Is(err, services.ErrRepoNotFound) {
			utils.WriteErrorResponse(w, http.StatusNotFound, "repository not found")
			return
		}
		slog.ErrorContext(r.Context(), "deregister webhook failed", "project", projectID, "error", err)
		utils.WriteErrorResponse(w, http.StatusInternalServerError, "failed to deregister webhook")
		return
	}

	w.WriteHeader(http.StatusNoContent)
}
