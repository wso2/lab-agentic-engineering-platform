package webhook

import (
	"context"
	"errors"
	"fmt"
	"hash/fnv"
	"log/slog"
	"time"

	"gorm.io/gorm"

	"github.com/wso2/asdlc/asdlc-service/models"
	"github.com/wso2/asdlc/asdlc-service/services"
)

// Projector applies derived events to ComponentTask state under per-task
// advisory locks. It is the only writer of ComponentTask.Status outside the
// dispatch path — the §11 invariant that "state transitions are declarative"
// is enforced here by routing every transition through ApplyTaskEvent.
//
// Locking: each transition takes a transaction-scoped advisory lock keyed
// on the task ID, so two concurrent webhook handlers that resolve to the
// same task serialise. Phase 0 single-replica BFF inherits this scheme
// unchanged when §9.3 (multi-replica hardening) lands — Postgres advisory
// locks are cluster-wide.
//
// DispatchHook (injected via SetDispatchHook) is invoked post-commit
// whenever a task transitions into `deployed` so dependents in
// `on_hold` siblings get auto-dispatched. See docs/design/cross-component-
// wiring-gaps.md §3 F1.
type Projector struct {
	db           *gorm.DB
	dispatchHook DispatchHook
}

// DispatchHook is the post-commit callback fired by the projector when a
// task transitions into a state that should unblock dependents. The hook
// implementation owns the cascade (eligibility scan + DispatchService
// call); the projector only owns the trigger. Implemented by
// services.DispatchCascadeHook.
type DispatchHook interface {
	OnTaskDeployed(ctx context.Context, orgID, projectID, componentName string)
}

func NewProjector(db *gorm.DB) *Projector {
	return &Projector{db: db}
}

// SetDispatchHook wires the post-commit dispatch cascade. Called once
// from main during service composition; nil is treated as "no cascade",
// preserving the legacy behaviour for callers that haven't wired it.
func (p *Projector) SetDispatchHook(h DispatchHook) {
	p.dispatchHook = h
}

// ApplyToTaskByPR locks the task whose PullRequestNumber matches prNumber
// in the repo identified by repoFullName, and advances its state via the
// given event. Returns ErrTaskNotFound when no platform task matches (a
// human-opened PR, or a PR number reused across projects).
//
// repoFullName scopes the lookup to one repository — PR numbers are unique
// per repo on GitHub but the BFF holds tasks for many repos, so an unscoped
// lookup can absorb a webhook into a stale task from a different project
// that happens to share the PR number.
//
// fillFields is called inside the transaction *before* state advance, so
// per-event mutations (set MergeCommitSHA on pr.merged) ride the same write.
func (p *Projector) ApplyToTaskByPR(
	ctx context.Context,
	repoFullName string,
	prNumber int,
	event services.TaskEvent,
	fillFields func(t *models.ComponentTask),
) error {
	if prNumber <= 0 {
		return fmt.Errorf("invalid PR number")
	}
	if repoFullName == "" {
		return fmt.Errorf("repoFullName is required")
	}
	return p.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// Resolve repo_full_name → project_id via git_repositories. The repo
		// row is the authority here; without it the PR number is ambiguous.
		projectID, err := lookupProjectByRepo(tx, repoFullName)
		if err != nil {
			return fmt.Errorf("resolve project for repo %q: %w", repoFullName, err)
		}
		if projectID == "" {
			return errTaskNotFound{prNumber: prNumber, repoFullName: repoFullName}
		}

		// Find the task scoped to (project, PR number). We use FOR UPDATE in
		// case this is called concurrently against the same task; combined
		// with the advisory lock below this is belt-and-suspenders.
		var task models.ComponentTask
		err = tx.Where("project_id = ? AND pull_request_number = ?", projectID, prNumber).
			Clauses().
			First(&task).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return errTaskNotFound{prNumber: prNumber, repoFullName: repoFullName}
		}
		if err != nil {
			return fmt.Errorf("find task by PR: %w", err)
		}
		if err := acquireTaskLock(tx, task.ID); err != nil {
			return err
		}

		// Re-read under the lock — guards against the race where another
		// handler advanced state between First() and lock acquisition.
		if err := tx.First(&task, "id = ?", task.ID).Error; err != nil {
			return fmt.Errorf("re-read task: %w", err)
		}

		if fillFields != nil {
			fillFields(&task)
		}

		next, err := services.ApplyTaskEvent(models.TaskStatus(task.Status), event)
		if err != nil {
			if errors.Is(err, services.ErrInvalidTransition) {
				slog.InfoContext(ctx, "projector: late event ignored",
					"task", task.ID, "current", task.Status, "event", event)
				// Late events on terminal states are absorbed silently.
				// Still persist any fillFields changes.
				now := time.Now().UTC()
				task.LastEventAt = &now
				return tx.Save(&task).Error
			}
			return err
		}
		task.Status = string(next)
		now := time.Now().UTC()
		task.LastEventAt = &now
		setCauseIfTerminal(&task, event)
		return tx.Save(&task).Error
	})
}

