# Task Execution Progress

Live, multi-tenant view of what is happening to a `ComponentTask` from
dispatch through deploy. Surfaces the coding-agent's intermediate work,
step-level WorkflowRun status, post-merge build progression, and
deployment outcome.

Reviewed by `oc-design-expert`, `wso2cloud-expert`, and an architect
review. Rev 2 incorporates: Service-DNS Observer URL (cloud), Flux-owned
obs-plane ordering (cloud), env overlay via `SecretReference + secretKeyRef`
(cloud), versioned NDJSON schema (architect), `/progress/agent` +
`/progress/build` split with documented cursor (architect), `phase`
derived per-response (architect), `Phase ∈ {Failed, Error}` (OC), watcher
driven off `ConditionWorkflowCompleted` (OC), per-org rate ceiling +
entropy-regex scrubber backstop (architect).

---

## 1. Goals & non-goals

**Goals**
- Replace today's "spinner + status pill" with a trustworthy live feed.
- Reuse platform-provided observability (OpenChoreo Observer →
  OpenSearch → fluent-bit). No bespoke log shipping.
- Scale cleanly to many orgs × many projects × many concurrent tasks.
- Zero new persistent state on the BFF for *intermediate* events.

**Non-goals**
- SSE / websockets for execution progress. The codebase has one SSE
  endpoint today — `services/task_stream.go` for tech-lead
  orchestration — and that pattern is appropriate there: a single
  short, user-initiated request with a definite end. Task execution is
  long-running (minutes to tens of minutes), multi-viewer, survives
  tab/browser sleep, and the Observer is the natural source. Polling
  with a cursor handles all that gracefully without a re-streaming
  layer. We accept the asymmetry deliberately.
- Cross-plane Kubernetes API access. Only the OC API server and the
  Observer service are sanctioned cross-plane consumers.
- New CRDs / new OC primitives. OC's `WorkflowRun` surface is the
  contract; we do not extend it.
- Replacing the GitHub PR / Issue as the source of truth for code
  review.

---

## 2. User-visible UX

Per-task page (route: `/orgs/{org}/projects/{project}/tasks/{taskId}`)
with three independently-fetched panels:

```
┌── Task: "Add JWT validation to /login" ─── in-progress · 4m22s ──────────┐
│ Pipeline strip (always visible)                                          │
│   ✓ Dispatched   ● In progress   · Ready   · Merged   · Building   · Live│
├──────────────────────────────────────────────────────────────────────────┤
│ Activity feed (live, expandable, oldest at bottom, sorted by ts)         │
│   14:32:08 🚀 Pushed b3c4f2a "Add tests"                                 │
│   14:31:45 📦 Committed b3c4f2a (3 files)                                │
│   14:30:12 ✏️  Edited services/auth/jwt.go                               │
│   14:29:50 🔍 Read services/auth/middleware.go                           │
│   14:29:30 🤖 Reading task issue                                         │
│   14:29:10 ⚙️  Workspace ready                                           │
├──────────────────────────────────────────────────────────────────────────┤
│ Artifacts                                                                │
│   Issue #142   PR #18 (draft)   Branch task/jwt-validation-9a3          │
└──────────────────────────────────────────────────────────────────────────┘
```

State-specific changes: after merge the activity feed continues with
build steps (sourced from `WorkflowRun.Tasks[].Phase` deltas) and
culminates in a live deployment URL. Failure states show the last
error message + a "View logs in OpenChoreo" link (we do not embed log
viewing in v1; defer the on-demand `/logs` disclosure to a follow-up).

The existing project Kanban (`ProjectTasksPage.tsx`) is unchanged — it
already polls task status at 5s. The new task detail page deepens the
view *for the currently-viewed task only*.

---

## 3. Signal sources (verified)

| UI surface | Source | How |
|---|---|---|
| Pipeline strip | `ComponentTask.Status` + `Cause` | DB, polled |
| Last-action ticker | Latest line returned in current `/progress/*` response | Derived per-response, never persisted |
| Activity feed (agent phase) | Coding-agent pod **stdout** → fluent-bit → OpenSearch → Observer | BFF queries Observer by `(workflowRunName, namespace, sinceTime)` |
| Activity feed (build phase) | `WorkflowRun.Status.Tasks[]` (`Name/Phase/Message/StartedAt/CompletedAt`) | OC REST `/status`, existing watcher |
| Pod-level k8s events (image pull, OOM) | OC REST `/events?task=` | Pulled on demand from build-step error states |
| Commits on feature branch | `git_commit` / `git_push` events emitted by the runner | Surfaced via Observer; no DB persistence |
| PR / Issue / Branch links | Existing `ComponentTask` columns | DB |
| Coarse audit on GitHub issue | Runner posts **one** structured comment when the PR is marked ready | `gh issue comment`, audit only |

Authoritative findings from the OC sources
(`/Users/wso2/openchoreo-sources/openchoreo`):

- `WorkflowRun.Status.Tasks[]` exposes only `Name/Phase/Message/StartedAt/CompletedAt`
  (`api/v1alpha1/workflowrun_types.go:80-109`). No `Outputs`.
