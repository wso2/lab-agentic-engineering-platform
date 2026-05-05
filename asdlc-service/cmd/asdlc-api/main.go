package main

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/wso2/asdlc/asdlc-service/api"
	"github.com/wso2/asdlc/asdlc-service/clients/agents"
	"github.com/wso2/asdlc/asdlc-service/clients/auth"
	dbclient "github.com/wso2/asdlc/asdlc-service/clients/database"
	"github.com/wso2/asdlc/asdlc-service/clients/gitservice"
	"github.com/wso2/asdlc/asdlc-service/clients/oauth"
	"github.com/wso2/asdlc/asdlc-service/clients/observability"
	"github.com/wso2/asdlc/asdlc-service/clients/openchoreo"
	remoteworkerclient "github.com/wso2/asdlc/asdlc-service/clients/remoteworker"
	"github.com/wso2/asdlc/asdlc-service/config"
	"github.com/wso2/asdlc/asdlc-service/controllers"
	"github.com/wso2/asdlc/asdlc-service/database"
	"github.com/wso2/asdlc/asdlc-service/database/migrations"
	"github.com/wso2/asdlc/asdlc-service/middleware"
	"github.com/wso2/asdlc/asdlc-service/middleware/jwtassertion"
	"github.com/wso2/asdlc/asdlc-service/middleware/logger"
	"github.com/wso2/asdlc/asdlc-service/models"
	"github.com/wso2/asdlc/asdlc-service/repositories"
	"github.com/wso2/asdlc/asdlc-service/services"
	"github.com/wso2/asdlc/asdlc-service/services/webhook"
)

