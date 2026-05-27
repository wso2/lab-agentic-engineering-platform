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

// PerTaskSkills carries the materialised AgentSkills plugin into the SDK
// query options. PR 3 of docs/design/skills-system.md.
//
// `skillsPluginDir` is the absolute path to .asdlc/skills-plugin/. If
// set, runner.ts adds a second `{type:"local"}` plugin entry pointing
// at it. `preloadBuiltinNames` lists the materialised names of every
// `kind: builtin` skill in that plugin, which we push into the SDK's
// `skills:` array so their full bodies inject at startup. Custom and
// imported skills sit in the same plugin and surface via the SDK's
// standard skill listing (description in context, body on invoke) —
// they are NOT in the preload array. See the design doc's
// "Why skills: <built-in names> and not skills: 'all'" rationale.
export interface PerTaskSkills {
  skillsPluginDir?: string;
  preloadBuiltinNames: string[];
}

export function runClaudeQuery(
  req: DispatchRequest,
  layout: WorkspaceLayout,
  log: TaskLog,
  perTaskSkills?: PerTaskSkills,
): StartedRun {
  // Spawn env: bearer + git-service URL passed by file path / URL only.
  // No tokens cross via env, so transcripts cannot leak credentials.
  // ANTHROPIC_API_KEY flows through from process.env (container env).
  // F3c — surface ASDLC_TASK_ID and ASDLC_PLATFORM_URL to the agent's
  // child env so the asdlc skill's verification-failed shell snippet can
  // hit POST $ASDLC_PLATFORM_URL/api/v1/tasks/$ASDLC_TASK_ID/verification-failed.
  // The bearer rides through a file (ASDLC_BEARER_FILE) so the agent's
  // SDK transcripts can't leak it; the curl snippet reads the file at
  // call time.
  const childEnv: Record<string, string> = {
    ...(process.env as Record<string, string>),
    PATH: `${layout.asdlcDir}:${process.env.PATH ?? ""}`,
    GH_CONFIG_DIR: layout.ghConfigDir,
    ASDLC_BEARER_FILE: layout.bearerFile,
    ASDLC_TASK_ID: req.taskId,
    ASDLC_PLATFORM_URL: process.env.ASDLC_PLATFORM_URL ?? "",
    ASDLC_GIT_SERVICE_URL: req.gitServiceUrl,
    ASDLC_CORRELATION_ID: req.correlationId ?? "",
  };

  // Two-tier plugin list: the base `asdlc` plugin (workflow + base
  // conventions) is always loaded; the per-task `asdlc-task-skills`
  // plugin (project-attached skills) is loaded conditionally when
  // workspace.ts materialised it. Per-task built-ins land in the
  // `skills:` preload so the SDK injects their full bodies at startup;
  // custom + imported sit in the same plugin and surface via the SDK's
  // standard discovery (description in context, body on invoke).
  const plugins: Array<{ type: "local"; path: string }> = [
    { type: "local", path: PLUGIN_PATH },
  ];
  const skillPreload: string[] = ["asdlc:asdlc"];
  if (perTaskSkills?.skillsPluginDir) {
    plugins.push({ type: "local", path: perTaskSkills.skillsPluginDir });
    for (const name of perTaskSkills.preloadBuiltinNames) {
      skillPreload.push(`asdlc-task-skills:${name}`);
    }
  }

  // SDK v0.2.126 auto-discovers the bundled native binary — no
  // pathToClaudeCodeExecutable needed. settingSources: [] ensures no
  // host filesystem settings leak into the container agent.
  const q = query({
    prompt: req.prompt,
    options: {
      cwd: layout.workspace,
      plugins,
      // Force built-in skill bodies into context at startup. Do NOT
      // replace with 'all' — see docs/design/skills-system.md.
      skills: skillPreload as unknown as string[],
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
