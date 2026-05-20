package middleware

import (
	"log/slog"
	"net/http"
	"runtime/debug"

	"github.com/wso2/asdlc/git-service/utils"
)

// RecovererOnPanic returns a middleware that recovers from panics and returns a 500 response.
func RecovererOnPanic() func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			defer func() {
				if rec := recover(); rec != nil {
					slog.ErrorContext(r.Context(), "panic recovered",
						"error", rec,
						"stack", string(debug.Stack()),
						"correlation_id", GetCorrelationID(r.Context()),
					)
					utils.WriteErrorResponse(w, http.StatusInternalServerError, "internal server error")
				}
			}()
			next.ServeHTTP(w, r)
		})
	}
}
