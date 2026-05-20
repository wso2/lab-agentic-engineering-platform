package services

import (
	"context"
	"fmt"
	"strings"
	"time"

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
	// Status is the ComponentTask execution status (pending, on_hold,
	// in_progress, verification_failed, ready_for_review, merged, building,
	// deployed, rejected, failed, abandoned). Empty when the row has no
	// backing ComponentTask.
	Status string `json:"status,omitempty"`
	// DispatchedAt is the time the task was dispatched for execution.
	// Nil for never-dispatched tasks; the frontend uses it to render
	// "started Xm ago" and to gate the Live progress affordance.
	DispatchedAt *time.Time `json:"dispatchedAt,omitempty"`
	// ExecType mirrors ComponentTask.ExecType ("SYSTEM","WORKER"). The
	// frontend gates the per-task "Execute Now" button on this — the
	// endpoint behind that button (/tasks/{id}/exec) only does meaningful
	// work for SYSTEM tasks; WORKER (coding-agent) tasks must go through
	// the batch /tasks/dispatch path via "Execute all → Remote Agents".
	ExecType string `json:"execType,omitempty"`
	// DependsOnComponents mirrors ComponentTask.DependsOnComponents — the
	// list of component names this task is waiting to be deployed before
	// it can dispatch. Populated for every task; the On Hold column
	// reads it to show "Waiting for: …".
	DependsOnComponents []string `json:"dependsOnComponents,omitempty"`
	// ComponentName mirrors ComponentTask.ComponentName so the frontend
	// can resolve dep -> task lookups (e.g. "what is component `todo-api`'s
	// task currently doing while we wait?").
	ComponentName string `json:"componentName,omitempty"`
	// ErrorMessage mirrors ComponentTask.ErrorMessage. For F3c
	// verification_failed it's the diagnostic the agent reported, shown
	// on the card so the operator can decide whether to retry.
	ErrorMessage string `json:"errorMessage,omitempty"`
}

// ProjectBoard holds tasks grouped by their kanban column.
type ProjectBoard struct {
	URL string `json:"url"`
	Todo        []BoardTask `json:"todo"`
	InProgress  []BoardTask `json:"inProgress"`
	Done        []BoardTask `json:"done"`
	OnHold      []BoardTask `json:"onHold"`
	Failed      []BoardTask `json:"failed"`
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
		id                  string
		lifecycleStatus     string
		status              string
		dispatchedAt        *time.Time
		execType            string
		dependsOnComponents []string
		componentName       string
		errorMessage        string
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
						id:                  ct.ID,
						lifecycleStatus:     ct.LifecycleStatus,
						status:              ct.Status,
						dispatchedAt:        ct.DispatchedAt,
						execType:            ct.ExecType,
						dependsOnComponents: []string(ct.DependsOnComponents),
						componentName:       ct.ComponentName,
						errorMessage:        ct.ErrorMessage,
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
			task.Status = meta.status
			task.DispatchedAt = meta.dispatchedAt
			task.ExecType = meta.execType
			task.DependsOnComponents = meta.dependsOnComponents
			task.ComponentName = meta.componentName
			task.ErrorMessage = meta.errorMessage
		}
		// The BFF's ComponentTask.Status is authoritative for kanban routing
		// of `on_hold` (dep-gated), terminal failure states, and
		// `verification_failed` tasks. Terminal failure states must never be
		// overridden by the GitHub board column (which may not have been
		// updated yet, e.g. when markFailed's MoveIssueToStatus call fails).
		switch task.Status {
		case string(models.TaskStatusOnHold):
			board.OnHold = append(board.OnHold, task)
			continue
		case string(models.TaskStatusFailed),
			string(models.TaskStatusRejected),
			string(models.TaskStatusAbandoned),
			string(models.TaskStatusVerificationFailed):
			board.Failed = append(board.Failed, task)
			continue
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
				ID:                  ct.ID,
				Title:               ct.Title,
				URL:                 ct.IssueURL,
				Description:         ct.Body,
				ComponentTaskID:     ct.ID,
				Labels:              labels,
				LifecycleStatus:     lifecycleStatus,
				Status:              ct.Status,
				DispatchedAt:        ct.DispatchedAt,
				ExecType:            ct.ExecType,
				DependsOnComponents: []string(ct.DependsOnComponents),
				ComponentName:       ct.ComponentName,
				ErrorMessage:        ct.ErrorMessage,
			}
			switch ct.Status {
			case "on_hold":
				board.OnHold = append(board.OnHold, task)
			case "in_progress":
				board.InProgress = append(board.InProgress, task)
			case "ready_for_review", "merged", "building", "deployed":
				board.Done = append(board.Done, task)
			case "failed", "rejected", "verification_failed":
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
			ID:                  ct.ID,
			Title:               ct.Title,
			ComponentTaskID:     ct.ID,
			LifecycleStatus:     ct.LifecycleStatus,
			Status:              ct.Status,
			DispatchedAt:        ct.DispatchedAt,
			ExecType:            ct.ExecType,
			DependsOnComponents: []string(ct.DependsOnComponents),
			ComponentName:       ct.ComponentName,
			ErrorMessage:        ct.ErrorMessage,
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
