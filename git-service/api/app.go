package api

import (
	"log/slog"
	"net/http"

	"gorm.io/gorm"

	"github.com/wso2/asdlc/git-service/controllers"
	"github.com/wso2/asdlc/git-service/middleware"
	"github.com/wso2/asdlc/git-service/middleware/jwtassertion"
	"github.com/wso2/asdlc/git-service/middleware/logger"
	"github.com/wso2/asdlc/git-service/pkg/credentials"
	"github.com/wso2/asdlc/git-service/repositories"
	"github.com/wso2/asdlc/git-service/services"
)

// AppParams holds all dependencies needed to build the HTTP handler.
type AppParams struct {
	TestMode         bool
	DB               *gorm.DB
	RepoCtrl         controllers.RepoController
	GitOpsCtrl       controllers.GitOpsController
	IssueCtrl        controllers.IssueController
	ProjectCtrl      controllers.ProjectController
	BranchCtrl       controllers.BranchController
	PullRequestCtrl  controllers.PullRequestController
	WebhookCtrl      controllers.WebhookRegistrationController
	ArtifactCtrl     controllers.ArtifactController
	CredCtrl         controllers.CredentialsRefreshController
	BoardCtrl        controllers.BoardController
	RepoService      services.RepoService
	RepoRepo         repositories.RepoRepository
	CredService      *services.CredentialService
	BuildCredService *services.BuildCredentialsService
	Validator        *credentials.Validator

	// ServiceJWT verifies User/Service JWTs presented to /api/v1/repos/* and
	// /internal/credentials/*. JWKS resolves to Thunder.
	ServiceJWT jwtassertion.Middleware
	// TaskJWT verifies Task JWTs presented to /api/v1/credentials/refresh.
	// JWKS resolves to the BFF's /auth/external/jwks.json.
	TaskJWT jwtassertion.Middleware
}

// NewHandler assembles the full HTTP handler with middleware and routes.
//
// Three muxes layered by auth requirement:
//   - outer mux: /health (open), webhooks (HMAC-only).
//   - apiMux: /api/v1/repos/*, /api/v1/orgs/*, /internal/credentials/*
//     gated by Service JWT (audience = git-service).
//   - taskMux: /api/v1/credentials/refresh gated by Task JWT (audience =
//     git-service, signed by BFF).
func NewHandler(params AppParams) http.Handler {
	mux := http.NewServeMux()

	// Health check — open.
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"status":"ok"}`)) //nolint:errcheck
	})

	// Service-JWT-protected mux: repo APIs + internal credential routes.
	apiMux := http.NewServeMux()
	if params.RepoCtrl != nil {
		var orgScope func(http.Handler) http.Handler
		if params.RepoRepo != nil {
			orgScope = middleware.RequireOrgScope(params.RepoRepo)
		}
		registerRepoOnlyRoutes(apiMux,
			params.RepoCtrl,
			params.GitOpsCtrl,
			params.IssueCtrl,
			params.BranchCtrl,
			params.PullRequestCtrl,
			params.WebhookCtrl,
			params.ArtifactCtrl,
			orgScope,
		)
	}
	if params.CredService != nil {
		registerCredentialsInternalRoutes(apiMux, params.CredService, params.BuildCredService, params.Validator)
	}
	registerOrgRoutes(mux, params.ProjectCtrl)
	registerBoardRoutes(mux, params.BoardCtrl)

	// Test-only reset endpoint (open — runs only when TEST_MODE=true).
	if params.TestMode {
		mux.HandleFunc("POST /api/v1/_test/reset", func(w http.ResponseWriter, r *http.Request) {
			if err := params.RepoService.DeleteAll(r.Context()); err != nil {
				slog.ErrorContext(r.Context(), "reset repos failed", "error", err)
			}
			w.WriteHeader(http.StatusOK)
			w.Write([]byte(`{"status":"reset"}`)) //nolint:errcheck
		})
	}

	// Task-JWT-protected mux: only the credential-refresh endpoint, called
	// from per-task workspaces by the credential helper. Audience is
	// "git-service"; signing is RS256 by the BFF.
	taskMux := http.NewServeMux()
	if params.CredCtrl != nil {
		taskMux.HandleFunc("POST /api/v1/credentials/refresh", params.CredCtrl.Refresh)
	}

	// Wire the muxes onto the outer mux. Order doesn't matter — paths are
	// disjoint — but the more specific (longer) prefix wins regardless.
	if params.ServiceJWT != nil {
		mux.Handle("/api/v1/repos/", params.ServiceJWT(apiMux))
		mux.Handle("/api/v1/orgs/", params.ServiceJWT(apiMux))
		mux.Handle("/internal/credentials/", params.ServiceJWT(apiMux))
		// /api/v1/repos POST (no trailing slash) hits the same apiMux.
		mux.Handle("/api/v1/repos", params.ServiceJWT(apiMux))
	} else {
		// Dev / test fallback: no JWT verification (relies on
		// IS_LOCAL_DEV_ENV=true on the verifier side).
		mux.Handle("/api/v1/repos/", apiMux)
		mux.Handle("/api/v1/orgs/", apiMux)
		mux.Handle("/internal/credentials/", apiMux)
		mux.Handle("/api/v1/repos", apiMux)
	}
	if params.TaskJWT != nil {
		// RequireTaskBearer projects jwtassertion's claims into the
		// TaskBearerClaims shape the controller reads via context. Wrapping
		// the verifier directly skips the projection and the controller
		// would see "missing bearer claims".
		mux.Handle("/api/v1/credentials/", middleware.RequireTaskBearer(params.TaskJWT)(taskMux))
	} else {
		mux.Handle("/api/v1/credentials/", taskMux)
	}

	// Global middleware stack (outermost applied last).
	var handler http.Handler = mux
	handler = logger.RequestLogger()(handler)
	handler = middleware.AddCorrelationID()(handler)
	handler = middleware.RecovererOnPanic()(handler)

	return handler
}
