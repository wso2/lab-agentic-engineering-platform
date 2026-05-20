import type { DocumentGenerationSkill } from "./types.js";
import { systemPrompt as baSystemPrompt } from "../../agents/business-analyst/prompt.js";

/**
 * Bootstrap `requirements.md` from a free-text user prompt. This is the
 * starting point of the spec — every other document derives from it.
 *
 * The system prompt is the existing business-analyst prompt (lifted from
 * `agents/business-analyst/prompt.ts`); future tweaks happen in that file.
 */
export const requirementsFromPrompt: DocumentGenerationSkill = {
  id: "requirements-from-prompt",
  label: "Requirements from prompt",
  systemPrompt: baSystemPrompt,
  buildUserPrompt: ({ prompt }) => {
    return prompt?.trim() ?? "";
  },
};
