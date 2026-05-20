package models

import "time"

// StringSlice is a reusable slice type for JSONB storage in PostgreSQL.
type StringSlice []string

// TaskLifecycleStatus tracks the GitHub issue creation phase for a ComponentTask.
type TaskLifecycleStatus string

const (
	// TaskLifecycleGhIssueWaiting is the default — the task row exists but the
	// GitHub issue has not been created yet (issue creation is in flight).
	TaskLifecycleGhIssueWaiting TaskLifecycleStatus = "gh_issue_waiting"
	// TaskLifecycleGhIssueSyncing is a response-only value — never written to DB.
	// GetBoard returns this when a gh_issue_created task's issue is not yet
	// visible on the GitHub Project board (fallback path, board has 0 items).
	TaskLifecycleGhIssueSyncing TaskLifecycleStatus = "gh_issue_syncing"
	// TaskLifecycleGhIssueCreated is set once the GitHub issue is successfully
	// created and the issue URL + number are persisted.
	TaskLifecycleGhIssueCreated TaskLifecycleStatus = "gh_issue_created"
	// TaskLifecycleGhIssueFailed is set when GitHub issue creation fails after
	// the issue goroutine exhausts its attempts.
	TaskLifecycleGhIssueFailed TaskLifecycleStatus = "gh_issue_failed"
)

// TaskStatus is the single, webhook-driven lifecycle for a ComponentTask.
// Transitions live in services/task_state.go; the projector in services/webhook
// is the only writer outside dispatch.
type TaskStatus string

const (
	TaskStatusPending        TaskStatus = "pending"
	TaskStatusInProgress     TaskStatus = "in_progress"
	TaskStatusReadyForReview TaskStatus = "ready_for_review"
	TaskStatusMerged         TaskStatus = "merged"
	TaskStatusBuilding       TaskStatus = "building"
	TaskStatusDeployed       TaskStatus = "deployed"
	TaskStatusRejected       TaskStatus = "rejected"
	TaskStatusFailed         TaskStatus = "failed"
	// TaskStatusAbandoned (Phase 2 PR B) is the cascade target when the
	// org's GitHub credential is disconnected (or, in PR D, when reach
	// reconciliation drops the task's repo from the App install). Terminal.
	TaskStatusAbandoned TaskStatus = "abandoned"
	// TaskStatusOnHold gates dispatch on un-deployed dependencies (F2
	// deploy-gating, docs/design/cross-component-wiring-gaps.md §3). The
	// dispatcher transitions a task into this state if any task it
	// dependsOn (by component name) is not yet `deployed`; an upstream
	// deploy fires the projector's onTaskDeployed cascade which
	// re-evaluates and auto-dispatches. Uses the same "on_hold" value as
	// the GitHub Project board column so both surfaces stay in sync.
	TaskStatusOnHold TaskStatus = "on_hold"
	// TaskStatusVerificationFailed (F3c, docs/design/cross-component-
	// wiring-gaps.md §3 F3c) is the task state when the dispatched agent
	// reports that integration verification against a dependency endpoint
	// failed. The PR stays a draft, an "Operator action required" surface
	// shows on the board, and the operator clicks Retry to re-dispatch
	// (transition back to in_progress).
	TaskStatusVerificationFailed TaskStatus = "verification_failed"
)

// IsTerminal reports whether the status is a terminal state. Terminal states
// absorb late events and reject further transitions.
func (s TaskStatus) IsTerminal() bool {
	switch s {
	case TaskStatusDeployed, TaskStatusRejected, TaskStatusFailed, TaskStatusAbandoned:
		return true
	}
	return false
}

