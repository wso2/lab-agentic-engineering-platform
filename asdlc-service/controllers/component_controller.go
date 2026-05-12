package controllers

import (
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"

	"github.com/wso2/asdlc/asdlc-service/services"
	"github.com/wso2/asdlc/asdlc-service/utils"
)

// ComponentController handles HTTP requests for component operations.
type ComponentController interface {
	ListComponents(w http.ResponseWriter, r *http.Request)
	GetComponent(w http.ResponseWriter, r *http.Request)
	TriggerBuild(w http.ResponseWriter, r *http.Request)
	ListBuilds(w http.ResponseWriter, r *http.Request)
	GetBuildStatus(w http.ResponseWriter, r *http.Request)
	GetBuildLogs(w http.ResponseWriter, r *http.Request)
	ListDeployments(w http.ResponseWriter, r *http.Request)
	GetComponentOpenAPI(w http.ResponseWriter, r *http.Request)
	TestProxy(w http.ResponseWriter, r *http.Request)
}

type componentController struct {
	service     services.ComponentService
	taskService services.TaskService
}

func NewComponentController(service services.ComponentService, taskService services.TaskService) ComponentController {
	return &componentController{service: service, taskService: taskService}
}

func (c *componentController) ListComponents(w http.ResponseWriter, r *http.Request) {
	org := r.PathValue("orgHandle")
	projectName := r.PathValue("projectName")
	if !requireOrgHandle(w, org) || !requireProjectName(w, projectName) {
		return
	}

	list, err := c.service.ListComponents(r.Context(), org, projectName, 100, "")
	if err != nil {
		if errors.Is(err, services.ErrUnauthorized) {
			utils.WriteErrorResponse(w, http.StatusUnauthorized, "invalid or expired token")
			return
		}
		slog.ErrorContext(r.Context(), "list components failed", "error", err, "org", org, "project", projectName)
		utils.WriteErrorResponse(w, http.StatusInternalServerError, "failed to list components")
		return
	}

	utils.WriteSuccessResponse(w, http.StatusOK, list)
}

func (c *componentController) GetComponent(w http.ResponseWriter, r *http.Request) {
	org := r.PathValue("orgHandle")
	projectName := r.PathValue("projectName")
	componentName := r.PathValue("componentName")
	if !requireOrgHandle(w, org) || !requireProjectName(w, projectName) || !requireComponentName(w, componentName) {
		return
	}

	comp, err := c.service.GetComponent(r.Context(), org, projectName, componentName)
	if err != nil {
		if errors.Is(err, services.ErrComponentNotFound) {
			utils.WriteErrorResponse(w, http.StatusNotFound, "component not found")
			return
		}
		if errors.Is(err, services.ErrUnauthorized) {
			utils.WriteErrorResponse(w, http.StatusUnauthorized, "invalid or expired token")
			return
		}
		slog.ErrorContext(r.Context(), "get component failed", "error", err, "org", org, "component", componentName)
		utils.WriteErrorResponse(w, http.StatusInternalServerError, "failed to get component")
		return
	}

	utils.WriteSuccessResponse(w, http.StatusOK, comp)
}

func (c *componentController) TriggerBuild(w http.ResponseWriter, r *http.Request) {
	org := r.PathValue("orgHandle")
	projectName := r.PathValue("projectName")
	componentName := r.PathValue("componentName")
	if !requireOrgHandle(w, org) || !requireProjectName(w, projectName) || !requireComponentName(w, componentName) {
		return
	}

	run, err := c.service.TriggerBuild(r.Context(), org, projectName, componentName)
	if err != nil {
		if errors.Is(err, services.ErrUnauthorized) {
			utils.WriteErrorResponse(w, http.StatusUnauthorized, "invalid or expired token")
			return
		}
		slog.ErrorContext(r.Context(), "trigger build failed", "error", err, "org", org, "component", componentName)
		utils.WriteErrorResponse(w, http.StatusInternalServerError, "failed to trigger build")
		return
	}

	utils.WriteSuccessResponse(w, http.StatusCreated, run)
}

