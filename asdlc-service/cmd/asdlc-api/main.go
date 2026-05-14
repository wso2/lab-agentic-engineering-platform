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
	"github.com/wso2/asdlc/asdlc-service/clients/observer"
	"github.com/wso2/asdlc/asdlc-service/clients/openchoreo"
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

	// Phase 4 — coding-agent ClusterWorkflow refactor. Adds
	// last_coding_agent_run_name to component_tasks. Idempotent.
	if err := migrations.RunPhase4CodingAgent(db); err != nil {
		slog.Error("phase4_coding_agent migration failed", "error", err)
		os.Exit(1)
	}

	// Phase 5 — F2 deploy-gating: renames task_depends_on → depends_on_components.
	// See docs/design/cross-component-wiring-gaps.md. Idempotent.
	if err := migrations.RunPhase5DeployGating(db); err != nil {
		slog.Error("phase5_deploy_gating migration failed", "error", err)
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

	// OpenChoreo clients. Each one resolves the OC namespace as the OC
	// org handle directly (== ouHandle); there is no override map. Migrated
	// clients (namespace, project) take an openchoreo.Config; the still-hand-
	// rolled clients (component, secretref) keep the legacy positional args
	// until they migrate too.
	ocConfig := openchoreo.Config{
		BaseURL:      cfg.PlatformAPI.BaseURL,
		HostHeader:   cfg.PlatformAPI.HostHeader,
		AuthProvider: tokenProvider,
	}
	projectClient := openchoreo.NewProjectClient(ocConfig)
	namespaceClient := openchoreo.NewNamespaceClient(ocConfig)
	componentClient := openchoreo.NewComponentClient(ocConfig)

	// Observability client (optional — build logs disabled when URL not set)
	var observClient observability.Client
	if cfg.Observability.BaseURL != "" {
		observClient = observability.NewClient(cfg.Observability.BaseURL)
		slog.Info("Observability API", "baseURL", cfg.Observability.BaseURL)
	}

	// Observer client for /progress/* — Thunder client_credentials against
	// the platform-default reader app. Falls back to nil (and 503 on the
	// route) if any of the OAuth params are missing.
	var observerTokenProvider *oauth.TokenProvider
	var observerClient observer.Client
	if cfg.Observability.BaseURL != "" && cfg.Observability.TokenURL != "" && cfg.Observability.ClientID != "" {
		observerTokenProvider = oauth.NewTokenProvider(
			cfg.Observability.TokenURL,
			cfg.Observability.ClientID,
			cfg.Observability.ClientSecret,
			cfg.Observability.HostHeader,
		)
		var err error
		observerClient, err = observer.NewClient(observer.Config{
			BaseURL:       cfg.Observability.BaseURL,
			TokenProvider: observerTokenProvider,
		})
		if err != nil {
			slog.Error("Observer client init failed", "error", err)
		} else if observerClient != nil {
			slog.Info("Observer client configured", "baseURL", cfg.Observability.BaseURL, "clientID", cfg.Observability.ClientID)
		}
	} else {
		slog.Warn("Observer client not configured — /progress/* will return 503 progress_unavailable")
	}

	// Per-target Service JWT providers. Each one is a Thunder client_credentials
	// flow with the audience pinned to the target service. nil providers fall
	// back to no-auth which only makes sense in dev/tests where the target
	// service is configured with IS_LOCAL_DEV_ENV.
	gitAuth := buildAuthProvider("git-service", cfg.ServiceAuthGitService)
	agentsAuth := buildAuthProvider("agents-service", cfg.ServiceAuthAgentsService)

	// Agents service client (AI SDK v6 — BA, architect, tech-lead)
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

	// Services. componentService is constructed before configService so
	// configService can call back into it to mirror env-var edits onto
	// the OC Component's workflow params.
	projectService := services.NewProjectService(projectClient, gitClient, artifactStore, taskRepo)
	organizationService := services.NewOrganizationService(db, namespaceClient)
	componentService := services.NewComponentService(componentClient, observClient, artifactStore)
	configService := services.NewConfigService(configRepo, componentService)
	requirementsService := services.NewRequirementsService(artifactStore, agentsClient, gitClient)
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
	if cfg.TaskTokenSigningKey != "" {
		mgr, err := services.NewTaskTokenManager(services.TaskTokenConfig{
			PrivateKey: cfg.TaskTokenSigningKey,
			Issuer:     cfg.TaskTokenIssuer,
			Audience:   cfg.TaskTokenAudience,
			TTL:        24 * time.Hour,
		})
		if err != nil {
			slog.Error("task token manager init failed", "error", err)
			os.Exit(1)
		}
		taskTokens = mgr
		slog.Info("Task token manager", "kid", mgr.KeyID(), "issuer", cfg.TaskTokenIssuer, "audience", cfg.TaskTokenAudience)
	} else {
		slog.Warn("BFF_TASK_SIGNING_KEY not set — task dispatch will fail")
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

	// Dispatch service drives the per-task Issue/branch/PR/Component
	// pipeline and creates a coding-agent WorkflowRun. wfRunService is
	// constructed below; we wire DispatchService after it.

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

	wfRunService := services.NewWorkflowRunService(db, taskRepo, componentClient, gitClient, artifactStore, projector, tokenInject)

	// Dispatch service — replaces the legacy RemoteWorkerService. Routes to
	// WorkflowRunService.TriggerCodingAgent (ClusterWorkflow `app-factory-coding-agent`)
	// for the per-task agent pod. AGENT_GIT_SERVICE_URL must be reachable from
	// the WorkflowPlane namespace (cross-namespace FQDN — see env-overlay).
	agentGitServiceURL := cfg.AgentGitServiceURL
	if agentGitServiceURL == "" {
		agentGitServiceURL = cfg.GitService.BaseURL
	}
	dispatchSvc := services.NewDispatchService(taskRepo, gitClient, componentService, configService, artifactStore, taskTokens, tokenInject, wfRunService, projector, agentGitServiceURL, cfg.AgentPlatformURL)
	slog.Info("Dispatch service", "agentGitServiceURL", agentGitServiceURL)

	// F1 — wire the post-deploy dispatch cascade. The projector fires
	// OnTaskDeployed whenever ApplyBuildResult lands a task in `deployed`;
	// the cascade takes a per-project lock and calls DispatchTasks to
	// re-evaluate `pending_deps` siblings and auto-dispatch the ones
	// whose deps are now satisfied. See docs/design/cross-component-
	// wiring-gaps.md §3 F1.
	projector.SetDispatchHook(services.NewDispatchCascadeHook(db, dispatchSvc))

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

	// Coding-agent watcher — same cadence, complementary to the GitHub
	// webhook path. Only acts on terminal-failed coding-agent WorkflowRuns;
	// success transitions ride the pull_request:ready_for_review webhook.
	codingAgentWatcher := webhook.NewCodingAgentWatcher(db, componentClient, projector, tokenInject)

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

	// Per-org Anthropic settings surface. Proxies to git-service's internal
	// credential routes; same JWT gating as GitHub Integration.
	var orgAnthropicCtrl controllers.OrgAnthropicController
	if gitClient != nil {
		orgAnthropicCtrl = controllers.NewOrgAnthropicController(gitClient)
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
		RequirementsController: controllers.NewRequirementsController(requirementsService),
		DesignController:       controllers.NewDesignController(designService),
		TaskController: controllers.NewTaskController(
			taskService,
			dispatchSvc,
			services.NewProgressService(taskService, componentClient, observerClient),
			componentClient,
			taskTokens,
		),
		BoardController:        controllers.NewBoardController(boardService),
		ConfigController:       controllers.NewConfigController(configService),
		CollabController:       controllers.NewCollabController(projectService),
		WebhookController:      webhookCtrl,
		TaskRepo:               taskRepo,
		ConfigRepo:             configRepo,
		OrgGitHubController:    orgGitHubCtrl,
		OrgAnthropicController: orgAnthropicCtrl,
		JWKSController:         controllers.NewJWKSController(taskTokens),
		ThunderJWKS:            thunderJWKS,
		OrganizationService:    organizationService,
	}

	slog.Info("OpenChoreo API", "baseURL", cfg.PlatformAPI.BaseURL)

	handler := api.NewHandler(params)

	server := &http.Server{
		Addr:              fmt.Sprintf("%s:%d", cfg.ServerHost, cfg.ServerPort),
		Handler:           handler,
		ReadHeaderTimeout: 15 * time.Second,
		WriteTimeout:      15 * time.Minute, // AI design generation can take up to 10 min
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
	go codingAgentWatcher.Run(watcherCtx)

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
