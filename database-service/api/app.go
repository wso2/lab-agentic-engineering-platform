package api

import (
	"net/http"

	"github.com/wso2/asdlc/database-service/controllers"
	"github.com/wso2/asdlc/database-service/mcp"
	"github.com/wso2/asdlc/database-service/middleware"
	"github.com/wso2/asdlc/database-service/middleware/jwtassertion"
	"github.com/wso2/asdlc/database-service/middleware/logger"
	"github.com/wso2/asdlc/database-service/services"
)

// AppParams holds all dependencies needed to build the HTTP handler.
type AppParams struct {
	DatabaseCtrl controllers.DatabaseController
	RegistryCtrl controllers.RegistryController
	DatabaseSvc  services.DatabaseService

	// Auth — task JWTs issued by the BFF. If nil, all routes (except /health)
	// reject requests with 401. Set JWKS to enable verification.
	JWKS            *jwtassertion.JWKSCache
	TaskJWTIssuer   string
	TaskJWTAudience string
}

// NewHandler assembles the full HTTP handler with middleware, REST routes, and the MCP server.
func NewHandler(params AppParams) http.Handler {
	mux := http.NewServeMux()

	// Health check — unauthenticated.
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"status":"ok"}`)) //nolint:errcheck
	})

	// All other routes require a valid task JWT issued by the BFF.
	auth := jwtassertion.Authenticator(jwtassertion.Config{
		JWKS:            params.JWKS,
		AllowedIssuer:   params.TaskJWTIssuer,
		AllowedAudience: params.TaskJWTAudience,
	})

	// REST routes (database operations + registry) — JWT-gated.
	protectedMux := http.NewServeMux()
	registerDatabaseRoutes(protectedMux, params.DatabaseCtrl, params.RegistryCtrl)
	mux.Handle("/api/", auth(protectedMux))

	// MCP server — AI agents call database tools here; also JWT-gated.
	if params.DatabaseSvc != nil {
		mux.Handle("/mcp", auth(mcp.NewServer(params.DatabaseSvc)))
	}

	// Global middleware stack (outermost applied last)
	var handler http.Handler = mux
	handler = logger.RequestLogger()(handler)
	handler = middleware.RecovererOnPanic()(handler)

	return handler
}
