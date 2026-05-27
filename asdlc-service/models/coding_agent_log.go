package models

import (
	"time"

	"github.com/google/uuid"
)

// CodingAgentLog is the per-run captured-on-terminal snapshot of a
// coding-agent pod's stdout/stderr. Sidecar to component_tasks rather
// than a column on it: agent logs run MB-scale and component_tasks is
// read on every list / status / dispatch path, so keeping the blob
// out of the hot row avoids TOAST detoasting overhead on common
// queries. Mirrors the webhook_payloads ↔ webhook_deliveries split.
//
// One row per `(task_id, run_name)` — a retried task gets a new
// `run_name` per minute-bucket and a new sidecar row, preserving
// per-run logs across retries.
//
// Captured by `services/codingagent/JobWatcher` when a Job hits
// terminal (Failed/Succeeded). Read by `services/progress_service`
// on the post-terminal branch of `GetAgentProgress`.
type CodingAgentLog struct {
	TaskID      uuid.UUID `gorm:"type:uuid;primaryKey;column:task_id" json:"taskId"`
	RunName     string    `gorm:"type:text;primaryKey;column:run_name" json:"runName"`
	FinalPhase  string    `gorm:"type:text;not null;column:final_phase" json:"finalPhase"`
	CapturedAt  time.Time `gorm:"type:timestamptz;not null;default:now();column:captured_at" json:"capturedAt"`
	LogText     string    `gorm:"type:text;not null;column:log_text" json:"-"`
	SizeBytes   int64     `gorm:"type:bigint;not null;column:size_bytes" json:"sizeBytes"`
}

func (CodingAgentLog) TableName() string { return "coding_agent_logs" }
