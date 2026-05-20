package utils

import (
	"encoding/json"
	"net/http"
)

// ErrorResponse is the standard error payload returned by the API.
type ErrorResponse struct {
	Error   string `json:"error"`
	Message string `json:"message"`
}

// WriteSuccessResponse writes a JSON success response.
func WriteSuccessResponse(w http.ResponseWriter, statusCode int, data any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(statusCode)
	json.NewEncoder(w).Encode(data) //nolint:errcheck
}

// WriteErrorResponse writes a JSON error response.
func WriteErrorResponse(w http.ResponseWriter, statusCode int, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(statusCode)
	json.NewEncoder(w).Encode(ErrorResponse{ //nolint:errcheck
		Error:   http.StatusText(statusCode),
		Message: message,
	})
}
