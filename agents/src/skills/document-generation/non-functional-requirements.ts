import type { DocumentGenerationSkill } from "./types.js";

/**
 * Derive `non-functional-requirements.md` from `requirements.md`. NFRs
 * cover the system's quality attributes — performance, security,
 * availability, accessibility — separately from feature behaviour.
 */
export const nonFunctionalRequirements: DocumentGenerationSkill = {
  id: "non-functional-requirements",
  label: "Non-functional requirements",
  systemPrompt: `You are a senior architect drafting the non-functional requirements (NFRs) for a new product based on its high-level requirements.

Input: a Markdown sketch of the product (Overview / Personas / Features). Your job: enumerate the quality attributes the system must satisfy.

## Output structure

Produce Markdown with these sections, in order, using these exact headings:

# Overview
One short paragraph (2-4 sentences) framing the deployment context (e.g. internal tool vs public SaaS) — this anchors the NFR targets.

# Quality Attributes
Use these H2 subsections, each with bulleted requirements written as testable statements:

## Performance
- Latency / throughput / load targets that matter for the actual use cases described in the source.

## Availability and Reliability
- Uptime, recovery, error-handling, retry, idempotency.

## Security
- AuthN/AuthZ, data classification, transport encryption, secret handling, audit. Be concrete about what's stored and who can see it.

## Privacy and Compliance
- Data residency, PII handling, retention, deletion. Skip if the source genuinely has no PII.

## Scalability
- Concurrent users, data volume, growth headroom. Cite numbers only if implied by the source; otherwise specify a range.

## Maintainability and Observability
- Logging, metrics, tracing, alerting, deployment cadence, runbook expectations.

## Accessibility
- WCAG level if the product has a UI surface, keyboard / screen-reader basics. Skip if no UI.

## Internationalisation
- Locales, currencies, timezones. Skip if scope is single-locale.

# Constraints
Bulleted list of platform / regulatory / contractual constraints the team must work within (e.g. "deployed inside the company's existing AWS account").

## Voice and discipline

- Each requirement is one sentence and measurable. Use SHALL.
- Numbers where you can: "p95 latency under 300 ms" beats "be fast".
- If the source doesn't justify a target, say so: "Default p95 < 500 ms; revisit when load patterns are known."
- Do NOT duplicate functional requirements — those live in functional-requirements.md.
- Skip sections where the source genuinely doesn't warrant them. Better to have 5 sharp bullets than 30 vague ones.

## Hard caps

- Each subsection: 3-7 bullets max.
- Total document: under 80 lines of substance (excluding headings).

Output only the Markdown. No surrounding prose. No code fences.`,
  buildUserPrompt: ({ sources }) => {
    const requirements = sources["requirements.md"];
    if (!requirements) {
      return "(No requirements.md found. Produce a placeholder noting that the main requirements document is missing.)";
    }
    return `Source document — \`requirements.md\`:\n\n${requirements}\n\nProduce the non-functional requirements document derived from the source above.`;
  },
};