- `Phase` enum is `Pending | Running | Succeeded | Failed | Skipped |
  Error` (`workflowrun_types.go:93`). Both `Failed` and `Error` are
  terminal failure states; we treat them as a single set throughout.
- Run-level completion is signalled by
  `Status.Conditions[type=WorkflowCompleted].Status=True`
  (`internal/controller/workflowrun/controller_conditions.go:151-152`),
  which is the canonical "the controller is done with this run" signal.
  Our watcher uses this — not `Tasks[]` — to decide terminal transition.
- OC `/logs?task=&sinceSeconds=` reads the pod's **container stdout**
  via the gateway proxy (10MB cap per call). Files in `emptyDir` are
  invisible to it.

Authoritative findings from `/Users/wso2/repos/wso2cloud-deployment` and
`/Users/wso2/repos/agent-manager`:

- `agent-manager-service/clients/observabilitysvc/client.go:74-126`
  is the reference Observer client (`GetWorkflowRunLogs(name, ns,
  sinceSeconds)`, Thunder `client_credentials` `AuthProvider`,
  401 → invalidate → retry).
- agent-manager pins Observer at the **Service DNS**
  `http://observer.openchoreo-observability-plane.svc.cluster.local:8080`
  (`deployments/helm-charts/wso2-agent-manager/values.yaml:61,102`).
  The `observer.openchoreo.localhost:11080` gateway hostname is for
  browsers / external callers only — in-cluster BFFs go directly to
  the Service.
- agent-manager's console polls REST with React Query
  (`refetchInterval`, gated on status). No SSE.

---

## 4. Data flow

```
                ┌─ Coding-agent pod (WorkflowPlane, Argo-managed) ──────────┐
                │  oneshot.ts                                               │
                │   ├─ Claude Agent SDK                                     │
                │   └─ secret-scrubber → stdout (NDJSON, schemaVersion: 1)  │
                └─────────────┬─────────────────────────────────────────────┘
                              │ stdout
                              ▼
                  fluent-bit (DaemonSet on WorkflowPlane)
                              │
                              ▼
                  OpenSearch (ObservabilityPlane)
                              │
                              ▼
            ┌─ Observer service (Service DNS) ──┐
            │  observer.openchoreo-             │
            │  observability-plane.svc:8080     │
            └─────────────┬─────────────────────┘
                          ▲
                          │ Thunder client_credentials bearer
                          │
┌─ Console (browser) ─┐   │
│  React Query        │ ──HTTP──► ┌─ app-factory-api (BFF) ─┐
│  /progress/agent     │           │  ObserverClient         │
│  /progress/build     │           │   ├─ request collapser  │
│  /status             │           │   └─ per-org bucket     │
└─────────────────────┘           │  WorkflowRunWatcher     │
                                  │  WebhookProjector       │
                                  └────────────┬────────────┘
                                               ▼
                                          PostgreSQL
                                       (ComponentTask only —
                                       no new tables)
```

---

## 5. Component-by-component design

### 5.1 `remote-worker/` (coding-agent runner)

The single highest-leverage change.

- **Tee SDK message stream to stdout** as newline-delimited JSON.
  Existing `.logs/claude.log` in `emptyDir` stays for in-pod debugging;
  stdout is the new contract that fluent-bit picks up.
- **Secret scrubber** wraps every stdout write with two layers:
  1. **Denylist**: `ANTHROPIC_API_KEY`, the per-task bearer (read from
     env at startup), GitHub tokens (`ghs_*`, `ghp_*`,
     `github_pat_*`), and any line containing `Authorization:` or
     `x-api-key:`.
  2. **Entropy backstop**: regex match on token-shaped substrings
     (≥32 chars, base64-ish charset, Shannon entropy > threshold) →
     redact. Catches secrets the denylist doesn't know about.
  Unit-tested with fixtures (denylist + entropy + multi-line spread).
  Coverage gate in CI.
- **One coarse audit comment** to the GitHub issue when the agent
  marks the PR ready (`✅ Ready for review · b3c4f2a`). Free-form,
  audit only. ETA-style and progress comments are deliberately not
  posted — telemetry stays in-platform; GitHub gets the milestone, not
  the trace.
- **No new env vars on the runner.** No sidecar.

**Stdout schema (v1)** — versioned and shared between runner and BFF:

```json
{"schemaVersion":1,"ts":"2026-05-07T14:30:12.413Z","seq":47,"kind":"phase","phase":"workspace_ready"}
{"schemaVersion":1,"ts":"2026-05-07T14:29:30.001Z","seq":48,"kind":"phase","phase":"reading_issue"}
{"schemaVersion":1,"ts":"2026-05-07T14:30:12.413Z","seq":49,"kind":"tool_use","tool":"Edit","summary":"services/auth/jwt.go"}
{"schemaVersion":1,"ts":"2026-05-07T14:31:45.221Z","seq":50,"kind":"git_commit","sha":"b3c4f2a","files":3,"summary":"Add JWT validation"}
{"schemaVersion":1,"ts":"2026-05-07T14:32:08.910Z","seq":51,"kind":"git_push","sha":"b3c4f2a","branch":"task/jwt-validation-9a3"}
{"schemaVersion":1,"ts":"2026-05-07T14:40:30.500Z","seq":52,"kind":"result","status":"success","summary":"Marked PR ready"}
```

