package controllers

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/wso2/asdlc/asdlc-service/services"
	"github.com/wso2/asdlc/asdlc-service/utils"
)

// writeCredentialServiceError maps the typed errors returned by
// CredentialService / AnthropicCredentialService to an HTTP response. The
// goal is to preserve the response shape callers (web UI, agents) saw
// from the old loopback HTTP client: 404 for NotFoundError, 409 for
// ConflictError, 400 for ValidationError. Anything else is a 500.
func writeCredentialServiceError(w http.ResponseWriter, err error) {
	var nfe *services.NotFoundError
	if errors.As(err, &nfe) {
		writeCredentialErrorJSON(w, http.StatusNotFound, err.Error(), "not_found")
		return
	}
	var ce *services.ConflictError
	if errors.As(err, &ce) {
		writeCredentialErrorJSON(w, http.StatusConflict, err.Error(), "conflict")
		return
	}
	var ve *services.ValidationError
	if errors.As(err, &ve) {
		writeCredentialErrorJSON(w, http.StatusBadRequest, err.Error(), "validation_failed")
		return
	}
	if errors.Is(err, services.ErrAppBindNotConfigured) {
		writeCredentialErrorJSON(w, http.StatusServiceUnavailable, err.Error(), "app_bind_not_configured")
		return
	}
	utils.WriteErrorResponse(w, http.StatusInternalServerError, err.Error())
}

func writeCredentialErrorJSON(w http.ResponseWriter, status int, msg, code string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{
		"error": msg,
		"code":  code,
	})
}
