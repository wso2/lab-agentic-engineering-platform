# Tech-Lead Agent — Task Generation

The tech-lead agent generates GitHub-issue-backed implementation tasks from the approved spec + design. It replaces the previous one-shot `task-generator` (and the dead `tech-lead` phases/risks agent). Implementation lives in `agents/src/agents/tech-lead/` and `agents/src/server/routes/tech-lead.ts`; persistence and GitHub orchestration live in `asdlc-service/services/task_service.go`.

Related:
- `architect-agent.md` — the upstream artifact this consumes; introduces the SSE shape we mirror.
- `agents-service.md` — shared SSE wire protocol.
- `api-service.md` — BFF; owns task DB rows and GitHub issue lifecycle.
- `git-integration.md` — `git-service` interface used to create / edit / close issues.

## 1. Goals

The previous task-generator (`generateObject` per component, called serially from the BFF in a loop) had three problems:

1. **No streaming UX.** Console called `POST /tasks/generate` and waited tens of seconds for the full array. Even the only valuable LLM output — the per-task instructions and considerations — was hidden behind a `<ExternalLink>` to GitHub. The task page showed only title + status.
2. **No incremental path.** A second run wiped existing pending tasks and regenerated from scratch. Anything past pending blocked regeneration entirely (`ErrTasksInFlight`). Adding one component meant either manually editing GitHub or living with a stale plan.
3. **Design data was denormalised onto each task row.** OpenAPI spec, app path, buildpack, language, key considerations all snapshotted into `ComponentTask` at generation time. A later design edit didn't propagate; tasks pointed at stale contracts.

The redesign:
- Splits generation into a **plan** phase (streamed array) and a **detail** phase (parallel streamed markdown bodies). UI cards land in seconds; bodies stream in afterwards.
- Treats each generation run as an **append-only batch** of new tasks. Prior tasks are immutable history. No "preserve verbatim" instruction, no diff-edit semantics on tasks.
- Keeps `.asdlc/design.json` as the single source of truth for component shape, OpenAPI, buildpack, etc. Task rows hold only task-level state.

## 2. Scope

**Phase 1 (this doc, ship).**
- Two-phase generation pipeline: plan (`streamObject`) then detail (`streamText` fan-out).
- SSE protocol with per-task body streaming; GH issue created on plan completion, body edited on detail completion.
- BFF reconciliation: pending tasks for components removed by the latest design diff are auto-closed with `cause=design.removed`.
- Schema migration: drop snapshot fields from `ComponentTask`; dispatch path reads design fresh.
- Multiple tasks per component allowed (incremental adds new work to existing components).
- Lineage: `SourceDesignVersion`, `SourceSpecVersion`, `BatchID` per task row.

**Out of scope.**
- No tasks.json artifact. Truth = DB rows + GitHub issues; lineage on the row.
- No tool calling. Body is the only large payload; `streamText` covers it natively.
- No "modify existing task" surface. Old tasks are history. If the human wants the work done differently, they edit the GH issue directly or close it and let a future generation pick up the gap.
- No phases/risks output (the previous tech-lead agent is deleted entirely).

## 3. Architecture

### Data flow — two-leg agents-service, BFF as merger

The existing agents-service convention (see `architect.ts`) is one HTTP POST → one SSE stream → one `data-finish`. We keep that convention by **splitting the agent into two routes** and making the BFF the single SSE producer for the console. The console always sees one continuous SSE stream; agents-service is called twice in sequence.

agents-service exposes:

| Route | Purpose | Stream events |
|---|---|---|
| `POST /v1/agents/tech-lead/plan` | Phase 1 — produce the plan | `data-plan-item × N`, `data-plan-complete{items}`, then closes |
| `POST /v1/agents/tech-lead/detail` | Phase 2 — produce N body bodies in parallel | `data-task-body-delta`, `data-task-body-complete`, then closes |

The BFF is the **only** SSE producer the console connects to. It owns the merging:

```
console ──POST /tasks/generate (SSE)──▶ asdlc-service (BFF, holds stream open)
                                                    │
            ┌───────────────────────────────────────┤
            │ T1  open POST plan SSE → agents-service
            │ T2  proxy data-plan-item × N → console
            │ T3  proxy data-plan-complete (transform: drop, BFF will emit later)
            │     Phase-1 leg closes.
            │
            │ T4  persist task rows + create GH issues in parallel
            │     (p-limit 3). Per success → emit data-task-issued to console.
            │     Per failure → emit data-task-issue-failed to console.
            │
            │ T5  open POST detail SSE → agents-service with the surviving
            │     {taskId, componentName, title, rationale, designSlice}[].
            │ T6  proxy data-task-body-delta / data-task-body-complete → console.
            │     On each body-complete: async gh issue edit.
            │     Phase-2 leg closes.
            │
            │ T7  reconciliation pass; emit data-task-rejected × M → console.
            │ T8  BFF emits data-finish; closes the console SSE.
```

**The console's SSE contract is unchanged from architect's**: one POST, one continuous stream, one `data-finish`. The two-leg split is invisible to the console.

**Ordering invariants** (load-bearing for the wire protocol — see §6):
- `data-task-issued{taskId}` always precedes the first `data-task-body-delta{taskId}` for that task. Phase 2 leg only starts after T4 finishes, so no `tempId → taskId` rekey is required on the console.
- All `data-task-rejected` events arrive between the last `data-task-body-complete` and `data-finish`.

**Failure handling across legs:**

| Failure | Behaviour |
|---|---|
| Phase-1 leg errors | BFF emits `data-error{scope:"plan"}`, then `data-finish`. No issues created. |
| Phase-1 closes but validator (server-side in agents-service) fails | Agent emits `data-error{scope:"plan", issues}` instead of `data-plan-complete`. BFF proxies it and closes. |
| GH issue creation fails for K of N items | BFF emits `data-task-issue-failed × K`. Phase-2 leg is opened with the surviving N−K items. The K orphaned plan items are *not* re-attempted automatically — user retries via UI button (§13). |
| Console SSE drops between T2 and T8 | Plan items are already persisted as rows (issueState=pending or issued). On reconnect, console hits `GET /tasks` → renders cards from DB. The half-finished generation completes server-side regardless: BFF orchestration is **not** tied to the console socket; it runs to T8 and updates DB + GitHub. The next `GET /tasks` shows the finished result. |
| Console disconnect → BFF cancels orchestration | Configurable via `TECH_LEAD_DETACH_ON_DISCONNECT` (default `false`). Default behaviour: orchestration completes; orphaned generations are DB-only. With `true`: an `AbortController` cancels both legs and aborts pending GH creates; partial state remains in DB but no further side effects. We default to `false` because user disconnects (closing tab) shouldn't waste a generation. |
| Phase-2 leg errors entirely | BFF emits `error{scope:"detail"}` for each task with `bodyState: "pending"`, then runs reconciliation, then emits `data-finish`. Cards stay with placeholder bodies; user retries per task. |

