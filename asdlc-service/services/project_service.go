package services

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"

	"github.com/wso2/asdlc/asdlc-service/clients/gitservice"
	"github.com/wso2/asdlc/asdlc-service/clients/openchoreo"
	"github.com/wso2/asdlc/asdlc-service/clients/requests"
	"github.com/wso2/asdlc/asdlc-service/models"
	"github.com/wso2/asdlc/asdlc-service/repositories"
)

// ProjectService handles business logic for project operations.
type ProjectService interface {
	ListProjects(ctx context.Context, orgName string, limit int, cursor string) (*models.ProjectList, error)
	GetProject(ctx context.Context, orgName, projectName string) (*models.Project, error)
	CreateProject(ctx context.Context, orgName string, req *models.CreateProjectRequest) (*models.Project, error)
	DeleteProject(ctx context.Context, orgName, projectName string) error
	GetRepoStatus(ctx context.Context, orgName, projectID string) (*gitservice.RepoInfo, error)
	GetProjectStatus(ctx context.Context, orgName, projectName string) (*models.ProjectStatus, error)
}

type projectService struct {
	client       openchoreo.ProjectClient
	gitClient    gitservice.Client
	secretRefCli openchoreo.SecretRefClient
	store        *ArtifactStore
	taskRepo     repositories.TaskRepository
}

func NewProjectService(
	client openchoreo.ProjectClient,
	gitClient gitservice.Client,
	secretRefCli openchoreo.SecretRefClient,
	store *ArtifactStore,
	taskRepo repositories.TaskRepository,
) ProjectService {
	return &projectService{
		client:       client,
		gitClient:    gitClient,
		secretRefCli: secretRefCli,
		store:        store,
		taskRepo:     taskRepo,
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
	if s.gitClient != nil {
		repoInfo, createErr := s.gitClient.InitProjectComponents(ctx, &gitservice.CreateRepoRequest{
			OrgID:       orgName,
			ProjectID:   project.Name,
			ProjectName: req.Name,
		})
		if createErr != nil {
			slog.ErrorContext(ctx, "failed to provision repo", "project", project.Name, "error", createErr)
			// Don't fail project creation — clone happens async and can be retried.
		} else {
			// Phase 2 PR C — create the OC SecretReference CR for this repo.
			// The CR points at OpenBao at secret/asdlc/{ocOrgId}/git/{repoSlug};
			// MintBuildToken writes the per-build token into that path
			// immediately before each WorkflowRun. The CR is idempotent on
			// (namespace, name) so re-creates are safe.
			if s.secretRefCli == nil {
				slog.WarnContext(ctx, "skipping OC SecretReference creation: no secretRefCli configured",
					"project", project.Name)
			} else if repoInfo == nil {
				slog.ErrorContext(ctx, "skipping OC SecretReference creation: git-service returned nil repoInfo (build will be stuck pending)",
					"project", project.Name)
			} else if repoInfo.OcSecretRefName == nil || *repoInfo.OcSecretRefName == "" || repoInfo.RepoSlug == "" {
				// Loud failure: this is exactly the silent skip that stranded
				// per-project builds in WorkflowPending after the
				// InitProject response shape changed under us. If the
				// downstream contract drifts again, fail at project create
				// rather than discovering it 10 minutes into a build.
				slog.ErrorContext(ctx, "BUG: git-service init response missing OcSecretRefName/RepoSlug — build will be stuck in WorkflowPending until SecretReference is created out-of-band",
					"project", project.Name,
					"orgId", orgName,
					"hasOcSecretRefName", repoInfo.OcSecretRefName != nil && *repoInfo.OcSecretRefName != "",
					"hasRepoSlug", repoInfo.RepoSlug != "",
					"repoUrl", repoInfo.RepoURL)
			} else {
				// Path is relative to the OpenBao KV v2 mount (`secret`); the
				// ClusterSecretStore's path/version=v2 config adds `data/` at
				// read time. The OC API normalises the apiVersion v1alpha1 →
				// v1 internally; we POST to /api/v1/.../secretreferences.
				vaultPath := fmt.Sprintf("asdlc/%s/git/%s", orgName, repoInfo.RepoSlug)
				if err := s.secretRefCli.EnsureSecretReference(ctx, orgName, *repoInfo.OcSecretRefName, vaultPath); err != nil {
					slog.ErrorContext(ctx, "failed to create OC SecretReference",
						"project", project.Name, "name", *repoInfo.OcSecretRefName, "error", err)
				} else {
					slog.InfoContext(ctx, "secretref ensure",
						"name", *repoInfo.OcSecretRefName, "ns", orgName, "vaultPath", vaultPath)
				}
			}
			// Register the per-repo webhook so the BFF starts receiving events
			// (pull_request, push, issue_comment) on this repo. Best-effort:
			// the GitHub repo is already created at this point, so a webhook
			// failure leaves a usable repo but no event flow — surfaceable in
			// logs and recoverable by re-running with the right scopes on the
			// PAT. Phase 2 App-mode credentials short-circuit registration
			// (returns strategy=platform) inside the resolver.
			if _, hookErr := s.gitClient.RegisterWebhook(ctx, orgName, project.Name); hookErr != nil {
				slog.ErrorContext(ctx, "failed to register webhook on repo",
					"project", project.Name, "error", hookErr)
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
	if s.gitClient != nil {
		if err := s.gitClient.DeleteRepo(ctx, orgName, projectName); err != nil {
			slog.ErrorContext(ctx, "failed to delete git repo for project", "org", orgName, "project", projectName, "error", err)
		}
	}

	return nil
}

func (s *projectService) GetRepoStatus(ctx context.Context, orgName, projectID string) (*gitservice.RepoInfo, error) {
	if s.gitClient == nil {
		return nil, fmt.Errorf("git service not configured")
	}
	repo, err := s.gitClient.GetRepo(ctx, orgName, projectID)
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
	if s.gitClient == nil {
		status.Phase = "no-repo"
		return status, nil
	}

	repo, err := s.gitClient.GetRepo(ctx, orgName, projectName)
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

	// Check requirements (any markdown doc under .asdlc/requirements/ counts).
	files, err := s.store.ListRequirements(ctx, orgName, projectName)
	if err != nil && !IsNotFound(err) {
		return nil, fmt.Errorf("list requirements: %w", err)
	}
	status.HasSpec = len(files) > 0

	if s.gitClient != nil {
		reqVersions, _ := s.gitClient.ListRequirementsVersions(ctx, orgName, projectName)
		designVersions, _ := s.gitClient.ListDesignVersions(ctx, orgName, projectName)

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

func translateHTTPError(err error) error {
	if err == nil {
		return nil
	}
	var httpErr *requests.HttpError
	if errors.As(err, &httpErr) {
		switch httpErr.StatusCode {
		case http.StatusNotFound:
			return fmt.Errorf("%w: %s", ErrProjectNotFound, httpErr.Body)
		case http.StatusUnauthorized:
			return ErrUnauthorized
		case http.StatusForbidden:
			return ErrForbidden
		}
	}
	return err
}
