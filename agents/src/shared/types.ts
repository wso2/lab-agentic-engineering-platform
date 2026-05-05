import type { Tool } from "ai";
import type { Skill } from "../skills/types.js";

export interface AgentDefinition<TInput, TOutput> {
  name: string;
  description: string;
  run: (input: TInput) => Promise<AgentResult<TOutput>>;
}

export interface AgentResult<T> {
  output: T;
  usage: { inputTokens: number; outputTokens: number };
}

export interface AgentConfig<TInput, TOutput> {
  name: string;
  description: string;
  systemPrompt: string;
  buildUserPrompt: (input: TInput) => string;
  outputSchema: import("zod").ZodType<TOutput>;
  tools?: Record<string, Tool>;
  skills?: Skill[];
  maxSteps?: number;
}
