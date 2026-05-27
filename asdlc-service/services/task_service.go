package services

import (
	"context"
	"fmt"
	"hash/fnv"
	"io"
	"log/slog"
	"strings"

	"gorm.io/gorm"

	"github.com/wso2/asdlc/asdlc-service/clients/agents"
	dbclient "github.com/wso2/asdlc/asdlc-service/clients/database"
	"github.com/wso2/asdlc/asdlc-service/clients/gitservice"
	"github.com/wso2/asdlc/asdlc-service/clients/oauth"
	"github.com/wso2/asdlc/asdlc-service/models"
	"github.com/wso2/asdlc/asdlc-service/repositories"
)

type TaskService interface {
	GetTask(ctx context.Context, taskID string) (*models.ComponentTask, error)
	GetTasks(ctx context.Context, orgID, projectID string) (*models.Tasks, error)
	GetTaskByComponent(ctx context.Context, orgID, projectID, componentName string) (*models.ComponentTask, error)
	ListTasks(ctx context.Context, orgID, projectID string) ([]models.ComponentTask, error)
	// ListTasksByOrg lists tasks across every project in the org with
	// optional status / cause / since filters. Used by the PR D
	// ReachReconciliationBanner ({status: abandoned, cause: repo.unselected,
	// since: now-24h}).
	ListTasksByOrg(ctx context.Context, orgID string, f repositories.ListByOrgFilter) ([]models.ComponentTask, error)
	// GenerateTasks is the legacy non-streaming entry point — kept on the
	// interface for the migration window only. Always returns an error
	// directing callers to the SSE endpoint.
	GenerateTasks(ctx context.Context, orgID, projectID string) ([]models.ComponentTask, error)
	// StreamGenerateTasks orchestrates the two-phase tech-lead agent:
	// Phase 1 (plan) → BFF persists task rows + creates GH issues →
	// Phase 2 (detail) → BFF edits issue bodies → reconciliation closes
	// pending tasks for removed components → emits data-finish.
	// Frames are written to `out`; flush is called after each frame.
	// See docs/design/tech-lead-agent.md §3, §6, §10.
	StreamGenerateTasks(ctx context.Context, orgID, projectID string, out io.Writer, flush func()) error
	// RegenerateTaskBody re-runs the Phase 2 detail call for a single task
	// and re-edits the GH issue body. Used when Phase 2 streaming failed
	// for one task and the user clicks "Retry body".
	RegenerateTaskBody(ctx context.Context, taskID string, out io.Writer, flush func()) error
	// ReconcilePendingForDesignChange auto-closes pending tasks whose
	// componentName no longer exists in the project's `specs/design/`
	// tree. Idempotent; emits no SSE; called from
	// design_service.SaveAndProceed after the design tag bump.
	ReconcilePendingForDesignChange(ctx context.Context, orgID, projectID string) error
	ExecTask(ctx context.Context, taskID string) error
}

type taskService struct {
	db            *gorm.DB
	taskRepo      repositories.TaskRepository
	store         *ArtifactStore
	componentSvc  ComponentService     // for creating OC components and checking build/deploy status
	tokenProvider *oauth.TokenProvider // for service-to-service auth (OC API)
	configSvc     ConfigService        // for fetching env vars at deploy time
	gitClient     gitservice.Client    // for committing and pushing code after implementation
	agentsClient  agents.Client        // for calling tech-lead agent (plan + detail)
	dbClient      dbclient.Client      // for provisioning and testing databases
	// skillSvc resolves the per-org skill catalogue for tech-lead plan
	// (attached-skills context) + detail (full bodies). Snapshot writes
	// to design_version_skill_snapshots also go through here. Optional
	// in tests; nil → tech-lead runs with no skills attached.
	skillSvc *SkillService
}

// SetSkillService wires the skills catalogue + snapshot writer.
// Mirrors the SetTraitSync setter pattern.
func (s *taskService) SetSkillService(svc *SkillService) {
	s.skillSvc = svc
}

func NewTaskService(
	db *gorm.DB,
	taskRepo repositories.TaskRepository,
	store *ArtifactStore,
	componentSvc ComponentService,
	tokenProvider *oauth.TokenProvider,
	configSvc ConfigService,
	gitClient gitservice.Client,
	agentsClient agents.Client,
	dbClient dbclient.Client,
) TaskService {
	return &taskService{
		db:            db,
		taskRepo:      taskRepo,
		store:         store,
		componentSvc:  componentSvc,
		tokenProvider: tokenProvider,
		configSvc:     configSvc,
		gitClient:     gitClient,
		agentsClient:  agentsClient,
		dbClient:      dbClient,
	}
}

// hashTechLeadKey returns the int64 advisory-lock key for a project's
// tech-lead generation. Mirrors webhook/projector.go::hashKey but scoped to
// the techlead namespace so it doesn't collide with task / project locks.
func hashTechLeadKey(projectID string) int64 {
	h := fnv.New64a()
	h.Write([]byte("techlead:" + projectID))
	return int64(h.Sum64()) //nolint:gosec
}

