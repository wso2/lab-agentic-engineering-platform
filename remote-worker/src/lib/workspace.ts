// Per-task workspace provisioning.
//
// On dispatch the BFF creates a WorkflowRun of `app-factory-coding-agent`
// and Argo schedules an ephemeral pod whose entrypoint (src/oneshot.ts)
// calls this function. We clone the project's repo on its **default
// branch** into $WORKSPACE_BASE_PATH/<orgId>/<projectId>/<taskId>/ and
// configure `.git/config` + `gh` so the agent can git/gh against GitHub
// without ever seeing a token in environment variables. The agent itself
// creates the feature branch and opens the PR with `Closes #<issueNumber>`
// — see remote-worker/plugin/skills/asdlc/SKILL.md.
//
// Layout inside the workspace:
//
//   <workspace>/
//     .git/                     ← cloned repo, default branch checked out
//     .gh-config/hosts.yml      ← gh's auth config (rewritten by ghWrapper)
//     .asdlc/
//       bearer                  ← chmod 600 — per-task JWT
//       credhelper.sh           ← chmod 700 — git credential helper
//       gh                      ← chmod 755 — gh wrapper, on PATH
//
// The agent runs with cwd=<workspace> and PATH prefixed with <workspace>/.asdlc
// so `gh ...` resolves to the wrapper. No tokens cross via process env.

import fs from "node:fs";
import { exec } from "node:child_process";
import path from "node:path";
import { promisify } from "node:util";
import http from "node:http";
import { config } from "../config.js";
import { credHelperScript, ghWrapperScript } from "./credhelper.js";

const execAsync = promisify(exec);

export interface WorkspaceLayout {
  workspace: string;
  ghConfigDir: string;
  bearerFile: string;
  asdlcDir: string;
  helperBin: string;
  ghWrapper: string;
}

export interface ProvisionRequest {
  orgId: string;
  projectId: string;
  taskId: string;
  repoUrl: string;
  bearer: string;
  identity: { name: string; email: string; login?: string };
  gitServiceUrl: string;
  correlationId?: string;
}

// computeLayout names every path the dispatch flow touches. Pure function
// so tests can verify the path layout without filesystem effects.
export function computeLayout(orgId: string, projectId: string, taskId: string): WorkspaceLayout {
  const workspace = path.join(config.workspaceBasePath, orgId, projectId, taskId);
  const asdlcDir = path.join(workspace, ".asdlc");
  return {
    workspace,
    ghConfigDir: path.join(workspace, ".gh-config"),
    bearerFile: path.join(asdlcDir, "bearer"),
    asdlcDir,
    helperBin: path.join(asdlcDir, "credhelper.sh"),
    ghWrapper: path.join(asdlcDir, "gh"),
  };
}

