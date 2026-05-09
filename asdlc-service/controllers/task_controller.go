package controllers

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/wso2/asdlc/asdlc-service/clients/openchoreo"
	"github.com/wso2/asdlc/asdlc-service/models"
	"github.com/wso2/asdlc/asdlc-service/repositories"
	"github.com/wso2/asdlc/asdlc-service/services"
	"github.com/wso2/asdlc/asdlc-service/utils"
)

type TaskController interface {
	ListTasks(w http.ResponseWriter, r *http.Request)
	ListOrgTasks(w http.ResponseWriter, r *http.Request)
	GetTask(w http.ResponseWriter, r *http.Request)
	GetTaskStatus(w http.ResponseWriter, r *http.Request)
	GetTasks(w http.ResponseWriter, r *http.Request)
	DispatchTasks(w http.ResponseWriter, r *http.Request)
	GenerateTasks(w http.ResponseWriter, r *http.Request)
	RegenerateTaskBody(w http.ResponseWriter, r *http.Request)
	ExecTask(w http.ResponseWriter, r *http.Request)

	// Progress endpoints — task-execution-progress.md §5.2.
	GetTaskAgentProgress(w http.ResponseWriter, r *http.Request)
	GetTaskBuildProgress(w http.ResponseWriter, r *http.Request)
}

type taskController struct {
	service     services.TaskService
	dispatchSvc services.DispatchService
	progressSvc services.ProgressService
	ocClient    openchoreo.ComponentClient
}

func NewTaskController(
	service services.TaskService,
	dispatchSvc services.DispatchService,
	progressSvc services.ProgressService,
	ocClient openchoreo.ComponentClient,
) TaskController {
	return &taskController{
		service:     service,
		dispatchSvc: dispatchSvc,
		progressSvc: progressSvc,
		ocClient:    ocClient,
	}
}

func (c *taskController) ListTasks(w http.ResponseWriter, r *http.Request) {
	org := r.PathValue("orgHandle")
	project := r.PathValue("projectName")
	if !requireOrgHandle(w, org) || !requireProjectName(w, project) {
		return
	}

	tasks, err := c.service.ListTasks(r.Context(), org, project)
	if err != nil {
		slog.ErrorContext(r.Context(), "list tasks failed", "error", err, "org", org, "project", project)
		utils.WriteErrorResponse(w, http.StatusInternalServerError, "failed to list tasks")
		return
	}

	utils.WriteSuccessResponse(w, http.StatusOK, tasks)
}

// ListOrgTasks lists every task under the org with optional ?status, ?cause,
// and ?since filters. since accepts either an RFC3339 timestamp or a relative
// "24h" / "7d" shorthand. Used by the ReachReconciliationBanner.
func (c *taskController) ListOrgTasks(w http.ResponseWriter, r *http.Request) {
	org := r.PathValue("orgHandle")
	if !requireOrgHandle(w, org) {
		return
	}
	q := r.URL.Query()
	filter := repositories.ListByOrgFilter{
		Status: q.Get("status"),
		Cause:  q.Get("cause"),
	}
	if rawSince := q.Get("since"); rawSince != "" {
		if dur, err := time.ParseDuration(rawSince); err == nil {
			t := time.Now().Add(-dur)
			filter.Since = &t
		} else if t, err := time.Parse(time.RFC3339, rawSince); err == nil {
			filter.Since = &t
		} else {
			utils.WriteErrorResponse(w, http.StatusBadRequest, "since must be RFC3339 or duration (e.g. 24h)")
			return
		}
	}
	tasks, err := c.service.ListTasksByOrg(r.Context(), org, filter)
	if err != nil {
		slog.ErrorContext(r.Context(), "list org tasks failed", "error", err, "org", org)
		utils.WriteErrorResponse(w, http.StatusInternalServerError, "failed to list org tasks")
		return
	}
	utils.WriteSuccessResponse(w, http.StatusOK, tasks)
}

func (c *taskController) GetTask(w http.ResponseWriter, r *http.Request) {
	taskID := r.PathValue("taskId")
	if !requireTaskID(w, taskID) {
		return
	}

	task, err := c.service.GetTask(r.Context(), taskID)
	if err != nil {
		if errors.Is(err, services.ErrTaskNotFound) {
			utils.WriteErrorResponse(w, http.StatusNotFound, "task not found")
			return
		}
		slog.ErrorContext(r.Context(), "get task failed", "error", err, "taskId", taskID)
		utils.WriteErrorResponse(w, http.StatusInternalServerError, "failed to get task")
		return
	}

	utils.WriteSuccessResponse(w, http.StatusOK, task)
}

