import { z } from "zod";

export const DeveloperInput = z.object({
  projectName: z.string(),
  component: z.string().describe("Name of the component to implement"),
  instructions: z.string().describe("Implementation instructions and context"),
});

export type DeveloperInput = z.infer<typeof DeveloperInput>;

export const DeveloperOutput = z.object({
  summary: z.string().describe("Summary of what was implemented"),
  filesGenerated: z
    .array(
      z.object({
        path: z.string(),
        description: z.string(),
      }),
    )
    .describe("Files that were generated or modified"),
  notes: z
    .array(z.string())
    .describe("Implementation notes, caveats, or follow-up items"),
});

export type DeveloperOutput = z.infer<typeof DeveloperOutput>;
