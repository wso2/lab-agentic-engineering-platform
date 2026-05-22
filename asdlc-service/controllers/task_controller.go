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

	// F3c — agent-driven verification failure + operator retry.
	// VerificationFailed authenticates the caller with the per-task JWT
	// minted at dispatch (verified locally by TaskTokenManager.Verify
	// against the BFF's own signing key). Retry is operator-only and
	// uses the standard auth middleware.
	VerificationFailed(w http.ResponseWriter, r *http.Request)
	Retry(w http.ResponseWriter, r *http.Request)

	// Database provisioning callbacks — called by the agent with its
	// per-task JWT. Same auth pattern as VerificationFailed.
	DbTesting(w http.ResponseWriter, r *http.Request)
	DbDeployed(w http.ResponseWriter, r *http.Request)
	DbFailed(w http.ResponseWriter, r *http.Request)

	// ListDatabaseArtifacts returns provisioned database metadata + health status
	// for all database component tasks in the project.
	ListDatabaseArtifacts(w http.ResponseWriter, r *http.Request)

	// Progress endpoints — task-execution-progress.md §5.2.
	GetTaskAgentProgress(w http.ResponseWriter, r *http.Request)
	GetTaskBuildProgress(w http.ResponseWriter, r *http.Request)
}

type taskController struct {
	service     services.TaskService
	dispatchSvc services.DispatchService
	progressSvc services.ProgressService
	ocClient    openchoreo.ComponentClient
	taskTokens  *services.TaskTokenManager
}

