/**
 * A document-generation skill produces one Markdown document from optional
 * sibling source documents and an optional user prompt. Skills are routed
 * by `id` from the BFF (the document-type registry holds the mapping).
 *
 * Each skill is one file under `skills/document-generation/` so the prompts
 * are easy to review and tweak without touching the agent factory or the
 * generic skill route.
 */
export interface DocumentGenerationSkill {
  /** Stable identifier used in the URL path: /v1/agents/document-generation/{id}. */
  id: string;
  /** Human-readable label for logs / docs. */
  label: string;
  /** Prompt text used as the model's system message. */
  systemPrompt: string;
  /**
   * Build the user prompt from the sources + optional user-supplied prompt.
   * Sources are filename → content. Skills decide which keys they care
   * about; missing keys are tolerated (the BFF passes whatever exists).
   */
  buildUserPrompt(input: SkillInput): string;
}

export interface SkillInput {
  sources: Record<string, string>;
  prompt?: string;
}