// setCauseIfTerminal writes ComponentTask.Cause when the new status is
// terminal and EventCause has a defined value for the event. Non-terminal
// transitions leave Cause unchanged. Re-applying the same event on a
// row already terminal is a no-op (the projector returns early).
func setCauseIfTerminal(task *models.ComponentTask, event services.TaskEvent) {
	if !models.TaskStatus(task.Status).IsTerminal() {
		return
	}
	cause := services.EventCause(event)
	if cause == "" {
		return
	}
	task.Cause = &cause
}

// MarkBuilding records the WorkflowRun name on the task when DispatchTaskBuild
// fires from the pr.closed handler, and atomically transitions
// merged → building in the same transaction. This is the only path that
// creates a `building` task.
func (p *Projector) MarkBuilding(
	ctx context.Context,
	taskID, sha, runName string,
) error {
	return p.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := acquireTaskLock(tx, taskID); err != nil {
			return err
		}
		var task models.ComponentTask
		if err := tx.First(&task, "id = ?", taskID).Error; err != nil {
			return fmt.Errorf("find task: %w", err)
		}
		task.LastBuildSHA = sha
		task.LastBuildRunName = runName
		now := time.Now().UTC()
		task.LastEventAt = &now
		if task.Status == string(models.TaskStatusMerged) {
			task.Status = string(models.TaskStatusBuilding)
		}
		return tx.Save(&task).Error
	})
}

// ApplyBuildResult applies a terminal/lifecycle event to a task: writes the
// resulting status, optional errMsg, and cause inside a per-task advisory
// lock. Used for any non-PR transition driven by build state — the build
// watcher (building → deployed/failed) and the dispatch path
// (merged → failed on TaskEventBuildPathMismatch).
//
// Post-commit, if the transition landed the task in `deployed` and a
// dispatch hook is wired, fire it asynchronously so dependents in
// `on_hold` siblings get re-evaluated + dispatched. The hook owns its own
// per-project advisory lock; firing here just kicks off the cascade.
func (p *Projector) ApplyBuildResult(
	ctx context.Context,
	taskID string,
	event services.TaskEvent,
	errMsg string,
) error {
	var (
		landedDeployed bool
		orgID          string
		projectID      string
		componentName  string
	)
	err := p.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := acquireTaskLock(tx, taskID); err != nil {
			return err
		}
		var task models.ComponentTask
		if err := tx.First(&task, "id = ?", taskID).Error; err != nil {
			return fmt.Errorf("find task: %w", err)
		}
		next, err := services.ApplyTaskEvent(models.TaskStatus(task.Status), event)
		if err != nil {
			if errors.Is(err, services.ErrInvalidTransition) {
				slog.InfoContext(ctx, "projector: late build result ignored",
					"task", taskID, "current", task.Status, "event", event)
				return nil
			}
			return err
		}
		task.Status = string(next)
		if errMsg != "" {
			task.ErrorMessage = errMsg
		}
		now := time.Now().UTC()
		task.LastEventAt = &now
		setCauseIfTerminal(&task, event)
		if next == models.TaskStatusDeployed {
			landedDeployed = true
			orgID = task.OrgID
			projectID = task.ProjectID
			componentName = task.ComponentName
		}
		return tx.Save(&task).Error
	})
	if err != nil {
		return err
	}
	if landedDeployed && p.dispatchHook != nil {
		// Fire-and-forget; the hook owns its own context lifecycle. We
		// pass a detached context so the watcher's tick deadline doesn't
		// cancel a cascade that may itself need to create WorkflowRuns.
		hookCtx := context.WithoutCancel(ctx)
		go p.dispatchHook.OnTaskDeployed(hookCtx, orgID, projectID, componentName)
	}
	return nil
}

