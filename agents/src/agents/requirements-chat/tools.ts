import { tool } from "ai";
import type { Tool } from "ai";
import { z } from "zod";
import { RequirementsDoc } from "./doc.js";
import { validate, type ValidationIssue } from "./validator.js";
import { WireframeElement, DomainAttribute } from "./schema.js";

// SSE sink mirrors the architect agent's pattern (architect/tools.ts:11-16).
// Tools push events to the client through this; the route forwards them
// downstream over the wire.
export interface SseSink {
  send(event: string, data: unknown): void;
  isClosed(): boolean;
}

export interface FinalizeResolver {
  finalized: boolean;
  resolve(): void;
}

const CLIENT_DISCONNECTED = { error: "client-disconnected" } as const;

function makeId(): string {
  return `tc_${Math.random().toString(36).slice(2, 10)}_${Date.now().toString(36)}`;
}

export function buildTools(
  doc: RequirementsDoc,
  sse: SseSink,
  finalizer: FinalizeResolver,
  mode: "edit" | "ask",
): Record<string, Tool> {
  const startCard = (
    name: string,
    filename: string,
    summary: string,
  ): string => {
    const id = makeId();
    sse.send("tool-started", { id, name, filename, summary });
    return id;
  };

  const finishCard = (
    id: string,
    payload: Record<string, unknown>,
  ): void => {
    sse.send("tool-result", { id, ...payload });
  };

  const failCard = (
    id: string,
    name: string,
    filename: string,
    message: string,
    errorCode = "tool_failed",
  ): { error: typeof errorCode; message: string } => {
    sse.send("tool-error", { id, name, filename, errorCode, message });
    return { error: errorCode, message };
  };

  // Wrap a write tool with the running-card + success-card + error-card
  // dance. Read tools use a leaner path.
  const writeTool =
    (name: string) =>
    <I extends { summary?: string }>(
      filename: (input: I) => string,
      run: (input: I) => ReturnType<RequirementsDoc["strReplace"]>,
    ) =>
    async (input: I) => {
      if (sse.isClosed()) return CLIENT_DISCONNECTED;
      const fn = filename(input);
      const card = startCard(name, fn, input.summary ?? "");
      try {
        const result = run(input);
        finishCard(card, {
          filename: result.filename,
          content: result.newContent,
          diff: result.diff,
        });
        return {
          ok: true,
          filename: result.filename,
          added: result.diff.added,
          removed: result.diff.removed,
        };
      } catch (err) {
        const message = err instanceof Error ? err.message : String(err);
        // Common case — uniqueness check failure. Surface a specific code so
        // the model knows to broaden context rather than retry blind.
        const code = /matched \d+ locations/i.test(message)
          ? "old_string_not_unique"
          : /not found/i.test(message)
            ? "old_string_not_found"
            : "tool_failed";
        return failCard(card, name, fn, message, code);
      }
    };

  const writeTools: Record<string, Tool> = mode === "ask" ? {} : {
    str_replace: tool({
      description:
        "Replace `oldString` with `newString` in a prose-markdown file. `oldString` must match EXACTLY ONE location in the file — include surrounding context (e.g. the preceding heading) if the snippet recurs. Use only on .md files; canvas files (.dsl / .excalidraw) need the wireframe_*/domain_* tools.",
      inputSchema: z.object({
        name: z.string().describe("Filename relative to the requirements directory (e.g. 'requirements.md')."),
        oldString: z.string().min(1),
        newString: z.string(),
        summary: z
          .string()
          .min(3)
          .describe("Past-tense one-liner shown on the chat card (e.g. 'Added Payroll feature')."),
      }),
      execute: writeTool("str_replace")(
        (i) => i.name,
        (i) => doc.strReplace(i.name, i.oldString, i.newString),
      ),
    }),

    create_file: tool({
      description:
        "Create a brand-new markdown file under specs/requirements/. Errors if the filename already exists or is not a .md file.",
      inputSchema: z.object({
        name: z.string(),
        content: z.string(),
        summary: z.string().min(3),
      }),
      execute: writeTool("create_file")(
        (i) => i.name,
        (i) => doc.createFile(i.name, i.content),
      ),
    }),

    delete_file: tool({
      description:
        "Delete a markdown file. Refuses to delete requirements.md.",
      inputSchema: z.object({
        name: z.string(),
        summary: z.string().min(3),
      }),
      execute: writeTool("delete_file")(
        (i) => i.name,
        (i) => doc.deleteFile(i.name),
      ),
    }),

    wireframe_add_screen: tool({
      description:
        "Append a new screen to wireframes.dsl. The renderer auto-lays out screens — coordinates inside each screen are relative to a 360×540 canvas.",
      inputSchema: z.object({
        name: z.string().describe("Screen name (single token, e.g. 'PayrollDashboard')."),
        elements: z.array(WireframeElement).default([]),
        summary: z.string().min(3),
      }),
      execute: writeTool("wireframe_add_screen")(
        () => "wireframes.dsl",
        (i) => doc.wireframeAddScreen(i.name, i.elements),
      ),
    }),

    wireframe_add_edge: tool({
      description: "Add a navigation edge between two existing screens in wireframes.dsl.",
      inputSchema: z.object({
        from: z.string(),
        to: z.string(),
        summary: z.string().min(3),
      }),
      execute: writeTool("wireframe_add_edge")(
        () => "wireframes.dsl",
        (i) => doc.wireframeAddEdge(i.from, i.to),
      ),
    }),

    wireframe_remove_screen: tool({
      description: "Remove a screen and any edges referencing it from wireframes.dsl.",
      inputSchema: z.object({
        name: z.string(),
        summary: z.string().min(3),
      }),
      execute: writeTool("wireframe_remove_screen")(
        () => "wireframes.dsl",
        (i) => doc.wireframeRemoveScreen(i.name),
      ),
    }),

    domain_add_entity: tool({
      description: "Append a new entity (with attributes) to domain-model.dsl.",
      inputSchema: z.object({
        name: z.string(),
        attributes: z.array(DomainAttribute).default([]),
        summary: z.string().min(3),
      }),
      execute: writeTool("domain_add_entity")(
        () => "domain-model.dsl",
        (i) => doc.domainAddEntity(i.name, i.attributes),
      ),
    }),

    domain_add_attribute: tool({
      description: "Add a single attribute to an existing entity in domain-model.dsl.",
      inputSchema: z.object({
        entity: z.string(),
        attribute: DomainAttribute,
        summary: z.string().min(3),
      }),
      execute: writeTool("domain_add_attribute")(
        () => "domain-model.dsl",
        (i) => doc.domainAddAttribute(i.entity, i.attribute),
      ),
    }),

    domain_add_relation: tool({
      description: "Add a relation between two existing entities in domain-model.dsl.",
      inputSchema: z.object({
        from: z.string(),
        to: z.string(),
        cardinality: z.string().default(""),
        label: z.string().default(""),
        summary: z.string().min(3),
      }),
      execute: writeTool("domain_add_relation")(
        () => "domain-model.dsl",
        (i) => doc.domainAddRelation(i.from, i.to, i.cardinality, i.label),
      ),
    }),

    domain_remove_entity: tool({
      description: "Remove an entity and any relations referencing it from domain-model.dsl.",
      inputSchema: z.object({
        name: z.string(),
        summary: z.string().min(3),
      }),
      execute: writeTool("domain_remove_entity")(
        () => "domain-model.dsl",
        (i) => doc.domainRemoveEntity(i.name),
      ),
    }),
  };

  return {
    ...writeTools,

    read_file: tool({
      description:
        "Read a file's CURRENT content. Use ONLY for files NOT listed under 'Files in scope' in the user message (they're already inlined there). No SSE card is emitted; this is a silent read.",
      inputSchema: z.object({ name: z.string() }),
      execute: async ({ name }) => {
        if (sse.isClosed()) return CLIENT_DISCONNECTED;
        try {
          return { content: doc.read(name) };
        } catch (err) {
          return {
            error: "not_found",
            message: err instanceof Error ? err.message : String(err),
          };
        }
      },
    }),

    finish: tool({
      description:
        "End the turn. Runs a validator over the bundle; on failure, returns the issues so you can fix them. On success, emits the final summary to the user.",
      inputSchema: z.object({
        summary: z.string().min(1).describe("One short sentence wrapping up the turn."),
      }),
      execute: async ({ summary }) => {
        if (sse.isClosed()) return CLIENT_DISCONNECTED;
        const issues: ValidationIssue[] = validate(doc);
        if (issues.length > 0) {
          sse.send("validation-failed", { issues });
          return { error: "validation", issues };
        }
        if (!finalizer.finalized) {
          finalizer.finalized = true;
          sse.send("finish", {
            summary,
            touched: doc.touchedFiles(),
          });
          finalizer.resolve();
        }
        return { ok: true, finalized: true };
      },
    }),
  };
}