Required fields: `schemaVersion` (currently `1`), `ts` (RFC3339 with
ms), `seq` (monotonic per-pod uint64; for tie-breaking on same-`ts`),
`kind`. `kind ∈ {phase, tool_use, git_commit, git_push, gh_action,
log, result}`. Unknown `kind` is tolerated by the parser
(rendered as `log`); lines that fail JSON parsing are wrapped into
`{kind:"log", summary:"<raw>"}` server-side. Order on read is
**`(ts, seq)` ascending**; dedup on `(ts, seq)` if the same line is
returned by overlapping cursor windows.

The schema lives at `remote-worker/src/lib/progress/schema.ts` (TS
emitter) with a Go mirror at `asdlc-service/clients/observer/schema.go`
generated from a shared JSON Schema fixture; CI fails if they drift.

The fluent-bit pipeline configured by the obs-plane HelmRelease parses
container stdout as JSON when keys are present. Verify our `kind` /
`ts` / `phase` fields don't collide with reserved fluent-bit parser
keys before merging — see §11.4.

### 5.2 `asdlc-service/` (BFF)

**New package: `clients/observer/`** — modeled on
`agent-manager-service/clients/observabilitysvc/client.go:74-126`,
reduced to one method:

```go
type Client interface {
    GetWorkflowRunLogs(ctx context.Context, runName, namespace string,
        sinceTime time.Time, limit int) ([]LogLine, error)
}
```

Auth is a Thunder `client_credentials` `AuthProvider` cached + refreshed
on 401 (mirror agent-manager line-for-line). Per-call timeout 5s.

**Endpoints** (under existing org/project scoping):

- `GET /api/v1/orgs/{org}/projects/{project}/tasks/{id}/progress/agent?sinceMillis=N&limit=M`
  - Source: Observer for the coding-agent WorkflowRun.
  - Returns `{ schemaVersion: 1, lines: [...], cursorMillis, phase, truncated }`.
  - `phase` is the most recent `kind:"phase"` line in this very response
    — never persisted, always consistent with the lines returned.
  - Initial-load contract: client passes `sinceMillis=0` ⇒ BFF substitutes
    `task.startedAt - 1s`. After that, client echoes `cursorMillis`.
  - Active until `Status` leaves `in_progress`; afterwards returns the
    final cached result with `final: true` so the UI freezes the
    feed.
- `GET /api/v1/orgs/{org}/projects/{project}/tasks/{id}/progress/build?sinceMillis=N`
  - Source: `WorkflowRun.Status.Tasks[]` deltas of the build run; the
    BFF computes per-step transitions and emits synthetic NDJSON
    lines using the same `schemaVersion: 1` envelope (`kind: "build_step"`,
    fields `step`, `phase`, `message`, `startedAt`, `completedAt`).
  - Cursor semantics identical to `/progress/agent`. Initial-load
    sinceMillis=0 ⇒ `mergeCommitObservedAt`.
  - Active during `building`; afterwards `final: true`.
- `GET /api/v1/orgs/{org}/projects/{project}/tasks/{id}/status`
  (extends existing): adds `tasks: [{name, phase, startedAt,
  completedAt}]` so the build pipeline strip can render without a
  second call. **No** new persisted columns.

The split into `/progress/agent` and `/progress/build` is deliberate —
it removes the silent mode-switch the architect flagged. Each endpoint
has one source, one cursor semantic, one `final` signal. The console
concatenates the two feeds for display; phase boundaries are explicit.

**Request collapsing**: the BFF wraps `ObserverClient.GetWorkflowRunLogs`
with a `singleflight.Group` keyed by `(runName, sinceMillis-bucket)`
where the bucket is `floor(sinceMillis / 1000)`. N concurrent viewers
of the same task at the same cursor → one Observer call. Capped at
1500ms wait before fallback to direct call.

**Per-org rate ceiling**: token bucket on `/progress/*` keyed by
`(org_handle)` — 100 req/s burst 200. Far above expected viewer load,
prevents one tenant from starving Observer for others. Returns 429 with
`Retry-After`.

**Watchers** (`services/webhook/coding_agent_watcher.go`,
`services/webhook/build_watcher.go`):

- Keep 10s poll cadence; this is the OC-canonical pattern.
- **Drive terminal-state decision off
  `Status.Conditions[type=WorkflowCompleted].Status=True`** (the OC-canonical
  signal — `controller_conditions.go:151-152`), not off `Tasks[]`. Tasks
  can finish before the controller marks the run completed; relying on
  tasks alone races against TTL deletion.
- Auth-failure heuristic: replace the broken `Tasks[].Outputs` lookup
  (see §11.2) with `Tasks[].Phase ∈ {Failed, Error}` on the
  `checkout-source` step + `Tasks[].Message` substring match. Fall
  back to fetching the failing step's stdout via OC `/logs?task=checkout-source`
  if the message is unhelpful.
