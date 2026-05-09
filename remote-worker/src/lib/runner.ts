import path from "node:path";
import { fileURLToPath } from "node:url";
import { query, type Query } from "@anthropic-ai/claude-agent-sdk";
import type { TaskLog } from "./logger.js";
import type { DispatchRequest } from "./types.js";
import type { WorkspaceLayout } from "./workspace.js";
import { emit } from "./progress/emitter.js";
import { progressFromSdkMessage } from "./progress/from-sdk.js";

const __dirname = path.dirname(fileURLToPath(import.meta.url));
const PLUGIN_PATH = path.resolve(__dirname, "../../plugin");

// Phase 0 allowed-tools: git, gh, build/test/lint via Bash; standard file
// tools. MCP is retired.
const ALLOWED_TOOLS = ["Read", "Write", "Edit", "Bash", "Glob", "Grep"];

export interface RunResult {
  exitCode: number;
  error?: string;
}

export interface StartedRun {
  query: Query;
  completion: Promise<RunResult>;
}

export function runClaudeQuery(
  req: DispatchRequest,
  layout: WorkspaceLayout,
  log: TaskLog,
): StartedRun {
  // Spawn env: bearer + git-service URL passed by file path / URL only.
  // No tokens cross via env, so transcripts cannot leak credentials.
  // ANTHROPIC_API_KEY flows through from process.env (container env).
  const childEnv: Record<string, string> = {
    ...(process.env as Record<string, string>),
    PATH: `${layout.asdlcDir}:${process.env.PATH ?? ""}`,
    GH_CONFIG_DIR: layout.ghConfigDir,
    ASDLC_BEARER_FILE: layout.bearerFile,
    ASDLC_GIT_SERVICE_URL: req.gitServiceUrl,
    ASDLC_CORRELATION_ID: req.correlationId ?? "",
  };

  // SDK v0.2.126 auto-discovers the bundled native binary — no
  // pathToClaudeCodeExecutable needed. settingSources: [] ensures no
  // host filesystem settings leak into the container agent.
  const q = query({
    prompt: req.prompt,
    options: {
      cwd: layout.workspace,
      plugins: [{ type: "local", path: PLUGIN_PATH }],
      allowedTools: ALLOWED_TOOLS,
      permissionMode: "bypassPermissions",
      allowDangerouslySkipPermissions: true,
      persistSession: false,
      settingSources: [],
      env: childEnv,
    },
  });

  const completion = (async (): Promise<RunResult> => {
    try {
      for await (const message of q) {
        log.write(message);
        for (const event of progressFromSdkMessage(message)) {
          emit(event);
        }
        if (message.type === "result") {
          if (message.subtype === "success") {
            return { exitCode: 0 };
          }
          const errors =
            "errors" in message && Array.isArray(message.errors)
              ? (message.errors as string[])
              : [];
          return {
            exitCode: 1,
            error: `agent result ${message.subtype}${errors.length ? ": " + errors.join(", ") : ""}`,
          };
        }
      }
      emit({ kind: "log", level: "warn", summary: "agent stream ended without result" });
      return { exitCode: 1, error: "agent stream ended without result" };
    } catch (err) {
      const msg = err instanceof Error ? err.message : String(err);
      log.write({ type: "worker_error", error: msg });
      emit({ kind: "result", status: "failure", error: msg });
      return { exitCode: 1, error: msg };
    } finally {
      log.close();
    }
  })();

  return { query: q, completion };
}
