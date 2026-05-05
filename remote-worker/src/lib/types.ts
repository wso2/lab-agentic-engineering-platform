import type { Query } from "@anthropic-ai/claude-agent-sdk";

export interface DispatchIdentity {
  name: string;
  email: string;
  login?: string;
}

export interface DispatchRequest {
  taskId: string;
  orgId: string;
  projectId: string;
  componentName: string;
  branchName: string;
  repoUrl: string;
  bearer: string;
  identity: DispatchIdentity;
  gitServiceUrl: string;
  prompt: string;
  /** Optional correlation ID for distributed tracing. Forwarded to git-service via credhelper. */
  correlationId?: string;
}

export interface DispatchResponse {
  taskId: string;
  workspacePath: string;
  status: "running" | "failed";
  error?: string;
}

export type TaskStatus = "running" | "completed" | "failed" | "unknown";

export interface StatusResponse {
  taskId: string;
  status: TaskStatus;
  exitCode?: number;
  startedAt?: string;
  duration?: string;
}

export interface TaskInfo {
  taskId: string;
  componentName: string;
  workspacePath: string;
  startedAt: Date;
  query?: Query;
  done: boolean;
  exitCode: number;
  error?: string;
}
