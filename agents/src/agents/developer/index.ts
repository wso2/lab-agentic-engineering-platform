import { createAgent } from "../../shared/create-agent.js";
import { codebaseExploration } from "../../skills/index.js";
import { DeveloperOutput } from "./schema.js";
import type { DeveloperInput } from "./schema.js";
import { systemPrompt, buildUserPrompt } from "./prompt.js";

export const developer = createAgent<DeveloperInput, DeveloperOutput>({
  name: "developer",
  description: "Implements components based on instructions and existing code context",
  systemPrompt,
  buildUserPrompt,
  outputSchema: DeveloperOutput,
  skills: [codebaseExploration],
});

export { DeveloperInput, DeveloperOutput } from "./schema.js";
