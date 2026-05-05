# Agents Service — Design

Concrete design for the HTTP/SSE surface over the `agents/` package. A thin wrapper that exposes each role agent as an SSE endpoint using the AI SDK v6 UI Message Stream protocol. Called by the BFF (`asdlc-service`) — never directly from the console.

The agents service is a **compute primitive**: take structured input, stream SDK-native chunks, done. It has no state machine, no project awareness, no artifact storage. All of that lives in the BFF.

Related:
- `agent-architecture.md` — the ideal end-state agent architecture.
- `api-service.md` — BFF; owns artifact storage, versioning, and pipeline sequencing.

## 1. Goal

Expose each agent over an SSE endpoint so the BFF can:
1. Load upstream artifact(s) from git (already done to render the UI).
2. POST to the agents service and receive an SDK-native streaming SSE response.
3. Forward the SSE straight through to the console for live progress.
4. Accumulate text deltas / artifact chunks on the way past and persist on completion.

Approval (tag creation) stays on the existing BFF `save` endpoints and never touches the agents service.

## 2. Scope

**In (v1):**
- Four SSE endpoints, one per agent: `business-analyst`, `architect`, `tech-lead`, `developer`.
- Wire protocol: AI SDK v6 UI Message Stream — chunks forwarded as the SDK produces them, no translation layer.
- Request-lifetime streaming: connection closes → agent aborted.
- In-process only; no persistence.

**In (this milestone — requirements stage only):**
- `POST /v1/agents/business-analyst` — pure text-delta streaming. The model output is markdown for `spec.md`; the BFF accumulates it from `text-delta` chunks.

**Deferred to follow-up milestones:**
- `data-artifact` custom chunk for stages that produce structured JSON (Architect → `design.json`, TechLead → `plan.json`). Introduced when the first structured-output stage lands.
- Architect, TechLead, Developer endpoints.

**Out:**
- Multi-stage state machine or pipeline orchestration (BFF).
- Gates / approval / feedback cycles (BFF + git tags).
- Resumability after disconnect.
- Artifact persistence (BFF + git-service).
- Authn/z (agents service is internal; BFF is the trust boundary).
- Worker fan-out for parallel Developer invocations.

## 3. Module Layout

```
agents/
├── src/
│   ├── agents/         ← existing role agents (unchanged)
│   │   └── business-analyst/
│   │       ├── schema.ts
│   │       ├── prompt.ts          ← systemPrompt + buildUserPrompt
│   │       └── index.ts
│   ├── shared/, skills/, tools/   ← existing
│   ├── server/         ← NEW
│   │   ├── index.ts          Express app + bootstrap
│   │   └── routes/
│   │       └── business-analyst.ts
│   └── index.ts        ← library exports (unchanged)
```

No `events.ts` or `translate.ts` — chunks are the SDK's, not ours. No need for a local envelope type.

**HTTP framework**: Express. SSE is a single call on the result (`pipeUIMessageStreamToResponse`).

## 4. Wire Protocol

The wire format is the **AI SDK v6 UI Message Stream Protocol** — unchanged from what the SDK emits for any `streamText` call. One SSE frame per `UIMessageChunk`.

Response headers set automatically by the SDK:
```
Content-Type: text/event-stream
x-vercel-ai-ui-message-stream: v1
```

Stream ends with `data: [DONE]`.

### Chunks the BA stage produces (text-only, no tools)

From the SDK's `UIMessageChunk` union (`ai/dist/index.d.ts`), the BA text-only path emits:

```ts
| { type: 'start'; messageId?: string }
| { type: 'start-step' }
| { type: 'text-start'; id: string }
| { type: 'text-delta'; id: string; delta: string }
| { type: 'text-end'; id: string }
| { type: 'finish-step' }
| { type: 'finish'; finishReason?: FinishReason }
| { type: 'abort'; reason?: string }        // on client disconnect
| { type: 'error'; errorText: string }      // on failure
```

