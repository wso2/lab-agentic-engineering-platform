package controllers

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"

	"github.com/wso2/asdlc/git-service/services"
	"github.com/wso2/asdlc/git-service/utils"
)

// BranchController handles HTTP requests for git branch operations.
type BranchController interface {
	CreateBranch(w http.ResponseWriter, r *http.Request)
	SeedCommit(w http.ResponseWriter, r *http.Request)
}

type branchController struct {
	service services.BranchService
}

func NewBranchController(service services.BranchService) BranchController {
	return &branchController{service: service}
}

type createBranchRequest struct {
	Branch  string `json:"branch"`
	FromRef string `json:"fromRef,omitempty"`
}

type createBranchResponse struct {
	Branch string `json:"branch"`
	SHA    string `json:"sha"`
}

func (c *branchController) CreateBranch(w http.ResponseWriter, r *http.Request) {
	projectID := r.PathValue("projectId")
	if !requireProjectIDSlug(w, projectID) {
		return
	}

	var req createBranchRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		utils.WriteErrorResponse(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.Branch == "" {
		utils.WriteErrorResponse(w, http.StatusBadRequest, "branch is required")
		return
	}

	sha, err := c.service.CreateBranch(r.Context(), projectID, req.Branch, req.FromRef)
	if err != nil {
		if errors.Is(err, services.ErrRepoNotFound) {
			utils.WriteErrorResponse(w, http.StatusNotFound, "repository not found")
			return
		}
		slog.ErrorContext(r.Context(), "create branch failed", "project", projectID, "branch", req.Branch, "error", err)
		utils.WriteErrorResponse(w, http.StatusInternalServerError, "failed to create branch")
		return
	}

	utils.WriteSuccessResponse(w, http.StatusCreated, createBranchResponse{Branch: req.Branch, SHA: sha})
}

type seedCommitRequest struct {
	Branch  string `json:"branch"`
	Path    string `json:"path"`
	Message string `json:"message"`
	Content string `json:"content"` // raw bytes; sent as a UTF-8 string
}

func (c *branchController) SeedCommit(w http.ResponseWriter, r *http.Request) {
	projectID := r.PathValue("projectId")
	if !requireProjectIDSlug(w, projectID) {
		return
	}

	var req seedCommitRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		utils.WriteErrorResponse(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.Branch == "" || req.Path == "" {
		utils.WriteErrorResponse(w, http.StatusBadRequest, "branch and path are required")
		return
	}
	message := req.Message
	if message == "" {
		message = "chore: seed " + req.Path
	}

	if err := c.service.SeedBranchCommit(r.Context(), projectID, req.Branch, req.Path, message, []byte(req.Content)); err != nil {
		if errors.Is(err, services.ErrRepoNotFound) {
			utils.WriteErrorResponse(w, http.StatusNotFound, "repository not found")
			return
		}
		slog.ErrorContext(r.Context(), "seed branch commit failed",
			"project", projectID, "branch", req.Branch, "path", req.Path, "error", err)
		utils.WriteErrorResponse(w, http.StatusInternalServerError, "failed to seed commit")
		return
	}

	utils.WriteSuccessResponse(w, http.StatusOK, map[string]string{"branch": req.Branch, "path": req.Path})
}
