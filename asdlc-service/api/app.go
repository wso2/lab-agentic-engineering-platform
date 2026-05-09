package api

import (
	"net/http"

	"github.com/wso2/asdlc/asdlc-service/config"
	"github.com/wso2/asdlc/asdlc-service/controllers"
	"github.com/wso2/asdlc/asdlc-service/middleware"
	jwtmw "github.com/wso2/asdlc/asdlc-service/middleware/jwt"
	"github.com/wso2/asdlc/asdlc-service/middleware/jwtassertion"
	"github.com/wso2/asdlc/asdlc-service/middleware/logger"
	"github.com/wso2/asdlc/asdlc-service/repositories"
)

// AppParams holds all dependencies needed to build the HTTP handler.
type AppParams struct {
	Config                 config.Config
	ProjectController       controllers.ProjectController
	ComponentController     controllers.ComponentController
	RequirementsController  controllers.RequirementsController
	DesignController        controllers.DesignController
	TaskController         controllers.TaskController
	BoardController        controllers.BoardController
	ConfigController       controllers.ConfigController
	CollabController       controllers.CollabController
	WebhookController      controllers.WebhookController
	OrgGitHubController    controllers.OrgGitHubController
	OrganizationController controllers.OrganizationController
	JWKSController         controllers.JWKSController
	TaskRepo               repositories.TaskRepository
	ConfigRepo             repositories.ConfigRepository

	// ThunderJWKS verifies User JWTs and Service JWTs presented to the BFF.
	// May be nil in dev/test, in which case inbound auth falls back to
	// unverified claim extraction — gated by IsLocalDevEnv.
	ThunderJWKS *jwtassertion.JWKSCache
}

// NewHandler assembles the full HTTP handler with middleware and routes.
// The console's nginx proxy strips the /asdlc-api-service prefix before
// forwarding, so routes are registered at root level.
func NewHandler(params AppParams) http.Handler {
	mux := http.NewServeMux()

	// Health check — unauthenticated.
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"status":"ok"}`)) //nolint:errcheck
	})

	// JWKS endpoint — unauthenticated, registered on the OUTER mux. git-service
	// fetches this to verify Task JWTs; gating it on a User JWT would create a
	// chicken/egg deadlock at first verify.
	if params.JWKSController != nil {
		registerJWKSRoute(mux, params.JWKSController)
	}

	// API routes — JWT-authenticated via JWKS-backed RS256 verification.
	apiMux := http.NewServeMux()
	if params.ProjectController != nil {
		registerProjectRoutes(apiMux, params.ProjectController)
	}
	if params.OrganizationController != nil {
		registerOrganizationRoutes(apiMux, params.OrganizationController)
	}
	if params.ComponentController != nil {
		registerComponentRoutes(apiMux, params.ComponentController)
	}
	if params.RequirementsController != nil {
		registerRequirementsRoutes(apiMux, params.RequirementsController)
	}
	if params.DesignController != nil {
		registerDesignRoutes(apiMux, params.DesignController)
	}
	if params.TaskController != nil {
		registerTaskRoutes(apiMux, params.TaskController)
	}
	if params.BoardController != nil {
		registerBoardRoutes(apiMux, params.BoardController)
	}
	if params.ConfigController != nil {
		registerConfigRoutes(apiMux, params.ConfigController)
	}
	if params.OrgGitHubController != nil {
		registerOrgGitHubRoutes(apiMux, params.OrgGitHubController)
	}

	// Test-only reset endpoint — truncates local DB tables.
	if params.Config.TestMode {
		apiMux.HandleFunc("POST /api/v1/_test/reset", testResetHandler(params))
	}

	// GitHub webhook receiver — outside JWT, HMAC-authed inside the handler.
	if params.WebhookController != nil {
		registerWebhookRoutes(mux, params.WebhookController)
	}

	// App-mode connect callback — outside JWT. The signed connect-state JWT
	// in the `state` query param is the authn here, not the console JWT.
	if params.OrgGitHubController != nil {
		registerConnectCallbackRoute(mux, params.OrgGitHubController)
	}

	if params.CollabController != nil {
		registerCollabRoutes(apiMux, mux, params.CollabController)
	}

	jwt := jwtmw.Middleware(jwtmw.Config{
		JWKS:                params.ThunderJWKS,
		AllowedIssuers:      filterEmpty(params.Config.JWTAllowedIssuer),
		AllowedAudiences:    filterEmpty(params.Config.JWTAllowedAudience),
		ResourceMetadataURL: params.Config.JWTResourceMetadataURL,
		IsLocalDevEnv:       params.ThunderJWKS == nil,
	})
	mux.Handle("/api/", jwt(apiMux))

	// Global middleware stack (outermost applied last).
	var handler http.Handler = mux
	handler = middleware.ExtractAuthToken()(handler)
	handler = logger.RequestLogger()(handler)
	handler = middleware.AddCorrelationID()(handler)
	handler = middleware.RecovererOnPanic()(handler)

	return handler
}

func filterEmpty(s string) []string {
	if s == "" {
		return nil
	}
	return []string{s}
}

func testResetHandler(params AppParams) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		if params.TaskRepo != nil {
			if err := params.TaskRepo.DeleteAll(ctx); err != nil {
				_ = err
			}
		}
		if params.ConfigRepo != nil {
			if err := params.ConfigRepo.DeleteAll(ctx); err != nil {
				_ = err
			}
		}
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"status":"reset"}`)) //nolint:errcheck
	}
}
