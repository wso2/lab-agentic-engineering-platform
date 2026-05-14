// Package controllers — per-org Anthropic key surface.
//
// Routes:
//
//	POST   /api/v1/organizations/{orgHandle}/anthropic — connect / replace
//	GET    /api/v1/organizations/{orgHandle}/anthropic — projection
//	DELETE /api/v1/organizations/{orgHandle}/anthropic — disconnect
//
// All routes proxy to git-service's internal credential surface, gated by
// the same Org JWT middleware that protects every other org-scoped route.
// The raw API key never lands in this service's logs or memory beyond the
// inbound request — it's forwarded directly to git-service.
//
// See docs/design/anthropic-key-dual-token.md.
package controllers

import (
	"encoding/json"
	"log/slog"
	"net/http"

	"github.com/wso2/asdlc/asdlc-service/clients/gitservice"
	"github.com/wso2/asdlc/asdlc-service/utils"
)

// OrgAnthropicController owns the per-org Anthropic settings surface.
type OrgAnthropicController interface {
	Connect(w http.ResponseWriter, r *http.Request)
	GetStatus(w http.ResponseWriter, r *http.Request)
	Disconnect(w http.ResponseWriter, r *http.Request)
}

type orgAnthropicController struct {
	gitClient gitservice.Client
}

// NewOrgAnthropicController wires the controller.
func NewOrgAnthropicController(gitClient gitservice.Client) OrgAnthropicController {
	return &orgAnthropicController{gitClient: gitClient}
}

// Connect proxies POST /api/v1/organizations/{orgHandle}/anthropic to git-service.
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
	proj, err := c.gitClient.CreateOrReplaceAnthropic(r.Context(), orgHandle, gitservice.AnthropicConnectRequest{
		APIKey: body.APIKey,
	})
	if err != nil {
		writeProxiedCredentialError(w, err)
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
	proj, err := c.gitClient.GetAnthropicProjection(r.Context(), orgHandle)
	if err != nil {
		if gitservice.IsNotFound(err) {
			utils.WriteSuccessResponse(w, http.StatusOK, map[string]any{
				"ocOrgId": orgHandle,
				"status":  "not_connected",
			})
			return
		}
		writeProxiedCredentialError(w, err)
		return
	}
	utils.WriteSuccessResponse(w, http.StatusOK, proj)
}

// Disconnect proxies DELETE /api/v1/organizations/{orgHandle}/anthropic.
func (c *orgAnthropicController) Disconnect(w http.ResponseWriter, r *http.Request) {
	orgHandle := r.PathValue("orgHandle")
	if !requireOrgHandle(w, orgHandle) {
		return
	}
	if err := c.gitClient.DisconnectAnthropic(r.Context(), orgHandle); err != nil {
		writeProxiedCredentialError(w, err)
		return
	}
	slog.InfoContext(r.Context(), "anthropic.disconnected", "ocOrgId", orgHandle)
	utils.WriteSuccessResponse(w, http.StatusOK, map[string]string{"status": "disconnected"})
}
