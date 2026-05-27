package services

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/wso2/asdlc/asdlc-service/clients/openchoreo"
	"github.com/wso2/asdlc/asdlc-service/models"
	"github.com/wso2/asdlc/asdlc-service/repositories"
)

// ProjectService handles business logic for project operations.
type ProjectService interface {
	ListProjects(ctx context.Context, orgName string, limit int, cursor string) (*models.ProjectList, error)
	GetProject(ctx context.Context, orgName, projectName string) (*models.Project, error)
	CreateProject(ctx context.Context, orgName string, req *models.CreateProjectRequest) (*models.Project, error)
	DeleteProject(ctx context.Context, orgName, projectName string) error
	GetRepoStatus(ctx context.Context, orgName, projectID string) (*models.GitRepository, error)
	GetProjectStatus(ctx context.Context, orgName, projectName string) (*models.ProjectStatus, error)
}

type projectService struct {
	client      openchoreo.ProjectClient
	repoSvc     RepoService
	webhookSvc  WebhookService
	artifactSvc ArtifactService
	store       *ArtifactStore
	taskRepo    repositories.TaskRepository
}

func NewProjectService(
	client openchoreo.ProjectClient,
	repoSvc RepoService,
	webhookSvc WebhookService,
	artifactSvc ArtifactService,
	store *ArtifactStore,
	taskRepo repositories.TaskRepository,
) ProjectService {
	return &projectService{
		client:      client,
		repoSvc:     repoSvc,
		webhookSvc:  webhookSvc,
		artifactSvc: artifactSvc,
		store:       store,
		taskRepo:    taskRepo,
	}
}

func (s *projectService) ListProjects(ctx context.Context, orgName string, limit int, cursor string) (*models.ProjectList, error) {
	list, err := s.client.ListProjects(ctx, orgName, limit, cursor)
	if err != nil {
		return nil, translateHTTPError(err)
	}
	return list, nil
}

func (s *projectService) GetProject(ctx context.Context, orgName, projectName string) (*models.Project, error) {
	project, err := s.client.GetProject(ctx, orgName, projectName)
	if err != nil {
		return nil, translateHTTPError(err)
	}
	return project, nil
}

func (s *projectService) CreateProject(ctx context.Context, orgName string, req *models.CreateProjectRequest) (*models.Project, error) {
	project, err := s.client.CreateProject(ctx, orgName, req)
	if err != nil {
		return nil, translateHTTPError(err)
	}

	// Provision + clone the platform-owned git repo (async — polling via GetRepoStatus).
	if s.repoSvc != nil {
		repoInfo, createErr := s.repoSvc.CreateRepo(ctx, orgName, project.Name, req.Name)
		if createErr != nil {
			slog.ErrorContext(ctx, "failed to provision repo", "project", project.Name, "error", createErr)
			// Don't fail project creation — clone happens async and can be retried.
		} else {
			// Build credentials are now pre-staged per WorkflowRun as a K8s
			// Secret named `<workflowRunName>-git-secret` in
			// workflows-<orgID> immediately before each dispatch — see
			// docs/design/build-credential-injection.md. Project creation
			// no longer participates in any secret provisioning;
			// OcSecretRefName is unused on new flows.
			if repoInfo == nil {
				slog.ErrorContext(ctx, "nil repoInfo on CreateRepo", "project", project.Name)
			}
			// Register the per-repo webhook so the BFF starts receiving events
			// (pull_request, push, issue_comment) on this repo. Best-effort.
			if s.webhookSvc != nil {
				if _, hookErr := s.webhookSvc.Register(ctx, project.Name); hookErr != nil {
					slog.ErrorContext(ctx, "failed to register webhook on repo",
						"project", project.Name, "error", hookErr)
				}
			}
		}
	}

	return project, nil
}