func NewTaskController(
	service services.TaskService,
	dispatchSvc services.DispatchService,
	progressSvc services.ProgressService,
	ocClient openchoreo.ComponentClient,
	taskTokens *services.TaskTokenManager,
) TaskController {
	return &taskController{
		service:     service,
		dispatchSvc: dispatchSvc,
		progressSvc: progressSvc,
		ocClient:    ocClient,
		taskTokens:  taskTokens,
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

// verificationFailedRequest is the per-task-bearer-authed JSON body for
// POST /api/v1/tasks/{taskId}/verification-failed. The diagnostic field
// is optional but strongly encouraged so the operator can see what the
// agent observed.
type verificationFailedRequest struct {
	Diagnostic string `json:"diagnostic"`
}

// VerificationFailed (F3c) is called by the dispatched agent inside the
// runner pod when it detects that a dependency endpoint is not behaving
// as the spec describes. Authenticated via the per-task JWT the runner
// already holds. The handler verifies the JWT, asserts the subject
// matches the URL's taskId, then drives in_progress → verification_failed.
func (c *taskController) VerificationFailed(w http.ResponseWriter, r *http.Request) {
	taskID := r.PathValue("taskId")
	if !requireTaskID(w, taskID) {
		return
	}
	if c.dispatchSvc == nil || c.taskTokens == nil {
		utils.WriteErrorResponse(w, http.StatusServiceUnavailable, "verification-failed not configured")
		return
	}

	authz := r.Header.Get("Authorization")
	const prefix = "Bearer "
	if len(authz) <= len(prefix) || authz[:len(prefix)] != prefix {
		utils.WriteErrorResponse(w, http.StatusUnauthorized, "bearer token required")
		return
	}
	claims, err := c.taskTokens.Verify(authz[len(prefix):])
	if err != nil {
		slog.WarnContext(r.Context(), "verification-failed: invalid bearer", "task", taskID, "error", err)
		utils.WriteErrorResponse(w, http.StatusUnauthorized, "invalid task bearer")
		return
	}
	if claims.TaskID != taskID {
		slog.WarnContext(r.Context(), "verification-failed: bearer subject mismatch",
			"task", taskID, "claimTaskId", claims.TaskID)
		utils.WriteErrorResponse(w, http.StatusForbidden, "task bearer does not match path")
		return
	}

	var req verificationFailedRequest
	if r.Body != nil {
		_ = json.NewDecoder(r.Body).Decode(&req) // diagnostic is optional
	}
	if err := c.dispatchSvc.MarkVerificationFailed(r.Context(), taskID, req.Diagnostic); err != nil {
		slog.ErrorContext(r.Context(), "verification-failed: apply transition failed", "task", taskID, "error", err)
		utils.WriteErrorResponse(w, http.StatusInternalServerError, "failed to apply verification_failed")
		return
	}
	utils.WriteSuccessResponse(w, http.StatusAccepted, map[string]string{"status": "verification_failed"})
}

// Retry (F3c) is the operator-driven retry path: transitions
// verification_failed → in_progress and re-dispatches with a fresh
// WorkflowRun + freshly minted per-task bearer. Standard user auth
// applies (mounted on the org/project-scoped task path).
func (c *taskController) Retry(w http.ResponseWriter, r *http.Request) {
	taskID := r.PathValue("taskId")
	if !requireTaskID(w, taskID) {
		return
	}
	if c.dispatchSvc == nil {
		utils.WriteErrorResponse(w, http.StatusServiceUnavailable, "dispatch service not configured")
		return
	}
	res, err := c.dispatchSvc.RetryTask(r.Context(), taskID)
	if err != nil {
		slog.ErrorContext(r.Context(), "retry: failed", "task", taskID, "error", err)
		utils.WriteErrorResponse(w, http.StatusInternalServerError, "failed to retry task")
		return
	}
	utils.WriteSuccessResponse(w, http.StatusOK, res)
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

func (c *taskController) ListDatabaseArtifacts(w http.ResponseWriter, r *http.Request) {
	org := r.PathValue("orgHandle")
	project := r.PathValue("projectName")
	if !requireOrgHandle(w, org) || !requireProjectName(w, project) {
		return
	}

	items, err := c.service.ListDatabaseArtifacts(r.Context(), org, project)
	if err != nil {
		slog.ErrorContext(r.Context(), "list database artifacts failed", "error", err, "org", org, "project", project)
		utils.WriteErrorResponse(w, http.StatusInternalServerError, "failed to list database artifacts")
		return
	}
	if items == nil {
		items = []services.DatabaseArtifactItem{}
	}

	type response struct {
		Databases []services.DatabaseArtifactItem `json:"databases"`
	}
	utils.WriteSuccessResponse(w, http.StatusOK, response{Databases: items})
}

// verifyTaskBearer is a shared helper that extracts and verifies the per-task
// bearer JWT, returning the task ID claim on success or writing an error
// response and returning "" on failure.
func (c *taskController) verifyTaskBearer(w http.ResponseWriter, r *http.Request, taskID string) string {
	authz := r.Header.Get("Authorization")
	const prefix = "Bearer "
	if len(authz) <= len(prefix) || authz[:len(prefix)] != prefix {
		utils.WriteErrorResponse(w, http.StatusUnauthorized, "bearer token required")
		return ""
	}
	claims, err := c.taskTokens.Verify(authz[len(prefix):])
	if err != nil {
		slog.WarnContext(r.Context(), "db callback: invalid bearer", "task", taskID, "error", err)
		utils.WriteErrorResponse(w, http.StatusUnauthorized, "invalid task bearer")
		return ""
	}
	if claims.TaskID != taskID {
		slog.WarnContext(r.Context(), "db callback: bearer subject mismatch",
			"task", taskID, "claimTaskId", claims.TaskID)
		utils.WriteErrorResponse(w, http.StatusForbidden, "task bearer does not match path")
		return ""
	}
	return claims.TaskID
}

func (c *taskController) DbTesting(w http.ResponseWriter, r *http.Request) {
	taskID := r.PathValue("taskId")
	if !requireTaskID(w, taskID) {
		return
	}
	if c.dispatchSvc == nil || c.taskTokens == nil {
		utils.WriteErrorResponse(w, http.StatusServiceUnavailable, "db callbacks not configured")
		return
	}
	if c.verifyTaskBearer(w, r, taskID) == "" {
		return
	}
	if err := c.dispatchSvc.MarkDbTesting(r.Context(), taskID); err != nil {
		slog.ErrorContext(r.Context(), "db-testing: apply transition failed", "task", taskID, "error", err)
		utils.WriteErrorResponse(w, http.StatusInternalServerError, "failed to apply db_testing")
		return
	}
	utils.WriteSuccessResponse(w, http.StatusAccepted, map[string]string{"status": "testing"})
}

func (c *taskController) DbDeployed(w http.ResponseWriter, r *http.Request) {
	taskID := r.PathValue("taskId")
	if !requireTaskID(w, taskID) {
		return
	}
	if c.dispatchSvc == nil || c.taskTokens == nil {
		utils.WriteErrorResponse(w, http.StatusServiceUnavailable, "db callbacks not configured")
		return
	}
	if c.verifyTaskBearer(w, r, taskID) == "" {
		return
	}
	if err := c.dispatchSvc.MarkDbDeployed(r.Context(), taskID); err != nil {
		slog.ErrorContext(r.Context(), "db-deployed: apply transition failed", "task", taskID, "error", err)
		utils.WriteErrorResponse(w, http.StatusInternalServerError, "failed to apply db_deployed")
		return
	}
	utils.WriteSuccessResponse(w, http.StatusAccepted, map[string]string{"status": "deployed"})
}

type dbFailedRequest struct {
	Diagnostic string `json:"diagnostic"`
}

func (c *taskController) DbFailed(w http.ResponseWriter, r *http.Request) {
	taskID := r.PathValue("taskId")
	if !requireTaskID(w, taskID) {
		return
	}
	if c.dispatchSvc == nil || c.taskTokens == nil {
		utils.WriteErrorResponse(w, http.StatusServiceUnavailable, "db callbacks not configured")
		return
	}
	if c.verifyTaskBearer(w, r, taskID) == "" {
		return
	}
	var req dbFailedRequest
	if r.Body != nil {
		_ = json.NewDecoder(r.Body).Decode(&req)
	}
	if err := c.dispatchSvc.MarkDbFailed(r.Context(), taskID, req.Diagnostic); err != nil {
		slog.ErrorContext(r.Context(), "db-failed: apply transition failed", "task", taskID, "error", err)
		utils.WriteErrorResponse(w, http.StatusInternalServerError, "failed to apply db_failed")
		return
	}
	utils.WriteSuccessResponse(w, http.StatusAccepted, map[string]string{"status": "failed"})
}
