package webhook

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"

	"gorm.io/gorm"

	"github.com/wso2/asdlc/asdlc-service/models"
	"github.com/wso2/asdlc/asdlc-service/repositories"
	"github.com/wso2/asdlc/asdlc-service/services"
)

// RegisterInstallationHandlers wires the Phase 2 PR B handlers for App-mode
// lifecycle events. The receiver pipeline (routing → HMAC → dispatch)
// runs in front of these unchanged.
//
// Handlers registered:
//
//   - installation.created      → no-op ack (the connect callback wins
//                                  the race; webhook recovery is "click
//                                  Connect again")
//   - installation.deleted      → trigger the disconnect cascade
//   - installation.suspend      → flip status='suspended' on the row
//   - installation.unsuspend    → flip status='active' on the row
//   - installation_repositories.added   → JSON-merge selected_repos
//   - installation_repositories.removed → JSON-merge selected_repos
//                                          (cascade lands in PR D)
func RegisterInstallationHandlers(
	router *Router,
	db *gorm.DB,
	credSvc *services.CredentialService,
	issueSvc services.IssueService,
	taskRepo repositories.TaskRepository,
	projector *Projector,
) {
	h := &installationHandler{
		db:         db,
		credSvc:    credSvc,
		issueSvc:   issueSvc,
		taskRepo:   taskRepo,
		projector:  projector,
		disconnect: services.NewOrgDisconnectService(taskRepo, db, credSvc, issueSvc),
	}
	router.Register("installation", "created", EventHandlerFunc(h.handleCreated))
	router.Register("installation", "deleted", EventHandlerFunc(h.handleDeleted))
	router.Register("installation", "suspend", EventHandlerFunc(h.handleSuspend))
	router.Register("installation", "unsuspend", EventHandlerFunc(h.handleUnsuspend))
	router.Register("installation_repositories", "added", EventHandlerFunc(h.handleReposAdded))
	router.Register("installation_repositories", "removed", EventHandlerFunc(h.handleReposRemoved))
}

type installationHandler struct {
	db         *gorm.DB
	credSvc    *services.CredentialService
	issueSvc   services.IssueService
	taskRepo   repositories.TaskRepository
	projector  *Projector
	disconnect *services.OrgDisconnectService
}

// installationPayload covers the parts of the installation /
// installation_repositories payloads we care about.
type installationPayload struct {
	Action       string `json:"action"`
	Installation struct {
		ID int64 `json:"id"`
	} `json:"installation"`
	RepositoriesAdded []struct {
		FullName string `json:"full_name"`
	} `json:"repositories_added"`
	RepositoriesRemoved []struct {
		FullName string `json:"full_name"`
	} `json:"repositories_removed"`
}

func (h *installationHandler) parse(payload []byte) (*installationPayload, error) {
	var p installationPayload
	if err := json.Unmarshal(payload, &p); err != nil {
		return nil, err
	}
	if p.Installation.ID == 0 {
		return nil, errors.New("missing installation.id")
	}
	return &p, nil
}

// handleCreated is informational only. Bindings are created exclusively
// by the connect callback flow (HandleConnectCallback), which proves
// user-OAuth admin access before writing the platform row. The webhook
// confirms an install happened on the GitHub side but does not auto-bind
// — auto-binding here would re-introduce the cross-tenant binding race
// the binding-centric refactor was designed to eliminate.
func (h *installationHandler) handleCreated(ctx context.Context, _ string, _ string, payload []byte) error {
	p, err := h.parse(payload)
	if err != nil {
		return err
	}
	slog.InfoContext(ctx, "webhook: installation.created (informational; bindings come from the connect flow)", "installationId", p.Installation.ID)
	return nil
}

