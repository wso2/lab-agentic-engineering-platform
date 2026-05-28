package services

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"gorm.io/gorm"

	"github.com/wso2/asdlc/asdlc-service/models"
	"github.com/wso2/asdlc/asdlc-service/repositories"
)

// ErrOrgNotFound surfaces from OrgDisconnectService when no credential
// row matches the requested ocOrgId.
var ErrOrgNotFound = errors.New("org credentials: not found")

// OrgDisconnectService runs the BFF-side disconnect cascade defined in
// phase2.md §6.7.
//
// Phase A (status flip — sub-second; runs synchronously on the request):
//   - Calls git-service's internal projection to confirm the row exists.
//   - Begins the cascade by calling git-service to flip status='disconnecting'.
//     (PR B simplification: phase D is wired separately; we use a single
//     DELETE call that does flip-then-finalize. The 'disconnecting'
//     intermediate state lives in phase2.md §6.7 but isn't load-bearing
//     for PR B because we run Phases A–D synchronously on this path.
//     PR D rebuilds the async split when the periodic validator lands.)
//
// Phase B (best-effort issue comments — async, no lock):
//   - Per task, post `gh issue comment "abandoned: org disconnected"` via
//     the existing git-service issue API. Failures are logged.
//
// Phase C (per-task projector apply):
//   - Per task, run the ApplyTaskEvent transition with cause
//     "org.disconnected" and persist the new status.
//
// Phase D (org-scoped finalize — git-service GC):
//   - DELETE /internal/credentials/orgs/{ocOrgId} on git-service. Git-service
//     marks status='disconnected' and best-effort GCs OpenBao keys.
//
// Phases A and D run synchronously; B and C run inline (the disconnect
// endpoint stays sub-second because Phase A is the only blocking step
// from the user's perspective in the dialog confirmation flow). For PR B
// we keep the wall-clock cost reasonable in dev (small number of tasks);
// PR D rebuilds the async split with a worker queue.
type OrgDisconnectService struct {
	taskRepo  repositories.TaskRepository
	db        *gorm.DB
	credSvc   *CredentialService
	issueSvc  IssueService
}

// NewOrgDisconnectService constructs the cascade orchestrator.
func NewOrgDisconnectService(
	taskRepo repositories.TaskRepository,
	db *gorm.DB,
	credSvc *CredentialService,
	issueSvc IssueService,
) *OrgDisconnectService {
	return &OrgDisconnectService{
		taskRepo: taskRepo,
		db:       db,
		credSvc:  credSvc,
		issueSvc: issueSvc,
	}
}

// Disconnect runs the cascade synchronously. `cause` is recorded on each
// cascaded task's Cause column so audit can distinguish manual disconnect
// from validator/webhook-driven cascades. Empty cause defaults to
// "org.disconnected".
//
// uninstallApp triggers Phase E (GitHub-side App uninstall via
// DELETE /app/installations/{id}) for App-mode connections. Set true for
// manual disconnects so the install on github.com is removed alongside
// the platform row — no orphans left behind. PAT-mode rows ignore the
// flag; webhook-driven cascades (installation.deleted) typically pass
// false to avoid a feedback loop.
func (s *OrgDisconnectService) Disconnect(ctx context.Context, ocOrgID, cause string, uninstallApp bool) error {
	if cause == "" {
		cause = "org.disconnected"
	}
	// Phase A — confirm the row exists. If not, return ErrOrgNotFound so
	// the controller can return 200 idempotent.
	proj, err := s.credSvc.Status(ctx, ocOrgID)
	if err != nil {
		var nfe *NotFoundError
		if errors.As(err, &nfe) {
			return ErrOrgNotFound
		}
		return fmt.Errorf("disconnect Phase A: status: %w", err)
	}
	slog.InfoContext(ctx, "disconnect: starting cascade", "ocOrgId", ocOrgID, "kind", proj.Kind, "status", proj.Status)
	if proj.Status == "disconnected" {
		// Already finalized — nothing to do.
		slog.InfoContext(ctx, "disconnect: already disconnected", "ocOrgId", ocOrgID)
		return nil
	}

	// Phase B + C — enumerate non-terminal tasks under the org. PR B keeps
	// this best-effort: the issue-comment write is allowed to fail without
	// blocking the cascade (the task still cascades to abandoned).
	tasks, err := s.taskRepo.ListNonTerminalByOrgID(ctx, ocOrgID)
	if err != nil {
		slog.ErrorContext(ctx, "disconnect: list tasks failed", "ocOrgId", ocOrgID, "error", err)
		// Continue to Phase D — the GitHub issue comments are nice-to-have,
		// but the credential teardown is load-bearing.
		tasks = nil
	}

	for i := range tasks {
		t := &tasks[i]
		// Phase B — best-effort comment.
		if t.IssueNumber > 0 && t.ProjectID != "" {
			if err := s.issueSvc.CommentIssue(ctx, t.ProjectID, t.IssueNumber, "abandoned: org disconnected"); err != nil {
				slog.WarnContext(ctx, "disconnect Phase B: comment failed", "taskId", t.ID, "error", err)
			}
		}
		// Phase C — projector apply. cause overrides the default
		// EventCause("org.disconnected") so callers can distinguish
		// manual.disconnect, validator.unauthorized, installation.deleted, etc.
		if newStatus, err := ApplyTaskEvent(models.TaskStatus(t.Status), TaskEventOrgDisconnected); err == nil {
			t.Status = string(newStatus)
			if newStatus.IsTerminal() {
				cc := cause
				t.Cause = &cc
			}
			if err := s.taskRepo.Update(ctx, t); err != nil {
				slog.ErrorContext(ctx, "disconnect Phase C: update failed", "taskId", t.ID, "error", err)
			}
		} else {
			slog.WarnContext(ctx, "disconnect Phase C: invalid transition", "taskId", t.ID, "fromStatus", t.Status, "error", err)
		}
	}

	// Phase D — finalize on git-service: status flip + OpenBao GC.
	if err := s.credSvc.Disconnect(ctx, ocOrgID); err != nil {
		var nfe *NotFoundError
		if errors.As(err, &nfe) {
			slog.InfoContext(ctx, "disconnect: already finalized during cascade", "ocOrgId", ocOrgID)
			return nil
		}
		return fmt.Errorf("disconnect Phase D: %w", err)
	}

	// Phase E — best-effort GitHub-side uninstall. App-mode only; PAT and
	// failure are silent (the platform row is gone regardless, and an
	// admin can clean up via github.com if needed).
	if uninstallApp && proj.Kind == "app-installation" {
		if err := s.credSvc.UninstallAppInstallation(ctx, ocOrgID); err != nil {
			slog.WarnContext(ctx, "disconnect Phase E: uninstall failed", "ocOrgId", ocOrgID, "error", err)
		}
	}

	slog.InfoContext(ctx, "disconnect: cascade complete", "ocOrgId", ocOrgID, "tasksAbandoned", len(tasks), "uninstallApp", uninstallApp)
	return nil
}
