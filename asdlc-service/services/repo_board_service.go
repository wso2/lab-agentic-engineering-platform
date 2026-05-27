package services

import (
	"context"
	"fmt"

	"github.com/wso2/asdlc/asdlc-service/internal/credentials"
	"github.com/wso2/asdlc/asdlc-service/repositories"
)

// RepoBoardService fetches and manages the GitHub Project v2 kanban board for
// a project. Folded in from git-service; renamed from the original
// BoardService to avoid collision with the BFF-side BoardService that
// aggregates ComponentTask DB rows on top of this layer.
type RepoBoardService interface {
	GetBoard(ctx context.Context, projectID string) (*ProjectBoardResult, error)
	MoveIssueToStatus(ctx context.Context, projectID, issueURL, targetStatus string) error
}

type repoBoardService struct {
	repo     repositories.RepoRepository
	github   GitHubV2Client
	resolver credentials.Resolver
}

func NewRepoBoardService(repo repositories.RepoRepository, github GitHubV2Client, resolver credentials.Resolver) RepoBoardService {
	return &repoBoardService{repo: repo, github: github, resolver: resolver}
}

func (s *repoBoardService) GetBoard(ctx context.Context, projectID string) (*ProjectBoardResult, error) {
	gitRepo, err := s.repo.GetByProjectID(ctx, projectID)
	if err != nil {
		return nil, fmt.Errorf("get repo: %w", err)
	}
	if gitRepo == nil {
		return nil, ErrRepoNotFound
	}
	if gitRepo.GithubProjectID == "" {
		return &ProjectBoardResult{}, nil
	}

	token, err := s.resolveToken(ctx, gitRepo.OrgID)
	if err != nil {
		return nil, err
	}
	return s.github.GetProjectBoard(ctx, gitRepo.GithubProjectID, token)
}

func (s *repoBoardService) MoveIssueToStatus(ctx context.Context, projectID, issueURL, targetStatus string) error {
	gitRepo, err := s.repo.GetByProjectID(ctx, projectID)
	if err != nil {
		return fmt.Errorf("get repo: %w", err)
	}
	if gitRepo == nil {
		return ErrRepoNotFound
	}
	if gitRepo.GithubProjectID == "" {
		return nil
	}

	token, err := s.resolveToken(ctx, gitRepo.OrgID)
	if err != nil {
		return err
	}
	return s.github.MoveProjectItemToStatus(ctx, gitRepo.GithubProjectID, issueURL, targetStatus, token)
}

func (s *repoBoardService) resolveToken(ctx context.Context, ocOrgID string) (string, error) {
	cred, err := s.resolver.Resolve(ctx, ocOrgID)
	if err != nil {
		return "", fmt.Errorf("resolve credential for org %q: %w", ocOrgID, err)
	}
	token, _, err := cred.Token(ctx)
	if err != nil {
		return "", fmt.Errorf("token for org %q: %w", ocOrgID, err)
	}
	return token, nil
}
