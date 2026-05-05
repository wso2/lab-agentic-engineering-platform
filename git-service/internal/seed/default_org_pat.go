package seed

import (
	"context"
	"errors"
	"log/slog"
	"strings"

	"gorm.io/gorm"

	"github.com/wso2/asdlc/git-service/config"
	"github.com/wso2/asdlc/git-service/models"
	"github.com/wso2/asdlc/git-service/services"
)

// DefaultOrgPATFromEnv seeds a PAT-mode credential row for one or more
// OC orgs from env vars. Dev-tier only, env-gated, idempotent.
//
// The list of orgs to seed comes from `GITHUB_PLATFORM_PAT_SEED_ORGS`
// (comma-separated, default "default"). Multi-org seeding lets dev
// clusters bootstrap orgs other than "default" — e.g. when the IDP
// returns an admin user with subject claim `admin`, the BFF passes
// `admin` as the OC org id and we need a credential row for it too.
//
// Routes through CredentialService.Connect (the same path the console
// UI uses) — there is no parallel PAT-validation logic. The seed is
// just a tier/env/idempotency-gated invocation of the existing connect
// flow with credentials lifted from env vars.
//
// Production deployments leave GITHUB_PLATFORM_PAT/GITHUB_REPO_OWNER
// empty; the seed silently skips, and per-org connect via the console
// remains the only credential entry point.
//
// Idempotency policy: skip if any row exists for an org, regardless of
// status. Operators who explicitly disconnected don't get auto-
// resurrected on the next restart — to re-seed, delete the row and
// restart. This deliberately respects user intent over dev convenience.
func DefaultOrgPATFromEnv(
	ctx context.Context,
	db *gorm.DB,
	cfg config.Config,
	credService *services.CredentialService,
) error {
	if cfg.DeploymentTier != "dev" {
		slog.InfoContext(ctx, "default-org seed: skipped (DeploymentTier != dev)",
			"tier", cfg.DeploymentTier)
		return nil
	}
	if cfg.GitHubPlatformPAT == "" || cfg.GitHubRepoOwner == "" {
		slog.InfoContext(ctx, "default-org seed: skipped (PAT or owner not set)",
			"patSet", cfg.GitHubPlatformPAT != "",
			"ownerSet", cfg.GitHubRepoOwner != "")
		return nil
	}

	orgs := parseSeedOrgs(cfg.GitHubPlatformPATSeedOrgs)
	if len(orgs) == 0 {
		slog.InfoContext(ctx, "default-org seed: skipped (no orgs configured)")
		return nil
	}

	for _, org := range orgs {
		seedOne(ctx, db, cfg, credService, org)
	}
	return nil
}

func seedOne(
	ctx context.Context,
	db *gorm.DB,
	cfg config.Config,
	credService *services.CredentialService,
	orgHandle string,
) {
	var existing models.OrgCredential
	err := db.WithContext(ctx).
		Where("oc_org_id = ?", orgHandle).
		First(&existing).Error
	if err == nil {
		slog.InfoContext(ctx, "org seed: skipped (row exists)",
			"ocOrgId", orgHandle, "kind", existing.Kind, "status", existing.Status)
		return
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		slog.WarnContext(ctx, "org seed: db error",
			"ocOrgId", orgHandle, "error", err)
		return
	}

	proj, err := credService.Connect(ctx, orgHandle, services.ConnectRequest{
		Kind:        "user-pat",
		PAT:         cfg.GitHubPlatformPAT,
		GitHubLogin: cfg.GitHubRepoOwner,
	})
	if err != nil {
		// Don't fail startup — log and continue. A bad PAT shouldn't
		// brick the service for every other org.
		slog.WarnContext(ctx, "org seed: connect failed",
			"ocOrgId", orgHandle, "githubLogin", cfg.GitHubRepoOwner, "error", err)
		return
	}
	slog.InfoContext(ctx, "org seeded",
		"ocOrgId", orgHandle,
		"kind", proj.Kind,
		"githubLogin", proj.GitHubLogin,
		"identityLogin", proj.IdentityLogin)
}

func parseSeedOrgs(raw string) []string {
	out := []string{}
	seen := map[string]bool{}
	for _, p := range strings.Split(raw, ",") {
		p = strings.TrimSpace(p)
		if p == "" || seen[p] {
			continue
		}
		seen[p] = true
		out = append(out, p)
	}
	return out
}
