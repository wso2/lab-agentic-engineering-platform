import { z } from "zod";

export const WireframeInput = z.object({
  spec: z.string().describe("Project specification (markdown)"),
  previousSpec: z
    .string()
    .optional()
    .describe("Previous specification — when present, focus on what changed"),
});

export type WireframeInput = z.infer<typeof WireframeInput>;

export const WireframeOutput = z.object({
  content: z
    .string()
    .describe("Complete HTML document mocking up the product UI"),
});

export type WireframeOutput = z.infer<typeof WireframeOutput>;