func (s *taskService) GetTaskByComponent(ctx context.Context, orgID, projectID, componentName string) (*models.ComponentTask, error) {
	task, err := s.taskRepo.GetByComponentName(ctx, orgID, projectID, componentName)
	if err != nil {
		return nil, fmt.Errorf("get task by component: %w", err)
	}
	return task, nil
}

func (s *taskService) GetTask(ctx context.Context, taskID string) (*models.ComponentTask, error) {
	task, err := s.taskRepo.GetByID(ctx, taskID)
	if err != nil {
		return nil, fmt.Errorf("get task: %w", err)
	}
	if task == nil {
		return nil, ErrTaskNotFound
	}
	return task, nil
}

func (s *taskService) GetTasks(ctx context.Context, orgID, projectID string) (*models.Tasks, error) {
	if s.gitClient == nil {
		return nil, fmt.Errorf("git client not configured")
	}

	// DB is the source of truth for which tasks this project owns. We enrich
	// with GitHub issue labels (for kanban-style status) but do NOT rely on
	// label-based GitHub filtering — label creation requires a PAT scope we
	// can't assume, so issues are often label-less.
	dbTasks, err := s.taskRepo.ListByProjectID(ctx, orgID, projectID)
	if err != nil {
		return nil, fmt.Errorf("list tasks: %w", err)
	}
	if len(dbTasks) == 0 {
		return nil, nil
	}

	// Best-effort: fetch all open GitHub issues to pick up kanban labels.
	// If this fails we still render tasks from DB.
	issueByNum := make(map[int]gitservice.IssueInfo)
	issues, err := s.gitClient.ListIssues(ctx, orgID, projectID, nil)
	if err != nil {
		slog.WarnContext(ctx, "failed to list github issues; rendering from DB only", "error", err)
	} else {
		for _, iss := range issues {
			if iss.State != "open" {
				continue
			}
			issueByNum[iss.Number] = iss
		}
	}

	tasks := make([]models.ComponentTask, 0, len(dbTasks))
	for i := range dbTasks {
		t := dbTasks[i]
		if iss, ok := issueByNum[t.IssueNumber]; ok {
			t.Title = iss.Title
			t.Labels = models.StringSlice(iss.Labels)
			t.IssueURL = iss.URL
		}
		tasks = append(tasks, t)
	}

	return &models.Tasks{
		ProjectID: projectID,
		OrgID:     orgID,
		Tasks:     tasks,
		Status:    "approved",
	}, nil
}

func (s *taskService) ListTasks(ctx context.Context, orgID, projectID string) ([]models.ComponentTask, error) {
	tasks, err := s.taskRepo.ListByProjectID(ctx, orgID, projectID)
	if err != nil {
		return nil, fmt.Errorf("list tasks: %w", err)
	}
	return tasks, nil
}

func (s *taskService) ListTasksByOrg(ctx context.Context, orgID string, f repositories.ListByOrgFilter) ([]models.ComponentTask, error) {
	tasks, err := s.taskRepo.ListByOrgID(ctx, orgID, f)
	if err != nil {
		return nil, fmt.Errorf("list tasks by org: %w", err)
	}
	return tasks, nil
}

// GenerateTasks is the legacy non-streaming entry point. The tech-lead
// agent revamp (docs/design/tech-lead-agent.md) replaces this with the
// SSE-streaming orchestrator wired to the two-phase agent. The HTTP
// controller now calls StreamGenerateTasks; this method exists only to
// keep the TaskService interface shape during the migration window.
//
// Returning an error here is intentional — any caller still hitting
// /tasks/generate as a non-SSE call has not been updated to the new
// streaming protocol and should be fixed, not silently routed to a
// half-implemented path.
func (s *taskService) GenerateTasks(ctx context.Context, orgID, projectID string) ([]models.ComponentTask, error) {
	return nil, fmt.Errorf("non-streaming task generation has been removed; use StreamGenerateTasks (POST /tasks/generate over SSE)")
}

