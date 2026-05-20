# Architect Agent — Component Design

The architect agent generates and evolves the project's `design.json` from the approved specification. This doc covers the agent's tool surface, doc model, validator, SSE protocol, versioning interaction, and rollout. The implementation lives in `agents/src/agents/architect/` and `agents/src/server/routes/architect.ts`; persistence lives in `asdlc-service/services/design_service.go`.

Related:
- `agents-service.md` — the SSE wire protocol and the BFF integration shape that all agents share.
- `api-service.md` — BFF; owns artifact storage and tag-based versioning.
- `repo-storage-ownership.md` — current layering for git/storage (the BFF normalizes; `git-service` is byte-level).

## 1. Goals

The previous architect (a single `streamObject` over a fat Zod schema with `openAPISpec` per component) had two problems we are fixing:

1. **Slow UI feedback during streaming.** Component cards only became visible after their full OpenAPI YAML had streamed, which dominates output tokens. UI stalled for tens of seconds.
2. **Brittle incremental edits.** Regen passed the previous design (with all YAMLs) into the prompt and asked the LLM to "preserve components verbatim." The model frequently reworded preserved components, producing spurious version-tag churn and token waste.

The redesign decouples component shape from OpenAPI content, makes incremental edits first-class via tool calls, and protects versioning from harmless LLM drift via canonical-form storage.

## 2. Scope

**Phase 1 (this doc, ship).**
- Tool-calling architect agent with structured shape mutations.
- Full-spec OpenAPI emission (`set_openapi(name, contents)` only) — no operation-level surgery.
- Server-side `DesignDoc` per request; OpenAPI stored as opaque YAML strings.
- `finalize` tool gates `data-finish` with a real validator.
- BFF normalizes design.json before write so byte-equality at `git-service` is robust against LLM whitespace/order drift.
- Coordinated SSE rename with one-release dual-emit compatibility window.

**Phase 2 (deferred, design pre-baked).** Operation-level surgical OpenAPI tools (`set_openapi_operation`, `set_openapi_schema`, etc.) once telemetry shows incremental small-edit traffic justifies the implementation cost. Constraints already known from review (yaml.Document AST, no-op detection, `jsonToYamlNode`, recursive `$ref` visitor, dealias-on-parse) — captured in §13.

## 3. Architecture

### Data flow

```
console ──POST /design/generate──▶ asdlc-service (BFF)
                                         │
                                         ▼
                                  agents-service /v1/agents/architect
                                         │   (DesignDoc per request,
                                         │    streamText with tools,
                                         │    SSE events as tools execute)
                                         ▼
                                   data-finish { design }
                                         │
asdlc-service normalizeDesignJSON ◀──────┘
         │
         ▼
  git-service Save (byte-level dedup) → tag bump if normalized bytes changed
```

### DesignDoc (per-request, in-memory, agents-service)

```ts
type ComponentEntry = {
  slim: SlimComponent;          // shape metadata
  openapi: string | null;       // raw YAML, opaque. null = pending
};

class DesignDoc {
  overview: string;
  requirements: string[];
  components: Map<string, ComponentEntry>;

  static fromPrevious(prev?): DesignDoc

  // Shape mutators
  setOverview(text)
  setRequirements(items)
  upsertSlim(slim)                 // openapi → null
  removeComponent(name)
  setAgentInstructions(name, text) // does NOT invalidate openapi (see §7)
  setLanguage(name, lang)          // invalidates openapi → null
  addDependency(name, dep)         // invalidates openapi → null
  removeDependency(name, dep)      // invalidates openapi → null

  // OpenAPI
  setOpenApi(name, yaml): { changed: boolean; reason?: string }
  // semantic-equality check: parses both old and new (per §10) and compares
  // structurally. Equal → returns {changed:false, reason:"semantic_equal_to_current"}
  // and stores nothing. Different → stores new yaml, returns {changed:true}.

  // Reads
  getComponent(name): { slim, openapi: string | null }

  // Validation & output
  validate(): ValidationIssue[]
  pendingSpecs(): string[]
  materialize(): ArchitectOutput
}
```

