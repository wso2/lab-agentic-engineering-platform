// Versioned NDJSON envelope emitted by the coding-agent runner to stdout.
// Source of truth: docs/design/task-execution-progress.md §5.1.
// The Go mirror lives at asdlc-service/clients/observer/schema.go;
// schemas/progress-event.schema.json gates them in CI.

export const PROGRESS_SCHEMA_VERSION = 1 as const;

export type ProgressKind =
  | "phase"
  | "tool_use"
  | "git_commit"
  | "git_push"
  | "gh_action"
  | "log"
  | "result";

interface ProgressEnvelope {
  schemaVersion: typeof PROGRESS_SCHEMA_VERSION;
  ts: string;
  seq: number;
  kind: ProgressKind;
}

export interface PhaseEvent extends ProgressEnvelope {
  kind: "phase";
  phase: string;
}

export interface ToolUseEvent extends ProgressEnvelope {
  kind: "tool_use";
  tool: string;
  summary: string;
}

export interface GitCommitEvent extends ProgressEnvelope {
  kind: "git_commit";
  sha?: string;
  files?: number;
  summary?: string;
}

export interface GitPushEvent extends ProgressEnvelope {
  kind: "git_push";
  sha?: string;
  branch?: string;
  summary?: string;
}

export interface GhActionEvent extends ProgressEnvelope {
  kind: "gh_action";
  command: string;
  summary?: string;
}

export interface LogEvent extends ProgressEnvelope {
  kind: "log";
  level?: "info" | "warn" | "error";
  summary: string;
}

export interface ResultEvent extends ProgressEnvelope {
  kind: "result";
  status: "success" | "failure";
  summary?: string;
  error?: string;
}

export type ProgressEvent =
  | PhaseEvent
  | ToolUseEvent
  | GitCommitEvent
  | GitPushEvent
  | GhActionEvent
  | LogEvent
  | ResultEvent;

// Discriminated union of payloads (no envelope fields). The emitter stamps
// schemaVersion / ts / seq itself so callers cannot forget.
export type ProgressEventInput =
  | { kind: "phase"; phase: string }
  | { kind: "tool_use"; tool: string; summary: string }
  | { kind: "git_commit"; sha?: string; files?: number; summary?: string }
  | { kind: "git_push"; sha?: string; branch?: string; summary?: string }
  | { kind: "gh_action"; command: string; summary?: string }
  | { kind: "log"; level?: "info" | "warn" | "error"; summary: string }
  | { kind: "result"; status: "success" | "failure"; summary?: string; error?: string };