func (c *componentController) ListBuilds(w http.ResponseWriter, r *http.Request) {
	org := r.PathValue("orgHandle")
	projectName := r.PathValue("projectName")
	componentName := r.PathValue("componentName")
	if !requireOrgHandle(w, org) || !requireProjectName(w, projectName) || !requireComponentName(w, componentName) {
		return
	}

	list, err := c.service.ListBuilds(r.Context(), org, projectName, componentName, 20, "")
	if err != nil {
		if errors.Is(err, services.ErrUnauthorized) {
			utils.WriteErrorResponse(w, http.StatusUnauthorized, "invalid or expired token")
			return
		}
		slog.ErrorContext(r.Context(), "list builds failed", "error", err, "org", org, "component", componentName)
		utils.WriteErrorResponse(w, http.StatusInternalServerError, "failed to list builds")
		return
	}

	utils.WriteSuccessResponse(w, http.StatusOK, list)
}

func (c *componentController) GetBuildStatus(w http.ResponseWriter, r *http.Request) {
	org := r.PathValue("orgHandle")
	buildName := r.PathValue("buildName")
	if !requireOrgHandle(w, org) {
		return
	}
	// buildName is a k8s WorkflowRun name (lowercase DNS-label-shaped slug).
	if err := validateSlugParam(w, "buildName", buildName); err != nil {
		return
	}

	run, err := c.service.GetBuildStatus(r.Context(), org, buildName)
	if err != nil {
		if errors.Is(err, services.ErrComponentNotFound) {
			utils.WriteErrorResponse(w, http.StatusNotFound, "build not found")
			return
		}
		if errors.Is(err, services.ErrUnauthorized) {
			utils.WriteErrorResponse(w, http.StatusUnauthorized, "invalid or expired token")
			return
		}
		slog.ErrorContext(r.Context(), "get build status failed", "error", err, "org", org, "build", buildName)
		utils.WriteErrorResponse(w, http.StatusInternalServerError, "failed to get build status")
		return
	}

	utils.WriteSuccessResponse(w, http.StatusOK, run)
}

func (c *componentController) GetBuildLogs(w http.ResponseWriter, r *http.Request) {
	org := r.PathValue("orgHandle")
	projectName := r.PathValue("projectName")
	componentName := r.PathValue("componentName")
	buildName := r.PathValue("buildName")
	if !requireOrgHandle(w, org) || !requireProjectName(w, projectName) || !requireComponentName(w, componentName) {
		return
	}
	if err := validateSlugParam(w, "buildName", buildName); err != nil {
		return
	}

	logs, err := c.service.GetBuildLogs(r.Context(), org, projectName, componentName, buildName)
	if err != nil {
		if errors.Is(err, services.ErrUnauthorized) {
			utils.WriteErrorResponse(w, http.StatusUnauthorized, "invalid or expired token")
			return
		}
		if errors.Is(err, services.ErrLogsUnavailable) {
			utils.WriteErrorResponse(w, http.StatusServiceUnavailable, "build logs service not available")
			return
		}
		slog.ErrorContext(r.Context(), "get build logs failed",
			"error", err, "org", org, "project", projectName, "component", componentName, "build", buildName)
		utils.WriteErrorResponse(w, http.StatusInternalServerError, "failed to get build logs")
		return
	}

	utils.WriteSuccessResponse(w, http.StatusOK, logs)
}

// GetComponentOpenAPI returns the OpenAPI spec for a service component
// from the project's .asdlc/design.json. Status codes:
//   - 200 → {componentName, componentType, spec}
//   - 404 → design.json missing OR no component matches the slug
//   - 409 → component exists but isn't a "service" (body carries componentType
//     so the UI can render a precise empty state)
func (c *componentController) GetComponentOpenAPI(w http.ResponseWriter, r *http.Request) {
	org := r.PathValue("orgHandle")
	projectName := r.PathValue("projectName")
	componentName := r.PathValue("componentName")
	if !requireOrgHandle(w, org) || !requireProjectName(w, projectName) || !requireComponentName(w, componentName) {
		return
	}

	spec, err := c.service.GetComponentOpenAPI(r.Context(), org, projectName, componentName)
	if err != nil {
		if errors.Is(err, services.ErrComponentNotFound) {
			utils.WriteErrorResponse(w, http.StatusNotFound, "no OpenAPI spec for this component")
			return
		}
		if errors.Is(err, services.ErrComponentNotService) {
			// Hand the type back so the client can say "this is a web-app, not a service".
			utils.WriteSuccessResponse(w, http.StatusConflict, spec)
			return
		}
		if errors.Is(err, services.ErrUnauthorized) {
			utils.WriteErrorResponse(w, http.StatusUnauthorized, "invalid or expired token")
			return
		}
		slog.ErrorContext(r.Context(), "get component openapi failed", "error", err, "org", org, "component", componentName)
		utils.WriteErrorResponse(w, http.StatusInternalServerError, "failed to get OpenAPI spec")
		return
	}

	utils.WriteSuccessResponse(w, http.StatusOK, spec)
}

