package services

import (
	"context"
	"fmt"

	"github.com/wso2/asdlc/asdlc-service/internal/credentials"
	"github.com/wso2/asdlc/asdlc-service/repositories"
)

// WebhookService manages per-repo webhook registration on GitHub.
//
// Phase 0 always uses the per-repo strategy (the credential's WebhookStrategy
// is WebhookPerRepo). Phase 2 introduces App-installation credentials whose
// strategy is WebhookPlatform; those skip per-repo registration entirely
// because the App's configured callback handles delivery for every install.
//
// Callers dispatch the strategy without inspecting the kind — see Register's
// short-circuit on WebhookPlatform.
type WebhookService interface {
	// Register installs a webhook on the repo. No-op when the credential's
	// strategy is WebhookPlatform. Idempotent on (repo, deliveryURL).
	Register(ctx context.Context, projectID string) (hookID *int64, err error)
	// Deregister removes the previously-registered webhook for the repo.
	Deregister(ctx context.Context, projectID string) error
}

type webhookService struct {
	repo               repositories.RepoRepository
	github             GitHubClient
	repoSvc            RepoService
	issue              *issueService
	deliveryURL        string
	hmacSecret         string
	subscribedEvents   []string
}

func NewWebhookService(
	repo repositories.RepoRepository,
	github GitHubClient,
	repoSvc RepoService,
	issueSvc IssueService,
	deliveryURL, hmacSecret string,
) WebhookService {
	is, _ := issueSvc.(*issueService)
	return &webhookService{
		repo:        repo,
		github:      github,
		repoSvc:     repoSvc,
		issue:       is,
		deliveryURL: deliveryURL,
		hmacSecret:  hmacSecret,
		// Events subscribed to. Repo-level webhooks only — App-installation
		// events like installation_repositories are scoped to the App's own
		// callback and are rejected by GitHub on repo webhooks (422).
		subscribedEvents: []string{
			"pull_request",
			"push",
			"issue_comment",
		},
	}
}

func (s *webhookService) Register(ctx context.Context, projectID string) (*int64, error) {
	if s.deliveryURL == "" || s.hmacSecret == "" {
		return nil, fmt.Errorf("webhook delivery URL or HMAC secret not configured — set GITHUB_WEBHOOK_DELIVERY_URL and GITHUB_WEBHOOK_SECRET")
	}

	owner, repoName, cred, err := s.issue.resolveRepoAndCredential(ctx, projectID)
	if err != nil {
		return nil, err
	}

	// Phase 2 App-mode short-circuit: platform-level delivery, no per-repo
	// registration. Phase 0 platform-PAT always returns WebhookPerRepo.
	if cred.WebhookStrategy() == credentials.WebhookPlatform {
		return nil, nil
	}

	hookID, err := s.github.RegisterWebhook(
		ctx, owner, repoName, cred,
		s.deliveryURL, s.hmacSecret,
		s.subscribedEvents,
	)
	if err != nil {
		return nil, fmt.Errorf("register webhook: %w", err)
	}

	if err := s.repoSvc.SetWebhookID(ctx, projectID, hookID); err != nil {
		return nil, fmt.Errorf("persist webhook id: %w", err)
	}
	return &hookID, nil
}

func (s *webhookService) Deregister(ctx context.Context, projectID string) error {
	owner, repoName, cred, err := s.issue.resolveRepoAndCredential(ctx, projectID)
	if err != nil {
		return err
	}

	gitRepo, err := s.repo.GetByProjectID(ctx, projectID)
	if err != nil {
		return fmt.Errorf("get repo: %w", err)
	}
	if gitRepo == nil || gitRepo.WebhookID == nil {
		return nil // nothing to deregister
	}

	if err := s.github.DeregisterWebhook(ctx, owner, repoName, cred, *gitRepo.WebhookID); err != nil {
		return fmt.Errorf("deregister webhook: %w", err)
	}
	gitRepo.WebhookID = nil
	if err := s.repo.Update(ctx, gitRepo); err != nil {
		return fmt.Errorf("clear webhook id: %w", err)
	}
	return nil
}
