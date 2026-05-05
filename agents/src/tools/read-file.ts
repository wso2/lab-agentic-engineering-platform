import { tool } from "ai";
import { z } from "zod";
import { readFile as fsReadFile } from "node:fs/promises";

export const readFile = tool({
  description:
    "Read the contents of a file at the given absolute path. Returns the file text or an error message if the file cannot be read.",
  inputSchema: z.object({
    path: z.string().describe("Absolute path to the file to read"),
  }),
  execute: async ({ path }) => {
    try {
      const content = await fsReadFile(path, "utf-8");
      return { success: true as const, content };
    } catch (err) {
      const message = err instanceof Error ? err.message : String(err);
      return { success: false as const, error: message };
    }
  },
});