// TestProxy forwards an HTTP request from the Test tab to the component's
// deployed endpoint, side-stepping browser CORS. The target URL is carried
// in the X-Asdlc-Target-Url request header; the HTTP method, body, and
// other safe headers are forwarded verbatim. The upstream response is
// streamed back unchanged so swagger-ui treats it like any other fetch.
func (c *componentController) TestProxy(w http.ResponseWriter, r *http.Request) {
	org := r.PathValue("orgHandle")
	projectName := r.PathValue("projectName")
	componentName := r.PathValue("componentName")
	if !requireOrgHandle(w, org) || !requireProjectName(w, projectName) || !requireComponentName(w, componentName) {
		return
	}

	target := r.Header.Get("X-Asdlc-Target-Url")
	method := r.Header.Get("X-Asdlc-Target-Method")
	if method == "" {
		method = http.MethodGet
	}

	// Strip headers we control + Authorization (the BFF JWT). Everything else
	// the user set in the Test UI flows through to the target.
	upstream := r.Header.Clone()
	for k := range upstream {
		lk := strings.ToLower(k)
		if lk == "authorization" || strings.HasPrefix(lk, "x-asdlc-") || lk == "host" || lk == "cookie" {
			upstream.Del(k)
		}
	}

	resp, err := c.service.ProxyTestRequest(r.Context(), org, projectName, componentName, target, method, upstream, r.Body)
	if err != nil {
		if errors.Is(err, services.ErrInvalidTestTarget) {
			utils.WriteErrorResponse(w, http.StatusBadRequest, "target URL does not match a deployment endpoint for this component")
			return
		}
		if errors.Is(err, services.ErrComponentNotFound) {
			utils.WriteErrorResponse(w, http.StatusNotFound, "component not found")
			return
		}
		if errors.Is(err, services.ErrUnauthorized) {
			utils.WriteErrorResponse(w, http.StatusUnauthorized, "invalid or expired token")
			return
		}
		slog.ErrorContext(r.Context(), "test-proxy failed", "error", err, "org", org, "component", componentName, "target", target)
		utils.WriteErrorResponse(w, http.StatusBadGateway, "test-proxy upstream failure")
		return
	}
	defer resp.Body.Close()

	for k, vv := range resp.Header {
		lk := strings.ToLower(k)
		if lk == "transfer-encoding" || lk == "content-length" || lk == "connection" {
			continue
		}
		for _, v := range vv {
			w.Header().Add(k, v)
		}
	}
	w.WriteHeader(resp.StatusCode)
	_, _ = io.Copy(w, resp.Body)
}

func (c *componentController) ListDeployments(w http.ResponseWriter, r *http.Request) {
	org := r.PathValue("orgHandle")
	projectName := r.PathValue("projectName")
	componentName := r.PathValue("componentName")
	if !requireOrgHandle(w, org) || !requireProjectName(w, projectName) || !requireComponentName(w, componentName) {
		return
	}

	list, err := c.service.ListDeployments(r.Context(), org, projectName, componentName)
	if err != nil {
		if errors.Is(err, services.ErrUnauthorized) {
			utils.WriteErrorResponse(w, http.StatusUnauthorized, "invalid or expired token")
			return
		}
		slog.ErrorContext(r.Context(), "list deployments failed", "error", err, "org", org, "component", componentName)
		utils.WriteErrorResponse(w, http.StatusInternalServerError, "failed to list deployments")
		return
	}

	utils.WriteSuccessResponse(w, http.StatusOK, list)
}