- Surface the `Tasks[]` array on the task status response so the
  console can render the build pipeline strip without a second call.
- **State label maintenance**: dispatcher writes
  `app-factory.openchoreo.dev/state: pending` on WorkflowRun create.
  The watcher (not OC) flips it to `succeeded` / `failed` on observed
  terminal transition. This is the contract the §6.3 list-call filter
  depends on.

**Webhook projector** (`services/webhook/projector.go`): unchanged in
scope — still the only writer of `ComponentTask.Status`. We do **not**
add intermediate event persistence here.

**No new tables.** Commits flow from Observer (live) and from
`MergeCommitSHA` (post-merge, existing column). Transition history is
derivable from the current row + GitHub PR timestamps + WorkflowRun
timestamps.

### 5.3 `console/` (frontend)

- New route `/orgs/{org}/projects/{project}/tasks/{taskId}` mounted in
  `console/src/`. Replaces the existing detail popup
  (`ProjectTasksPage.tsx:42-43`).
- Three React Query hooks, each with `refetchInterval` derived from
  task state and gated on tab visibility (`refetchIntervalInBackground:
  false`):

  | Hook | Cadence (active) | Cadence (terminal) |
  |---|---|---|
  | `useTaskStatus(id)` | 5s | manual |
  | `useTaskAgentProgress(id)` | 3s with `cursorMillis` echo | stops on `final: true` |
  | `useTaskBuildProgress(id)` | 3s with `cursorMillis` echo | stops on `final: true` |

- New Oxygen components: `TaskPipelineStrip`, `TaskActivityFeed`,
  `TaskArtifactsBar`. The activity feed merges agent + build lines
  client-side, sorted by `(ts, seq)`, deduped on the same key.

### 5.4 Deployments / platform

This is where the "bring up the obs-plane" work lives.

The submodule already has every obs-plane manifest at
`wso2cloud-local/init/observability-plane/`:

- `observability-plane.yaml` — HelmRelease `observability-plane`
  (Observer + cluster-agent + OpenSearch dependency, `dependsOn:
  control-plane` at lines 35-37).
- `observability-logs-opensearch.yaml`, `observability-traces-…`,
  `observability-metrics-prometheus.yaml`.
- `cluster-observability-plane.yaml` — registers
  `ClusterObservabilityPlane` with `observerURL:
  http://observer.openchoreo.localhost:11080` (gateway URL for
  browsers; in-cluster traffic uses the Service — see below).
- `observability-secrets.yaml` — Observer OAuth client secret +
  OpenSearch admin creds via ESO (already seeded in OpenBao at
  `init/layer-0/tools/openbao.yaml:85`).

Layer-2 already creates the Thunder OAuth app
(`openchoreo-observer-resource-reader-client`,
`thunder.yaml:847-866`) and the controlplane RBAC binding
(`controlplane.yaml:161, 423-431`).

The submodule also declares a Flux Kustomization
`kustomizations/observability-plane.yaml` with
`dependsOn: layer-2` — meaning **Flux owns the apply ordering**, not
`platform.sh`. We do not race it imperatively.

**Bring-up changes** (in `deployments-v2/scripts/lib/platform.sh`):

1. Ensure `kustomizations/observability-plane.yaml` is included in
   the kustomizations directory that Flux picks up. (Verify; if missing,
   that is the only submodule edit needed.)
2. After the existing `_wait_for_hr workflow-plane` line
   (`platform.sh:176`), add a new wait — Flux will have started
   reconciling obs-plane in parallel:
   ```bash
   _wait_for_hr observability-plane openchoreo-observability-plane
   ```
   This wait is **non-blocking** to Argo CRD bring-up — they're on
   independent reconciliation paths. We accept that the OpenSearch
   image (~30 min first-pull) is the long pole on cold setup; document
   this in the README.

**Service-DNS in-cluster URL** (BFF → Observer): use the Service
directly, not the gateway hostname. agent-manager's helm values
(`deployments/helm-charts/wso2-agent-manager/values.yaml:61,102`) pin:

```
http://observer.openchoreo-observability-plane.svc.cluster.local:8080
```

We mirror it. The gateway hostname `observer.openchoreo.localhost:11080`
is for browser/external callers only.

**Secret + env wiring** — follow the canonical ESO `SecretReference`
shape, not file-mount strings. Add a new SecretReference at
`wso2cloud-local/domains/platform/namespaces/wso2cloud/secret-references/app-factory-observer-oauth.yaml`
(alongside `anthropic-credentials.yaml`):

```yaml
apiVersion: openchoreo.dev/v1alpha1
kind: SecretReference
metadata:
  name: app-factory-observer-oauth
  namespace: wso2cloud
spec:
  remoteRef:
    key: secret/observer-oauth-client-secret
  target:
    name: app-factory-observer-oauth
```

In `deployments-v2/manifests/env-overlays/app-factory-api.yaml`:

```yaml
env:
  OBSERVER_URL: http://observer.openchoreo-observability-plane.svc.cluster.local:8080
  OBSERVER_OAUTH_TOKEN_URL: http://thunder-service.thunder.svc.cluster.local:8090/oauth2/token
  OBSERVER_OAUTH_CLIENT_ID: openchoreo-observer-resource-reader-client
  OBSERVER_OAUTH_CLIENT_SECRET:
    valueFrom:
      secretKeyRef:
        name: app-factory-observer-oauth
        key: client_secret
```

The OAuth client itself (`openchoreo-observer-resource-reader-client`)
is the platform-default Observer reader app shared with the
controlplane. **Promotion-time decision**: in production WSO2 Cloud,
sharing the platform's observer client across tenant apps conflates
audit. For the local-app-factory branch this is acceptable; revisit
when promoting to a multi-tenant cloud environment (per-app OAuth
registration via Thunder).

**Teardown** (`deployments-v2/scripts/lib/cluster.sh`): unchanged.
HelmRelease deletion cascades on `teardown.sh --all`.

No changes to `ClusterWorkflow: app-factory-coding-agent`. The runner's
stdout change is a code change, not a manifest change.

---

## 6. Multi-tenant scale

App Factory is org-scoped at the top: an org owns many projects, a
project owns many `ComponentTask`s, and many tasks can be in flight
concurrently across the platform.

### 6.1 Per-task isolation (already correct)

- Workspace path inside the agent pod:
  `<orgId>/<projectId>/<taskId>` — already org/project scoped.
- WorkflowRun naming: `app-factory-coding-agent-<taskId>` in the
  WorkflowPlane namespace owned by the project's OC binding. **TaskID
  must be DNS-1123-safe** (lowercase, hyphens only); dispatcher
  enforces. Observer queries are by `(runName, namespace)` — natural
  multi-tenant partition.
- GitHub repo: per-org, per-project. Webhook deliveries deduped by
  GitHub's `X-GitHub-Delivery`.

### 6.2 Polling load + request collapsing

Polling is bounded by what is on screen:

- Project board: 5s poll over the visible project's tasks (existing).
  No `/progress/*` calls — only status.
- Task detail page: at most one task per browser tab. 3s for both
  `/progress/agent` (during `in_progress`) or `/progress/build`
  (during `building`), plus 5s status. Tab not visible → polling
  pauses.
- **BFF-side request collapsing** (singleflight by `(runName,
  sinceMillis-bucket)`) makes N concurrent viewers cost 1 Observer
  call. Eliminates the "N viewers × N tasks" multiplier.
- **Per-org rate ceiling** on `/progress/*` (token bucket, 100 req/s,
  burst 200) caps any one tenant's draw on Observer.

Observer's actual qps budget is platform-owned; we coordinate with the
platform team before promotion. For the local-app-factory branch this
is uncontended.

### 6.3 BFF watchers

Each watcher (coding-agent, build) sweeps every 10s.

- **List filter**: `labelSelector` of
  `openchoreo.dev/workflow-type=app-factory-coding-agent,app-factory.openchoreo.dev/state notin (succeeded, failed)`.
  The dispatcher writes `state: pending` on create; the watcher flips
  it to `succeeded` / `failed` on observed terminal transition (see
  §5.2).
- **Cap per-tick batch** at 200 runs; spillover rolls into the next
  tick. Provides backpressure without losing updates.
- **Single shared loop** for now (current scale). Sharded by namespace
  hash if cluster-wide WorkflowRun count grows past ~10k active.

### 6.4 DB indexes

Project-board query is
`(org_handle, project_name, status, updated_at DESC)`. Verify this
index exists on `component_tasks`; add if missing. No new columns,
no new tables — index footprint unchanged.

### 6.5 Observer query cost

- Always send `sinceTime` (cursor from last poll). Never re-query
  whole task history.
- Cap `limit` per call at 200 lines. Truncate + indicate
  `truncated: true` in the response so the UI can show "…and more".
- Out-of-order delivery from fluent-bit is possible; we sort by
  `(ts, seq)` server-side before returning, dedup on the same key
  across overlapping cursor windows.
- Log retention is whatever OpenSearch ISM configures on the
  ObservabilityPlane — currently the upstream chart default. The UI
  must degrade gracefully when the cursor predates retention: feed
  shows "Earlier output no longer available — see PR for context".
  We do not assert a specific retention number as a platform contract.

### 6.6 GitHub rate limits

The runner posts at most one coarse comment per task (PR ready), plus
its existing PR `gh pr ready`. No proportional-to-progress GitHub
traffic.

---

## 7. State → UI mapping

| `ComponentTask.Status` | Pipeline cell active | Activity feed source | Notable UI |
|---|---|---|---|
| `pending` | Dispatched | (empty) | Issue/PR/branch links populate as the BFF provisions them |
| `in_progress` | In progress (animated) | `/progress/agent` (Observer) | Last-action ticker shows latest line's `phase` |
| `ready_for_review` | Ready | Frozen `/progress/agent` (`final:true`) + last commit | "Review on GitHub" CTA |
| `merged` | Merged | Build dispatch line | Merge SHA shown |
| `building` | Building (animated) | `/progress/build` deltas | Per-step expand → "View logs in OpenChoreo" link |
| `deployed` | Deployed (success) | Final build summary + endpoint URL | |
| `rejected` | Rejected | Last `Cause` + last comment | Reopen path documented |
| `failed` | Failed (red) | Last error from Observer or `Tasks[].Message` | Retry CTA |
| `abandoned` | (greyed) | Static reason | |