### Happy-path example

```
data: {"type":"start","messageId":"msg_01..."}

data: {"type":"start-step"}

data: {"type":"text-start","id":"txt_01..."}

data: {"type":"text-delta","id":"txt_01...","delta":"# Requirements\n\n"}

data: {"type":"text-delta","id":"txt_01...","delta":"## Summary\n"}

... more text-delta frames ...

data: {"type":"text-end","id":"txt_01..."}

data: {"type":"finish-step"}

data: {"type":"finish","finishReason":"stop"}

data: [DONE]
```

### Custom chunks

None in v1. Later stages that need structured output will introduce `data-artifact` (`{ type: 'data-artifact'; data: <StageOutput>; id?: string }`) — designed when that stage lands, not before.

## 5. HTTP API

Base path: `/v1`. Port: **3400**.

### POST /v1/agents/business-analyst
```jsonc
// Request body
{
  "prompt": "string — the fully composed user prompt"
}
```

Response: UI Message Stream SSE (see §4).

Body validated against `{ prompt: z.string().min(1) }`. `400` with detail on failure — no SSE opened.

**Prompt composition is the BFF's job.** The BFF has project context, prior artifacts, and user feedback in hand; it concatenates them into a single prompt string and sends it. The agents service does not understand `projectName`, `projectDescription`, or `feedback` as separate fields. The only thing it owns is the **role system prompt** (the BA persona), which stays fixed in `agents/src/agents/business-analyst/prompt.ts`.

This keeps the agents service opinionated about *what a BA is* and agnostic about *which project is asking*.

### GET /healthz
`{ "ok": true }`.

### Request / response rules
- Content type: `application/json` in, `text/event-stream` out (set by the SDK helper).
- Heartbeat not manually needed — the SDK protocol frames are frequent enough during generation, and `[DONE]` terminates cleanly. Add a comment-line heartbeat only if we observe proxy timeouts in practice.

## 6. Handler Shape

The BA stage is simple enough to skip the `createAgent` abstraction (which bundles one-shot `run` + Zod validation). The handler validates the request body, invokes `streamText` with the role system prompt + the client-supplied user prompt, and pipes the SDK's UI Message Stream directly to the response:

```ts
import express from 'express';
import { z } from 'zod';
import { streamText } from 'ai';
import { anthropic } from '@ai-sdk/anthropic';
import { config } from '../shared/config.js';
import { systemPrompt } from '../agents/business-analyst/prompt.js';

const RequestBody = z.object({ prompt: z.string().min(1) });

export function registerBusinessAnalyst(app: express.Express) {
  app.post('/v1/agents/business-analyst', (req, res) => {
    const parsed = RequestBody.safeParse(req.body);
    if (!parsed.success) {
      res.status(400).json({ error: parsed.error.format() });
      return;
    }

    const ac = new AbortController();
    req.on('close', () => ac.abort());

    const result = streamText({
      model: anthropic(config.model),
      system: systemPrompt,
      prompt: parsed.data.prompt,
      abortSignal: ac.signal,
    });

    result.pipeUIMessageStreamToResponse(res);
  });
}
```

`pipeUIMessageStreamToResponse` handles SSE framing, headers, and `[DONE]` termination. No custom envelope, no translator, no coalescing logic of our own.

The existing `buildUserPrompt` helper in `agents/src/agents/business-analyst/prompt.ts` remains exported for programmatic library use but is not called by the server path — prompt composition lives in the BFF.

When structured-output stages land, the shape becomes:

```ts
const stream = createUIMessageStream({
  execute: ({ writer }) => {
    const result = streamText({ ..., onFinish: ({ text }) => {
      const artifact = StageOutput.parse(JSON.parse(text));
      writer.write({ type: 'data-artifact', data: artifact });
    }});
    writer.merge(result.toUIMessageStream());
  },
});
pipeUIMessageStreamToResponse(res, { stream });
```

