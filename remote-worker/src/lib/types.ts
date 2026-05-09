export interface DispatchIdentity {
  name: string;
  email: string;
  login?: string;
}

// DispatchRequest is the input to a one-shot pod run. The values come from
// ASDLC_* env vars assembled by the Argo Workflow from the WorkflowRun's
// parameters (see app-factory-coding-agent.yaml).
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
