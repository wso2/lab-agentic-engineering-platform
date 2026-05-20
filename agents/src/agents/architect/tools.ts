import { tool } from "ai";
import type { Tool } from "ai";
import { z } from "zod";
import { DesignDoc } from "./doc.js";
import { DependentApi, SlimComponent } from "./schema.js";
import { validate, type ValidationIssue } from "./validator.js";

// Side-channel for tools to push SSE events to the client. Each tool emits at
// most one event; events are name-keyed and idempotent under reorder, so we
// don't need a mutex around execute().
export interface SseSink {
  send(event: string, data: unknown): void;
  // True after the response stream is closed (client disconnect). Tools
  // short-circuit when the socket is gone so we stop burning model time.
  isClosed(): boolean;
}

// FinalizeResolver — the route waits on this. When finalize() validates and
// emits data-finish, it also resolves so streamText loop can exit cleanly.
export interface FinalizeResolver {
  finalized: boolean;
  resolve(): void;
}

// `result` shape returned to the model. Keep these stable strings — the
// system prompt instructs the model to react to specific values.
type ToolResult = Record<string, unknown>;

const CLIENT_DISCONNECTED: ToolResult = {
  error: "client-disconnected",
};

export function buildTools(
  doc: DesignDoc,
  sse: SseSink,
  finalizer: FinalizeResolver,
  wireframes: Record<string, string> = {},
): Record<string, Tool> {
  const guard = (run: () => ToolResult): ToolResult => {
    if (sse.isClosed()) return CLIENT_DISCONNECTED;
    try {
      return run();
    } catch (err) {
      return {
        error: "tool-failed",
        message: err instanceof Error ? err.message : String(err),
      };
    }
  };

  return {
    set_overview: tool({
      description: "Replace the project overview text.",
      inputSchema: z.object({
        text: z
          .string()
          .describe(
            "2-3 sentence architecture overview summarizing system design and component structure",
          ),
      }),
      execute: async ({ text }) =>
        guard(() => {
          doc.setOverview(text);
          sse.send("overview", { text });
          return { ok: true };
        }),
    }),

    add_component: tool({
      description:
        "Add a new component. Fails if a component with the same name already exists.",
      inputSchema: SlimComponent,
      execute: async (slim) =>
        guard(() => {
          if (doc.hasComponent(slim.name)) {
            return {
              error: "name-exists",
              message:
                "To modify, use the surgical setters; to replace, call remove_component first.",
            };
          }
          doc.addComponent(slim);
          sse.send("component-added", { component: slim });
          return { ok: true };
        }),
    }),

    remove_component: tool({
      description: "Remove a component by name. Clears its pending spec.",
      inputSchema: z.object({ name: z.string() }),
      execute: async ({ name }) =>
        guard(() => {
          if (!doc.hasComponent(name)) {
            return { error: "not-found", message: `${name} does not exist` };
          }
          doc.removeComponent(name);
          sse.send("component-removed", { name });
          return { ok: true };
        }),
    }),

    set_agent_instructions: tool({
      description:
        "Replace componentAgentInstructions for a component. Does NOT invalidate openapi (instruction-only edits do not require a spec re-emit).",
      inputSchema: z.object({
        name: z.string(),
        text: z.string(),
      }),
      execute: async ({ name, text }) =>
        guard(() => {
          if (!doc.hasComponent(name)) {
            return { error: "not-found" };
          }
          doc.setAgentInstructions(name, text);
          sse.send("component-updated", {
            name,
            patch: { componentAgentInstructions: text },
            openapiInvalidated: false,
          });
          return { ok: true, openapiInvalidated: false };
        }),
    }),

    set_language: tool({
      description: "Set the language/framework for a component. Invalidates its openapi.",
      inputSchema: z.object({
        name: z.string(),
        language: z.string(),
      }),
      execute: async ({ name, language }) =>
        guard(() => {
          if (!doc.hasComponent(name)) {
            return { error: "not-found" };
          }
          doc.setLanguage(name, language);
          sse.send("component-updated", {
            name,
            patch: { language },
            openapiInvalidated: true,
          });
          return { ok: true, openapiInvalidated: true };
        }),
    }),

    add_dependency: tool({
      description:
        "Add a dependsOn entry to a component. Idempotent (duplicate adds are silently ignored). Invalidates the component's openapi (contract drift).",
      inputSchema: z.object({
        name: z.string(),
        dependsOn: z.string(),
      }),
      execute: async ({ name, dependsOn }) =>
        guard(() => {
          if (!doc.hasComponent(name)) {
            return { error: "not-found" };
          }
          doc.addDependency(name, dependsOn);
          const after = doc.getComponent(name);
          sse.send("component-updated", {
            name,
            patch: { dependsOn: after.slim.dependsOn },
            openapiInvalidated: true,
          });
          return { ok: true, openapiInvalidated: true };
        }),
    }),

    remove_dependency: tool({
      description:
        "Remove a dependsOn entry. No-op if the dep is not present. Invalidates the component's openapi.",
      inputSchema: z.object({
        name: z.string(),
        dependsOn: z.string(),
      }),
      execute: async ({ name, dependsOn }) =>
        guard(() => {
          if (!doc.hasComponent(name)) {
            return { error: "not-found" };
          }
          doc.removeDependency(name, dependsOn);
          const after = doc.getComponent(name);
          sse.send("component-updated", {
            name,
            patch: { dependsOn: after.slim.dependsOn },
            openapiInvalidated: true,
          });
          return { ok: true, openapiInvalidated: true };
        }),
    }),

    add_dependent_api: tool({
      description:
        "Declare an EXTERNAL HTTP API the component depends on at runtime — i.e. an API that already exists outside this project (corporate directory, payments processor, third-party SaaS). Use this for any upstream NOT built by this project; use add_dependency for sibling components that are. Idempotent on `dependentApi.name`. Does NOT invalidate openapi.",
      inputSchema: z.object({
        name: z
          .string()
          .describe("Name of the component that consumes the external API."),
        dependentApi: DependentApi,
      }),
      execute: async ({ name, dependentApi }) =>
        guard(() => {
          if (!doc.hasComponent(name)) {
            return { error: "not-found" };
          }
          doc.addDependentApi(name, dependentApi);
          const after = doc.getComponent(name);
          sse.send("component-updated", {
            name,
            patch: { dependentApis: after.slim.dependentApis },
            openapiInvalidated: false,
          });
          return { ok: true, openapiInvalidated: false };
        }),
    }),

    remove_dependent_api: tool({
      description:
        "Remove an external dependent API by name. No-op if not present.",
      inputSchema: z.object({
        name: z.string().describe("Name of the component."),
        dependentApiName: z
          .string()
          .describe("`name` field of the DependentApi to remove."),
      }),
      execute: async ({ name, dependentApiName }) =>
        guard(() => {
          if (!doc.hasComponent(name)) {
            return { error: "not-found" };
          }
          doc.removeDependentApi(name, dependentApiName);
          const after = doc.getComponent(name);
          sse.send("component-updated", {
            name,
            patch: { dependentApis: after.slim.dependentApis },
            openapiInvalidated: false,
          });
          return { ok: true, openapiInvalidated: false };
        }),
    }),

    set_openapi: tool({
      description:
        "Set the OpenAPI 3.0.3 YAML for a 'service' component. Rejected with {error:'not-applicable'} for 'web-app' components — frontends have no wire contract. If the new spec is semantically equal to the current one, returns {changed: false} and emits no SSE event — do not retry in that case.",
      inputSchema: z.object({
        name: z.string(),
        contents: z.string().describe("Full OpenAPI 3.0.3 YAML"),
      }),
      execute: async ({ name, contents }) =>
        guard(() => {
          if (!doc.hasComponent(name)) {
            return { error: "not-found" };
          }
          const entry = doc.getComponent(name);
          if (entry.slim.componentType === "web-app") {
            return {
              error: "not-applicable",
              message:
                "web-app components do not get an OpenAPI spec — describe screens / flows / which services they call in componentAgentInstructions instead.",
            };
          }
          const result = doc.setOpenApi(name, contents);
          if (result.changed) {
            sse.send("component-spec-updating", { name });
            return { changed: true };
          }
          return { changed: false, reason: result.reason };
        }),
    }),

    get_component: tool({
      description:
        "Read a component's current state. Returns slim metadata and the raw OpenAPI YAML (or null if pending).",
      inputSchema: z.object({ name: z.string() }),
      execute: async ({ name }) =>
        guard(() => {
          if (!doc.hasComponent(name)) {
            return { error: "not-found" };
          }
          return doc.getComponent(name);
        }),
    }),

    read_wireframe: tool({
      description:
        "Read a wireframe / domain-model canvas as DSL text. Use this to fetch the screen flows or entity model when designing components — only call when the spec mentions a relevant canvas. Returns {dsl: string} on success or {error:'not-found'} if no canvas with that name was supplied.",
      inputSchema: z.object({
        name: z
          .string()
          .describe(
            "Canvas name without extension, e.g. 'wireframes' or 'domain-model'",
          ),
      }),
      execute: async ({ name }) =>
        guard(() => {
          const dsl = wireframes[name];
          if (!dsl) {
            return {
              error: "not-found",
              message: `No wireframe DSL named ${name}. Available: ${Object.keys(wireframes).join(", ") || "(none)"}`,
            };
          }
          return { name, dsl };
        }),
    }),

    finalize: tool({
      description:
        "End the session. Runs the validator. On validation failure, returns {error:'validation', issues:[...]} for you to address. On success, emits data-finish.",
      inputSchema: z.object({}),
      execute: async () =>
        guard(() => {
          const issues: ValidationIssue[] = validate(doc);
          if (issues.length > 0) {
            return { error: "validation", issues };
          }
          if (!finalizer.finalized) {
            finalizer.finalized = true;
            sse.send("finish", { design: doc.materialize() });
            finalizer.resolve();
          }
          return { ok: true, finalized: true };
        }),
    }),
  };
}
