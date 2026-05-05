import { Router } from "express";
import * as taskRegistry from "../lib/taskRegistry.js";

const router = Router();

router.get("/health", (_req, res) => {
  res.json({ status: "ok", runningProcesses: taskRegistry.runningCount() });
});

export default router;
