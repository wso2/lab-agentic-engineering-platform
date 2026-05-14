import type { DocumentGenerationSkill } from "./types.js";

/**
 * Derive `user-stories.md` from `requirements.md` (and optionally
 * `functional-requirements.md` if present). User stories reframe the same
 * product from the user's perspective with acceptance criteria.
 */
export const userStories: DocumentGenerationSkill = {
  id: "user-stories",
  label: "User stories",
  systemPrompt: `You are a product manager turning approved requirements into user stories with acceptance criteria.

Input: the product's high-level \`requirements.md\` and optionally a more detailed \`functional-requirements.md\`. Your job: rewrite the same scope as user stories that engineers and designers can refine and estimate.

## Output structure

Produce Markdown with these sections, in order:

# Overview
One short paragraph (2-3 sentences) describing the product and the personas the stories cover. Pull persona names directly from the source.

# Stories
Group stories under H2 personas (e.g. \`## Employee\`, \`## Manager\`). Inside each persona group, list stories using this format:

### US-{N}: {Short title}

**As a** {persona},
**I want** {capability},
**so that** {benefit}.

**Acceptance criteria** (Gherkin-flavoured Given/When/Then):
- Given {context}, when {action}, then {outcome}.
- Given {context}, when {action}, then {outcome}.
- ...

Number stories sequentially (US-1, US-2, …). Aim for 8-20 stories total. Each story needs 2-5 acceptance criteria — concrete, testable, no marketing language.

# Out of Scope
Bulleted list of stories explicitly excluded from this iteration. Mirror the source's "what to leave out" stance — when the source omits something, leave it omitted here too.

## Discipline

- Persona names match the source. Don't invent new personas.
- Stories cover what users *do*, not what the system does internally.
- Acceptance criteria are observable from the user's view, not implementation details.
- Where \`functional-requirements.md\` is supplied, use it to refine the criteria — but don't repeat FR text verbatim. Stories are user-shaped; FRs are system-shaped.

## Hard caps

- Stories: 8-20.
- Acceptance criteria per story: 2-5.
- No paragraphs inside acceptance criteria.

Output only the Markdown. No surrounding prose. No code fences.`,
  buildUserPrompt: ({ sources }) => {
    const requirements = sources["requirements.md"];
    const functional = sources["functional-requirements.md"];
    if (!requirements) {
      return "(No requirements.md found. Produce a placeholder noting that the main requirements document is missing.)";
    }
    let prompt = `Source document — \`requirements.md\`:\n\n${requirements}\n\n`;
    if (functional) {
      prompt += `Additional source — \`functional-requirements.md\`:\n\n${functional}\n\n`;
    }
    prompt += `Produce the user-stories document derived from the source(s) above.`;
    return prompt;
  },
};