// ComponentTask is one implementation task targeting a single component.
// Scoped to an append-only batch (see BatchID, SourceDesignVersion,
// SourceSpecVersion). Maps 1:1:1:1 to a GitHub issue, feature branch, and
// draft PR; state is driven by webhooks (services/webhook/projector.go).
//
// As of the tech-lead agent revamp (docs/design/tech-lead-agent.md), the row
// no longer snapshots component shape (OpenAPI, language, dependencies, etc.).
// Dispatch reads the current design from the multi-file `specs/design/`
// tree on every run.
type ComponentTask struct {
	ID        string `gorm:"primaryKey;type:uuid;default:gen_random_uuid()" json:"id"`
	ProjectID string `gorm:"index;not null" json:"projectId"`
	OrgID     string `gorm:"index;not null" json:"-"`

	// Component identity — name only. Full component shape lives under
	// `specs/design/components/<ComponentName>/` (design.md +
	// optional openapi.yaml).
	ComponentName string `gorm:"not null" json:"componentName"`

	// Title is the GitHub issue title. Used as the dependsOn key within a
	// batch and surfaces in the UI / kanban.
	Title string `gorm:"type:text" json:"title"`

	// Rationale is the planner's one-sentence "why this task exists".
	// Surfaced under the title in each card.
	Rationale string `gorm:"type:text" json:"rationale,omitempty"`

	// Body is the GitHub issue body authored by the detail phase, before
	// the platform's "Local Developer Setup" + "Closes #N" suffix is
	// appended at issue-create / edit time. The DB row is canonical for
	// dispatch; if the GH edit fails BodySyncPending is set and a
	// reconciler retries.
	Body string `gorm:"type:text" json:"body,omitempty"`

	// DependsOnComponents lists component names this task's component
	// depends on, sourced directly from the `specs/design/` tree
	// (design.Components[*].DependsOn, parsed from per-component
	// design.md frontmatter). The value is platform-authored,
	// not LLM-authored, so gating cannot silently fail open on a
	// hallucinated identifier. Dispatch (deploy-gated): a task is
	// dispatchable only when, for every entry c in DependsOnComponents,
	// the batch contains a task whose ComponentName == c and Status
	// == deployed. See services/dispatch_service.go::depsAllDeployed.
	DependsOnComponents StringSlice `gorm:"column:depends_on_components;type:jsonb;serializer:json" json:"dependsOnComponents,omitempty"`

	// Lineage — set at generation time, immutable thereafter.
	BatchID             *string `gorm:"type:uuid;index" json:"batchId,omitempty"`
	SourceDesignVersion string  `gorm:"type:text" json:"sourceDesignVersion,omitempty"`
	SourceSpecVersion   string  `gorm:"type:text" json:"sourceSpecVersion,omitempty"`

	// Execution
	Order         int    `json:"order"` // 1-indexed; surfaces as a stable display order
	Status        string `gorm:"default:pending;index" json:"status"`
	WorkspacePath string `json:"workspacePath"`
	ExecType      string `json:"execType"` // "SYSTEM","WORKER"

	// GitHub artifacts (1:1 with this task) — set at dispatch.
	IssueURL          string      `gorm:"type:text;index" json:"issueUrl,omitempty"`
	IssueNumber       int         `json:"issueNumber,omitempty"`
	Labels            StringSlice `gorm:"type:jsonb;serializer:json" json:"labels,omitempty"`
	LifecycleStatus   string      `gorm:"not null;default:gh_issue_waiting" json:"lifecycleStatus"`
	BranchName        string      `gorm:"type:text;index" json:"branchName,omitempty"`
	PullRequestNumber int         `gorm:"index" json:"pullRequestNumber,omitempty"`
	PullRequestURL    string      `gorm:"type:text" json:"pullRequestUrl,omitempty"`

	// State derived from webhooks.
	MergeCommitSHA   string     `gorm:"type:text;index" json:"mergeCommitSha,omitempty"`
	LastEventAt      *time.Time `gorm:"index" json:"lastEventAt,omitempty"`
	LastBuildRunName string     `gorm:"type:text" json:"lastBuildRunName,omitempty"`
	LastBuildSHA     string     `gorm:"type:text" json:"lastBuildSha,omitempty"`
	// LastCodingAgentRunName is the most recent OC WorkflowRun that ran the
	// per-task coding agent. Mirrors LastBuildRunName for the build phase.
	// Set by DispatchService at dispatch time; the coding-agent watcher
	// reads it to poll the run's status. Replaces the legacy WorkspacePath
	// column populated by remote-worker.
	LastCodingAgentRunName string `gorm:"type:text" json:"lastCodingAgentRunName,omitempty"`

	// Error tracking
	ErrorMessage string `gorm:"type:text" json:"errorMessage,omitempty"`

	// Cause records the projector event that drove the most recent
	// terminal transition. NULL on non-terminal rows.
	Cause *string `gorm:"type:text;index" json:"cause,omitempty"`

	// BuildAuthRetryCount counts how many times the build watcher has
	// re-minted + recreated this task's WorkflowRun in response to
	// git_clone_failed_auth. Budget enforced in build_watcher.
	BuildAuthRetryCount int `gorm:"not null;default:0" json:"buildAuthRetryCount,omitempty"`

	// DispatchDeferredAt is set the first time dispatchOne reverts a task
	// to on_hold because a dependency's external URL is not yet available
	// in the OC ReleaseBinding status (timing race between build completion
	// and the OC controller resolving the ingress). The on_hold_watcher
	// retries dispatch every 10s; after deferDeadline (2 min) the task is
	// permanently failed. Nil on tasks that were never deferred.
	DispatchDeferredAt *time.Time `gorm:"column:dispatch_deferred_at" json:"dispatchDeferredAt,omitempty"`

	// BodySyncPending is set when the GitHub issue body edit failed after
	// retries; a periodic reconciler re-attempts the edit. The DB row's
	// Body field is canonical for dispatch regardless of GH state.
	BodySyncPending bool `gorm:"not null;default:false" json:"bodySyncPending,omitempty"`

	DispatchedAt *time.Time `json:"dispatchedAt,omitempty"`
	CreatedAt    time.Time  `json:"createdAt"`
	UpdatedAt    time.Time  `json:"updatedAt"`
}
