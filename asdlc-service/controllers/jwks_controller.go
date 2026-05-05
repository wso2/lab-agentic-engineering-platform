package controllers

import (
	"encoding/json"
	"log/slog"
	"net/http"

	"github.com/wso2/asdlc/asdlc-service/services"
)

// JWKSController serves the BFF Task JWT public key set at
// /auth/external/jwks.json. The endpoint is public (no auth) — verifiers
// (today: git-service) need to fetch it before any authenticated call.
type JWKSController interface {
	GetJWKS(w http.ResponseWriter, r *http.Request)
}

type jwksController struct {
	taskTokens *services.TaskTokenManager
}

// NewJWKSController returns a controller that serves the active signing
// public key. taskTokens may be nil when Task JWT issuance is not configured;
// in that case the endpoint returns an empty JWK set.
func NewJWKSController(taskTokens *services.TaskTokenManager) JWKSController {
	return &jwksController{taskTokens: taskTokens}
}

func (c *jwksController) GetJWKS(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	if c.taskTokens == nil {
		_ = json.NewEncoder(w).Encode(services.JWKSResponse{Keys: []services.JWK{}})
		return
	}

	if err := json.NewEncoder(w).Encode(c.taskTokens.JWKS()); err != nil {
		slog.ErrorContext(r.Context(), "failed to encode JWKS", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
}