func main() {
	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to load config: %v\n", err)
		os.Exit(1)
	}

	setupLogger(cfg.LogLevel)

	// Database. ComponentTask + ComponentConfig + Phase 0 webhook +
	// push-rendezvous tables. The org_credentials table moved to git-service
	// in Phase 2 PR A — the BFF no longer auto-migrates or reads it locally.
	db, err := database.Open(cfg.DatabaseURL,
		&models.ComponentTask{},
		&models.ComponentConfig{},
		&models.WebhookDelivery{},
		&models.WebhookPayload{},
		&models.ProjectDefaultPush{},
		&models.Organization{},
	)
	if err != nil {
		slog.Error("database init failed", "error", err)
		os.Exit(1)
	}

	// Phase 2 PR A migration — DROP TABLE org_credentials (relocated to
	// git-service) and TRUNCATE the dev tables that held legacy
	// 'platform' references. Idempotent and dev-only.
	if err := migrations.RunPhase2PRA(db, cfg.DeploymentTier); err != nil {
		slog.Error("phase2_pra migration failed", "error", err)
		os.Exit(1)
	}

	// Phase 0 destructive migration — drops legacy four-status columns and
	// truncates component_tasks. Refuses unless DEPLOYMENT_TIER=dev.
	if err := migrations.RunPhase0(db, cfg.DeploymentTier); err != nil {
		slog.Error("phase0 migration failed", "error", err)
		os.Exit(1)
	}

	// Phase 2 PR D — adds `cause` and `build_auth_retry_count` columns to
	// component_tasks. Idempotent; runs in every environment.
	if err := migrations.RunPhase2PRD(db); err != nil {
		slog.Error("phase2_prd migration failed", "error", err)
		os.Exit(1)
	}

	// Phase 3 — tech-lead agent revamp. Drops snapshot fields from
	// component_tasks (component shape now read from design.json on
	// dispatch), adds body + lineage + batch fields. Idempotent.
	if err := migrations.RunPhase3TechLead(db); err != nil {
		slog.Error("phase3_tech_lead migration failed", "error", err)
		os.Exit(1)
	}

	// Repositories — only task and config remain
	taskRepo := repositories.NewTaskRepository(db)
	configRepo := repositories.NewConfigRepository(db)

	// Token provider for service-to-service auth. OC authorizes requests by
	// the service client subject (asdlc-api-client), so every OC API call
	// must carry this token rather than the end-user's token.
	var tokenProvider *oauth.TokenProvider
	if cfg.ServiceAuth.TokenURL != "" && cfg.ServiceAuth.ClientID != "" {
		tokenProvider = oauth.NewTokenProvider(
			cfg.ServiceAuth.TokenURL,
			cfg.ServiceAuth.ClientID,
			cfg.ServiceAuth.ClientSecret,
			cfg.ServiceAuth.HostHeader,
		)
		slog.Info("Service auth configured", "tokenURL", cfg.ServiceAuth.TokenURL, "clientID", cfg.ServiceAuth.ClientID)
	}

	// OpenChoreo clients
	projectClient := openchoreo.NewProjectClient(cfg.PlatformAPI.BaseURL, cfg.PlatformAPI.HostHeader, tokenProvider, cfg.PlatformAPI.OrgNamespaceOverride)
	componentClient := openchoreo.NewComponentClient(cfg.PlatformAPI.BaseURL, cfg.PlatformAPI.HostHeader, tokenProvider, cfg.PlatformAPI.OrgNamespaceOverride)
	secretRefClient := openchoreo.NewSecretRefClient(cfg.PlatformAPI.BaseURL, cfg.PlatformAPI.HostHeader, tokenProvider, cfg.PlatformAPI.OrgNamespaceOverride)
	namespaceClient := openchoreo.NewNamespaceClient(cfg.PlatformAPI.BaseURL, cfg.PlatformAPI.HostHeader, tokenProvider)

	// Observability client (optional — build logs disabled when URL not set)
	var observClient observability.Client
	if cfg.Observability.BaseURL != "" {
		observClient = observability.NewClient(cfg.Observability.BaseURL)
		slog.Info("Observability API", "baseURL", cfg.Observability.BaseURL)
	}

	// Per-target Service JWT providers. Each one is a Thunder client_credentials
	// flow with the audience pinned to the target service. nil providers fall
	// back to no-auth which only makes sense in dev/tests where the target
	// service is configured with IS_LOCAL_DEV_ENV.
	gitAuth := buildAuthProvider("git-service", cfg.ServiceAuthGitService)
	agentsAuth := buildAuthProvider("agents-service", cfg.ServiceAuthAgentsService)
	rwAuth := buildAuthProvider("remote-worker", cfg.ServiceAuthRemoteWorker)

	// Agents service client (AI SDK v6 — BA, architect, task-generator, wireframe)
	agentsClient := agents.NewClient(cfg.AgentsService.BaseURL, agentsAuth)
	slog.Info("Agents service", "baseURL", cfg.AgentsService.BaseURL)

	// Git service client (optional — disabled when GIT_SERVICE_BASE_URL not set).
	var gitClient gitservice.Client
	if cfg.GitService.BaseURL != "" {
		gitClient = gitservice.NewClient(cfg.GitService.BaseURL, gitAuth)
		slog.Info("Git service", "baseURL", cfg.GitService.BaseURL)
	}

	// Database service client (optional — disabled when DATABASE_SERVICE_BASE_URL not set)
	var dbClient dbclient.Client
	if cfg.DatabaseService.BaseURL != "" {
		dbClient = dbclient.NewClient(cfg.DatabaseService.BaseURL)
		slog.Info("Database service", "baseURL", cfg.DatabaseService.BaseURL)
	}

	// Artifact store — PR 2 of repo-storage-ownership: HTTP-backed via
	// git-service. The BFF no longer mounts /data/repos.
	artifactStore := services.NewArtifactStore(gitClient)

	// Services
	configService := services.NewConfigService(configRepo)
	projectService := services.NewProjectService(projectClient, gitClient, secretRefClient, artifactStore, taskRepo)
	organizationService := services.NewOrganizationService(db, namespaceClient)
	componentService := services.NewComponentService(componentClient, observClient, configService, cfg.PlatformAPI.BuildRegistry)
	specService := services.NewSpecService(artifactStore, agentsClient, gitClient)
	designService := services.NewDesignService(artifactStore, agentsClient, gitClient)

	taskService := services.NewTaskService(db, taskRepo, artifactStore, componentService, tokenProvider, configService, gitClient, agentsClient, dbClient)
	boardService := services.NewBoardService(gitClient, taskRepo)

	if hook, ok := designService.(services.DesignServiceWithTaskHook); ok {
		hook.SetTaskService(taskService)
	}

	// Connect-state JWT issuer (App-mode OAuth CSRF state). The Task JWT
	// path moved to RS256 (taskTokens below); this signing key is HS256 and
	// only ever leaves the BFF as a JWT signature inside the GitHub OAuth
	// `state` query param.
	bearerSvc := services.NewBearerService(cfg.OAuthStateSigningKey, 24*time.Hour)
	if cfg.OAuthStateSigningKey == "" {
		slog.Warn("OAUTH_STATE_SIGNING_KEY not set — connect-state JWTs will fail to mint")
	}

	// Task JWT manager — RS256, 24h TTL. Public key is published on the
	// JWKS endpoint and verified by git-service /credentials/refresh.
	var taskTokens *services.TaskTokenManager
	if cfg.TaskTokenSigningKeyPath != "" {
		mgr, err := services.NewTaskTokenManager(services.TaskTokenConfig{
			PrivateKeyPath: cfg.TaskTokenSigningKeyPath,
			Issuer:         cfg.TaskTokenIssuer,
			Audience:       cfg.TaskTokenAudience,
			TTL:            24 * time.Hour,
		})
		if err != nil {
			slog.Error("task token manager init failed", "error", err)
			os.Exit(1)
		}
		taskTokens = mgr
		slog.Info("Task token manager", "kid", mgr.KeyID(), "issuer", cfg.TaskTokenIssuer, "audience", cfg.TaskTokenAudience)
	} else {
		slog.Warn("BFF_TASK_SIGNING_KEY_PATH not set — task dispatch will fail")
	}

	// Token injector for OC API calls from inside dispatch, webhook handlers,
	// and the build watcher. Uses the same service auth as the rest of the BFF.
	tokenInject := func(ctx context.Context) context.Context {
		if tokenProvider == nil {
			return ctx
		}
		token, err := tokenProvider.Token()
		if err != nil {
			slog.WarnContext(ctx, "service token fetch failed", "error", err)
			return ctx
		}
		return middleware.WithAuthToken(ctx, token)
	}

	// Remote-worker (optional — disabled when REMOTE_WORKER_BASE_URL not set)
	var remoteWorkerSvc services.RemoteWorkerService
	if cfg.RemoteWorker.BaseURL != "" {
		workerClient := remoteworkerclient.NewClient(cfg.RemoteWorker.BaseURL, rwAuth)
		gitServiceHostURL := cfg.RemoteWorker.GitServiceHostURL
		if gitServiceHostURL == "" {
			gitServiceHostURL = cfg.GitService.BaseURL
		}
		remoteWorkerSvc = services.NewRemoteWorkerService(taskRepo, workerClient, gitClient, componentService, artifactStore, taskTokens, tokenInject, gitServiceHostURL)
		slog.Info("Remote-worker", "baseURL", cfg.RemoteWorker.BaseURL, "gitServiceHostURL", gitServiceHostURL)
	}

	// Webhook receiver wiring. PR B swaps EnvSecretProvider for
	// GitServiceSecretProvider — secrets now come from the per-org
	// credential record (via git-service). The receiver pipeline shape
	// is unchanged from Phase 0; only the lookup backend changes.
	var (
		secretProvider webhook.SecretProvider
		routingLookup  webhook.OcOrgIDLookup
	)
	if gitClient != nil {
		secretProvider = webhook.NewGitServiceSecretProvider(gitClient, 30*time.Second)
		routingLookup = gitClient
	} else {
		// Defensive — running without a git-service is not a supported
		// dev configuration but main.go shouldn't crash on it.
		secretProvider = webhook.NewGitServiceSecretProvider(nilSecretFetcher{}, 30*time.Second)
		routingLookup = nilLookup{}
	}
	webhookVerifier := webhook.NewVerifier(secretProvider).
		WithRefetchLimiter(webhook.NewRefetchLimiter(1, 5))
	routingCache := webhook.NewRoutingCache(60 * time.Second)
	deliveryStore := webhook.NewDeliveryStore(db)
	webhookRouter := webhook.NewRouter()
	projector := webhook.NewProjector(db)

	wfRunService := services.NewWorkflowRunService(db, taskRepo, componentClient, gitClient, artifactStore, tokenInject)
	webhook.Register(webhookRouter, db, projector, wfRunService)
	if gitClient != nil {
		webhook.RegisterInstallationHandlers(webhookRouter, db, gitClient, taskRepo, projector)
	}
	webhookCtrl := controllers.NewWebhookController(webhookVerifier, deliveryStore, webhookRouter, routingLookup, routingCache)

	// Build watcher — 10s sweep for in-flight WorkflowRuns. Started after
	// the HTTP server is up so it's not killed during handler init failures.
	// Phase 2 PR D — wfRunService.RetryAuthFailedBuild backs the auth
	// retry path. authBudget is configurable for tests via env.
	buildWatcher := webhook.NewBuildWatcher(db, componentClient, projector, tokenInject, wfRunService, cfg.BuildAuthRetryBudget)

	// Phase 2 PR B — org-scoped GitHub connect/disconnect surface.
	var orgGitHubCtrl controllers.OrgGitHubController
	if gitClient != nil {
		disconnectSvc := services.NewOrgDisconnectService(taskRepo, db, gitClient)
		orgGitHubCtrl = controllers.NewOrgGitHubController(
			gitClient,
			disconnectSvc,
			bearerSvc,
			cfg.GithubAppSlug,
			cfg.BFFPublicURL,
			cfg.GithubAppClientID,
		)
	}

	// Inbound JWT verifier — Thunder publishes the User JWT and Service JWT
	// signing keys at JWKSURL. Lazy fetch on first request avoids compose
	// start-order races.
	var thunderJWKS *jwtassertion.JWKSCache
	if cfg.JWKSURL != "" {
		thunderJWKS = jwtassertion.NewJWKSCache(cfg.JWKSURL)
		slog.Info("Inbound JWT verifier", "jwksURL", cfg.JWKSURL, "audience", cfg.JWTAllowedAudience, "issuer", cfg.JWTAllowedIssuer)
	} else {
		slog.Warn("JWKS_URL not set — inbound JWT verification disabled (dev/test only)")
	}

	// Controllers
	params := api.AppParams{
		Config:                 cfg,
		ProjectController:      controllers.NewProjectController(projectService),
		OrganizationController: controllers.NewOrganizationController(organizationService),
		ComponentController:    controllers.NewComponentController(componentService, taskService),
		SpecController:         controllers.NewSpecController(specService),
		DesignController:       controllers.NewDesignController(designService),
		TaskController:         controllers.NewTaskController(taskService, remoteWorkerSvc),
		BoardController:        controllers.NewBoardController(boardService),
		ConfigController:       controllers.NewConfigController(configService),
		CollabController:       controllers.NewCollabController(projectService),
		WebhookController:      webhookCtrl,
		TaskRepo:               taskRepo,
		ConfigRepo:             configRepo,
		OrgGitHubController:    orgGitHubCtrl,
		JWKSController:         controllers.NewJWKSController(taskTokens),
		ThunderJWKS:            thunderJWKS,
	}

	slog.Info("OpenChoreo API", "baseURL", cfg.PlatformAPI.BaseURL)

	handler := api.NewHandler(params)

	server := &http.Server{
		Addr:              fmt.Sprintf("%s:%d", cfg.ServerHost, cfg.ServerPort),
		Handler:           handler,
		ReadHeaderTimeout: 15 * time.Second,
		WriteTimeout:      15 * time.Minute, // AI design + wireframe generation can take up to 10 min
		IdleTimeout:       60 * time.Second,
	}

	go func() {
		slog.Info("server started", "addr", server.Addr)
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			slog.Error("server error", "error", err)
			os.Exit(1)
		}
	}()

	// Build watcher polls OC for in-flight WorkflowRun status. Goroutine is
	// fine because state lives in Postgres — a restart resumes from the
	// next tick with no in-memory state.
	watcherCtx, cancelWatcher := context.WithCancel(context.Background())
	defer cancelWatcher()
	go buildWatcher.Run(watcherCtx)

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit

	slog.Info("shutting down server")
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	if err := server.Shutdown(ctx); err != nil {
		slog.Error("server shutdown failed", "error", err)
		os.Exit(1)
	}
	slog.Info("server stopped")
}