ETA estimation is **deferred to a later iteration**. v1 shows elapsed
time only; ETA is calibration work that needs production data and
benefits little from synthetic numbers up-front.

---

## 8. Failure modes & graceful degradation

| Failure | Behaviour |
|---|---|
| Observer unreachable | `/progress/*` returns `503 progress_unavailable`; UI banners "Live progress unavailable", pipeline + status keep working off existing watcher; **the spinner-replacement promise degrades to the existing spinner during outages** — accepted limitation, see §9 |
| fluent-bit lag | Same fallback. Ticker shows "Last update Nm ago" so users notice |
| Stdout flooded / non-JSON | Parser tolerates plain text (`kind: log`). Never throws |
| Secret leaked past denylist scrubber | Entropy-regex backstop catches token-shaped strings; OpenSearch retention is bounded; PR review checks secret-bearing patterns |
| Phantom CRD field bug (`Tasks[].Outputs`) | Fixed as §11.2 prerequisite — auth-failure detection currently silent no-op |
| OC `/logs` 10MB cap | We don't embed log views in v1 (deferred); externalise to OpenChoreo UI |
| Console tab inactive for long periods | Polling pauses; on refocus, single catch-up fetch with echoed `cursorMillis` |
| Cursor crosses retention boundary | UI shows "Earlier output no longer available"; never errors |
| Out-of-order Observer lines | Sort + dedup on `(ts, seq)` server-side |
| In-flight tasks at runner-image rollout | Tasks running on the old image emit no NDJSON; their feeds appear empty until they complete. Documented in rollout (§11.5) |

---

## 9. Security & data handling

- **No new secrets on the runner pod.** It already has
  `ANTHROPIC_API_KEY` and a per-task bearer; both are added to the
  scrubber denylist on startup.
- **BFF → Observer auth**: Thunder `client_credentials` only. Token
  never leaves the BFF. ESO mounts the OAuth secret from the same
  OpenBao path used by the platform default (`secret/observer-oauth-…`).
- **Cross-plane access**: BFF talks **only** to Observer (via Service
  DNS) and to the OC API server. No direct kube-API access to the
  WorkflowPlane.
- **PII in agent output**: agent reads source code, may emit file paths
  and small code snippets to stdout. These land in OpenSearch with the
  platform's standard retention. Treat as no worse than any other
  Workload's stdout. Do not log secrets, environment variable dumps,
  or full file contents — enforced by scrubber (denylist + entropy
  backstop) and reviewed in PR.
- **Coarse GitHub comment**: by design contains only sanitised
  milestone (sha + PR ready), never tool-call detail.

---

## 10. Testing

- **Runner**: unit tests for the scrubber against fixtures (denylist,
  entropy backstop, multi-line spread, partial-overlap matches).
  Coverage gate in CI. Snapshot test that NDJSON round-trips through
  the writer without escaping issues.
- **BFF**: contract tests against a fake Observer (httptest server)
  for: success path, 401 → re-auth, 503 → mapped error, malformed JSON
  line tolerance, `sinceMillis` cursor correctness.
- **Cursor handoff contract test**: agent finishes → `/progress/agent`
  returns `final:true` → `/progress/build` becomes active. Verify
  the console-side concatenated feed has no gap and no overlap.
- **Schema-drift CI gate**: shared JSON Schema fixture tested against
  both the TS emitter (runner) and the Go decoder (BFF). CI fails on
  drift.
- **Singleflight collapsing test**: N parallel `/progress/agent`
  requests for the same `(runName, sinceMillis-bucket)` → exactly 1
  Observer call.
- **E2E** (Playwright): drive in-cluster setup. Dispatch a task,
  confirm activity feed populates within 15s of pod start, has at
  least one `tool_use` line, transitions through merged → deployed.

---

## 11. Prerequisites (must land before this design)

These are not blockers for review — they are simple PRs that should
land first.

### 11.1 Bring up the obs-plane

