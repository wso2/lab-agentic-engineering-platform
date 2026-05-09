package services

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/wso2/asdlc/asdlc-service/clients/gitservice"
	"github.com/wso2/asdlc/asdlc-service/models"
	"github.com/wso2/asdlc/asdlc-service/repositories"
)

// DispatchResult represents the outcome of dispatching a single task.
type DispatchResult struct {
	TaskID         string `json:"taskId"`
	ComponentName  string `json:"componentName"`
	RunName        string `json:"runName,omitempty"`
	BranchName     string `json:"branchName"`
	PullRequestURL string `json:"pullRequestUrl,omitempty"`
	Status         string `json:"status"`
	Error          string `json:"error,omitempty"`
}

// DispatchService orchestrates dispatching pending tasks. Per task it:
//
//  1. Verifies a GitHub issue exists (created at task generation).
//  2. Idempotently creates the feature branch + seed commit + draft PR.
//  3. Ensures the OC Component exists (with AutoBuild=false).
//  4. Mints a fresh per-task RS256 JWT.
//  5. Creates a WorkflowRun of ClusterWorkflow `app-factory-coding-agent`
//     via WorkflowRunService.TriggerCodingAgent — replaces the legacy
//     POST to remote-worker /dispatch. The Argo pod runs the same
//     provisionWorkspace + Claude Agent SDK code as the legacy in-process
//     worker, but in an ephemeral per-task container.
//
// Each step is idempotent on its persisted column so re-dispatch is safe
// across crashes:
//
//	IssueNumber set        → skip create-issue
//	BranchName set         → skip create-branch
//	PullRequestNumber set  → skip create-PR
//	DispatchedAt set       → skip WorkflowRun create
type DispatchService interface {
	DispatchTasks(ctx context.Context, orgID, projectID string) ([]DispatchResult, error)
}

type dispatchService struct {
	taskRepo      repositories.TaskRepository
	gitClient     gitservice.Client
	componentSvc  ComponentService
	store         *ArtifactStore
	taskTokens    *TaskTokenManager
	tokenInject   func(ctx context.Context) context.Context
	wfRunService  WorkflowRunService
	gitServiceURL string // URL the agent pod uses to reach git-service; cross-namespace FQDN in cluster
}

func NewDispatchService(
	taskRepo repositories.TaskRepository,
	gitClient gitservice.Client,
	componentSvc ComponentService,
	store *ArtifactStore,
	taskTokens *TaskTokenManager,
	tokenInject func(ctx context.Context) context.Context,
	wfRunService WorkflowRunService,
	gitServiceURL string,
) DispatchService {
	return &dispatchService{
		taskRepo:      taskRepo,
		gitClient:     gitClient,
		componentSvc:  componentSvc,
		store:         store,
		taskTokens:    taskTokens,
		tokenInject:   tokenInject,
		wfRunService:  wfRunService,
		gitServiceURL: gitServiceURL,
	}
}

func (s *dispatchService) DispatchTasks(ctx context.Context, orgID, projectID string) ([]DispatchResult, error) {
	tasks, err := s.taskRepo.ListByProjectID(ctx, orgID, projectID)
	if err != nil {
		return nil, fmt.Errorf("list tasks: %w", err)
	}

	repoInfo, err := s.gitClient.GetRepo(ctx, orgID, projectID)
	if err != nil {
		return nil, fmt.Errorf("get repo: %w", err)
	}
	if repoInfo == nil {
		return nil, fmt.Errorf("project repo not provisioned")
	}
	if repoInfo.DefaultBranch == "" {
		repoInfo.DefaultBranch = "main"
	}
	identity, err := s.gitClient.GetCredentialIdentity(ctx, orgID)
	if err != nil {
		return nil, fmt.Errorf("get credential identity: %w", err)
	}

	// Build a {title → status} index for dependsOn resolution. Multi-task-
	// per-component (tech-lead revamp §12) introduces base-commit ordering:
	// a task is dispatchable only when every task it dependsOn (by title)
	// has merged.
	statusByTitle := make(map[string]string, len(tasks))
	for _, t := range tasks {
		statusByTitle[t.Title] = t.Status
	}

	var results []DispatchResult
	for i := range tasks {
		task := &tasks[i]
		if task.Status == string(models.TaskStatusPendingDeps) {
			if !depsAllMerged(task, statusByTitle) {
				continue
			}
			task.Status = string(models.TaskStatusPending)
			if err := s.taskRepo.Update(ctx, task); err != nil {
				slog.WarnContext(ctx, "clear pending_deps", "task", task.ID, "error", err)
				continue
			}
		}
		if task.Status != string(models.TaskStatusPending) {
			continue
		}

		if !depsAllMerged(task, statusByTitle) {
			task.Status = string(models.TaskStatusPendingDeps)
			if err := s.taskRepo.Update(ctx, task); err != nil {
				slog.WarnContext(ctx, "set pending_deps", "task", task.ID, "error", err)
			}
			continue
		}

		res := s.dispatchOne(ctx, task, repoInfo, identity)
		results = append(results, res)
	}

	return results, nil
}