// handleDeleted runs the disconnect cascade for the org bound to this
// installation. Same path as DELETE /api/v1/orgs/{ocOrgId}/github.
func (h *installationHandler) handleDeleted(ctx context.Context, _ string, _ string, payload []byte) error {
	p, err := h.parse(payload)
	if err != nil {
		return err
	}
	ocOrgID, err := h.credSvc.OrgIDByInstallationID(ctx, p.Installation.ID)
	if err != nil {
		var nfe *services.NotFoundError
		if errors.As(err, &nfe) {
			// Install never connected on our side — ack noop.
			slog.InfoContext(ctx, "webhook: installation.deleted: no matching org (ack noop)", "installationId", p.Installation.ID)
			return nil
		}
		return err
	}
	// uninstallApp=false: the install is already gone on GitHub (that's
	// why we got this webhook); calling DELETE again would 404 harmlessly
	// but adds noise. The disconnect cascade just needs the platform-side
	// row torn down.
	if err := h.disconnect.Disconnect(ctx, ocOrgID, "installation.deleted", false); err != nil {
		if errors.Is(err, services.ErrOrgNotFound) {
			return nil
		}
		return err
	}
	slog.InfoContext(ctx, "webhook: installation.deleted → cascade complete", "ocOrgId", ocOrgID, "installationId", p.Installation.ID)
	return nil
}

// handleSuspend flips the row to status='suspended'. New dispatches refuse
// (the resolver returns OrgNotActiveError); in-flight tasks remain in
// their current status.
func (h *installationHandler) handleSuspend(ctx context.Context, _ string, _ string, payload []byte) error {
	p, err := h.parse(payload)
	if err != nil {
		return err
	}
	if err := h.credSvc.SuspendInstallation(ctx, p.Installation.ID); err != nil {
		return err
	}
	slog.InfoContext(ctx, "webhook: installation.suspend", "installationId", p.Installation.ID)
	return nil
}

// handleUnsuspend flips the row back to status='active'.
func (h *installationHandler) handleUnsuspend(ctx context.Context, _ string, _ string, payload []byte) error {
	p, err := h.parse(payload)
	if err != nil {
		return err
	}
	if err := h.credSvc.UnsuspendInstallation(ctx, p.Installation.ID); err != nil {
		return err
	}
	slog.InfoContext(ctx, "webhook: installation.unsuspend", "installationId", p.Installation.ID)
	return nil
}

// handleReposAdded merges new selected_repos. Phase 2 §6.8 Phase A only
// (PR B); the cascade for "added" is a no-op (over-permissive reach is
// a soft failure — the App's actual install state is what GitHub
// enforces at API call time).
func (h *installationHandler) handleReposAdded(ctx context.Context, _ string, _ string, payload []byte) error {
	p, err := h.parse(payload)
	if err != nil {
		return err
	}
	added := make([]string, 0, len(p.RepositoriesAdded))
	for _, r := range p.RepositoriesAdded {
		if r.FullName != "" {
			added = append(added, r.FullName)
		}
	}
	if len(added) == 0 {
		return nil
	}
	if err := h.credSvc.MergeSelectedRepos(ctx, p.Installation.ID, added, nil); err != nil {
		return err
	}
	slog.InfoContext(ctx, "webhook: installation_repositories.added", "installationId", p.Installation.ID, "added", added)
	return nil
}

