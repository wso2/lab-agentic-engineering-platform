package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"

	"github.com/wso2/asdlc/git-service/internal/validate"
	"github.com/wso2/asdlc/git-service/pkg/credentials"
	"github.com/wso2/asdlc/git-service/services"
)

// registerCredentialsInternalRoutes wires the per-org connect / status /
// disconnect / webhook-secrets / lookup surface.
//
// All routes mount under /internal/credentials/ and are gated by Service
// JWT auth at the app.go layer (apiMux). The /internal/ prefix is now a
// path convention, not a separate auth class.
//
// **Security contract**: NEVER returns the GitHub token (PAT bytes or App
// installation token). Identity, kind, status, etc. are projection-only.
// /mint-build writes the token into OpenBao but returns only the
// SecretReference name + expiry — the token still doesn't cross the
// boundary.
func registerCredentialsInternalRoutes(mux *http.ServeMux, svc *services.CredentialService, buildSvc *services.BuildCredentialsService, validator *credentials.Validator) {
	internal := mux

	// POST /internal/credentials/orgs/{ocOrgId} — connect or replace.
	internal.HandleFunc("POST /internal/credentials/orgs/{ocOrgId}", func(w http.ResponseWriter, r *http.Request) {
		ocOrgID := r.PathValue("ocOrgId")
		if err := validate.Slug(ocOrgID); err != nil {
			writeJSONError(w, http.StatusBadRequest, "ocOrgId: "+err.Error())
			return
		}
		var body services.ConnectRequest
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			writeJSONError(w, http.StatusBadRequest, "invalid JSON body: "+err.Error())
			return
		}
		proj, err := svc.Connect(r.Context(), ocOrgID, body)
		if err != nil {
			writeServiceError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, proj)
	})

	// GET /internal/credentials/orgs/{ocOrgId} — projection.
	internal.HandleFunc("GET /internal/credentials/orgs/{ocOrgId}", func(w http.ResponseWriter, r *http.Request) {
		ocOrgID := r.PathValue("ocOrgId")
		if err := validate.Slug(ocOrgID); err != nil {
			writeJSONError(w, http.StatusBadRequest, "ocOrgId: "+err.Error())
			return
		}
		proj, err := svc.Status(r.Context(), ocOrgID)
		if err != nil {
			writeServiceError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, proj)
	})

	// GET /internal/credentials/orgs/{ocOrgId}/identity — identity-only
	// projection used by the BFF dispatch path.
	internal.HandleFunc("GET /internal/credentials/orgs/{ocOrgId}/identity", func(w http.ResponseWriter, r *http.Request) {
		ocOrgID := r.PathValue("ocOrgId")
		if err := validate.Slug(ocOrgID); err != nil {
			writeJSONError(w, http.StatusBadRequest, "ocOrgId: "+err.Error())
			return
		}
		ident, err := svc.IdentityFor(r.Context(), ocOrgID)
		if err != nil {
			writeServiceError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, ident)
	})

	// DELETE /internal/credentials/orgs/{ocOrgId} — disconnect Phase D.
	internal.HandleFunc("DELETE /internal/credentials/orgs/{ocOrgId}", func(w http.ResponseWriter, r *http.Request) {
		ocOrgID := r.PathValue("ocOrgId")
		if err := validate.Slug(ocOrgID); err != nil {
			writeJSONError(w, http.StatusBadRequest, "ocOrgId: "+err.Error())
			return
		}
		if err := svc.Disconnect(r.Context(), ocOrgID); err != nil {
			writeServiceError(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})

	// POST /internal/credentials/orgs/{ocOrgId}/uninstall — disconnect Phase E.
	// Calls GitHub DELETE /app/installations/{id} to remove the install on
	// the GitHub side. App-mode only; PAT rows are a no-op. The BFF's
	// disconnect cascade calls this after the platform-side row is gone, so
	// disconnect is symmetric and orphans don't accumulate on github.com.
	internal.HandleFunc("POST /internal/credentials/orgs/{ocOrgId}/uninstall", func(w http.ResponseWriter, r *http.Request) {
		ocOrgID := r.PathValue("ocOrgId")
		if err := validate.Slug(ocOrgID); err != nil {
			writeJSONError(w, http.StatusBadRequest, "ocOrgId: "+err.Error())
			return
		}
		if err := svc.UninstallAppInstallation(r.Context(), ocOrgID); err != nil {
			if errors.Is(err, services.ErrAppBindNotConfigured) {
				writeJSON(w, http.StatusServiceUnavailable, map[string]string{
					"error": err.Error(),
					"code":  "app_bind_not_configured",
				})
				return
			}
			writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})

	// GET /internal/credentials/orgs/{ocOrgId}/webhook-secrets — list.
	internal.HandleFunc("GET /internal/credentials/orgs/{ocOrgId}/webhook-secrets", func(w http.ResponseWriter, r *http.Request) {
		ocOrgID := r.PathValue("ocOrgId")
		if err := validate.Slug(ocOrgID); err != nil {
			writeJSONError(w, http.StatusBadRequest, "ocOrgId: "+err.Error())
			return
		}
		secrets, err := svc.WebhookSecrets(r.Context(), ocOrgID)
		if err != nil {
			writeServiceError(w, err)
			return
		}
		out := make([]string, 0, len(secrets))
		for _, s := range secrets {
			out = append(out, string(s))
		}
		writeJSON(w, http.StatusOK, map[string]any{"secrets": out})
	})

	// POST /internal/credentials/orgs/{ocOrgId}/webhook-secrets — append.
	// PAT only.
	internal.HandleFunc("POST /internal/credentials/orgs/{ocOrgId}/webhook-secrets", func(w http.ResponseWriter, r *http.Request) {
		ocOrgID := r.PathValue("ocOrgId")
		if err := validate.Slug(ocOrgID); err != nil {
			writeJSONError(w, http.StatusBadRequest, "ocOrgId: "+err.Error())
			return
		}
		var body struct {
			Secret string `json:"secret"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			writeJSONError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
			return
		}
		if err := svc.AppendWebhookSecret(r.Context(), ocOrgID, body.Secret); err != nil {
			writeServiceError(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})

	// DELETE /internal/credentials/orgs/{ocOrgId}/webhook-secrets/{secret} — drop.
	internal.HandleFunc("DELETE /internal/credentials/orgs/{ocOrgId}/webhook-secrets/{secret}", func(w http.ResponseWriter, r *http.Request) {
		ocOrgID := r.PathValue("ocOrgId")
		if err := validate.Slug(ocOrgID); err != nil {
			writeJSONError(w, http.StatusBadRequest, "ocOrgId: "+err.Error())
			return
		}
		secret := r.PathValue("secret")
		if err := svc.RemoveWebhookSecret(r.Context(), ocOrgID, secret); err != nil {
			writeServiceError(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})

	// GET /internal/credentials/lookup/installation/{installationId} — used
	// by the BFF webhook receiver to route App-mode events.
	internal.HandleFunc("GET /internal/credentials/lookup/installation/{installationId}", func(w http.ResponseWriter, r *http.Request) {
		idStr := r.PathValue("installationId")
		id, err := strconv.ParseInt(idStr, 10, 64)
		if err != nil {
			writeJSONError(w, http.StatusBadRequest, "installationId must be int64")
			return
		}
		ocOrgID, err := svc.OrgIDByInstallationID(r.Context(), id)
		if err != nil {
			writeServiceError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"ocOrgId": ocOrgID})
	})

	// GET /internal/credentials/lookup/repo/{owner}/{repo} — used by the
	// BFF webhook receiver to route PAT-mode and App-mode per-repo events
	// (pull_request, push, issue_comment, issues).
	internal.HandleFunc("GET /internal/credentials/lookup/repo/{owner}/{repo}", func(w http.ResponseWriter, r *http.Request) {
		owner := r.PathValue("owner")
		repo := r.PathValue("repo")
		if owner == "" || repo == "" {
			writeJSONError(w, http.StatusBadRequest, "owner and repo are required")
			return
		}
		ocOrgID, err := svc.OrgIDByRepoFullName(r.Context(), owner+"/"+repo)
		if err != nil {
			writeServiceError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"ocOrgId": ocOrgID})
	})

	// PATCH /internal/credentials/installations/{installationId}/status —
	// suspend / unsuspend handlers.
	internal.HandleFunc("PATCH /internal/credentials/installations/{installationId}/status", func(w http.ResponseWriter, r *http.Request) {
		idStr := r.PathValue("installationId")
		id, err := strconv.ParseInt(idStr, 10, 64)
		if err != nil {
			writeJSONError(w, http.StatusBadRequest, "installationId must be int64")
			return
		}
		var body struct {
			Status string `json:"status"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			writeJSONError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
			return
		}
		switch body.Status {
		case "suspended":
			err = svc.SuspendInstallation(r.Context(), id)
		case "active":
			err = svc.UnsuspendInstallation(r.Context(), id)
		default:
			writeJSONError(w, http.StatusBadRequest, "status must be 'suspended' or 'active'")
			return
		}
		if err != nil {
			writeServiceError(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})

	// POST /internal/credentials/installations/{installationId}/repos —
	// installation_repositories.added/removed projection. Body
	// {added:[...], removed:[...]} — Phase A merge only.
	internal.HandleFunc("POST /internal/credentials/installations/{installationId}/repos", func(w http.ResponseWriter, r *http.Request) {
		idStr := r.PathValue("installationId")
		id, err := strconv.ParseInt(idStr, 10, 64)
		if err != nil {
			writeJSONError(w, http.StatusBadRequest, "installationId must be int64")
			return
		}
		var body struct {
			Added   []string `json:"added"`
			Removed []string `json:"removed"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			writeJSONError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
			return
		}
		if err := svc.MergeSelectedRepos(r.Context(), id, body.Added, body.Removed); err != nil {
			writeServiceError(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})

	// GET /internal/credentials/installations/{installationId}/repositories
	// — Phase 2 PR D. Confirms an App install's current reach by calling
	// GitHub directly. Used by the BFF's reach-reconciliation Phase B to
	// distinguish a forged installation_repositories.removed payload from
	// a genuine deselect before cascading tasks (§6.8).
	internal.HandleFunc("GET /internal/credentials/installations/{installationId}/repositories", func(w http.ResponseWriter, r *http.Request) {
		idStr := r.PathValue("installationId")
		id, err := strconv.ParseInt(idStr, 10, 64)
		if err != nil {
			writeJSONError(w, http.StatusBadRequest, "installationId must be int64")
			return
		}
		repos, err := svc.ListInstallationRepos(r.Context(), id)
		if err != nil {
			writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"repositories": repos})
	})

	// POST /internal/credentials/orgs/{ocOrgId}/stage-build-secret —
	// pre-stages a per-WorkflowRun K8s Secret named
	// <workflowRunName>-git-secret in workflows-<ocOrgId> with the org's
	// GitHub credential as kubernetes.io/basic-auth. The BFF calls this
	// immediately before POSTing the WorkflowRun. The token never crosses
	// the boundary. See docs/design/build-credential-injection.md.
	internal.HandleFunc("POST /internal/credentials/orgs/{ocOrgId}/stage-build-secret", func(w http.ResponseWriter, r *http.Request) {
		if buildSvc == nil {
			writeJSONError(w, http.StatusServiceUnavailable, "build-credentials service unavailable")
			return
		}
		ocOrgID := r.PathValue("ocOrgId")
		if err := validate.Slug(ocOrgID); err != nil {
			writeJSONError(w, http.StatusBadRequest, "ocOrgId: "+err.Error())
			return
		}
		var body struct {
			RepoSlug        string `json:"repoSlug"`
			WorkflowRunName string `json:"workflowRunName"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			writeJSONError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
			return
		}
		if body.RepoSlug == "" {
			writeJSONError(w, http.StatusBadRequest, "repoSlug is required")
			return
		}
		if body.WorkflowRunName == "" {
			writeJSONError(w, http.StatusBadRequest, "workflowRunName is required")
			return
		}
		res, err := buildSvc.StageBuildSecret(r.Context(), ocOrgID, body.RepoSlug, body.WorkflowRunName)
		if err != nil {
			writeStageError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, res)
	})

	// POST /internal/credentials/app/resolve-user-installations — the only
	// install-discovery surface. Body: {ocOrgID, oauthCode, redirectURI}.
	// Exchanges the OAuth code for a user-token, intersects the user's
	// /user/installations with our App's /app/installations, and returns
	// only candidates that the user actually administers AND aren't bound
	// to a different ASDLC org. The user-token never leaves git-service.
	//
	// Replaces the prior /app/discover (which leaked all installs cross-
	// tenant) and /app/bind (which is now reduced to the BFF calling the
	// existing /orgs/{ocOrgId} connect endpoint after this resolves).
	internal.HandleFunc("POST /internal/credentials/app/resolve-user-installations", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			OcOrgID     string `json:"ocOrgId"`
			OauthCode   string `json:"oauthCode"`
			RedirectURI string `json:"redirectUri"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			writeJSONError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
			return
		}
		if err := validate.Slug(body.OcOrgID); err != nil {
			writeJSONError(w, http.StatusBadRequest, "ocOrgId: "+err.Error())
			return
		}
		candidates, err := svc.ResolveUserInstallations(r.Context(), body.OcOrgID, body.OauthCode, body.RedirectURI)
		if err != nil {
			if errors.Is(err, services.ErrAppBindNotConfigured) {
				writeJSON(w, http.StatusServiceUnavailable, map[string]string{
					"error": err.Error(),
					"code":  "app_bind_not_configured",
				})
				return
			}
			writeServiceError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"candidates": candidates})
	})

	// POST /internal/credentials/_validator/tick — Phase 2 PR D §6.10.
	// Forces one validator pass synchronously and returns a summary.
	// Production also runs the validator on a 24h ticker; this endpoint
	// is for ops debugging and E2E tests (manual revocation → tick →
	// observe disconnect cascade).
	internal.HandleFunc("POST /internal/credentials/_validator/tick", func(w http.ResponseWriter, r *http.Request) {
		if validator == nil {
			writeJSONError(w, http.StatusServiceUnavailable, "validator not configured")
			return
		}
		summary, err := validator.RunOnce(r.Context())
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, summary)
	})

}

// writeStageError maps build_credentials_service errors to status codes.
// ErrRepoNotInOrg → 404 (server-side ownership fence). ErrOrgDisconnected
// → 409. Everything else is 500.
func writeStageError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, services.ErrRepoNotInOrg):
		writeJSON(w, http.StatusNotFound, map[string]string{"error": err.Error(), "code": "repo_not_in_org"})
	case errors.Is(err, services.ErrOrgDisconnected):
		writeJSON(w, http.StatusConflict, map[string]string{"error": err.Error(), "code": "org_disconnected"})
	default:
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
	}
}

// ----------------------------------------------------------------------------
// Response helpers
// ----------------------------------------------------------------------------

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

func writeJSONError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

func writeServiceError(w http.ResponseWriter, err error) {
	var ve *services.ValidationError
	var ce *services.ConflictError
	var ne *services.NotFoundError
	switch {
	case errors.As(err, &ve):
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": ve.Message, "code": ve.Code})
	case errors.As(err, &ce):
		writeJSON(w, http.StatusConflict, map[string]string{"error": ce.Reason})
	case errors.As(err, &ne):
		writeJSON(w, http.StatusNotFound, map[string]string{"error": ne.Error()})
	default:
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": fmt.Sprintf("internal: %v", err)})
	}
}
