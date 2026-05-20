import { tool } from "ai";
import { z } from "zod";
import { readdir } from "node:fs/promises";

export const listDirectory = tool({
  description:
    "List the files and directories at the given path. Returns entries with their names and types (file or directory).",
  inputSchema: z.object({
    path: z.string().describe("Absolute path to the directory to list"),
  }),
  execute: async ({ path }) => {
    try {
      const entries = await readdir(path, { withFileTypes: true });
      const items = entries.map((entry) => ({
        name: entry.name,
        type: entry.isDirectory() ? ("directory" as const) : ("file" as const),
      }));
      return { success: true as const, entries: items };
    } catch (err) {
      const message = err instanceof Error ? err.message : String(err);
      return { success: false as const, error: message };
    }
  },
});
