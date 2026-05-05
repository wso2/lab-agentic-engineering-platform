package logger

import (
	"context"
	"log/slog"
	"net/http"
)

type loggerKey struct{}

// RequestLogger returns a middleware that attaches a structured logger to the request context.
func RequestLogger() func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			correlationID := r.Header.Get("X-Correlation-ID")

			reqLogger := slog.Default().With(
				slog.String("method", r.Method),
				slog.String("path", r.URL.Path),
				slog.String("correlation_id", correlationID),
				slog.String("remote_addr", r.RemoteAddr),
			)

			ctx := WithLogger(r.Context(), reqLogger)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// WithLogger stores a logger in the context.
func WithLogger(ctx context.Context, logger *slog.Logger) context.Context {
	return context.WithValue(ctx, loggerKey{}, logger)
}

// GetLogger retrieves the logger from the context, falling back to the default logger.
func GetLogger(ctx context.Context) *slog.Logger {
	if l, ok := ctx.Value(loggerKey{}).(*slog.Logger); ok {
		return l
	}
	return slog.Default()
}
