package controllers

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"

	"github.com/wso2/asdlc/asdlc-service/clients/oidc"
	"github.com/wso2/asdlc/asdlc-service/services"
	"github.com/wso2/asdlc/asdlc-service/utils"
)

// IDPController serves /api/v1/organizations/{orgId}/idp-profile — the
// read endpoint backs the Phase 4 console UI; the rotate endpoint is a
// platform-admin convenience for emergency rotation. EnsurePublisher
// is invoked lazily from trait_sync.SyncComponentTraits so there's no
// explicit POST endpoint for it — the platform model is "user marks
// component protected → publisher provisioning happens transparently".
type IDPController interface {
	GetProfile(w http.ResponseWriter, r *http.Request)
	UpdateProfile(w http.ResponseWriter, r *http.Request)
	RegenerateSecret(w http.ResponseWriter, r *http.Request)
	DiscoverIssuer(w http.ResponseWriter, r *http.Request)
}

type idpController struct {
	service services.IDPService
}

func NewIDPController(service services.IDPService) IDPController {
	return &idpController{service: service}
}

// GetProfile returns the org's IDP profile (kind, issuer, jwks_url,
// publisher_client_id, has-secret). 200 even when the profile hasn't
// been created yet — the response carries `kind=null` so the console
// can render "no IDP configured" state.
func (c *idpController) GetProfile(w http.ResponseWriter, r *http.Request) {
	orgID := r.PathValue("orgId")
	if orgID == "" {
		utils.WriteErrorResponse(w, http.StatusBadRequest, "orgId required")
		return
	}
	profile, err := c.service.GetProfile(r.Context(), orgID)
	if err != nil {
		slog.ErrorContext(r.Context(), "idp_controller.GetProfile", "orgID", orgID, "error", err)
		utils.WriteErrorResponse(w, http.StatusInternalServerError, "failed to load IDP profile")
		return
	}
	if profile == nil {
		utils.WriteSuccessResponse(w, http.StatusOK, map[string]interface{}{
			"orgId":   orgID,
			"kind":    nil,
			"message": "no IDP profile configured yet; first protected-component deploy provisions one",
		})
		return
	}
	utils.WriteSuccessResponse(w, http.StatusOK, profileResponse(profile))
}

// UpdateProfile changes kind/issuer/JWKS URL — Phase 7 editable picker.
// Body shape: {"kind":"platform|asgardeo|custom", "issuer":"...", "jwksUrl":"..."}.
// Empty fields leave the existing value unchanged.
func (c *idpController) UpdateProfile(w http.ResponseWriter, r *http.Request) {
	orgID := r.PathValue("orgId")
	if orgID == "" {
		utils.WriteErrorResponse(w, http.StatusBadRequest, "orgId required")
		return
	}
	var body struct {
		Kind    string `json:"kind"`
		Issuer  string `json:"issuer"`
		JWKSURL string `json:"jwksUrl"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		utils.WriteErrorResponse(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	actor := actorFromContext(r.Context())
	updated, err := c.service.UpdateProfile(r.Context(), orgID, actor, services.UpdateProfileRequest{
		Kind:    body.Kind,
		Issuer:  body.Issuer,
		JWKSURL: body.JWKSURL,
	})
	if err != nil {
		slog.ErrorContext(r.Context(), "idp_controller.UpdateProfile", "orgID", orgID, "error", err)
		utils.WriteErrorResponse(w, http.StatusBadRequest, err.Error())
		return
	}
	utils.WriteSuccessResponse(w, http.StatusOK, profileResponse(updated))
}

// RegenerateSecret rotates the publisher client_secret. The new secret
// is returned in the response body — caller MUST hand it to OpenBao /
// secret-consumer pods immediately; subsequent GetProfile responses
// will only confirm has-secret=true, never expose the value.
func (c *idpController) RegenerateSecret(w http.ResponseWriter, r *http.Request) {
	orgID := r.PathValue("orgId")
	if orgID == "" {
		utils.WriteErrorResponse(w, http.StatusBadRequest, "orgId required")
		return
	}
	actor := actorFromContext(r.Context())
	newSecret, err := c.service.RegenerateClientSecret(r.Context(), orgID, actor)
	if err != nil {
		if errors.Is(err, services.ErrIDPThunderUnavailable) {
			utils.WriteErrorResponse(w, http.StatusServiceUnavailable, "Thunder admin client not configured")
			return
		}
		slog.ErrorContext(r.Context(), "idp_controller.RegenerateSecret", "orgID", orgID, "error", err)
		utils.WriteErrorResponse(w, http.StatusInternalServerError, "failed to regenerate client secret")
		return
	}
	utils.WriteSuccessResponse(w, http.StatusOK, map[string]string{
		"clientSecret": newSecret,
	})
}

func profileResponse(p interface{}) interface{} {
	// Marshal then unmarshal through json to drop the JSON `-` tagged
	// publisher_client_secret field (kept off the wire by the model's
	// json:"-" tag — verified by serializing first).
	b, _ := json.Marshal(p)
	var out map[string]interface{}
	_ = json.Unmarshal(b, &out)
	return out
}

// DiscoverIssuer fetches the OIDC discovery document for an
// `?issuer=...` URL and returns the discovered issuer + jwks_uri so
// the console picker can auto-populate the JWKS URL field. v1 helper
// for BYO-IDP setup. Validates the issuer matches the discovery doc
// (OIDC Discovery 1.0 §4.3).
func (c *idpController) DiscoverIssuer(w http.ResponseWriter, r *http.Request) {
	issuer := r.URL.Query().Get("issuer")
	if issuer == "" {
		utils.WriteErrorResponse(w, http.StatusBadRequest, "issuer query param required")
		return
	}
	md, err := oidc.DiscoverFromIssuer(r.Context(), issuer)
	if err != nil {
		slog.WarnContext(r.Context(), "idp_controller.DiscoverIssuer", "issuer", issuer, "error", err)
		utils.WriteErrorResponse(w, http.StatusBadGateway, err.Error())
		return
	}
	utils.WriteSuccessResponse(w, http.StatusOK, map[string]string{
		"issuer":  md.Issuer,
		"jwksUrl": md.JWKSURI,
	})
}

// actorFromContext lives in org_github_controller.go — reusing it
// here keeps the audit-event actor field consistent across all
// admin-action endpoints.