OpenAPI is **opaque** in Phase 1. We never parse-then-reserialize it inside the doc; Phase 2's surgical layer would. The semantic-equality check uses parse + structural compare for *idempotency detection only*, never re-emits parsed form back to disk.

## 4. Tool catalog

### Shape (8)

| Tool | Args | Notes |
|---|---|---|
| `set_overview` | `{ text }` | replaces |
| `set_requirements` | `{ items[] }` | replaces |
| `add_component` | `{ ...slim }` | fails if name exists |
| `set_agent_instructions` | `{ name, text }` | replaces full string; does NOT invalidate openapi |
| `set_language` | `{ name, language }` | invalidates openapi |
| `add_dependency` | `{ name, dependsOn }` | union-add; invalidates openapi |
| `remove_dependency` | `{ name, dependsOn }` | invalidates openapi |
| `remove_component` | `{ name }` | clears its pendingSpecs entry |

Renames are not supported. Rename = remove + add.

### OpenAPI (1)

| Tool | Args | Notes |
|---|---|---|
| `set_openapi` | `{ name, contents }` | full OpenAPI 3.0.3 YAML. Idempotency-checked via §10 normalization; no-op returns `{changed:false, reason:"semantic_equal_to_current"}` and emits no SSE event. |

No `force` parameter. Wholesale replace = `remove_component` + `add_component` + `set_openapi` (intent visible on the wire as a removed+added pair).

### Reads (1)

| Tool | Args | Notes |
|---|---|---|
| `get_component` | `{ name }` | returns `{ slim, openapi: string \| null }` — model can refresh memory of preloaded or just-set state |

### Termination (1)

| Tool | Args | Notes |
|---|---|---|
| `finalize` | `{}` | runs validator. On issues, returns `{error:"validation", issues:[...]}` for the model to recover from. On success, emits `data-finish` and ends the loop. Only sanctioned terminator. |

`stepCountIs(64)` is a runaway-loop guard; `finalize` is the real terminator. Natural "no tool calls" termination is treated as a malformed run — `data-error` emitted, no persistence.

## 5. Validator (`finalize`)

The validator runs server-side in agents-service. Returns a list of structured issues; `data-finish` only emits when the list is empty.

**Per-component**
- `pendingSpecs()` is empty (every component has openapi).
- `componentType` ↔ `entrypoint` consistent (`service`→`deployment/service`, `web-app`→`deployment/web-application`, `scheduled-task`→`cronjob/scheduled-task`).
- `service` and `scheduled-task` components have at least one path operation.
- `service` components expose `GET /health`.
- No two components share `appPath`.
- No two components share `name` (defense in depth — `add_component` already rejects duplicates).