### TaskBatch (per-request, in-memory, agents-service)

There is **no persistent doc**. Each generation request constructs a transient `TaskBatch` to hold validator state and to feed Phase 2:

```ts
type PlanItem = {
  tempId: string;          // assigned by agents-service; opaque to LLM
  componentName: string;   // ref into current design.json
  title: string;           // GitHub issue title
  rationale: string;       // ~1 sentence — why this task exists
  dependsOn: string[];     // titles of other plan items in this batch
};

class TaskBatch {
  items: PlanItem[];           // populated by Phase 1
  bodies: Map<string, string>; // tempId → markdown body, populated by Phase 2

  validatePlan(currentDesign, existingTasks): PlanIssue[]
  attachBody(tempId, body): void
  isComplete(): boolean        // every item has a non-empty body
}
```

The batch is throwaway state for the lifetime of one HTTP request. It exists to gate Phase 2 (validator must pass first) and to track per-task body completion so `data-finish` only emits when every body is in.

## 4. Two-phase pipeline

### Phase 1 — Plan (`streamObject`)

Single Anthropic call with `streamObject` against `z.array(PlanItem)`. The AI SDK's `partialObjectStream` yields a *progressive snapshot* of the partially-parsed array on every model token chunk — it is **not** a sequence of "completed element" events. Per-element emission is a derived signal we compute server-side, and we **must not emit a partially-streamed element** (e.g. ship `rationale: "Core CRUD"` while the model is still typing `" service backing the todo list"`).

The seal rule:

> Element at index `i` is sealed when the partial array has length ≥ `i + 2` **OR** the stream has ended. Sealing means: index `i` is no longer the trailing element being typed; the model has begun emitting index `i + 1`. Once sealed, the value is treated as final.

This is structural, not heuristic — JSON parsing reaches `partial.length === i + 2` only after the parser has seen the comma + opening `{` for element `i + 1`, by which point element `i`'s closing `}` is already in the buffer. A `safeParse` for the full `PlanItem` shape on element `i` is also required before emit (defends against partial JSON that happens to be valid but missing fields).

```ts
let sealedThrough = -1;   // highest index already emitted
for await (const partial of result.partialObjectStream) {
  if (!Array.isArray(partial)) continue;
  // An element i is sealed iff the array has reached element i+1 (so i is
  // no longer being typed). The trailing element is never emitted from
  // inside the loop — only at stream end.
  const sealedTo = partial.length - 2;
  for (let i = sealedThrough + 1; i <= sealedTo; i++) {
    const result = PlanItem.safeParse(partial[i]);
    if (!result.success) {
      // Sealed but doesn't fully validate → schema violation; abort run.
      sse.send("error", { scope: "plan", errorText: "malformed-plan-item",
                          index: i, issues: result.error.format() });
      return;
    }
    sse.send("plan-item", { tempId: `p-${i}`, ...result.data });
    sealedThrough = i;
  }
}
// Stream ended — flush the trailing element (if any).
const final = await result.object;
for (let i = sealedThrough + 1; i < final.length; i++) {
  sse.send("plan-item", { tempId: `p-${i}`, ...final[i] });
}
```

**Why no fallback flag is needed.** The seal rule structurally cannot ship truncated content. If the model retries / repairs JSON in a way that shifts earlier offsets (rare with structured output mode), the worst case is `safeParse` rejecting a sealed element — that aborts the run with a structured error, not a silently-truncated emission. The fixture-replay test in §14 covers the happy path; the seal rule is the production guard.

| Event mapping | Phase 1 |
|---|---|
| Element first becomes sealed + parse-complete | `data-plan-item{tempId, ...item}` |
| Stream end | Trailing element flushed; validator runs (§5) → `data-plan-complete{items}` or `data-error{scope:"plan"}` |

The plan is intentionally cheap: title + rationale + componentName + dependsOn. No OpenAPI bodies in the prompt, no markdown bodies in the output. Plan completes in seconds even for 8-component projects.

**Single-shot degenerate case.** For 1-item plans (or any run where every element seals only at stream end) the wire shape is identical: all `data-plan-item` events emit at stream-end, immediately followed by `data-plan-complete`. The console renders the same way; the only UX difference is that cards arrive as a batch.

### Phase 2 — Detail (`streamText`, fan-out)

After Phase 1 completes, the BFF persists rows + creates GH issues in parallel (see §3 step T3, §10) and then signals agents-service to start Phase 2 over the now-issued tasks. agents-service spawns one `streamText` per task. A `p-limit(4)` cap keeps Anthropic concurrency bounded per project.

Each detail call gets a focused prompt:

```
## Project: <name>
## Task: <plan.title>
## Rationale: <plan.rationale>
## Component to implement: <componentName>
<full design.json entry for this component, including OpenAPI YAML>

## Components this task depends on (slim — name, type, language only)
<for each dep>

## Existing tasks already merged for this component (titles only, for context)
<list>

Write the GitHub issue body in markdown using this structure:
  ## What
  ## Acceptance criteria
  ## Implementation notes
  ## Contracts (refer to .asdlc/design.json — do not duplicate the spec inline)
  ## Dependencies
```

The Local Developer Setup section, "Closes #N", and branch hint are **template-appended by the BFF after the LLM body** — same pattern as `issue_body.go` today. The model never writes them.

| Event mapping | Phase 2 |
|---|---|
| Token delta | Coalesced server-side to ~250ms ticks → `data-task-body-delta{taskId, delta}` |
| Stream end (per task) | `data-task-body-complete{taskId, body}` |
| All complete | `data-finish{batchId, taskCount}` |
| Per-task error | `error{scope:"detail", taskId, errorText}`; other tasks continue |

A Phase 2 failure for one task does **not** abort the batch. The issue stays with its placeholder body; the user retries that one task. (Retry endpoint: `POST /tasks/{id}/regenerate-body`.)

## 5. Plan validator

Runs server-side in agents-service, between Phase 1 stream end and BFF issue creation.

- `componentName` exists in current `design.json` for every plan item.
- `dependsOn` resolves to another item in the same batch (by title) **or** to an existing non-rejected task in the project.
- No cycles in `dependsOn` (within batch + against existing non-rejected tasks).
- No two plan items share `title` (issue-title collisions are confusing and break dependsOn-by-title).
- Plan is non-empty in **fresh-iteration mode** (no prior batch).
- **ADDED-component coverage**: every component listed as `ADDED` in the design diff must appear as the `componentName` of at least one plan item. Catches the model laziness mode where a major architecture change produces an empty or under-covered plan.
- Empty plan is allowed in **incremental mode** *only when the design diff has zero `ADDED` components and zero contract-affecting `MODIFIED` entries* (where contract-affecting = openapi changed OR dependsOn changed). Otherwise the validator demands at least one plan item per non-trivial diff entry.

