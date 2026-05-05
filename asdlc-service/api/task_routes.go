package api

import (
	"net/http"

	"github.com/wso2/asdlc/asdlc-service/controllers"
)

func registerTaskRoutes(mux *http.ServeMux, c controllers.TaskController) {
	// Org-scoped tasks list (Phase 2 PR D — used by ReachReconciliationBanner).
	// Supports ?status=, ?cause=, ?since= filters.
	mux.HandleFunc("GET /api/v1/organizations/{orgHandle}/tasks", c.ListOrgTasks)
	mux.HandleFunc("GET /api/v1/organizations/{orgHandle}/projects/{projectName}/tasks", c.ListTasks)
	mux.HandleFunc("GET /api/v1/organizations/{orgHandle}/projects/{projectName}/tasks/generated", c.GetTasks)
	mux.HandleFunc("GET /api/v1/organizations/{orgHandle}/projects/{projectName}/tasks/{taskId}", c.GetTask)
	mux.HandleFunc("POST /api/v1/organizations/{orgHandle}/projects/{projectName}/tasks/dispatch", c.DispatchTasks)
	mux.HandleFunc("POST /api/v1/organizations/{orgHandle}/projects/{projectName}/tasks/generate", c.GenerateTasks)
	mux.HandleFunc("POST /api/v1/organizations/{orgHandle}/projects/{projectName}/tasks/{taskId}/exec", c.ExecTask)
	mux.HandleFunc("POST /api/v1/organizations/{orgHandle}/projects/{projectName}/tasks/{taskId}/regenerate-body", c.RegenerateTaskBody)
}
