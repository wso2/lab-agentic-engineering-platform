// uninstall-orphans is a one-shot operational CLI that finds GitHub App
// installations with no matching org_credentials row and uninstalls them
// on github.com via DELETE /app/installations/{id}.
//
// Background: the architectural refactor that introduced the binding-
// centric connect flow ensures going forward that disconnect uninstalls
// the App on github.com (Phase E) so install ↔ binding lifetimes stay
// in lockstep. But pre-refactor disconnects left the GitHub-side install
// alive, which had two bad effects:
//
//   1. Orphans accumulated on github.com — visible to the platform admin
//      via /app/installations but not bound to any ASDLC org.
//   2. Pre-refactor's `discover` endpoint surfaced these orphans cross-
//      tenant; even though /discover is removed now, the GitHub state
//      should still be cleaned up so the platform's view of "where is
//      my App installed" matches reality.
//
// Usage:
//
//	go run ./cmd/uninstall-orphans            # dry-run (default)
//	go run ./cmd/uninstall-orphans --apply    # actually delete
//
// Reads the same config the main git-service binary reads (config.Load)
// so DATABASE_URL / OPENBAO_* / GITHUB_APP_* are picked up identically.
package main

import (
	"context"
	"encoding/base64"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"time"

	"gorm.io/gorm"

	"github.com/wso2/asdlc/git-service/config"
	"github.com/wso2/asdlc/git-service/database"
	"github.com/wso2/asdlc/git-service/models"
	"github.com/wso2/asdlc/git-service/pkg/credentials"
	"github.com/wso2/asdlc/git-service/services"
)

func main() {
	apply := flag.Bool("apply", false, "actually uninstall orphans (default: dry-run)")
	flag.Parse()

	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo})))

	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintf(os.Stderr, "config load: %v\n", err)
		os.Exit(1)
	}

	db, err := database.Open(cfg.DatabaseURL)
	if err != nil {
		fmt.Fprintf(os.Stderr, "db open: %v\n", err)
		os.Exit(1)
	}

	credKey, err := base64.StdEncoding.DecodeString(cfg.CredentialEncryptionKey)
	if err != nil || len(credKey) != 32 {
		fmt.Fprintf(os.Stderr, "CREDENTIAL_ENCRYPTION_KEY must be base64-encoded 32 bytes: %v\n", err)
		os.Exit(1)
	}
	store, err := credentials.NewDBStore(db, credKey)
	if err != nil {
		fmt.Fprintf(os.Stderr, "credential store init: %v\n", err)
		os.Exit(1)
	}

	loadCtx, cancelLoad := context.WithTimeout(context.Background(), 60*time.Second)
	appKey, err := credentials.LoadAppKeyFromOpenBao(loadCtx, store)
	cancelLoad()
	if err != nil || appKey == nil {
		fmt.Fprintf(os.Stderr, "app key load: %v\n", err)
		os.Exit(1)
	}
	minter, err := credentials.NewAppTokenMinter(appKey)
	if err != nil {
		fmt.Fprintf(os.Stderr, "minter init: %v\n", err)
		os.Exit(1)
	}
	minter.WithOpenBao(store)

	gh := services.NewGitHubClient()

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	all, err := gh.ListAppInstallations(ctx, minter)
	if err != nil {
		fmt.Fprintf(os.Stderr, "list installations: %v\n", err)
		os.Exit(1)
	}

	bound, err := loadBoundInstallationIDs(ctx, db)
	if err != nil {
		fmt.Fprintf(os.Stderr, "load bound: %v\n", err)
		os.Exit(1)
	}

	var orphans []services.AppInstallationSummary
	for _, inst := range all {
		if _, isBound := bound[inst.InstallationID]; !isBound {
			orphans = append(orphans, inst)
		}
	}

	mode := "dry-run"
	if *apply {
		mode = "apply"
	}
	fmt.Fprintf(os.Stdout, "summary: total_installs=%d bound=%d orphans=%d mode=%s\n",
		len(all), len(bound), len(orphans), mode)
	for _, o := range orphans {
		fmt.Fprintf(os.Stdout, "  orphan: installationId=%d account=%s type=%s\n",
			o.InstallationID, o.AccountLogin, o.AccountType)
	}

	if !*apply {
		fmt.Fprintf(os.Stdout, "\ndry-run only — re-run with --apply to uninstall the %d orphan(s) above.\n", len(orphans))
		return
	}

	deleted := 0
	failed := 0
	for _, o := range orphans {
		if err := gh.DeleteInstallation(ctx, minter, o.InstallationID); err != nil {
			fmt.Fprintf(os.Stderr, "  FAILED installationId=%d account=%s err=%v\n", o.InstallationID, o.AccountLogin, err)
			failed++
			continue
		}
		fmt.Fprintf(os.Stdout, "  deleted installationId=%d account=%s\n", o.InstallationID, o.AccountLogin)
		deleted++
	}
	fmt.Fprintf(os.Stdout, "\ndone: deleted=%d failed=%d\n", deleted, failed)
	if failed > 0 {
		os.Exit(2)
	}
}

func loadBoundInstallationIDs(ctx context.Context, db *gorm.DB) (map[int64]struct{}, error) {
	type boundRow struct {
		InstallationID int64
	}
	var rows []boundRow
	if err := db.WithContext(ctx).
		Model(&models.OrgCredential{}).
		Where("installation_id IS NOT NULL").
		Select("installation_id").
		Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("scan bound installs: %w", err)
	}
	out := make(map[int64]struct{}, len(rows))
	for _, r := range rows {
		out[r.InstallationID] = struct{}{}
	}
	return out, nil
}
