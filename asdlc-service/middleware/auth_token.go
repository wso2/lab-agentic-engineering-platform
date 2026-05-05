package middleware

import (
	"context"
	"net/http"
	"strings"
)

type authTokenKey struct{}

// ExtractAuthToken returns a middleware that reads the Bearer token from the
// Authorization header and stores it in the request context for downstream use.
func ExtractAuthToken() func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
			ctx := context.WithValue(r.Context(), authTokenKey{}, token)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// GetAuthToken retrieves the Bearer token stored in the context.
func GetAuthToken(ctx context.Context) string {
	if token, ok := ctx.Value(authTokenKey{}).(string); ok {
		return token
	}
	return ""
}

// WithAuthToken returns a copy of ctx that carries the given Bearer token.
// Use this to inject a service token for async operations (e.g. MCP-triggered deploys).
func WithAuthToken(ctx context.Context, token string) context.Context {
	return context.WithValue(ctx, authTokenKey{}, token)
}
