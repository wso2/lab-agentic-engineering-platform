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
import { emit, primeScrubber } from "./lib/progress/emitter.js";
import { pullTaskSkills } from "./lib/skills_pull.js";
import { materializeSkills } from "./lib/skills_materializer.js";

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
    // Pre-scrubber stderr is the only safe channel here — the bearer
    // hasn't been read yet so we can't enroll it as a redaction literal.
    console.error("[oneshot] env validation failed:", err instanceof Error ? err.message : String(err));
    return 2;
  }

  primeScrubber([process.env.ANTHROPIC_API_KEY, req.bearer]);

  emit({
    kind: "phase",
    phase: "workspace_provisioning",
  });

  let layout;
  try {
    layout = await provisionWorkspace(req);
  } catch (err) {
    const msg = err instanceof Error ? err.message : String(err);
    emit({ kind: "result", status: "failure", error: `workspace_provisioning: ${msg}` });
    console.error("[oneshot] provisionWorkspace failed:", msg);
    return 2;
  }

  emit({ kind: "phase", phase: "workspace_ready" });

  // Per-task skills — pull snapshotted SKILL.md bodies from the BFF,
  // materialise into the AgentSkills plugin tree under
  // .asdlc/skills-plugin/. Best-effort: failures log + continue
  // (empty pull means runner loads the base asdlc plugin only).
  // See docs/design/skills-system.md > "Coding agent".
  let preloadBuiltinNames: string[] = [];
  let skillsPluginDir: string | undefined;
  const platformURL = process.env.ASDLC_PLATFORM_URL ?? "";
  if (platformURL) {
    try {
      const skills = await pullTaskSkills({
        platformURL,
        taskId: req.taskId,
        bearer: req.bearer,
        correlationId: req.correlationId,
      });
      const result = await materializeSkills(layout.workspace, skills.skills);
      if (result) {
        skillsPluginDir = result.pluginDir;
        preloadBuiltinNames = result.builtinNames;
        console.log(
          `[oneshot] materialised ${skills.skills.length} skill(s); preload=${preloadBuiltinNames.length} builtin(s)`,
        );
      } else {
        console.log("[oneshot] no per-task skills to materialise");
      }
    } catch (err) {
      const msg = err instanceof Error ? err.message : String(err);
      console.warn("[oneshot] skill pull/materialize failed — continuing without per-task plugin:", msg);
    }
  } else {
    console.log("[oneshot] ASDLC_PLATFORM_URL not set — skipping per-task skills pull");
  }

  const log = openTaskLog(layout.workspace);
  const { completion } = runClaudeQuery(req, layout, log, {
    skillsPluginDir,
    preloadBuiltinNames,
  });
  const result = await completion;
  return result.exitCode;
}

main()
  .then((code) => process.exit(code))
  .catch((err) => {
    console.error("[oneshot] unhandled error:", err);
    process.exit(2);
  });
