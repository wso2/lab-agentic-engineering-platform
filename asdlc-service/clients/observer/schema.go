// Package observer holds the BFF's client to the OpenChoreo Observer
// service plus the on-the-wire schema for runner progress events.
//
// schemaVersion=1 mirrors the TS source-of-truth at
// remote-worker/src/lib/progress/schema.ts. CI gate (Track E) ensures
// the two stay in sync against schemas/progress-event.schema.json.
package observer

import (
	"encoding/json"
	"strings"
)

// ProgressSchemaVersion is the only version this BFF supports today.
// Tolerant decoding still wraps unknown shapes as a `log` event so
// out-of-band lines never break the feed.
const ProgressSchemaVersion = 1

// ProgressEvent is the unified shape returned to /progress/agent and
// /progress/build callers. Optional fields use omitempty so JSON
// payloads stay compact.
type ProgressEvent struct {
	SchemaVersion int    `json:"schemaVersion"`
	Ts            string `json:"ts"`
	Seq           int64  `json:"seq"`
	Kind          string `json:"kind"`

	// Phase events.
	Phase string `json:"phase,omitempty"`

	// Tool-use events.
	Tool string `json:"tool,omitempty"`

	// git_commit / git_push.
	SHA    string `json:"sha,omitempty"`
	Branch string `json:"branch,omitempty"`
	Files  int    `json:"files,omitempty"`

	// gh_action.
	Command string `json:"command,omitempty"`

	// log + result.
	Level   string `json:"level,omitempty"`
	Status  string `json:"status,omitempty"`
	Summary string `json:"summary,omitempty"`
	Error   string `json:"error,omitempty"`

	// build_step (BFF-synthetic, emitted by progress_service from
	// WorkflowRun.Status.Tasks[] deltas).
	Step        string `json:"step,omitempty"`
	StartedAt   string `json:"startedAt,omitempty"`
	CompletedAt string `json:"completedAt,omitempty"`
	Message     string `json:"message,omitempty"`
}

// ParseProgressLine attempts to decode a fluent-bit log line as a versioned
// progress envelope. Lines that are non-JSON or that don't carry a
// recognised schema version are wrapped as a `log` event so the feed
// stays continuous (the runner is not the only thing that can write to
// stdout — fluent-bit may still pick up stray lines from libraries).
func ParseProgressLine(raw string) ProgressEvent {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" || trimmed[0] != '{' {
		return ProgressEvent{Kind: "log", Summary: raw}
	}
	var ev ProgressEvent
	if err := json.Unmarshal([]byte(trimmed), &ev); err != nil {
		return ProgressEvent{Kind: "log", Summary: raw}
	}
	if ev.SchemaVersion != ProgressSchemaVersion || ev.Kind == "" {
		return ProgressEvent{Kind: "log", Summary: raw}
	}
	return ev
}