**Per-OpenAPI** (parse-only, not a full linter)
- YAML parses.
- Top-level `openapi` field present.
- Each path's method is one of `get|post|put|delete|patch|head|options|trace`.
- Each operation's `responses` keys are 3-digit HTTP codes or `default`.
- `operationId` (where present) is unique within the spec.
- Each schema in `components.schemas` has `type`, `$ref`, one of `allOf/oneOf/anyOf`, **or** a non-empty `properties` (treated as implicit `type: object` to match OpenAPI tooling reality).
- Every `$ref: "#/components/<kind>/<name>"` resolves within the same spec, for any `kind` in `schemas|parameters|requestBodies|responses|headers|securitySchemes|examples|callbacks|links`. Cross-component refs are unresolved (OpenAPI doesn't support them).

**Cross-component**
- `dependsOn` names exist in the doc.
- No cycles in `dependsOn` (topological sort succeeds).

Issue shape:
```ts
{ component?: string, code: string, ...detail }
// e.g. { component: "todo-api", code: "missing-health" }
//      { component: "todo-web", code: "dangling-dep", dep: "removed-svc" }
//      { component: "todo-api", code: "unresolved-ref", ref: "#/components/schemas/Item" }
```

## 6. SSE protocol

UI strategy: card-level live updates during shape edits; "spec updating" spinner during `set_openapi`; full re-render of OpenAPI from `materialize()` output at `data-finish`.

| Event | Payload | When |
|---|---|---|
| `data-overview` | `{ text }` | `set_overview` |
| `data-requirements` | `{ items }` | `set_requirements` |
| `data-component-added` | `{ component: SlimComponent }` | `add_component` |
| `data-component-updated` | `{ name, patch, openapiInvalidated }` | any of `set_*`/`add_dependency`/`remove_dependency` |
| `data-component-removed` | `{ name }` | `remove_component` |
| `data-component-spec-updating` | `{ name }` | `set_openapi` with `changed: true`. Coalesced per step boundary (multiple `set_openapi` calls for the same name in one parallel-tool-call step emit one event). |
| `data-finish` | `{ design: ArchitectOutput }` | only from `finalize` after validator passes |
| `error` | `{ errorText }` | unrecoverable |

Events are name-keyed and idempotent under reorder — no mutex on `execute()`. Tools check `res.writableEnded` and return `{error:"client-disconnected"}` when the socket is closed so the model stops emitting.

### Rollout — dual-emit for one release

The current route emits `data-component` (singular). To survive non-atomic deploys (k8s rolling update where console pods may be ahead of agents-service or vice versa), agents-service emits both old and new event names for one release:

```ts
// e.g. inside add_component's execute()
sse.send("component", { component: slim, type: "added" });   // OLD — one release only
sse.send("component-added", { component: slim });              // NEW
```

Console listens for both during the transition. After one release: drop the old emission. ~5 LOC each side, eliminates rolling-deploy hang risk.

## 7. System prompt (key clauses)

```
You are a software architect. You operate by calling tools that mutate a
design document. The current state is shown to you under "Current design".
Your job: make the document match the specification.

# Workflow

1. Emit shape mutations first (set_overview, set_requirements, add_component,
   add_dependency, set_agent_instructions, etc). Use parallel tool calls in
   one step where mutations don't conflict.

2. For each component whose OpenAPI is missing (hasOpenApi: false), call
   set_openapi(name, contents). If a component's spec is unchanged in your
   intended design, do NOT re-emit set_openapi for it — it is preserved
   verbatim from the previous design.

3. If set_openapi returns {changed: false, reason: "semantic_equal_to_current"},
   do not retry it for the same component.

4. Call finalize() to end the session. If finalize returns validation issues,
   address them and call finalize again.

# Rules for components
  - Names: lowercase kebab-case.
  - Each component is a Docker microservice on Kubernetes.
  - entrypoint must match componentType.
  - buildpack is always "docker".
  - Backend services prefer Go + net/http on port 9090.
  - Every service exposes /health.
  - dependsOn names must reference other components verbatim.

# Rules for OpenAPI
  - OpenAPI 3.0.3.
  - Include /health in every service.
  - Cross-component contracts must agree: when component A depends on B, A's
     callsite (path, method, request schema) must match B's spec.
  - If you change componentAgentInstructions in a way that affects the wire
     contract (new endpoint, changed schema), call set_openapi for that
     component as well. Otherwise instruction-only edits do not require a
     spec re-emit.

# Incremental rules (Current design is non-empty)
  - The doc is preloaded with the previous design including OpenAPI specs.
  - Components you don't touch are kept verbatim. Do not re-emit their specs.
  - Prefer adding a new component over expanding an existing one.
  - Renames are not supported. A rename is remove + add.
  - To wholesale-rewrite a component, call remove_component + add_component
     + set_openapi. The destructive intent is then visible.
```

## 8. User prompt

The skeleton view (no YAML bodies, only `hasOpenApi: true|false` per component) is what the model sees as "Current design." For a 5-component design with rich specs this lands at 1–2K tokens of context vs ~30K for the previous-design-with-YAMLs format.

```
Project: <projectName>

## Specification
<spec>

## Current design
<empty>  ← when fresh

— or for incremental —

```json
{
  "overview": "...",
  "requirements": [...],
  "components": [
    {
      "name": "todo-api", "componentType": "service", "language": "Go",
      "dependsOn": [], "entrypoint": "deployment/service", "buildpack": "docker",
      "appPath": "/todo-api", "componentAgentInstructions": "...",
      "hasOpenApi": true
    },
    ...
  ]
}
```

The doc above is preloaded. Mutate it via tool calls until it matches the
specification. Components you do not touch are preserved verbatim including
their OpenAPI spec.
```

## 9. Sample sessions

### A. Fresh — todo app

**Step 1** (parallel):
```jsonc
[
  { "tool": "set_overview", "args": {"text": "..."} },
  { "tool": "set_requirements", "args": {"items": [...]} },
  { "tool": "add_component", "args": {"name": "todo-web", "componentType": "web-app", ...} },
  { "tool": "add_component", "args": {"name": "todo-api", "componentType": "service", ...} }
]
```
SSE: `overview`, `requirements`, `component-added × 2`. UI cards visible.

**Step 2** (parallel):
```jsonc
[
  { "tool": "set_openapi", "args": {"name": "todo-web", "contents": "openapi: 3.0.3\n..."} },
  { "tool": "set_openapi", "args": {"name": "todo-api", "contents": "openapi: 3.0.3\n..."} }
]
```
SSE: `component-spec-updating × 2`.

**Step 3:** `finalize` → validator passes → `data-finish`.

### B. Incremental — add notification feature

Doc preloaded with `todo-web` + `todo-api` (both `hasOpenApi: true`).

**Step 1** (parallel):
```jsonc
[
  { "tool": "set_requirements", "args": {"items": [...prev, "Notify users when a todo becomes due"]} },
  { "tool": "add_component", "args": {"name": "notification-svc", "componentType": "service", ...} },
  { "tool": "add_dependency", "args": {"name": "todo-api", "dependsOn": "notification-svc"} },
  { "tool": "set_agent_instructions", "args": {"name": "todo-api", "text": "[updated to include notification-svc callout]"} }
]
```
Tool results: `add_dependency` returns `{openapiInvalidated: true}` (contract change). The model now owes specs for `notification-svc` (new) and `todo-api` (invalidated).

**Step 2** (parallel):
```jsonc
[
  { "tool": "set_openapi", "args": {"name": "notification-svc", "contents": "..."} },
  { "tool": "set_openapi", "args": {"name": "todo-api", "contents": "..."} }
]
```
`todo-web` is untouched → preserved verbatim including its spec.

**Step 3:** `finalize`.

### C. Wholesale rewrite of one component

```jsonc
[
  { "tool": "remove_component", "args": {"name": "todo-api"} },
  { "tool": "add_component", "args": {"name": "todo-api", ...newSlim} },
  { "tool": "set_openapi", "args": {"name": "todo-api", "contents": "..."} }
]
```
Then `finalize`. SSE shows `component-removed` then `component-added` — destructive intent visible to the user.

## 10. Versioning + canonical normalization

The previous architect produced LLM-authored YAML byte-stream every regen. Even semantically-identical regens differed in whitespace, key order, scalar style — every save bumped a `design-vN` tag. v5 fixes this via canonical-form storage.

### Where normalization runs

In **the BFF**, on the write path, before calling `git-service.Save`:

```
asdlc-service/services/design_service.go
  StreamGenerateDesign / SaveAndProceed
    └─▶ normalizeDesignJSON(rawJson) []byte
        ├─ unmarshal models.Design
        ├─ for each component: normalizeOpenApiYaml(c.OpenAPISpec)
        └─ marshal models.Design (struct-tag-deterministic field order)
```

`git-service` continues to use byte equality (`strings.TrimSpace(taggedContent) == strings.TrimSpace(req.Content)` at `git-service/services/artifact_service.go:464`) — no YAML dependency added there. The format-agnostic boundary defined by the `repo-storage-ownership` refactor stays intact.

### Normalization rules

| Aspect | Rule |
|---|---|
| Key order | Alphabetical at every nesting level (recursive). Loses author-chosen ordering; gains determinism. |
| HTTP status code keys | Coerced to string (`200` → `"200"`). |
| `x-*` extensions | Preserved verbatim — they carry intent. |
| Empty arrays | `required: []`, `parameters: []`, `tags: []` → omit. |
| `additionalProperties: true` | Implicit allowed; left as-is. |
| `$ref` vs inlined definitions | Different intent; treated as different. |
| OpenAPI 3.0 `nullable` vs 3.1 type-array | Different versions; treated as different. |
| Block scalars vs flow strings | Equal — emit block where multi-line. |
| YAML anchors / aliases | Dealiased on parse; semantic content compared. |

Implementation: ~80 LOC in `asdlc-service/services/openapi_normalize.go`. Idempotent: re-normalizing canonical content is a no-op.

### Migration

Designs tagged before v5 will hit the new normalizer on first save. Bytes change → `git-service` correctly bumps to `design-v(N+1)`. **One-time migration cost.** Documented in the rollout PR. Two test fixtures: pre-v5 design that's already canonical (still bumps once due to first re-write); pre-v5 design with normalization differences (bumps for genuine reason).

### Audit gap (known limitation)

When a regen against a new spec produces zero validator-passing edits, no new tag is created. The existing `design-vM` tag's `source-spec` lineage continues to point to the spec it was originally generated against — there is no record that the user re-evaluated against `spec-v(M+k)`. For audit, consult agents-service request logs. Out of scope for Phase 1; if audit becomes a real ask, options include a `specs/regen-log.json` committed on every regen attempt or annotated `design-regen-vM-against-spec-vN` tags. Document in the doc; defer.

## 11. Failure modes

| Mode | Behavior |
|---|---|
| `finalize` with pending specs | Tool returns `{error:"validation", issues:[{component,code:"missing-spec"}]}`. Model recovers in next step. |
| YAML parse error in `set_openapi` | Tool returns `{error:"parse", line, column, message}`. Model recovers. |
| `set_openapi` semantically equal to current | Tool returns `{changed:false, reason:"semantic_equal_to_current"}`. No SSE event. Prompt rule prevents retry. |
| Validation issues at `finalize` | Returned as structured list. Model addresses and re-calls `finalize`. |
| `add_component` for existing name | Tool returns `{error:"name-exists", message:"To modify, use the surgical setters; to replace, call remove_component first."}`. |
| Client disconnects mid-stream | `AbortController` fires; tools see `res.writableEnded` and return `client-disconnected` errors; AI SDK stops cleanly. No partial persistence (BFF only writes on `data-finish`). |
| Step cap exceeded | `streamText` returns with `finishReason:"stop"` and no `finalize` call. agents-service emits `data-error`; BFF doesn't write. User retries. |
| Stream errors before `data-finish` | BFF doesn't write → on-disk design unchanged. Console refetches `/design` on stream end (success or fail) to reconcile UI optimistic state with disk. |

## 12. Pinning the agents-service ↔ BFF contract

agents-service `materialize()` produces canonical `ArchitectOutput`. BFF transforms only via JSON marshal — deterministic via struct-tag-declared field order in `asdlc-service/models/design.go`.

CI fixtures live in `agents/test/fixtures/architect-output/`:
- Three canonical JSON files (small, medium with `x-*`, large with anchors-pre-dealias).
- TS test (`agents/src/agents/architect/__tests__/materialize.test.ts`) — DesignDoc constructed from each fixture, `materialize()` byte-equal to fixture.
- Go test (`asdlc-service/services/design_service_test.go`) — fixture unmarshal → marshal → byte-equal.
- Cross-side test (`tests/api/architect-roundtrip.test.ts`) — POST synthetic agents-service output to BFF, read back, byte-equal to fixture.

The Go side is the byte-order authority (struct tag order is deterministic; `map[string]…` would not be). TS materialize emits the same field order via Zod schema definition order.

## 13. Phase 2 — deferred design

Build the surgical OpenAPI layer if telemetry shows >30% of architect runs are incremental small-edits. Constraints (from prior reviews) baked in so the design is not from scratch:

1. Storage switches to `yaml.Document` AST per component (npm `yaml` lib).
2. Surgical tools: `set_openapi_info`, `set_openapi_operation`, `remove_openapi_operation`, `set_openapi_schema`, `remove_openapi_schema`. All ref-aware (forward + reverse walk).
3. **No-op detection in every surgical tool** — structural compare new vs current node before `setIn`; short-circuit equal writes (otherwise round-trip can flip bytes even on no-op).
4. **`jsonToYamlNode` helper** — promotes multi-line strings to block literals; picks block style for objects/arrays. Without this, JSON-shaped tool args produce flow-style siblings to block-style original content, ugly diffs.
5. **Recursive `$ref` visitor** — walks every node; treats any object with `$ref: "#/..."` key as a ref. Avoids enumerating fields.
6. **Anchors / aliases**: dealias on parse for predictability. Lose dedup; accept.
7. **Sort `paths` map** on path insertion to keep diffs clean.
8. **Cross-spec refs**: treat as `unresolved-refs` with hint message (OpenAPI doesn't support them).
9. **Read tools `get_*` return YAML excerpts**, not parsed JSON, so the model authors block scalars correctly when writing back.
10. **ContractIndex cache key** = `(componentName, revision-counter)`; bump on every write.
11. **Force-replace path** (remove + add + set_openapi) discouraged via prompt + tool-result warning on `remove_component` for components with > N operations.

Effort estimate (Phase 2 alone): ~10 person-days for an engineer familiar with the `yaml` lib; ~15 if learning it.

## 14. Files affected

```
agents/src/agents/architect/
  doc.ts                    NEW    ~150 LOC — DesignDoc with opaque-string openapi
  tools.ts                  NEW    ~250 LOC — buildTools(doc, sse) factory; dual-emit
  prompt.ts                 REWRITE — system prompt + buildUserPrompt(input, doc)
  schema.ts                 EDIT   — split SlimComponent / DesignComponent
  index.ts                  EDIT   — re-exports
  __tests__/                NEW

agents/src/server/routes/
  architect.ts              COLLAPSE  ~150 → ~50 LOC

asdlc-service/services/
  openapi_normalize.go      NEW    ~80 LOC
  design_service.go         EDIT   — call normalizeDesignJSON in write paths
  design_service_test.go    EDIT   — fixture round-trip

console/src/services/api/
  rest.ts                   EDIT   — listen for both old + new event names (one release)
  types.ts                  EDIT   — openAPISpec optional

console/src/pages/
  ProjectArchitecturePage.tsx  EDIT — spec-updating spinner; validation-error inline UI

agents/test/fixtures/architect-output/    NEW
tests/api/architect-roundtrip.test.ts     NEW
```

## 15. Effort summary

| Item | Person-days |
|---|---|
| DesignDoc + tools + finalize validator | 3 |
| Normalization + migration tests | 1.5 |
| Coordinated SSE rename + dual-emit + console refactor (incl validation-error UX) | 3 |
| Tests (validator scenarios, dedup, sample sessions, parser-error, cycle detection, x-* preservation) | 2.5 |
| Doc updates | 1 |
| **Phase 1 total** | **~11** |
| Phase 2 (deferred) | ~10–15 |

## 16. Implementation sequencing

1. Build `DesignDoc` + serialization round-trip; unit tests on fixtures.
2. Build tools + validator + prompt; swap the route handler. Verify against the existing console flow with new event names.
3. Add normalization + CI fixtures. Migrate test designs.
4. Console: rename event handlers, optional `openAPISpec`, spinner state, validation-error inline UI.
5. End-to-end verify on all three sample sessions with a real Anthropic key.
6. Tighten the system prompt based on observed model behavior (the "don't re-emit unchanged specs" rule and the spec-vs-instructions invalidation rule are the most likely to need tuning).
7. Ship dual-emit release; one cycle later, drop old event names.
8. Decide on Phase 2 based on incremental-edit traffic post-launch.
