package webhook

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"gorm.io/gorm"

	"github.com/wso2/asdlc/asdlc-service/models"
	"github.com/wso2/asdlc/asdlc-service/services"
)

// Handlers wires the per-event handlers onto a Router. The shape is one
// constructor that registers every (event, action) tuple at once, so the
// caller doesn't need to know the routing table.
//
// State transitions go through the projector; build triggers go through the
// WorkflowRunService. Cross-handler rendezvous (push before pr.merged or
// vice-versa) goes through project_default_pushes.
func Register(
	router *Router,
	db *gorm.DB,
	projector *Projector,
	wfService services.WorkflowRunService,
) {
	h := &Handler{
		db:        db,
		projector: projector,
		wfService: wfService,
	}
	router.Register("pull_request", "opened", EventHandlerFunc(h.PullRequestOpened))
	router.Register("pull_request", "reopened", EventHandlerFunc(h.PullRequestReopened))
	router.Register("pull_request", "ready_for_review", EventHandlerFunc(h.PullRequestReady))
	router.Register("pull_request", "closed", EventHandlerFunc(h.PullRequestClosed))
	router.Register("push", "", EventHandlerFunc(h.Push))
	router.Register("issue_comment", "", EventHandlerFunc(h.IssueComment))
}

type Handler struct {
	db        *gorm.DB
	projector *Projector
	wfService services.WorkflowRunService
}

// pull_request payload subset.
type pullRequestPayload struct {
	Action      string `json:"action"`
	Number      int    `json:"number"`
	PullRequest struct {
		Number          int    `json:"number"`
		Merged          bool   `json:"merged"`
		MergeCommitSHA  string `json:"merge_commit_sha"`
		Head            struct {
			Ref string `json:"ref"`
		} `json:"head"`
		Base struct {
			Ref string `json:"ref"`
		} `json:"base"`
	} `json:"pull_request"`
	Repository struct {
		FullName string `json:"full_name"`
	} `json:"repository"`
}

func (h *Handler) PullRequestOpened(ctx context.Context, event, action string, body []byte) error {
	// We open our own PRs at dispatch — opened is a noop on our side. A
	// human-opened PR is unrelated to a task and silently ignored.
	return nil
}

func (h *Handler) PullRequestReopened(ctx context.Context, event, action string, body []byte) error {
	// Phase 0: ignore. Reopen lands as a follow-up alongside `superseded`
	// semantics.
	return nil
}

func (h *Handler) PullRequestReady(ctx context.Context, event, action string, body []byte) error {
	var p pullRequestPayload
	if err := json.Unmarshal(body, &p); err != nil {
		return fmt.Errorf("parse pull_request payload: %w", err)
	}
	if p.PullRequest.Number == 0 {
		return nil
	}
	err := h.projector.ApplyToTaskByPR(ctx, p.Repository.FullName, p.PullRequest.Number, services.TaskEventPRReady, nil)
	if IsTaskNotFound(err) {
		slog.DebugContext(ctx, "ready_for_review: no matching task — likely human PR")
		return nil
	}
	return err
}

// pushPayload subset.
type pushPayload struct {
	Ref      string `json:"ref"`
	Before   string `json:"before"`
	After    string `json:"after"`
	Repository struct {
		DefaultBranch string `json:"default_branch"`
		FullName      string `json:"full_name"`
	} `json:"repository"`
	HeadCommit struct {
		ID string `json:"id"`
	} `json:"head_commit"`
	Commits []struct {
		ID       string   `json:"id"`
		Added    []string `json:"added"`
		Modified []string `json:"modified"`
		Removed  []string `json:"removed"`
	} `json:"commits"`
}

