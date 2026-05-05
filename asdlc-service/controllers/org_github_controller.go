// Package controllers — phase 2's org-scoped GitHub integration surface.
//
// Routes:
//   POST   /api/v1/organizations/{orgHandle}/github/connect/start  — start App connect (OAuth-driven)
//   GET    /api/v1/github/connect/callback?...                     — App OAuth + post-install callback (unscoped)
//   POST   /api/v1/organizations/{orgHandle}/github/pat            — PAT-mode connect / replace
//   GET    /api/v1/organizations/{orgHandle}/github                — projection (no token)
//   DELETE /api/v1/organizations/{orgHandle}/github                — disconnect cascade
//
// Architecture: connect is fully binding-centric. Every App-mode connect
// goes through GitHub OAuth first; the callback intersects /user/installations
// with our App's installs and binds only what the requesting user actually
// administers. There is no platform-wide "discover unbound installs"
// surface — that would leak install metadata across tenants.
package controllers

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/wso2/asdlc/asdlc-service/clients/gitservice"
	"github.com/wso2/asdlc/asdlc-service/services"
	"github.com/wso2/asdlc/asdlc-service/utils"
)

// OrgGitHubController handles the per-org GitHub connect / status /
// disconnect surface.
type OrgGitHubController interface {
	StartConnect(w http.ResponseWriter, r *http.Request)
	HandleConnectCallback(w http.ResponseWriter, r *http.Request)
	ConnectPAT(w http.ResponseWriter, r *http.Request)
	GetStatus(w http.ResponseWriter, r *http.Request)
	Disconnect(w http.ResponseWriter, r *http.Request)
}

type orgGitHubController struct {
	gitClient    gitservice.Client
	disconnectSv *services.OrgDisconnectService
	bearerSvc    *services.BearerService
	appSlug      string
	publicURL    string // for the post-callback redirect
	// appClientID is the GitHub App's OAuth client_id used to build the
	// authorize URL. Empty disables the App-mode connect path (StartConnect 503).
	appClientID string
}

// NewOrgGitHubController constructs the controller. publicURL is the
// user-visible BFF base URL (default http://localhost:8090 for dev — the
// console nginx proxies /api/* through to the BFF, so 8090 is correct).
// appClientID is the GitHub App's OAuth client_id; empty disables
// App-mode connect.
func NewOrgGitHubController(
	gitClient gitservice.Client,
	disconnectSv *services.OrgDisconnectService,
	bearerSvc *services.BearerService,
	appSlug, publicURL, appClientID string,
) OrgGitHubController {
	if appSlug == "" {
		appSlug = "asdlc-platform"
	}
	if publicURL == "" {
		publicURL = "http://localhost:8090"
	}
	return &orgGitHubController{
		gitClient:    gitClient,
		disconnectSv: disconnectSv,
		bearerSvc:    bearerSvc,
		appSlug:      appSlug,
		publicURL:    publicURL,
		appClientID:  appClientID,
	}
}

// connectCallbackPath is the single GitHub-side callback URL configured
// in both the App's "Setup URL" and "Callback URL" fields. Constant so
// the OAuth authorize URL and the redirect_uri in the code exchange
// always match (GitHub enforces exact equality).
const connectCallbackPath = "/api/v1/github/connect/callback"

