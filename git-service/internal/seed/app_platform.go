package seed

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/wso2/asdlc/git-service/config"
	"github.com/wso2/asdlc/git-service/pkg/credentials"
)

// AppPlatformFromEnv writes the GitHub App's appID, clientID, private key,
// and webhook secret into OpenBao at secret/asdlc/_platform/github/app/*
// when DeploymentTier=dev and the env values are present.
//
// Idempotent: re-runs overwrite with the same values (KV v2 versions, but
// the latest version is what readers see).
//
// Refuses outside dev — production seeds via the operational runbook
// documented in docs/operations/github-app.md.
//
// Reaches into the platform namespace via OpenBaoStore.AsPlatformSeeder,
// which is the narrow seam exported from pkg/credentials/ for exactly
// this purpose. The wider OpenBaoStore interface deliberately omits
// platform-write so per-org code can't escape into _platform/.
func AppPlatformFromEnv(ctx context.Context, store credentials.OpenBaoStore, cfg config.Config) error {
	if cfg.DeploymentTier != "dev" {
		slog.Info("app-platform seed: skipped (DeploymentTier != dev)", "tier", cfg.DeploymentTier)
		return nil
	}
	if cfg.GitHubAppID == "" || cfg.GitHubAppPrivateKeyPath == "" {
		slog.Info("app-platform seed: skipped (no GITHUB_APP_ID or GITHUB_APP_PRIVATE_KEY_PATH set)",
			"appIdSet", cfg.GitHubAppID != "",
			"keyPathSet", cfg.GitHubAppPrivateKeyPath != "")
		return nil
	}

	pemBytes, err := os.ReadFile(cfg.GitHubAppPrivateKeyPath)
	if err != nil {
		// Missing key file is a soft skip: the operator hasn't dropped the
		// PEM yet. git-service stays in "no App configured" mode and PR B's
		// PAT-mode flows still work. App-mode connect surfaces a clear
		// "App not configured" error at the connect endpoint.
		if os.IsNotExist(err) {
			slog.Warn("app-platform seed: private key file not found; App-mode connect will be unavailable",
				"path", cfg.GitHubAppPrivateKeyPath,
				"hint", "download from github.com/settings/apps/asdlc-platform → 'Generate a private key' and drop at this path")
			return nil
		}
		return fmt.Errorf("app-platform seed: read %s: %w", cfg.GitHubAppPrivateKeyPath, err)
	}
	if len(pemBytes) == 0 {
		slog.Warn("app-platform seed: private key file is empty; App-mode connect will be unavailable", "path", cfg.GitHubAppPrivateKeyPath)
		return nil
	}
	if !looksLikePEM(pemBytes) {
		// File is non-empty but not a PEM — surface as a hard error so the
		// operator notices they dropped the wrong file (vs. silently going
		// into no-App mode and being confused later).
		return fmt.Errorf("app-platform seed: %s is %d bytes but does not contain a PEM-encoded RSA key (drop the .pem you downloaded from GitHub App settings → 'Generate a private key')", cfg.GitHubAppPrivateKeyPath, len(pemBytes))
	}

	seeder, ok := credentials.AsPlatformSeeder(store)
	if !ok {
		slog.Warn("app-platform seed: store is not the real OpenBao implementation; skipping")
		return nil
	}

	pairs := map[string]string{
		"github/app/app_id":      cfg.GitHubAppID,
		"github/app/private_key": string(pemBytes),
	}
	if cfg.GitHubAppClientID != "" {
		pairs["github/app/client_id"] = cfg.GitHubAppClientID
	}
	// PR D-followup §6.4 — the OAuth bind path needs the client_secret
	// to exchange the user's OAuth code for a user-token. Stored alongside
	// the App's other platform secrets; only consumed by git-service's
	// CredentialService.BindAppInstallation. Empty value disables the
	// bind path (logged at startup; discover endpoint returns 503).
	if cfg.GitHubAppClientSecret != "" {
		pairs["github/app/client_secret"] = cfg.GitHubAppClientSecret
	}
	// Reuse GITHUB_WEBHOOK_SECRET for the App-mode webhook (per the PR B
	// plan decision). Stored as a JSON list of {secret, added_at} entries
	// so rotation has the same shape as PAT-mode webhook_secrets[].
	if cfg.WebhookHMACSecret != "" {
		entries := []map[string]any{
			{"secret": cfg.WebhookHMACSecret, "added_at": time.Now().UTC().Format(time.RFC3339)},
		}
		buf, _ := json.Marshal(entries)
		pairs["github/app/webhook_secret"] = string(buf)
	}

	for key, value := range pairs {
		if err := seeder.SeedPlatformValue(ctx, key, value, "app-platform"); err != nil {
			return fmt.Errorf("app-platform seed: write %s: %w", key, err)
		}
	}
	slog.Info("app-platform seed: complete",
		"appId", cfg.GitHubAppID,
		"appSlug", cfg.GitHubAppSlug,
		"wroteWebhookSecret", cfg.WebhookHMACSecret != "")
	return nil
}

// looksLikePEM does a cheap shape check on the file before we try to
// write it into OpenBao. Catches the common "user copied wrong file"
// mistake during the connect-flow operator runbook.
func looksLikePEM(b []byte) bool {
	s := string(b)
	return strings.Contains(s, "-----BEGIN") && strings.Contains(s, "PRIVATE KEY")
}
