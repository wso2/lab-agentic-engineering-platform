# Cross-component wiring — gap analysis and proposed fixes

**Scenario under audit:** user enters "todo web app without auth" → architect proposes two components (`todo-api` Service in Go, `todo-web` Web App in TS/React, with `todo-web → todo-api` dependency) → tasks are generated, one per component → both are dispatched → `todo-api` builds and deploys → `todo-web` is implemented next, calling the `todo-api`'s deployed URL.

**Why this doc exists.** The user expects (paraphrased): *"webapp depends on api; once api is built, that url should be sent to the issue link of webapp."*

This doc maps what's already working, where the flow silently degrades, and the agreed fixes. The plan went through three rounds of independent architect review and then a directional pivot from the user (drop OC's `dependencies.endpoints` wiring entirely; the agent verifies live api integration before opening its PR) — the design below reflects that final pivot.

---

## 1. Evidence (validated end-to-end)

A live Playwright walk-through of project `todo-audit-2` on the local cluster, plus targeted code reads, confirmed each item below. File paths are repo-relative.

### 1.1 What already works ✅

| # | Behaviour | Evidence |
|---|---|---|
| W1 | Architect produces a multi-component design with `dependsOn` | `agents/src/agents/architect/schema.ts:20-24`; console rendered "Todo Web — Depends on: Todo Api" |
| W2 | Design persists `DesignComponent.DependsOn []string` | `asdlc-service/models/design.go:9` |
| W3 | Tech-lead produces exactly one task per component | `agents/src/agents/tech-lead/prompt.ts:28-32`; DB confirmed 2 rows for 2 components |
| W4 | Task-level `dependsOn` is persisted as `ComponentTask.TaskDependsOn` (currently as titles — see G3) | `asdlc-service/services/task_stream.go:429`; DB query showed `todo-web.task_depends_on = ["Implement todo-api service with CRUD endpoints"]` |
| W5 | Issue is created at *generation* time (not dispatch) | BFF log: `"created github issue for task" component=todo-web issue=…/issues/2` arrived during generation; both tasks had `issue_number` set before any dispatch |
| W6 | The BFF resolves a component's deployed URL via `ListDeployments()` | `asdlc-service/clients/openchoreo/component_client.go:204-212` → `Deployment.EndpointURL`; consumed by `console/src/pages/ComponentDeployPage.tsx:183-196` (the Deploy page) |
| W7 | Dispatch gates dependent tasks via `pending_deps` | `services/dispatch_service.go:115-135` + `depsAllMerged()` at `:144-161`; DB after Execute-all: `todo-api=in_progress`, `todo-web=pending_deps` |
| W8 | Console shows a "Waiting on deps" badge for `pending_deps` tasks | Playwright snapshot of the `todo-web` card |
| W9 | Merge of a dep flips its dependents `pending_deps → pending` | `services/webhook/handlers.go:283-336` (`reevaluatePendingDepsForProject`) invoked from `pull_request.closed merged=true` |

### 1.3 Load-bearing invariant — URL reachability across three surfaces

The plan in §3 quietly assumes that the URL `ListDeployments().EndpointURL` returns is reachable from **all three** of the surfaces below using the same string. If any one fails, F3c (verify-before-PR) misfires silently or the deployed bundle 404s. Confirm this holds before merging the plan.

1. **The user's browser**, loading the Vite-built static bundle. The URL is baked into the bundle (`VITE_*_URL`).
2. **The static-bundle pod's nginx process** — only if the bundle proxies (it doesn't, for our scenario; the SPA fetches directly from the browser). Listed for completeness.
3. **The coding-agent runner pod**, running on the WorkflowPlane in the local k3d cluster, for F3c's `curl` step.