// StartConnect signs a connect-state JWT and returns the GitHub OAuth
// authorize URL. The state JWT carries the OC org and (optionally) a
// pinned installationID — the latter is set when the user picks a
// candidate from the 2+ picker page so the callback knows which install
// the user intends to bind.
func (c *orgGitHubController) StartConnect(w http.ResponseWriter, r *http.Request) {
	orgHandle := r.PathValue("orgHandle")
	if !requireOrgHandle(w, orgHandle) {
		return
	}
	if c.appClientID == "" {
		utils.WriteErrorResponse(w, http.StatusServiceUnavailable, "github app oauth client not configured")
		return
	}

	var body struct {
		InstallationID int64 `json:"installationId"`
	}
	if r.ContentLength > 0 {
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			utils.WriteErrorResponse(w, http.StatusBadRequest, "invalid JSON body")
			return
		}
	}

	actor := actorFromContext(r.Context())
	state, err := c.bearerSvc.IssueConnectState(orgHandle, actor, body.InstallationID, 15*time.Minute)
	if err != nil {
		slog.ErrorContext(r.Context(), "issue connect-state failed", "error", err, "org", orgHandle)
		utils.WriteErrorResponse(w, http.StatusInternalServerError, "could not start connect")
		return
	}

	redirectURI := c.publicURL + connectCallbackPath
	authorizeURL := "https://github.com/login/oauth/authorize?client_id=" + url.QueryEscape(c.appClientID) +
		"&redirect_uri=" + url.QueryEscape(redirectURI) +
		"&state=" + url.QueryEscape(state)
	utils.WriteSuccessResponse(w, http.StatusOK, map[string]string{"authorizeUrl": authorizeURL})
}

// HandleConnectCallback is the single callback for every App-mode connect
// roundtrip. Three shapes arrive at this endpoint:
//
//   - ?code present → OAuth callback. Resolve user's installs, then either
//     bind directly (1 candidate), redirect to install flow (0 candidates),
//     or send to the picker (2+ candidates). When the state JWT pinned an
//     installationID (picker re-OAuth), verify it's in the candidates and
//     bind it.
//   - ?installation_id present → post-install callback. The user just
//     installed the App on a GitHub org via the install flow; bind directly
//     (GitHub enforces installer-is-admin).
//   - neither → invalid; redirect with an error.
func (c *orgGitHubController) HandleConnectCallback(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	state := q.Get("state")
	if state == "" {
		http.Error(w, "missing state", http.StatusBadRequest)
		return
	}
	claims, err := c.bearerSvc.VerifyConnectState(state)
	if err != nil {
		slog.WarnContext(r.Context(), "connect callback: invalid state", "error", err)
		http.Error(w, "invalid state: "+err.Error(), http.StatusBadRequest)
		return
	}
	settingsURL := c.publicURL + "/organizations/" + claims.OcOrgID + "/settings/github"

	if code := q.Get("code"); code != "" {
		c.handleOAuthCallback(w, r, claims, code, settingsURL)
		return
	}
	if idStr := q.Get("installation_id"); idStr != "" {
		c.handlePostInstallCallback(w, r, claims, idStr, settingsURL)
		return
	}
	http.Redirect(w, r, settingsURL+"?error=callback_invalid", http.StatusSeeOther)
}

