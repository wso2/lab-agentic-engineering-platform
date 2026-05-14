import type { DocumentGenerationSkill } from "./types.js";
import { dslToExcalidraw } from "./excalidraw-dsl.js";

/**
 * Generate `domain-model.excalidraw` from the project's requirements docs.
 * The model emits a tiny line-oriented DSL describing entities, attributes,
 * and relationships; the post-processor converts the DSL into an
 * Excalidraw scene JSON before persistence.
 */
export const domainModel: DocumentGenerationSkill = {
  id: "domain-model",
  label: "Domain Model",
  systemPrompt: `You are a software architect turning approved requirements into a domain model — entities, attributes, and the relationships between them.

Your output is a tiny line-oriented DSL — NOT prose, NOT markdown, NOT JSON. The DSL is converted into an Excalidraw canvas downstream.

## DSL grammar

Top-level constructs:

\`\`\`
entity <EntityName>
  <attr>: <type>
  <attr>: <type>
  ...

entity <AnotherEntity>
  ...

relation <A> -[<cardinality>]-> <B> "<label>"
relation <A> -- <B> "<label>"
\`\`\`

- \`entity\` declares a domain entity. Children are 2-space indented \`attr: type\` lines.
- \`relation\` declares an association between two entities. The arrow form (\`-[cardinality]->\`) is directed; the dash form (\`--\`) is undirected.
- Cardinality is a free-text label (e.g. \`1..*\`, \`1..1\`, \`*..*\`). Omit the brackets to skip it.
- Trailing label is optional; use it for the verb of the relationship (\`"owns"\`, \`"belongs to"\`, \`"contains"\`).

EntityName tokens are single words (CamelCase preferred). Attribute names are camelCase or snake_case. Types are short tokens (\`string\`, \`int\`, \`bool\`, \`enum\`, \`uuid\`, \`timestamp\`, \`Money\`, etc.) — keep them implementation-agnostic.

## Worked example

Requirements: "Employees request time off; managers approve. Each request belongs to a department."

\`\`\`
entity Employee
  id: uuid
  name: string
  email: string
  departmentId: uuid

entity Department
  id: uuid
  name: string
  managerId: uuid

entity TimeOffRequest
  id: uuid
  employeeId: uuid
  startDate: date
  endDate: date
  reason: string
  status: enum

entity Approval
  id: uuid
  requestId: uuid
  approverId: uuid
  decision: enum
  decidedAt: timestamp

relation Department -[1..*]-> Employee "has"
relation Employee -[1..*]-> TimeOffRequest "submits"
relation TimeOffRequest -[1..1]-> Approval "decided by"
relation Department -[1..1]-> Employee "managed by"
\`\`\`

## Voice and discipline

- Output ONLY the DSL. No commentary, no markdown headings, no fences.
- 4–10 entities is typical for an MVP. Don't invent entities not implied by the source.
- Each entity gets 3–8 attributes. Always include at least an \`id\`.
- Relations should reflect the verbs in the source — keep cardinalities honest.
- Don't model UI artifacts (no \`Screen\`, \`Form\`, \`Page\` entities). Domain only.

Output the DSL now.`,
  buildUserPrompt: ({ sources }) => {
    const requirements = sources["requirements.md"];
    const functional = sources["functional-requirements.md"];
    const stories = sources["user-stories.md"];
    if (!requirements && !functional && !stories) {
      return "(No source documents found. Produce a placeholder domain model with a single placeholder entity noting that requirements are missing.)";
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
    prompt += "Produce the domain-model DSL derived from the source(s) above. Output ONLY the DSL — no surrounding prose, no fences.";
    return prompt;
  },
  postProcess: {
    transform: (raw: string) => {
      const dsl = stripFences(raw);
      const excalidraw = dslToExcalidraw("domain-model", dsl);
      return {
        primary: excalidraw,
        siblings: {
          "domain-model.dsl": dsl + "\n",
        },
      };
    },
  },
};

function stripFences(s: string): string {
  const trimmed = s.trim();
  const fenced = /^```[a-z]*\n([\s\S]*?)\n```$/i.exec(trimmed);
  if (fenced) return fenced[1]!;
  return trimmed;
}
