# Agents-Service LLM Record / Replay

## Why

E2E tests drive the full stack (`tests/e2e/` Playwright + `tests/api/` vitest)
against the local stack brought up by `deployments/` (Docker Compose around
the in-cluster OpenChoreo + Thunder + OpenBao platform). Most of those flows
go through `agents-service`, which forwards every call to Claude via the
Anthropic API. A single "create project → generate requirements →
generate design → generate tasks" run can burn tens of thousands of tokens.

We want a way to **record real Claude responses once** and **replay them
byte-for-byte** on subsequent test runs, so the everyday verification loop
costs nothing and is also faster and more deterministic. We also want a
straightforward escape hatch: re-record selectively, or flip a single call
back to live, so the last sanity-check before shipping still talks to the
real model.

## Scope

In scope — all five LLM-backed routes in agents-service:

| Route                                                | Style                                   |
|------------------------------------------------------|-----------------------------------------|
| `POST /v1/agents/document-generation/:skillId`       | SSE, `streamText`, text deltas          |
| `POST /v1/agents/architect`                          | SSE, `streamText`, tool-driven mutations |
| `POST /v1/agents/tech-lead/plan`                     | SSE, `streamObject`, partial array      |
| `POST /v1/agents/tech-lead/detail`                   | SSE, fan-out `streamText` per task      |
| `POST /v1/agents/requirements-chat`                  | SSE, `streamText`, tool-driven          |

Out of scope:

- `POST /v1/internal/dsl/render` — pure deterministic DSL → Excalidraw
  conversion, no LLM call.
- `coding-agent-runner` — that's the per-task Argo pod that runs
  Claude Agent SDK end-to-end; its execution model (workspace + git +
  `gh`) is fundamentally different and warrants a separate solution.
- Streaming through to non-agents-service services. BFF, git-service,
  the coding-agent etc. are unchanged.

## Approach: HTTP-level cassettes

We intercept at the **Express layer**, just inside the agents-service
process, after auth and `requireOrgId` but before the per-route handler.
The recorder/replayer treats each route as a black box:

- **Record mode**: the real handler runs as normal; we tee the response
  stream to a JSON file on disk while it's being written to the client.
- **Replay mode**: the real handler is skipped entirely; we emit the
  stored frames to the client with the original timing (or instantly,
  configurable).

### Why HTTP-level, not Vercel-SDK-level

Three of the five routes (`architect`, `requirements-chat`,
`tech-lead/detail` for its concurrent fan-out) are **tool-driven**: their
output is emitted by tool side-effects writing to an `SseSink`, not by the
model's natural-language token stream. The model's tokens are largely
discarded (`architect.ts:115` literally throws them away in
`for await (const _chunk of result.textStream)`). Anything that wraps
`streamText` would have to faithfully replay the tool-execution side
effects too, which means re-running the tools — pointless complexity
when the resulting SSE frames are exactly what the client cares about.

HTTP-level capture sidesteps this entirely: the recorded SSE stream
*is* the contract.

It also means the recorder is route-agnostic. Adding a sixth route later
costs one line in the allowlist.

## Cassette layout

```
agents/
  fixtures/
    cassettes/
      v1/
        document-generation/
          requirementsFromPrompt/
            <16-hex-hash>.json
            <16-hex-hash>.json
          functionalRequirements/
            <16-hex-hash>.json
        architect/
          <16-hex-hash>.json
        tech-lead-plan/
          <16-hex-hash>.json
        tech-lead-detail/
          <16-hex-hash>.json
        requirements-chat/
          <16-hex-hash>.json
      index.json         # optional human-readable map of hash → label
```

The `v1/` prefix lets us evolve the cassette schema without a big-bang
migration. Cassettes are committed to git — diffs in PRs are how we
review fixture changes.

### File shape