// handleOAuthCallback resolves the user's installs and routes by candidate
// count. When claims.InstallationID is non-zero (picker re-OAuth), verifies
// the pinned install is in the candidates before binding.
func (c *orgGitHubController) handleOAuthCallback(w http.ResponseWriter, r *http.Request, claims *services.ConnectStateClaims, code, settingsURL string) {
	redirectURI := c.publicURL + connectCallbackPath
	candidates, err := c.gitClient.ResolveUserInstallations(r.Context(), claims.OcOrgID, code, redirectURI)
	if err != nil {
		if errors.Is(err, gitservice.ErrAppBindNotConfigured) {
			http.Redirect(w, r, settingsURL+"?error=app_bind_not_configured", http.StatusSeeOther)
			return
		}
		slog.ErrorContext(r.Context(), "connect callback: resolve user installations failed",
			"error", err, "ocOrgId", claims.OcOrgID)
		http.Redirect(w, r, settingsURL+"?error=connect_failed", http.StatusSeeOther)
		return
	}

	// Picker re-OAuth — state pinned a specific install. Verify it's in
	// the candidates (i.e. user actually administers it) and bind.
	if claims.InstallationID > 0 {
		for _, cand := range candidates {
			if cand.InstallationID == claims.InstallationID {
				c.bindAndRedirect(w, r, claims, claims.InstallationID, settingsURL)
				return
			}
		}
		slog.InfoContext(r.Context(), "connect callback: pinned install not in user's installations",
			"ocOrgId", claims.OcOrgID, "installationId", claims.InstallationID, "actor", claims.Actor)
		http.Redirect(w, r, settingsURL+"?error=oauth_unauthorized", http.StatusSeeOther)
		return
	}

	switch len(candidates) {
	case 0:
		// User has no install of our App they admin. Send them to the
		// install flow with a fresh state JWT (installationID still 0).
		state, err := c.bearerSvc.IssueConnectState(claims.OcOrgID, claims.Actor, 0, 15*time.Minute)
		if err != nil {
			slog.ErrorContext(r.Context(), "connect callback: re-issue state failed", "error", err)
			http.Redirect(w, r, settingsURL+"?error=connect_failed", http.StatusSeeOther)
			return
		}
		installURL := "https://github.com/apps/" + c.appSlug + "/installations/new?state=" + url.QueryEscape(state)
		http.Redirect(w, r, installURL, http.StatusSeeOther)
	case 1:
		c.bindAndRedirect(w, r, claims, candidates[0].InstallationID, settingsURL)
	default:
		// Picker. Encode the candidates in the URL so the picker page can
		// render without another round-trip; the user will pick one and
		// re-enter StartConnect with installationId pinned.
		raw, err := json.Marshal(candidates)
		if err != nil {
			slog.ErrorContext(r.Context(), "connect callback: marshal candidates failed", "error", err)
			http.Redirect(w, r, settingsURL+"?error=connect_failed", http.StatusSeeOther)
			return
		}
		encoded := base64.RawURLEncoding.EncodeToString(raw)
		http.Redirect(w, r, settingsURL+"/pick?candidates="+encoded, http.StatusSeeOther)
	}
}

// handlePostInstallCallback handles the redirect after the user installed
// the App via the github.com install flow. We trust GitHub's enforcement
// that "only admins can install Apps" and bind directly without an OAuth
// re-check. Same trust assumption the original install-callback path used.
func (c *orgGitHubController) handlePostInstallCallback(w http.ResponseWriter, r *http.Request, claims *services.ConnectStateClaims, idStr, settingsURL string) {
	installID, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		http.Redirect(w, r, settingsURL+"?error=callback_invalid", http.StatusSeeOther)
		return
	}
	c.bindAndRedirect(w, r, claims, installID, settingsURL)
}

// bindAndRedirect calls CreateOrReplaceCredential to insert the platform
// row for the installation, then 302s to the settings page.
func (c *orgGitHubController) bindAndRedirect(w http.ResponseWriter, r *http.Request, claims *services.ConnectStateClaims, installID int64, settingsURL string) {
	_, err := c.gitClient.CreateOrReplaceCredential(r.Context(), claims.OcOrgID, gitservice.ConnectRequest{
		Kind:           "app-installation",
		InstallationID: installID,
	})
	if err != nil {
		var ce *gitservice.CredentialError
		switch {
		case gitservice.IsConflict(err):
			http.Redirect(w, r, settingsURL+"?error=cross_mode", http.StatusSeeOther)
		case errors.As(err, &ce) && ce.Code != "":
			// Validation failure with a structured code — pass the code
			// through so the console can map it to a friendly message.
			slog.InfoContext(r.Context(), "connect callback: bind refused",
				"ocOrgId", claims.OcOrgID, "installationId", installID, "code", ce.Code, "msg", ce.Msg)
			http.Redirect(w, r, settingsURL+"?error="+url.QueryEscape(ce.Code), http.StatusSeeOther)
		default:
			slog.ErrorContext(r.Context(), "connect callback: bind failed",
				"ocOrgId", claims.OcOrgID, "installationId", installID, "error", err)
			http.Redirect(w, r, settingsURL+"?error=connect_failed", http.StatusSeeOther)
		}
		return
	}
	slog.InfoContext(r.Context(), "connect callback: connected",
		"ocOrgId", claims.OcOrgID, "installationId", installID, "actor", claims.Actor)
	http.Redirect(w, r, settingsURL+"?connected=app", http.StatusSeeOther)
}

