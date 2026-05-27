package api

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/wso2/asdlc/asdlc-service/utils/validate"
	"github.com/wso2/asdlc/asdlc-service/services"
)

// registerAnthropicCredentialsRoutes wires the per-org Anthropic API key
// surface. All routes mount under /internal/credentials/orgs/{ocOrgId}/anthropic
// and are gated by Service JWT at the app.go layer.
//
// Routes:
//
//	POST   /internal/credentials/orgs/{ocOrgId}/anthropic                    — connect / replace
//	GET    /internal/credentials/orgs/{ocOrgId}/anthropic                    — projection
//	DELETE /internal/credentials/orgs/{ocOrgId}/anthropic                    — disconnect
//	GET    /internal/credentials/orgs/{ocOrgId}/anthropic/effective-key      — agents-service resolver call
//	POST   /internal/credentials/orgs/{ocOrgId}/anthropic/apply-wp-secret    — dispatch-time SSA refresh
//
// Security contract: never returns the raw key over /anthropic (projection
// only carries prefix + last4). /effective-key does return the key — that
// endpoint is for agents-service alone (same Service-JWT gate as everything
// else under /internal/).
//
// See docs/design/anthropic-key-dual-token.md.
func registerAnthropicCredentialsRoutes(mux *http.ServeMux, svc *services.AnthropicCredentialService) {
	mux.HandleFunc("POST /internal/credentials/orgs/{ocOrgId}/anthropic", func(w http.ResponseWriter, r *http.Request) {
		ocOrgID := r.PathValue("ocOrgId")
		if err := validate.Slug(ocOrgID); err != nil {
			writeJSONError(w, http.StatusBadRequest, "ocOrgId: "+err.Error())
			return
		}
		var body services.AnthropicConnectRequest
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

	mux.HandleFunc("GET /internal/credentials/orgs/{ocOrgId}/anthropic", func(w http.ResponseWriter, r *http.Request) {
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

	mux.HandleFunc("DELETE /internal/credentials/orgs/{ocOrgId}/anthropic", func(w http.ResponseWriter, r *http.Request) {
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

	// NOTE: GET /effective-key is intentionally NOT registered here —
	// agents-service calls it without a Service JWT (matches cloud
	// release-binding for `app-factory-agents-service`, which carries
	// no SERVICE_AUTH_GIT_* envs). Mounted unauthenticated on the outer
	// mux by `registerAnthropicEffectiveKeyUnauth` so the merged binary
	// preserves the pre-fold git-service behavior.

	mux.HandleFunc("POST /internal/credentials/orgs/{ocOrgId}/anthropic/apply-wp-secret", func(w http.ResponseWriter, r *http.Request) {
		ocOrgID := r.PathValue("ocOrgId")
		if err := validate.Slug(ocOrgID); err != nil {
			writeJSONError(w, http.StatusBadRequest, "ocOrgId: "+err.Error())
			return
		}
		out, err := svc.ApplyWPSecret(r.Context(), ocOrgID)
		if err != nil {
			if errors.Is(err, services.ErrAnthropicKeyRequired) {
				writeJSON(w, http.StatusUnprocessableEntity, map[string]string{
					"error": err.Error(),
					"code":  "anthropic_key_required",
				})
				return
			}
			writeServiceError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, out)
	})
}

// registerAnthropicEffectiveKeyUnauth mounts the single resolver route
// the agents-service calls without an Authorization header. Lives on
// the outer mux (not behind ServiceJWT) — see the note above the
// in-mux registration for the rationale.
func registerAnthropicEffectiveKeyUnauth(mux *http.ServeMux, svc *services.AnthropicCredentialService) {
	mux.HandleFunc("GET /internal/credentials/orgs/{ocOrgId}/anthropic/effective-key", func(w http.ResponseWriter, r *http.Request) {
		ocOrgID := r.PathValue("ocOrgId")
		if err := validate.Slug(ocOrgID); err != nil {
			writeJSONError(w, http.StatusBadRequest, "ocOrgId: "+err.Error())
			return
		}
		out, err := svc.EffectiveKey(r.Context(), ocOrgID)
		if err != nil {
			writeServiceError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, out)
	})
}
