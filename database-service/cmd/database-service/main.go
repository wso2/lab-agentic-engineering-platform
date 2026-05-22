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

	"github.com/wso2/asdlc/database-service/api"
	"github.com/wso2/asdlc/database-service/config"
	"github.com/wso2/asdlc/database-service/controllers"
	"github.com/wso2/asdlc/database-service/middleware/jwtassertion"
	"github.com/wso2/asdlc/database-service/services"

	_ "github.com/lib/pq"
)

func main() {
	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to load config: %v\n", err)
		os.Exit(1)
	}

	setupLogger(cfg.LogLevel)

	// Services
	dbProvisioningService := services.NewDatabaseProvisioningService(
		cfg.MySQLRootURL,
		cfg.MySQLHost,
		cfg.MySQLPort,
	)

	// PostgreSQL-backed registry (required for BFF list/register/status endpoints)
	pg, err := services.OpenPostgres(cfg.DatabaseURL)
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to connect to postgres: %v\n", err)
		os.Exit(1)
	}
	defer pg.Close()

	registryService := services.NewDatabaseRegistryService(pg)
	if err := registryService.Migrate(context.Background()); err != nil {
		fmt.Fprintf(os.Stderr, "failed to migrate databases table: %v\n", err)
		os.Exit(1)
	}

	// Composite DatabaseService used by the MCP server.
	dbService := services.NewDatabaseService(registryService, dbProvisioningService, cfg.MySQLHost, cfg.MySQLPort)

	// Controllers
	dbCtrl := controllers.NewDatabaseController(dbProvisioningService)
	regCtrl := controllers.NewRegistryController(registryService)

	// JWKS cache for task JWT verification.
	var jwksCache *jwtassertion.JWKSCache
	if cfg.BFFJWKSURL != "" {
		jwksCache = jwtassertion.NewJWKSCache(cfg.BFFJWKSURL)
	} else {
		slog.Warn("BFF_JWKS_URL not set — all protected routes will reject requests with 401")
	}

	// Handler
	handler := api.NewHandler(api.AppParams{
		DatabaseCtrl:    dbCtrl,
		RegistryCtrl:    regCtrl,
		DatabaseSvc:     dbService,
		JWKS:            jwksCache,
		TaskJWTIssuer:   cfg.TaskJWTIssuer,
		TaskJWTAudience: cfg.TaskJWTAudience,
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
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		Level: logLevel,
	})))
}
