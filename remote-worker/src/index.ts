import express from "express";
import type { Server } from "node:http";
import { config } from "./config.js";
import * as taskRegistry from "./lib/taskRegistry.js";
import { correlationIdMiddleware } from "./middleware/correlation.js";
import { jwtAuthMiddleware } from "./middleware/jwt.js";
import dispatchRouter from "./routes/dispatch.js";
import healthRouter from "./routes/health.js";
import statusRouter from "./routes/status.js";

const app = express();
app.use(express.json({ limit: "10mb" }));

// Correlation first — even /healthz benefits from a stable correlation ID
// in logs. JWT comes after, scoped to the protected paths.
app.use(correlationIdMiddleware());

app.use(healthRouter);

const jwksUrl = process.env.JWKS_URL;
const jwtIssuer = process.env.JWT_ISSUER;
const jwtAudience = process.env.JWT_AUDIENCE || "remote-worker";
const resourceMetadataUrl = process.env.JWT_RESOURCE_METADATA_URL;

// JWT auth temporarily disabled — the BFF lacks SERVICE_AUTH_RW_*
// client credentials to send a valid Service JWT.  Re-enable when the
// BFF is configured with per-target client_credentials for this service.
if (jwksUrl) {
  console.warn(
    "JWKS_URL is set but JWT auth is DISABLED — service-auth not yet " +
      "configured on the BFF side.  Accepting unauthenticated /dispatch.",
  );
}

app.use(statusRouter);
app.use(dispatchRouter);

const httpServer: Server = app.listen(config.port, config.bindHost, () => {
  console.log(`remote-worker listening on ${config.bindHost}:${config.port}`, {
    workspace: config.workspaceBasePath,
    claude: config.claudeExecutablePath,
    maxConcurrentTasks: config.maxConcurrentTasks,
  });
});

let shuttingDown = false;
async function shutdown(signal: string): Promise<void> {
  if (shuttingDown) return;
  shuttingDown = true;
  console.log(`received ${signal}, shutting down`);

  const httpClose = new Promise<void>((resolve) =>
    httpServer.close(() => resolve()),
  );

  const interrupts = taskRegistry
    .allTasks()
    .filter((t) => !t.done && t.query)
    .map((t) =>
      t
        .query!.interrupt()
        .catch((err: unknown) =>
          console.warn(
            `interrupt failed for ${t.taskId}`,
            err instanceof Error ? err.message : err,
          ),
        ),
    );

  await Promise.race([
    Promise.all([httpClose, ...interrupts]),
    new Promise((resolve) => setTimeout(resolve, 10_000)),
  ]);
  process.exit(0);
}

process.on("SIGINT", () => void shutdown("SIGINT"));
process.on("SIGTERM", () => void shutdown("SIGTERM"));
