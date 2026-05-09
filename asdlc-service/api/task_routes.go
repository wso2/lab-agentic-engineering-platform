package api

import (
	"net/http"

	"github.com/wso2/asdlc/asdlc-service/controllers"
	"github.com/wso2/asdlc/asdlc-service/middleware"
)

func registerTaskRoutes(mux *http.ServeMux, c controllers.TaskController) {
	// Org-scoped tasks list (Phase 2 PR D — used by ReachReconciliationBanner).
	// Supports ?status=, ?cause=, ?since= filters.
	mux.HandleFunc("GET /api/v1/organizations/{orgHandle}/tasks", c.ListOrgTasks)
	mux.HandleFunc("GET /api/v1/organizations/{orgHandle}/projects/{projectName}/tasks", c.ListTasks)
	mux.HandleFunc("GET /api/v1/organizations/{orgHandle}/projects/{projectName}/tasks/generated", c.GetTasks)
	mux.HandleFunc("GET /api/v1/organizations/{orgHandle}/projects/{projectName}/tasks/{taskId}", c.GetTask)
	mux.HandleFunc("GET /api/v1/organizations/{orgHandle}/projects/{projectName}/tasks/{taskId}/status", c.GetTaskStatus)
	mux.HandleFunc("POST /api/v1/organizations/{orgHandle}/projects/{projectName}/tasks/dispatch", c.DispatchTasks)
	mux.HandleFunc("POST /api/v1/organizations/{orgHandle}/projects/{projectName}/tasks/generate", c.GenerateTasks)
	mux.HandleFunc("POST /api/v1/organizations/{orgHandle}/projects/{projectName}/tasks/{taskId}/exec", c.ExecTask)
	mux.HandleFunc("POST /api/v1/organizations/{orgHandle}/projects/{projectName}/tasks/{taskId}/regenerate-body", c.RegenerateTaskBody)

	// Progress endpoints — task-execution-progress.md §5.2. Per-org rate
	// limited (token bucket, 100 req/s burst 200) so a single tenant can't
	// starve Observer for others.
	progressLimiter := middleware.ProgressRateLimit(100, 200)
	mux.Handle("GET /api/v1/organizations/{orgHandle}/projects/{projectName}/tasks/{taskId}/progress/agent",
		progressLimiter(http.HandlerFunc(c.GetTaskAgentProgress)))
	mux.Handle("GET /api/v1/organizations/{orgHandle}/projects/{projectName}/tasks/{taskId}/progress/build",
		progressLimiter(http.HandlerFunc(c.GetTaskBuildProgress)))
}
