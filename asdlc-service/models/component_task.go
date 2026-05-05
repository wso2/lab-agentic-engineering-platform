package models

import "time"

// StringSlice is a reusable slice type for JSONB storage in PostgreSQL.
type StringSlice []string

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
	// TaskStatusPendingDeps gates dispatch on un-merged dependencies (tech-
	// lead revamp, design §12). The dispatcher transitions a task into this
	// state if any task it dependsOn is not yet merged; a merge webhook
	// re-evaluates and dispatches it.
	TaskStatusPendingDeps TaskStatus = "pending_deps"
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
// Dispatch reads the current design from .asdlc/design.json on every run.
type ComponentTask struct {
	ID        string `gorm:"primaryKey;type:uuid;default:gen_random_uuid()" json:"id"`
	ProjectID string `gorm:"index;not null" json:"projectId"`
	OrgID     string `gorm:"index;not null" json:"-"`

	// Component identity — name only. Full component shape lives in
	// .asdlc/design.json keyed by ComponentName.
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

	// TaskDependsOn lists titles of other tasks (in the same batch) this
	// task depends on. Dispatch waits for all listed tasks to merge before
	// running this one. Already-merged baseline work is omitted by the
	// planner — only intra-batch / non-merged refs land here.
	TaskDependsOn StringSlice `gorm:"type:jsonb;serializer:json" json:"taskDependsOn,omitempty"`

	// Lineage — set at generation time, immutable thereafter.
	BatchID             *string `gorm:"type:uuid;index" json:"batchId,omitempty"`
	SourceDesignVersion string  `gorm:"type:text" json:"sourceDesignVersion,omitempty"`
	SourceSpecVersion   string  `gorm:"type:text" json:"sourceSpecVersion,omitempty"`

	// WireframePath stays — wireframe artefacts are still per-task, not
	// derived from design.json.
	WireframePath string `gorm:"type:text" json:"wireframePath,omitempty"`

	// Execution
	Order         int    `json:"order"` // 1-indexed; surfaces as a stable display order
	Status        string `gorm:"default:pending;index" json:"status"`
	WorkspacePath string `json:"workspacePath"`
	ExecType      string `json:"execType"` // "SYSTEM","WORKER"

	// GitHub artifacts (1:1 with this task) — set at dispatch.
	IssueURL          string      `gorm:"type:text;index" json:"issueUrl,omitempty"`
	IssueNumber       int         `json:"issueNumber,omitempty"`
	Labels            StringSlice `gorm:"type:jsonb;serializer:json" json:"labels,omitempty"`
	BranchName        string      `gorm:"type:text;index" json:"branchName,omitempty"`
	PullRequestNumber int         `gorm:"index" json:"pullRequestNumber,omitempty"`
	PullRequestURL    string      `gorm:"type:text" json:"pullRequestUrl,omitempty"`

	// State derived from webhooks.
	MergeCommitSHA   string     `gorm:"type:text;index" json:"mergeCommitSha,omitempty"`
	LastEventAt      *time.Time `gorm:"index" json:"lastEventAt,omitempty"`
	LastBuildRunName string     `gorm:"type:text" json:"lastBuildRunName,omitempty"`
	LastBuildSHA     string     `gorm:"type:text" json:"lastBuildSha,omitempty"`

	// Error tracking
	ErrorMessage string `gorm:"type:text" json:"errorMessage,omitempty"`

	// Cause records the projector event that drove the most recent
	// terminal transition. NULL on non-terminal rows.
	Cause *string `gorm:"type:text;index" json:"cause,omitempty"`

	// BuildAuthRetryCount counts how many times the build watcher has
	// re-minted + recreated this task's WorkflowRun in response to
	// git_clone_failed_auth. Budget enforced in build_watcher.
	BuildAuthRetryCount int `gorm:"not null;default:0" json:"buildAuthRetryCount,omitempty"`

	// BodySyncPending is set when the GitHub issue body edit failed after
	// retries; a periodic reconciler re-attempts the edit. The DB row's
	// Body field is canonical for dispatch regardless of GH state.
	BodySyncPending bool `gorm:"not null;default:false" json:"bodySyncPending,omitempty"`

	DispatchedAt *time.Time `json:"dispatchedAt,omitempty"`
	CreatedAt    time.Time  `json:"createdAt"`
	UpdatedAt    time.Time  `json:"updatedAt"`
}