Issue shape:
```ts
{ tempId?: string, code: string, ...detail }
// e.g. { tempId: "p-2", code: "unknown-component", componentName: "user-svc" }
//      { tempId: "p-3", code: "dangling-dep", dep: "Set up auth" }
//      { code: "title-collision", title: "Implement notify" }
//      { code: "missing-coverage", componentName: "notify-svc",
//          reason: "ADDED in design diff, no plan item targets it" }
//      { code: "missing-coverage", componentName: "todo-api",
//          reason: "openapi changed, no plan item targets it" }
```

Validator failure → `data-error{scope:"plan", issues:[...]}` and the run aborts before any GH issue is created. The user retries (typically by re-running; if missing-coverage persists, the prompt nudges the model with the issue list on retry).

## 6. SSE protocol

| Event | Payload | When |
|---|---|---|
| `data-plan-item` | `{ tempId, componentName, title, rationale, dependsOn }` | each element completes during Phase 1 |
| `data-plan-complete` | `{ items: PlanItem[] }` | Phase 1 stream ends + validator passes |
| `data-task-issued` | `{ tempId, taskId, issueUrl, issueNumber }` | BFF finishes creating the GH issue (one per plan item; emitted as soon as that issue's REST call returns) |
| `data-task-issue-failed` | `{ tempId, errorText }` | BFF could not create the GH issue for that plan item (rate limit, permission, etc.). The plan item is dropped from Phase 2 fan-out; the corresponding card surfaces a "Retry plan item" button. |
| `data-task-body-delta` | `{ taskId, delta }` | coalesced 250ms ticks during Phase 2. Always preceded by `data-task-issued` for the same `taskId`. |
| `data-task-body-complete` | `{ taskId, body }` | per-task Phase 2 stream ends; BFF edits the issue |
| `data-task-rejected` | `{ taskId, reason }` | BFF reconciliation, between the last `data-task-body-complete` and `data-finish` |
| `data-finish` | `{ batchId, taskCount }` | all bodies complete AND reconciliation done |
| `error` | `{ errorText, scope: "plan" \| "detail", tempId?, taskId? }` | unrecoverable for that scope |

### Identity discipline

The wire uses two id namespaces:
- **`tempId`** — assigned by agents-service to each Phase 1 element (`p-0`, `p-1`, …). Used only for plan-time events: `data-plan-item`, `data-task-issued` (carries both `tempId` and `taskId`), `data-task-issue-failed`.
- **`taskId`** — the persisted DB UUID. Used for every Phase 2 / reconciliation event: `data-task-body-delta`, `data-task-body-complete`, `data-task-rejected`.

**Phase 2 fan-out is gated on issue creation.** agents-service starts Phase 2 only after the BFF has either created an issue (→ `data-task-issued`) or recorded a creation failure (→ `data-task-issue-failed`) for every plan item. Therefore the console never receives a `data-task-body-delta` before the corresponding `data-task-issued`. There is no need for a `tempId → taskId` rekey on the console — Phase 2 events arrive only after identity is stable.

Events are name-keyed and idempotent under reorder. Server checks `res.writableEnded` and bails when the socket is closed.

**Keep-alive.** Both agents-service routes (`/plan`, `/detail`) and the BFF's console-facing stream emit a `: keep-alive` SSE comment line every 15s while the connection is open, mirroring `architect.ts`'s heartbeat. Without this, OpenChoreo gateway / browser idle timeouts can drop a stream during the 8-15s per-body Phase-2 wait or during BFF-side issue creation between the two legs.

## 7. Incremental input

The Phase 1 user prompt in incremental mode (i.e. when at least one prior task batch exists for the project):

```
Project: <projectName>

## Specification (current)
<spec.md>

## Architecture (slim — no OpenAPI bodies)
<{name, componentType, language, dependsOn} for each component>

## What changed since the last task batch

### Spec diff (vs <SourceSpecVersion of last batch>)
<unified diff lines, +/- only>

### Design diff (vs <SourceDesignVersion of last batch>)
- ADDED component: <name> (<type>, <lang>)
- MODIFIED component: <name>
    - dependsOn: + <newDep>, - <removedDep>
    - openapi: + POST /foo, ~ PUT /bar (request schema)
- REMOVED component: <name>

## Existing tasks (for context — do not duplicate)
- #12 "Implement todo-api" — merged
- #13 "Implement todo-web" — merged
- #14 "Add /metrics to todo-api" — pending

## Your job
Produce the list of NEW tasks needed for the changes above. A change may
require zero, one, or multiple new tasks — judge based on size. A new task
may target a component that already has merged tasks; that's normal in
incremental mode. Do not propose tasks that duplicate work in
"Existing tasks".
```

Diffs are computed by the BFF, not the agent:
- **Spec diff**: unified diff of current `spec.md` vs the spec at the *baseline* `SourceSpecVersion` (defined below). Drop the `+++/---` headers; keep `+`/`-` lines.
- **Design diff**: `task_diff.go` walks current vs baseline `design.json`. Per-component additions/removals/modifications (`dependsOn` set diff, `openapi` op-level diff via the same parse used by `openapi_normalize.go`).

### Baseline selection — "the most recent prior batch"

The baseline is computed deterministically from DB state. It is **not** "newest by createdAt of the batch row" — that breaks if a batch was discarded.

```sql
SELECT batch_id, source_spec_version, source_design_version, MAX(created_at) AS batch_created_at
FROM component_tasks
WHERE project_id = $1
  AND batch_id IS NOT NULL                       -- skip legacy rows
  AND status IN ('pending', 'in_progress',
                 'ready_for_review', 'merged',
                 'building', 'deployed')          -- "live" tasks only
GROUP BY batch_id, source_spec_version, source_design_version
ORDER BY batch_created_at DESC
LIMIT 1;
```

Plain English:
- Status filter excludes `rejected` / `failed` / `abandoned` — those represent work the user (or the platform) discarded. Their batch is allowed to be the baseline only if at least one *live* task in the same batch survived.
- A batch is a *baseline candidate* iff it has ≥ 1 live task.
- Baseline = the most recent live-tasked batch. If none, `NULL` → fresh-iteration mode.
- All tasks in a batch share `source_spec_version` / `source_design_version`, so the GROUP BY is safe.

`SourceSpecVersion` / `SourceDesignVersion` from the baseline batch are what the diff is computed against — exact tag versions, not "previous version of the artifact" (which would be ambiguous after a revert + re-save).

### Existing-tasks rule for the prompt — and partial-discard handling

The "Existing tasks" section in the Phase 1 prompt is **not** the same set as the baseline filter. It serves a different purpose: telling the model what work is already real and should not be duplicated. The rule:

> Include in "Existing tasks" only tasks with status ∈ {`merged`, `building`, `deployed`, `ready_for_review`, `in_progress`, `pending`}. Exclude `rejected`, `failed`, `abandoned`.

Concrete consequence — the partial-discard scenario:
- Batch B5 had 4 tasks: 2 merged, 2 manually rejected by the user.
- Baseline batch = B5 (it has live merged tasks, so it qualifies).
- Diff is computed vs B5's `source_design_version`.
- "Existing tasks" lists only the 2 merged tasks. The 2 rejected ones are *not* listed.
- The model sees: "diff says these components had work, prior tasks merged work for some of them, but two components/areas have no covering existing task." It naturally re-proposes those.

This is the right behaviour: rejected work falls out of the "do not duplicate" set, so the model can pick it up again. The user gets a fresh attempt without manually flagging anything.

The baseline is also what the UI uses to label the "New tasks for design-vN" divider (§11) — by reading current design version vs baseline's design version.

For **fresh-iteration mode** (no prior batch), the diff sections collapse:

```
### Spec diff
(initial spec — entire document is new)

### Design diff
- ADDED component: <name> ...    ← every component listed
- (no MODIFIED, no REMOVED)

## Existing tasks
(none)
```

Same prompt template, degenerate values. One code path.

## 8. System prompt (key clauses)

```
You are a senior tech lead. You produce GitHub issues that translate
specification + architecture changes into concrete implementation work.

You operate in two phases. In Phase 1 you produce a list of task plans.
In Phase 2 you write the issue body for each plan item. The phases are
separated; you only see one prompt at a time.

# Phase 1 — Plan

Output a JSON array of task plans. Each plan has:
  - componentName  (must exist in the current architecture)
  - title          (GitHub issue title format)
  - rationale      (one sentence — why this task exists)
  - dependsOn      (titles of other plans in this batch this depends on)

Rules:
  - Each task targets exactly one component (componentName).
  - Multiple tasks may target the same component. This is normal in
    incremental mode when a component is gaining new functionality.
  - Do not propose tasks that duplicate work listed under "Existing tasks".
  - In incremental mode, scope each task to the change in the spec/design
    diff. Do not re-plan the original implementation — that work is already
    captured by existing merged tasks.
  - dependsOn names must match other titles in this batch verbatim. To
    depend on already-merged work, omit it from dependsOn (it's done).
  - Order does not matter — dependsOn carries the topology.

# Phase 2 — Detail

You write the GitHub issue body for one task. Structure:

  ## What         (one paragraph)
  ## Acceptance criteria  (testable bullets — what "done" means)
  ## Implementation notes (gotchas, key considerations)
  ## Contracts          (reference .asdlc/design.json — do not inline the
                          OpenAPI spec; cite the path / operation / schema)
  ## Dependencies      (other tasks in this batch, by title)

Rules:
  - Refer to OpenAPI specs by path/operation, not by inlining YAML.
    The implementing agent will read the spec from .asdlc/design.json.
  - Acceptance criteria are testable. "Returns 200 on /health" — not "is
    healthy".
  - Do NOT include local developer setup, branch checkout, or "Closes #N".
    The platform appends those.
```

## 9. Sample sessions

### A. Fresh — todo app (3 components)

Phase 1:
```jsonc
[
  { "componentName": "todo-api", "title": "Implement todo-api", "rationale": "Core CRUD service backing the todo list.", "dependsOn": [] },
  { "componentName": "todo-web", "title": "Implement todo-web", "rationale": "User-facing UI consuming todo-api.", "dependsOn": ["Implement todo-api"] },
  { "componentName": "notify-svc", "title": "Implement notify-svc", "rationale": "Sends due-date notifications.", "dependsOn": [] }
]
```
SSE: `data-plan-item × 3`, `data-plan-complete`. UI shows three cards. BFF creates three GH issues, emits `data-task-issued × 3`.

Phase 2 fan-out (parallel, capped at 4): three `streamText` runs. SSE: `data-task-body-delta × many`, `data-task-body-complete × 3`. BFF edits each issue body when complete.

`data-finish{batchId, taskCount: 3}`.

### B. Incremental — add notification feature

Prior batch: `todo-api` and `todo-web` (merged). Spec adds "users get notified when a todo is due." Architecture diff adds `notify-svc` and modifies `todo-api`'s OpenAPI to add `POST /todos/{id}/notify`.

Phase 1:
```jsonc
[
  { "componentName": "notify-svc", "title": "Implement notify-svc", "rationale": "New service for due-date notifications.", "dependsOn": [] },
  { "componentName": "todo-api",   "title": "Add notification endpoint to todo-api", "rationale": "Expose POST /todos/{id}/notify to schedule reminders via notify-svc.", "dependsOn": ["Implement notify-svc"] }
]
```

The agent does NOT re-propose "Implement todo-api" — it's in Existing tasks (merged). It DOES propose a new task targeting `todo-api` because that component now has new work. Multiple tasks per component is fine.

### C. Incremental — component removed

Prior batch: `todo-api`, `todo-web`, `legacy-export-svc` (all merged). Spec drops the export feature; architecture removes `legacy-export-svc`.

Phase 1: agent outputs an empty array (validator allows in incremental mode), or one cleanup task ("Remove legacy-export-svc references from todo-api") if the modified-component diff shows todo-api has dangling deps.

BFF reconciliation runs after `data-finish`: pending tasks targeting `legacy-export-svc` (none in this case) would be auto-closed. Merged tasks targeting `legacy-export-svc` are left alone — they're history. The component's running deployment is handled by a separate decommission flow (out of scope here).

### D. Phase 2 partial failure

Phase 1 plans 4 tasks. Phase 2 fan-out: 3 succeed, 1 hits a transient Anthropic error. SSE: `data-task-body-complete × 3`, `error{scope:"detail", taskId: t4, errorText:"..."}`, then `data-finish{taskCount: 4}` (the run *did* finish; one task has placeholder body).

Console renders task t4 with an inline retry button → `POST /tasks/t4/regenerate-body` re-runs Phase 2 for that single task.

## 10. BFF — issue lifecycle and reconciliation

The BFF performs three distinct mechanical (no-LLM) passes around the agent stream.

### 10.1 Plan persistence + GH issue creation (Phase 1 → Phase 2 gate)

On `data-plan-complete`:

```go
const issueCreateConcurrency = 3   // bounded GH REST concurrency

g, gctx := errgroup.WithContext(ctx)
g.SetLimit(issueCreateConcurrency)

for _, item := range plan.Items {
  item := item
  g.Go(func() error {
    task := persistTaskRow(item, batchID, designVersion, specVersion)  // status: pending
    issue, err := gh.CreateIssue(gctx, ..., placeholderBody(task))
    if err != nil {
      sse.send("data-task-issue-failed", { tempId: item.TempID, errorText: err.Error() })
      // task row stays with status=failed, cause=github.create_failed
      return nil   // partial-batch: do not abort siblings
    }
    persistIssueMetadata(task, issue)
    sse.send("data-task-issued", { tempId: item.TempID, taskId: task.ID,
                                   issueUrl: issue.URL, issueNumber: issue.Number })
    return nil
  })
}
g.Wait()
```

After `g.Wait()`, the BFF tells agents-service to start Phase 2 over the issued task list (POST `/v1/agents/tech-lead/detail` with the resolved `taskId`s + per-task design slices). Issue-create failures are skipped from Phase 2; the user retries them via `POST /tasks/{id}/regenerate-issue` (drops the failed row, re-runs creation).

`p-limit(3)` is the GH REST concurrency budget. Anthropic's secondary rate limit on issue creation is per-org; 3 in flight is safely below typical thresholds and avoids `429`. On `429`, the BFF retries the individual create with exponential backoff (max 3 attempts, ~5s budget) before emitting `data-task-issue-failed`.

### 10.2 Body persistence + GH issue edit (during Phase 2)

For each `data-task-body-complete`:

```go
task.Body = body
save(task)
go func() {
  if err := gh.EditIssueBody(ctx, ..., renderFinalBody(task)); err != nil {
    // Logged + retried with backoff (3 attempts). The DB row holds the
    // canonical body — even if GH edit ultimately fails, dispatch reads
    // task.Body from DB, not from GH.
    retryEditWithBackoff(...)
  }
}()
```

Issue edit runs async to the SSE stream — it never blocks `data-task-body-complete` from emitting. Failure to edit is observable via a `body-sync-pending` flag on the row (drained by a periodic reconciler).

### 10.3 Reconciliation pass (after Phase 2, before `data-finish`)

```go
for _, task := range pendingTasksForProject {
  comp := designLookup(task.ComponentName)
  if comp == nil {
    closeIssue(task.IssueNumber, "Component removed from architecture.")
    task.Status = TaskStatusRejected
    task.Cause  = ptr("design.removed")
    save(task)
    sse.send("data-task-rejected", { taskId: task.ID, reason: "design.removed" })
  }
}
sse.send("data-finish", { batchId, taskCount })
```

Only `pending` tasks (no GH activity yet) are auto-closed. Anything `≥ ready_for_review` is left alone — the human decides. Anything terminal (merged, deployed, rejected, failed, abandoned) is untouched.

This logic is exclusively BFF-side. The agent never sees `task.Status`; it sees `Existing tasks` only as title + status text in the prompt.

### 10.4 Reconciliation also runs on design save (not on architect finish)

A design change without a tech-lead run can still leave `pending` tasks pointing at removed components. The same reconciliation pass therefore also runs from `design_service.SaveAndProceed` — **only** the explicit save action, after the design tag is bumped — never from architect's `data-finish` (which writes the working copy but is not user approval). It emits no SSE (no open stream) but updates DB rows and closes issues identically. Tasks rejected this way show `cause=design.removed` in the UI and via the `ListTasksByOrg` filter, same as the in-stream path.

This closes the gap where `architect → save` advances `design-vN` but no follow-up `tech-lead` run happens for hours/days. Without this, dispatch could later try to execute a pending task whose component no longer exists.

**Service dependency.** `design_service` today does not touch tasks. The reconciliation hook is exposed by `task_service` as `ReconcilePendingForDesignChange(ctx, orgID, projectID)` and called from `design_service.SaveAndProceed` after the tag bump. Concretely:

```go
// task_service.go — public, idempotent, no SSE.
func (s *taskService) ReconcilePendingForDesignChange(ctx context.Context, orgID, projectID string) error

// design_service.go SaveAndProceed — at the end, after gitClient.Tag(...).
if err := s.taskSvc.ReconcilePendingForDesignChange(ctx, orgID, projectID); err != nil {
    slog.WarnContext(ctx, "task reconciliation after design save failed", "error", err)
    // Best-effort: don't fail the save. Periodic reconciler picks it up.
}
```

The reverse dependency (task_service depending on design read) already exists via `ArtifactStore`, so this adds only `designService → taskService` — a one-way edge from a higher-level orchestrator to a lower-level service, which matches the existing layering.

## 11. Console UI — `ProjectTasksPage.tsx`

The page is rewritten around the SSE stream. The static "Generate Tasks" button + table is gone; in its place is a single rendering surface that handles:
- The empty state (no tasks yet, "Generate Tasks" button).
- The streaming state (cards being filled in).
- The settled state (cards with full bodies + GH links + dispatch button).
- The incremental state (mix of frozen prior tasks + new streaming tasks from this batch).

### State shape

```ts
type CardState = {
  // Identity. tempId is the only key during plan + issue-create; once
  // data-task-issued lands, taskId is filled in and used for downstream
  // events. The server guarantees no body events arrive before
  // data-task-issued, so a single key (tempId) covers the streaming UI;
  // taskId is just a label.
  tempId: string;
  taskId?: string;
  componentName: string;
  title: string;
  rationale: string;
  dependsOn: string[];
  issueUrl?: string;
  issueNumber?: number;
  issueState: "pending" | "issued" | "failed";
  issueError?: string;
  body: string;                    // accumulated from data-task-body-delta
  bodyState: "pending" | "streaming" | "complete" | "error";
  bodyError?: string;
  status: TaskStatus;              // populated from authoritative refetch on data-finish
  isFromPriorBatch: boolean;       // true for cards rendered from initial GET
};
```

Cards are stored in a `Map<string, CardState>` keyed by `tempId`. Prior-batch cards (rendered from the initial `GET /tasks`) are inserted with `tempId = "prior-" + taskId` so the keying scheme is uniform. No dual-key map, no rekey logic.

### Rendering

Each card has three layers, vertically:

```
┌─────────────────────────────────────────────────────────────┐
│  [component chip]  Task title                  [GH link →]  │
│  Rationale: one-line why.                                    │
├─────────────────────────────────────────────────────────────┤
│  [Body — one of:]                                           │
│   pending:   "Generating details…" + spinner                │
│   streaming: <ReactMarkdown> rendered live as deltas arrive │
│   complete:  <ReactMarkdown> final body                     │
│   error:     red banner + "Retry body" button               │
├─────────────────────────────────────────────────────────────┤
│  [Status chip]  [Depends on: task-A, task-B]                │
└─────────────────────────────────────────────────────────────┘
```

Cards from prior batches render in `complete` state immediately (body fetched from DB on initial load).

### SSE consumption

```ts
// taskId → tempId index, populated on data-task-issued. Lets reconciliation
// events (data-task-rejected) keyed by taskId find their card.
const taskIdToTempId = new Map<string, string>();

const stream = api.streamGenerateTasks(orgId, projectId);

stream.on("plan-item", e => {
  cards.set(e.tempId, {
    tempId: e.tempId,
    componentName: e.componentName,
    title: e.title,
    rationale: e.rationale,
    dependsOn: e.dependsOn,
    issueState: "pending",
    body: "",
    bodyState: "pending",
    isFromPriorBatch: false,
    status: "pending",
  });
});

stream.on("plan-complete", _ => setPhase("issuing"));

stream.on("task-issued", e => {
  const card = cards.get(e.tempId);
  if (!card) return;
  card.taskId = e.taskId;
  card.issueUrl = e.issueUrl;
  card.issueNumber = e.issueNumber;
  card.issueState = "issued";
  taskIdToTempId.set(e.taskId, e.tempId);
});

stream.on("task-issue-failed", e => {
  const card = cards.get(e.tempId);
  if (!card) return;
  card.issueState = "failed";
  card.issueError = e.errorText;
  card.bodyState = "complete";   // no Phase 2 will run for this card
});

stream.on("task-body-delta", e => {
  const tempId = taskIdToTempId.get(e.taskId);
  const card = tempId ? cards.get(tempId) : undefined;
  if (!card) return;             // unreachable per protocol; defensive
  card.body += e.delta;
  card.bodyState = "streaming";
});

stream.on("task-body-complete", e => {
  const card = cards.get(taskIdToTempId.get(e.taskId)!);
  if (!card) return;
  card.body = e.body;            // authoritative — replaces accumulated deltas
  card.bodyState = "complete";
});

stream.on("task-rejected", e => {
  const card = cards.get(taskIdToTempId.get(e.taskId)!);
  if (!card) return;
  card.status = "rejected";
  card.bodyState = "complete";
});

stream.on("error", e => {
  if (e.scope === "plan") {
    setActionError("Plan generation failed: " + e.errorText);
    return;
  }
  const tempId = e.taskId ? taskIdToTempId.get(e.taskId) : e.tempId;
  const card = tempId ? cards.get(tempId) : undefined;
  if (!card) return;
  card.bodyState = "error";
  card.bodyError = e.errorText;
});

stream.on("finish", _ => {
  // Reconciliation has already emitted any data-task-rejected; safe to refetch.
  void loadTasks();
});
```

The `taskIdToTempId` map is a thin translation index, not a dual-key store. There is exactly one card per plan item, keyed by `tempId` for its lifetime. The map only exists because Phase 2 / reconciliation events use the persisted `taskId` as the natural identifier on the wire.

### Markdown rendering

Bodies render via `react-markdown` with GFM. The streaming state re-renders on every delta — fine for typical body sizes (~1-3KB), and the 250ms server-side coalescing keeps re-renders bounded.

Code blocks use the same prism theme as the rest of the console (prior art in `ProjectSpecPage.tsx` and `ProjectArchitecturePage.tsx`).

### Empty / fresh state

When `tasks.length === 0` and not generating, show the existing "No tasks yet — Generate Tasks" CTA. Click → opens the SSE stream, transitions into the streaming layout. No modal, no separate screen.

### Incremental state

When `tasks.length > 0` and the user clicks "Generate Tasks" (now labeled "Generate Tasks for Latest Spec/Design" if the prior batch's `SourceDesignVersion` ≠ current `design-vN`), prior cards stay in place at the top in `complete` state. New `data-plan-item` events append cards below a divider labeled "New tasks for design-vN". After `data-finish`, the divider stays — visual provenance.

The rule for showing the regen button: any time `currentDesignVersion > maxBatchSourceDesignVersion`. If they're equal, the button is hidden — there is nothing new to plan.

### Per-task body retry

`bodyState: "error"` cards show a "Retry body" button → `POST /api/v1/projects/.../tasks/{taskId}/regenerate-body`. BFF re-runs Phase 2 for that one task and edits the issue body. Console re-streams via a small SSE subroute.

### Dispatch eligibility

The "Start Implementation" menu is enabled iff **all** of the following hold across every visible card:

1. The card is not currently streaming. `bodyState ∈ {"complete", "error"}` and `issueState ∈ {"issued", "failed"}`.
2. **No card is in a transient state.** During an open SSE stream (i.e. before `data-finish` fires), the button is disabled regardless of per-card state — `status` is server-of-truth and is only refreshed by `loadTasks()` at `data-finish`.
3. After `data-finish` + refetch: button enables iff every card with `issueState === "issued"` has `status === "pending"`. Cards with `issueState === "failed"` are excluded from this check (they have no taskId, so cannot dispatch); they show a per-card retry button instead.
4. `bodyState === "error"` cards block dispatch — those are tasks whose Phase-2 body never completed; dispatching them would push the agent at a placeholder body. User must retry the body first.

This is more conservative than today (today: any `pending` allows dispatch) but matches the reality that streaming or partially-bodied tasks aren't dispatchable.

### Lineage chip

Each card shows a small chip "from design-v3, spec-v2" (the values stored on the task row). Reuses the `LineageLabel` component from `architect-agent.md` §10.

## 12. Schema migration

`ComponentTask` shrinks. Single migration, no transitional aliases.

```sql
-- 1. Backfill body BEFORE dropping agent_instructions, so we don't lose data.
ALTER TABLE component_tasks ADD COLUMN body TEXT;
UPDATE component_tasks SET body = agent_instructions WHERE body IS NULL;

-- 2. Drop snapshot columns. Dispatch path now reads design fresh.
ALTER TABLE component_tasks
  DROP COLUMN component_type,
  DROP COLUMN language,
  DROP COLUMN responsibilities,
  DROP COLUMN architecture_context,
  DROP COLUMN key_considerations,
  DROP COLUMN api_contract,
  DROP COLUMN dependencies,
  DROP COLUMN open_api_spec,
  DROP COLUMN app_path,
  DROP COLUMN buildpack,
  DROP COLUMN entrypoint,
  DROP COLUMN agent_instructions;   -- replaced by body, no alias

-- 3. Add new fields.
ALTER TABLE component_tasks
  ADD COLUMN rationale            TEXT,
  ADD COLUMN task_depends_on      JSONB DEFAULT '[]'::jsonb,
  ADD COLUMN batch_id             UUID,
  ADD COLUMN source_design_version TEXT,
  ADD COLUMN source_spec_version  TEXT,
  ADD COLUMN body_sync_pending    BOOLEAN DEFAULT FALSE;

CREATE INDEX idx_tasks_batch ON component_tasks(batch_id);
```

Existing rows: `body` is backfilled from `agent_instructions`. `batch_id` left NULL on legacy rows — UI groups NULL-batch rows under a "Pre-batch tasks" heading. `source_design_version` / `source_spec_version` left NULL on legacy rows — UI hides the lineage chip. `rationale` left NULL on legacy rows — UI hides the rationale line.

**No dual-column window.** `agent_instructions` is dropped in the same migration. Every reader (dispatch path, issue body builder, console types) is updated to read `body` in the same PR. The migration is a single transaction; on rollback, the column is restored from the dump (we don't run this in prod under load — the project is pre-launch).

### Dispatch path callsites

Today the dispatch path (`remote_worker_service.go`, `bearer_service.go`, issue body builder) reads:
- `task.OpenAPISpec`
- `task.AppPath`, `task.Buildpack`, `task.Entrypoint`
- `task.ComponentType`, `task.Language`
- `task.Dependencies`, `task.Responsibilities`, `task.KeyConsiderations`
- `task.AgentInstructions`

All component-shape fields become `ArtifactStore.ReadDesign(ctx, orgID, projectID).Components[componentName].*` via a centralised `resolveDesignFor(task) *DesignComponent` helper. `AgentInstructions` becomes `task.Body`.

**Design read-time semantics.** Dispatch reads the *current* design at the moment of dispatch — not a snapshot from when the task was generated. This is intentional: between generation and dispatch, the user may have re-architected and re-saved. Dispatch should reflect what's true now. The trade-off: a task generated against `design-v3` may be dispatched against `design-v5`. If the task's `componentName` no longer exists, dispatch fails fast with `cause=design.removed_after_generation` (mirrors the reconciliation cause). Reconciliation (§10.4) closes such tasks proactively on design save, so this should be a rare edge.

The OpenAPI rendering in the issue body changes from "embed YAML" to "reference design.json path", per the user requirement.

### Multi-task-per-component dispatch ordering

Allowing multiple tasks to target the same component (incremental mode) introduces a base-commit question:

> Task B targets `todo-api` and depends on Task A (also `todo-api`). When B's branch is created, what does it base on?

Today every task's branch is `task/<slug>-<short8(taskID)>` — UUID-derived, so collisions are impossible. The remaining question is base-commit:

**Rule.** A task is dispatched only when **every task it `dependsOn` has merged**. The dispatcher checks this on each `POST /tasks/dispatch`:
- Tasks whose `dependsOn` is empty or fully merged → dispatch immediately.
- Tasks with un-merged deps → status `pending_deps`, skipped this dispatch.
- A webhook `pull_request.closed merged=true` for any task in the project triggers a re-evaluation: any `pending_deps` task whose deps have just been satisfied is dispatched.

Branch base = current `main` HEAD at dispatch time. Since deps are merged before dispatch, `main` already contains their commits. No cross-task branch-stacking required — keeps the existing one-PR-per-task model intact.

This is added behaviour, not a redesign of the dispatcher. ~50 LOC in `task_service.go::dispatchTasks` + the merge webhook handler.

## 13. Failure modes

| Mode | Behavior |
|---|---|
| Phase 1 stream error | `data-error{scope:"plan"}`. No issues created. User retries. |
| Phase 1 returns malformed JSON | AI SDK `streamObject` rejects; same as above. |
| Validator fails | `data-error{scope:"plan", issues:[...]}`. No issues created. Console renders inline issues; user retries (or edits design and retries). |
| Phase 2 stream error for one task | Issue stays with placeholder body. `data-task-body-complete` is NOT emitted for that task; `error{scope:"detail", taskId}` is. Other tasks finish normally. `data-finish` still emits. |
| Phase 2 stream error for all tasks | Same as above, but every task is in error state. User can mass-retry. |
| GH issue create fails | BFF marks task `failed` with `cause=github.create_failed`. `data-task-issued` not emitted; card stays in plan-only render with retry button. |
| GH issue edit fails | Body is persisted in DB but the GH issue keeps the placeholder. `data-task-body-complete` still emits with body content. BFF retries edit on a backoff (3 attempts) before giving up. |
| Client disconnects mid-stream | `AbortController` fires. Plan items already persisted stay; in-flight Phase 2 streams cancel. `data-finish` not emitted; on next page load, console fetches state from DB. Tasks with `bodyState !== complete` show retry. |
| Concurrent `POST /tasks/generate` | BFF holds a per-project advisory lock (`pg_advisory_xact_lock(hashtext('techlead:'||projectID))`) for the duration of the run. Second caller gets `409 Generation in progress`. |
| Design save during in-flight tech-lead run | `design_service.SaveAndProceed`'s reconciliation hook (§10.4) takes the same per-project advisory lock before mutating task rows. If a generation is in flight, the save's reconciliation step blocks until generation completes (or its lock holder times out). Generation already reads design at start; if save races to bump `design-vN` mid-run, the in-flight generation's tasks are tagged with the *pre-save* version. The next user-initiated generation immediately sees the new version and produces a covering batch. No corruption; one redundant batch in pathological races. |
| Empty plan in incremental mode (trivial diff) | Allowed. `data-finish{taskCount: 0}`. Console shows toast "No new tasks needed for the latest design." |
| Empty plan in fresh mode | Validator rejects → `data-error{scope:"plan", issues:[{code:"empty-plan-fresh"}]}`. |
| Empty plan in incremental mode but diff has ADDED components | Validator rejects → `data-error{scope:"plan", issues:[{code:"missing-coverage", componentName: "..."}]}`. |
| Phase 1 model emits a sealed element that fails `safeParse` | Aborts the run with `data-error{scope:"plan", code:"malformed-plan-item"}`. No issues created. The seal rule (§4) prevents shipping truncated content; this row covers the rare case where the model produces structurally invalid JSON for an already-sealed element. |
| Design changes (architect run) without a follow-up tech-lead run | Reconciliation runs from `design_service.SaveAndProceed` (§10.4). Pending tasks for removed components are auto-closed even without an open SSE stream. |
| Task with un-merged `dependsOn` | Dispatcher leaves the task in `pending_deps` state and skips it. A `pull_request.closed merged=true` webhook re-evaluates and dispatches any newly-eligible tasks. |
| Dispatch on a task whose component was removed after generation | Dispatch fails fast with `cause=design.removed_after_generation`. Should be rare due to §10.4's eager reconciliation. |
| GH issue body edit fails persistently | Row carries `body_sync_pending=true`; periodic reconciler retries every 5min. DB body is canonical for dispatch. |

## 13a. Cost and latency budget

Numbers below assume Claude Sonnet 4.6 (the current `agents-service` model) and a representative 8-component project with ~5KB OpenAPI per component.

**Phase 1 (single call):**
- Input: spec (~5KB) + slim design (~2KB) + spec/design diff (~1KB) + existing tasks list (~1KB) ≈ 10KB ≈ 2.5K input tokens.
- Output: 8 plan items × ~120 tokens ≈ 1K output tokens.
- Wall time: 3-7s (mostly TTFT + a few hundred output tokens).

**Phase 2 (fan-out, p-limit 4):**
- Per-call input: spec (~5KB) + the one component's full design entry incl. OpenAPI (~6KB) + dep summaries (~1KB) ≈ 12KB ≈ 3K input tokens.
- Per-call output: ~3KB markdown body ≈ 750 output tokens.
- Per-call wall time: 8-15s.
- 8 components at concurrency 4 = 2 sequential batches → 16-30s total.

**End-to-end UX:** plan cards visible at 3-7s; first body completing at ~12-22s; all bodies done at ~20-37s. Reconciliation pass adds <1s.

**Cost (rough):** ~30K input tokens + ~7K output tokens per fresh 8-component run. At Sonnet 4.6 pricing this is single-digit cents per generation. Cost is not a constraint; latency is.

**Concurrency knobs:**
- Phase 2 fan-out: `p-limit(4)` (env `TECH_LEAD_PHASE2_CONCURRENCY`).
- GH issue creation: `p-limit(3)` (env `TECH_LEAD_GH_CREATE_CONCURRENCY`). GitHub publishes a primary rate limit of 5000 req/h per user-token + a secondary "content-creating" limit of ~80 req/min and ~500/h. 3 concurrent creates with a typical 200-500ms latency lands well below those thresholds; on `429` the BFF backs off with the `Retry-After` header per GH guidance.

## 14. Pinning the agents-service ↔ BFF contract

CI fixtures live in `agents/test/fixtures/tech-lead/`:
- Three plan fixtures: fresh-3-component, incremental-add, incremental-empty.
- Two body fixtures: short-body, long-body-with-code-blocks.
- TS test asserts SSE event sequence is byte-stable for each fixture (replay test using a recorded LLM response).
- Go test (`asdlc-service/services/task_service_test.go`) drives the BFF with a stub agents-service emitting the fixture event stream; asserts DB rows + `gh` calls.

## 15. Files affected

```
agents/src/agents/task-generator/      DELETE (entire directory)
agents/src/agents/tech-lead/           REWRITE (replaces phases/risks)
  schema.ts        ~80 LOC — PlanItem, TechLeadInput, plan + detail Zod schemas
  prompt.ts        ~120 LOC — planSystem + buildPlanUser, detailSystem + buildDetailUser
  validator.ts     ~50 LOC — pure validatePlan(items, design, existing) → issues[]
  index.ts         re-exports
agents/src/server/routes/
  task-generator.ts   DELETE
  tech-lead.ts        NEW ~220 LOC — two routes:
                          POST /v1/agents/tech-lead/plan   (Phase 1, streamObject + seal-rule emitter)
                          POST /v1/agents/tech-lead/detail (Phase 2, parallel streamText with p-limit)
asdlc-service/services/
  task_service.go     REWRITE — orchestrator (two-leg agent calls + GH create + reconciliation),
                                 ReconcilePendingForDesignChange (idempotent, no-SSE), dispatch deps gate
  task_diff.go        NEW ~120 LOC — computeDesignDiff, computeSpecDiff, baselineBatch query
  design_service.go   EDIT — call taskSvc.ReconcilePendingForDesignChange in SaveAndProceed
  issue_body.go       EDIT — drop "embed OpenAPI YAML", add "see design.json: components[name=...]"
asdlc-service/clients/agents/
  client.go           EDIT — TaskGenerator → TechLead; two SSE methods (StreamPlan, StreamDetail)
asdlc-service/models/
  component_task.go   SHRINK fields, ADD Rationale/Body/TaskDependsOn/BatchID/SourceDesignVersion/SourceSpecVersion
  migrations/         NEW migration for the schema delta above
asdlc-service/repositories/
  task_repository.go  EDIT — UpdateBody, ListByBatchID
console/src/pages/
  ProjectTasksPage.tsx   REWRITE ~600 LOC — SSE consumer + card renderer + retry UX
console/src/services/api/
  rest.ts             EDIT — streamGenerateTasks (SSE), regenerateTaskBody, listen for new events
  types.ts            EDIT — drop snapshot fields, add Rationale/Body/lineage
agents/test/fixtures/tech-lead/        NEW
tests/api/tech-lead-roundtrip.test.ts  NEW
tests/e2e/tasks-streaming.spec.ts      NEW
```

## 16. Effort summary

| Item | Person-days |
|---|---|
| TaskBatch + Phase 1 (seal-rule emission) + Phase 2 prompts + routes + validator (incl. ADDED-coverage rule) | 3 |
| BFF SSE bridge + parallel GH create with bounded concurrency + Phase 2 trigger gate + async issue edit + retry | 3 |
| Schema migration (single-PR, no alias) + drop snapshot reads in dispatch path + issue body refactor | 2 |
| Multi-task-per-component dispatch ordering (`pending_deps` state + merge-webhook re-evaluation) | 1 |
| Diff computers (spec + design) + baseline-batch SQL | 1.5 |
| Reconciliation: in-stream + on-design-save trigger | 1 |
| Console rewrite (SSE consumer with single-key cards, streaming markdown, issue-failed retry, body retry, lineage chip) | 3 |
| Tests (validator scenarios, fresh, incremental, removed-component, partial Phase 2 failure, GH-create failure, design-save reconciliation, dispatch-deps gating, snapshot-diff fixture) | 3 |
| Doc updates (this file + agents-service.md + api-service.md) | 1 |
| **Phase 1 total** | **~18.5** |

## 17. Implementation sequencing

1. Schema migration + ComponentTask shrink + dispatch-path read switch (no agent changes yet — verify dispatch still works on legacy data).
2. Build Phase 1 (plan) end-to-end: prompt, route, validator, SSE wire. Console renders cards but no bodies. Verify against fresh-mode fixtures.
3. Build Phase 2 (detail) fan-out + SSE delta coalescing + GH edit. Console renders streaming markdown. Verify on the 3 sample sessions.
4. Add diff computer + incremental prompt. Verify on incremental-add and component-removed scenarios.
5. Reconciliation pass + `data-task-rejected`. Verify on component-removed.
6. Per-task body retry endpoint + UI. Verify on injected Phase 2 failure.
7. Dual-emit window: keep the old `POST /tasks/generate` (non-SSE) returning the eventually-consistent batch for one release. Drop next release.
8. Tighten prompts based on observed model behaviour — most likely the "scope each task to the diff, do not re-plan original implementation" rule.