// depsAllMerged returns true when every task title listed in TaskDependsOn
// has status=merged|building|deployed (i.e. work landed on main).
func depsAllMerged(task *models.ComponentTask, statusByTitle map[string]string) bool {
	for _, depTitle := range task.TaskDependsOn {
		st, ok := statusByTitle[depTitle]
		if !ok {
			continue
		}
		switch st {
		case string(models.TaskStatusMerged),
			string(models.TaskStatusBuilding),
			string(models.TaskStatusDeployed):
			continue
		}
		return false
	}
	return true
}

// dispatchOne drives the idempotency contract for a single task.
func (s *dispatchService) dispatchOne(
	ctx context.Context,
	task *models.ComponentTask,
	repoInfo *gitservice.RepoInfo,
	identity *gitservice.IdentityProjection,
) DispatchResult {
	defaultBranch := repoInfo.DefaultBranch
	res := DispatchResult{TaskID: task.ID, ComponentName: task.ComponentName}

	if task.IssueNumber == 0 || task.IssueURL == "" {
		s.markFailed(ctx, task, "no GitHub issue on task — generation must precede dispatch")
		return failResult(res, task.ErrorMessage)
	}

	branchName := task.BranchName
	if branchName == "" {
		branchName = computeBranchName(task)
	}
	if _, err := s.gitClient.CreateBranch(ctx, task.OrgID, task.ProjectID, branchName, defaultBranch); err != nil {
		s.markFailed(ctx, task, fmt.Sprintf("create branch %s: %v", branchName, err))
		return failResult(res, task.ErrorMessage)
	}
	if task.BranchName == "" {
		task.BranchName = branchName
		if err := s.taskRepo.Update(ctx, task); err != nil {
			s.markFailed(ctx, task, fmt.Sprintf("persist branch: %v", err))
			return failResult(res, task.ErrorMessage)
		}
	}

	seedPayload := fmt.Sprintf(`{"taskId":"%s","componentName":"%s","issueNumber":%d}`+"\n",
		task.ID, task.ComponentName, task.IssueNumber)
	seedMsg := fmt.Sprintf("chore: seed task %s for %s", task.ID, task.ComponentName)
	if err := s.gitClient.SeedBranchCommit(ctx, task.OrgID, task.ProjectID, branchName, ".asdlc/task.json", seedMsg, seedPayload); err != nil {
		s.markFailed(ctx, task, fmt.Sprintf("seed branch commit: %v", err))
		return failResult(res, task.ErrorMessage)
	}

	if task.PullRequestNumber == 0 {
		prTitle := fmt.Sprintf("[%s] %s", task.ComponentName, task.Title)
		prBody := fmt.Sprintf("Implementation for component **%s**.\n\nCloses #%d", task.ComponentName, task.IssueNumber)
		pr, err := s.gitClient.CreateDraftPR(ctx, task.OrgID, task.ProjectID, &gitservice.CreateDraftPRRequest{
			Title: prTitle,
			Body:  prBody,
			Head:  branchName,
			Base:  defaultBranch,
		})
		if err != nil {
			s.markFailed(ctx, task, fmt.Sprintf("create draft PR: %v", err))
			return failResult(res, task.ErrorMessage)
		}
		task.PullRequestNumber = pr.Number
		task.PullRequestURL = pr.URL
		if err := s.taskRepo.Update(ctx, task); err != nil {
			s.markFailed(ctx, task, fmt.Sprintf("persist PR: %v", err))
			return failResult(res, task.ErrorMessage)
		}
	}

	if s.componentSvc != nil {
		if err := s.ensureOCComponent(ctx, task, repoInfo); err != nil {
			s.markFailed(ctx, task, fmt.Sprintf("ensure OC component: %v", err))
			return failResult(res, task.ErrorMessage)
		}
	}

	if s.taskTokens == nil {
		s.markFailed(ctx, task, "task token manager not configured")
		return failResult(res, task.ErrorMessage)
	}
	bearer, err := s.taskTokens.Issue(task.ID, task.OrgID, task.ProjectID)
	if err != nil {
		s.markFailed(ctx, task, fmt.Sprintf("issue task jwt: %v", err))
		return failResult(res, task.ErrorMessage)
	}

	if s.wfRunService == nil {
		s.markFailed(ctx, task, "workflow run service not configured")
		return failResult(res, task.ErrorMessage)
	}
	runName, err := s.wfRunService.TriggerCodingAgent(ctx, CodingAgentTrigger{
		Task:          task,
		RepoURL:       repoInfo.RepoURL,
		IdentityName:  identity.Name,
		IdentityEmail: identity.Email,
		IdentityLogin: identity.Login,
		Prompt:        buildAgentPrompt(task),
		Bearer:        bearer,
		GitServiceURL: s.gitServiceURL,
	})
	if err != nil {
		s.markFailed(ctx, task, fmt.Sprintf("trigger coding-agent: %v", err))
		return failResult(res, task.ErrorMessage)
	}

	now := time.Now()
	task.DispatchedAt = &now
	task.LastCodingAgentRunName = runName
	task.Status = string(models.TaskStatusInProgress)
	if err := s.taskRepo.Update(ctx, task); err != nil {
		slog.ErrorContext(ctx, "failed to update task after dispatch",
			"task", task.ID, "error", err)
	}

	slog.InfoContext(ctx, "task dispatched",
		"task", task.ID, "component", task.ComponentName,
		"branch", branchName, "pr", task.PullRequestNumber, "run", runName)

	res.RunName = runName
	res.BranchName = branchName
	res.PullRequestURL = task.PullRequestURL
	res.Status = "running"
	return res
}