func (s *projectService) DeleteProject(ctx context.Context, orgName, projectName string) error {
	if err := translateHTTPError(s.client.DeleteProject(ctx, orgName, projectName)); err != nil {
		return err
	}

	// Clean up the git clone
	if s.repoSvc != nil {
		if err := s.repoSvc.DeleteRepo(ctx, projectName); err != nil {
			slog.ErrorContext(ctx, "failed to delete git repo for project", "org", orgName, "project", projectName, "error", err)
		}
	}

	return nil
}

func (s *projectService) GetRepoStatus(ctx context.Context, orgName, projectID string) (*models.GitRepository, error) {
	if s.repoSvc == nil {
		return nil, fmt.Errorf("repo service not configured")
	}
	repo, err := s.repoSvc.GetRepo(ctx, projectID)
	if err != nil {
		return nil, fmt.Errorf("get repo: %w", err)
	}
	if repo == nil {
		return nil, ErrProjectNotFound
	}
	return repo, nil
}

func (s *projectService) GetProjectStatus(ctx context.Context, orgName, projectName string) (*models.ProjectStatus, error) {
	status := &models.ProjectStatus{}

	// Check git repo
	if s.repoSvc == nil {
		status.Phase = "no-repo"
		return status, nil
	}

	repo, err := s.repoSvc.GetRepo(ctx, projectName)
	if err != nil {
		slog.ErrorContext(ctx, "get repo for project status", "error", err, "project", projectName)
		status.Phase = "no-repo"
		return status, nil
	}
	if repo == nil {
		status.Phase = "no-repo"
		return status, nil
	}

	status.RepoStatus = repo.Status
	status.RepoURL = repo.RepoURL

	if repo.Status == "pending" || repo.Status == "cloning" {
		status.Phase = "repo-cloning"
		return status, nil
	}

	if repo.Status == "error" {
		status.Phase = "no-repo"
		return status, nil
	}

	// Check requirements (any markdown doc under specs/requirements/ counts).
	files, err := s.store.ListRequirements(ctx, orgName, projectName)
	if err != nil && !IsNotFound(err) {
		return nil, fmt.Errorf("list requirements: %w", err)
	}
	status.HasSpec = len(files) > 0

	if s.artifactSvc != nil {
		reqVersions, _ := s.artifactSvc.ListRequirementsVersions(ctx, projectName)
		designVersions, _ := s.artifactSvc.ListDesignVersions(ctx, projectName)

		if len(reqVersions) > 0 {
			status.SpecStatus = "approved"
		} else if status.HasSpec {
			status.SpecStatus = "draft"
		}
		if len(designVersions) > 0 {
			status.DesignStatus = "approved"
		}
	}

	if !status.HasSpec {
		status.Phase = "prompt"
		return status, nil
	}

	// Check design
	design, err := s.store.ReadDesign(ctx, orgName, projectName)
	if err != nil && !IsNotFound(err) {
		return nil, fmt.Errorf("read design: %w", err)
	}
	status.HasDesign = design != nil

	if !status.HasDesign {
		status.Phase = "spec"
		return status, nil
	}

	// Check tasks — the unified Tasks feature replaces the old Plan step.
	tasks, err := s.taskRepo.ListByProjectID(ctx, orgName, projectName)
	if err != nil {
		return nil, fmt.Errorf("list tasks: %w", err)
	}
	status.HasTasks = len(tasks) > 0

	if !status.HasTasks {
		status.Phase = "tasks"
		return status, nil
	}

	status.Phase = "components"
	return status, nil
}

// translateHTTPError lifts OC-level sentinel errors (openchoreo.ErrNotFound
// etc.) into the project-service vocabulary the controllers branch on. The
// underlying err is preserved in the chain so deeper layers can still
// errors.Is against openchoreo.* if they want richer context.
func translateHTTPError(err error) error {
	if err == nil {
		return nil
	}
	switch {
	case errors.Is(err, openchoreo.ErrNotFound):
		return fmt.Errorf("%w: %v", ErrProjectNotFound, err)
	case errors.Is(err, openchoreo.ErrUnauthorized):
		return ErrUnauthorized
	case errors.Is(err, openchoreo.ErrForbidden):
		return ErrForbidden
	}
	return err
}