That's the point at which we introduce `createUIMessageStream` — not now.

## 7. Cancellation

`req.on('close', () => ac.abort())` wires client disconnect to `AbortController.abort()`, which propagates through `streamText`'s `abortSignal` down to the provider. The SDK emits a final `abort` chunk before closing. No residual state, no orphaned token consumption.

## 8. Error Handling

| Condition | Response |
|---|---|
| Input fails Zod | `400` JSON with error detail. No SSE opened. |
| LLM provider error mid-stream | SDK emits `error` chunk with `errorText`, then closes. |
| Client disconnect | `AbortController` fires → SDK emits `abort` → closes. |

No auto-retry, no fallbacks.

## 9. Configuration

```
PORT=3400
ANTHROPIC_API_KEY=sk-ant-...
AGENT_MODEL=claude-sonnet-4-...
LOG_LEVEL=info
```

## 10. BFF Integration

Console → BFF → agents. Never direct.

```
[Console]         [BFF: asdlc-service]         [agents: 3400]
    │ click         │                             │
    ├─────────────▶ │ POST /projects/:id/         │
    │ requirements/ │  requirements/generate      │
    │               │ compose prompt from         │
    │               │   project context + prior   │
    │               │   artifacts + feedback      │
    │               │ POST /v1/agents/            │
    │               │  business-analyst           │
    │               │  { prompt } ───────────────▶│
    │ ◀── SSE ──────│ ◀── SDK UI Message Stream ──│
    │               │   (text-delta × N)          │
    │ ◀── SSE ──────│ ◀── finish, [DONE] ─────────│
    │               │ accumulate text → write     │
    │               │   .asdlc/spec.md (draft)    │
    │ ◀── done ─────│                             │
    │                                              │
    │ click "Save & Proceed"                       │
    ├─────────────▶ │ POST /projects/:id/          │
    │               │  requirements/save           │
    │               │ commit + push + tag spec-v1  │
    │ ◀── done ─────│                              │
```

Two distinct BFF endpoints per artifact:
- **generate** — composes the prompt, calls agents, forwards the SSE stream to the console unchanged, accumulates `text-delta` content for the draft write.
- **save** — commits + tags. Handled by existing `versioning.go`.

BFF responsibilities on `generate`:
- Compose the full user prompt from project context + prior artifacts (if any) + user feedback. This is the BFF's concern; the agents service only sees the final string.
- Pass SSE frames through byte-for-byte to the console.
- Tee `text-delta` deltas into an in-memory buffer; on `finish`, write the buffer to `.asdlc/spec.md` (draft).
- On console disconnect, abort the agents connection (propagates `abort` down the chain).

The BFF contract surface is out of scope for this doc — described in `api-service.md` when updated.

## 11. What We Are NOT Doing (v1)

- Resumable streams / `Last-Event-ID` / execution IDs.
- State machines, gates, approval flow (BFF + git tags).
- Worker fan-out.
- Persistent event logs.
- Authn/z on the agents service.
- Multiple concurrent viewers of the same stream.
- Custom chunks of any kind (`data-*` parts). Deferred to the first structured-output stage.
- Token coalescing — the SDK already produces sensibly-sized deltas.

## 12. Build Order

Each step is independently testable and shippable.

1. **Express skeleton + `/healthz`.** Package entry point `src/server/index.ts`; `npm run serve` works.
2. **`POST /v1/agents/business-analyst`.** Handler as sketched in §6. Smoke-test with `curl -N`.
3. **Input validation + 400 path.** Zod validation, error shape.
4. **Cancellation.** Assert `abort` chunk fires and provider call terminates on `req.close`.
5. **BFF `requirements/generate` endpoint + SSE proxy.** Separate change in `asdlc-service`; this doc is the contract.

Later milestones (not part of this design iteration):
- Structured-output stages with `data-artifact` custom chunk (Architect, TechLead).
- Developer stage.
- `createAgent` streaming abstraction once two stages share the same shape.
