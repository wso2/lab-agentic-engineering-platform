package services

import (
	"context"
	"fmt"
	"strings"

	"github.com/wso2/asdlc/asdlc-service/clients/gitservice"
	"github.com/wso2/asdlc/asdlc-service/models"
	"github.com/wso2/asdlc/asdlc-service/repositories"
)

// BoardTask is a single task item on the project board.
type BoardTask struct {
	ID              string                 `json:"id"`
	Title           string                 `json:"title"`
	URL             string                 `json:"url"`
	Description     string                 `json:"description,omitempty"`
	Assignee        string                 `json:"assignee,omitempty"`
	ComponentTaskID string                 `json:"componentTaskId,omitempty"`
	Labels          []gitservice.LabelInfo `json:"labels,omitempty"`
}

// ProjectBoard holds tasks grouped by their kanban column.
type ProjectBoard struct {
	URL        string      `json:"url"`
	Todo       []BoardTask `json:"todo"`
	InProgress []BoardTask `json:"inProgress"`
	Done       []BoardTask `json:"done"`
	OnHold     []BoardTask `json:"onHold"`
	Failed     []BoardTask `json:"failed"`
}

// BoardService fetches the kanban board for a project.
type BoardService interface {
	GetBoard(ctx context.Context, orgID, projectID string) (*ProjectBoard, error)
}

type boardService struct {
	gitClient gitservice.Client
	taskRepo  repositories.TaskRepository
}

func NewBoardService(gitClient gitservice.Client, taskRepo repositories.TaskRepository) BoardService {
	return &boardService{gitClient: gitClient, taskRepo: taskRepo}
}

func (s *boardService) GetBoard(ctx context.Context, orgID, projectID string) (*ProjectBoard, error) {
	board := &ProjectBoard{
		URL:        "",
		Todo:       []BoardTask{},
		InProgress: []BoardTask{},
		Done:       []BoardTask{},
		OnHold:     []BoardTask{},
		Failed:     []BoardTask{},
	}

	if s.gitClient == nil {
		return board, nil
	}

	result, err := s.gitClient.GetBoard(ctx, projectID)
	if err != nil {
		return nil, fmt.Errorf("get board from git-service: %w", err)
	}

	// Build a map of issue URL -> component task ID to enrich board items.
	issueURLToTaskID := map[string]string{}
	var allComponentTasks []models.ComponentTask
	if s.taskRepo != nil {
		if componentTasks, err := s.taskRepo.ListByProjectID(ctx, orgID, projectID); err == nil {
			allComponentTasks = componentTasks
			for _, ct := range componentTasks {
				if ct.IssueURL != "" {
					issueURLToTaskID[ct.IssueURL] = ct.ID
				}
			}
		}
	}

	for _, item := range result.Items {
		task := BoardTask{
			ID:          item.ID,
			Title:       item.Title,
			URL:         item.URL,
			Description: item.Body,
			Assignee:    item.Assignee,
			Labels:      item.Labels,
		}
		if id, ok := issueURLToTaskID[item.URL]; ok {
			task.ComponentTaskID = id
		}
		switch normalizeStatus(item.Status) {
		case "in progress":
			board.InProgress = append(board.InProgress, task)
		case "done":
			board.Done = append(board.Done, task)
		case "on hold":
			board.OnHold = append(board.OnHold, task)
		case "failed":
			board.Failed = append(board.Failed, task)
		default:
			board.Todo = append(board.Todo, task)
		}
	}
	board.URL = result.URL

	// Fallback: when the GitHub board has no items, show component tasks from DB.
	if len(result.Items) == 0 && len(allComponentTasks) > 0 {
		for _, ct := range allComponentTasks {
			labels := make([]gitservice.LabelInfo, 0, len(ct.Labels))
			for _, l := range ct.Labels {
				labels = append(labels, gitservice.LabelInfo{Name: l})
			}
			task := BoardTask{
				ID:              ct.ID,
				Title:           ct.Title,
				URL:             ct.IssueURL,
				Description:     ct.Body,
				ComponentTaskID: ct.ID,
				Labels:          labels,
			}
			switch ct.Status {
			case "in_progress":
				board.InProgress = append(board.InProgress, task)
			case "ready_for_review", "merged", "building", "deployed":
				board.Done = append(board.Done, task)
			case "failed", "rejected":
				board.Failed = append(board.Failed, task)
			default:
				board.Todo = append(board.Todo, task)
			}
		}
	}

	return board, nil
}

func normalizeStatus(s string) string {
	return strings.ToLower(strings.TrimSpace(s))
}
