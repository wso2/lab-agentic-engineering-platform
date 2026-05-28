// Package controllers — per-org Anthropic key surface.
//
// Routes:
//
//	POST   /api/v1/organizations/{orgHandle}/anthropic — connect / replace
//	GET    /api/v1/organizations/{orgHandle}/anthropic — projection
//	DELETE /api/v1/organizations/{orgHandle}/anthropic — disconnect
//
// All routes are gated by the same Org JWT middleware that protects every
// other org-scoped route. The raw API key never lands in this service's
// logs or memory beyond the inbound request.
//
// See docs/design/anthropic-key-dual-token.md.
package controllers

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"

	"github.com/wso2/asdlc/asdlc-service/services"
	"github.com/wso2/asdlc/asdlc-service/utils"
)

// OrgAnthropicController owns the per-org Anthropic settings surface.
type OrgAnthropicController interface {
	Connect(w http.ResponseWriter, r *http.Request)
	GetStatus(w http.ResponseWriter, r *http.Request)
	Disconnect(w http.ResponseWriter, r *http.Request)
}

type orgAnthropicController struct {
	anthropicSvc *services.AnthropicCredentialService
}

// NewOrgAnthropicController wires the controller.
func NewOrgAnthropicController(anthropicSvc *services.AnthropicCredentialService) OrgAnthropicController {
	return &orgAnthropicController{anthropicSvc: anthropicSvc}
}

// Connect handles POST /api/v1/organizations/{orgHandle}/anthropic.
// Body: { "apiKey": "sk-ant-..." }. Returns the projection (no key bytes).
func (c *orgAnthropicController) Connect(w http.ResponseWriter, r *http.Request) {
	orgHandle := r.PathValue("orgHandle")
	if !requireOrgHandle(w, orgHandle) {
		return
	}
	var body struct {
		APIKey string `json:"apiKey"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		utils.WriteErrorResponse(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	proj, err := c.anthropicSvc.Connect(r.Context(), orgHandle, services.AnthropicConnectRequest{
		APIKey: body.APIKey,
	})
	if err != nil {
		writeCredentialServiceError(w, err)
		return
	}
	slog.InfoContext(r.Context(), "anthropic.connected", "ocOrgId", orgHandle, "keyPrefix", proj.KeyPrefix)
	utils.WriteSuccessResponse(w, http.StatusOK, proj)
}

// GetStatus returns the projection. Renders {status:"not_connected"} when
// no row exists so the console can show the "Configure" panel without
// surfacing an error.
func (c *orgAnthropicController) GetStatus(w http.ResponseWriter, r *http.Request) {
	orgHandle := r.PathValue("orgHandle")
	if !requireOrgHandle(w, orgHandle) {
		return
	}
	proj, err := c.anthropicSvc.Status(r.Context(), orgHandle)
	if err != nil {
		var nfe *services.NotFoundError
		if errors.As(err, &nfe) {
			utils.WriteSuccessResponse(w, http.StatusOK, map[string]any{
				"ocOrgId": orgHandle,
				"status":  "not_connected",
			})
			return
		}
		writeCredentialServiceError(w, err)
		return
	}
	utils.WriteSuccessResponse(w, http.StatusOK, proj)
}

// Disconnect handles DELETE /api/v1/organizations/{orgHandle}/anthropic.
func (c *orgAnthropicController) Disconnect(w http.ResponseWriter, r *http.Request) {
	orgHandle := r.PathValue("orgHandle")
	if !requireOrgHandle(w, orgHandle) {
		return
	}
	if err := c.anthropicSvc.Disconnect(r.Context(), orgHandle); err != nil {
		writeCredentialServiceError(w, err)
		return
	}
	slog.InfoContext(r.Context(), "anthropic.disconnected", "ocOrgId", orgHandle)
	utils.WriteSuccessResponse(w, http.StatusOK, map[string]string{"status": "disconnected"})
}
