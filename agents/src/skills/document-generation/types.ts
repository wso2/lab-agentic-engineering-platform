/**
 * A document-generation skill produces one document from optional sibling
 * source documents and an optional user prompt. Skills are routed by `id`
 * from the BFF (the document-type registry holds the mapping).
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
  /**
   * Optional post-processor applied once the LLM stream finishes. The skill
   * route runs `transform(accumulatedDeltas)` and emits the result as a
   * final `text-delta` carrying `replace: true`, signalling the BFF to
   * discard its accumulated buffer and persist this payload instead.
   *
   * Used by skills whose persisted format differs from the LLM's natural
   * output — e.g. wireframes/domain-model emit a small DSL that gets
   * converted to Excalidraw scene JSON before storage.
   */
  postProcess?: SkillPostProcessor;
}

export interface SkillInput {
  sources: Record<string, string>;
  prompt?: string;
}

/**
 * Output of a SkillPostProcessor.transform call. Either the persisted
 * payload as a single string (legacy / single-file skills) or an object
 * with the primary file content plus optional sibling files (multi-file
 * skills like wireframes/domain-model that write both `.dsl` and
 * `.excalidraw`).
 */
export type SkillPostProcessOutput = string | {
  /** Content written to the primary target file (the one in the request URL). */
  primary: string;
  /** Additional files keyed by filename to write alongside the primary. */
  siblings: Record<string, string>;
};

export interface SkillPostProcessor {
  /** Transform the accumulated raw LLM output into the persisted payload. */
  transform(raw: string): SkillPostProcessOutput;
}
