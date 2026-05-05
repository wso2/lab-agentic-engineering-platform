import os from "node:os";
import path from "node:path";

const apiKey = process.env.ANTHROPIC_API_KEY;
const apiKeyStatus = apiKey === undefined ? "not set" : apiKey.length > 0 ? true : "empty";

export const config = {
  port: parseInt(process.env.PORT || "3200", 10),
  // Bind to 0.0.0.0 so the BFF (running in Docker) can reach this host-side
  // process via host.docker.internal — Docker's host-gateway maps to a bridge
  // IP, not host loopback, so a 127.0.0.1 bind would refuse those connections.
  // Security boundary is the JWT middleware on /dispatch and /status, plus
  // the dev machine's firewall blocking 3200 from external networks.
  bindHost: process.env.BIND_HOST || "0.0.0.0",
  workspaceBasePath:
    process.env.WORKSPACE_BASE_PATH ||
    path.join(os.homedir(), "asdlc-workspace"),
  claudeExecutablePath: process.env.CLAUDE_PATH || "claude",
  // Backpressure cap. Above this many concurrent in-flight tasks, /dispatch
  // returns 429. Default 8 keeps a development laptop responsive.
  maxConcurrentTasks: parseInt(
    process.env.REMOTE_WORKER_MAX_CONCURRENT_TASKS || "8",
    10,
  ),
};
