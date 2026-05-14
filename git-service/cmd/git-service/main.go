package main

import (
	"context"
	"encoding/base64"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/wso2/asdlc/git-service/api"
	k8sclient "github.com/wso2/asdlc/git-service/clients/k8s"
	"github.com/wso2/asdlc/git-service/config"
	"github.com/wso2/asdlc/git-service/controllers"
	"github.com/wso2/asdlc/git-service/database"
	"github.com/wso2/asdlc/git-service/database/migrations"
	"github.com/wso2/asdlc/git-service/internal/seed"
	"github.com/wso2/asdlc/git-service/middleware/jwtassertion"
	"github.com/wso2/asdlc/git-service/middleware/logger"
	"github.com/wso2/asdlc/git-service/models"
	"github.com/wso2/asdlc/git-service/pkg/credentials"
	"github.com/wso2/asdlc/git-service/repositories"
	"github.com/wso2/asdlc/git-service/services"
)

func main() {
	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to load config: %v\n", err)
		os.Exit(1)
	}

	setupLogger(cfg.LogLevel)

	// Database. PR A migration runs raw SQL FIRST so the org_credentials
	// table is created with the §4.1 column names + CHECK constraints. GORM
	// AutoMigrate runs after to verify the shape; OrgCredential is
	// intentionally absent from the AutoMigrate list because the raw SQL
	// is authoritative.
	db, err := database.Open(cfg.DatabaseURL)
	if err != nil {
		slog.Error("database init failed", "error", err)
		os.Exit(1)
	}

	// Phase 2 PR A schema — CHECK constraints, partial unique indexes,
	// git_repositories.oc_secret_ref_name. Idempotent.
	pra := func() error {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		return migrations.RunPhase2PRA(ctx, db)
	}
	if err := pra(); err != nil {
		slog.Error("phase2_pra migration failed", "error", err)
		os.Exit(1)
	}

	// Phase 2 PR C schema — repo_slug column + backfill on git_repositories.
	// Idempotent. Runs before AutoMigrate so the column shape is in place.
	prc := func() error {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		return migrations.RunPhase2PRC(ctx, db)
	}
	if err := prc(); err != nil {
		slog.Error("phase2_prc migration failed", "error", err)
		os.Exit(1)
	}

	// org_secrets table — credential store replacing OpenBao. Idempotent.
	orgSecrets := func() error {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		return migrations.RunOrgSecretsMigration(ctx, db)
	}
	if err := orgSecrets(); err != nil {
		slog.Error("org_secrets migration failed", "error", err)
		os.Exit(1)
	}

	// Post-2f26614 — collapse git_repositories.oc_secret_ref_name from the
	// legacy per-repo `git-<org>-<slug>` shape to the per-org `git-<org>`
	// shape. Idempotent. See docs/design/cross-component-wiring-gaps.md.
	perOrgSecret := func() error {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		return migrations.RunPerOrgSecretName(ctx, db)
	}
	if err := perOrgSecret(); err != nil {
		slog.Error("per_org_secret_name migration failed", "error", err)
		os.Exit(1)
	}

	// org_anthropic_credentials table — per-org Anthropic key projection
	// (encrypted bytes live in org_secrets). See
	// docs/design/anthropic-key-dual-token.md §4.2. Idempotent.
	anthropicCreds := func() error {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		return migrations.RunOrgAnthropicCredentialsMigration(ctx, db)
	}
	if err := anthropicCreds(); err != nil {
		slog.Error("org_anthropic_credentials migration failed", "error", err)
		os.Exit(1)
	}

	// AutoMigrate after — only for GORM-shaped tables. AutoMigrate is
	// additive (won't drop columns) so re-running with the raw migration
	// already applied is a no-op for org_credentials.
	if err := db.AutoMigrate(&models.GitRepository{}); err != nil {
		slog.Error("automigrate failed", "error", err)
		os.Exit(1)
	}

	// Ensure repo base path exists
	if err := os.MkdirAll(cfg.RepoBasePath, 0o755); err != nil {
		slog.Error("failed to create repo base path", "path", cfg.RepoBasePath, "error", err)
		os.Exit(1)
	}

	credKey, err := base64.StdEncoding.DecodeString(cfg.CredentialEncryptionKey)
	if err != nil || len(credKey) != 32 {
		slog.Error("CREDENTIAL_ENCRYPTION_KEY must be a base64-encoded 32-byte key (generate: openssl rand -base64 32)", "error", err)
		os.Exit(1)
	}
	store, err := credentials.NewDBStore(db, credKey)
	if err != nil {
		slog.Error("credential store init failed", "error", err)
		os.Exit(1)
	}
	slog.Info("credential store: postgres (aes-256-gcm)")

	// Workflow-plane k8s client — used by BuildCredentialsService.MintBuildToken
	// to SSA the per-org git-credentials Secret straight into workflows-<orgID>.
	// nil when running outside a cluster (local dev without KUBECONFIG); the
	// service degrades to "credential persisted but not delivered" with a
	// loud warning per mint call. See clients/k8s/client.go.
	wpClient, err := k8sclient.NewInClusterClient()
	if err != nil {
		slog.Warn("k8s client init failed — mint-build will skip Secret writes; builds will fail at clone",
			"error", err)
		wpClient = nil
	} else {
		slog.Info("k8s client: in-cluster (workflow-plane Secret writes enabled)")
	}

	// Repositories
	repoRepo := repositories.NewRepoRepository(db)

	// AppTokenMinter — best-effort App-key load. PR A's seed is user-pat
	// only, so the minter constructs in "no app configured" mode and is
	// never reached by the resolver. PR B's connect flow seeds the App key
	// and the minter starts answering MintForInstallation calls.
	loadCtx, cancelLoad := context.WithTimeout(context.Background(), 10*time.Second)
	appKey, err := credentials.LoadAppKeyFromOpenBao(loadCtx, store)
	cancelLoad()
	if err != nil {
		slog.Warn("app key load failed; App-mode credentials will return ErrAppNotConfigured", "error", err)
		appKey = nil
	}
	if appKey == nil {
		slog.Info("no GitHub App configured (PR A: user-pat only); minter in no-app mode")
	}
	minter, err := credentials.NewAppTokenMinter(appKey)
	if err != nil {
		slog.Error("app token minter init failed", "error", err)
		os.Exit(1)
	}
	// Make OpenBao reachable to the minter for post-startup _platform reads
	// (App webhook secret list). Confined to pkg/credentials/ via the
	// import-fence test.
	minter.WithOpenBao(store)

	// PR B — seed App platform credentials (private key, app ID, client ID,
	// webhook secret) into OpenBao when running in dev. No-op outside dev
	// or when env values are absent. Runs BEFORE the App-key load retry
	// below so first-boot dev environments come up clean.
	platformSeedCtx, cancelPlatformSeed := context.WithTimeout(context.Background(), 30*time.Second)
	if err := seed.AppPlatformFromEnv(platformSeedCtx, store, cfg); err != nil {
		cancelPlatformSeed()
		slog.Error("app platform seed failed", "error", err)
		os.Exit(1)
	}
	cancelPlatformSeed()

	// Re-attempt App-key load after the seed: a fresh-deploy dev
	// environment seeds and loads in one process, so the minter ends up
	// fully wired (no second restart needed).
	if appKey == nil {
		retryCtx, cancelRetry := context.WithTimeout(context.Background(), 10*time.Second)
		if reloaded, rerr := credentials.LoadAppKeyFromOpenBao(retryCtx, store); rerr == nil && reloaded != nil {
			cancelRetry()
			minter, err = credentials.NewAppTokenMinter(reloaded)
			if err != nil {
				slog.Error("app token minter re-init failed", "error", err)
				os.Exit(1)
			}
			minter.WithOpenBao(store)
			slog.Info("github app loaded post-seed", "appId", reloaded.AppID)
		} else {
			cancelRetry()
		}
	}

	// Best-effort load of the App's bot identity (GET /app). PR A leaves
	// this nil; PR B's first App-mode connect retries lazily if startup
	// load failed.
	if minter.AppID() != 0 {
		idCtx, cancelID := context.WithTimeout(context.Background(), 10*time.Second)
		if err := minter.LoadAppBotIdentity(idCtx, "https://api.github.com"); err != nil {
			slog.Warn("app bot identity load failed; will retry on first connect", "error", err)
		} else {
			slog.Info("app bot identity loaded", "login", minter.BotIdentity().Login)
		}
		cancelID()
	}

	// PR D-followup §6.4 — load the App's OAuth client_secret for the
	// discover-then-bind path. Empty value disables the bind path; the
	// discover endpoint surfaces 503 in that mode (logged at startup).
	var appClientSecret string
	if minter.AppID() != 0 {
		csCtx, cancelCS := context.WithTimeout(context.Background(), 10*time.Second)
		if cs, err := minter.LoadAppClientSecret(csCtx); err != nil {
			slog.Warn("app oauth client_secret load failed; bind path disabled", "error", err)
		} else if cs == "" {
			slog.Warn("no GITHUB_CLIENT_SECRET seeded; OAuth bind path disabled — only fresh-install callback works")
		} else {
			appClientSecret = cs
			slog.Info("app oauth client_secret loaded", "len", len(cs))
		}
		cancelCS()
	}

	// Phase 2 resolver — DB-backed, switches on kind. The Phase 0 invariant
	// (every call site threads ocOrgID) means no other code changes.
	resolver := credentials.NewOrgResolver(db, store, minter)

	// Services
	githubClient := services.NewGitHubClient()
	repoService := services.NewRepoService(
		repoRepo,
		githubClient,
		resolver,
		cfg.GitHubRepoVisibility,
		cfg.RepoBasePath,
	)
	gitOpsService := services.NewGitOpsService(repoRepo, resolver, cfg.RepoBasePath, githubClient)
	artifactService := services.NewArtifactService(repoRepo, gitOpsService)
	githubV2Client := services.NewGitHubV2Client()
	issueService := services.NewIssueService(repoRepo, githubClient, githubV2Client, resolver)

	gitOpsService.CleanupOrphanTmpClones()
	go func() {
		warmCtx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
		defer cancel()
		warmed, failed := gitOpsService.PreWarmClones(warmCtx, 10)
		slog.Info("pre-warm complete", "warmed", warmed, "failed", failed)
	}()
	branchService := services.NewBranchService(repoRepo, githubClient, issueService)
	prService := services.NewPullRequestService(repoRepo, githubClient, issueService)
	webhookService := services.NewWebhookService(
		repoRepo, githubClient, repoService, issueService,
		cfg.WebhookDeliveryURL, cfg.WebhookHMACSecret,
	)
	// Task JWTs carry verified ocOrgID and taskID claims (RS256, BFF-signed).
	// The cross-service callback into the BFF that the prior tripwire required
	// is gone — the JWT signature is the trust path.
	credRefreshService := services.NewCredentialsRefreshService(resolver)

	// Phase 2 PR B — internal credential routes. The service holds the
	// connect/disconnect orchestration, validation chain, install
	// lookups. Mounted under shared-secret middleware.
	credService := services.NewCredentialService(db, store, minter, cfg.WebhookHMACSecret, cfg.GitHubAppClientID, appClientSecret, githubClient)

	// No default-org PAT seed: the binary is org-agnostic. The local-dev
	// admin org is pre-connected by deployments-v2/scripts/lib/seed-admin-github.sh,
	// which calls the same /credentials/connect endpoint the console uses.
	// Hosted environments connect via the console UI per GUIDELINES.md §9.

	// Build credentials service. The stage-build-secret endpoint validates
	// (ocOrgId, repoSlug) ownership, resolves the org's GitHub credential,
	// and SSAs a per-WorkflowRun `kubernetes.io/basic-auth` Secret named
	// `<workflowRunName>-git-secret` into workflows-<ocOrgID>. The BFF
	// generates the WorkflowRun name upfront, calls this endpoint, then
	// POSTs the WorkflowRun with secretRef="" so the upstream workflow's
	// ExternalSecret synth is skipped. The token never crosses the boundary.
	buildCredService := services.NewBuildCredentialsService(repoRepo, resolver, wpClient)

	// Wire the post-disconnect WP-secret cleanup hook so disconnecting an
	// org wipes its build credential from the WP namespace too.
	credService.WithBuildSecretCleaner(buildCredService)

	// Anthropic credential service — per-org Anthropic API key surface.
	// Reads/writes encrypted bytes via `store` (Postgres org_secrets), maintains
	// the org_anthropic_credentials projection table, SSAs a per-org K8s Secret
	// into workflows-<orgID> on dispatch, and broadcasts a best-effort cache
	// invalidate to agents-service on Connect/Disconnect.
	//
	// Service-JWT-authed Bearer is omitted in dev; the agents-service
	// /v1/internal/cache/invalidate endpoint is reachable cluster-internally on
	// the in-cluster service URL.
	anthropicInvalidator := services.HTTPAgentsCacheInvalidator(cfg.AgentsServiceURL, "")
	anthropicCredService := services.NewAnthropicCredentialService(
		db, store, wpClient, cfg.AnthropicPlatformKey, anthropicInvalidator,
	)
	if cfg.AnthropicPlatformKey == "" {
		slog.Warn("ANTHROPIC_PLATFORM_KEY is empty — platform fallback disabled; orgs without their own key will see 503 from agents-service")
	} else {
		slog.Info("anthropic credential service: platform fallback configured", "agentsServiceURL", cfg.AgentsServiceURL)
	}

	// Periodic credential validator. Walks every active org_credentials row
	// once per cfg.CredentialValidatorInterval (default 24h), probes GitHub,
	// and records identity drift on confirmed unauthorised credentials.
	// Single-flight via pg_advisory_xact_lock(hashtext('validator')). The
	// BFF discovers disconnects via GitHub webhooks (uninstall, suspended);
	// the validator only flags stale identity in the DB.
	validatorProbes := services.NewValidatorProbes(credService, githubClient, resolver, minter)
	validator := credentials.NewValidator(db, validatorProbes, nil, cfg.CredentialValidatorInterval)
	boardService := services.NewBoardService(repoRepo, githubV2Client, resolver)

	// Controllers
	repoCtrl := controllers.NewRepoController(repoService)
	gitOpsCtrl := controllers.NewGitOpsController(gitOpsService)
	issueCtrl := controllers.NewIssueController(issueService)
	branchCtrl := controllers.NewBranchController(branchService)
	prCtrl := controllers.NewPullRequestController(prService)
	webhookCtrl := controllers.NewWebhookRegistrationController(webhookService)
	artifactCtrl := controllers.NewArtifactController(artifactService)
	credRefreshCtrl := controllers.NewCredentialsRefreshController(credRefreshService)

	// Two JWKS sources, one verifier each.
	//   1. Thunder JWKS  → User JWT + Service JWT (audience: git-service)
	//   2. BFF JWKS      → Task JWT (audience: git-service, RS256)
	var serviceJWT, taskJWT jwtassertion.Middleware
	if cfg.JWKSURL != "" {
		thunderJWKS := jwtassertion.NewJWKSCache(cfg.JWKSURL)
		serviceJWT = jwtassertion.Authenticator(jwtassertion.Config{
			JWKS:                thunderJWKS,
			AllowedIssuers:      filterEmpty(cfg.JWTAllowedIssuer),
			AllowedAudiences:    filterEmpty(cfg.JWTAllowedAudience),
			ResourceMetadataURL: cfg.JWTResourceMetadataURL,
		})
		slog.Info("Service JWT verifier", "jwksURL", cfg.JWKSURL, "audience", cfg.JWTAllowedAudience)
	} else {
		slog.Warn("JWKS_URL not set — Service JWT verification disabled (dev/test only)")
	}
	if cfg.BFFJWKSURL != "" {
		bffJWKS := jwtassertion.NewJWKSCache(cfg.BFFJWKSURL)
		taskJWT = jwtassertion.Authenticator(jwtassertion.Config{
			JWKS:                bffJWKS,
			AllowedIssuers:      filterEmpty(cfg.TaskJWTAllowedIssuer),
			AllowedAudiences:    filterEmpty(cfg.TaskJWTAllowedAudience),
			ResourceMetadataURL: cfg.JWTResourceMetadataURL,
		})
		slog.Info("Task JWT verifier", "jwksURL", cfg.BFFJWKSURL, "audience", cfg.TaskJWTAllowedAudience)
	} else {
		slog.Warn("BFF_JWKS_URL not set — Task JWT verification disabled (dev/test only)")
	}
	projectCtrl := controllers.NewProjectController(githubV2Client, resolver, repoService)
	boardCtrl := controllers.NewBoardController(boardService)

	// Handler
	handler := api.NewHandler(api.AppParams{
		TestMode:         cfg.TestMode,
		DB:               db,
		RepoCtrl:         repoCtrl,
		GitOpsCtrl:       gitOpsCtrl,
		IssueCtrl:        issueCtrl,
		BranchCtrl:       branchCtrl,
		PullRequestCtrl:  prCtrl,
		WebhookCtrl:      webhookCtrl,
		ArtifactCtrl:     artifactCtrl,
		CredCtrl:         credRefreshCtrl,
		BoardCtrl:        boardCtrl,
		RepoService:      repoService,
		RepoRepo:         repoRepo,
		CredService:          credService,
		BuildCredService:     buildCredService,
		AnthropicCredService: anthropicCredService,
		Validator:            validator,
		ServiceJWT:       serviceJWT,
		TaskJWT:          taskJWT,
		ProjectCtrl:      projectCtrl,
	})

	server := &http.Server{
		Addr:         fmt.Sprintf("%s:%d", cfg.ServerHost, cfg.ServerPort),
		Handler:      handler,
		ReadTimeout:  15 * time.Second,
		WriteTimeout: 5 * time.Minute,
		IdleTimeout:  60 * time.Second,
	}

	go func() {
		slog.Info("server started", "addr", server.Addr)
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			slog.Error("server error", "error", err)
			os.Exit(1)
		}
	}()

	// Phase 2 PR D — start the validator goroutine. Cancellation rides on
	// the same SIGINT/SIGTERM signal as the HTTP server.
	validatorCtx, cancelValidator := context.WithCancel(context.Background())
	defer cancelValidator()
	go func() {
		slog.Info("credential validator started", "interval", cfg.CredentialValidatorInterval)
		validator.Run(validatorCtx)
	}()

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


// filterEmpty maps "" to nil so an unconfigured allowed-issuer/audience
// becomes the empty list rather than [""].
func filterEmpty(s string) []string {
	if s == "" {
		return nil
	}
	return []string{s}
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
