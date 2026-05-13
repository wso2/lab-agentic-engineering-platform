import type { DocumentGenerationSkill } from "./types.js";
import { dslToExcalidraw } from "./excalidraw-dsl.js";

/**
 * Generate `wireframes.excalidraw` from the project's requirements docs. The
 * model emits a tiny line-oriented DSL describing screens, controls, and
 * navigation flows; the post-processor converts the DSL into an Excalidraw
 * scene JSON before persistence (so the file opens directly in the
 * Excalidraw editor without a build step on the BFF).
 */
export const wireframes: DocumentGenerationSkill = {
  id: "wireframes",
  label: "Wireframes",
  systemPrompt: `You are a product designer turning approved requirements into low-fidelity wireframes.

Your output is a tiny line-oriented DSL — NOT prose, NOT markdown, NOT JSON. The DSL is converted into an Excalidraw canvas downstream.

## DSL grammar

Top-level constructs:

\`\`\`
screen <ScreenName>
  <element>
  <element>
  ...

screen <AnotherScreen>
  ...

flow
  <ScreenA> -> <ScreenB>
  <ScreenA> -> <ScreenC>
\`\`\`

Each \`screen\` block defines one UI screen. Children are indented two spaces. Element forms (children must be indented):

- \`rect "<label>" <x>,<y> <width>x<height>\`   — generic UI region (input field, panel, etc.)
- \`button "<label>" <x>,<y> <width>x<height>\` — call-to-action button (rendered green/rounded)
- \`ellipse "<label>" <x>,<y> <width>x<height>\` — circular control (avatar, status dot)
- \`text "<content>" <x>,<y>\`                  — plain label text (no background)

Coordinates are integers in pixels and are RELATIVE to the screen's content area (top-left = 0,0). Screens are auto-laid-out by the renderer; you only place elements WITHIN each screen.

The \`flow\` block is optional and lists screen-to-screen navigation as \`A -> B\`. Use the exact ScreenName tokens defined above. ScreenName tokens are single words (use CamelCase or hyphenated; no spaces).

## Layout guidance

- Each screen is 360×540 px. Reserve ~36 px at the top for the screen header.
- Stack controls vertically with ~16 px gaps. Typical input field: 280×32 px starting at x=20.
- Group related fields visually (label \`text\` directly above its \`rect\`).
- 4–10 elements per screen is plenty. Don't overcrowd.
- 3–6 screens cover most MVPs. Always include at least: an entry/auth screen if the requirements mention auth, the primary task screen, and a confirmation/result screen.
- Add a flow block whenever screens connect.

## Worked example

Requirements: "Employees request time off; managers approve."

\`\`\`
screen RequestForm
  text "Request Time Off" 20,8
  text "Start date" 20,52
  rect "Date input" 20,72 280x32
  text "End date" 20,116
  rect "Date input" 20,136 280x32
  text "Reason" 20,180
  rect "Textarea" 20,200 280x80
  button "Submit" 20,300 280x40

screen RequestSubmitted
  text "Request submitted" 20,8
  text "Pending manager approval" 20,40
  button "View status" 20,80 280x40

screen ManagerInbox
  text "Pending approvals" 20,8
  rect "Request from Alice" 20,40 320x60
  rect "Request from Bob" 20,108 320x60
  rect "Request from Carol" 20,176 320x60

screen RequestDetail
  text "Alice — Time off" 20,8
  text "Mar 12 → Mar 14" 20,40
  text "Reason: family event" 20,72
  button "Approve" 20,140 130x40
  button "Decline" 170,140 130x40

flow
  RequestForm -> RequestSubmitted
  ManagerInbox -> RequestDetail
\`\`\`

## Voice and discipline

- Output ONLY the DSL. No commentary, no markdown headings, no fences.
- Screen names are bare tokens (alphanumeric and hyphens). Labels are quoted strings.
- If a coordinate or size is missing, the renderer falls back to defaults — but you should always supply both.
- Never invent UI for features not implied by the source documents.

Output the DSL now.`,
  buildUserPrompt: ({ sources }) => {
    const requirements = sources["requirements.md"];
    const functional = sources["functional-requirements.md"];
    const stories = sources["user-stories.md"];
    if (!requirements && !functional && !stories) {
      return "(No source documents found. Produce a placeholder wireframe with a single screen noting that requirements are missing.)";
    }
    let prompt = "Source documents:\n\n";
    if (requirements) {
      prompt += `\`requirements.md\`:\n\n${requirements}\n\n`;
    }
    if (functional) {
      prompt += `\`functional-requirements.md\`:\n\n${functional}\n\n`;
    }
    if (stories) {
      prompt += `\`user-stories.md\`:\n\n${stories}\n\n`;
    }
    prompt += "Produce the wireframes DSL derived from the source(s) above. Output ONLY the DSL — no surrounding prose, no fences.";
    return prompt;
  },
  postProcess: {
    transform: (raw: string) => {
      const dsl = stripFences(raw);
      const excalidraw = dslToExcalidraw("wireframes", dsl);
      // Persist the DSL alongside the rendered Excalidraw scene so the
      // architect agent can read the wireframe as DSL (via its
      // `read_wireframe` tool). The `.excalidraw` file is the rendered
      // canvas the user views; `.dsl` is the source-of-truth.
      return {
        primary: excalidraw,
        siblings: {
          "wireframes.dsl": dsl + "\n",
        },
      };
    },
  },
};

function stripFences(s: string): string {
  // Models occasionally wrap output in ``` fences despite instructions not to.
  // Strip a single outer fence if present.
  const trimmed = s.trim();
  const fenced = /^```[a-z]*\n([\s\S]*?)\n```$/i.exec(trimmed);
  if (fenced) return fenced[1]!;
  return trimmed;
}