- Verify `wso2cloud-local/kustomizations/observability-plane.yaml` is
  picked up by the Flux source applying the kustomizations directory
  (it's already declared with `dependsOn: layer-2`). Add to the
  kustomization include list if missing.
- Add `_wait_for_hr observability-plane openchoreo-observability-plane`
  to `deployments-v2/scripts/lib/platform.sh` after the existing
  `_wait_for_hr workflow-plane` (line 176). Do not apply imperatively.
- Verify Observer reachable from the BFF pod at
  `http://observer.openchoreo-observability-plane.svc.cluster.local:8080`.

**Status (2026-05-07):** ✓ done.
- Submodule kustomization include uncommented at
  `wso2cloud-local/kustomizations/kustomization.yaml:18` for production
  posture.
- Local k3d has no Flux `GitRepository` watching the submodule, so the
  obs-plane kustomize is also applied imperatively from
  `platform.sh` (added between layer-2 apply and Argo CRD wait), with
  `_wait_for_hr observability-plane openchoreo-observability-plane 2400`
  added after the workflow-plane wait. `_wait_for_hr` extended to take
  an optional 3rd timeout-seconds arg (default 900; 2400 here matches
  the Flux Kustomization's 35m budget for OpenSearch first-pull).
- All four obs-plane HelmReleases Ready: `observability-plane`,
  `observability-logs-opensearch`, `observability-metrics-prometheus`,
  `observability-traces-opensearch`.
- Observer reachable via Service DNS — `GET /health` returns HTTP 200
  from an in-cluster probe pod.
- Hit one cluster-state side-quest: ESO's connection to OpenBao was in
  `InvalidProviderConfig` ("service account name not authorized") with
  stale token state; fresh-from-`kubectl create token` JWT authenticated
  cleanly, so a `kubectl rollout restart deployment external-secrets -n
  external-secrets` resolved it. Same restart will be needed any time
  ESO drifts (e.g. across cluster restart). Not specific to obs-plane.
- Also fixed an unrelated pre-existing bug: `set -u` violation at
  `deployments-v2/scripts/lib/asdlc.sh:63` where `${#COMPONENTS[@]}` was
  referenced before `components.sh` was sourced one line later. Moved
  the `source` calls above the log line.

### 11.2 Phantom `Outputs` field bugfix (failure-cause taxonomy)

`asdlc-service/models/component.go:117` declares
`Outputs map[string]string` on `WorkflowRunTask`, and
`coding_agent_watcher.go` / `build_watcher.go` use this for git-clone
auth-failure detection (PR D §9.3 in the prior phase doc). OC's
CRD has no such field
(`api/v1alpha1/workflowrun_types.go:80-109`); the value is always nil
on the wire. The auth-failure heuristic is currently silent no-op.

This is a **separate design note** (`docs/design/auth-failure-classification.md`,
to be filed as part of this work) covering:
- The full failure-cause taxonomy (`task_state.go` causes that may
  depend on the broken signal).
- Replacement classifier: `Tasks[].Phase ∈ {Failed, Error}` on
  `checkout-source` + `Tasks[].Message` regex; fallback to
  `/logs?task=checkout-source` for ambiguous messages.
- Migration path: dual-classify for one release (old + new) and log
  divergence; switch over once we see the new classifier matches reality.
- Step-name verification: `Task.Name` is the Argo node `displayName`
  (`workflowrun_types.go:85-87`), not the templateName. Confirm
  `checkout-source` is the actual displayName the
  `app-factory-coding-agent` ClusterWorkflow emits by inspecting a
  live `WorkflowRun.Status.Tasks[].Name` once the obs-plane is up;
  adjust the classifier's step match accordingly.

**Status (2026-05-07):** ✓ design note filed at
`docs/design/auth-failure-classification.md`. Includes the full failure
taxonomy (every `TaskEvent` from `services/task_state.go:13-33`, with
`build.auth_retry_exceeded` flagged as the only one downstream of the
phantom Outputs), drop-in replacement of `isGitCloneAuthFailure`, the
`/logs?task=` fallback for empty `Message`, the dual-classify
migration plan, and the step-name verification action item. Verified
that `checkout-source` IS the literal step name in both build
ClusterWorkflows (`dockerfile-builder.yaml:141`,
`paketo-buildpacks-builder.yaml:111`); the coding-agent ClusterWorkflow
needs a separate live check once it next runs. The watcher fix itself
is §5.2 work, not a prereq.

### 11.3 Confirm Observer auth shape

Confirm the BFF's `client_credentials` against
`openchoreo-observer-resource-reader-client` succeeds end-to-end and
returns a token whose claims authorise reading the WorkflowRun's
namespace logs. The Thunder OAuth app already exists; this is a smoke
test, not a new app registration.

**Status (2026-05-07):** ✓ verified end-to-end with a three-way probe
from an in-cluster pod:
1. `POST http://thunder-service.thunder.svc.cluster.local:8090/oauth2/token`
   with `grant_type=client_credentials`, `client_id=openchoreo-observer-resource-reader-client`,
   `client_secret=openchoreo-observer-resource-reader-client-secret` →
   returns a 907-byte JWT (`aud=openchoreo-observer-resource-reader-client`,
   `iss=http://thunder.openchoreo.localhost:8080`).
2. `POST http://observer.openchoreo-observability-plane.svc.cluster.local:8080/api/v1/logs/query`
   with that bearer + `WorkflowSearchScope{namespace:"openchoreo-workflow-plane",workflowRunName:"dummy-not-real"}`
   → HTTP 400 `query time range cannot exceed 30 days`. Reaching the
   business validation layer means the bearer was accepted; the
   namespace scope was authorised.
3. Same call without bearer → HTTP 401 `MISSING_TOKEN`.
   Same call with garbage bearer → HTTP 401 `INVALID_TOKEN`.

The actual Observer endpoint is `POST /api/v1/logs/query` (not the
`/api/logs/workflow-runs/...` REST shape some earlier docs implied);
matches the OpenAPI contract at
`/Users/wso2/repos/agent-manager/agent-manager-service/clients/observabilitysvc/gen/`.
Pin this when building the BFF Observer client in §5.2.

### 11.4 fluent-bit JSON parser key collision check

Pull the fluent-bit ConfigMap shipped by `observability-logs-opensearch.yaml`
and confirm our `kind`, `ts`, `phase`, `seq` keys don't collide with
its structured-log parser keys. Otherwise lines render as
`_grok_failure` in OpenSearch. Document the resolved keys in §5.1.

**Status (2026-05-07):** ✓ no collisions. Inspected the live
`configmap/fluent-bit -n openchoreo-observability-plane`:
- Pipeline is `tail` (containers/*.log) → `kubernetes` filter
  (`Merge_Log Off`) → `opensearch` output (index `container-logs-YYYY-MM-DD`,
  `Replace_Dots On`, `Generate_ID On`).
- Critical: **`Merge_Log Off`** means fluent-bit does NOT JSON-parse the
  container stdout into top-level fields. Our NDJSON arrives in the
  OpenSearch document's `log` field as an opaque string. Reserved
  fluent-bit / k8s-filter keys (`time`, `stream`, `log`, `kubernetes`,
  `@timestamp`) sit alongside the `log` string, never inside it — zero
  collision surface with our `schemaVersion / ts / seq / kind / phase /
  tool / summary / sha / branch / files / status` fields.
- `parsers.conf` defines one `docker_no_time` parser with `Time_Key
  time`, but the kubernetes filter doesn't invoke it (no `Parser`
  directive); `multiline.parser docker, cri` on the input handles the
  docker/cri envelope only.

**§5.1 update needed:** the design's "fluent-bit pipeline parses
container stdout as JSON when keys are present" line is **not true** for
this config — fluent-bit hands stdout through opaquely. The Observer
client / UI is responsible for `JSON.parse`-ing the `log` field. Track
this as a small §5.1 wording fix; no field renames required.

### 11.5 Runner-image rollout

Old runners on in-flight tasks will emit no NDJSON. Decide before
deployment: either drain old runners (cancel + re-dispatch tasks at
cutover) or accept silent feeds for the runtime of the longest
in-flight task. Recommend the latter — simpler, bounded by typical
agent runtime (~30 min worst case).

**Status (2026-05-07):** ✓ decision recorded — accept silent feeds for
in-flight tasks at runner-image cutover. Bounded by typical agent
runtime (~30 min worst case). No code change.

---

## 12. Out of scope / deferred

- SSE / websockets for execution progress (justified in §1).
- True step-level Argo node graph (OC drops it).
- Storing intermediate progress in our DB.
- Cross-task aggregations like "project velocity" — separate design.
- Multi-region / cross-cluster Observer federation.
- ETA estimation (deferred to v2 once production timing data exists).
- On-demand build-step log disclosure embedded in the console (link
  out to OpenChoreo UI in v1).
- Multiple coarse audit comments (started, pushed). v1 emits only
  "ready"; the others are noise without a clear payoff.

---

## 13. Top risks (named explicitly)

1. **Observer becomes a hard dependency for the primary UX, not just
   the activity feed.** §8 fallback degrades to the existing spinner
   during Observer outages — exactly what we set out to replace. Mitigation:
   the pipeline strip + status remain functional from the existing watcher;
   we accept the activity feed degradation. Revisit if Observer
   reliability is below 99.5%.
2. **Cursor correctness across the agent → build phase boundary.**
   Most likely correctness bug. Mitigation: split endpoints (§5.2),
   explicit `final:true`, contract test in §10.
3. **Per-tenant noisy-neighbour on Observer.** A chatty agent can
   blow `limit=200` lines/3s. Mitigation: per-org rate ceiling +
   request collapsing (§6.2). Revisit if any tenant routinely hits 429.

---

## 14. Review status

Reviewed by `oc-design-expert`, `wso2cloud-expert`, and an architect
review.

- **OC: APPROVED** (rev 2). Soft items folded in: `Phase ∈ {Failed,
  Error}`, `ConditionWorkflowCompleted` as terminal signal,
  state-label maintenance contract, `/events` source, DNS-1123 task
  IDs. Non-blocking nit: confirm `checkout-source` is the actual
  Argo node `displayName` once obs-plane is live (in scope of §11.2).
- **Cloud: APPROVED** (rev 2). All three hard blockers addressed —
  Service-DNS Observer URL, ESO `SecretReference + secretKeyRef`
  env shape, Flux-owned obs-plane ordering. Soft items (singleflight,
  retention softening, fluent-bit collision check, OAuth promotion
  note) folded in.
- **Architect: APPROVED** (rev 2). All nine concerns addressed —
  schemaVersion'd NDJSON with `(ts, seq)` ordering, split
  `/progress/{agent,build}` with `final:true`, `phase` derived
  per-response, `sinceMillis=0` anchoring, scrubber entropy backstop,
  per-org rate ceiling, ETA + audit-comment cuts, `task_stream.go`
  asymmetry justified, file-path corrections.
