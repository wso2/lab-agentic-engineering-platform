import { tool } from "ai";
import { z } from "zod";
import { readdir } from "node:fs/promises";
import { join, relative } from "node:path";

export const searchFiles = tool({
  description:
    "Search for files matching a glob-like pattern within a directory. " +
    "Supports simple patterns: '*' matches any filename segment, '**' matches nested directories. " +
    "Returns matching file paths relative to the search root.",
  inputSchema: z.object({
    directory: z.string().describe("Absolute path to the directory to search"),
    pattern: z
      .string()
      .describe(
        "File name pattern to match (e.g. '*.ts', '*.test.ts', 'index.*')",
      ),
    maxDepth: z
      .number()
      .optional()
      .default(5)
      .describe("Maximum directory depth to search (default: 5)"),
  }),
  execute: async ({ directory, pattern, maxDepth }) => {
    const matches: string[] = [];
    const regex = new RegExp(
      "^" +
        pattern
          .replace(/\./g, "\\.")
          .replace(/\*\*/g, "§§")
          .replace(/\*/g, "[^/]*")
          .replace(/§§/g, ".*") +
        "$",
    );

    async function walk(dir: string, depth: number): Promise<void> {
      if (depth > maxDepth) return;
      try {
        const entries = await readdir(dir, { withFileTypes: true });
        for (const entry of entries) {
          const fullPath = join(dir, entry.name);
          if (entry.isDirectory()) {
            if (entry.name === "node_modules" || entry.name === ".git") {
              continue;
            }
            await walk(fullPath, depth + 1);
          } else if (regex.test(entry.name)) {
            matches.push(relative(directory, fullPath));
          }
        }
      } catch {
        // Skip directories we can't read
      }
    }

    try {
      await walk(directory, 0);
      return { success: true as const, matches };
    } catch (err) {
      const message = err instanceof Error ? err.message : String(err);
      return { success: false as const, error: message };
    }
  },
});
