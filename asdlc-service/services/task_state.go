package services

import (
	"errors"

	"github.com/wso2/asdlc/asdlc-service/models"
)

// TaskEvent identifies a transition trigger. Mirrors the names used by the
// webhook projector and build watcher.
type TaskEvent string

const (
	TaskEventDispatchSuccess TaskEvent = "dispatch.success"
	TaskEventPRReady         TaskEvent = "pr.ready_for_review"
	TaskEventPRMerged        TaskEvent = "pr.merged"
	TaskEventPRRejected      TaskEvent = "pr.rejected"
	TaskEventPushMatched     TaskEvent = "push.matched"
	TaskEventBuildSucceeded  TaskEvent = "build.succeeded"
	TaskEventBuildFailed     TaskEvent = "build.failed"
	// Phase 2 PR B / PR D — org-disconnect and reach-reconciliation cascades.
	// Both targets are TaskStatusAbandoned; the events differ so the
	// projector can record a distinct cause for audit.
	TaskEventOrgDisconnected TaskEvent = "org.disconnected"
	TaskEventRepoUnselected  TaskEvent = "repo.unselected"
	// Phase 2 PR D — git-clone auth-failure retry budget exhausted (§9.3).
	// Drives building → failed; cause column gets "build.auth_retry_exceeded".
	TaskEventBuildAuthRetryExhausted TaskEvent = "build.auth_retry_exceeded"
	// Coding-agent WorkflowRun terminated Failed/Error. Drives in_progress → failed.
	// Emitted by services/webhook/coding_agent_watcher.go on terminal failure
	// of the per-task `app-factory-coding-agent` WorkflowRun.
	TaskEventCodingAgentFailed TaskEvent = "coding_agent.failed"
)

// EventCause maps a TaskEvent to the value written into ComponentTask.Cause
// on a successful terminal transition. The mapping is single-source so
// callers don't fork the lookup. Empty string for events that are not
// terminal (e.g. dispatch.success, push.matched).
func EventCause(event TaskEvent) string {
	switch event {
	case TaskEventBuildSucceeded:
		return "build.deployed"
	case TaskEventBuildFailed:
		return "build.failed"
	case TaskEventBuildAuthRetryExhausted:
		return "build.auth_retry_exceeded"
	case TaskEventCodingAgentFailed:
		return "coding_agent.failed"
	case TaskEventPRRejected:
		return "pr.rejected"
	case TaskEventOrgDisconnected:
		// Generic; specific reasons (manual.disconnect, validator.unauthorized,
		// installation.deleted) are passed through OrgDisconnectService's
		// `cause` parameter and override this default at the call site.
		return "org.disconnected"
	case TaskEventRepoUnselected:
		return "repo.unselected"
	default:
		return ""
	}
}

// stateTransition is one row of the transition table.
type stateTransition struct {
	From  models.TaskStatus
	To    models.TaskStatus
	Event TaskEvent
}

// allowedTransitions is the source of truth for the lifecycle. Rejected
// transitions panic in tests via TaskState.Apply (returning ErrInvalidTransition).
//
// The shape mirrors github-integration-phase0.md §4.6:
//
//   pending → in_progress → ready_for_review → merged → building → deployed
//                                            ↘ rejected
//                       (any) ↘ failed (build)
var allowedTransitions = []stateTransition{
	{models.TaskStatusPending, models.TaskStatusInProgress, TaskEventDispatchSuccess},
	{models.TaskStatusInProgress, models.TaskStatusReadyForReview, TaskEventPRReady},
	{models.TaskStatusReadyForReview, models.TaskStatusMerged, TaskEventPRMerged},
	{models.TaskStatusReadyForReview, models.TaskStatusRejected, TaskEventPRRejected},
	{models.TaskStatusInProgress, models.TaskStatusRejected, TaskEventPRRejected},
	{models.TaskStatusMerged, models.TaskStatusBuilding, TaskEventPushMatched},
	{models.TaskStatusBuilding, models.TaskStatusDeployed, TaskEventBuildSucceeded},
	{models.TaskStatusBuilding, models.TaskStatusFailed, TaskEventBuildFailed},
	// Phase 2 PR B — org disconnect cascade. All non-terminal statuses
	// transition to abandoned. (Once a task is in 'building', the build
	// will run to completion under whatever credential it captured —
	// abandoning would leak a half-built run.)
	{models.TaskStatusPending, models.TaskStatusAbandoned, TaskEventOrgDisconnected},
	{models.TaskStatusInProgress, models.TaskStatusAbandoned, TaskEventOrgDisconnected},
	{models.TaskStatusReadyForReview, models.TaskStatusAbandoned, TaskEventOrgDisconnected},
	// Phase 2 PR D — reach-reconciliation cascade (App mode only,
	// installation_repositories.removed). PR B stages the wiring; PR D
	// adds the actual cascade trigger.
	{models.TaskStatusPending, models.TaskStatusAbandoned, TaskEventRepoUnselected},
	{models.TaskStatusInProgress, models.TaskStatusAbandoned, TaskEventRepoUnselected},
	{models.TaskStatusReadyForReview, models.TaskStatusAbandoned, TaskEventRepoUnselected},
	// Phase 2 PR D — build watcher auth-retry budget exhausted (§9.3).
	// Stays in the building → failed lane (the auth-retry events that did
	// not exhaust the budget loop the watcher without changing status).
	{models.TaskStatusBuilding, models.TaskStatusFailed, TaskEventBuildAuthRetryExhausted},
	// Coding-agent WorkflowRun terminated Failed/Error. Drives the task to
	// terminal `failed` so the operator sees the dispatch never produced a
	// PR-ready state. The webhook path (pr.ready_for_review) is preferred
	// when both fire — first-write-wins on the projector.
	{models.TaskStatusInProgress, models.TaskStatusFailed, TaskEventCodingAgentFailed},
}

// ErrInvalidTransition is returned by Apply when the current status doesn't
// allow the requested event. Terminal-state late events return this error;
// the projector treats it as a no-op (logged and ignored).
var ErrInvalidTransition = errors.New("invalid task state transition")

// ApplyTaskEvent computes the next status given the current state and an
// event. Returns ErrInvalidTransition if the transition isn't in the table.
//
// This is a pure function — no I/O, no logging. Callers that need to react
// to invalid transitions should check errors.Is(err, ErrInvalidTransition)
// and decide whether to log or ignore.
func ApplyTaskEvent(current models.TaskStatus, event TaskEvent) (models.TaskStatus, error) {
	if current.IsTerminal() {
		return current, ErrInvalidTransition
	}
	for _, t := range allowedTransitions {
		if t.From == current && t.Event == event {
			return t.To, nil
		}
	}
	return current, ErrInvalidTransition
}
