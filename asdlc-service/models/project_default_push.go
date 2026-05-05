package models

import "time"

// ProjectDefaultPush records a push observed on a project's default branch.
//
// This table is the cross-handler rendezvous between the push handler and the
// PR handler: webhook arrival order is not guaranteed, so when a push for SHA
// X arrives before the matching pull_request.closed event, the push handler
// records it here without touching task rows; when the PR handler later
// processes the merge, it queries this table by MergeCommitSHA and short-
// circuits the task to `building` immediately.
//
// Composite PK on (ProjectID, SHA). BuiltAt is set when WorkflowRuns are
// created for the push (idempotency anchor for §8.4 step 3).
type ProjectDefaultPush struct {
	ProjectID string     `gorm:"primaryKey;type:text" json:"projectId"`
	SHA       string     `gorm:"primaryKey;type:text" json:"sha"`
	PushedAt  time.Time  `gorm:"index;not null" json:"pushedAt"`
	BuiltAt   *time.Time `json:"builtAt,omitempty"`
}
