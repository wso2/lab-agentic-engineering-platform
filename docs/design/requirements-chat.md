# Requirements Chat — Brainstorming Assistant

Status: **Draft (v0.3 — second architect review folded in)**
Owner: anjanasupun05@gmail.com
Last updated: 2026-05-16

**Changes since v0.2** (in response to the second review):
- §4.4 — Replaced the transaction-scoped advisory lock with a
  session-scoped lock on a dedicated `*sql.Conn`. Transaction-scoped
  can't span the SSE stream; v0.2's claim that the projector pattern
  applied was wrong.
- §4.6 — Split the canvas tools into **wireframe_*** and **domain_***
  variants. v0.2 modelled both DSLs under one `canvas` parameter,
  which doesn't survive a look at `domain-model.ts:22-31` (entities /
  attributes / relations, not screens / flows).
- §4.5 — Removed the "Yjs re-initialises from disk" primitive that
  the hook doesn't expose; replaced it with the existing
  `editorRef.current?.setActiveMarkdown(...)` reseed path that
  `ProjectRequirementsPage` already uses for historical views.
- §4.8.2 — Clarified that `requirementsPageEvent` is a new bus
  (not pre-existing) and that pushing canvas siblings into
  `liveContents` is sufficient (no canvas-editor-specific API).

## 1. Problem

The right-side chat panel on every project page is currently a UI mock
(`console/src/components/ChatPanel.tsx`, `console/src/services/chatStore.ts`).
It only ever appends canned text to a single buffer and never talks to a
backend. We want to turn it — starting with the **Requirements** page —
into a real brainstorming assistant.

On the Requirements page the user works with up to six artefact files
laid out under `specs/requirements/`:

| File | Kind | Generator skill | Notes |
| --- | --- | --- | --- |
| `requirements.md` | Markdown | `requirements-from-prompt` | The high-level product sketch. Mandatory, can't be deleted. |
| `functional-requirements.md` | Markdown | `functional-requirements` | EARS-style FRs derived from `requirements.md`. |
| `non-functional-requirements.md` | Markdown | `non-functional-requirements` | NFRs derived from `requirements.md`. |
| `user-stories.md` | Markdown | `user-stories` | Stories derived from `requirements.md` + FRs. |
| `wireframes.excalidraw` + `wireframes.dsl` | Canvas | `wireframes` | DSL is source of truth; `.excalidraw` is the rendered scene. |
| `domain-model.excalidraw` + `domain-model.dsl` | Canvas | `domain-model` | Same DSL+canvas pattern as wireframes. |

These files are *related* — a single product change usually touches
several of them at once. Today the only way to update them is:

