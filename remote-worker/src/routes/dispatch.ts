import { Router } from "express";
import { config } from "../config.js";
import { openTaskLog } from "../lib/logger.js";
import { runClaudeQuery } from "../lib/runner.js";
import * as taskRegistry from "../lib/taskRegistry.js";
import { isSlug, isUUID } from "../lib/uuid.js";
import { provisionWorkspace } from "../lib/workspace.js";
import { getCorrelationId } from "../middleware/correlation.js";
import type { DispatchRequest, DispatchResponse } from "../lib/types.js";

const router = Router();

router.post("/dispatch", async (req, res) => {
  // Backpressure — refuse new work above the concurrency cap so the host's
  // Claude session and disk pressure stay bounded. Idempotent re-dispatches
  // skip this check below.
  if (taskRegistry.runningCount() >= config.maxConcurrentTasks) {
    res.setHeader("Retry-After", "30");
    res.status(429).json({
      error: "remote-worker at max concurrent tasks",
      maxConcurrentTasks: config.maxConcurrentTasks,
    });
    return;
  }

  const body = (req.body ?? {}) as Partial<DispatchRequest>;
  const {
    taskId,
    projectId,
    componentName,
    prompt,
    branchName,
    repoUrl,
    bearer,
    identity,
    gitServiceUrl,
    orgId,
  } = body;

  if (
    !taskId ||
    !projectId ||
    !componentName ||
    !prompt ||
    !branchName ||
    !repoUrl ||
    !bearer ||
    !identity ||
    !gitServiceUrl ||
    !orgId
  ) {
    res.status(400).json({
      error:
        "taskId, orgId, projectId, componentName, prompt, branchName, repoUrl, bearer, identity, and gitServiceUrl are required",
    });
    return;
  }

  // Identifier validation at the boundary — these end up in workspace paths
  // and OpenBao keys, so reject anything that could traverse the filesystem
  // before any disk I/O. taskId is a UUID; orgId and projectId are DNS-label
  // slugs (lowercase, ≤63 chars, alphanumeric + hyphen). shellSingleQuote in
  // credhelper.ts is kept as defense-in-depth.
  if (!isUUID(taskId)) {
    res.status(400).json({ error: "taskId must be a UUID" });
    return;
  }
  if (!isSlug(orgId) || !isSlug(projectId)) {
    res.status(400).json({
      error: "orgId and projectId must be DNS-label slugs (lowercase, ≤63 chars, alphanumeric + hyphen)",
    });
    return;
  }
  // Defense in depth — characters that could traverse out of the workspace.
  if (
    componentName.includes("/") ||
    componentName.includes("..") ||
    componentName.includes("\0")
  ) {
    res.status(400).json({ error: "componentName contains invalid characters" });
    return;
  }

  // Idempotent: a duplicate dispatch for an in-flight task returns the
  // running task's workspace path without re-provisioning.
  const existing = taskRegistry.get(taskId);
  if (existing && !existing.done) {
    const resp: DispatchResponse = {
      taskId,
      workspacePath: existing.workspacePath,
      status: "running",
    };
    res.json(resp);
    return;
  }

  // Inbound correlation ID — threaded into the workspace env so the
  // credhelper.sh and gh wrapper add `X-Correlation-ID` on every refresh
  // call to git-service. Closes the BFF → remote-worker → credhelper →
  // git-service trace gap. getCorrelationId returns "-" if missing, which
  // we treat as absent (the helper script skips the header on empty).
  const inboundCorrelationId = getCorrelationId(res);
  const dispatchReq: DispatchRequest = {
    taskId,
    orgId,
    projectId,
    componentName,
    branchName,
    repoUrl,
    bearer,
    identity,
    gitServiceUrl,
    prompt,
    correlationId: inboundCorrelationId === "-" ? undefined : inboundCorrelationId,
  };

  let layout;
  try {
    layout = await provisionWorkspace(dispatchReq);
  } catch (err) {
    const msg = err instanceof Error ? err.message : String(err);
    console.error("failed to provision workspace", { taskId, error: msg });
    const resp: DispatchResponse = {
      taskId,
      workspacePath: "",
      status: "failed",
      error: "failed to provision workspace: " + msg,
    };
    res.json(resp);
    return;
  }

  const startedAt = new Date();
  let started;
  try {
    const log = openTaskLog(layout.workspace);
    started = runClaudeQuery(dispatchReq, layout, log);
  } catch (err) {
    const msg = err instanceof Error ? err.message : String(err);
    console.error("failed to start claude query", {
      taskId,
      component: componentName,
      error: msg,
    });
    const resp: DispatchResponse = {
      taskId,
      workspacePath: layout.workspace,
      status: "failed",
      error: "failed to start claude query: " + msg,
    };
    res.json(resp);
    return;
  }

  taskRegistry.set(taskId, {
    taskId,
    componentName,
    workspacePath: layout.workspace,
    startedAt,
    query: started.query,
    done: false,
    exitCode: 0,
  });

  console.log("dispatched task", {
    taskId,
    component: componentName,
    workspace: layout.workspace,
    branch: branchName,
  });

  started.completion.then((result) => {
    taskRegistry.markDone(taskId, result.exitCode, result.error);
    const duration = Date.now() - startedAt.getTime();
    if (result.exitCode === 0) {
      console.log("claude query completed", {
        taskId,
        component: componentName,
        durationMs: duration,
      });
    } else {
      console.error("claude query failed", {
        taskId,
        component: componentName,
        exitCode: result.exitCode,
        error: result.error,
        durationMs: duration,
      });
    }
  });

  const resp: DispatchResponse = {
    taskId,
    workspacePath: layout.workspace,
    status: "running",
  };
  res.json(resp);
});

export default router;
