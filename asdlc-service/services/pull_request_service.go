package services

import (
	"context"
	"fmt"

	"github.com/wso2/asdlc/asdlc-service/repositories"
)

// PullRequestService opens draft pull requests linking task issues to feature
// branches. The PR is the human-review gate — a draft is opened at dispatch,
// the agent flips it to ready when implementation is complete, and a human
// merges. See github-integration-evolution.md §3.1.
type PullRequestService interface {
	// CreateDraftPR opens a draft PR. Idempotent on (head, base): returns the
	// existing PR if one is already open against the same head branch.
	CreateDraftPR(ctx context.Context, projectID string, req CreateDraftPRRequest) (*PullRequestResult, error)
}

type pullRequestService struct {
	repo   repositories.RepoRepository
	github GitHubClient
	issue  *issueService
}

func NewPullRequestService(repo repositories.RepoRepository, github GitHubClient, issueSvc IssueService) PullRequestService {
	is, _ := issueSvc.(*issueService)
	return &pullRequestService{repo: repo, github: github, issue: is}
}

func (s *pullRequestService) CreateDraftPR(ctx context.Context, projectID string, req CreateDraftPRRequest) (*PullRequestResult, error) {
	if req.Head == "" || req.Base == "" || req.Title == "" {
		return nil, fmt.Errorf("head, base, and title are required")
	}

	owner, repoName, cred, err := s.issue.resolveRepoAndCredential(ctx, projectID)
	if err != nil {
		return nil, err
	}
	return s.github.CreateDraftPR(ctx, owner, repoName, cred, req)
}
