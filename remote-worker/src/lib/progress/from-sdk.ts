// Translate Claude Agent SDK messages into runner progress events.
// One SDK message can yield zero, one, or many events (an assistant
// message often carries multiple tool_use content blocks). The caller
// emits each returned event in order.

import type { ProgressEventInput } from "./schema.js";

const MAX_SUMMARY = 200;

function trimSummary(s: string): string {
  const collapsed = s.replace(/\s+/g, " ").trim();
  if (collapsed.length <= MAX_SUMMARY) return collapsed;
  return collapsed.slice(0, MAX_SUMMARY - 1) + "…";
}

function bashEvents(command: string): ProgressEventInput[] {
  const cmd = command.trim();
  // git commit -m "..." or -F file
  if (/^git\s+commit\b/.test(cmd)) {
    const msgMatch = cmd.match(/-m\s+(['"])(.+?)\1/);
    return [{
      kind: "git_commit",
      summary: msgMatch ? trimSummary(msgMatch[2]) : trimSummary(cmd),
    }];
  }
  // git push origin <branch> / git push -u origin <branch>
  if (/^git\s+push\b/.test(cmd)) {
    const tokens = cmd.split(/\s+/);
    const branch = tokens[tokens.length - 1];
    return [{
      kind: "git_push",
      branch: branch && branch !== "push" ? branch : undefined,
      summary: trimSummary(cmd),
    }];
  }
  // gh anything
  if (/^gh\s+/.test(cmd)) {
    return [{
      kind: "gh_action",
      command: trimSummary(cmd),
    }];
  }
  return [{
    kind: "tool_use",
    tool: "Bash",
    summary: trimSummary(cmd),
  }];
}

function summaryFromInput(toolName: string, input: unknown): string {
  if (input && typeof input === "object") {
    const obj = input as Record<string, unknown>;
    // Most file tools carry file_path / pattern / path.
    const candidate = obj.file_path ?? obj.path ?? obj.pattern ?? obj.glob ?? obj.url;
    if (typeof candidate === "string") return trimSummary(candidate);
    // Fall back to a compact JSON dump (truncated). Helps surface unknown tools.
    try {
      return trimSummary(JSON.stringify(obj));
    } catch {
      return "";
    }
  }
  return "";
}

// Best-effort pluck of tool_use content blocks from an assistant SDK message.
// The SDK exposes message.message.content[] where each block has a `type`.
function assistantToolUseBlocks(message: unknown): Array<{ name: string; input: unknown }> {
  if (!message || typeof message !== "object") return [];
  const m = message as Record<string, unknown>;
  const inner = m.message;
  if (!inner || typeof inner !== "object") return [];
  const content = (inner as Record<string, unknown>).content;
  if (!Array.isArray(content)) return [];
  const out: Array<{ name: string; input: unknown }> = [];
  for (const block of content) {
    if (!block || typeof block !== "object") continue;
    const b = block as Record<string, unknown>;
    if (b.type !== "tool_use") continue;
    const name = typeof b.name === "string" ? b.name : "";
    if (!name) continue;
    out.push({ name, input: b.input });
  }
  return out;
}

export function progressFromSdkMessage(message: unknown): ProgressEventInput[] {
  if (!message || typeof message !== "object") return [];
  const m = message as Record<string, unknown>;
  const type = m.type;

  if (type === "system" && m.subtype === "init") {
    return [{ kind: "phase", phase: "agent_started" }];
  }

  if (type === "assistant") {
    const events: ProgressEventInput[] = [];
    for (const tu of assistantToolUseBlocks(m)) {
      if (tu.name === "Bash") {
        const cmd = (tu.input && typeof tu.input === "object")
          ? String((tu.input as Record<string, unknown>).command ?? "")
          : "";
        if (!cmd) {
          events.push({ kind: "tool_use", tool: "Bash", summary: "" });
        } else {
          events.push(...bashEvents(cmd));
        }
        continue;
      }
      events.push({
        kind: "tool_use",
        tool: tu.name,
        summary: summaryFromInput(tu.name, tu.input),
      });
    }
    return events;
  }

  if (type === "result") {
    const subtype = String(m.subtype ?? "");
    if (subtype === "success") {
      return [{ kind: "result", status: "success" }];
    }
    const errors = Array.isArray(m.errors) ? (m.errors as string[]).join(", ") : "";
    return [{
      kind: "result",
      status: "failure",
      error: errors || subtype,
    }];
  }

  return [];
}
