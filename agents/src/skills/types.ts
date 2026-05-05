import type { Tool } from "ai";

/**
 * A skill is a composable capability that bundles related tools with
 * system prompt instructions. Skills differ from raw tools: a tool is a
 * single function the model can call, while a skill is a higher-level
 * capability that may combine multiple tools with guidance on when and
 * how to use them.
 */
export interface Skill {
  name: string;
  description: string;
  /** Prompt text appended to the agent's system prompt. */
  instructions: string;
  /** Tools this skill provides to the agent. */
  tools: Record<string, Tool>;
}
