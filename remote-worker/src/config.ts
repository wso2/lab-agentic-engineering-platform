import os from "node:os";
import path from "node:path";

// One-shot config. Workspace base path is the only knob that needs to vary
// between local docker runs (homedir) and cluster pods (a writable mount
// inside /home/asdlc/asdlc-workspace).
export const config = {
  workspaceBasePath:
    process.env.WORKSPACE_BASE_PATH ||
    path.join(os.homedir(), "asdlc-workspace"),
};