func (h *Handler) PullRequestClosed(ctx context.Context, event, action string, body []byte) error {
	var p pullRequestPayload
	if err := json.Unmarshal(body, &p); err != nil {
		return fmt.Errorf("parse pull_request payload: %w", err)
	}
	if p.PullRequest.Number == 0 {
		return nil
	}

	if !p.PullRequest.Merged {
		err := h.projector.ApplyToTaskByPR(ctx, p.Repository.FullName, p.PullRequest.Number, services.TaskEventPRRejected, nil)
		if IsTaskNotFound(err) {
			return nil
		}
		return err
	}

	// Merged: record the merge SHA and advance.
	mergeSHA := p.PullRequest.MergeCommitSHA
	err := h.projector.ApplyToTaskByPR(ctx, p.Repository.FullName, p.PullRequest.Number, services.TaskEventPRMerged, func(t *models.ComponentTask) {
		if mergeSHA != "" {
			t.MergeCommitSHA = mergeSHA
		}
	})
	if IsTaskNotFound(err) {
		return nil
	}
	if err != nil {
		return err
	}

	// Cross-handler rendezvous: if the matching push already arrived, we'll
	// find a row in project_default_pushes and can immediately advance to
	// building. Look up the task fresh — scoped to this repo's project so
	// stale tasks from unrelated projects (same PR number) don't bleed in.
	if mergeSHA == "" {
		return nil
	}
	projectID, err := lookupProjectByRepo(h.db.WithContext(ctx), p.Repository.FullName)
	if err != nil || projectID == "" {
		return nil
	}
	var task models.ComponentTask
	if err := h.db.WithContext(ctx).
		Where("project_id = ? AND pull_request_number = ?", projectID, p.PullRequest.Number).
		First(&task).Error; err != nil {
		return nil // already handled above
	}
	var existing models.ProjectDefaultPush
	err = h.db.WithContext(ctx).
		Where("project_id = ? AND sha = ?", task.ProjectID, mergeSHA).
		First(&existing).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil // push will arrive later
		}
		return fmt.Errorf("lookup project_default_push: %w", err)
	}
	// Push already processed — advance to building immediately.
	if _, advErr := h.projector.AdvanceMergedTasksForPush(ctx, task.ProjectID, []string{mergeSHA}); advErr != nil {
		slog.WarnContext(ctx, "advance after merge rendezvous failed",
			"task", task.ID, "error", advErr)
	}

	// Tech-lead revamp §12: a merge may unblock sibling tasks in
	// pending_deps. Flip them back to pending so the next dispatch (or the
	// existing dispatch loop's re-evaluation) picks them up. This does not
	// auto-dispatch — that's handled by the existing pending re-check in
	// remote_worker_service.go::DispatchTasks. Best-effort.
	if rerr := h.reevaluatePendingDepsForProject(ctx, task.ProjectID); rerr != nil {
		slog.WarnContext(ctx, "re-evaluate pending_deps", "project", task.ProjectID, "error", rerr)
	}
	return nil
}

// reevaluatePendingDepsForProject flips pending_deps tasks to pending when
// every task they dependsOn (by title) has reached merged|building|deployed.
// Idempotent and safe to call from any merge webhook.
func (h *Handler) reevaluatePendingDepsForProject(ctx context.Context, projectID string) error {
	var tasks []models.ComponentTask
	if err := h.db.WithContext(ctx).
		Where("project_id = ?", projectID).
		Find(&tasks).Error; err != nil {
		return err
	}
	statusByTitle := make(map[string]string, len(tasks))
	for _, t := range tasks {
		statusByTitle[t.Title] = t.Status
	}
	for i := range tasks {
		t := &tasks[i]
		if t.Status != string(models.TaskStatusPendingDeps) {
			continue
		}
		blocked := false
		for _, dep := range t.TaskDependsOn {
			st, ok := statusByTitle[dep]
			if !ok {
				continue
			}
			switch st {
			case string(models.TaskStatusMerged),
				string(models.TaskStatusBuilding),
				string(models.TaskStatusDeployed):
				continue
			}
			blocked = true
			break
		}
		if blocked {
			continue
		}
		t.Status = string(models.TaskStatusPending)
		if err := h.db.WithContext(ctx).Save(t).Error; err != nil {
			slog.WarnContext(ctx, "clear pending_deps", "task", t.ID, "error", err)
		}
	}
	return nil
}