```json
{
  "version": 1,
  "route": "/v1/agents/architect",
  "method": "POST",
  "label": "create-todo-app — initial design",
  "model": "claude-sonnet-4-5",
  "recordedAt": "2026-05-17T11:24:03Z",
  "recordedBy": "anjana@local",
  "requestHash": "a3f1c8d029b04e17",
  "request": {
    "headers": {
      "x-oc-org-id": "demo-org",
      "content-type": "application/json"
    },
    "body": { /* canonicalised input */ }
  },
  "response": {
    "status": 200,
    "headers": {
      "content-type": "text/event-stream",
      "x-vercel-ai-ui-message-stream": "v1"
    },
    "frames": [
      { "t": 0,    "raw": "data: {\"type\":\"data-overview\",\"data\":{...}}\n\n" },
      { "t": 47,   "raw": "data: {\"type\":\"data-component-added\",\"data\":{...}}\n\n" },
      { "t": 312,  "raw": ": keep-alive\n\n" },
      { "t": 1284, "raw": "data: {\"type\":\"data-finish\",\"data\":{}}\n\n" },
      { "t": 1285, "raw": "data: [DONE]\n\n" }
    ],
    "usage": { "inputTokens": 4128, "outputTokens": 6802 }
  }
}
```

- `t` is milliseconds since the response started, so we can preserve
  pacing in replay or ignore it (`AGENT_LLM_REPLAY_SPEED=instant`).
- `raw` keeps the original `data: …\n\n` bytes verbatim so framing edge
  cases (custom event names, comments for keep-alives, the `[DONE]`
  terminator) all round-trip without per-route knowledge.
- `label` is optional, hand-edited or set via header
  `X-Asdlc-Cassette-Label` at record time, purely to make `git diff`
  navigable.
- `usage` is informational — useful when reviewing how much money a
  fixture saves per run.

## Request hashing

```
requestHash = sha256(
    method
  + "\n" + route
  + "\n" + canonicalJSON(body)
  + "\n" + AGENT_MODEL
).slice(0, 16)
```

- **Canonical JSON**: stable key ordering (sort recursively), no
  whitespace, JS `JSON.stringify` numerics. Equivalent inputs hash the
  same regardless of source.
