package main

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
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
	"github.com/wso2/asdlc/asdlc-service/clients/thundersvc"
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
	// component_tasks (component shape now read from the multi-file
	// `specs/design/` tree on dispatch), adds body + lineage + batch
	// fields. Idempotent.
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

	// Phase 6 — API platform IDP: organization_idp_profiles + idp_audit_events
	// tables for per-org Thunder publisher client lifecycle. See
	// docs/design/api-platform-integration.md §6 Phase 3.
	if err := migrations.RunPhase6APIPlatformIDP(db); err != nil {
		slog.Error("phase6_api_platform_idp migration failed", "error", err)
		os.Exit(1)
	}

	// Phase 6 DB tasks — adds `component_type` column to component_tasks so
	// database-type tasks can be distinguished from service/web-app tasks.
	if err := migrations.RunPhase6DbTasks(db); err != nil {
		slog.Error("phase6_db_tasks migration failed", "error", err)
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

	// Database service client — instantiated after taskTokens so we can attach
	// a service-level bearer. See below, after the task token manager block.
	var dbClient dbclient.Client
	dbServiceURL := cfg.DatabaseService.BaseURL
	if dbServiceURL == "" {
		dbServiceURL = cfg.AgentDatabaseServiceURL
	}

	// Artifact store — PR 2 of repo-storage-ownership: HTTP-backed via
	// git-service. The BFF no longer mounts /data/repos.
	artifactStore := services.NewArtifactStore(gitClient)

	// Services. componentService is constructed before configService so
	// configService can call back into it to mirror env-var edits onto
	// the OC Component's workflow params.
	projectService := services.NewProjectService(projectClient, gitClient, artifactStore, taskRepo)
	organizationService := services.NewOrganizationService(db, namespaceClient)
	// componentService.WithGitClient wires git-service in so TriggerBuild
	// can pre-stage the per-WorkflowRun build Secret in workflows-<orgID>
	// before the WorkflowRun is created (see
	// docs/design/build-credential-injection.md).
	componentService := services.NewComponentService(componentClient, observClient, artifactStore)
	if cs, ok := componentService.(interface {
		WithGitClient(gitservice.Client) services.ComponentService
	}); ok && gitClient != nil {
		componentService = cs.WithGitClient(gitClient)
	}
	configService := services.NewConfigService(configRepo, componentService)
	requirementsDirLocker := services.NewRequirementsDirLocker(db)
	requirementsService := services.NewRequirementsService(artifactStore, agentsClient, gitClient)
	if locked, ok := requirementsService.(interface {
		WithLocker(*services.RequirementsDirLocker) services.RequirementsService
	}); ok {
		requirementsService = locked.WithLocker(requirementsDirLocker)
	}
	requirementsChatService := services.NewRequirementsChatService(artifactStore, agentsClient, gitClient, requirementsDirLocker)
	designService := services.NewDesignService(artifactStore, agentsClient, gitClient)

	taskService := services.NewTaskService(db, taskRepo, artifactStore, componentService, tokenProvider, configService, gitClient, agentsClient, dbClient)
	boardService := services.NewBoardService(gitClient, taskRepo)

	if hook, ok := designService.(services.DesignServiceWithTaskHook); ok {
		hook.SetTaskService(taskService)
	}

	// Phase 2 (api-platform-integration) — trait_sync is the single shared
	// emitter that reconciles the `api-configuration` ClusterTrait on a
	// Component CR + per-environment ReleaseBindings. Hooked from both the
	// dispatch path (after CreateComponent) and the design-edit path
	// (after `components/<name>/design.md` PUT). See
	// docs/design/api-platform-integration.md §6 Phase 2.
	traitSyncService := services.NewTraitSyncService(componentClient, artifactStore, cfg.FeatureEmitAPITrait)
	slog.Info("Trait sync service", "enabled", cfg.FeatureEmitAPITrait)
	if hook, ok := designService.(services.DesignServiceWithTraitSync); ok {
		hook.SetTraitSync(traitSyncService)
	}

	// Phase 3 — Thunder admin client + IDP service. Reads
	// asdlc-system-client credentials from env (THUNDER_*) and exposes
	// EnsureOrgPublisher / RevokeOrgPublisher / RegenerateClientSecret
	// for per-org publisher OAuth app lifecycle. Optional — when the
	// Thunder base URL is empty the IDP service still runs and serves
	// GetProfile / GetOrCreateProfile, but mutating calls fail with
	// ErrIDPThunderUnavailable (non-fatal — protected components keep
	// deploying, just without per-org publishers).
	var thunderAdminClient thundersvc.Client
	thunderBase := cfg.ThunderAdmin.BaseURL
	if thunderBase == "" {
		// Fall back to the public Thunder URL the auth middleware
		// already trusts. setup-prerequisites and docker compose set
		// this to http://k3d-openchoreo-serverlb:8080 in-cluster /
		// http://thunder.openchoreo.localhost:8080 from the host.
		thunderBase = cfg.ServiceAuth.TokenURL
		// TokenURL contains /oauth2/token — strip back to the host:
		if idx := strings.Index(thunderBase, "/oauth2/"); idx > 0 {
			thunderBase = thunderBase[:idx]
		}
	}
	if cfg.ThunderAdmin.ClientID != "" && cfg.ThunderAdmin.ClientSecret != "" && thunderBase != "" {
		thunderAdminClient = thundersvc.New(thundersvc.Config{
			BaseURL:      thunderBase,
			ClientID:     cfg.ThunderAdmin.ClientID,
			ClientSecret: cfg.ThunderAdmin.ClientSecret,
		})
		slog.Info("Thunder admin client", "baseURL", thunderBase, "clientID", cfg.ThunderAdmin.ClientID)
	} else {
		slog.Warn("Thunder admin client disabled — set THUNDER_ADMIN_URL + THUNDER_SYSTEM_CLIENT_ID + THUNDER_SYSTEM_CLIENT_SECRET")
	}
	idpService := services.NewIDPService(db, thunderAdminClient, services.PlatformIDPConfig{
		Issuer:  cfg.PlatformIDP.Issuer,
		JWKSURL: cfg.PlatformIDP.JWKSURL,
	})
	// Make idpService available to trait_sync so first-protected-deploy
	// provisions the publisher app lazily.
	traitSyncService.SetIDPService(idpService)

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

	// Finish database-service client construction now that taskTokens is available.
	if dbServiceURL != "" {
		var svcBearer string
		if taskTokens != nil {
			var err error
			svcBearer, err = taskTokens.IssueServiceToken()
			if err != nil {
				slog.Error("failed to issue database-service bearer", "error", err)
				os.Exit(1)
			}
		}
		dbClient = dbclient.NewClient(dbServiceURL, svcBearer)
		slog.Info("Database service", "baseURL", dbServiceURL)
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
	dispatchSvc := services.NewDispatchService(taskRepo, gitClient, componentService, configService, artifactStore, taskTokens, tokenInject, wfRunService, projector, agentGitServiceURL, cfg.AgentPlatformURL, cfg.AgentDatabaseServiceURL, dbClient)
	if hook, ok := dispatchSvc.(services.DispatchServiceWithTraitSync); ok {
		hook.SetTraitSync(traitSyncService)
	}
	// Wire the user-apps OIDC config so dispatch posts the
	// `## OIDC client provisioned` comment on web-app issues with
	// design.auth.kind=oidc-spa. See docs/design/oauth-protected-webapp.md.
	if oidcSetter, ok := dispatchSvc.(interface {
		SetUserAppsOIDC(services.UserAppsOIDC)
	}); ok {
		oidcSetter.SetUserAppsOIDC(services.UserAppsOIDC{
			Issuer:            cfg.UserAppsOIDC.Issuer,
			ClientID:          cfg.UserAppsOIDC.ClientID,
			Scopes:            cfg.UserAppsOIDC.Scopes,
			InternalProxyPass: cfg.UserAppsOIDC.InternalProxyPass,
		})
		if cfg.UserAppsOIDC.ClientID == "" {
			slog.Warn("USER_APPS_OIDC_CLIENT_ID empty — user web-apps with auth.kind=oidc-spa will deploy without an OIDC client comment posted")
		} else {
			slog.Info("UserApps OIDC wired", "issuer", cfg.UserAppsOIDC.Issuer, "clientID", cfg.UserAppsOIDC.ClientID, "scopes", cfg.UserAppsOIDC.Scopes, "internalProxyPass", cfg.UserAppsOIDC.InternalProxyPass)
		}
	}
	// Plumb the Thunder admin client into dispatch so the cascade hook can
	// register a deployed user-webapp's external URL on the user-apps
	// OAuth client's redirect_uris set. Without this, browser sign-in
	// fails on the first load with "Invalid redirect URI" until an
	// operator appends the URL by hand. See dispatch_service.go's
	// RegisterUserAppRedirectURI.
	if thunderAdminClient != nil {
		if setter, ok := dispatchSvc.(interface {
			SetThunderAdminClient(thundersvc.Client)
		}); ok {
			setter.SetThunderAdminClient(thunderAdminClient)
			slog.Info("Thunder admin client wired into dispatch (auto-register webapp redirect URIs on deploy)")
		}
	}
	slog.Info("Dispatch service", "agentGitServiceURL", agentGitServiceURL, "agentDatabaseServiceURL", cfg.AgentDatabaseServiceURL)

	// F1 — wire the post-deploy dispatch cascade. The projector fires
	// OnTaskDeployed whenever ApplyBuildResult lands a task in `deployed`;
	// the cascade takes a per-project lock and calls DispatchTasks to
	// re-evaluate `on_hold` siblings and auto-dispatch the ones
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

	// Phase 2 — trait_sync drift watcher (10 s cadence). Idempotent
	// reconcile of the `api-configuration` ClusterTrait on every
	// (org,project,component) tuple that has a task record. Closes
	// write-write races between dispatch / design PUT and provides the
	// convergence backstop the §6 Phase 2 plan calls for.
	traitSyncWatcher := webhook.NewTraitSyncWatcher(db, traitSyncService, tokenInject)

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
		RequirementsController:     controllers.NewRequirementsController(requirementsService),
		RequirementsChatController: controllers.NewRequirementsChatController(requirementsChatService),
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
		IDPController:          controllers.NewIDPController(idpService),
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

	// On-hold watcher retries dispatch for tasks deferred due to OC
	// ReleaseBinding URL resolution lag (timing race at cascade time).
	onHoldWatcher := webhook.NewOnHoldWatcher(db, dispatchSvc)

	// Build watcher polls OC for in-flight WorkflowRun status. Goroutine is
	// fine because state lives in Postgres — a restart resumes from the
	// next tick with no in-memory state.
	watcherCtx, cancelWatcher := context.WithCancel(context.Background())
	defer cancelWatcher()
	go buildWatcher.Run(watcherCtx)
	go codingAgentWatcher.Run(watcherCtx)
	go onHoldWatcher.Run(watcherCtx)
	go traitSyncWatcher.Run(watcherCtx)

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
