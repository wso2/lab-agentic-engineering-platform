# Agent Architecture

Design for the AI agent system that drives the spec-driven SDLC pipeline: user prompt → requirements → architecture → implementation plan → parallel implementation → review → build/deploy.

This document describes the **ideal agent architecture** independent of current implementation. It is the target shape the system should evolve toward.

## 1. Pattern Summary

The system is a composition of three patterns from [Anthropic's "Building Effective Agents"](https://www.anthropic.com/engineering/building-effective-agents):

> **Prompt-chained workflow** (BA → Architect → TechLead) with human gates
> **→ Orchestrator-workers fan-out** (Developer agents implement in parallel)
> **→ Evaluator-optimizer loop** (Reviewer ↔ Developer)
> **→ Deterministic deploy step.**

Only two components in the system are **true agents** (tool-use loops driven by environmental feedback): the Developer worker and the Reviewer. Everything else is an **augmented LLM call** or a **code-driven workflow step**. This distinction matters — it keeps cost bounded and control flow debuggable.

## 2. High-Level Shape

```
                    ┌─────────────────────────────────────┐
                    │   Orchestrator (code, not an LLM)   │
                    │   - run state machine               │
                    │   - streams events to client        │
                    │   - persists artifacts per stage    │
                    └──────────────┬──────────────────────┘
                                   │ invokes one stage at a time
          ┌────────────┬───────────┼────────────┬──────────────┐
          ▼            ▼           ▼            ▼              ▼
   BusinessAnalyst  Architect  TechLead    Reviewer       Deployer
   (requirements)  (arch JSON) (tasks JSON)(quality gate)  (CI/CD)
    [LLM call]     [LLM call]  [LLM call]  [agent loop]    [code]
                                   │
                                   │ dispatches N tasks
                                   ▼
                     ┌──────────────────────────┐
                     │  Worker Pool (Developer) │
                     │  - 1 agent per task      │
                     │  - isolated workspace    │
                     │  - MCP tools → platform  │
                     └──────────────────────────┘
```

## 3. Orchestrator

The orchestrator is **not an LLM**. It is a code-driven state machine that drives the pipeline.

```
DRAFT → REQS_GENERATING → REQS_REVIEW (human gate)
      → ARCH_GENERATING  → ARCH_REVIEW  (human gate, editable)
      → PLAN_GENERATING  → PLAN_REVIEW  (human gate)
      → IMPLEMENTING     (fan-out workers)
      → REVIEWING        (evaluator loop, may send tasks back)
      → BUILDING → DEPLOYED
```

Responsibilities:
- Load prior artifact(s), invoke the next stage, stream events, persist result, wait for gate or auto-advance.
- Own the event bus for SSE/WebSocket streaming to the client.
- Own the task queue for worker fan-out.
- Never delegates control flow to an LLM. Predictable stages → code-driven pipeline.

**Rationale**: the article is explicit — workflows offer predictability and consistency; agents are for open-ended problems where steps can't be enumerated in advance. SDLC stages are predictable, so the pipeline is a workflow.

## 4. Stage Definitions

Each stage has: an input artifact, a structured output schema, a tool set (often empty), and a prompt. Artifacts are stored on disk (git-tracked) between stages. No cross-stage conversation memory — each stage re-reads upstream artifacts.

| Stage | Kind | Input | Output (structured) | Tools |
|---|---|---|---|---|
| **BusinessAnalyst** | LLM call | user prompt + clarifications | `Requirements { goals, userStories[], constraints[], nonFunctional[] }` | none by default; `askUser` for one clarification round |
| **Architect** | LLM call | Requirements | `Architecture { overview, components[], dataFlows[], integrations[] }` | none (greenfield); repo-read tools only if brownfield |
| **TechLead** | LLM call | Architecture | `Plan { tasks[]: { id, componentId, deps[], acceptance } }` | none |
| **Developer** | **Agent** | one Task + repo context | `TaskResult { filesChanged[], summary, tests }` | full FS + shell + MCP (`reportProgress`, `submitImplementation`) |
| **Reviewer** | **Agent** | all TaskResults + Architecture + Plan | `ReviewReport { pass, issues[] { taskId, severity, detail } }` | `readFile`, `runTests`, `grep` |
| **Deployer** | code | approved build | deploy events | build/deploy APIs |

**Design rules:**
- **Default to zero tools on workflow stages.** Adding tools turns a clean LLM call into a mini-agent with unbounded cost. Add tools only when a concrete need is measured.
- **Structured output is contract.** The only way a stage "finishes" is by returning a value that validates against its schema. Use the provider's native structured output / tool-call mode.
- **One source of truth for schemas.** Zod definitions are shared by the agent tool definition, API DTOs, UI form validators, and on-disk artifact formats.

## 5. Orchestrator-Workers (Implementation Stage)

Entering `IMPLEMENTING` triggers fan-out:

1. Orchestrator materializes the Plan into `Task` rows (status=`queued`).
2. Tasks are pushed onto a work queue (Redis stream / Postgres `FOR UPDATE SKIP LOCKED` / equivalent).
3. A worker pulls one task, spins up a Developer agent in an isolated workspace (git worktree or container).
4. Developer agent talks back to the platform **only through MCP tools** the orchestrator owns: `get_task`, `get_context`, `report_progress`, `submit_result`.
5. On `submit_result`, the task row becomes `done`. Orchestrator waits for `COUNT(done) == N` via NOTIFY/pub-sub, then advances to `REVIEWING`.

**Rules:**
- Workers never mutate shared state directly — only through MCP tools.
- Task dependencies: topologically sort into waves, or let the Developer `wait_for_task(id)` via MCP.
- Every worker has a hard timeout + token budget + tool-call budget. Runaway agents are the #1 cost leak.
- Workers do not talk to clients; they publish to an internal event bus and the orchestrator session re-emits over SSE.

**Why orchestrator-workers and not parallelization**: subtasks are generated from the Architecture — they aren't pre-defined. That's the defining condition for orchestrator-workers per the article.

## 6. Evaluator-Optimizer (Review Loop)

The Reviewer stage is **mandatory and iterative**:

```
REVIEWING:
  report = Reviewer(all TaskResults)
  if report.pass: advance to BUILDING
  else:
    for each issue in report.issues:
      re-queue task with issue as extra context
    go to IMPLEMENTING (re-run only failing tasks)
    bounded by max_review_cycles (e.g. 3)
```

Only failing tasks are re-run, so the loop is cheap. This is the highest-leverage quality improvement in the system.

## 7. Message and Context Handling

- **System prompt** = role + output schema (inlined) + tool descriptions.
- **User message** = upstream artifact(s) as JSON in a fenced block, plus user edits/clarifications. Not prose reconstructions.
- **No cross-stage memory.** Each stage re-reads the prior artifact from storage. Keeps context windows small and makes re-runs deterministic.
- **Within-stage conversation** exists only for: tool loops (agents) and human clarifications. On a clarification, pause the loop, store pending state, resume on answer with `question + answer` appended.
- **Prompt caching**: system prompt + upstream artifact are stable within a stage → mark as a cache breakpoint. Large cost win on user-triggered re-runs.

## 8. Streaming (SSE / WebSocket)

**Default: SSE.** Use WebSocket only if bidirectional mid-stream input is required. For SDLC flows, SSE + a separate POST endpoint for user input is simpler, survives proxies, and is resumable.

### Event envelope

All system output flows through one envelope. Raw provider deltas are **not** exposed to the client.

```ts
type Event =
  | { type: "stage.started"; stage: Stage; runId }
  | { type: "agent.thinking"; stage; text }          // reasoning / narration
  | { type: "agent.token"; stage; delta }            // streamed text token
  | { type: "tool.call"; stage; tool; argsPreview }
  | { type: "tool.result"; stage; tool; ok }
  | { type: "artifact.partial"; stage; jsonPatch }   // structured output streaming
  | { type: "artifact.final"; stage; artifact }
  | { type: "worker.progress"; taskId; pct; message }
  | { type: "stage.completed"; stage }
  | { type: "stage.awaiting_input"; stage; prompt }
  | { type: "error"; stage; message }
```

### Streaming rules

- **Structured output streaming**: providers emit partial JSON for tool-call arguments. Emit `artifact.partial` with a JSON Patch (RFC 6902) so the UI can render incrementally (e.g., components appearing on the architecture diagram). Validate against Zod only at `artifact.final`.
- **Worker fan-out streaming**: workers publish to an internal pub/sub. The orchestrator session subscribes to `run:{id}:events` and re-emits over SSE. One fan-in point, one client connection.
- **Resumable streams**: SSE `Last-Event-ID` + monotonic event sequence. On reconnect, replay from `lastSeq`. Persist events to Postgres (`run_events` append-only table) for audit and resume.
- **Backpressure**: `agent.token` is high-volume — coalesce to ~20 Hz server-side or drop on slow clients. `tool.call`, `artifact.*`, `stage.*` are low-volume and guaranteed.

## 9. Human-in-the-Loop Gates

Gates live **between** stages, owned by the orchestrator, not inside any agent.

At each gate:
1. Orchestrator emits `stage.awaiting_input` with the artifact.
2. UI renders editable view (form for Requirements, JSON editor + diagram for Architecture, task list for Plan).
3. User approves, edits, or rejects (with feedback).
4. Edit: overwrite the artifact on disk and transition to next stage.
5. Reject with feedback: re-run the same stage with feedback appended as user message.
6. Approve: advance.

The same stage agent handles re-runs — there is no separate "edit mode."

## 10. Tech Choices (Reference)

These are defaults; revisit as the system scales.

- **LLM**: Claude Opus/Sonnet via Anthropic API. Native structured output via tool-use.
- **Orchestrator**: server-side code (any language), state persisted in Postgres.
- **Event bus**: Postgres LISTEN/NOTIFY for <100 concurrent runs; Redis streams or NATS beyond.
- **Work queue**: Postgres `FOR UPDATE SKIP LOCKED` for simplicity; upgrade later if needed.
- **Worker isolation**: git worktree + process per task for local; container per task for production.
- **Agent↔platform protocol**: MCP (Model Context Protocol) over streamable HTTP.
- **Schema**: Zod as single source of truth.

## 11. What NOT to Build

- **A "master orchestrator" LLM** that decides which stage runs next. Control flow is code.
- **Subagents inside workflow stages** (e.g., Architect spawning ComponentDesigner subagents) until measured evidence shows single-call quality degrades.
- **Tools on workflow stages** that aren't needed — every unused tool is a cost leak.
- **Cross-stage conversational memory.** Stages read artifacts, not transcripts.
- **Custom streaming protocols per stage.** One envelope, everywhere.
- **Autonomous planning.** This is a workflow, not an autonomous agent.

## 12. Build Order

1. Event envelope + SSE transport + `run_events` append-only table.
2. Orchestrator state machine with two stages (BA + Architect) and gates.
3. Structured output via tool-call + Zod, streaming `artifact.partial`.
4. Add TechLead, then fan-out with a single worker, then N workers via MCP.
5. Reviewer with evaluator-optimizer loop.
6. Deployer.

Each step is shippable and independently testable.