// LinkTaskByIssue persists per-PR fields (PullRequestNumber,
// PullRequestURL, BranchName, …) on the task identified by the
// (repoFullName, issueNumber) tuple, under the same per-task advisory
// lock used by ApplyToTaskByPR. No state transition — callers chain
// ApplyToTaskByPR after to advance the lifecycle. Returns
// errTaskNotFound when no task matches the issue (a human-opened PR
// citing an unrelated issue, or a stale repo row).
//
// fillFields runs inside the transaction; it MUST only mutate columns
// that are safe to write without going through ApplyTaskEvent
// (PullRequestNumber, PullRequestURL, BranchName).
func (p *Projector) LinkTaskByIssue(
	ctx context.Context,
	repoFullName string,
	issueNumber int,
	fillFields func(t *models.ComponentTask),
) error {
	if issueNumber <= 0 {
		return fmt.Errorf("invalid issue number")
	}
	if repoFullName == "" {
		return fmt.Errorf("repoFullName is required")
	}
	return p.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		projectID, err := lookupProjectByRepo(tx, repoFullName)
		if err != nil {
			return fmt.Errorf("resolve project for repo %q: %w", repoFullName, err)
		}
		if projectID == "" {
			return errTaskNotFound{issueNumber: issueNumber, repoFullName: repoFullName}
		}

		var task models.ComponentTask
		err = tx.Where("project_id = ? AND issue_number = ?", projectID, issueNumber).
			First(&task).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return errTaskNotFound{issueNumber: issueNumber, repoFullName: repoFullName}
		}
		if err != nil {
			return fmt.Errorf("find task by issue: %w", err)
		}
		if err := acquireTaskLock(tx, task.ID); err != nil {
			return err
		}
		// Re-read under the lock — same belt-and-suspenders pattern used
		// by ApplyToTaskByPR.
		if err := tx.First(&task, "id = ?", task.ID).Error; err != nil {
			return fmt.Errorf("re-read task: %w", err)
		}
		if fillFields != nil {
			fillFields(&task)
		}
		now := time.Now().UTC()
		task.LastEventAt = &now
		return tx.Save(&task).Error
	})
}

// errTaskNotFound is a sentinel for "no task with this PR number" (or
// issue number, when set via LinkTaskByIssue) so callers can distinguish
// from real DB errors.
type errTaskNotFound struct {
	prNumber     int
	issueNumber  int
	repoFullName string
}

func (e errTaskNotFound) Error() string {
	switch {
	case e.issueNumber > 0 && e.repoFullName != "":
		return fmt.Sprintf("no task for issue #%d in repo %s", e.issueNumber, e.repoFullName)
	case e.issueNumber > 0:
		return fmt.Sprintf("no task for issue #%d", e.issueNumber)
	case e.repoFullName != "":
		return fmt.Sprintf("no task for PR #%d in repo %s", e.prNumber, e.repoFullName)
	default:
		return fmt.Sprintf("no task for PR #%d", e.prNumber)
	}
}

// IsTaskNotFound reports whether err comes from ApplyToTaskByPR with no
// matching PR — a human-opened PR or a state mismatch we should ignore.
func IsTaskNotFound(err error) bool {
	var t errTaskNotFound
	return errors.As(err, &t)
}

// acquireTaskLock takes a transaction-scoped Postgres advisory lock keyed
// on hash(taskID). Blocks until acquired; released on commit/rollback.
func acquireTaskLock(tx *gorm.DB, taskID string) error {
	return tx.Exec(`SELECT pg_advisory_xact_lock(?)`, hashKey("task:"+taskID)).Error
}

// acquireProjectLock takes a project-scoped advisory lock so concurrent
// pushes to the same project's default branch serialise.
func acquireProjectLock(tx *gorm.DB, projectID string) error {
	return tx.Exec(`SELECT pg_advisory_xact_lock(?)`, hashKey("project:"+projectID)).Error
}

func hashKey(s string) int64 {
	h := fnv.New64a()
	h.Write([]byte(s))
	return int64(h.Sum64()) //nolint:gosec
}
