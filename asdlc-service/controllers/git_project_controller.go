package controllers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"

	"github.com/wso2/asdlc/asdlc-service/internal/credentials"
	"github.com/wso2/asdlc/asdlc-service/services"
	"github.com/wso2/asdlc/asdlc-service/utils"
)

// GitProjectController handles HTTP requests for GitHub project + repo
// initialization. Renamed from the original ProjectController on fold-in so
// the BFF-side ProjectController (which manages OC Projects via the platform
// API) keeps its name.
type GitProjectController interface {
	InitProject(w http.ResponseWriter, r *http.Request)
}

type gitProjectController struct {
	client      services.GitHubV2Client
	resolver    credentials.Resolver
	repoService services.RepoService
}

func NewGitProjectController(client services.GitHubV2Client, resolver credentials.Resolver, repoService services.RepoService) GitProjectController {
	return &gitProjectController{client: client, resolver: resolver, repoService: repoService}
}

type initProjectRequest struct {
	Title       string `json:"title"`
	OrgID       string `json:"orgId"`
	ProjectID   string `json:"projectId"`
	ProjectName string `json:"projectName"`
}

// InitProject godoc
// POST /api/v1/orgs/{org}/projects
func (c *gitProjectController) InitProject(w http.ResponseWriter, r *http.Request) {
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

	owner, token, err := c.resolveOwnerAndToken(r.Context(), req.OrgID)
	if err != nil {
		slog.ErrorContext(r.Context(), "resolve org credential failed", "ocOrgId", req.OrgID, "error", err)
		utils.WriteErrorResponse(w, http.StatusInternalServerError, "failed to resolve org credential")
		return
	}

	orgNodeID, err := c.client.GetOrgID(r.Context(), owner, token)
	if err != nil {
		slog.ErrorContext(r.Context(), "get org id failed", "org", owner, "error", err)
		utils.WriteErrorResponse(w, http.StatusInternalServerError, "failed to resolve org id")
		return
	}

	githubProjectID, err := c.client.CreateGitHubV2Project(r.Context(), orgNodeID, token, req.ProjectName)
	if err != nil {
		slog.ErrorContext(r.Context(), "create org project failed", "org", owner, "title", req.ProjectName, "error", err)
		utils.WriteErrorResponse(w, http.StatusInternalServerError, "failed to create project")
		return
	}

	if err := c.repoService.SetGithubProjectID(r.Context(), req.ProjectID, githubProjectID); err != nil {
		slog.WarnContext(r.Context(), "failed to save github project id", "project", req.ProjectID, "error", err)
	}

	repoOwner, repoName, parseErr := services.ParseOwnerRepo(repo.RepoURL)
	if parseErr == nil {
		if linkErr := c.client.LinkProjectToRepository(r.Context(), githubProjectID, repoOwner, repoName, token); linkErr != nil {
			slog.WarnContext(r.Context(), "failed to link project to repository", "project", req.ProjectID, "error", linkErr)
		}
	}

	utils.WriteSuccessResponse(w, http.StatusCreated, map[string]any{"projectId": githubProjectID, "repo": repo})
}

// resolveOwnerAndToken returns the GitHub repo owner login and a usable
// bearer for V2 (GraphQL) calls, both sourced from the per-org credential.
// No env-driven owner, no platform PAT — every call is parameterised by
// the request's ocOrgID.
func (c *gitProjectController) resolveOwnerAndToken(ctx context.Context, ocOrgID string) (owner, token string, err error) {
	cred, err := c.resolver.Resolve(ctx, ocOrgID)
	if err != nil {
		return "", "", fmt.Errorf("resolve credential: %w", err)
	}
	token, _, err = cred.Token(ctx)
	if err != nil {
		return "", "", fmt.Errorf("token: %w", err)
	}
	owner = cred.RepoOwner()
	if owner == "" {
		return "", "", fmt.Errorf("credential has no repo owner")
	}
	return owner, token, nil
}
