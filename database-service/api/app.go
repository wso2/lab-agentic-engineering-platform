package api

import (
	"net/http"

	"github.com/wso2/asdlc/database-service/controllers"
	"github.com/wso2/asdlc/database-service/middleware"
	"github.com/wso2/asdlc/database-service/middleware/logger"
)

// AppParams holds all dependencies needed to build the HTTP handler.
type AppParams struct {
	DatabaseCtrl controllers.DatabaseController
}

// NewHandler assembles the full HTTP handler with middleware and routes.
func NewHandler(params AppParams) http.Handler {
	mux := http.NewServeMux()

	// Health check
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"status":"ok"}`)) //nolint:errcheck
	})

	// API routes (no JWT — internal service)
	registerDatabaseRoutes(mux, params.DatabaseCtrl)

	// Global middleware stack (outermost applied last)
	var handler http.Handler = mux
	handler = logger.RequestLogger()(handler)
	handler = middleware.RecovererOnPanic()(handler)

	return handler
}
