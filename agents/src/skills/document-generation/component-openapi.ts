import type { DocumentGenerationSkill } from "./types.js";

/**
 * Regenerate `components/<name>/openapi.yaml` from the matching component's
 * `design.md`. The BFF passes the design.md content as the source and the
 * component name in the prompt. Only meaningful for `type: service`
 * components — the BFF should not invoke this skill for web-apps.
 */
export const componentOpenApi: DocumentGenerationSkill = {
  id: "component-openapi",
  label: "Component OpenAPI spec",
  systemPrompt: `You are an API designer producing a precise OpenAPI 3.0.3 specification for a single service component.

You receive the component's \`design.md\` (frontmatter + Markdown body) and the component name. Your job: emit a complete OpenAPI 3.0.3 YAML document describing the component's HTTP contract.

## Discipline

- Output is exactly one OpenAPI 3.0.3 YAML document. No surrounding prose, no Markdown headings, no fences.
- The top of the file MUST be \`openapi: 3.0.3\`.
- Set \`info.title\` to the component name and \`info.version\` to a sensible value (e.g. "0.1.0").
- Cover every endpoint implied by the component's Responsibilities and Interfaces sections.
- Include request/response schemas under \`components.schemas\` with reasonable types and required fields. Reuse schemas via \`$ref\` instead of inlining duplicates.
- Use canonical HTTP verbs and status codes (200/201/204 for success; 400/401/403/404/409 for client errors; 500 for server errors).
- For internal-service endpoints, document any auth scheme under \`components.securitySchemes\` and reference it from each operation.
- Don't invent endpoints the design doesn't mention. If the design references a flow generally (e.g. "exposes CRUD for users"), produce the obvious REST shape.

Output the YAML now.`,
  buildUserPrompt: ({ sources, prompt }) => {
    const target = (prompt ?? "").trim();
    if (!target) {
      return "(No target component specified. Cannot regenerate.)";
    }
    const designPath = `components/${target}/design.md`;
    const designMd = sources[designPath];
    if (!designMd) {
      return `(No design.md found at ${designPath}. Cannot regenerate OpenAPI without it.)`;
    }
    return `Target component: \`${target}\`

\`${designPath}\`:

${designMd}

Produce the full \`components/${target}/openapi.yaml\` content. Output ONLY the YAML document — no fences, no surrounding prose.`;
  },
};