// resolvePATForClone calls git-service /api/v1/credentials/refresh using
// the task bearer to obtain the GitHub PAT.  Used during workspace
// provisioning to embed credentials in the clone URL — avoids the
// GIT_ASKPASS protocol mismatch where credhelper.sh outputs two-line
// key=value format but GIT_ASKPASS reads only one line per call.
async function resolvePATForClone(
  bearerFile: string,
  _helperScript: string,
  req: ProvisionRequest,
): Promise<string> {
  const bearer = await fs.promises.readFile(bearerFile, "utf-8");
  if (!bearer.trim()) {
    throw new Error("bearer file is empty");
  }

  const url = new URL(req.gitServiceUrl);
  if (!url.pathname.endsWith("/")) url.pathname += "/";
  url.pathname += "api/v1/credentials/refresh";

  const headers: Record<string, string> = {
    "Authorization": `Bearer ${bearer.trim()}`,
    "Content-Type": "application/json",
  };
  if (req.correlationId) {
    headers["X-Correlation-ID"] = req.correlationId;
  }

  return new Promise((resolve, reject) => {
    const hReq = http.request(
      url,
      { method: "POST", headers, timeout: 10000 },
      (res) => {
        let body = "";
        res.on("data", (chunk: Buffer) => { body += chunk.toString(); });
        res.on("end", () => {
          if (res.statusCode !== 200) {
            return reject(new Error(`git-service returned ${res.statusCode}: ${body.slice(0, 200)}`));
          }
          try {
            const data = JSON.parse(body);
            if (!data.token) {
              return reject(new Error("git-service response missing token"));
            }
            resolve(data.token as string);
          } catch {
            reject(new Error("invalid git-service response: " + body.slice(0, 200)));
          }
        });
      },
    );
    hReq.on("error", reject);
    hReq.on("timeout", () => { hReq.destroy(); reject(new Error("git-service request timed out")); });
    hReq.write("{}");
    hReq.end();
  });
}
// provisionWorkspace clones the feature branch and writes credentials.
// Idempotent: it removes any existing workspace first (§12.1 step 5
// resume-safety: a crash mid-clone leaves DispatchedAt=null, the resume
// sweep re-enters this step, which begins with rm -rf).
//
// Order matters: `git clone <url> <dir>` refuses to write into an existing
// non-empty directory. So we stage the credhelper in a sibling tmp dir,
// clone into the workspace path (which must not exist yet), and only then
// drop the .asdlc/ and .gh-config/ directories inside the cloned tree.
export async function provisionWorkspace(req: ProvisionRequest): Promise<WorkspaceLayout> {
  const layout = computeLayout(req.orgId, req.projectId, req.taskId);
  const stageDir = layout.workspace + ".stage";

  // Wipe both the target and any prior stage. Don't pre-create the workspace
  // dir — git clone will materialise it.
  await fs.promises.rm(layout.workspace, { recursive: true, force: true });
  await fs.promises.rm(stageDir, { recursive: true, force: true });
  await fs.promises.mkdir(path.dirname(layout.workspace), { recursive: true, mode: 0o755 });
  await fs.promises.mkdir(stageDir, { recursive: true, mode: 0o700 });

  // Stage credhelper + bearer in the sibling dir so the clone can authenticate.
  // The staged helper points at the (about-to-be-created) workspace dir so
  // identity-rewrite calls during the clone don't have a target to write to —
  // benign no-op (the .git dir doesn't exist yet during clone). The runtime
  // helper (re-written below in the workspace) gets the real path.
  const stageBearer = path.join(stageDir, "bearer");
  const stageHelper = path.join(stageDir, "credhelper.sh");
  await fs.promises.writeFile(stageBearer, req.bearer, { mode: 0o600 });
  await fs.promises.writeFile(
    stageHelper,
    credHelperScript({ taskId: req.taskId, workspaceDir: layout.workspace }),
    { mode: 0o700 },
  );

  try {
    // Resolve the PAT from git-service before cloning so we can embed it
    // in the clone URL. GIT_ASKPASS protocol reads only one line per call,
    // but the credhelper outputs two-line key=value format — using it as
    // GIT_ASKPASS causes git to use the literal string
    // "username=x-access-token" as the credential, which GitHub rejects.
    //
    // Embedding the PAT directly in the clone URL avoids the askpass
    // mismatch. The URL credentials are consumed once during clone and do
    // not persist in .git/config (which is materialised after the clone
    // with the proper credential.https://github.com.helper).
    const patResp = await resolvePATForClone(stageBearer, stageHelper, req);
    const cloneEnv = {
      ...process.env,
      GIT_TERMINAL_PROMPT: "0",
    };
    const authedURL = req.repoUrl.replace("https://", `https://x-access-token:${patResp}@`);
    // No --branch: clone the remote's default branch (HEAD). The agent
    // creates its own feature branch via `git checkout -b ...` once it
    // starts working, per the asdlc skill workflow.
    const cloneCmd = `git clone ${shellQuote(authedURL)} ${shellQuote(layout.workspace)}`;
    await execAsync(cloneCmd, { env: cloneEnv, maxBuffer: 16 * 1024 * 1024 });

    // Materialise the runtime layout inside the cloned tree.
    await fs.promises.mkdir(layout.asdlcDir, { recursive: true, mode: 0o755 });
    await fs.promises.mkdir(layout.ghConfigDir, { recursive: true, mode: 0o755 });
    await fs.promises.writeFile(layout.bearerFile, req.bearer, { mode: 0o600 });
    await fs.promises.writeFile(
      layout.helperBin,
      credHelperScript({ taskId: req.taskId, workspaceDir: layout.workspace }),
      { mode: 0o700 },
    );

    // gh wrapper (chmod 755). Resolve the real gh binary path eagerly so the
    // wrapper doesn't have to. If gh is not on PATH we fall back to
    // /usr/bin/env gh and let the wrapper fail at run time with a useful error.
    let realGhPath = "gh";
    try {
      const which = await execAsync("which gh");
      realGhPath = which.stdout.trim() || "gh";
    } catch {
      realGhPath = "/usr/bin/env gh";
    }
    await fs.promises.writeFile(
      layout.ghWrapper,
      ghWrapperScript(realGhPath, { taskId: req.taskId, workspaceDir: layout.workspace }),
      { mode: 0o755 },
    );

    // .git/config: identity + credential helper so subsequent ops don't need
    // GIT_ASKPASS env.
    await execAsync(
      `git -C ${shellQuote(layout.workspace)} config user.name ${shellQuote(req.identity.name)}`,
    );
    await execAsync(
      `git -C ${shellQuote(layout.workspace)} config user.email ${shellQuote(req.identity.email)}`,
    );
    await execAsync(
      `git -C ${shellQuote(layout.workspace)} config credential.https://github.com.helper ${shellQuote(layout.helperBin)}`,
    );
  } finally {
    await fs.promises.rm(stageDir, { recursive: true, force: true });
  }

  return layout;
}

function shellQuote(s: string): string {
  // Single-quote and escape any embedded single-quote.
  return `'${s.replaceAll("'", "'\\''")}'`;
}
