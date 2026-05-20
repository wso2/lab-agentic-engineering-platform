package controllers

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"

	"github.com/wso2/asdlc/git-service/internal/validate"
	"github.com/wso2/asdlc/git-service/services"
	"github.com/wso2/asdlc/git-service/utils"
)

// requireProjectIDSlug validates the {projectId} path param and writes a 400
// to w if invalid. Returns true if validation passed and the caller can
// continue. projectID flows into the on-disk clone path (REPO_BASE_PATH /
// orgId / projectId) so a malformed value would let a request escape the
// per-org subtree before any service call.
func requireProjectIDSlug(w http.ResponseWriter, projectID string) bool {
	if err := validate.Slug(projectID); err != nil {
		utils.WriteErrorResponse(w, http.StatusBadRequest, "projectId: "+err.Error())
		return false
	}
	return true
}

// GitOpsController handles HTTP requests for git operations.
type GitOpsController interface {
	Commit(w http.ResponseWriter, r *http.Request)
	Push(w http.ResponseWriter, r *http.Request)
	Pull(w http.ResponseWriter, r *http.Request)
	Status(w http.ResponseWriter, r *http.Request)
	CreateTag(w http.ResponseWriter, r *http.Request)
	ListTags(w http.ResponseWriter, r *http.Request)
	GetFileAtTag(w http.ResponseWriter, r *http.Request)
}

type gitOpsController struct {
	service services.GitOpsService
}

func NewGitOpsController(service services.GitOpsService) GitOpsController {
	return &gitOpsController{service: service}
}

func (c *gitOpsController) Commit(w http.ResponseWriter, r *http.Request) {
	projectID := r.PathValue("projectId")
	if !requireProjectIDSlug(w, projectID) {
		return
	}

	var req services.CommitRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		utils.WriteErrorResponse(w, http.StatusBadRequest, "invalid request body")
		return
	}

	if req.Message == "" {
		utils.WriteErrorResponse(w, http.StatusBadRequest, "message is required")
		return
	}

	result, err := c.service.Commit(r.Context(), projectID, req)
	if err != nil {
		handleGitOpsError(w, r, err, "commit")
		return
	}

	utils.WriteSuccessResponse(w, http.StatusOK, result)
}

type pushRequest struct {
	Branch string `json:"branch"`
}

func (c *gitOpsController) Push(w http.ResponseWriter, r *http.Request) {
	projectID := r.PathValue("projectId")
	if !requireProjectIDSlug(w, projectID) {
		return
	}

	var req pushRequest
	json.NewDecoder(r.Body).Decode(&req) //nolint:errcheck — branch is optional

	if err := c.service.Push(r.Context(), projectID, req.Branch); err != nil {
		handleGitOpsError(w, r, err, "push")
		return
	}

	utils.WriteSuccessResponse(w, http.StatusOK, map[string]string{"status": "pushed"})
}

type pullRequest struct {
	Branch string `json:"branch"`
}

func (c *gitOpsController) Pull(w http.ResponseWriter, r *http.Request) {
	projectID := r.PathValue("projectId")
	if !requireProjectIDSlug(w, projectID) {
		return
	}

	var req pullRequest
	json.NewDecoder(r.Body).Decode(&req) //nolint:errcheck — branch is optional

	if err := c.service.Pull(r.Context(), projectID, req.Branch); err != nil {
		handleGitOpsError(w, r, err, "pull")
		return
	}

	utils.WriteSuccessResponse(w, http.StatusOK, map[string]string{"status": "pulled"})
}

func (c *gitOpsController) Status(w http.ResponseWriter, r *http.Request) {
	projectID := r.PathValue("projectId")
	if !requireProjectIDSlug(w, projectID) {
		return
	}

	status, err := c.service.Status(r.Context(), projectID)
	if err != nil {
		handleGitOpsError(w, r, err, "status")
		return
	}

	utils.WriteSuccessResponse(w, http.StatusOK, status)
}

func handleGitOpsError(w http.ResponseWriter, r *http.Request, err error, op string) {
	if errors.Is(err, services.ErrRepoNotFound) {
		utils.WriteErrorResponse(w, http.StatusNotFound, "repository not found")
		return
	}
	if errors.Is(err, services.ErrRepoNotReady) {
		utils.WriteErrorResponse(w, http.StatusUnprocessableEntity, "repository is not ready")
		return
	}
	if errors.Is(err, services.ErrAuthFailed) {
		utils.WriteErrorResponse(w, http.StatusUnauthorized, "git authentication failed")
		return
	}
	if errors.Is(err, services.ErrPushConflict) {
		utils.WriteErrorResponse(w, http.StatusConflict, err.Error())
		return
	}
	if errors.Is(err, services.ErrTagAlreadyExists) {
		utils.WriteErrorResponse(w, http.StatusConflict, err.Error())
		return
	}
	if errors.Is(err, services.ErrTagNotFound) {
		utils.WriteErrorResponse(w, http.StatusNotFound, err.Error())
		return
	}
	if errors.Is(err, services.ErrFileNotFound) {
		utils.WriteErrorResponse(w, http.StatusNotFound, err.Error())
		return
	}
	slog.ErrorContext(r.Context(), "git "+op+" failed", "error", err)
	utils.WriteErrorResponse(w, http.StatusInternalServerError, "git "+op+" failed")
}

func (c *gitOpsController) CreateTag(w http.ResponseWriter, r *http.Request) {
	projectID := r.PathValue("projectId")
	if !requireProjectIDSlug(w, projectID) {
		return
	}

	var req services.CreateTagRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		utils.WriteErrorResponse(w, http.StatusBadRequest, "invalid request body")
		return
	}

	if req.TagName == "" {
		utils.WriteErrorResponse(w, http.StatusBadRequest, "tagName is required")
		return
	}
	if req.Message == "" {
		req.Message = req.TagName
	}

	result, err := c.service.CreateTag(r.Context(), projectID, req)
	if err != nil {
		handleGitOpsError(w, r, err, "create tag")
		return
	}

	utils.WriteSuccessResponse(w, http.StatusCreated, result)
}

func (c *gitOpsController) ListTags(w http.ResponseWriter, r *http.Request) {
	projectID := r.PathValue("projectId")
	if !requireProjectIDSlug(w, projectID) {
		return
	}
	prefix := r.URL.Query().Get("prefix")

	tags, err := c.service.ListTags(r.Context(), projectID, prefix)
	if err != nil {
		handleGitOpsError(w, r, err, "list tags")
		return
	}

	utils.WriteSuccessResponse(w, http.StatusOK, tags)
}

func (c *gitOpsController) GetFileAtTag(w http.ResponseWriter, r *http.Request) {
	projectID := r.PathValue("projectId")
	if !requireProjectIDSlug(w, projectID) {
		return
	}
	tag := r.PathValue("tag")
	filePath := r.URL.Query().Get("path")

	if filePath == "" {
		utils.WriteErrorResponse(w, http.StatusBadRequest, "path query parameter is required")
		return
	}

	content, err := c.service.GetFileAtTag(r.Context(), projectID, tag, filePath)
	if err != nil {
		handleGitOpsError(w, r, err, "get file at tag")
		return
	}

	utils.WriteSuccessResponse(w, http.StatusOK, map[string]string{"content": content})
}