func (c *taskController) GetTasks(w http.ResponseWriter, r *http.Request) {
	org := r.PathValue("orgHandle")
	project := r.PathValue("projectName")
	if !requireOrgHandle(w, org) || !requireProjectName(w, project) {
		return
	}

	tasks, err := c.service.GetTasks(r.Context(), org, project)
	if err != nil {
		slog.ErrorContext(r.Context(), "get tasks failed", "error", err, "org", org, "project", project)
		utils.WriteErrorResponse(w, http.StatusInternalServerError, "failed to get tasks")
		return
	}

	if tasks == nil {
		utils.WriteSuccessResponse(w, http.StatusOK, nil)
		return
	}
	utils.WriteSuccessResponse(w, http.StatusOK, tasks)
}

func (c *taskController) DispatchTasks(w http.ResponseWriter, r *http.Request) {
	if c.dispatchSvc == nil {
		utils.WriteErrorResponse(w, http.StatusServiceUnavailable, "dispatch service not configured")
		return
	}

	org := r.PathValue("orgHandle")
	project := r.PathValue("projectName")
	if !requireOrgHandle(w, org) || !requireProjectName(w, project) {
		return
	}

	results, err := c.dispatchSvc.DispatchTasks(r.Context(), org, project)
	if err != nil {
		slog.ErrorContext(r.Context(), "dispatch tasks failed", "error", err, "org", org, "project", project)
		utils.WriteErrorResponse(w, http.StatusInternalServerError, "failed to dispatch tasks")
		return
	}

	utils.WriteSuccessResponse(w, http.StatusOK, results)
}

// GenerateTasks streams the two-phase tech-lead orchestration as SSE.
// Mirrors design_controller.GenerateDesign.
func (c *taskController) GenerateTasks(w http.ResponseWriter, r *http.Request) {
	org := r.PathValue("orgHandle")
	project := r.PathValue("projectName")
	if !requireOrgHandle(w, org) || !requireProjectName(w, project) {
		return
	}

	flusher, ok := w.(http.Flusher)
	if !ok {
		utils.WriteErrorResponse(w, http.StatusInternalServerError, "streaming not supported")
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	w.Header().Set("x-vercel-ai-ui-message-stream", "v1")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	if err := c.service.StreamGenerateTasks(r.Context(), org, project, w, flusher.Flush); err != nil {
		slog.ErrorContext(r.Context(), "generate tasks failed", "error", err, "org", org, "project", project)
		errText := err.Error()
		switch {
		case errors.Is(err, services.ErrDesignNotFound):
			errText = "design not found"
		case errors.Is(err, services.ErrSpecNotFound):
			errText = "spec not found"
		}
		errFrame, _ := json.Marshal(map[string]any{"type": "error", "data": map[string]any{"scope": "plan", "errorText": errText}})
		fmt.Fprintf(w, "data: %s\n\n", errFrame)
		flusher.Flush()
	}
}

// RegenerateTaskBody re-runs Phase 2 detail for a single task.
func (c *taskController) RegenerateTaskBody(w http.ResponseWriter, r *http.Request) {
	taskID := r.PathValue("taskId")
	if !requireTaskID(w, taskID) {
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		utils.WriteErrorResponse(w, http.StatusInternalServerError, "streaming not supported")
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	w.Header().Set("x-vercel-ai-ui-message-stream", "v1")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()
	if err := c.service.RegenerateTaskBody(r.Context(), taskID, w, flusher.Flush); err != nil {
		slog.ErrorContext(r.Context(), "regenerate task body failed", "taskId", taskID, "error", err)
		errFrame, _ := json.Marshal(map[string]any{"type": "error", "data": map[string]any{"scope": "detail", "taskId": taskID, "errorText": err.Error()}})
		fmt.Fprintf(w, "data: %s\n\n", errFrame)
		flusher.Flush()
	}
}

// TaskStatusResponse extends the per-task GET payload with the build
// run's per-step task list, so the console's pipeline strip can render
// without an extra round-trip. The task fields are inlined alongside a
// separate buildSteps slice — design §5.2.
type TaskStatusResponse struct {
	Task       *models.ComponentTask    `json:"task"`
	BuildSteps []models.WorkflowRunTask `json:"buildSteps,omitempty"`
}

// GetTaskStatus combines ComponentTask + WorkflowRun.Status.Tasks[] for
// the build run (when present). No new persisted state.
func (c *taskController) GetTaskStatus(w http.ResponseWriter, r *http.Request) {
	taskID := r.PathValue("taskId")
	if !requireTaskID(w, taskID) {
		return
	}

	task, err := c.service.GetTask(r.Context(), taskID)
	if err != nil {
		if errors.Is(err, services.ErrTaskNotFound) {
			utils.WriteErrorResponse(w, http.StatusNotFound, "task not found")
			return
		}
		slog.ErrorContext(r.Context(), "get task status: load task failed", "error", err, "taskId", taskID)
		utils.WriteErrorResponse(w, http.StatusInternalServerError, "failed to get task")
		return
	}

	resp := TaskStatusResponse{Task: task}
	// Only fetch build steps while the task is actively building. Once the
	// run is terminal the steps are frozen — fetching on every poll for a
	// `deployed`/`failed` task is wasted OC calls.
	if task.Status == string(models.TaskStatusBuilding) && task.LastBuildRunName != "" && c.ocClient != nil {
		run, err := c.ocClient.GetWorkflowRun(r.Context(), task.OrgID, task.LastBuildRunName)
		if err != nil {
			slog.WarnContext(r.Context(), "get task status: load build run failed",
				"error", err, "run", task.LastBuildRunName)
		} else if run != nil {
			resp.BuildSteps = run.Tasks
		}
	}
	utils.WriteSuccessResponse(w, http.StatusOK, resp)
}

// GetTaskAgentProgress returns coding-agent NDJSON lines pulled from
// Observer for the per-task WorkflowRun's pod stdout. Cursor-driven —
// pass ?sinceMillis=N (0 ⇒ initial load anchored to task.DispatchedAt).
func (c *taskController) GetTaskAgentProgress(w http.ResponseWriter, r *http.Request) {
	taskID := r.PathValue("taskId")
	if !requireTaskID(w, taskID) {
		return
	}
	if c.progressSvc == nil {
		utils.WriteErrorResponse(w, http.StatusServiceUnavailable, "progress_unavailable")
		return
	}
	sinceMillis, _ := strconv.ParseInt(r.URL.Query().Get("sinceMillis"), 10, 64)
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))

	resp, err := c.progressSvc.GetAgentProgress(r.Context(), taskID, sinceMillis, limit)
	if err != nil {
		writeProgressError(w, r, err, "get agent progress")
		return
	}
	utils.WriteSuccessResponse(w, http.StatusOK, resp)
}

