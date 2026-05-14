import express from "express";
import { jwtAuthMiddleware } from "../middleware/jwt.js";
import { correlationIdMiddleware } from "../middleware/correlation.js";
import { requireOrgId } from "../middleware/org-id.js";
import { invalidateAnthropicCache } from "../shared/anthropic-key-resolver.js";
import { registerDocumentGeneration } from "./routes/document-generation.js";
import { registerArchitect } from "./routes/architect.js";
import {
  registerTechLeadPlan,
  registerTechLeadDetail,
} from "./routes/tech-lead.js";

const port = parseInt(process.env.PORT || "3400", 10);

const app = express();
app.use(express.json({ limit: "1mb" }));

// Correlation ID first so /healthz also gets one in logs.
app.use(correlationIdMiddleware());

app.get("/healthz", (_req, res) => {
  res.json({ ok: true });
});

const jwksUrl = process.env.JWKS_URL;
const jwtIssuer = process.env.JWT_ISSUER;
const jwtAudience = process.env.JWT_AUDIENCE || "agents-service";
const resourceMetadataUrl = process.env.JWT_RESOURCE_METADATA_URL;

if (!jwksUrl) {
  console.warn(
    "JWKS_URL not set — agents-service is running with auth DISABLED. " +
      "This is only safe in local development.",
  );
} else {
  if (!jwtIssuer) {
    console.error("JWT_ISSUER must be set when JWKS_URL is set");
    process.exit(1);
  }
  app.use(
    "/v1/agents",
    jwtAuthMiddleware({
      jwksUrl,
      issuer: jwtIssuer,
      audience: jwtAudience,
      resourceMetadataUrl,
    }),
  );
}

// Every /v1/agents/* route needs the org id for the Anthropic-key resolver.
app.use("/v1/agents", requireOrgId());

// Internal cache-invalidate endpoint — git-service POSTs here on
// Connect/Disconnect so the resolver's 5-min LRU drops the orgId
// immediately. Mounted under /v1/internal so the existing JWT middleware
// (which gates /v1/agents) doesn't apply; in production an explicit
// service-JWT gate should wrap this — deferred until the cloud-side env
// brings up agents-service as a first-class Component.
app.post("/v1/internal/cache/invalidate", (req, res) => {
  const orgId = (req.body as { orgId?: unknown })?.orgId;
  if (typeof orgId !== "string" || orgId.length === 0) {
    res.status(400).json({ error: "orgId required" });
    return;
  }
  invalidateAnthropicCache(orgId);
  console.log(`[anthropic-key-resolver] cache invalidated orgId=${orgId}`);
  res.status(204).end();
});

registerDocumentGeneration(app);
registerArchitect(app);
registerTechLeadPlan(app);
registerTechLeadDetail(app);

app.listen(port, () => {
  console.log(`agents-service listening on :${port}`);
});
