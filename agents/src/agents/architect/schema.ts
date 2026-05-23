import { z } from "zod";

// DependentApi — an HTTP API outside this project that a component consumes
// at runtime. The architect emits these so the cell diagram can render the
// dependency outside the cell boundary, the tech-lead can carry the URL into
// the coding-agent's issue body, and the BFF can pin the URL into a build-
// time env var on the consuming component.
export const DependentApi = z.object({
  name: z
    .string()
    .describe(
      "Lowercase kebab-case identifier for the external API, e.g. 'employee-api'. The tech-lead will UPPER_SNAKE_CASE this for env-var names.",
    ),
  url: z
    .string()
    .describe(
      "Base URL the consuming component must call, e.g. 'http://development-default.openchoreoapis.localhost:19080/employee-app-employee-api-http/employees'.",
    ),
  description: z
    .string()
    .describe(
      "One-line description of what the API returns / does, so the coding agent knows how to use it.",
    ),
  authentication: z
    .enum(["none", "bearer", "api-key"])
    .optional()
    .describe(
      "Auth scheme the upstream requires. 'none' = unauthenticated (default), 'bearer' = caller attaches Authorization: Bearer <token>, 'api-key' = static key via header/query.",
    ),
});

export type DependentApi = z.infer<typeof DependentApi>;

// SlimComponent — shape metadata only, no openAPISpec. The architect emits
// these via add_component / set_* tools so the UI can render component cards
// before the (large) OpenAPI YAML has streamed.
export const SlimComponent = z.object({
  name: z
    .string()
    .describe("Lowercase kebab-case component name, e.g. 'user-api'"),
  componentType: z
    .enum(["service", "web-app"])
    .describe(
      "Component type: 'web-app' for frontends, 'service' for backend APIs.",
    ),
  language: z
    .string()
    .describe(
      "Primary programming language and framework, e.g. 'Go', 'TypeScript / React', 'Ballerina'",
    ),
  dependsOn: z
    .array(z.string())
    .describe(
      "Names of other components this one depends on (must match other components' 'name' values exactly)",
    ),
  entrypoint: z
    .enum(["deployment/service", "deployment/web-application"])
    .describe(
      "OpenChoreo component type: 'deployment/service' for backend APIs, 'deployment/web-application' for frontends/SPAs",
    ),
  buildpack: z.literal("docker").describe("Build strategy"),
  appPath: z
    .string()
    .describe(
      "Folder (directory) within the monorepo where this component's source code lives, relative to the repo root. This is NOT an HTTP route or API path — it is a filesystem path. Must NOT start with a leading slash. Examples: 'user-api', 'services/auth'. The coding agent will create files like '<appPath>/main.go', '<appPath>/Dockerfile', '<appPath>/workload.yaml'.",
    ),
  componentAgentInstructions: z
    .string()
    .describe(
      "Detailed implementation instructions for the Generator agent",
    ),
  api: z
    .object({
      security: z
        .enum(["required", "none"])
        .describe(
          "Legacy alias for exposesAPI.auth. 'required' enables JWT validation at the gateway; 'none' (or omitted) is a public API. Prefer 'exposesAPI' on new designs.",
        ),
    })
    .optional()
    .describe(
      "Deprecated. Use 'exposesAPI' on new designs — emitted only for backwards compatibility with old design.md files.",
    ),
  exposesAPI: z
    .object({
      managed: z
        .boolean()
        .optional()
        .describe(
          "True when the service should be routed through the WSO2 API Platform gateway (CORS + JWT validation + rate limiting). Default true when 'auth' is set.",
        ),
      auth: z
        .enum(["end-user-required", "service-required", "none"])
        .describe(
          "Caller authentication policy. 'end-user-required' = the gateway validates an end-user JWT and injects X-User-Id. 'service-required' = the gateway validates a service-to-service JWT (no end-user). 'none' = public.",
        ),
      userContext: z
        .string()
        .optional()
        .describe(
          "Header name the gateway injects upstream when 'auth' is end-user-required. Default 'X-User-Id'.",
        ),
    })
    .optional()
    .describe(
      "API exposure policy (services only). Omit for public APIs. Set 'auth: end-user-required' when callers are end users; the gateway validates the JWT and injects X-User-Id. Replaces the legacy 'api.security' field.",
    ),
  callerIdentity: z
    .object({
      mode: z
        .enum(["end-user", "service-account", "none"])
        .describe(
          "How the component identifies its caller. 'end-user' = SPA signs in users via the platform IDP (OIDC + PKCE). 'service-account' = workload-to-workload auth. 'none' = no auth.",
        ),
    })
    .optional()
    .describe(
      "Caller-identity intent. Replaces the legacy 'auth.kind: oidc-spa' for web-app components — set 'mode: end-user' instead, and the platform handles OIDC provisioning + runtime config injection.",
    ),
  auth: z
    .object({
      kind: z
        .literal("oidc-spa")
        .describe(
          "Legacy alias for callerIdentity.mode='end-user'. Kept for backwards compatibility with old design.md files.",
        ),
      upstream: z
        .string()
        .optional()
        .describe(
          "Name of the protected service this SPA signs in to call. Now optional — the platform derives upstream URLs from the dependsOn graph.",
        ),
    })
    .optional()
    .describe(
      "Deprecated. Use 'callerIdentity' on new designs. Emitted only for backwards compatibility with old design.md files.",
    ),
  dependentApis: z
    .array(DependentApi)
    .optional()
    .describe(
      "External HTTP APIs this component depends on at runtime. UNLIKE `dependsOn` (which references sibling components built by this project), these are pre-existing APIs outside the project — e.g. a corporate employee directory. They render outside the cell in the architecture diagram, and the tech-lead surfaces their URL + auth info in the coding agent's issue body. Omit when the component has no external upstreams.",
    ),
});

export type SlimComponent = z.infer<typeof SlimComponent>;

// DesignComponent — slim + openAPISpec. This is the wire shape the BFF and
// console expect at data-finish; produced by DesignDoc.materialize().
export const DesignComponent = SlimComponent.extend({
  openAPISpec: z
    .string()
    .describe("Complete OpenAPI 3.0 YAML spec for this component"),
});

export type DesignComponent = z.infer<typeof DesignComponent>;

export const ArchitectOutput = z.object({
  overview: z
    .string()
    .describe(
      "A 2-3 sentence architecture overview summarizing the system design, component structure, and communication patterns",
    ),
  components: z
    .array(DesignComponent)
    .describe("Deployable service components"),
});

export type ArchitectOutput = z.infer<typeof ArchitectOutput>;

export const ArchitectInput = z.object({
  projectName: z.string(),
  spec: z.string().describe("Specification document to design against"),
  previousDesign: ArchitectOutput.optional().describe(
    "Existing design to evolve — preserve component names and structure where possible",
  ),
  // Wireframes / domain-models live alongside the spec under
  // `specs/requirements/`. The BFF passes the raw DSL keyed by canvas
  // name (without extension); the architect calls `read_wireframe(name)`
  // on demand to pull in the DSL when a screen flow is relevant.
  wireframes: z
    .record(z.string(), z.string())
    .optional()
    .describe(
      "Map of canvas name (e.g. 'wireframes', 'domain-model') to DSL text",
    ),
  availableWireframes: z
    .array(z.string())
    .optional()
    .describe(
      "List of canvas names available via the read_wireframe tool. Mentioned in the system prompt so the model knows what to fetch.",
    ),
});

export type ArchitectInput = z.infer<typeof ArchitectInput>;
