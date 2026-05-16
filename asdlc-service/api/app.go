package api

import (
	"net/http"
	"strings"

	"github.com/wso2/asdlc/asdlc-service/config"
	"github.com/wso2/asdlc/asdlc-service/controllers"
	"github.com/wso2/asdlc/asdlc-service/middleware"
	jwtmw "github.com/wso2/asdlc/asdlc-service/middleware/jwt"
	"github.com/wso2/asdlc/asdlc-service/middleware/jwtassertion"
	"github.com/wso2/asdlc/asdlc-service/middleware/logger"
	"github.com/wso2/asdlc/asdlc-service/middleware/orgensure"
	"github.com/wso2/asdlc/asdlc-service/repositories"
	"github.com/wso2/asdlc/asdlc-service/services"
)

// AppParams holds all dependencies needed to build the HTTP handler.
type AppParams struct {
	Config                     config.Config
	ProjectController          controllers.ProjectController
	ComponentController        controllers.ComponentController
	RequirementsController     controllers.RequirementsController
	RequirementsChatController controllers.RequirementsChatController
	DesignController           controllers.DesignController
	TaskController         controllers.TaskController
	BoardController        controllers.BoardController
	ConfigController       controllers.ConfigController
	CollabController       controllers.CollabController
	WebhookController      controllers.WebhookController
	OrgGitHubController    controllers.OrgGitHubController
	OrgAnthropicController controllers.OrgAnthropicController
	OrganizationController controllers.OrganizationController
	JWKSController         controllers.JWKSController
	TaskRepo               repositories.TaskRepository
	ConfigRepo             repositories.ConfigRepository

	// OrganizationService backs the JIT org-provisioning middleware. nil
	// disables the middleware (tests, dev configurations without a DB).
	OrganizationService services.OrganizationService

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
	if params.RequirementsChatController != nil {
		registerRequirementsChatRoutes(apiMux, params.RequirementsChatController)
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
	if params.OrgAnthropicController != nil {
		registerOrgAnthropicRoutes(apiMux, params.OrgAnthropicController)
	}

	// Test-only reset endpoint — truncates local DB tables.
	if params.Config.TestMode {
		apiMux.HandleFunc("POST /api/v1/_test/reset", testResetHandler(params))
	}

	// GitHub webhook receiver — outside JWT, HMAC-authed inside the handler.
	if params.WebhookController != nil {
		registerWebhookRoutes(mux, params.WebhookController)
	}

	// F3c — per-task verification-failed callback. Outside the Thunder JWT
	// (the runner pod has no user identity); authenticated inside the
	// handler with the per-task RS256 bearer the runner already holds.
	if params.TaskController != nil {
		mux.HandleFunc("POST /api/v1/tasks/{taskId}/verification-failed", params.TaskController.VerificationFailed)
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
		AllowedIssuers:      splitAndTrim(params.Config.JWTAllowedIssuer),
		AllowedAudiences:    splitAndTrim(params.Config.JWTAllowedAudience),
		ResourceMetadataURL: params.Config.JWTResourceMetadataURL,
		IsLocalDevEnv:       params.ThunderJWKS == nil,
	})
	// JIT org-onboarding sits between JWT verification and the org-aware
	// route handlers. Tenants materialise on first authenticated request;
	// no env var, manifest, or seed names an org. See
	// docs/design/default-org-seed-removal.md §3.2.
	ensureOrg := orgensure.Middleware(params.OrganizationService)
	mux.Handle("/api/", jwt(ensureOrg(apiMux)))

	// Global middleware stack (outermost applied last).
	var handler http.Handler = mux
	handler = middleware.ExtractAuthToken()(handler)
	handler = logger.RequestLogger()(handler)
	handler = middleware.AddCorrelationID()(handler)
	handler = middleware.RecovererOnPanic()(handler)

	return handler
}

// splitAndTrim splits a comma-separated env value into a list, dropping
// empty entries. Lets JWT_ISSUER / JWT_AUDIENCE accept multiple values
// (e.g. "APP_FACTORY_CONSOLE,local-dev-seeder") so a single BFF can
// accept both end-user and S2S tokens that carry different `aud`
// claims, without weakening the matcher to a wildcard.
func splitAndTrim(s string) []string {
	if s == "" {
		return nil
	}
	parts := strings.Split(s, ",")
	out := parts[:0]
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
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
