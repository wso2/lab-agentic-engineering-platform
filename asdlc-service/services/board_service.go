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
	LifecycleStatus string                 `json:"lifecycleStatus,omitempty"`
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

	// Load DB tasks for enrichment.
	type taskDBMeta struct {
		id              string
		lifecycleStatus string
	}
	issueURLToMeta := map[string]taskDBMeta{}
	var allComponentTasks []models.ComponentTask
	// unissuedTasks are tasks with no IssueURL (gh_issue_waiting or gh_issue_failed).
	// They never appear on the GitHub Project board and must be surfaced separately.
	var unissuedTasks []models.ComponentTask
	if s.taskRepo != nil {
		if componentTasks, err := s.taskRepo.ListByProjectID(ctx, orgID, projectID); err == nil {
			allComponentTasks = componentTasks
			for _, ct := range componentTasks {
				if ct.IssueURL != "" {
					issueURLToMeta[ct.IssueURL] = taskDBMeta{
						id:              ct.ID,
						lifecycleStatus: ct.LifecycleStatus,
					}
				} else {
					unissuedTasks = append(unissuedTasks, ct)
				}
			}
		}
	}

	for _, item := range result.Items {
		task := BoardTask{
			ID:              item.ID,
			Title:           item.Title,
			URL:             item.URL,
			Description:     item.Body,
			Assignee:        item.Assignee,
			Labels:          item.Labels,
			LifecycleStatus: string(models.TaskLifecycleGhIssueCreated),
		}
		if meta, ok := issueURLToMeta[item.URL]; ok {
			task.ComponentTaskID = meta.id
			task.LifecycleStatus = meta.lifecycleStatus
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

	// Fallback: when the GitHub board has no items, show all component tasks from DB.
	if len(result.Items) == 0 && len(allComponentTasks) > 0 {
		for _, ct := range allComponentTasks {
			labels := make([]gitservice.LabelInfo, 0, len(ct.Labels))
			for _, l := range ct.Labels {
				labels = append(labels, gitservice.LabelInfo{Name: l})
			}
			// Board has 0 items — GitHub Project hasn't synced yet.
			// Override gh_issue_created → gh_issue_syncing in the response so
			// the frontend shows a skeleton instead of a labelless task card.
			// This value is never written to DB.
			lifecycleStatus := ct.LifecycleStatus
			if lifecycleStatus == string(models.TaskLifecycleGhIssueCreated) {
				lifecycleStatus = string(models.TaskLifecycleGhIssueSyncing)
			}
			task := BoardTask{
				ID:              ct.ID,
				Title:           ct.Title,
				URL:             ct.IssueURL,
				Description:     ct.Body,
				ComponentTaskID: ct.ID,
				Labels:          labels,
				LifecycleStatus: lifecycleStatus,
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
		return board, nil
	}

	// Always surface unissued tasks (gh_issue_waiting / gh_issue_failed) even
	// when the primary path is active. These have no IssueURL and are invisible
	// to the GitHub Project board.
	for _, ct := range unissuedTasks {
		task := BoardTask{
			ID:              ct.ID,
			Title:           ct.Title,
			ComponentTaskID: ct.ID,
			LifecycleStatus: ct.LifecycleStatus,
		}
		if ct.LifecycleStatus == string(models.TaskLifecycleGhIssueFailed) {
			board.Failed = append(board.Failed, task)
		} else {
			board.Todo = append(board.Todo, task)
		}
	}

	return board, nil
}

func normalizeStatus(s string) string {
	return strings.ToLower(strings.TrimSpace(s))
}