// nilSecretFetcher / nilLookup are defensive fallbacks for the (unsupported
// in dev, but possible in tests) configuration where git-service isn't
// configured. Both reject every call so the receiver returns 5xx, which
// is the right signal — webhook routing without git-service is broken.
type nilSecretFetcher struct{}

func (nilSecretFetcher) GetWebhookSecrets(context.Context, string) ([][]byte, error) {
	return nil, fmt.Errorf("git-service not configured")
}

type nilLookup struct{}

func (nilLookup) OrgIDByInstallationID(context.Context, int64) (string, error) {
	return "", fmt.Errorf("git-service not configured")
}
func (nilLookup) OrgIDByRepoFullName(context.Context, string) (string, error) {
	return "", fmt.Errorf("git-service not configured")
}

// buildAuthProvider returns an AuthProvider when client_credentials are
// configured for the given target, or nil otherwise. nil providers cause
// outbound calls to skip the Authorization header — only OK when the
// downstream service has IS_LOCAL_DEV_ENV=true.
func buildAuthProvider(target string, c config.ServiceAuthConfig) *auth.AuthProvider {
	if c.TokenURL == "" || c.ClientID == "" {
		slog.Warn("service auth not configured for target — outbound calls will be unauthenticated", "target", target)
		return nil
	}
	return auth.NewAuthProvider(auth.Config{
		TokenURL:     c.TokenURL,
		ClientID:     c.ClientID,
		ClientSecret: c.ClientSecret,
		HostHeader:   c.HostHeader,
	})
}

func setupLogger(level string) {
	var logLevel slog.Level
	switch level {
	case "debug":
		logLevel = slog.LevelDebug
	case "warn":
		logLevel = slog.LevelWarn
	case "error":
		logLevel = slog.LevelError
	default:
		logLevel = slog.LevelInfo
	}
	base := slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: logLevel})
	slog.SetDefault(slog.New(logger.NewContextHandler(base)))
}
