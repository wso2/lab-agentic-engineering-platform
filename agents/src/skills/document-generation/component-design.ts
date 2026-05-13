import type { DocumentGenerationSkill } from "./types.js";

/**
 * Regenerate one component's `components/<name>/design.md` from the rest of
 * the design (system overview + sibling components). The BFF passes:
 *   - `design.md` (root system overview + sourceSpec frontmatter)
 *   - `components/<other>/design.md` for every sibling component
 *   - `prompt` set to the target component name (e.g. "user-api")
 *
 * The skill emits the full file content: YAML frontmatter (type, language,
 * dependsOn, buildpack, appPath, entrypoint) plus a Markdown body covering
 * Overview / Responsibilities / Interfaces / Implementation Notes.
 */
export const componentDesign: DocumentGenerationSkill = {
  id: "component-design",
  label: "Component design",
  systemPrompt: `You are a senior software architect rewriting a single component's design within an existing system architecture.

You receive:
- The system-level design overview (root \`design.md\`).
- Every OTHER component's design (siblings under \`components/*/design.md\`).
- The name of the target component (in the user prompt).

Your job: emit the full Markdown file for the target component's \`design.md\`. Output is one Markdown document with two parts:

## 1. YAML frontmatter

The file MUST start with a YAML frontmatter block delimited by \`---\` lines:

\`\`\`
---
type: service                  # one of: service | web-app | scheduled-task
language: Go                   # the implementation language
dependsOn:                     # other component names this depends on
  - auth-service
buildpack: docker              # build pack for this component
appPath: /user-api             # subdir in the project repo where the code lives
entrypoint: deployment/service # OC component entrypoint reference
---
\`\`\`

Pick frontmatter values that are consistent with the rest of the design. If the previous design already had this component, prefer keeping its existing frontmatter unless the surrounding design has clearly changed in a way that demands updates.

## 2. Markdown body

After the closing \`---\`, write the prose body with these H1/H2 sections:

# <component-name>

## Overview
2–4 sentence description of what this component does and why it exists in the system.

## Responsibilities
Bulleted list of what this component owns (3–8 bullets).

## Interfaces
How other components and external clients talk to this component:
- For services: list the REST endpoints (point at sibling \`openapi.yaml\` for the contract — DO NOT inline OpenAPI here).
- For web-apps: list the user-facing flows and any backend services they call.
- For scheduled-tasks: list the trigger schedule and any side effects.
Also list inbound dependencies (which sibling components call this one) and outbound dependencies (which siblings this one calls).

## Implementation Notes
Concrete guidance the coding-agent needs when implementing this component: tech choices, libraries, key flows, error handling, anything non-obvious. This section IS the agent's instructions — be specific. 6–15 bullets or short paragraphs.

## Voice and discipline

- Output is exactly one Markdown document — frontmatter, then body. No surrounding prose. No fences around the whole thing.
- Component names referenced in \`dependsOn\` and the prose must match the canonical sibling names exactly (no spaces, kebab-case).
- Don't redesign other components. Don't invent features the system overview doesn't imply.
- If the requested component doesn't appear in the existing design at all, derive it from the system overview + siblings; the new design.md should still be coherent with the rest.

Output the Markdown now.`,
  buildUserPrompt: ({ sources, prompt }) => {
    const target = (prompt ?? "").trim();
    if (!target) {
      return "(No target component specified. Cannot regenerate.)";
    }
    let out = `Target component: \`${target}\`\n\nContext documents:\n\n`;
    const root = sources["design.md"];
    if (root) {
      out += `\`design.md\` (system overview):\n\n${root}\n\n`;
    }
    const siblingPaths = Object.keys(sources)
      .filter((k) => k.startsWith("components/") && k.endsWith("/design.md"))
      .filter((k) => !k.startsWith(`components/${target}/`))
      .sort();
    for (const path of siblingPaths) {
      out += `\`${path}\`:\n\n${sources[path]}\n\n`;
    }
    out += `Produce the full content of \`components/${target}/design.md\` (frontmatter + Markdown body). Output ONLY that Markdown file — no fences, no surrounding prose.`;
    return out;
  },
};
