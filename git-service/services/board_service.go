package services

import (
	"context"
	"fmt"

	"github.com/wso2/asdlc/git-service/repositories"
)

// BoardService fetches and manages the GitHub Project v2 kanban board for a project.
type BoardService interface {
	GetBoard(ctx context.Context, projectID string) (*ProjectBoardResult, error)
	MoveIssueToStatus(ctx context.Context, projectID, issueURL, targetStatus string) error
}

type boardService struct {
	repo   repositories.RepoRepository
	github GitHubV2Client
	pat    string
}

func NewBoardService(repo repositories.RepoRepository, github GitHubV2Client, pat string) BoardService {
	return &boardService{repo: repo, github: github, pat: pat}
}

func (s *boardService) GetBoard(ctx context.Context, projectID string) (*ProjectBoardResult, error) {
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

	return s.github.GetProjectBoard(ctx, gitRepo.GithubProjectID, s.pat)
}

func (s *boardService) MoveIssueToStatus(ctx context.Context, projectID, issueURL, targetStatus string) error {
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

	return s.github.MoveProjectItemToStatus(ctx, gitRepo.GithubProjectID, issueURL, targetStatus, s.pat)
}