// ConnectPAT proxies POST /github/pat to git-service.
func (c *orgGitHubController) ConnectPAT(w http.ResponseWriter, r *http.Request) {
	orgHandle := r.PathValue("orgHandle")
	if !requireOrgHandle(w, orgHandle) {
		return
	}
	var body struct {
		PAT         string `json:"pat"`
		GitHubLogin string `json:"githubLogin"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		utils.WriteErrorResponse(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	proj, err := c.gitClient.CreateOrReplaceCredential(r.Context(), orgHandle, gitservice.ConnectRequest{
		Kind:        "user-pat",
		PAT:         body.PAT,
		GitHubLogin: body.GitHubLogin,
	})
	if err != nil {
		writeProxiedCredentialError(w, err)
		return
	}
	utils.WriteSuccessResponse(w, http.StatusOK, proj)
}

// GetStatus returns the projection for the org. Renders the read-only
// "not connected" / "connected via App|PAT" payload that the UI consumes.
func (c *orgGitHubController) GetStatus(w http.ResponseWriter, r *http.Request) {
	orgHandle := r.PathValue("orgHandle")
	if !requireOrgHandle(w, orgHandle) {
		return
	}
	proj, err := c.gitClient.GetCredentialProjection(r.Context(), orgHandle)
	if err != nil {
		if gitservice.IsNotFound(err) {
			// Not connected — return a minimal payload so the UI can render
			// the "Choose a connection method" panel without an error toast.
			utils.WriteSuccessResponse(w, http.StatusOK, map[string]any{
				"ocOrgId": orgHandle,
				"status":  "not_connected",
				"kind":    "",
			})
			return
		}
		writeProxiedCredentialError(w, err)
		return
	}
	utils.WriteSuccessResponse(w, http.StatusOK, proj)
}

// Disconnect runs the BFF-side cascade Phases A–C, forwards Phase D to
// git-service, and (optionally, App-mode only) calls Phase E to uninstall
// the App on github.com. ?uninstall query param defaults true; setting
// uninstall=false leaves the install on GitHub so the user can re-adopt
// it via the next connect flow.
func (c *orgGitHubController) Disconnect(w http.ResponseWriter, r *http.Request) {
	orgHandle := r.PathValue("orgHandle")
	if !requireOrgHandle(w, orgHandle) {
		return
	}
	uninstall := true
	if v := r.URL.Query().Get("uninstall"); v == "false" {
		uninstall = false
	}

	if err := c.disconnectSv.Disconnect(r.Context(), orgHandle, "manual.disconnect", uninstall); err != nil {
		if errors.Is(err, services.ErrOrgNotFound) {
			utils.WriteSuccessResponse(w, http.StatusOK, map[string]string{"status": "not_connected"})
			return
		}
		slog.ErrorContext(r.Context(), "disconnect failed", "error", err, "org", orgHandle)
		utils.WriteErrorResponse(w, http.StatusInternalServerError, "disconnect failed: "+err.Error())
		return
	}
	utils.WriteSuccessResponse(w, http.StatusOK, map[string]string{"status": "disconnected"})
}

// ----------------------------------------------------------------------------
// Helpers
// ----------------------------------------------------------------------------

// actorFromContext extracts the requesting user's identifier from the
// JWT-authenticated context. Falls back to "unknown" when the JWT
// middleware didn't populate claims.
func actorFromContext(ctx context.Context) string {
	if v := ctx.Value("user.sub"); v != nil { //nolint:revive — string-keyed legacy ctx
		if s, ok := v.(string); ok {
			return s
		}
	}
	return "unknown"
}

func writeProxiedCredentialError(w http.ResponseWriter, err error) {
	var ce *gitservice.CredentialError
	if errors.As(err, &ce) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(ce.Status)
		_ = json.NewEncoder(w).Encode(map[string]string{
			"error": ce.Msg,
			"code":  ce.Code,
		})
		return
	}
	utils.WriteErrorResponse(w, http.StatusInternalServerError, err.Error())
}

