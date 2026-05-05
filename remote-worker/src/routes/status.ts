import { Router } from "express";
import { formatDuration } from "../lib/logger.js";
import * as taskRegistry from "../lib/taskRegistry.js";
import type { StatusResponse } from "../lib/types.js";

const router = Router();

router.get("/status/:taskId", (req, res) => {
  const { taskId } = req.params;
  const info = taskRegistry.get(taskId);

  if (!info) {
    const resp: StatusResponse = { taskId, status: "unknown" };
    res.json(resp);
    return;
  }

  const elapsedMs = Date.now() - info.startedAt.getTime();
  const resp: StatusResponse = {
    taskId,
    status: info.done
      ? info.exitCode === 0
        ? "completed"
        : "failed"
      : "running",
    startedAt: info.startedAt.toISOString(),
    duration: formatDuration(elapsedMs),
  };
  if (info.done) {
    resp.exitCode = info.exitCode;
  }

  res.json(resp);
});

export default router;