- **Model is part of the hash**. Switching from `claude-sonnet-4-5` to
  `claude-opus-4-7` invalidates fixtures (correct: they'd no longer
  represent the right model's behaviour).
- **Route is the path template, not the full URL** — `:skillId` is
  expanded so different skills produce different files.
- **Headers other than `X-Oc-Org-Id` are excluded.** Correlation IDs,
  user-agents, JWTs, etc. are noise.
- **`X-Asdlc-LLM-Mode` and `X-Asdlc-Cassette-Label` are stripped before
  hashing.** Otherwise the very header that switches modes would change
  the cassette identity.

If prompts in `src/agents/**/prompt.ts` change, the body hash is
unaffected (prompts are server-side), but the **recorded response** is
now stale — a re-record is needed. We accept this: the alternative
(hashing the constructed system+user prompts too) would invalidate every
cassette on every comment change in `prompt.ts`. Fixture staleness is
caught by the test suite itself diverging.

### What's stripped from the body before hashing

Some fields are recorded but excluded from the hash so cassettes survive
non-semantic input changes:

- Nothing, in v1.

Deliberately empty. We start strict and only relax per-route after we
see real false-negatives. Loose normalisation per route can be added
later as a separate `normalisers/<route>.ts` map; this is exactly the
"loose match" option from the design Q&A, deferred until we have a
case for it.

## Mode resolution

Modes, in priority order — first non-empty wins:

1. **Per-request override**: `X-Asdlc-LLM-Mode` header on the incoming
   request. Lets a single Playwright step pin one call live while the
   rest of the run stays on replay.
2. **Env var**: `AGENT_LLM_MODE` on the agents-service process.
3. **Default**: `live`.

The five modes:

| Mode             | If cassette exists                                | If cassette missing                  |
|------------------|---------------------------------------------------|--------------------------------------|
| `live`           | ignore it, call Anthropic                         | call Anthropic                       |
| `replay`         | serve cassette                                    | **400 — missing cassette**           |
| `replay-or-live` | serve cassette                                    | call Anthropic (does not save)       |
| `record`         | call Anthropic, **overwrite** cassette            | call Anthropic, save cassette        |
| `record-missing` | serve cassette                                    | call Anthropic, save cassette        |

CI / day-to-day testing runs `replay`, which fails loudly when a
fixture is missing — that's how we catch tests that drifted from
their cassettes. Local development runs `record-missing` so missing
fixtures get filled in as soon as someone exercises the path. Final
pre-release verification runs `live` for the whole suite, or — more
cheaply — `replay` with a one-off `X-Asdlc-LLM-Mode: live` on the
single call worth re-checking.

## Implementation sketch

New module: `agents/src/replay/`.

```
agents/src/replay/
  index.ts                # exported: wrapWithRecorder(app)
  config.ts               # env parsing, mode resolution
  cassette-store.ts       # fs read/write under fixtures/cassettes/v1/
  request-hash.ts         # canonical-JSON, sha256, body-strip
  recorder.ts             # tees res.write into a buffered cassette
  replayer.ts             # streams cassette frames back as SSE
  routes.ts               # allowlist + path-template extraction
```

Wiring (`agents/src/server/index.ts`) — one new line per route, e.g.:

```ts
import { wrapWithRecorder } from "../replay/index.js";

// ... existing requireOrgId middleware ...

app.use("/v1/agents", wrapWithRecorder({
  routes: [
    "/v1/agents/document-generation/:skillId",
    "/v1/agents/architect",
    "/v1/agents/tech-lead/plan",
    "/v1/agents/tech-lead/detail",
    "/v1/agents/requirements-chat",
  ],
}));

registerDocumentGeneration(app);
// ... etc, unchanged
```

`wrapWithRecorder` is a single Express middleware that:

1. Matches `req.path` against the allowlist (using
   `path-to-regexp` so `:skillId` works). On miss → `next()`.
2. Reads `X-Asdlc-LLM-Mode` → falls back to `AGENT_LLM_MODE` → `live`.
3. Buffers the JSON body (Express already gave us `req.body`),
   strips ignored headers, computes `requestHash`.
4. Looks up the cassette file.
5. Dispatches by mode:
   - `replay` / `replay-or-live` with hit → `replayer.serve(res, cassette)`,
     skip `next()`.
   - `replay` miss → respond `400 cassette-not-found` with the hash and
     the suggested filename, so the dev sees exactly what to record.
   - `live` / `replay-or-live` miss / `record-missing` hit (replay path)
     / `record` / `record-missing` miss → call `next()`. For `record*`
     and `replay-or-live` miss, wrap `res.write` / `res.end` so each
     chunk is appended to an in-memory frame list with a millisecond
     offset. On `res.end`, flush the cassette to disk (only for `record*`).

Wrapping `res.write` is straightforward — store the original, then
override:

```ts
const startedAt = Date.now();
const frames: { t: number; raw: string }[] = [];
const origWrite = res.write.bind(res);
res.write = (chunk, ...args) => {
  if (typeof chunk === "string" || Buffer.isBuffer(chunk)) {
    frames.push({
      t: Date.now() - startedAt,
      raw: chunk.toString("utf8"),
    });
  }
  return origWrite(chunk, ...args);
};
```

This survives `flushHeaders`, `setInterval` keep-alives, and the SDK's
internal chunking — every byte that ends up on the wire is captured.

`replayer.serve` is the dual:

```ts
async function serve(res: Response, cassette: Cassette) {
  res.writeHead(cassette.response.status, cassette.response.headers);
  res.flushHeaders?.();
  for (const frame of cassette.response.frames) {
    if (res.writableEnded) return;
    if (replaySpeed !== "instant") {
      await delay(frame.t - elapsed);
    }
    res.write(frame.raw);
  }
  res.end();
}
```

### Bypassing key resolution in replay

In `replay` mode we should never hit git-service for the Anthropic
key. Two ways to enforce that:

- **Preferred**: the middleware short-circuits before the route handler
  runs, so the `resolveAnthropicKey` call in the handler never happens.
  Already true with the design above.
- **Defensive**: the handler still tries to resolve; we make the
  resolver return a dummy key when `AGENT_LLM_MODE=replay`. Adds a
  surprising side door; not needed if we short-circuit cleanly.

We go with the preferred path.

## Tooling

A small npm script set in `agents/package.json`:

```
npm run cassettes:list     # tree of stored cassettes with labels + sizes
npm run cassettes:rm <hash> # delete a cassette so the next run re-records
npm run cassettes:stats    # aggregate input/output tokens saved
```

For the test suites, two new run targets:

```
# In tests/, run E2E against agents-service in replay mode.
AGENT_LLM_MODE=replay npm run test:e2e

# Local dev: any missing cassette is recorded on first hit.
AGENT_LLM_MODE=record-missing npm run test:e2e

# Belt-and-suspenders pre-release run.
AGENT_LLM_MODE=live npm run test:e2e
```

`AGENT_LLM_MODE` is plumbed through the `agents-service:` block in
`deployments/docker-compose.yml` (sibling to `AGENT_MODEL`,
`ANTHROPIC_API_KEY`, etc.) and sourced from `deployments/.env`. A
`docker compose up -d --build agents-service` from `deployments/` after
editing `.env` rolls the change.

The cassette directory is bind-mounted into the container so
`record` / `record-missing` runs write straight back to the host repo:

```yaml
# deployments/docker-compose.yml — under agents-service.volumes
- ../agents/fixtures/cassettes:/app/fixtures/cassettes
```

## Open questions

- **Fixture cleanup**: do we want a "prune orphans" command that diffs
  cassettes against a list of hashes observed in a passing replay run?
  Cheap to add; leaving out of v1 until we see fixture bloat.
- **Concurrent recording**: two `record-missing` runs in parallel hitting
  the same path with the same input could race on the write. We
  serialise via `O_EXCL | O_CREAT` and accept "last writer wins" on
  collision — recorded responses are equivalent up to nondeterminism
  we'd want to canonicalise anyway.
- **Anthropic Files / multimodal**: none of the five routes upload
  files today. If `architect` later starts attaching wireframe PNGs to
  the model call, the request body grows large and binary; we'd extend
  the canonical JSON to handle that. Out of scope for v1.
- **PII in cassettes**: requirements/spec text the user typed is in the
  body. Cassettes are committed to git, so anything we record is
  effectively a public test fixture. Tests should use seeded synthetic
  inputs (we already do this in `tests/e2e/fixtures/`), not real user
  data. Worth a one-liner in the test README.

## Non-goals

- **Cross-version replay**. If we change a prompt, we re-record. The
  cassette is not a contract with the model, it's a contract with
  *that exact prompt + that exact model*.
- **Partial matching / similarity**. Strict hash, no fuzz. Easier to
  reason about, easier to debug "why didn't my cassette hit".
- **Recording the coding-agent runner**. Different surface, different
  cost profile, different solution.

## Rollout

1. Land `agents/src/replay/` plus the one-line wire-in. Default mode
   stays `live` — nothing in production behaviour changes.
2. Run the E2E suite once locally with `AGENT_LLM_MODE=record-missing`
   to populate `agents/fixtures/cassettes/v1/`. Commit the fixtures.
3. Switch the local-dev default in `deployments/.env` (and document it
   in `deployments/README.md`) to `AGENT_LLM_MODE=replay-or-live` — fast
   and free for normal work, but still calls the model for any new path.
4. CI sets `AGENT_LLM_MODE=replay` and fails on cassette miss. PRs that
   change prompts must include the re-recorded fixtures or the test
   suite breaks.
5. The pre-ship sanity pass runs `AGENT_LLM_MODE=live` against a known
   seed input.

## CLAUDE.md / AGENTS.md additions (apply after implementation)

Add this one-liner to `CLAUDE.md` (under **Testing**) and to any
`agents/AGENTS.md`:

```markdown
**LLM record/replay**: agents-service replays recorded SSE from
`agents/fixtures/cassettes/v1/` instead of calling Claude. Mode via
`AGENT_LLM_MODE` in `deployments/.env` (`live` | `replay` |
`replay-or-live` | `record` | `record-missing`) or per-request
`X-Asdlc-LLM-Mode` header. Local default `replay-or-live`, CI `replay`.
Re-record after prompt edits (`AGENT_LLM_MODE=record`) and commit the
updated cassettes. Design: `docs/design/llm-record-replay.md`.
```

And one line in `deployments/README.md`'s env table:

```
AGENT_LLM_MODE   live|replay|replay-or-live|record|record-missing — see docs/design/llm-record-replay.md
```
