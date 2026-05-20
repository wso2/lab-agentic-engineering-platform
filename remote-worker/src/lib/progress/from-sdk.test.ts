import { test } from "node:test";
import assert from "node:assert/strict";
import { progressFromSdkMessage } from "./from-sdk.js";

test("from-sdk: system init → agent_started phase", () => {
  const events = progressFromSdkMessage({ type: "system", subtype: "init" });
  assert.equal(events.length, 1);
  assert.deepEqual(events[0], { kind: "phase", phase: "agent_started" });
});

test("from-sdk: result success → success result", () => {
  const events = progressFromSdkMessage({ type: "result", subtype: "success" });
  assert.equal(events.length, 1);
  assert.deepEqual(events[0], { kind: "result", status: "success" });
});

test("from-sdk: result error → failure result with errors joined", () => {
  const events = progressFromSdkMessage({
    type: "result",
    subtype: "error_during_execution",
    errors: ["failed to push", "auth denied"],
  });
  assert.equal(events.length, 1);
  assert.deepEqual(events[0], {
    kind: "result",
    status: "failure",
    error: "failed to push, auth denied",
  });
});

test("from-sdk: assistant Bash git commit -m → git_commit event with message", () => {
  const events = progressFromSdkMessage({
    type: "assistant",
    message: {
      role: "assistant",
      content: [
        {
          type: "tool_use",
          name: "Bash",
          input: { command: "git commit -m \"Add JWT validation\"" },
        },
      ],
    },
  });
  assert.equal(events.length, 1);
  assert.equal(events[0].kind, "git_commit");
  assert.equal((events[0] as { summary: string }).summary, "Add JWT validation");
});

test("from-sdk: assistant Bash git push → git_push event with branch", () => {
  const events = progressFromSdkMessage({
    type: "assistant",
    message: {
      content: [
        {
          type: "tool_use",
          name: "Bash",
          input: { command: "git push origin task/jwt-9a3" },
        },
      ],
    },
  });
  assert.equal(events.length, 1);
  assert.equal(events[0].kind, "git_push");
  assert.equal((events[0] as { branch: string }).branch, "task/jwt-9a3");
});

test("from-sdk: assistant Bash gh → gh_action event", () => {
  const events = progressFromSdkMessage({
    type: "assistant",
    message: {
      content: [
        {
          type: "tool_use",
          name: "Bash",
          input: { command: "gh pr ready 18" },
        },
      ],
    },
  });
  assert.equal(events.length, 1);
  assert.equal(events[0].kind, "gh_action");
  assert.equal((events[0] as { command: string }).command, "gh pr ready 18");
});

test("from-sdk: assistant Bash other → generic tool_use", () => {
  const events = progressFromSdkMessage({
    type: "assistant",
    message: {
      content: [
        {
          type: "tool_use",
          name: "Bash",
          input: { command: "go test ./..." },
        },
      ],
    },
  });
  assert.equal(events.length, 1);
  assert.equal(events[0].kind, "tool_use");
  assert.equal((events[0] as { tool: string }).tool, "Bash");
  assert.equal((events[0] as { summary: string }).summary, "go test ./...");
});

test("from-sdk: assistant Edit → tool_use with file_path summary", () => {
  const events = progressFromSdkMessage({
    type: "assistant",
    message: {
      content: [
        {
          type: "tool_use",
          name: "Edit",
          input: { file_path: "services/auth/jwt.go" },
        },
      ],
    },
  });
  assert.equal(events.length, 1);
  assert.equal(events[0].kind, "tool_use");
  assert.equal((events[0] as { summary: string }).summary, "services/auth/jwt.go");
});

test("from-sdk: assistant with multiple tool_use blocks emits in order", () => {
  const events = progressFromSdkMessage({
    type: "assistant",
    message: {
      content: [
        { type: "text", text: "first I'll read..." },
        { type: "tool_use", name: "Read", input: { file_path: "a.go" } },
        { type: "tool_use", name: "Edit", input: { file_path: "b.go" } },
      ],
    },
  });
  assert.equal(events.length, 2);
  assert.equal((events[0] as { summary: string }).summary, "a.go");
  assert.equal((events[1] as { summary: string }).summary, "b.go");
});

test("from-sdk: unknown message type → empty", () => {
  assert.deepEqual(progressFromSdkMessage({ type: "user", message: { content: [] } }), []);
  assert.deepEqual(progressFromSdkMessage(null), []);
  assert.deepEqual(progressFromSdkMessage("garbage"), []);
});

test("from-sdk: assistant with no content array → empty", () => {
  assert.deepEqual(
    progressFromSdkMessage({ type: "assistant", message: { content: null } }),
    [],
  );
});

test("from-sdk: long Bash command summary is truncated", () => {
  const longCmd = "echo " + "x".repeat(500);
  const events = progressFromSdkMessage({
    type: "assistant",
    message: {
      content: [{ type: "tool_use", name: "Bash", input: { command: longCmd } }],
    },
  });
  assert.equal(events.length, 1);
  const summary = (events[0] as { summary: string }).summary;
  assert.ok(summary.length <= 200);
  assert.ok(summary.endsWith("…"));
});
