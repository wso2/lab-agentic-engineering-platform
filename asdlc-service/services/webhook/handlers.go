package webhook

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
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
	router.Register("pull_request", "edited", EventHandlerFunc(h.PullRequestEdited))
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
		Number         int    `json:"number"`
		HTMLURL        string `json:"html_url"`
		Title          string `json:"title"`
		Body           string `json:"body"`
		Draft          bool   `json:"draft"`
		Merged         bool   `json:"merged"`
		MergeCommitSHA string `json:"merge_commit_sha"`
		Head           struct {
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

// closesIssueRefRE matches GitHub's closing-keyword forms in a PR body:
//
//	close|closes|closed|fix|fixes|fixed|resolve|resolves|resolved
//
// followed by `#<n>` (same-repo). Cross-repo (`org/repo#N`) is intentionally
// not matched — that would link a PR in repo A to an issue in repo B, which
// isn't the platform's contract.
var closesIssueRefRE = regexp.MustCompile(
	`(?i)\b(?:close[sd]?|fix(?:e[sd])?|resolve[sd]?)\s*:?\s*#(\d+)\b`,
)

// parseClosesIssueRef returns the first issue number referenced by a
// closing keyword in `body`, or 0 if none.
func parseClosesIssueRef(body string) int {
	m := closesIssueRefRE.FindStringSubmatch(body)
	if len(m) < 2 {
		return 0
	}
	var n int
	if _, err := fmt.Sscanf(m[1], "%d", &n); err != nil {
		return 0
	}
	return n
}

// PullRequestOpened links the PR to its task by parsing `Closes #N` from
// the PR body. The agent owns branch+PR creation, so this is the platform's
// only handle on which task a PR belongs to.
//
// If the PR body has no `Closes #N`, the PR is assumed unrelated to a task
// and silently ignored — a `pull_request.edited` event later may still
// link it if the agent or a human adds the closing keyword.
func (h *Handler) PullRequestOpened(ctx context.Context, event, action string, body []byte) error {
	return h.linkPRFromPayload(ctx, body, "opened")
}

// PullRequestEdited re-runs the link logic when the PR body or title
// changes. Catches the case where the agent opens the PR without
// `Closes #N` and then adds it in a follow-up edit.
func (h *Handler) PullRequestEdited(ctx context.Context, event, action string, body []byte) error {
	return h.linkPRFromPayload(ctx, body, "edited")
}

// linkPRFromPayload parses the PR body for `Closes #N`, looks up the task
// by issue number, and persists PR fields. If the PR is open and not a
// draft, also fires `TaskEventPRReady` so the task transitions out of
// `in_progress`. Idempotent — re-running on an already-linked task is a
// no-op for the link, and `ApplyTaskEvent` absorbs late events on
// terminal states.
func (h *Handler) linkPRFromPayload(ctx context.Context, body []byte, action string) error {
	var p pullRequestPayload
	if err := json.Unmarshal(body, &p); err != nil {
		return fmt.Errorf("parse pull_request payload: %w", err)
	}
	if p.PullRequest.Number == 0 {
		return nil
	}
	issueNum := parseClosesIssueRef(p.PullRequest.Body)
	if issueNum == 0 {
		slog.DebugContext(ctx, "pull_request: no Closes #N in body — ignoring",
			"action", action, "pr", p.PullRequest.Number, "repo", p.Repository.FullName)
		return nil
	}

	prFields := func(t *models.ComponentTask) {
		t.PullRequestNumber = p.PullRequest.Number
		t.PullRequestURL = p.PullRequest.HTMLURL
		t.BranchName = p.PullRequest.Head.Ref
	}

	// Always persist the PR linkage. Then, if the PR is open and not a
	// draft, advance the lifecycle through the projector. A draft PR
	// signals the agent isn't done — the `pull_request.ready_for_review`
	// event will fire that transition later.
	if err := h.projector.LinkTaskByIssue(ctx, p.Repository.FullName, issueNum, prFields); err != nil {
		if IsTaskNotFound(err) {
			slog.DebugContext(ctx, "pull_request: no matching task for Closes #N",
				"action", action, "issue", issueNum, "pr", p.PullRequest.Number, "repo", p.Repository.FullName)
			return nil
		}
		return fmt.Errorf("link task by issue: %w", err)
	}

	if !p.PullRequest.Draft {
		err := h.projector.ApplyToTaskByPR(
			ctx, p.Repository.FullName, p.PullRequest.Number, services.TaskEventPRReady, nil,
		)
		if IsTaskNotFound(err) {
			// Race: link succeeded above, but the row was renamed/deleted
			// between transactions. Don't fail the webhook delivery.
			return nil
		}
		return err
	}
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
	// find a row in project_default_pushes and can dispatch the build now.
	// Look up the task fresh — scoped to this repo's project so stale tasks
	// from unrelated projects (same PR number) don't bleed in.
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
	// Push already processed — dispatch the build now. DispatchTaskBuild
	// is idempotent on (LastBuildSHA, LastBuildRunName) and atomically
	// transitions merged → building via projector.MarkBuilding.
	if h.wfService != nil {
		if _, derr := h.wfService.DispatchTaskBuild(ctx, &task, mergeSHA); derr != nil {
			slog.WarnContext(ctx, "dispatch build at merge rendezvous failed",
				"task", task.ID, "sha", mergeSHA, "error", derr)
		}
	}

	// Under F2 deploy-gating, pending_deps re-evaluation is driven by the
	// dep's deploy event (services/webhook/projector.go::onTaskDeployed),
	// not by PR merge. The merge handler no longer needs to touch siblings.
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

	// State transition (merged → building) is performed atomically inside
	// TriggerForPush via projector.MarkBuilding when a build is dispatched.
	// AdvanceMergedTasksForPush is no longer wired here — keeping it would
	// be a no-op given its LastBuildRunName guard, but routing the
	// transition through one path keeps the invariant easy to reason about
	// ("a task in `building` always has a WorkflowRun").
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
