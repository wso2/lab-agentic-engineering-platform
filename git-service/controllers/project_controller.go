package controllers

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"

	"github.com/wso2/asdlc/git-service/services"
	"github.com/wso2/asdlc/git-service/utils"
)

// ProjectController handles HTTP requests for GitHub project operations.
type ProjectController interface {
	InitProject(w http.ResponseWriter, r *http.Request)
}

type projectController struct {
	client      services.GitHubV2Client
	pat         string
	repoOwner   string
	repoService services.RepoService
}

func NewProjectController(client services.GitHubV2Client, pat, repoOwner string, repoService services.RepoService) ProjectController {
	return &projectController{client: client, pat: pat, repoOwner: repoOwner, repoService: repoService}
}

type initProjectRequest struct {
	Title       string `json:"title"`
	OrgID       string `json:"orgId"`
	ProjectID   string `json:"projectId"`
	ProjectName string `json:"projectName"`
}

// InitProject godoc
// POST /api/v1/orgs/{org}/projects
func (c *projectController) InitProject(w http.ResponseWriter, r *http.Request) {
	var req initProjectRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		utils.WriteErrorResponse(w, http.StatusBadRequest, "invalid request body")
		return
	}

	if req.OrgID == "" || req.ProjectID == "" || req.ProjectName == "" {
		utils.WriteErrorResponse(w, http.StatusBadRequest, "orgId, projectId, and projectName are required")
		return
	}

	repo, err := c.repoService.CreateRepo(r.Context(), req.OrgID, req.ProjectID, req.ProjectName)
	if err != nil {
		if errors.Is(err, services.ErrRepoAlreadyExists) {
			utils.WriteErrorResponse(w, http.StatusConflict, "repository already exists for this project")
			return
		}
		slog.ErrorContext(r.Context(), "create repo failed", "error", err)
		utils.WriteErrorResponse(w, http.StatusInternalServerError, "failed to create repository")
		return
	}

	orgNodeID, err := c.client.GetOrgID(r.Context(), c.repoOwner, c.pat)
	if err != nil {
		slog.ErrorContext(r.Context(), "get org id failed", "org", c.repoOwner, "error", err)
		utils.WriteErrorResponse(w, http.StatusInternalServerError, "failed to resolve org id")
		return
	}

	githubProjectID, err := c.client.CreateGitHubV2Project(r.Context(), orgNodeID, c.pat, req.ProjectName)
	if err != nil {
		slog.ErrorContext(r.Context(), "create org project failed", "org", c.repoOwner, "title", req.ProjectName, "error", err)
		utils.WriteErrorResponse(w, http.StatusInternalServerError, "failed to create project")
		return
	}

	if err := c.repoService.SetGithubProjectID(r.Context(), req.ProjectID, githubProjectID); err != nil {
		slog.WarnContext(r.Context(), "failed to save github project id", "project", req.ProjectID, "error", err)
	}

	owner, repoName, parseErr := services.ParseOwnerRepo(repo.RepoURL)
	if parseErr == nil {
		if linkErr := c.client.LinkProjectToRepository(r.Context(), githubProjectID, owner, repoName, c.pat); linkErr != nil {
			slog.WarnContext(r.Context(), "failed to link project to repository", "project", req.ProjectID, "error", linkErr)
		}
	}

	utils.WriteSuccessResponse(w, http.StatusCreated, map[string]any{"projectId": githubProjectID, "repo": repo})
}