// topoSortComponents returns design.Components in dependency order: a component
// is emitted only after all components it dependsOn have been emitted. Missing
// or cyclic dependencies don't block output — remaining components are emitted
// in their original design order. Names are compared case-insensitively to
// match the rest of the codebase's lookup pattern.
func topoSortComponents(components []models.DesignComponent) []models.DesignComponent {
	if len(components) == 0 {
		return components
	}
	emitted := make(map[string]bool, len(components))
	result := make([]models.DesignComponent, 0, len(components))

	for {
		progressed := false
		for _, c := range components {
			key := strings.ToLower(c.Name)
			if emitted[key] {
				continue
			}
			ready := true
			for _, dep := range c.DependsOn {
				if !emitted[strings.ToLower(dep)] {
					// Only block on dependencies that actually refer to
					// known components; dangling deps are treated as satisfied.
					for _, other := range components {
						if strings.EqualFold(other.Name, dep) {
							ready = false
							break
						}
					}
					if !ready {
						break
					}
				}
			}
			if ready {
				result = append(result, c)
				emitted[key] = true
				progressed = true
			}
		}
		if !progressed {
			break
		}
	}

	// Emit any remaining (cyclic) components in original order.
	for _, c := range components {
		if !emitted[strings.ToLower(c.Name)] {
			result = append(result, c)
			emitted[strings.ToLower(c.Name)] = true
		}
	}
	return result
}
// ExecTask starts executing a task. For now, it just logs and doesn't perform any actions.
func (s *taskService) ExecTask(ctx context.Context, taskID string) error {
	task, err := s.taskRepo.GetByID(ctx, taskID)
	if err != nil {
		return fmt.Errorf("get task: %w", err)
	}
	if task == nil {
		return ErrTaskNotFound
	}

	slog.InfoContext(ctx, "executing task",
		"taskId", taskID,
		"component", task.ComponentName,
		"status", task.Status,
		"title", task.Title)

	if task.ExecType == "SYSTEM" {
		slog.InfoContext(ctx, "Setting up environment for task execution",
			"taskId", taskID, "title", task.Title)

		if s.dbClient != nil {
			// Provision database for the component
			slog.InfoContext(ctx, "Provisioning database for component",
				"taskId", taskID, "component", task.ComponentName)

			dbCreds, err := s.dbClient.ProvisionDatabase(ctx, task.ProjectID)
			if err != nil {
				slog.ErrorContext(ctx, "failed to provision database",
					"taskId", taskID, "component", task.ComponentName, "error", err)
				return fmt.Errorf("provision database: %w", err)
			}

			slog.InfoContext(ctx, "Database provisioned successfully",
				"taskId", taskID, "component", task.ComponentName,
				"host", dbCreds.Host, "port", dbCreds.Port, "database", dbCreds.Database)

			// Test the database connection
			slog.InfoContext(ctx, "Testing database connection",
				"taskId", taskID, "component", task.ComponentName)

			if err := s.dbClient.TestConnection(ctx, dbCreds); err != nil {
				slog.ErrorContext(ctx, "failed to test database connection",
					"taskId", taskID, "component", task.ComponentName, "error", err)
				return fmt.Errorf("test connection: %w", err)
			}

			slog.InfoContext(ctx, "Database connection test passed",
				"taskId", taskID, "component", task.ComponentName)
		} else {
			slog.WarnContext(ctx, "Database client not configured, skipping database provisioning",
				"taskId", taskID, "component", task.ComponentName)
		}
	}
	return nil
}

// ensureIssueForTask creates a GitHub issue for a task if one has not already
// been created, and stores the URL + number back on the task. Idempotent —
// a task that already has an IssueURL is left untouched.
//
// `comp` is the DesignComponent resolved fresh from the `specs/design/`
// tree. It drives the issue body's Component Reference card and the Local
// Developer Setup `cd <appPath>` line. Pass nil when the design entry
// can't be resolved (e.g. component already removed) — the body falls back
// to a generic shape.
//
// repoURL + repoSlug are baked into the body's Local Developer Setup
// section. The branch name is computed from the persisted task.ID via
// computeBranchName; same value the dispatch path uses, so the local-flow
// developer's `git checkout` lands on the branch the agent will eventually
// push to.
func (s *taskService) ensureIssueForTask(
	ctx context.Context,
	task *models.ComponentTask,
	comp *models.DesignComponent,
	repoURL, repoSlug string,
) error {
	if task.IssueURL != "" {
		return nil
	}
	if s.gitClient == nil {
		return fmt.Errorf("git client not configured")
	}

	// SNAPSHOT — freeze the project's currently-attached skills' bodies
	// at (project_id, design_version) so the dispatched agent's
	// workspace materialises the same content the tech-lead used. Idempotent:
	// no-op when a snapshot already exists for the key. Best-effort —
	// failures log but don't block issue creation, so a missing snapshot
	// just produces an empty preload set on dispatch (same effective
	// behaviour as today).
	if s.skillSvc != nil && task.SourceDesignVersion != "" {
		if err := snapshotProjectSkills(ctx, s.db, s.store, s.skillSvc, task.OrgID, task.ProjectID, task.SourceDesignVersion); err != nil {
			slog.WarnContext(ctx, "ensureIssueForTask: skill snapshot failed — continuing",
				"task", task.ID, "designVersion", task.SourceDesignVersion, "error", err)
		}
	}

	// The agent owns branch + PR creation, so the issue body intentionally
	// doesn't pre-name a branch. BranchName is filled in later by the
	// pull_request.opened webhook handler when the agent opens its PR.

	issue, err := s.gitClient.CreateIssue(ctx, task.OrgID, task.ProjectID, &gitservice.CreateIssueRequest{
		Title:  issueTitle(task),
		Body:   buildIssueBody(task, comp, repoURL, repoSlug),
		Labels: []string{"asdlc", "implementation"},
	})
	if err != nil {
		return err
	}

	task.IssueURL = issue.URL
	task.IssueNumber = issue.Number
	task.LifecycleStatus = string(models.TaskLifecycleGhIssueCreated)
	if err := s.taskRepo.Update(ctx, task); err != nil {
		return fmt.Errorf("persist issue metadata: %w", err)
	}

	slog.InfoContext(ctx, "created github issue for task",
		"task", task.ID, "component", task.ComponentName, "issue", issue.URL)
	return nil
}

// toK8sName is defined in design_service.go (same package).
