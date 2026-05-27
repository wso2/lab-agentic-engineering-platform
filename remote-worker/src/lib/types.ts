export interface DispatchIdentity {
  name: string;
  email: string;
  login?: string;
}

// DispatchRequest is the input to a one-shot pod run. The values come from
// ASDLC_* env vars assembled by the Argo Workflow from the WorkflowRun's
// parameters (see app-factory-coding-agent.yaml). No `branchName` field —
// the workspace clones the project's default branch and the agent itself
// creates the feature branch and opens the PR with `Closes #<issueNumber>`
// so the BFF webhook can link the PR back to the task.
export interface DispatchRequest {
  taskId: string;
  orgId: string;
  projectId: string;
  componentName: string;
  repoUrl: string;
  bearer: string;
  identity: DispatchIdentity;
  gitServiceUrl: string;
  prompt: string;
  /** Optional correlation ID for distributed tracing. Forwarded to git-service via credhelper. */
  correlationId?: string;
  /**
   * WS2.4 — full URL for the credentials/refresh endpoint used during
   * workspace bootstrap. Defaults to the legacy
   * `${gitServiceUrl}/api/v1/credentials/refresh`; oneshot.ts overrides
   * to the path-scoped `${platformUrl}/api/v1/tasks/{taskId}/credentials/refresh`
   * when publisher cc creds are present.
   */
  refreshUrl?: string;
}