// GetTaskBuildProgress returns synthetic build_step lines derived from
// the build WorkflowRun's per-step Phase/Message/timestamps. Cursor
// driven — same shape as /progress/agent.
func (c *taskController) GetTaskBuildProgress(w http.ResponseWriter, r *http.Request) {
	taskID := r.PathValue("taskId")
	if !requireTaskID(w, taskID) {
		return
	}
	if c.progressSvc == nil {
		utils.WriteErrorResponse(w, http.StatusServiceUnavailable, "progress_unavailable")
		return
	}
	sinceMillis, _ := strconv.ParseInt(r.URL.Query().Get("sinceMillis"), 10, 64)

	resp, err := c.progressSvc.GetBuildProgress(r.Context(), taskID, sinceMillis)
	if err != nil {
		writeProgressError(w, r, err, "get build progress")
		return
	}
	utils.WriteSuccessResponse(w, http.StatusOK, resp)
}

func writeProgressError(w http.ResponseWriter, r *http.Request, err error, op string) {
	if errors.Is(err, services.ErrTaskNotFound) {
		utils.WriteErrorResponse(w, http.StatusNotFound, "task not found")
		return
	}
	if errors.Is(err, services.ErrProgressUnavailable) {
		utils.WriteErrorResponse(w, http.StatusServiceUnavailable, "progress_unavailable")
		return
	}
	slog.ErrorContext(r.Context(), op+" failed", "error", err)
	utils.WriteErrorResponse(w, http.StatusInternalServerError, op+" failed")
}

func (c *taskController) ExecTask(w http.ResponseWriter, r *http.Request) {
	taskID := r.PathValue("taskId")
	if !requireTaskID(w, taskID) {
		return
	}

	if err := c.service.ExecTask(r.Context(), taskID); err != nil {
		if errors.Is(err, services.ErrTaskNotFound) {
			utils.WriteErrorResponse(w, http.StatusNotFound, "task not found")
			return
		}
		slog.ErrorContext(r.Context(), "exec task failed", "error", err, "taskId", taskID)
		utils.WriteErrorResponse(w, http.StatusInternalServerError, "failed to execute task")
		return
	}

	utils.WriteSuccessResponse(w, http.StatusOK, map[string]string{"status": "task execution started"})
}