func (h *Handler) Push(ctx context.Context, event, action string, body []byte) error {
	var p pushPayload
	if err := json.Unmarshal(body, &p); err != nil {
		return fmt.Errorf("parse push payload: %w", err)
	}
	if !strings.HasPrefix(p.Ref, "refs/heads/") {
		return nil
	}
	branch := strings.TrimPrefix(p.Ref, "refs/heads/")
	if branch != p.Repository.DefaultBranch {
		// Pushes to feature branches are recorded for audit only.
		return nil
	}

	sha := p.HeadCommit.ID
	if sha == "" {
		sha = p.After
	}
	if sha == "" {
		return nil
	}

	// Resolve project from the repo full_name.
	projectID, err := lookupProjectByRepo(h.db.WithContext(ctx), p.Repository.FullName)
	if err != nil || projectID == "" {
		slog.WarnContext(ctx, "push: project not found for repo",
			"repo", p.Repository.FullName, "error", err)
		return nil
	}

	// Record the push (idempotent on (project, sha)). Used as the
	// rendezvous row when the matching pr.closed arrives later.
	if err := h.db.WithContext(ctx).
		Where("project_id = ? AND sha = ?", projectID, sha).
		Attrs(&models.ProjectDefaultPush{
			ProjectID: projectID,
			SHA:       sha,
			PushedAt:  time.Now().UTC(),
		}).
		FirstOrCreate(&models.ProjectDefaultPush{}).Error; err != nil {
		return fmt.Errorf("persist project_default_push: %w", err)
	}

	// Compute changed paths from the push payload.
	changed := changedPathsFromPush(&p)

	// Trigger builds for components matching the changed paths and whose
	// merged task is still at LastBuildSHA != sha.
	if h.wfService != nil {
		// Determine orgID for this project. Read any existing task row for
		// the project to get OrgID.
		var taskRow models.ComponentTask
		if err := h.db.WithContext(ctx).
			Where("project_id = ?", projectID).
			Order("created_at ASC").
			First(&taskRow).Error; err == nil {
			if _, terr := h.wfService.TriggerForPush(ctx, taskRow.OrgID, projectID, sha, changed); terr != nil {
				slog.WarnContext(ctx, "trigger builds for push failed", "error", terr)
			}
		}
	}

	// Advance any merged tasks whose MergeCommitSHA matches a commit in this
	// push. Project lock keeps this serialised vs. concurrent pushes.
	commitSHAs := []string{sha}
	for _, c := range p.Commits {
		if c.ID != "" {
			commitSHAs = append(commitSHAs, c.ID)
		}
	}
	if _, err := h.projector.AdvanceMergedTasksForPush(ctx, projectID, commitSHAs); err != nil {
		slog.WarnContext(ctx, "advance merged tasks for push failed", "error", err)
	}
	return nil
}

func (h *Handler) IssueComment(ctx context.Context, event, action string, body []byte) error {
	// Persisted in webhook_payloads for audit; no state effect.
	return nil
}

// lookupProjectByRepo translates a GitHub repo full_name to an ASDLC project
// ID via the git_repositories table. Pass either a request-scoped *gorm.DB
// (db.WithContext(ctx)) or an open transaction.
func lookupProjectByRepo(db *gorm.DB, repoFullName string) (string, error) {
	if repoFullName == "" {
		return "", nil
	}
	var r struct{ ProjectID string }
	err := db.Raw(`
		SELECT project_id
		FROM git_repositories
		WHERE repo_url ILIKE ? OR repo_url ILIKE ?
		LIMIT 1
	`, "%"+repoFullName, "%"+repoFullName+".git").Scan(&r).Error
	if err != nil {
		return "", err
	}
	return r.ProjectID, nil
}

// changedPathsFromPush flattens push.commits[].{added,modified,removed} into
// a unique slice. The commit list is capped by GitHub at 2048 entries; for
// pushes that exceed that or the per-commit file-list cap, the caller falls
// back to a `compare` API call (see github-integration-phase0.md §8.4 step 2).
func changedPathsFromPush(p *pushPayload) []string {
	seen := map[string]struct{}{}
	for _, c := range p.Commits {
		for _, path := range c.Added {
			seen[path] = struct{}{}
		}
		for _, path := range c.Modified {
			seen[path] = struct{}{}
		}
		for _, path := range c.Removed {
			seen[path] = struct{}{}
		}
	}
	out := make([]string, 0, len(seen))
	for path := range seen {
		out = append(out, path)
	}
	return out
}
