package middleware

import (
	"context"
	"net/http"

	"github.com/google/uuid"
)

type contextKey string

const correlationIDKey contextKey = "correlationID"

const CorrelationIDHeader = "X-Correlation-ID"

// AddCorrelationID returns a middleware that attaches a correlation ID to every request.
// It reads the ID from the incoming X-Correlation-ID header, or generates a new UUID if absent.
func AddCorrelationID() func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			correlationID := r.Header.Get(CorrelationIDHeader)
			if correlationID == "" {
				correlationID = uuid.New().String()
			}

			w.Header().Set(CorrelationIDHeader, correlationID)

			ctx := context.WithValue(r.Context(), correlationIDKey, correlationID)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// GetCorrelationID retrieves the correlation ID from the context.
func GetCorrelationID(ctx context.Context) string {
	if id, ok := ctx.Value(correlationIDKey).(string); ok {
		return id
	}
	return ""
}

// WithCorrelationID returns a derived context carrying the given ID. Useful
// for background workers that synthesize their own correlation ID before
// calling outbound clients (which read the ID via GetCorrelationID).
func WithCorrelationID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, correlationIDKey, id)
}
