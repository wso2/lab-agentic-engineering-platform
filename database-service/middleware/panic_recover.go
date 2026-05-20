package middleware

import (
	"log/slog"
	"net/http"
)

// RecovererOnPanic returns a middleware that recovers from panics.
func RecovererOnPanic() func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			defer func() {
				if err := recover(); err != nil {
					slog.ErrorContext(r.Context(), "panic recovered", "error", err)
					w.Header().Set("Content-Type", "application/json")
					w.WriteHeader(http.StatusInternalServerError)
					w.Write([]byte(`{"error":"internal server error"}`)) //nolint:errcheck
				}
			}()
			next.ServeHTTP(w, r)
		})
	}
}
