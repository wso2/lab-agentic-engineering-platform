// Per-task skills pull — runner-side client for
// GET $ASDLC_PLATFORM_URL/api/v1/tasks/$ASDLC_TASK_ID/skills.
//
// Auth: the per-task RS256 bearer the runner already holds (same one
// used by the verification-failed endpoint). Returns the resolved
// SKILL.md bodies + materialised names to feed into the AgentSkills
// plugin tree under .asdlc/skills-plugin/.
//
// See docs/design/skills-system.md > "Coding agent".

import http from "node:http";
import https from "node:https";
import { URL } from "node:url";

export interface SkillResolution {
  id: string;               // e.g. "builtin/api-management"
  materializedName: string; // e.g. "builtin-api-management"
  kind: "builtin" | "custom" | "imported";
  skillMd: string;
  references: Record<string, string>;
}

export interface SkillsPullResponse {
  skills: SkillResolution[];
}

export interface PullArgs {
  platformURL: string; // e.g. "http://asdlc-api:9090"
  taskId: string;
  bearer: string;
  correlationId?: string;
  timeoutMs?: number;
}

/**
 * Pull the snapshotted skills for this task from the BFF. Empty list
 * (NOT an error) when the task has no snapshot — pre-PR-1 backfilled
 * tasks or designs with no attached skills. Throws on transport
 * failures, 4xx/5xx responses, or malformed bodies.
 */
export async function pullTaskSkills(args: PullArgs): Promise<SkillsPullResponse> {
  const base = new URL(args.platformURL);
  const url = new URL(`/api/v1/tasks/${encodeURIComponent(args.taskId)}/skills`, base);

  const headers: Record<string, string> = {
    Authorization: `Bearer ${args.bearer.trim()}`,
    Accept: "application/json",
  };
  if (args.correlationId) {
    headers["X-Correlation-ID"] = args.correlationId;
  }

  const lib = url.protocol === "https:" ? https : http;
  return new Promise((resolve, reject) => {
    const req = lib.request(
      url,
      { method: "GET", headers, timeout: args.timeoutMs ?? 10000 },
      (res) => {
        let body = "";
        res.on("data", (chunk: Buffer) => {
          body += chunk.toString();
        });
        res.on("end", () => {
          if (res.statusCode !== 200) {
            return reject(
              new Error(
                `task skills endpoint returned ${res.statusCode}: ${body.slice(0, 200)}`,
              ),
            );
          }
          try {
            const parsed = JSON.parse(body);
            // BFF wraps responses in { status, data } via WriteSuccessResponse.
            const data: unknown = parsed?.data ?? parsed;
            if (!data || typeof data !== "object" || !Array.isArray((data as { skills?: unknown }).skills)) {
              return reject(new Error("malformed skills response: missing skills[]"));
            }
            resolve(data as SkillsPullResponse);
          } catch (err) {
            reject(
              new Error(`invalid skills response: ${err instanceof Error ? err.message : String(err)}`),
            );
          }
        });
      },
    );
    req.on("error", (err) => reject(err));
    req.on("timeout", () => {
      req.destroy();
      reject(new Error("task skills request timed out"));
    });
    req.end();
  });
}
