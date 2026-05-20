import { readFile } from "./read-file.js";
import { listDirectory } from "./list-directory.js";

export const sharedTools = {
  readFile,
  listDirectory,
} as const;

export { readFile, listDirectory };
