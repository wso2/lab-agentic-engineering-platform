import type { Skill } from "../types.js";
import { readFile } from "../../tools/read-file.js";
import { listDirectory } from "../../tools/list-directory.js";
import { searchFiles } from "./tools.js";
import { instructions } from "./prompt.js";

export const codebaseExploration: Skill = {
  name: "Codebase Exploration",
  description:
    "Enables systematic navigation and understanding of codebases through file reading, directory listing, and pattern-based file search.",
  instructions,
  tools: {
    readFile,
    listDirectory,
    searchFiles,
  },
};
