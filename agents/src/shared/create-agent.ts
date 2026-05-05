import { streamText, stepCountIs } from "ai";
import type { Tool } from "ai";
import { anthropic } from "@ai-sdk/anthropic";
import { config } from "./config.js";
import { sharedTools } from "../tools/index.js";
import type { AgentConfig, AgentDefinition, AgentResult } from "./types.js";
import type { Skill } from "../skills/types.js";

function buildSystemPromptWithSkills(
  basePrompt: string,
  skills: Skill[],
): string {
  if (skills.length === 0) return basePrompt;

  const skillSections = skills
    .map(
      (skill) =>
        `### ${skill.name}\n${skill.description}\n\n${skill.instructions}`,
    )
    .join("\n\n");

  return `${basePrompt}\n\n## Skills\n\n${skillSections}`;
}

function collectSkillTools(skills: Skill[]): Record<string, Tool> {
  const tools: Record<string, Tool> = {};
  for (const skill of skills) {
    Object.assign(tools, skill.tools);
  }
  return tools;
}

export function createAgent<TInput, TOutput>(
  agentConfig: AgentConfig<TInput, TOutput>,
): AgentDefinition<TInput, TOutput> {
  return {
    name: agentConfig.name,
    description: agentConfig.description,

    run: async (input: TInput): Promise<AgentResult<TOutput>> => {
      const skills = agentConfig.skills ?? [];

      const systemPrompt = buildSystemPromptWithSkills(
        agentConfig.systemPrompt,
        skills,
      );

      const tools = {
        ...sharedTools,
        ...collectSkillTools(skills),
        ...(agentConfig.tools ?? {}),
      };

      const maxSteps = agentConfig.maxSteps ?? config.maxSteps;

      console.log(`[${agentConfig.name}] starting`);

      const result = streamText({
        model: anthropic(config.model),
        system: systemPrompt,
        prompt: agentConfig.buildUserPrompt(input),
        tools,
        stopWhen: stepCountIs(maxSteps),
      });

      // Consume the stream to completion and collect the final text
      let fullText = "";
      for await (const chunk of result.textStream) {
        fullText += chunk;
      }

      const usage = await result.usage;

      const parsed = agentConfig.outputSchema.safeParse(JSON.parse(fullText));
      if (!parsed.success) {
        throw new Error(
          `[${agentConfig.name}] output validation failed: ${parsed.error.message}`,
        );
      }

      console.log(`[${agentConfig.name}] completed`);

      return {
        output: parsed.data,
        usage: {
          inputTokens: usage.inputTokens ?? 0,
          outputTokens: usage.outputTokens ?? 0,
        },
      };
    },
  };
}
