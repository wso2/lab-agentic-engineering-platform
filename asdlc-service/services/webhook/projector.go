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
type Projector struct {
	db *gorm.DB
}

func NewProjector(db *gorm.DB) *Projector {
	return &Projector{db: db}
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

// AdvanceMergedTasksForPush advances all tasks in a project whose
// MergeCommitSHA appears in the given push's commit list. Used by the push
// handler to flip merged → building when the push that included a merge
// commit arrives. Project-scoped lock keeps this serialised against
// concurrent pushes for the same project.
//
// commitSHAs includes head_commit.id plus every commits[].id from the push
// payload. Empty MergeCommitSHA is matched separately by the caller using
// the PR head ref (the §8.3 backfill case) — that lookup happens before
// this function is invoked.
func (p *Projector) AdvanceMergedTasksForPush(
	ctx context.Context,
	projectID string,
	commitSHAs []string,
) (int, error) {
	if len(commitSHAs) == 0 {
		return 0, nil
	}
	advanced := 0
	err := p.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := acquireProjectLock(tx, projectID); err != nil {
			return err
		}
		var tasks []models.ComponentTask
		if err := tx.
			Where("project_id = ? AND status = ? AND merge_commit_sha IN ?",
				projectID, string(models.TaskStatusMerged), commitSHAs).
			Find(&tasks).Error; err != nil {
			return fmt.Errorf("scan merged tasks: %w", err)
		}
		now := time.Now().UTC()
		for i := range tasks {
			next, err := services.ApplyTaskEvent(models.TaskStatus(tasks[i].Status), services.TaskEventPushMatched)
			if err != nil {
				continue
			}
			tasks[i].Status = string(next)
			tasks[i].LastEventAt = &now
			if err := tx.Save(&tasks[i]).Error; err != nil {
				return fmt.Errorf("save task %s: %w", tasks[i].ID, err)
			}
			advanced++
		}
		return nil
	})
	return advanced, err
}

// MarkBuilding records the WorkflowRun name on the task when the push
// handler creates the build. Decoupled from AdvanceMergedTasksForPush so
// the build watcher can resume polling on restart by reading
// LastBuildRunName + LastBuildSHA.
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

// ApplyBuildResult advances a task from building → deployed/failed.
func (p *Projector) ApplyBuildResult(
	ctx context.Context,
	taskID string,
	event services.TaskEvent,
	errMsg string,
) error {
	return p.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
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
		return tx.Save(&task).Error
	})
}

// errTaskNotFound is a sentinel for "no task with this PR number" so callers
// can distinguish from real DB errors.
type errTaskNotFound struct {
	prNumber     int
	repoFullName string
}

func (e errTaskNotFound) Error() string {
	if e.repoFullName != "" {
		return fmt.Sprintf("no task for PR #%d in repo %s", e.prNumber, e.repoFullName)
	}
	return fmt.Sprintf("no task for PR #%d", e.prNumber)
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