// handleReposRemoved runs the §6.8 two-phase cascade for
// installation_repositories.removed.
//
// Phase A (org-scoped lock): merge the removed repos into the org's
// `selected_repos` JSON via git-service. Sub-second; releases the lock
// before Phase B starts.
//
// Phase B (no org lock; per-task transactions): confirm via GitHub
// that the install no longer reaches each removed repo (mitigates a
// forged-webhook abandonment), look up tasks targeting the confirmed
// repos, and apply TaskEventRepoUnselected on each — moving them to
// `abandoned` with cause `repo.unselected`. Best-effort posts a comment
// on the task's GitHub issue.
func (h *installationHandler) handleReposRemoved(ctx context.Context, _ string, _ string, payload []byte) error {
	p, err := h.parse(payload)
	if err != nil {
		return err
	}
	removed := make([]string, 0, len(p.RepositoriesRemoved))
	for _, r := range p.RepositoriesRemoved {
		if r.FullName != "" {
			removed = append(removed, r.FullName)
		}
	}
	if len(removed) == 0 {
		return nil
	}

	// --- Phase A — JSON merge under git-service's org lock. ---
	if err := h.credSvc.MergeSelectedRepos(ctx, p.Installation.ID, nil, removed); err != nil {
		return err
	}
	slog.InfoContext(ctx, "webhook: installation_repositories.removed Phase A merged",
		"installationId", p.Installation.ID, "removed", removed)

	// --- Phase B — confirm via GitHub, then cascade. ---

	// Confirm: ask GitHub directly via git-service. The install's actual
	// repo list is the authoritative signal — a forged webhook payload
	// removing a repo that's still selected on GitHub will hit this
	// confirmation step and stop here.
	currentRepos, err := h.credSvc.ListInstallationRepos(ctx, p.Installation.ID)
	if err != nil {
		// Confirmation failed (network/GitHub transient). Skip cascade —
		// next webhook redelivery or the periodic validator catches it.
		slog.WarnContext(ctx, "webhook: reach reconciliation Phase B confirm failed; skipping cascade",
			"installationId", p.Installation.ID, "error", err)
		return nil
	}
	stillSelected := make(map[string]struct{}, len(currentRepos))
	for _, r := range currentRepos {
		stillSelected[r] = struct{}{}
	}
	confirmed := make([]string, 0, len(removed))
	for _, r := range removed {
		if _, ok := stillSelected[r]; !ok {
			confirmed = append(confirmed, r)
		}
	}
	if len(confirmed) == 0 {
		slog.InfoContext(ctx, "webhook: reach reconciliation Phase B no confirmed removals (forged event or already re-added)",
			"installationId", p.Installation.ID, "claimed", removed)
		return nil
	}

	// Resolve removed repo full_names → project IDs via git_repositories.
	projectIDs := make([]string, 0, len(confirmed))
	repoByProject := make(map[string]string, len(confirmed))
	for _, r := range confirmed {
		pid, err := lookupProjectByRepo(h.db.WithContext(ctx), r)
		if err != nil || pid == "" {
			// No project for this repo on our side — nothing to cascade.
			continue
		}
		projectIDs = append(projectIDs, pid)
		repoByProject[pid] = r
	}
	if len(projectIDs) == 0 {
		slog.InfoContext(ctx, "webhook: reach reconciliation Phase B confirmed but no matching projects",
			"installationId", p.Installation.ID, "confirmed", confirmed)
		return nil
	}

	// List non-terminal tasks under the affected projects. The org-scoped
	// lock has been released; per-task locks are acquired by the projector.
	var tasks []models.ComponentTask
	terminal := []string{
		string(models.TaskStatusDeployed),
		string(models.TaskStatusRejected),
		string(models.TaskStatusFailed),
		string(models.TaskStatusAbandoned),
	}
	if err := h.db.WithContext(ctx).
		Where("project_id IN ? AND status NOT IN ?", projectIDs, terminal).
		Find(&tasks).Error; err != nil {
		slog.ErrorContext(ctx, "webhook: reach reconciliation Phase B list tasks failed",
			"installationId", p.Installation.ID, "error", err)
		return nil
	}
	if len(tasks) == 0 {
		slog.InfoContext(ctx, "webhook: reach reconciliation Phase B no in-flight tasks",
			"installationId", p.Installation.ID, "confirmed", confirmed)
		return nil
	}

	// Cascade per task — comment best-effort, then projector apply.
	abandoned := 0
	for i := range tasks {
		t := &tasks[i]
		if t.IssueNumber > 0 && t.ProjectID != "" {
			if err := h.issueSvc.CommentIssue(ctx, t.ProjectID, t.IssueNumber, "abandoned: repo unselected on GitHub App install"); err != nil {
				slog.WarnContext(ctx, "reach reconciliation: comment failed", "taskId", t.ID, "error", err)
			}
		}
		next, err := services.ApplyTaskEvent(models.TaskStatus(t.Status), services.TaskEventRepoUnselected)
		if err != nil {
			slog.WarnContext(ctx, "reach reconciliation: invalid transition (race with terminal)",
				"taskId", t.ID, "fromStatus", t.Status, "error", err)
			continue
		}
		t.Status = string(next)
		if next.IsTerminal() {
			cause := "repo.unselected"
			t.Cause = &cause
		}
		if err := h.taskRepo.Update(ctx, t); err != nil {
			slog.ErrorContext(ctx, "reach reconciliation: update failed", "taskId", t.ID, "error", err)
			continue
		}
		abandoned++
	}
	slog.InfoContext(ctx, "webhook: reach reconciliation Phase B cascade complete",
		"installationId", p.Installation.ID, "confirmed", confirmed, "tasksAbandoned", abandoned)
	return nil
}