func failResult(r DispatchResult, msg string) DispatchResult {
	r.Status = "failed"
	r.Error = msg
	return r
}

func (s *dispatchService) markFailed(ctx context.Context, task *models.ComponentTask, msg string) {
	task.Status = string(models.TaskStatusFailed)
	task.ErrorMessage = msg
	if err := s.taskRepo.Update(ctx, task); err != nil {
		slog.ErrorContext(ctx, "failed to mark task failed", "task", task.ID, "error", err)
	}
	slog.ErrorContext(ctx, "dispatch step failed", "task", task.ID, "error", msg)
}

func computeBranchName(task *models.ComponentTask) string {
	slug := strings.ToLower(task.ComponentName)
	slug = strings.NewReplacer(" ", "-", "_", "-", "/", "-").Replace(slug)
	slug = trimNonSlug(slug)
	if slug == "" {
		slug = "component"
	}
	short := task.ID
	if len(short) > 8 {
		short = short[:8]
	}
	short = strings.ReplaceAll(short, "-", "")
	return "task/" + slug + "-" + short
}

func trimNonSlug(s string) string {
	var b strings.Builder
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' {
			b.WriteRune(r)
		}
	}
	out := b.String()
	for strings.Contains(out, "--") {
		out = strings.ReplaceAll(out, "--", "-")
	}
	return strings.Trim(out, "-")
}

// buildAgentPrompt returns the user prompt given to the Claude agent. The full
// task context lives in the GitHub issue body (services/issue_body.go); the
// prompt points the agent at the issue and tells them the working branch is
// already checked out.
func buildAgentPrompt(task *models.ComponentTask) string {
	if task.BranchName != "" {
		return fmt.Sprintf("Work on this GitHub issue: %s\n\nYour working branch %s is already checked out in this directory.",
			task.IssueURL, task.BranchName)
	}
	return fmt.Sprintf("Work on this GitHub issue: %s", task.IssueURL)
}

// ensureOCComponent creates the OC Component (one per task component) needed
// for the build to fire when the merge push arrives. AutoBuild=false — every
// build is driven by the BFF's push-webhook handler creating a WorkflowRun
// pinned to the merge SHA.
func (s *dispatchService) ensureOCComponent(
	ctx context.Context,
	task *models.ComponentTask,
	repoInfo *gitservice.RepoInfo,
) error {
	if s.tokenInject != nil {
		ctx = s.tokenInject(ctx)
	}
	componentName := toK8sName(task.ComponentName)

	comp, err := resolveDesignComponentVia(ctx, s.store, task)
	if err != nil {
		return fmt.Errorf("resolve component: %w", err)
	}

	dockerContext := comp.AppPath
	dockerFilePath := "Dockerfile"
	if dockerContext != "" {
		dockerFilePath = dockerContext + "/Dockerfile"
	} else {
		dockerContext = "."
	}

	if repoInfo == nil || repoInfo.OcSecretRefName == nil || *repoInfo.OcSecretRefName == "" {
		return fmt.Errorf("repo has no SecretReference name; project=%s", task.ProjectID)
	}
	secretRefName := *repoInfo.OcSecretRefName

	branch := repoInfo.DefaultBranch
	if branch == "" {
		branch = "main"
	}

	description := task.Title
	if task.Rationale != "" {
		description = task.Title + " — " + task.Rationale
	}

	_, err = s.componentSvc.CreateComponent(ctx, task.OrgID, task.ProjectID, &models.CreateComponentRequest{
		Name:        componentName,
		DisplayName: task.ComponentName,
		Description: description,
		Type:        ocEntrypoint(comp.ComponentType),
		AutoBuild:   false,
		AutoDeploy:  false,
		Workflow: &models.ComponentWorkflowSpec{
			Kind: "ClusterWorkflow",
			Name: "dockerfile-builder",
			Parameters: &models.ComponentWorkflowParameters{
				Repository: &models.WorkflowRepository{
					URL:       repoInfo.RepoURL,
					SecretRef: secretRefName,
					AppPath:   comp.AppPath,
					Revision:  &models.WorkflowRevision{Branch: branch},
				},
				Docker: &models.DockerParameters{
					Context:  dockerContext,
					FilePath: dockerFilePath,
				},
			},
		},
	})
	if err != nil {
		return fmt.Errorf("create component: %w", err)
	}
	return nil
}