1. Hand-edit each file in the in-browser editor, or
2. Click **Generate** on a single file, which regenerates the **whole**
   file from its declared sources (a destructive operation that wipes
   the reader's manual edits).

What the user wants:

> "Add a Payroll & Compensation module."

A single prompt should touch `requirements.md` (add a feature section),
`functional-requirements.md` (add new FRs), and the wireframes (add a
Payroll screen + flow edge) — coherently and surgically, not by nuking
each file and regenerating it.

The user must be able to **see** every change before it lands (diff per
file), be able to undo **a single chat turn** without losing earlier
turns' work, and never lose a manual keystroke to an agent write.

## 2. Goals & non-goals

**Goals**

- Single chat prompt → surgical, multi-file edits across the
  requirements bundle.
- File edits are **string-replacement-style** (`str_replace`) for prose
  markdown, **structural** for canvas DSL (see §4.6). Never whole-file
  regeneration unless the user explicitly asks. Each edit is small
  enough to read as a diff.
- The user reviews changes inline (the existing per-file diff view) and
  approves with **Publish** (existing flow → new `v<N>` tag) or rejects
  with **Discard turn** (new — granular to a single chat turn) /
  **Discard all** (existing — drops all drafts).
- No collab-vs-agent or generate-vs-agent races. While a chat turn
  is in flight, all *other* writers to `specs/requirements/` are
  blocked.
- Chat history is scoped per project and persists across navigation
  inside a browser session (`localStorage`).
- Works for the three primary file kinds: prose markdown (req, FR, NFR,
  stories), canvas DSL (wireframes + domain model), and new-file
  creation.

**Non-goals (v1)**

- Architecture / Implementation / Components chat — design only covers
  the **Requirements** tab. The same shape will generalise but each
  tab needs its own toolset.
- Persisting chat history server-side. v1 keeps it in `localStorage`
  per `(orgId, projectId)`.
- Real-time multi-user chat. Yjs collaborative *editing* of the file
  content remains available between chat turns but is **suspended**
  for the duration of a turn (§4.5).
- Anything beyond the requirements directory. The agent never touches
  `specs/design/`, OC primitives, or GitHub.

## 3. UX design

### 3.1 Layout

The page already has three columns:

```
┌──────────────┬──────────────────────────────┬──────────────┐
│   Explorer   │       MdEditor / Canvas      │   ChatPanel  │
│  (sidebar)   │       (active file)          │  (380 px)    │
└──────────────┴──────────────────────────────┴──────────────┘
```

ChatPanel keeps its current chrome (header, message list, suggestion
chips, composer). The only structural change is what the messages look
like and what's in the composer footer.

### 3.2 Message types

The chat list renders four message kinds:

| Role | Looks like |
| --- | --- |
| `user` | Right-aligned bubble, primary colour. Same as today. |
| `assistant` | Left-aligned bubble, free-form markdown. The agent's "thinking out loud" — short, max 2-3 sentences before tool use. |
| `tool` | Compact tool card — file pill + 1-line summary + collapsible diff preview. Spinner while pending. |
| `error` | Red banner inside the chat. Tool failures, agent crashes, BFF errors. |

**Tool cards** (the visual money shot) look like:

```
┌─────────────────────────────────────────────┐
│ ⚙️  str_replace  requirements.md            │  <-- pill + filename
│ Added "Payroll & Compensation" feature      │  <-- 1-line agent summary
│ ┌─ diff (+6, −0) ────────────────────▼ ────┐│  <-- collapsible
│ │ + ## Payroll & Compensation              ││
│ │ + - View monthly salary breakdown        ││
│ │ + - Download payslips as PDF             ││
│ └──────────────────────────────────────────┘│
└─────────────────────────────────────────────┘
```

`▼` toggles the inline diff. Cards collapse by default after the agent
moves on, to keep the chat scannable. Clicking the filename pill
activates that file in the editor.

A turn's tool cards are visually grouped under the user message that
triggered them, with a small **"Undo this turn"** link on the user
bubble when the turn has at least one successful write. The link is
disabled while the turn is in flight, and removed once any *later* turn
runs or the draft is published / discarded (see §3.5 and §4.7).

### 3.3 Live edit, draft state — and why this design isn't "just architect"

We **considered** stage-then-apply (accumulate a diff set, apply on a
big "Apply" button) and rejected it in favour of **live edit, draft
state**: each successful tool call writes to the working tree
immediately, and the user reviews via the same draft / diff / publish
flow they already use for manual edits.

> **Honest framing — what is and isn't reused.** The architect agent
> (`agents/src/agents/architect/`) emits per-tool SSE events that the
> BFF only forwards to the browser; it writes once, on `data-finish`,
> by reading `frame.Data.Design`. We do something **genuinely
> different**: the BFF treats every `data-tool-result` frame as a
> persistence event and writes per tool. That's not "copying
> architect"; it's a new pattern in this codebase. We adopt it because
> we want the on-disk state to be the canonical chat history (so
> Discard / Publish work as-is and the user can leave mid-turn without
> losing earlier edits). The cost is on us: partial-failure UX,
> rollback granularity, and the BFF must re-validate each edit
> against the live tree (§4.4), not a snapshot.

Specifically:

- Every successful tool call writes the affected file(s) to the
  working tree via the existing `ArtifactStore.WriteRequirementFile`
  path. The on-disk draft is the source of truth.
- The Explorer surfaces a **busy dot** next to every file the current
  turn is touching (reuses the existing `pendingPaths` plumbing — see
  §4.8).
- The active file's editor re-renders from the new server content on
  every tool result.
- The page header gains a **"Review changes"** button — a multi-file
  diff dialog grouping changes by turn (§3.5).
- Per-turn undo and a clean Discard-mid-stream are first-class (§4.7),
  not "open questions".

### 3.4 Composer

Add two things to the existing composer:

1. **Files-in-scope tag.** The composer already shows `project:` +
   `context:` chips. Add `files: 4`. Click → popover lists every file
   in `specs/requirements/`; user can uncheck. Default: all files
   selected. Out-of-scope files are not inlined into the agent's
   prompt and cannot be edited by the turn (the agent's
   `str_replace` tool rejects writes to filenames outside scope).
2. **Mode toggle.** Radio between **Edit** (default — write tools
   exposed) and **Ask** (write tools omitted from the model's tool
   list, read tools only, response is plain text). Stops accidental
   edits when the user wants to ask a question.

### 3.5 Review changes dialog

Two-step UX, ready in v1:

1. Per-file inline diff via the existing `GitCompare` button in the
   page header (already wired against `lastTaggedActive`).
2. A new **"Review changes"** dialog (button in the page header,
   visible when `hasUnsavedChanges`). Shows a stacked diff grouped by
   chat turn (most recent first). For each turn:
   - Turn timestamp + user message excerpt.
   - One diff block per file (`MdDiffViewer` for `.md`; plain-text
     diff for `.dsl`; canvas-thumbnail-with-changed-screens-highlighted
     for `.excalidraw` — initial version can fall back to "see DSL
     diff").
   - **Undo this turn** button (calls the per-turn undo endpoint —
     §4.7).
3. Bottom of dialog: **Publish all** (calls existing
   `POST /requirements/save`) and **Discard all** (existing
   `POST /requirements/discard`).

### 3.6 Scenario walkthrough — "Add a Payroll module"

State going in:

- Project `hr-portal` has `requirements.md` (~60 lines),
  `functional-requirements.md` (FR-1 .. FR-14),
  `wireframes.excalidraw` + `wireframes.dsl` (3 screens — Login,
  Dashboard, TimeOff).
- Last published version: `v2`. No draft changes.

Sequence:

1. ChatPanel POSTs `POST /api/v1/.../requirements/chat` with
   `{ message, history, scopeFiles, mode: "edit" }`. The BFF holds an
   advisory lock on the requirements directory (§4.5).
2. BFF reads every in-scope file from the working tree, packages them
   into the user prompt, and opens an SSE stream upstream to
   `agents-service /v1/agents/requirements-chat`. Returns its own SSE
   stream to the browser.
3. First frame: `data-turn-started` carries the turn ID + a snapshot
   ref the BFF just captured (`refs/asdlc/reqchat/<turn-id>`) — the
   browser stashes the turn ID against the user message so the **Undo
   turn** link knows which ref to roll back to (§4.7).
4. Agent emits a short streamed `text-delta`: *"Found `## Features` in
   `requirements.md`. I'll extend that, add FRs under a new `###
   Payroll`, and add a Payroll screen to the wireframes."*
5. Tool calls (each → SSE `data-tool-started` then
   `data-tool-result`):
   - `str_replace requirements.md` — `old`/`new`/`summary`. BFF
     re-applies against the **live working tree** (not a cached
     snapshot — §4.4), writes, emits result with the new content.
   - `str_replace functional-requirements.md` — same shape.
   - `add_screen wireframes` — structural canvas tool (§4.6): `{
     name: "Payroll", elements: [...] }`. BFF appends the screen to
     `wireframes.dsl`, re-renders `wireframes.excalidraw` via the
     in-process DSL helper, writes both, emits result.
   - `add_edge wireframes` — `{ from: "Dashboard", to: "Payroll" }`.
     Appends an edge to the DSL's `flow` block. BFF re-renders and
     writes.
6. Agent emits `data-tool-call finish { summary: "Modified 3 files." }`.
   BFF runs a turn-validator (§4.4), closes the stream, releases the
   lock.
7. Page shows the Explorer with three busy-dot entries cleared, the
   active file rendered with the inline diff turned on, the
   unsaved-changes pill on, and the **Publish** + **Review changes**
   buttons primed.
8. The user can either: (a) Publish → `v3`; (b) keep chatting (turn
   2 might tweak the new FRs); (c) Undo this turn → working tree
   resets to the snapshot ref → Explorer / editor refresh; (d)
   Discard all → existing flow.

### 3.7 Impact summary

| Surface | Today | After v1 |
| --- | --- | --- |
| ChatPanel | Mock, hardcoded canned replies | Real SSE-driven assistant |
| chatStore | In-memory, ephemeral, 3 message kinds | `localStorage`-backed (versioned blob), per `(orgId, projectId)`, 4 message kinds, tool cards |
| Requirements PUT path | User-initiated only (manual edits + Generate-from-sources) | Also written by the agent via tool calls. All writers (manual / Yjs / Generate / chat) share one directory-scoped lock (§4.5) |
| Yjs collab loop | Always running while the page is open | Suspended for the duration of a chat turn (§4.5) |
| Versioning | One `v<N>` per Publish | Unchanged. Plus a per-turn snapshot ref (`refs/asdlc/reqchat/<id>`) for in-draft undo (§4.7) |
| Discard | Reverts to last tag | Unchanged. Plus a **per-turn undo** (§4.7) |
| New API surface | — | 1 BFF route (chat SSE) + 1 BFF route (per-turn undo) + 1 agents-service route + 1 agent module + 1 git-service endpoint (snapshot/revert refs) |

## 4. Technical design

### 4.1 Component map

```
┌──────────────────────────────┐
│  Console (React, Vite)       │
│  ChatPanel ──┐               │
│              ▼               │
│  POST /api/v1/.../requirements/chat (SSE)
└──────────────│───────────────┘
               ▼
┌──────────────────────────────┐
│  app-factory-api (Go BFF)    │
│  RequirementsChatService     │
│  - acquires dir lock         │
│  - captures snapshot ref     │
│  - opens upstream SSE        │
│  - re-applies + persists     │
│    each tool result          │
│  - re-renders DSL siblings   │
│  - forwards frames to client │
└──────────────│───────────────┘
               ▼ POST /v1/agents/requirements-chat
┌──────────────────────────────┐
│  app-factory-agents-service  │
│  requirements-chat agent     │
│  - in-memory file map        │
│  - tools: read / str_replace │
│    / canvas-structural /     │
│    create / delete / finish  │
│  - emits SSE per tool        │
└──────────────────────────────┘
```

The agent uses the same in-memory-doc + tool-events shape as the
architect agent — but the BFF role is fundamentally different: it
**writes per tool**, not "once on finish". §3.3 covers the framing;
§4.4 covers the write semantics.

### 4.2 Wire format

We extend the existing `text/event-stream` + `x-vercel-ai-ui-message-stream: v1`
wire format that the document-generation route already uses:

```jsonc
// Turn started — carries the turn ID + the snapshot ref the BFF captured
{ "type": "data-turn-started", "turnId": "t_01HF…", "snapshotRef": "refs/asdlc/reqchat/<id>" }

// Free-form assistant text (streamed character-by-character)
{ "type": "text-delta", "delta": "Sounds good. Let me…" }

// Tool call started — UI inserts a "running" tool card
{
  "type": "data-tool-started",
  "id": "tc_01",
  "name": "str_replace",
  "filename": "requirements.md",
  "summary": "Add Payroll feature"
}

// Tool call result — UI flips card to done, attaches diff preview, page
// updates savedFiles[filename] from `content`.
{
  "type": "data-tool-result",
  "id": "tc_01",
  "filename": "requirements.md",
  "content": "…full new file content…",      // cheap path; absent on delete
  "siblings": {                                 // present for DSL writes
    "wireframes.excalidraw": "…rendered JSON…"
  },
  "diff": { "added": 6, "removed": 0, "preview": "+ ## Payroll…\n+ - …" }
}

// Tool call failed — model may retry
{
  "type": "data-tool-error",
  "id": "tc_01",
  "name": "str_replace",
  "filename": "functional-requirements.md",
  "errorCode": "old_string_not_unique",
  "message": "Pattern matched 3 locations; please broaden the context."
}

// Turn finished cleanly
{ "type": "data-finish", "turnId": "t_01HF…", "summary": "Modified 3 files." }

// Fatal error — turn aborted; snapshot ref is still rollback-able
{ "type": "error", "errorText": "..." }
```

`text-delta` uses the existing Vercel UI Message Stream frame so the
browser-side parser stays compatible with the architect / doc-gen
parsers we already have in `console/src/services/api.ts`.

### 4.3 Agent module (`agents/src/agents/requirements-chat/`)

```
agents/src/agents/requirements-chat/
├── doc.ts        // RequirementsDoc: in-memory file map + dirty tracking
├── prompt.ts     // System prompt + user-prompt builder
├── tools.ts      // read_file / str_replace / canvas tools / create / delete / finish
├── schema.ts     // Zod input/output types for the chat route
├── validator.ts  // turn-end validator (analogous to architect/validator.ts)
└── index.ts      // exports
```

`doc.ts` mirrors `architect/doc.ts` — an in-process file map mutated
by tool execution:

```ts
class RequirementsDoc {
  private files: Map<string, string>;             // filename -> content
  private touched: Set<string>;                   // files modified this turn
  constructor(initialFiles: Record<string, string>) { ... }

  read(name: string): string;
  strReplace(name: string, oldStr: string, newStr: string): {
    occurrences: number;   // we require exactly 1
    newContent: string;
    diff: DiffSummary;
  };
  create(name: string, content: string): void;
  delete(name: string): void;                     // refuses requirements.md
  // Wireframes-specific structural ops — see §4.6.1
  wireframeAddScreen(screen: ScreenSpec): { newDsl: string };
  wireframeAddEdge(edge: EdgeSpec): { newDsl: string };
  wireframeRemoveScreen(screenName: string): { newDsl: string };
  // Domain-model-specific structural ops — see §4.6.2
  domainAddEntity(entity: EntitySpec): { newDsl: string };
  domainAddAttribute(entityName: string, attr: AttributeSpec): { newDsl: string };
  domainAddRelation(relation: RelationSpec): { newDsl: string };
  domainRemoveEntity(entityName: string): { newDsl: string };
  asMap(): Record<string, string>;
  touchedFiles(): string[];
}
```

Tools the agent sees (`tools.ts`):

| Tool | Inputs | Behaviour |
| --- | --- | --- |
| `read_file` | `{name}` | Returns content. **For files outside the in-scope set** (or files added by an earlier turn). In-scope files are already inlined in the user prompt — see §4.3.1. No SSE event. |
| `str_replace` | `{name, oldString, newString, summary}` | Prose-markdown files only. Requires exactly one match. Mutates doc + emits `data-tool-result`. Rejects `.dsl` and `.excalidraw` filenames with a hint to use the canvas tools. |
| `wireframe_add_screen` / `wireframe_add_edge` / `wireframe_remove_screen` | see §4.6.1 | Wireframes DSL only (`wireframes.dsl`). |
| `domain_add_entity` / `domain_add_attribute` / `domain_add_relation` / `domain_remove_entity` | see §4.6.2 | Domain-model DSL only (`domain-model.dsl`). |
| `create_file` | `{name, content, summary}` | New file. Errors if filename already exists. Rejects `.dsl` / `.excalidraw` filenames (canvases are created by the user via the explorer, not via chat). |
| `delete_file` | `{name, summary}` | Refuses `requirements.md`. |
| `finish` | `{summary}` | Runs the turn validator (§4.4.1). On failure returns `{error: "validation", issues: [...]}` to the model so it can fix. On success, emits `data-finish`. |

We deliberately omit a `list_files` tool — the user prompt enumerates
the in-scope files. The model only needs `read_file` for the rare case
of an out-of-scope reference (e.g. a sibling project doc the user
mentioned by name).

#### 4.3.1 User-prompt content (the read-file-burn fix)

The architect reviewer flagged that telling the model "always read
every file before writing" would burn 3-6 round trips per turn re-reading
content the BFF already had. We adopt the pattern from
`agents/src/skills/document-generation/wireframes.ts:103-119` and
**inline the full content of every in-scope file** in the user prompt:

```
User message: <message>

Conversation so far: <history>

Files in scope (full content; do NOT call read_file for these):

=== requirements.md (1247 bytes) ===
<content>

=== functional-requirements.md (892 bytes) ===
<content>

=== wireframes.dsl (478 bytes) ===
<content>
```

Total budget: 24 KB combined across in-scope files (well under
Anthropic's caching threshold; large files are tail-truncated with a
note). For files larger than 24 KB the BFF rewrites the inline content
as a 1-line stub + the model is told to `read_file` for the full text.
`read_file` thus exists for the edge cases (huge files, out-of-scope
references, files added mid-turn) — not as the primary path.

The system prompt explicitly says: *"Files listed under 'Files in
scope' are already shown above with full content. Do NOT call
`read_file` for those — you already have them. Use `read_file` only
for files not listed."*

### 4.4 BFF: `RequirementsChatService`

```
asdlc-service/services/requirements_chat_service.go
asdlc-service/controllers/requirements_chat_controller.go
asdlc-service/api/requirements_chat_routes.go    // mounts under existing prefix
```

Routes (mounted under
`/api/v1/organizations/{orgHandle}/projects/{projectName}/requirements`):

- `POST  /chat` — opens the chat SSE stream.
- `POST  /chat/turns/{turnId}/undo` — rolls the working tree back to
  the snapshot ref captured at the start of `turnId`.

**Mutex.** A Postgres **session-scoped** advisory lock on
`hashtext('reqdir:'||orgId||':'||projectId)` gates every mutating
operation on the requirements directory:

- `POST /chat` (full duration of the SSE stream)
- `POST /chat/turns/{id}/undo`
- `PUT  /requirements/files/{name}` (manual edit + Yjs save loop)
- `POST /requirements/files/{name}/generate` (Generate-from-sources)
- `POST /requirements/save` and `POST /requirements/discard`

> **Why session-scoped, not transaction-scoped.** The projector
> (`services/webhook/projector.go:339`) uses `pg_advisory_xact_lock`
> because each `ApplyToTaskByPR` is a short, single-transaction unit
> of work. A chat SSE stream runs 10–60 seconds across many DB
> round-trips and an arbitrary number of model wait windows; you can't
> keep a `BEGIN` open for that long without burning a pooled
> connection, blocking autovacuum, and tripping over PgBouncer in
> transaction mode. We therefore use `pg_advisory_lock` / `pg_advisory_unlock`
> on a **dedicated connection pulled out of the GORM pool for the
> stream's lifetime** (`db.DB().Conn(ctx)`), and release explicitly on
> stream close, abort, or panic. The short-lived writers (PUT, save,
> discard) keep using the transaction-scoped variant inside their own
> `db.Transaction(func(tx)`) — they finish in milliseconds. The chat
> stream and the short writers compete for the *same* lock key, so
> contention is correctly serialised; only the lock scope differs.

This is a bigger refactor than the original draft acknowledged
(`requirements_controller.go` currently has no awareness of mutual
exclusion). The session-scoped acquire is wrapped in a small
`RequirementsDirLock` helper that owns the dedicated `*sql.Conn`,
recovers it on `defer`, and is used by both `RequirementsChatService`
(holds for the full stream) and the short writers (holds for the
duration of a request handler).

**Lock contention UX.** PUTs that lose the race return `409` with a
`retry_after_ms` hint; the editor's auto-save retries silently. The
`/chat` route returns `409 { code: "chat_in_progress" }` if a
different chat is already running for the same project; ChatPanel
surfaces this to the user.

**Turn handling.** On `POST /chat`:

1. Acquire lock. If contended, return 409.
2. Generate a turn ID (`t_` + ULID).
3. Capture a **snapshot ref** by calling a new git-service endpoint
   (`POST /api/v1/.../artifacts/snapshots`) that writes the current
   `specs/requirements/` tree object to
   `refs/asdlc/reqchat/<turn-id>`. This is a no-checkout, no-commit
   `update-ref` of a tree-pointing tag-like ref. Cheap (single
   ref write, content-addressable, GC-eligible after 7 days).
4. Read the in-scope files from the working tree, build the user
   prompt (§4.3.1).
5. Open the SSE stream to agents-service.
6. Send `data-turn-started { turnId, snapshotRef }` downstream.
7. For each upstream frame:
   - `data-tool-started`: forward as-is.
   - `data-tool-result`: **re-apply server-side against the LIVE
     working tree** (not a cached copy):
     - `str_replace`: read file from git-service, run str-replace,
       require occurrence count = 1, write. If the working-tree
       content has drifted from what the agent saw (e.g. a manual
       PUT racing — should be impossible given the lock, but defence
       in depth), the str-replace either still applies (the
       surrounding context still matches) or returns `409`
       upstream so the agent retries with fresh content.
     - canvas-structural: read `.dsl` from git-service, apply the
       structural op (§4.6), write `.dsl`, render `.excalidraw`,
       write `.excalidraw`. Renderer is in agents-service
       (§4.6.2) — the BFF calls
       `POST /v1/internal/dsl/render { kind, dsl } → { excalidraw }`.
     - `create_file` / `delete_file`: direct PUT / DELETE on
       git-service.
     - Forward the result frame downstream (the BFF augments the
       agent's result frame with the authoritative `content` field
       it just wrote).
   - `data-tool-error`: forward as-is; do not write.
   - `data-finish`: run the **turn validator** (§4.4.1). If validation
     fails, emit `data-tool-error` and let the agent retry; if it
     also fails the second time, emit a fatal `error` frame.
8. On any terminal frame, close the stream and release the lock.

**Why re-apply server-side, given the lock?** Two reasons. First,
defence-in-depth — if a future change widens the write surface (e.g. a
webhook-driven write path), the BFF guard still holds. Second,
correctness against a different process / restart — the agents-service
holds a snapshot of the file map taken at request entry; the BFF holds
the same; but the *git-service* file is the only authoritative one and
worth one extra read per tool to verify.

#### 4.4.1 Turn validator

A small, mechanical validator (`agents/src/agents/requirements-chat/validator.ts`,
mirrored by a Go-side check in `requirements_chat_service.go`):

- `requirements.md` exists and is non-empty.
- Every `.dsl` parses successfully via `excalidraw-dsl`.
- File map has no duplicate filenames (case-insensitive).
- No file exceeds 256 KB.

Validation runs on the BFF when `data-finish` arrives, after all writes.
On failure, the BFF emits `data-tool-error` and the agent gets one more
chance to fix (e.g. complete an unterminated `screen` block); a second
failure aborts the turn (the user can Undo it).

### 4.5 Collab loop coexistence

The reviewer correctly flagged that `console/src/hooks/useCollabEditor.ts`
runs a 5-second unconditional save timer that PUTs the editor buffer
back to the BFF, and that this will clobber agent writes if left
running. We address it head-on, but in a way that respects what the
hook actually exposes — the v0.2 wording invented a "re-initialise
the Yjs document" primitive that doesn't exist; v0.3 replaces it with
the existing seed path.

**Server-side (authoritative):** the Postgres session-scoped advisory
lock (§4.4) blocks every PUT path — including the 5-second save loop's
PUT — for the duration of a chat turn. Even if the client forgot to
pause, writes would be rejected with `409`.

**Client-side (etiquette + buffer sync):**

- ChatPanel exposes a single boolean: `chatTurnInFlight` (default
  false). It flips true on `data-turn-started` and false on the
  terminal frame (`data-finish`, `error`, or stream close).
- `useCollabEditor` accepts a new prop `paused: boolean` (passed by
  `ProjectRequirementsPage` from `chatTurnInFlight`). When paused:
  - The 5-second save timer's tick early-returns
    (`if (pausedRef.current) return;` at the top of the existing
    `setInterval` body at `useCollabEditor.ts:117-124`).
  - The Yjs awareness state broadcasts `{ agentLockedBy: userId }`
    via the existing `provider.awareness.setLocalStateField(...)`
    pattern (already used at `useCollabEditor.ts:144` for the
    user identity); remote peers' `CollabAwarenessBar` shows the
    lock and remote editors flip to readOnly.
- **Reseeding when the turn ends — no new hook API needed.** The
  `Y.Doc` is created once inside `start()` (`useCollabEditor.ts:81`)
  and the hook deliberately does *not* expose a "replace buffer"
  primitive. Instead, when `chatTurnInFlight` flips false,
  `ProjectRequirementsPage` calls
  `editorRef.current?.setActiveMarkdown(savedFiles[activePath])`
  (the same call the streaming-bootstrap and historical-version paths
  already use — see `ProjectRequirementsPage.tsx:128, 309, 440`).
  Inside `MdEditor` this routes through the Yjs `Y.Text` delete +
  insert path that the existing code already uses for the diff
  toggle / historical view, so peers see the change as a normal
  CRDT operation.
- On any explicit failure mode (turn aborted by user, fatal
  `error` frame), the same reseed runs from `liveContents`
  (which the BFF has already updated from each `data-tool-result`).

The Postgres advisory lock is the **server-side** correctness
guarantee; the client-side pause + reseed is purely for UX (avoids
a visible flash of stale content while the local Yjs buffer catches
up). Either alone would keep the data safe, but both together avoid a
2–3-second window where the editor shows pre-turn content after the
lock releases.

### 4.6 Canvas (DSL) edits — structural tools, not `str_replace`

The reviewer pointed out that `str_replace` against the wireframes DSL
is brittle: lines like `text "Submit" 20,...` and `rect "Date input"
20,72 280x32` recur across screens, so uniqueness checks will fail
constantly and the model will burn turns broadening context. We agree
and replace `str_replace` for canvas files with two **kind-specific**
toolsets. (v0.2 conflated wireframes and domain-model under a single
`canvas` parameter; the two DSLs have different vocabularies — see
`agents/src/skills/document-generation/wireframes.ts:36-44` vs
`agents/src/skills/document-generation/domain-model.ts:22-31` — so the
tools must split.)

#### 4.6.1 Wireframes tools (target file `wireframes.dsl`)

| Tool | Inputs | Behaviour |
| --- | --- | --- |
| `wireframe_add_screen` | `{name, elements: ScreenElement[]}` | Appends a new `screen <name>` block before the `flow` block (or at EOF if no flow). |
| `wireframe_add_edge` | `{from, to}` | Adds a `from -> to` line inside the `flow` block (creates the block if absent). Idempotent. |
| `wireframe_remove_screen` | `{name}` | Removes the `screen <name>` block and any edges referencing it. |

`ScreenElement` is a discriminated union mirroring the wireframes
grammar at `wireframes.ts:36-44`:

```ts
type ScreenElement =
  | { kind: 'rect';    label: string; x: number; y: number; width?: number; height?: number }
  | { kind: 'button';  label: string; x: number; y: number; width?: number; height?: number }
  | { kind: 'ellipse'; label: string; x: number; y: number; width?: number; height?: number }
  | { kind: 'text';    label: string; x: number; y: number }
```

`x`, `y` are integers in `[0, 360] × [0, 540]` (the DSL screen bounds
at `wireframes.ts:48-49`). `width`/`height` default to grammar-typical
sizes (`button` 280×40, `rect` 280×32). `label` is trimmed; quotes
are escaped during DSL formatting in `doc.ts`.

#### 4.6.2 Domain-model tools (target file `domain-model.dsl`)

The domain-model DSL is entity-relation-flavoured rather than UI-flavoured:

```
entity Order
  attr id: uuid
  attr total: money

relation Customer -[1..*]-> Order "places"
```

Tools:

| Tool | Inputs | Behaviour |
| --- | --- | --- |
| `domain_add_entity` | `{name, attributes: AttributeSpec[]}` | Appends a new `entity <name>` block to the DSL. |
| `domain_add_attribute` | `{entity, name, type}` | Adds an `attr <name>: <type>` line inside an existing entity. Errors if the entity doesn't exist. |
| `domain_add_relation` | `{from, to, cardinality, label}` | Adds a `relation <from> -[<cardinality>]-> <to> "<label>"` line at the end of the DSL. |
| `domain_remove_entity` | `{name}` | Removes the `entity <name>` block and any relations referencing it. |

`AttributeSpec` is `{ name: string, type: string }` where `type` is a
free-form token (`uuid`, `money`, `Date`, `string`, `int`, etc.) —
the DSL grammar at `domain-model.ts:22-31` doesn't constrain types
further.

#### 4.6.3 Why kind-specific, not polymorphic

A single `add_node` tool taking a `canvas` argument was rejected
because the underlying DSLs share no vocabulary — `screen`/`flow` is
meaningless in `domain-model.dsl`, and `entity`/`relation` is
meaningless in `wireframes.dsl`. Splitting them keeps each tool's
schema tight (the model can't accidentally pass `kind: "rect"` into
`domain_add_entity`) and matches how the existing document-generation
skills are organised (one file per canvas: `wireframes.ts`,
`domain-model.ts`).

#### 4.6.4 In-place edits

There is no `edit_screen` / `edit_entity` tool — to modify a node,
the agent calls `remove_*` + `add_*`. That's deliberate: in-place
edits would re-introduce the DSL-uniqueness problem, and the user
prompt already inlines the full DSL so the agent can read existing
nodes before re-emitting them.

#### 4.6.5 Renderer endpoint

The BFF needs to re-render `.excalidraw` after every canvas tool. The
`dslToExcalidraw` converter lives in
`agents/src/skills/document-generation/excalidraw-dsl.ts`. We stand
this up as an internal helper in agents-service:

- `POST /v1/internal/dsl/render` body `{ kind: "wireframes" | "domain-model", dsl: string }`
  → `200 { excalidraw: string }` or `400 { error, line, column }`.
- No auth (cluster-internal only); same pattern as the existing
  `/v1/internal/cache/invalidate` endpoint at
  `agents/src/server/index.ts:60-69`.

In-process latency is ~5-15 ms for typical DSLs; cluster RTT to
agents-service is ~2 ms. Two round trips per canvas edit (DSL render
+ writes) is well under budget. We commit to this in v1 — the
alternative (vendoring the parser into Go) is double the maintenance
and risks divergence.

### 4.7 Per-turn snapshot + undo

Each turn captures the requirements directory tree to
`refs/asdlc/reqchat/<turn-id>` before any write. This is a **new
git-service endpoint** because the existing client surface
(`gitservice.Client.PutRequirementFile` etc.) doesn't expose ref
manipulation:

| Endpoint | Behaviour |
| --- | --- |
| `POST /api/v1/orgs/{org}/projects/{project}/artifacts/requirements/snapshots` body `{ name }` | Writes the working-tree state to `refs/asdlc/reqchat/<name>`. Returns `{ ref, treeSha }`. |
| `POST /api/v1/orgs/{org}/projects/{project}/artifacts/requirements/snapshots/{name}/restore` | Replaces the working-tree state with the ref's tree. No tag changes. |
| `DELETE /api/v1/orgs/{org}/projects/{project}/artifacts/requirements/snapshots/{name}` | Removes the ref. |

GC: a daily git-service job prunes `refs/asdlc/reqchat/*` older than
7 days. They're not part of any branch and don't appear in tag lists.

**Undo behaviour.** `POST /chat/turns/{id}/undo`:

1. Acquire the advisory lock.
2. Restore the snapshot ref into the working tree.
3. Mark the turn message in chat as `status: undone` (visible
   strikethrough on the tool cards).
4. Release the lock.

The browser refreshes `savedFiles` + `liveContents` from a fresh
`GET /requirements`.

**Why a ref-based snapshot, not a commit?** Snapshots are not part of
the project's commit history (we don't want a commit per chat turn
cluttering `git log`). Refs that point to a tree (`update-ref
refs/asdlc/reqchat/<id> <tree-sha>`) are exactly the right primitive —
content-addressable, cheap to create, easy to garbage-collect.

### 4.8 Console wiring

#### 4.8.1 ChatPanel

- `getMockResponse` and the canned reply table are removed.
- `handleSend` POSTs to `/api/v1/.../requirements/chat` (streaming).
  Reuses the existing SSE parser from `console/src/services/api.ts`
  (currently inline in `generateRequirementFile`). Extract it into
  `console/src/services/aiStream.ts` as a small helper shared by the
  chat path and the existing generate paths.
- A reducer maps each frame type to a chatStore mutation + a
  page-event emission:
  - `data-turn-started` → stash `{ turnId, snapshotRef }` on the last
    user message; flip `chatTurnInFlight`.
  - `text-delta` → append to the current assistant message (create
    one if none).
  - `data-tool-started` → insert a tool card in `running` state.
  - `data-tool-result` → flip the card to `done`; emit
    `requirementsPageEvent { kind: "fileWritten", filename, content, siblings }`.
  - `data-tool-error` → flip the card to `error` with message.
  - `data-finish` → close the current turn; clear
    `chatTurnInFlight`.
  - `error` (fatal) → insert an error message; clear
    `chatTurnInFlight`.

#### 4.8.2 ProjectRequirementsPage

- Subscribe to `requirementsPageEvent` (a new in-page event bus the
  chat reducer publishes to — there is no existing equivalent on the
  page today):
  - `fileWritten`: update `savedFiles[name]` and `liveContents[name]`
    from `content`. For each entry in `siblings`, do the same. The
    Explorer routes `.excalidraw` filenames to the excalidraw-editor
    component (see `ProjectRequirementsPage.tsx:947-972`); it reads
    the latest content from `explorerFiles[name]` on the next render,
    so pushing the new JSON into `liveContents` is sufficient — no
    canvas-editor-specific API is required.
- Track `agentBusyPaths: Set<string>` — populated on
  `data-tool-started`, cleared on the turn's terminal frame. **Pass
  this as the union with the existing `pendingPaths` into the
  Explorer** — the Explorer already shows a spinner for paths in
  `pendingPaths` (see `ProjectRequirementsPage.tsx:946-960`), so we
  reuse that.
- Pass `chatTurnInFlight` into `useCollabEditor` as `paused`
  (§4.5).
- Pass `editorProps.readOnly = chatTurnInFlight && agentBusyPaths.has(activePath)`
  so the user can still freely browse other files while the agent
  works.

#### 4.8.3 chatStore.ts

```ts
interface ChatMessage {
  id: string;
  role: 'user' | 'assistant' | 'tool' | 'error';
  content: string;
  timestamp: number;
  turnId?: string;        // present on user / assistant / tool / error
                          // associated with a turn

  // For role === 'user'
  snapshotRef?: string;   // for the Undo-this-turn button
  turnStatus?: 'in_flight' | 'completed' | 'undone' | 'failed';

  // For role === 'tool'
  toolName?: 'str_replace' | 'add_screen' | 'add_edge' | 'remove_screen'
             | 'create_file' | 'delete_file' | 'read_file';
  toolStatus?: 'running' | 'done' | 'error';
  toolFilename?: string;
  toolSummary?: string;
  toolDiffPreview?: string;
  toolDiffStats?: { added: number; removed: number };
  toolErrorText?: string;
}
```

Persistence:

```ts
type StoredChatBlob = {
  schemaVersion: 1;       // bump on breaking changes; old blobs discarded silently
  projectKey: string;     // <orgId>.<projectId>
  messages: ChatMessage[];
  updatedAt: number;
};
```

Key: `asdlc.chat.v1.<orgId>.<projectId>`. Bounded to 200 messages per
project. On schema-version mismatch, the old blob is dropped and a
fresh history starts (acceptable — chat history is transient by
design).

### 4.9 Errors

| Failure | What the user sees | What recovers it |
| --- | --- | --- |
| `str_replace` non-unique | Red banner inside the chat card; agent retries with broader context; after 3 retries, agent emits `finish` with a partial-success summary | Undo the turn or try again |
| `str_replace` not found | Same — agent retries by re-reading the file | Same |
| Canvas DSL parse error after structural op | BFF aborts the write, returns `data-tool-error`; agent re-attempts with corrected structure | Same |
| Agent step limit (`stepCountIs(64)`) | Stream emits a fatal `error` frame; turn marked `failed` | Undo the turn |
| BFF lock contention | Chat send blocked with `409` toast: "Another writer is editing the requirements (manual save / generate)." | Wait |
| Client disconnect mid-turn | Server `r.Context().Done()` fires, abort propagates upstream, lock releases. Turn is marked `failed`; partial writes remain on disk | Undo the turn |
| Discard pressed mid-turn | New behaviour: page sends `POST /chat/abort` first (cancels the in-flight stream), waits for the SSE to close, then calls existing `POST /requirements/discard`. The order matters so the abort releases the lock before discard tries to acquire it | n/a |
| Anthropic key missing / org disconnected | `AnthropicKeyError` → fatal `error` frame with `code: "anthropic_missing"`; ChatPanel renders a friendly Settings link | Connect a key |

### 4.10 Auth, observability, tests

- **Auth**: unchanged — browser→BFF uses PKCE cookies; BFF→agents-service
  uses `client_credentials` JWT; agents-service→Anthropic uses the
  per-org key resolver. The new internal endpoint
  `POST /v1/internal/dsl/render` follows the existing
  `/v1/internal/cache/invalidate` convention (cluster-internal,
  no auth header required; future hardening can add a service JWT).
- **Logs**: BFF logs at INFO `chat turn start`, every tool result
  (filename + diff stats), `data-finish` summary, lock acquire /
  release, and snapshot ref creation. Mirrors the existing
  `RequirementsService.StreamGenerate` log shape
  (`services/requirements_service.go:210-212, 308-310`).
- **Metrics (deferred)**: mean tool calls per turn, mean turn duration.
- **Tests**:
  - Unit (agents-service): `RequirementsDoc` — `str_replace` requires
    unique match; refuses non-existent file; canvas-structural ops
    produce parseable DSL; `delete` rejects `requirements.md`.
  - Unit (BFF): `RequirementsChatService.applyToolResult` — happy path
    on a markdown str-replace; canvas structural op writes both `.dsl`
    and `.excalidraw`; lock contention returns 409; snapshot
    capture/restore round-trip.
  - Integration (Playwright): full Payroll scenario from §3.6 against
    the cluster brought up by `deployments-v2/scripts/setup.sh`. Plus
    a regression: open the page in tab A and tab B, start a chat in
    A, verify B's editor goes read-only and recovers after the turn.

## 5. Open questions (still open after v0.2)

1. **History trimming.** v1 sends the full chat history on every turn.
   Anthropic context limits don't bite for the first ~20 turns; beyond
   that we need a summarisation pass. Punt to v1.1.
2. **chat-level retry.** If a whole turn fails (e.g. step-count
   limit), should there be a "Retry turn" button distinct from "Undo
   turn + send the same message again"? Probably yes in v1.1, but
   the latter works in v1.
3. **Cross-tab chat state.** Two tabs of the same project: which one
   owns the chat? v1 picks "whichever sent the request first" (the
   advisory lock decides); the loser tab sees the `chat_in_progress`
   toast. v1.1 could mirror chat state across tabs via `BroadcastChannel`.
4. **`Review changes` per-turn diff for `.excalidraw`.** Diffing two
   Excalidraw JSON blobs is messy; we fall back to "see DSL diff" in
   v1. A nicer canvas-aware diff is a follow-up.

## 6. Rollout

1. Land the agents-service `requirements-chat` agent behind a feature
   flag (env var `REQUIREMENTS_CHAT_ENABLED=true`).
2. Land the new git-service snapshot endpoints behind the same flag.
3. Land BFF routes + service (chat, chat/abort, undo).
4. Land the directory-scoped advisory lock — this is the riskiest
   refactor because it changes every write path. Land it first, behind
   a separate flag (`REQUIREMENTS_DIR_LOCK_ENABLED=true`), so we can
   roll it back independently if it surfaces issues with the existing
   manual edit + Generate flows.
5. Land console changes; the chat flag is exposed to the SPA via
   `GET /api/v1/config` (existing endpoint).
6. Dogfood on the team's HR-portal demo project. Iterate on prompts,
   tool error messages, and the canvas structural-tool ergonomics.
7. Remove both flags once stable.

## 7. References

- `agents/src/agents/architect/tools.ts` — in-memory doc + tool-event
  topology we adopt (but with persist-per-tool semantics, not
  persist-on-finish — see §3.3).
- `agents/src/server/routes/architect.ts` — SSE pattern (keep-alive,
  flush headers, abort coordination).
- `agents/src/skills/document-generation/wireframes.ts` — DSL grammar,
  prompt-inlining pattern, post-process step.
- `agents/src/skills/document-generation/excalidraw-dsl.ts` — DSL
  parser, used by the new `/v1/internal/dsl/render` endpoint.
- `asdlc-service/services/requirements_service.go` — existing generate
  stream + has-unsaved-changes derivation.
- `asdlc-service/services/webhook/projector.go` — Postgres advisory-lock
  idiom we extend.
- `console/src/components/ChatPanel.tsx` — current mock.
- `console/src/hooks/useCollabEditor.ts` — Yjs save loop that must
  pause during chat turns (§4.5).
- `console/src/pages/ProjectRequirementsPage.tsx` — page being
  augmented; reuses `pendingPaths` (line 96) for the agent's busy
  indicators.
- `docs/design/agent-orchestrator.md` — agents-service architecture.