Today (local cluster), `EndpointURL` returns the form `http://http-<component>-development-<hash>.openchoreoapis.localhost:19080`. Surface (1) works (the host's `/etc/hosts` or the `.localhost` TLD pattern resolves it). Surface (3) only works if the CoreDNS rewrite for `*.openchoreoapis.localhost → <gateway-service>` is in place inside the cluster and the runner-pod namespace has egress to the gateway service. This is configured in `wso2cloud-deployment` (CoreDNS rewrite for the `openchoreoapis.localhost` TLD). **Verify this with a one-line `kubectl exec` from a workflow-pod-style namespace to `curl` the URL before F3c is rolled out**; if it fails, the plan needs a different URL surface (in-cluster service DNS) for surface (3) and we lose "the same URL across three surfaces" — flag it and revise.

Companion invariant: services that have dependents **MUST** declare their endpoint with `visibility: external` so `ListDeployments()` returns a non-empty `ExternalURLs.Http` (`clients/openchoreo/component_client.go:204-212`). Project-visibility services would return an empty URL and the plan collapses. The skill (F3) must instruct the agent: "every service component MUST declare at least one endpoint with `visibility: external` for v1". Track as a known limitation when service-to-service deps come online (those can stay `project`-visibility once we have a runtime-injection story).

### 1.2 What is broken / missing ❌

---

## 2. Gaps

### Gap G1 — No auto-dispatch after a dep becomes eligible *(blocker; user-visible)*

After `todo-api`'s PR merges and the dep ultimately deploys, the webhook handler flips `todo-web` from `pending_deps` to `pending`. **But nothing dispatches `todo-web`.** The task sits in `pending` indefinitely; the user has to come back and click "Execute all" again.

`services/webhook/handlers.go:283-290` only writes the new status. The trailing comment claims "the existing dispatch loop's re-evaluation" picks it up — **no such loop exists.** Dispatch is exclusively a synchronous HTTP path; there is no cron / watcher / sweeper that retries pending tasks.

### Gap G2 — Deployed URL is never given to the dependent task's agent *(the user's explicit ask; reframed)*

When `todo-api` reaches `deployed`, its real cluster URL is known to the BFF (`ListDeployments() → EndpointURL` — exactly the value the Deploy page renders). **But this URL never reaches the `todo-web` agent.** The agent runs without ever seeing the api's address — under today's plan it would have to rely on OC's runtime env-var injection at the deployed pod's startup, which is too late for code generation or integration verification.

### Gap G3 — Dependency identifier is an LLM-authored string *(latent correctness bug)*

Architect's design uses component **names** (`todo-api`). Tech-lead's `PlanItem.dependsOn` rewrites these into task **titles** which `ComponentTask.TaskDependsOn` stores. `dispatch_service.go:147-150` matches against titles and silently skips unknown keys:

```go
st, ok := statusByTitle[depTitle]
if !ok { continue }   // unknown title treated as satisfied
```

If the LLM emits a `dependsOn` value that doesn't exactly match any task title, gating *silently fails open*.

"LLM-authored strings in the gating path" is a category-level mistake. The dependency identifier should be a primitive the LLM didn't invent.

### Gap G4 — `dependencies.endpoints` YAML is over-machined for a simple need *(complexity)*

`issue_body.go:76-94` writes a `workload.yaml` snippet directing the agent to use OC's `dependencies.endpoints` for runtime URL injection. That machinery is correct in OC, but it solves a runtime-injection problem we don't actually need to have:

- The webapp is a *web-app*: OC builds it as static files behind nginx. The browser makes HTTP calls to the api, not the pod. So the pod itself doesn't need OC-injected env vars at runtime — the URL needs to be in the **bundle** (Vite env-var, baked at build), not in the pod env.
- For service-to-service dependencies (backend → backend) we *would* need runtime injection — but the audit scenario doesn't have any.
- Keeping `dependencies.endpoints` as the wiring story forces every component to learn an OC primitive, learn an env-var-name convention, and wire two files (`workload.yaml` + source) that have to agree. For the frontend → backend case this is gratuitous complexity.

### Gap G5 — Agent has no integration verification *(quality)*

Today the webapp agent codes against the api's OpenAPI spec in `design.json` and opens a PR. It never talks to the live api. So an api whose runtime behaviour drifts from its spec produces a webapp PR that looks correct but breaks at first browser-test. The agent has the OpenAPI; it doesn't have a live curl-able URL during its run.

### Gap G6 — Board column buckets don't reflect `pending_deps` *(minor UX)*

DB shows `todo-api=in_progress` and `todo-web=pending_deps`, but the Playwright snapshot of the board shows both cards still under "To Do" with "In Progress 0" and "On Hold 0". `useProjectBoard.ts` likely lumps `pending_deps` under "To Do" and the dispatch mutation doesn't invalidate the right query key.

---

## 3. Agreed fixes

The shape below is the converged design. F1 + F2 + F3 together satisfy the audit scenario; F4 + F5 + F6 are cleanup. Independently shippable in the order in §6.

### F1 — Auto-dispatch dependents when a component deploys *(fixes G1)*

**Reframed from "auto-dispatch on merge" to "auto-dispatch on deploy"** because the dispatch needs to inject the resolved URL into the agent prompt, and the URL is only available once the dep is `deployed`.

**Implementation.**

1. Inject `DispatchService` into the webhook plumbing.
2. Extract `func (h *Handler) onTaskDeployed(ctx context.Context, taskID, projectID, orgID, componentName string)` from `webhook/projector.go::ApplyBuildResult` — runs post-commit when `next == TaskStatusDeployed`.
3. Inside `onTaskDeployed`: take a per-*project* advisory lock (mirrors the per-task lock pattern in `projector.go::acquireTaskLock` — extend with `acquireProjectLock(tx, projectID)`). Under the lock: find every task in the project that lists `componentName` in `DependsOnComponents` (per F2), check the rest of its deps are also deployed, flip eligible tasks `pending_deps → pending`, and call `DispatchService.DispatchTasks(ctx, orgID, projectID)`. The lock prevents two near-simultaneous deploys from racing on the eligibility scan + dispatch loop and double-dispatching the same dependent task.
4. Structured BFF log per dispatch: `slog.InfoContext(ctx, "dispatched with dep endpoints", "task", taskID, "deps", []DependencyEndpoint{...})`. This is the durable audit trail (the prompt and PR are user-facing; the runner pod's logs are ephemeral).
5. Failures route through the existing `dispatchService.markFailed()` path → task transitions to `TaskStatusFailed` with `ErrorMessage` populated. F4's distinct `failed` column surfaces this. No new observability schema.
6. One unit test: `building → deployed` event on `task_a` causes a `pending_deps` `task_b` (which only deps on `task_a`'s component) to land in `in_progress`. A second test: two near-simultaneous `deployed` events on `task_a` and `task_b` (both deps of `task_c`) result in `task_c` dispatching exactly once.

`reevaluatePendingDepsForProject` from today's merge-handler logic moves out — under deploy-gating it no longer fires on merge; it fires on deploy.

### F2 — Component name as the dependency identifier with deploy-gating *(fixes G3)*

Drop title-matching. Use **component names** sourced directly from `design.Components[*].DependsOn` as the stable dependency identifier.

**Design.**

1. Schema rename: `ComponentTask.TaskDependsOn` → `ComponentTask.DependsOnComponents []string`. JSONB column rename + value backfill on local dev (App Factory is pre-prod; no production migration needed).
2. Persistence change in `task_stream.go:410-490`: ignore the LLM-emitted `PlanItem.dependsOn` for persistence; pull `DependsOnComponents` directly from the design via `design.Components[taskComp].DependsOn`. The LLM still emits `dependsOn` for Phase 2 *prompt context*, but it never enters the gating path.
3. Validation at persist time: if a component's `DependsOn` lists a name not in `design.Components[*].Name`, fail the whole generation loudly and abort issue creation.
4. **Dispatch semantic (deploy-gated):**
   ```go
   // A task is dispatchable when, for every entry c in DependsOnComponents,
   // there exists a task in this batch whose ComponentName == c and Status == deployed.
   // No cross-batch fall-through under the simplified plan — deploy-gating is per-batch.
   ```

**Why component name is the right primitive.**

- Stable in the design (the architect explicitly preserves it across regenerations).
- Present at planner time; the LLM doesn't invent it.
- 1:1 with a task per batch.
- Task UUIDs / issue numbers don't exist when the planner runs.

**Why deploy-gating (the previous round of review pushed back on this; the user's pivot reverses that).** Under the new model the agent needs the dep's URL **at code-gen time** (to bake into the bundle and to verify integration). Merge-gating doesn't provide a URL; deploy-gating does. The cost — serial latency along the dep chain — is accepted explicitly. For the audit scenario (`todo-web → todo-api`) that's ~3–5 minutes of additional serial wait, paid once. Multi-level chains (5+ components in series) are an acknowledged tax; revisit if it becomes painful.

### F3 — Drop OC dep wiring; flow the URL through dispatch prompt + frontend constant + verify-before-PR *(fixes G2 + G4 + G5)*

This is the central simplification. Three coordinated parts:

**F3a — Dispatch-time URL handoff to the agent.**

Extend `buildAgentPrompt` in `dispatch_service.go:250-259` to include a "Dependency endpoints" section when the task has resolved deps:

```
Work on this GitHub issue: <issue-url>

## Dependency endpoints (resolved at dispatch)
- todo-api: http://todo-api.dp-…/

You are at the project repo root, on its default branch. Create your own
feature branch, implement the task, and open a PR whose body includes
the literal text `Closes #<n>`.
```

Dispatcher calls `componentSvc.ListDeployments(ctx, orgID, projectID, depComponentName).EndpointURL` for each dep (the same call powering the Deploy page; single source of truth). Under F2's deploy-gating, the URL is guaranteed present at this moment. No env var schema, no immutable-pod-env trap.

**F3b — Delete `dependencies.endpoints` from the BFF issue body; agent's `workload.yaml` stops emitting it too.**

Important scope clarification: F3b deletes ONLY the *consumer*-side dep-wiring (`dependencies.endpoints`). The *provider*-side endpoint declaration on each service's own `workload.yaml` (`spec.endpoints: [- name: http, type: HTTP, port: …, visibility: [external]]`) stays — that's what OC needs to mint the external URL the dependent will use, and removing it would zero out `ListDeployments().EndpointURL` and collapse the plan (see §1.3).

In `services/issue_body.go:76-94`, delete the `## Component Dependencies` section entirely. The issue body has no dep-wiring instructions.

The `asdlc` skill (`remote-worker/plugin/skills/asdlc/SKILL.md`) is updated to instruct the agent:

> *Every service component MUST declare at least one endpoint in `workload.yaml`'s `spec.endpoints` with `visibility: external` for v1 (HTTP services: `type: HTTP`, `port: <chosen>`). This is what makes the deployed URL visible to dependents and to the browser.*
>
> *If your task's "Dependency endpoints (resolved at dispatch)" section in the prompt lists upstream components, bake each URL into your component as a build-time constant. For Vite/React, put it in `.env` as `VITE_<UPSTREAM>_URL` and read via `import.meta.env.VITE_<UPSTREAM>_URL`. For other frameworks, use the framework's idiomatic build-time constant mechanism. Do NOT add a `dependencies.endpoints` block to `workload.yaml` — consumer-side runtime URL injection is not used in this platform for v1.*

**F3c — Verify-before-PR, with a first-class failure state.**

Skill instructs the agent:

> *Before opening your PR ready-for-review, curl each dependency URL listed in the prompt. Verify:*
> 1. *Reachability (200/2xx on a basic GET).*
> 2. *At least one happy-path operation per resource group from the OpenAPI (e.g. POST + GET + DELETE on `/todos`).*
> 3. *Response shapes loosely match the OpenAPI (status codes + top-level fields).*
> *If verification fails: keep the PR as a **draft**, post a "Dependency verification failed" comment on the issue with the diagnostic (which URL, which curl, what response), and call the BFF's `POST /tasks/{id}/verification-failed` endpoint (authed by the per-task JWT the runner already holds). The BFF transitions the task to `verification_failed` and surfaces it on the board.*

**New task state.** Add `TaskStatusVerificationFailed` to `models/component_task.go`'s status enum and to `services/task_state.go`'s transition table:

```
in_progress → verification_failed   on TaskEventVerificationFailed
verification_failed → in_progress   on TaskEventRetry (user-initiated)
```

`TaskEventRetry` re-dispatches the same task (the existing `DispatchService.dispatchOne` is idempotent on `DispatchedAt` — we clear `DispatchedAt` and `LastCodingAgentRunName` on retry so a fresh WorkflowRun is created). A "Retry" button on the dependent task's board card invokes this — surfaced via a new BFF endpoint.

Without an explicit failure state, F3c is theatre: the task sits in `in_progress` indefinitely with no signal and no recovery action. With `verification_failed` as a distinct state, the operator sees the queue (F4 surfaces it as its own column or badge), reads the diagnostic, and clicks Retry.

This raises the artifact quality: the webapp PR is no longer "I wrote code against the spec"; it's "I wrote code against the spec and the live api conforms to it".

**Trade-offs we accept "for now":**

- **Single-environment only.** Vite bakes `.env` at build time. Promoting to staging means rebuilding with a different `VITE_<UPSTREAM>_URL`. Document as a known limitation.
- **Frontend → backend only.** Service-to-service deps would still want runtime env injection (Go services aren't built per-env). When the scenario shows up, re-introduce a minimal `dependencies.endpoints` story for that case — likely guided by the dep's component-type (web-app vs service) at task-generation time.
- **No multi-comment audit trail.** The URL is in the source code constant (visible in the PR diff), in the dispatch prompt (visible in the agent's run), and on the Deploy page (visible in the console). We don't post a separate sticky comment on the dependent's issue — three surfaces is enough. (Previous round of review proposed `dependency_endpoint_announcements` table + sticky comment + PATCH-on-redeploy — all dropped.)

### F4 — `pending_deps` as a distinct, descriptive board column *(fixes G6)*

In `console/src/hooks/useProjectBoard.ts`: route `pending_deps` to its own column — *not* folded under user-managed "On Hold". Surface what it's waiting on — "Waiting for: todo-api (building)" — derived from `DependsOnComponents`. Invalidate the relevant `react-query` key on the dispatch mutation so column counts refresh without manual reload. Pure-frontend.

---

## 4. Out of scope (intentionally)

- **Multi-environment URL handling.** Vite bakes `.env` at build time → one URL per build. Per-env builds when staging/prod come online.
- **Service-to-service (backend → backend) dependencies.** When this shows up, bring back a minimal runtime-injection story — likely the OC `dependencies.endpoints` path but gated on component-type at the task layer.
- **Auto-redispatch when a dep's URL changes (re-deploy with new host/port).** The webapp's already-shipped bundle goes stale; manual rebuild required. Acceptable for v1.
- **Cross-batch dependency resolution.** Under deploy-gating with per-batch scope (F2), a v2 webapp task in a new batch can only depend on a v2 api task in the same batch. The simplified model doesn't try to model "depend on a previously-deployed version".
- **Polling fallback for missed webhooks.** Per `CLAUDE.md`, the webhook relay is the source of truth; missed deliveries replay through smee.io. F1's auto-dispatch piggybacks on the deploy webhook — if the relay is down, the whole platform is, not just F1.

**Deployment-environment property worth restating** (not out of scope, but a precondition):

- The runner pod (WorkflowPlane namespace, local k3d) **must** be able to egress to the api's external URL — i.e. CoreDNS rewrite for `.openchoreoapis.localhost` must resolve to the gateway service from inside the cluster, and NetworkPolicy must permit it. See §1.3. This is a property of the `wso2cloud-deployment` config; verify with `kubectl run --rm -it --image=curlimages/curl -- curl -v <EndpointURL>` from the workflow-pod namespace before F3c lands.

---

## 5. Validation done for this doc

- **Live cluster run.** Project `todo-audit-2` on the local cluster via Playwright with prompt "todo web app without auth"; walked Requirements → Architecture → Tasks → Execute all.
- **DB shape verified.** `psql` against `wso2cloud/app-factory-postgresql` confirmed 2 tasks with the expected `task_depends_on` (title-based today), `issue_number`, and post-dispatch states `in_progress` / `pending_deps`.
- **Issue body verified.** `gh issue view 2 --repo asdlc-repos/todo-audit-2619` returned a body containing the current `dependencies.endpoints` snippet (to be deleted per F3b).
- **OC schema check.** `platform-design-expert` confirmed the existing snippet conforms to `Workload.spec.dependencies.endpoints[]` (`api/v1alpha1/workload_types.go:169-195`). The fix path drops that machinery, not because it's wrong but because it's gratuitous for the frontend → backend case.
- **Architect review.** Two rounds against the merge-gating + sticky-comment + skill-templated-YAML design, then a user-directed pivot to URL-as-constant + deploy-gating + verify-before-PR + drop-OC-dep-wiring. The plan above reflects the pivot; a fresh architect pass against this revised shape is appended as §8.
- **Code paths confirmed by direct read:** `agents/src/agents/architect/{schema,prompt}.ts`, `agents/src/agents/tech-lead/{schema,prompt}.ts`, `asdlc-service/models/{design,component_task}.go`, `asdlc-service/services/{task_stream,dispatch_service,issue_body,task_state}.go`, `asdlc-service/services/webhook/{handlers,projector,build_watcher}.go`, `asdlc-service/clients/openchoreo/component_client.go`.

---

## 6. Implementation order

1. **F2 (component-name identifier + deploy-gating).** Schema rename + persistence rewrite + dispatch resolver swap (`depsAllMerged → depsAllDeployed`) + persist-time validation. Smallest blast-radius foundational change; everything else assumes the new identifier and gating.
2. **F1 (auto-dispatch on deploy).** Extract `onTaskDeployed` in `webhook/projector.go::ApplyBuildResult`, call `DispatchService.DispatchTasks`. Remove the `reevaluatePendingDepsForProject` call from the merge handler (it moves to the deploy handler under the new model).
3. **F3 (URL-as-constant + skill update + verification failure path).** Extend `buildAgentPrompt` with the "Dependency endpoints" section. Delete `## Component Dependencies` from `issue_body.go`. Add `TaskStatusVerificationFailed` + transitions to `task_state.go`; add `POST /tasks/{id}/verification-failed` (per-task-JWT-authed) + `POST /tasks/{id}/retry` BFF endpoints. Update `asdlc` skill with the frontend-constant pattern + verify-before-PR step + worked examples. Tests: (a) prompt-rendering test with deterministic ordering; (b) state-machine test for `in_progress → verification_failed → in_progress`; (c) integration-shaped test simulating an agent reporting verification failure and a subsequent operator retry triggering a fresh dispatch.
4. **F4 (board column for `pending_deps`).** Frontend-only. `useProjectBoard.ts` + dispatch query-key invalidation. "Waiting for: …" annotation derived from `DependsOnComponents`.

Each fix should ride through the standard pre-merge gate: the **`platform-design-expert` review** (per `CLAUDE.md` — anything touching OC primitives, the OC client, env overlays, or webhook handlers) and the **cluster-health pre-flight** before any live re-test.

---

## 7. Refactoring opportunities along the way

The code paths touched by F1–F4 carry incidental debt that is fair game in the same PRs.

- **`asdlc-service/services/webhook/projector.go::ApplyBuildResult`** has no post-commit hook surface. F1 adds the `onTaskDeployed` named method (symmetric with where `onTaskMerged` used to live before the pivot). `ApplyBuildResult` returns the resulting `next` status; the thin `onTaskDeployed(taskID)` handler runs the cascading dispatch outside the transaction.
- **`asdlc-service/services/webhook/handlers.go`** loses `reevaluatePendingDepsForProject` from the merge path and the stale comment at line 283. The merge handler shrinks to: apply state, record merge SHA, attempt rendezvous-build. Cleaner.
- **`asdlc-service/services/dispatch_service.go::DispatchTasks`** mixes eligibility computation with dispatch side-effects in one loop (`:112-139`). Split eligibility resolution (build status-by-component map, filter to dispatchable, transition `pending_deps ↔ pending`) from `dispatchOne` per eligible task. Also rename `depsAllMerged` → `depsAllDeployed` (or `depsSatisfied`) since the semantic is now deploy-gated and component-keyed.
- **`asdlc-service/services/issue_body.go::buildIssueBody`** keeps two unused parameters "to preserve call sites". F3b rewrites the function anyway; drop `_repoURL` and `_repoSlug` and update call sites in the same diff. Split into `componentReferenceSection(comp)` (kept) and `componentDependenciesSection(comp)` (deleted, with the call site removed).
- **`asdlc-service/services/dispatch_service.go::buildAgentPrompt`** is a one-line `fmt.Sprintf`. F3a extends it; while you're there, move it to a typed prompt builder with a `DependencyEndpoint{Component, URL}` slice argument so the test surface is clean.
- **`asdlc-service/models/component_task.go`** `TaskDependsOn` column docstring (line 98) talks about "titles". F2 renames the field — also update the docstring to the component-name semantic.
- **Deterministic prompt ordering.** When `buildAgentPrompt` becomes a typed builder accepting `[]DependencyEndpoint`, sort the slice by `Component` name before rendering. Determinism matters for prompt caching and test snapshots — same task, same prompt bytes, every time.
- **`agents/src/agents/tech-lead/prompt.ts`** lines 25 + 54-55 + 275-279 currently emphasise "dependsOn names must match other titles in this batch verbatim". Under F2 the requirement evaporates — the LLM's emission of `dependsOn` is for *prompt context only* (Phase 2 detail) and is not persisted. Update the prompt to instruct the LLM to emit `dependsOn` as **component names**, matching the slim-design input it already receives; clarify the context-only role.
- **`remote-worker/plugin/skills/asdlc/SKILL.md`** lines 143-172 today document the OC `dependencies.endpoints` workflow. F3 replaces that section with the frontend-constant pattern + verify-before-PR step. Keep the OC dep-wiring documentation in a section labelled "Legacy / advanced — service-to-service" for when we re-introduce it.

These refactors ARE the bulk of F1–F4's diff. Stating them here so reviewers know they're expected scope, not gratuitous churn.

---

## 8. Architect review trail

Three rounds against the pre-pivot design converged on **merge-gating + OC `dependencies.endpoints` runtime injection + sticky comment**. The user then pivoted with two explicit directives (drop consumer dep wiring; agent must integration-verify). A fourth round, against this revised doc, surfaced and resolved the following issues (all folded into the §1.3 invariant, §3 fixes, §4 out-of-scope, and §7 refactors above):

- **URL reachability invariant was unstated.** Named in §1.3; pinned `visibility: external` for service components with dependents.
- **F1 concurrency on near-simultaneous deploys.** Per-project advisory lock added to `onTaskDeployed`; second test case added.
- **F3c was theatre without a failure state.** New `TaskStatusVerificationFailed` + `TaskEventRetry` transition + `POST /tasks/{id}/{verification-failed,retry}` endpoints + operator-facing Retry path. Without these, a verification failure leaves the task stuck in `in_progress` indefinitely.
- **No durable audit log.** Structured BFF log `"dispatched with dep endpoints"` added at dispatch.
- **Runner-pod egress assumption** explicitly called out in §4 as a precondition, with a one-line verification command.
- **`persistAndIssue` split removed from §7** — unrelated to F1–F4; deferred.
- **Deterministic prompt ordering** added to §7 (sort `DependencyEndpoint`s by component name).

The doc is closed for design pending operator validation of the §1.3 invariant (`kubectl run --rm` curl from the workflow-pod namespace to a known external URL).
