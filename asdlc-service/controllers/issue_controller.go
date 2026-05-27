package controllers

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"strings"

	"github.com/wso2/asdlc/asdlc-service/services"
	"github.com/wso2/asdlc/asdlc-service/utils"
)

// IssueController handles HTTP requests for GitHub issue operations.
type IssueController interface {
	CreateIssue(w http.ResponseWriter, r *http.Request)
	ListIssues(w http.ResponseWriter, r *http.Request)
	CloseIssue(w http.ResponseWriter, r *http.Request)
	CommentIssue(w http.ResponseWriter, r *http.Request)
	EditIssueBody(w http.ResponseWriter, r *http.Request)
}

type issueController struct {
	service services.IssueService
}

func NewIssueController(service services.IssueService) IssueController {
	return &issueController{service: service}
}

func (c *issueController) CreateIssue(w http.ResponseWriter, r *http.Request) {
	projectID := r.PathValue("projectId")
	if !requireProjectIDSlug(w, projectID) {
		return
	}

	var req services.CreateIssueRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		utils.WriteErrorResponse(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.Title == "" {
		utils.WriteErrorResponse(w, http.StatusBadRequest, "title is required")
		return
	}

	result, err := c.service.CreateIssue(r.Context(), projectID, req)
	if err != nil {
		if errors.Is(err, services.ErrRepoNotFound) {
			utils.WriteErrorResponse(w, http.StatusNotFound, "repository not found")
			return
		}
		slog.ErrorContext(r.Context(), "create issue failed", "project", projectID, "error", err)
		utils.WriteErrorResponse(w, http.StatusInternalServerError, "failed to create issue")
		return
	}

	utils.WriteSuccessResponse(w, http.StatusCreated, result)
}

func (c *issueController) ListIssues(w http.ResponseWriter, r *http.Request) {
	projectID := r.PathValue("projectId")
	if !requireProjectIDSlug(w, projectID) {
		return
	}

	// Optional comma-separated labels filter via query param: ?labels=asdlc,implementation
	var labels []string
	if raw := r.URL.Query().Get("labels"); raw != "" {
		for _, l := range strings.Split(raw, ",") {
			if l = strings.TrimSpace(l); l != "" {
				labels = append(labels, l)
			}
		}
	}

	issues, err := c.service.ListIssues(r.Context(), projectID, labels)
	if err != nil {
		if errors.Is(err, services.ErrRepoNotFound) {
			utils.WriteErrorResponse(w, http.StatusNotFound, "repository not found")
			return
		}
		slog.ErrorContext(r.Context(), "list issues failed", "project", projectID, "error", err)
		utils.WriteErrorResponse(w, http.StatusInternalServerError, "failed to list issues")
		return
	}

	utils.WriteSuccessResponse(w, http.StatusOK, issues)
}

type closeIssueRequest struct {
	Comment string `json:"comment,omitempty"`
}

func (c *issueController) CloseIssue(w http.ResponseWriter, r *http.Request) {
	projectID := r.PathValue("projectId")
	if !requireProjectIDSlug(w, projectID) {
		return
	}
	number, err := strconv.Atoi(r.PathValue("number"))
	if err != nil || number <= 0 {
		utils.WriteErrorResponse(w, http.StatusBadRequest, "invalid issue number")
		return
	}

	var req closeIssueRequest
	// Body is optional — only decode if present.
	if r.ContentLength > 0 {
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			utils.WriteErrorResponse(w, http.StatusBadRequest, "invalid request body")
			return
		}
	}

	if err := c.service.CloseIssue(r.Context(), projectID, number, req.Comment); err != nil {
		if errors.Is(err, services.ErrRepoNotFound) {
			utils.WriteErrorResponse(w, http.StatusNotFound, "repository not found")
			return
		}
		slog.ErrorContext(r.Context(), "close issue failed", "project", projectID, "issue", number, "error", err)
		utils.WriteErrorResponse(w, http.StatusInternalServerError, "failed to close issue")
		return
	}

	utils.WriteSuccessResponse(w, http.StatusOK, map[string]any{"number": number, "state": "closed"})
}

type commentIssueRequest struct {
	Body string `json:"body"`
}

type editIssueBodyRequest struct {
	Body string `json:"body"`
}

func (c *issueController) EditIssueBody(w http.ResponseWriter, r *http.Request) {
	projectID := r.PathValue("projectId")
	if !requireProjectIDSlug(w, projectID) {
		return
	}
	number, err := strconv.Atoi(r.PathValue("number"))
	if err != nil || number <= 0 {
		utils.WriteErrorResponse(w, http.StatusBadRequest, "invalid issue number")
		return
	}
	var req editIssueBodyRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		utils.WriteErrorResponse(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if strings.TrimSpace(req.Body) == "" {
		utils.WriteErrorResponse(w, http.StatusBadRequest, "body is required")
		return
	}
	if err := c.service.EditIssueBody(r.Context(), projectID, number, req.Body); err != nil {
		if errors.Is(err, services.ErrRepoNotFound) {
			utils.WriteErrorResponse(w, http.StatusNotFound, "repository not found")
			return
		}
		slog.ErrorContext(r.Context(), "edit issue body failed", "project", projectID, "issue", number, "error", err)
		utils.WriteErrorResponse(w, http.StatusInternalServerError, "failed to edit issue body")
		return
	}
	utils.WriteSuccessResponse(w, http.StatusOK, map[string]any{"number": number})
}

func (c *issueController) CommentIssue(w http.ResponseWriter, r *http.Request) {
	projectID := r.PathValue("projectId")
	if !requireProjectIDSlug(w, projectID) {
		return
	}
	number, err := strconv.Atoi(r.PathValue("number"))
	if err != nil || number <= 0 {
		utils.WriteErrorResponse(w, http.StatusBadRequest, "invalid issue number")
		return
	}

	var req commentIssueRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		utils.WriteErrorResponse(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if strings.TrimSpace(req.Body) == "" {
		utils.WriteErrorResponse(w, http.StatusBadRequest, "body is required")
		return
	}

	if err := c.service.CommentIssue(r.Context(), projectID, number, req.Body); err != nil {
		if errors.Is(err, services.ErrRepoNotFound) {
			utils.WriteErrorResponse(w, http.StatusNotFound, "repository not found")
			return
		}
		slog.ErrorContext(r.Context(), "comment issue failed", "project", projectID, "issue", number, "error", err)
		utils.WriteErrorResponse(w, http.StatusInternalServerError, "failed to comment on issue")
		return
	}

	utils.WriteSuccessResponse(w, http.StatusCreated, map[string]any{"number": number})
}
