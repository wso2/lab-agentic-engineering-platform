// One-shot entrypoint for the per-task coding-agent pod.
//
// The Argo Workflow renders a pod from app-factory-coding-agent ClusterWorkflow,
// passing the dispatch payload via ASDLC_* env vars (no HTTP, no token in body).
// We reuse the same provisionWorkspace + runClaudeQuery code that the legacy
// HTTP server used; only the wrapper changes shape.
//
// Exit codes:
//   0 — agent reported success
//   1 — agent reported failure (commit/push/PR-ready issue, etc.)
//   2 — provisioning or unexpected error before the agent ran
//
// Argo treats any non-zero exit as a Failed step, which the BFF coding-agent
// watcher picks up via the WorkflowRun terminal status.

import { randomUUID } from "node:crypto";
import { provisionWorkspace } from "./lib/workspace.js";
import { runClaudeQuery } from "./lib/runner.js";
import { openTaskLog } from "./lib/logger.js";
import { isUUID, isSlug } from "./lib/uuid.js";
import type { DispatchRequest } from "./lib/types.js";

function requireEnv(name: string): string {
  const v = process.env[name];
  if (v === undefined || v === "") {
    throw new Error(`missing required env var: ${name}`);
  }
  return v;
}

function readDispatchFromEnv(): DispatchRequest {
  const taskId = requireEnv("ASDLC_TASK_ID");
  const orgId = requireEnv("ASDLC_ORG_ID");
  const projectId = requireEnv("ASDLC_PROJECT_ID");
  const componentName = requireEnv("ASDLC_COMPONENT_NAME");
  const branchName = requireEnv("ASDLC_BRANCH_NAME");
  const repoUrl = requireEnv("ASDLC_REPO_URL");
  const bearer = requireEnv("ASDLC_BEARER");
  const gitServiceUrl = requireEnv("ASDLC_GIT_SERVICE_URL");
  const prompt = requireEnv("ASDLC_PROMPT");
  const identityName = requireEnv("ASDLC_IDENTITY_NAME");
  const identityEmail = requireEnv("ASDLC_IDENTITY_EMAIL");
  const identityLogin = process.env.ASDLC_IDENTITY_LOGIN || "";
  const correlationId = process.env.ASDLC_CORRELATION_ID || randomUUID();

  if (!isUUID(taskId)) throw new Error(`ASDLC_TASK_ID is not a valid UUID: ${taskId}`);
  if (!isSlug(orgId)) throw new Error(`ASDLC_ORG_ID is not a valid slug: ${orgId}`);
  if (!isSlug(projectId)) throw new Error(`ASDLC_PROJECT_ID is not a valid slug: ${projectId}`);
  if (componentName.includes("/") || componentName.includes("..")) {
    throw new Error(`ASDLC_COMPONENT_NAME must not contain '/' or '..': ${componentName}`);
  }

  return {
    taskId,
    orgId,
    projectId,
    componentName,
    branchName,
    repoUrl,
    bearer,
    identity: { name: identityName, email: identityEmail, login: identityLogin || undefined },
    gitServiceUrl,
    prompt,
    correlationId,
  };
}

async function main(): Promise<number> {
  let req: DispatchRequest;
  try {
    req = readDispatchFromEnv();
  } catch (err) {
    console.error("[oneshot] env validation failed:", err instanceof Error ? err.message : String(err));
    return 2;
  }

  console.log(
    `[oneshot] starting taskId=${req.taskId} component=${req.componentName} branch=${req.branchName} correlationId=${req.correlationId}`,
  );

  let layout;
  try {
    layout = await provisionWorkspace(req);
  } catch (err) {
    console.error("[oneshot] provisionWorkspace failed:", err instanceof Error ? err.message : String(err));
    return 2;
  }

  const log = openTaskLog(layout.workspace);
  const { completion } = runClaudeQuery(req, layout, log);
  const result = await completion;

  if (result.exitCode === 0) {
    console.log(`[oneshot] agent completed successfully taskId=${req.taskId}`);
  } else {
    console.error(
      `[oneshot] agent failed taskId=${req.taskId} exitCode=${result.exitCode} error=${result.error ?? "unknown"}`,
    );
  }
  return result.exitCode;
}

main()
  .then((code) => process.exit(code))
  .catch((err) => {
    console.error("[oneshot] unhandled error:", err);
    process.exit(2);
  });
