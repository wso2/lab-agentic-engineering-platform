# Remote-Worker Refactor: In-Process Agent Service → ClusterWorkflow

**Status:** Draft v3.1 — straight replace, no feature-flag coexistence. App Factory is pre-production today; the v2 cutover machinery (feature flag, dual CI images, 2-week burn-in) is overkill. v3.1 incorporates round-2 review fixes for two concrete cutover hazards: image-tag mutability and multi-repo merge ordering.

**Scope:** Replace the long-lived `app-factory-remote-worker` HTTP service with a per-task Argo `WorkflowRun` of a new `ClusterWorkflow: app-factory-coding-agent`, **with no change to user-facing functionality, API surface, or task state machine.**

**Why this first:** Every other part of the broader migration (per-tenant namespaces, custom ComponentTypes, autobuild rerouting) leaves the worker alone. The worker is the largest single architectural deviation from Agent-Manager's pattern and the only piece that is *both* user-impacting at scale (per-task isolation, retries) *and* completable as a self-contained refactor.

---

## 0. Functional parity contract

After this refactor, end users observe **no change**:

| Observable | Today | After refactor |
|---|---|---|
| Console UI | identical | identical |
| BFF REST API surface | identical | identical |
| GitHub artifacts (Issue, branch, PR) created in same order, by BFF before agent runs | yes | yes |
| Agent does code edits + commits + `gh pr ready` | yes | yes |
| ComponentTask state machine (events, transitions, terminal states) | unchanged | unchanged |
| Webhook-driven status transitions (`pull_request:ready_for_review`, `pull_request:closed`, `push`) | unchanged | unchanged |
| Task JWT (RS256, 24h) | unchanged | unchanged |
| `gh` wrapper + git credential helper using `/credentials/refresh` | unchanged | unchanged |
| Allowed agent tools (Read, Write, Edit, Bash, Glob, Grep) | unchanged | unchanged |
| Failure mode: agent crashes mid-run | task stuck `in_progress` | **same** (see §6.7 — terminal failure detection deferred) |

**Architectural changes**:

| Aspect | Today | After |
|---|---|---|
| Process model | One long-lived `app-factory-remote-worker` Deployment with in-process Claude Agent SDK pool | One ephemeral Argo pod per task on the WorkflowPlane |
| Dispatch | BFF `RemoteWorkerService.Dispatch` POSTs to `/dispatch` on remote-worker | BFF `WorkflowRunService.TriggerCodingAgent` creates a WorkflowRun via OC REST |
| Concurrency control | In-process semaphore inside remote-worker (default 8) | Argo / cluster scheduling; per-tenant concurrency optional (out of scope) |
| Resource isolation | All tasks share one pod's resources | Each task gets its own pod with its own limits |
| Per-task workspace | Persistent path in remote-worker pod's filesystem | `emptyDir` volume on the per-task pod |
| Status observability | None (worker doesn't report; GitHub webhooks drive state) | WorkflowRun `status.conditions` queryable via OC REST |

---

## 1. Current state (concise)

`asdlc-service` (BFF) on every dispatch:

1. Calls `RemoteWorkerService.dispatchOne` (`services/remote_worker_service.go:172-315`), which:
   - Verifies GitHub Issue exists.
   - Creates feature branch (idempotent on `BranchName`).
   - Seeds `specs/task.json` commit (so a draft PR can be opened).
   - Creates draft PR (idempotent on `PullRequestNumber`).
   - Ensures the OC Component for the user-app exists (with `AutoBuild=false`).
   - Mints fresh task JWT (RS256, 24h).
   - **POSTs to `app-factory-remote-worker:/dispatch`** with payload: `{taskId, orgId, projectId, componentName, branchName, repoURL, bearer, identity{name,email,login}, gitServiceURL, prompt}`.
   - Marks `DispatchedAt`, sets task status → `in_progress`.
2. `app-factory-remote-worker` (TS, `src/index.ts` + `src/routes/dispatch.ts`):
   - Provisions `~/asdlc-workspace/{orgId}/{projectId}/{taskId}/`: `.git/` (cloned via PAT from one-shot `/credentials/refresh`), `.gh-config/hosts.yml`, `specs/{bearer, credhelper.sh, gh}` (chmod-restricted).
   - Spawns Claude Agent SDK `query()` with `allowedTools: [Read, Write, Edit, Bash, Glob, Grep]`, `cwd: <workspace>`, env passed by file path (no token in env).
   - Agent edits files, commits, pushes via `gh` wrapper, runs `gh pr ready`.
   - Pod's task registry tracks completion `{exitCode, error}`. **No callback to BFF on success.**
3. State transitions are GitHub-webhook-driven thereafter:
   - `gh pr ready` → `pull_request:ready_for_review` → BFF marks `ready_for_review`.
   - PR merged → `pull_request:closed merged=true` → BFF marks `merged`.
   - Default-branch push → BFF triggers a *build* WorkflowRun via the **already-existing** `WorkflowRunService.TriggerForPush` (`services/workflowrun_service.go:86-201`) which calls `componentClient.TriggerBuildAtCommit` (`clients/openchoreo/component_client.go:561-563`).

So: **the BFF already creates WorkflowRuns for builds.** This refactor adds a second kind of WorkflowRun (coding-agent), reusing the same client + watcher patterns.

---

## 2. Design space

### Design A — ClusterWorkflow + WorkflowRun (Argo on WorkflowPlane)

BFF creates a `WorkflowRun` of `ClusterWorkflow: app-factory-coding-agent`. Argo schedules a pod on the WorkflowPlane. Pod runs the same Claude Agent SDK code (image repurposed as a one-shot, not an HTTP server). On completion, pod exits; Argo marks the WorkflowRun `Succeeded`/`Failed`. BFF polls WorkflowRun status (mirrors existing build watcher).

**Pros:**
- Mirrors Agent-Manager's `amp-monitor-evaluation` ClusterWorkflowTemplate pattern exactly.
- Aligns with the broader migration plan v6.1.
- Per-task pod isolation (resources, failures, blast radius).
- Free retry, TTL, status from Argo.
- Reuses BFF's existing build-WorkflowRun client and watcher patterns.
- Status visible via OC REST (debugging, dashboards).

**Cons:**
- Per-task pod startup latency (~30–60s). Coding tasks already take 5–30 min, so the relative overhead is small.
- Adds Argo template + ClusterWorkflow + runner image to maintain.
- Workflow pod is in WorkflowPlane; needs network reachability to git-service (cluster-internal DNS may not work cross-plane — see §6.4).

### Design B — K8s Job per task (no Argo)

BFF creates a Kubernetes `Job` directly per task. Same image. No WorkflowPlane involvement.

**Pros:**
- No Argo dependency.
- Per-task pod isolation.
- Standard K8s primitive.

**Cons:**
- Doesn't match Agent-Manager pattern; doesn't align with broader migration.
- Status is observable but not via OC REST — BFF must list/watch Jobs directly.
- Reimplementing what Argo gives free (retry policy, TTL strategy, parameter substitution).
- BFF gains direct k8s client dependency for Jobs (currently it only uses OC REST).

### Design C — Horizontal remote-worker (status quo, scaled out)

Multiple `app-factory-remote-worker` pods. BFF distributes work via consistent hashing or queue. Each pod still runs in-process agent sessions.

**Pros:**
- Smallest change.
- Lowest latency (warm pods).

**Cons:**
- Still no per-task isolation (one pod handles many agents).
- One pod crash kills all its in-flight agents.
- Concurrency control still bespoke.
- Doesn't move toward the broader migration plan.
- Doesn't solve the user-visible "stuck in_progress" failure mode any better than today.

### Design D — In-cluster orchestrator pod that spawns child Pods per task

Remote-worker becomes a controller: receives BFF dispatch, spawns a child Pod per task, watches and manages.

**Pros:**
- Per-task isolation without Argo.

**Cons:**
- We're reimplementing what Argo does.
- Custom controller code to maintain.
- Doesn't align with canonical pattern.

---

## 3. Recommendation: Design A, with sub-decisions

**Recommendation: Design A — ClusterWorkflow + WorkflowRun.** Sub-decisions below.

### 3.1 Runner image — repurpose the existing `app-factory-remote-worker` image

Take `lab-app-factory:remote-worker/Dockerfile` (Alpine + Node 22, pre-installs git, github-cli, curl, python3, bash, runs as non-root user `asdlc`), strip the HTTP server (`src/index.ts` + `src/routes/`), and make the entrypoint a one-shot script:

```typescript
// src/oneshot.ts (new entrypoint)
const params = readParamsFromEnv();      // ASDLC_TASK_ID, ASDLC_ORG_ID, ..., ASDLC_PROMPT
await provisionWorkspace(params);         // existing src/lib/workspace.ts logic
const result = await runAgent(params);    // existing src/lib/runner.ts logic
process.exit(result.exitCode);
```

The dispatch HTTP handler (`src/routes/dispatch.ts`) becomes dead code — kept in the repo behind a build flag during cutover, deleted after.

Image published as `ghcr.io/wso2/app-factory-coding-agent-runner:{tag}`. Tag policy: same as today's app-factory-remote-worker.

### 3.2 Per-task auth — keep RS256 task JWT, no changes

BFF mints the JWT exactly as today (`taskTokenManager`). Argo passes it as a parameter; the runner pod reads it from env (`ASDLC_BEARER`). The pod writes it to `specs/bearer` (chmod 600) for the `gh` wrapper and credential helper to consume. Same flow, same key, same expiry.

### 3.3 GitHub credentials — keep `/credentials/refresh` against git-service

Runner pod calls `git-service:/credentials/refresh` exactly as today. Network reachability is the open question (§6.4); behavior is unchanged.

### 3.4 PR creation — stays in BFF dispatch path, before WorkflowRun

The BFF's existing dispatch sequence (Issue → Branch → seed commit → draft PR → ensure Component) runs **before** the WorkflowRun is created. The runner pod inherits a workspace where the branch is already pushed and the draft PR is already open. Agent commits code → `gh pr ready` → GitHub webhook → BFF transitions `in_progress → ready_for_review`. **No worker callback to BFF.** This matches today's behavior 1:1.

### 3.5 Status mapping — minimal new event, watcher mirrors build watcher

BFF adds a coding-agent watcher symmetric to the existing build watcher in `WorkflowRunService`. It polls WorkflowRuns labeled `app-factory.openchoreo.dev/coding-agent-task: {taskId}` (separate from build runs). Status mapping for v1:

| WorkflowRun condition | Existing task status | Action |
|---|---|---|
| `WorkflowRunning=true` | `in_progress` | no-op (matches dispatch transition) |
| `WorkflowCompleted reason=Succeeded` | `in_progress` (still — waiting for `gh pr ready` webhook to flip to `ready_for_review`) | no-op; record completion in `WorkflowRunRef` |
| `WorkflowCompleted reason=Failed/Error` | `in_progress` | **v1: log + metric, do NOT transition.** (Matches today's "stuck in_progress on agent crash" behavior. Adding a `TaskEventCodingAgentFailed` is a follow-up — explicitly out of scope to preserve functional parity.) |

The watcher persists `WorkflowRunRef` rows (mirrors `LastBuildRunName` pattern but for kind=`coding-agent`).

### 3.6 Concurrency — drop the in-process semaphore; rely on Argo + cluster

Today's `MAX_CONCURRENT=8` (in remote-worker) becomes irrelevant — there's no shared pod. Per-tenant or per-project quotas are out of scope (broader migration Phase 2/Phase 5). For v1, no quota: each WorkflowRun creates a pod, scheduled by k8s. If runaway is a concern in dev, add an Argo `parallelism` cap on the WorkflowRun spec or a cluster ResourceQuota — both deferred.

### 3.7 Tenancy / namespace — stay in current namespace (`asdlc-user-projects`)

For v1, WorkflowRuns land in the same namespace where build WorkflowRuns land today (`asdlc-user-projects`, per current `PLATFORM_API_NAMESPACE_OVERRIDE`). Per-tenant `aft-{tenantSlug}` namespaces are migration plan Phase 2 — explicitly out of scope. This decouples this refactor from the tenancy work.

### 3.8 Workflow definition location — `wso2cloud-deployment@local-app-factory`

For v1, the new `ClusterWorkflow` + `ClusterWorkflowTemplate` land in the existing `local-app-factory` branch. When the broader migration's Phase 1 lands (`wso2-app-factory-platform-resources-extension` Helm chart), they migrate there. No need to introduce the chart machinery just for this refactor.

### 3.9 Cutover — straight replace, no feature flag

App Factory is pre-production today. Real customers don't depend on this path. The v2 plan's feature flag + dual-image CI + 2-week burn-in were imported from production-migration habit and aren't justified here.

**This refactor replaces `RemoteWorkerService` in one atomic BFF change** (PR 3 in §5). The dispatch callsite changes from `remoteWorkerService.Dispatch(ctx, task)` to `workflowRunService.TriggerCodingAgent(ctx, task)`. The same PR deletes `services/remote_worker_service.go`, `clients/remoteworker/`, and the `app-factory-remote-worker` Component + ReleaseBindings in `local-app-factory`.

**Rollback** = `git revert` of PR 3. The dev cluster picks up the previous Component + ReleaseBinding via Flux reconcile; BFF redeploys with the previous dispatch path; in-flight tasks finish on whichever path created them (dispatch is idempotent on the persisted columns from §1).

**No in-flight task migration concern.** Dispatch is idempotent on `DispatchedAt`, `BranchName`, `IssueNumber`, `PullRequestNumber`. A task already dispatched to remote-worker before PR 3 lands stays in_progress; its status will continue to flow via GitHub webhooks (`pull_request:ready_for_review`, etc.) regardless of which code path created the workspace.

If/when App Factory has real external customers and we need true zero-downtime cutover semantics, that's a separate concern handled at the broader migration plan level — not at this single-refactor level.

### 3.10 Retry semantics — two distinct layers

Two retry concepts apply, at different layers:

- **Argo's built-in `retryStrategy`** (embedded in the runner template: `limit: 2, retryPolicy: OnError, backoff: {duration: 30s, factor: 2, maxDuration: 5m}`) covers transient pod-level failures: scheduling errors, image-pull failures, node evictions. Argo retries within the same WorkflowRun; the BFF doesn't see these.
- **App-Factory-level task retry** (operator abandons + re-dispatches a task) is unchanged from today. Each re-dispatch creates a new WorkflowRun with attempt+1, persisted in `WorkflowRunRef.attempt`. The BFF's existing dispatch idempotency contract (state per persisted columns) holds.

---

## 4. Target architecture (per-task data flow)

```
┌─────────────────────────────────────────────────────────────────┐
│ BFF dispatch path (asdlc-service)                                │
│                                                                  │
│  Task pending                                                    │
│    │                                                             │
│    ▼                                                             │
│  Verify Issue → Create branch → Seed commit → Create draft PR   │
│    → Ensure OC Component (AutoBuild=false)                      │
│    → Mint task JWT                                               │
│    │                                                             │
│    ▼                                                             │
│  if USE_WORKFLOW_CODING_AGENT:                                   │
│    WorkflowRunService.TriggerCodingAgent(task)                   │
│      → POST /api/v1/namespaces/asdlc-user-projects/workflowruns │
│        body: {                                                   │
│          metadata.name: coding-agent-{taskId}-{attempt}-{ts},   │
│          metadata.labels: {                                      │
│            app-factory.openchoreo.dev/coding-agent-task: ...,   │
│            openchoreo.dev/component: ...,                        │
│            openchoreo.dev/project: ...                           │
│          },                                                      │
│          spec.workflow: {                                        │
│            kind: ClusterWorkflow,                                │
│            name: app-factory-coding-agent,                       │
│            parameters: {                                         │
│              task: {id, branchName, prompt, ...},               │
│              repository: {url, identity: {...}},                │
│              bff: {bearer},                                      │
│              gitService: {url}                                   │
│            }                                                     │
│          },                                                      │
│          spec.ttlAfterCompletion: "24h"                          │
│        }                                                         │
│  else:                                                           │
│    RemoteWorkerService.Dispatch(task)  // legacy, unchanged     │
│                                                                  │
│  Task → in_progress; persist DispatchedAt                        │
└─────────────────────────────────────────────────────────────────┘
                                │
                                ▼
┌─────────────────────────────────────────────────────────────────┐
│ OC WorkflowRun controller                                         │
│   Looks up ClusterWorkflow `app-factory-coding-agent`            │
│   Renders runTemplate (Argo Workflow) into WorkflowPlane          │
└─────────────────────────────────────────────────────────────────┘
                                │
                                ▼
┌─────────────────────────────────────────────────────────────────┐
│ Argo Workflow on WorkflowPlane                                    │
│                                                                  │
│  Pod: ghcr.io/wso2/app-factory-coding-agent-runner:tag           │
│   env: {                                                         │
│     ASDLC_TASK_ID, ASDLC_ORG_ID, ASDLC_PROJECT_ID,              │
│     ASDLC_COMPONENT_NAME, ASDLC_BRANCH_NAME,                    │
│     ASDLC_REPO_URL, ASDLC_BEARER, ASDLC_GIT_SERVICE_URL,        │
│     ASDLC_PROMPT, ASDLC_IDENTITY_NAME, ASDLC_IDENTITY_EMAIL,    │
│     ASDLC_IDENTITY_LOGIN                                         │
│   }                                                              │
│   volumeMounts: workspace (emptyDir)                             │
│                                                                  │
│   1. provisionWorkspace() — clone (PAT from /credentials/refresh)│
│      writes specs/{bearer, credhelper.sh, gh}                   │
│   2. runAgent() — Claude Agent SDK query()                       │
│      tools: Read, Write, Edit, Bash, Glob, Grep                  │
│      cwd: workspace                                              │
│   3. Agent commits, pushes, runs `gh pr ready`                   │
│   4. Process exits 0 (success) or non-zero (failure)             │
└─────────────────────────────────────────────────────────────────┘
                                │
                  ┌─────────────┴────────────┐
                  ▼                          ▼
┌────────────────────────────┐    ┌──────────────────────────┐
│ GitHub                       │    │ BFF coding-agent watcher │
│   pull_request:              │    │   polls WorkflowRuns     │
│     ready_for_review →       │    │   by label,              │
│     webhook to BFF →         │    │   updates                │
│     task → ready_for_review  │    │   WorkflowRunRef.phase   │
│                              │    │   (failure: log+metric,  │
│                              │    │    no task transition)   │
└────────────────────────────┘    └──────────────────────────┘
```

---

## 5. Implementation plan

Three PRs land in sequence. Total: ~1.5–2 person-weeks engineering, ~2 weeks elapsed.

### PR 1 — Define ClusterWorkflow + ClusterWorkflowTemplate (1–2 days)

Add to `wso2cloud-deployment@local-app-factory:wso2cloud-local/`:

```yaml
# domains/platform/cluster-shared/cluster-workflows/app-factory-coding-agent.yaml
apiVersion: openchoreo.dev/v1alpha1
kind: ClusterWorkflow
metadata:
  name: app-factory-coding-agent
spec:
  workflowPlaneRef:
    kind: ClusterWorkflowPlane
    name: default
  ttlAfterCompletion: "24h"
  parameters:
    openAPIV3Schema:
      type: object
      required: [task, repository, bff, gitService]
      properties:
        task:
          type: object
          required: [id, orgId, projectId, componentName, branchName, prompt]
          properties:
            id:            { type: string }
            orgId:         { type: string }
            projectId:     { type: string }
            componentName: { type: string }
            branchName:    { type: string }
            prompt:        { type: string }
        repository:
          type: object
          required: [url, identity]
          properties:
            url: { type: string }
            identity:
              type: object
              required: [name, email, login]
              properties:
                name:  { type: string }
                email: { type: string }
                login: { type: string }
        bff:
          type: object
          required: [bearer]
          properties:
            bearer: { type: string }   # RS256 task JWT, 24h
        gitService:
          type: object
          required: [url]
          properties:
            url: { type: string }
  runTemplate:
    apiVersion: argoproj.io/v1alpha1
    kind: Workflow
    spec:
      activeDeadlineSeconds: 3600
      ttlStrategy:
        secondsAfterCompletion: 86400
      arguments:
        parameters:
          - { name: task-id,         value: "${parameters.task.id}" }
          - { name: org-id,          value: "${parameters.task.orgId}" }
          - { name: project-id,      value: "${parameters.task.projectId}" }
          - { name: component-name,  value: "${parameters.task.componentName}" }
          - { name: branch-name,     value: "${parameters.task.branchName}" }
          - { name: prompt,          value: "${parameters.task.prompt}" }
          - { name: repo-url,        value: "${parameters.repository.url}" }
          - { name: identity-name,   value: "${parameters.repository.identity.name}" }
          - { name: identity-email,  value: "${parameters.repository.identity.email}" }
          - { name: identity-login,  value: "${parameters.repository.identity.login}" }
          - { name: bearer,          value: "${parameters.bff.bearer}" }
          - { name: git-service-url, value: "${parameters.gitService.url}" }
      entrypoint: run-agent
      templates:
        - name: run-agent
          retryStrategy:
            limit: 2
            retryPolicy: OnError
            backoff:
              duration: "30s"
              factor: 2
              maxDuration: "5m"
          container:
            # Image name `app-factory-coding-agent-runner` is intentionally distinct
            # from the legacy `app-factory-remote-worker` image. PR 2 publishes the
            # new image; the legacy Deployment continues pulling its existing image
            # name and is not affected. Round-2 review fix.
            #
            # Tag pinned literally here; this YAML lives in a flat kustomize tree
            # (wso2cloud-deployment@local-app-factory:.../cluster-shared/cluster-workflows/),
            # NOT a Helm chart. Pattern matches existing ClusterWorkflows in the same dir
            # (ballerina-buildpack-builder.yaml, dockerfile-builder.yaml, etc.) which all
            # use literal tags. When the broader migration's `wso2-app-factory-platform-resources-extension`
            # Helm chart is introduced (oc-native-migration.md Phase 1), this becomes
            # `image: {{ .Values.runner.image }}:{{ .Values.runner.tag }}`.
            #
            # Tag policy during this refactor: use `dev`, `stage`, `prod` per environment overlay
            # (kustomize `images:` transformer in the env-specific kustomization.yaml).
            image: ghcr.io/wso2/app-factory-coding-agent-runner:dev
            imagePullPolicy: IfNotPresent
            env:
              - { name: ASDLC_TASK_ID,         value: '{{`{{workflow.parameters.task-id}}`}}' }
              - { name: ASDLC_ORG_ID,          value: '{{`{{workflow.parameters.org-id}}`}}' }
              - { name: ASDLC_PROJECT_ID,      value: '{{`{{workflow.parameters.project-id}}`}}' }
              - { name: ASDLC_COMPONENT_NAME,  value: '{{`{{workflow.parameters.component-name}}`}}' }
              - { name: ASDLC_BRANCH_NAME,     value: '{{`{{workflow.parameters.branch-name}}`}}' }
              - { name: ASDLC_REPO_URL,        value: '{{`{{workflow.parameters.repo-url}}`}}' }
              - { name: ASDLC_IDENTITY_NAME,   value: '{{`{{workflow.parameters.identity-name}}`}}' }
              - { name: ASDLC_IDENTITY_EMAIL,  value: '{{`{{workflow.parameters.identity-email}}`}}' }
              - { name: ASDLC_IDENTITY_LOGIN,  value: '{{`{{workflow.parameters.identity-login}}`}}' }
              - { name: ASDLC_GIT_SERVICE_URL, value: '{{`{{workflow.parameters.git-service-url}}`}}' }
              - { name: ASDLC_PROMPT,          value: '{{`{{workflow.parameters.prompt}}`}}' }
              - { name: ASDLC_BEARER,          value: '{{`{{workflow.parameters.bearer}}`}}' }
            resources:
              requests: { cpu: "200m",  memory: "512Mi" }
              limits:   { cpu: "2",     memory: "4Gi"   }
            securityContext:
              runAsNonRoot: true
              runAsUser: 1000
            volumeMounts:
              - { name: workspace, mountPath: /home/asdlc/workspace }
          volumes:
            - { name: workspace, emptyDir: {} }
```

Verify against existing `agent-manager:wso2-amp-evaluation-extension/templates/workflow-templates/cluster-workflow-monitor-evaluation.yaml` for shape consistency.

**Done when:** `kubectl apply` against dev cluster succeeds; `kubectl get clusterworkflows app-factory-coding-agent` returns the resource (no Components reference it yet — that lands in PR 3).

### PR 2 — Add one-shot runner image under a NEW image name (3–5 days)

The new one-shot image ships under a **separate name** (`app-factory-coding-agent-runner`), not under the existing `app-factory-remote-worker` name. **Round-2 critique fix:** if we reused the existing image name, the existing remote-worker Deployment in dev would pull the retagged image on its next pod restart and crash (one-shot exits immediately) before PR 3 lands. Two image names eliminate the hazard.

In `lab-app-factory:remote-worker/`:

1. Add `src/oneshot.ts` as the one-shot entrypoint:
   - Reads `ASDLC_*` env vars.
   - Calls existing `provisionWorkspace()` (`src/lib/workspace.ts`) and `runAgent()` (`src/lib/runner.ts`) — unchanged.
   - Exits with the agent's exit code.
2. Add `Dockerfile.runner` (separate from existing `Dockerfile`) with `ENTRYPOINT ["npx", "tsx", "src/oneshot.ts"]`. Existing `Dockerfile` (HTTP-server entrypoint) is unchanged.
3. **CI publishes both images during the PR-2-to-PR-3 window**:
   - `ghcr.io/wso2/app-factory-remote-worker:{tag}` — unchanged, built from existing `Dockerfile`. Existing Deployment continues to pull and run this.
   - `ghcr.io/wso2/app-factory-coding-agent-runner:{tag}` — new, built from `Dockerfile.runner`. Referenced only by the ClusterWorkflow from PR 1.
4. **Smoke-test the runner image locally before opening the PR:**
   ```bash
   # Set up a real test repo with an open issue and a draft PR; export the
   # task-scoped JWT issued by the BFF dev instance.
   docker run --rm \
     -e ASDLC_TASK_ID=test-task-id \
     -e ASDLC_ORG_ID=test-org \
     # ...all ASDLC_* env vars from §4 diagram
     ghcr.io/wso2/app-factory-coding-agent-runner:dev
   ```
   Verify:
   - Container exits 0 on success, non-zero on induced agent crash.
   - No lingering processes (workspace cleaned up; emptyDir scope honoured).
   - `gh` wrapper functions identically to today's HTTP-server context — specifically: agent runs `gh pr ready` against the test PR successfully and the PR transitions out of draft state on GitHub.
   - `git-service:/credentials/refresh` is reachable and returns a fresh PAT for the clone + push.

**No changes** to `src/lib/workspace.ts`, `src/lib/runner.ts`, the `gh` wrapper, the credential helper, or the agent prompt logic. They run in a different container shape but with identical inputs and behavior.

**Done when:** new image published; smoke test passes including `gh pr ready`; existing `app-factory-remote-worker` Deployment in dev keeps running unaffected (different image name → no retag, no rolling restart).

### PR 3 — Cutover (BFF dispatch swap + GitOps Component deletion, 3–5 days)

The cutover spans two repos, so it cannot be a single PR. Round-2 critique correctly flagged that calling this "atomic" was misleading. v3.1 splits cleanly into **PR 3a** (`lab-app-factory`) and **PR 3b** (`wso2cloud-deployment@local-app-factory`), with **explicit merge ordering** to avoid ordering hazards:

**Merge order: PR 3a first, deploy, then PR 3b.** Rationale below.

#### PR 3a — BFF + lab-app-factory cleanup (merge first)

**BFF code changes** (`asdlc-service`):

1. **Add `clients/openchoreo/component_client.go:TriggerCodingAgent(ctx, taskCtx CodingAgentParams)`** mirroring existing `TriggerBuildAtCommit`:
   - Workflow ref: `{kind: ClusterWorkflow, name: app-factory-coding-agent}`.
   - Parameters: shape from §5 PR 1 schema.
   - Labels: `app-factory.openchoreo.dev/coding-agent-task: {taskId}` plus existing `openchoreo.dev/component`, `openchoreo.dev/project`.
   - Run name: `coding-agent-{taskId}-{attempt}-{unixMillis}`.

2. **Add `services/workflowrun_service.go:TriggerCodingAgent(ctx, task ComponentTask)`** — builds params from the same inputs `RemoteWorkerService.Dispatch` builds today, calls `componentClient.TriggerCodingAgent`, persists `WorkflowRunRef{taskId, kind: "coding-agent", ocName, attempt, phase: Pending}`.

3. **Replace dispatch callsite**: the BFF function that today calls `remoteWorkerService.Dispatch(ctx, task)` directly calls `workflowRunService.TriggerCodingAgent(ctx, task)`. **No feature flag**, no branching.

4. **Add coding-agent watcher**: extend the existing build-status watcher in `services/webhook/build_watcher.go` (or add a parallel one) — polls WorkflowRuns by label `app-factory.openchoreo.dev/coding-agent-task` at the same 10s cadence; updates `WorkflowRunRef.phase`; on terminal failure logs + emits metric (no task state transition — see §3.5).

5. **Delete:**
   - `lab-app-factory:remote-worker/src/index.ts`, `src/routes/`, `src/lib/server*.ts` (and any HTTP-server-specific code).
   - `lab-app-factory:asdlc-service/services/remote_worker_service.go`.
   - `lab-app-factory:asdlc-service/clients/remoteworker/`.
   - The `RemoteWorkerService` constructor wiring in `cmd/asdlc-api/main.go`.
   - Config keys `REMOTE_WORKER_BASE_URL` from `.env.example`.
   - Existing `Dockerfile` (HTTP-server entrypoint); `Dockerfile.runner` from PR 2 becomes the only Dockerfile.
   - The CI job that publishes `ghcr.io/wso2/app-factory-remote-worker` (only the runner image continues to be published).

**Setup script changes** (`lab-app-factory:deployments-v2/scripts/lib/asdlc.sh`):

6. Remove `asdlc-bff-to-remote-worker` and `asdlc-bff-to-remote-worker-secret` from the Thunder OAuth client registration loop.

**Doc updates:**

7. Update `CLAUDE.md`, `AGENTS.md`, `docs/design/architecture.md` to remove remote-worker references.

**Code-review gate**: `git grep -i RemoteWorkerService` and `git grep -i "remote-worker"` (excluding this design doc and the changelog) must return zero matches in `lab-app-factory` after PR 3a.

**Effect after PR 3a merge + BFF redeploy**: BFF stops POSTing to remote-worker. The existing `app-factory-remote-worker` Deployment in dev keeps running but is now idle (no incoming dispatches). It's harmless (just consuming resources) until PR 3b deletes it.

#### PR 3b — GitOps cleanup in wso2cloud-deployment (merge after PR 3a deploys)

After PR 3a is merged, the dev cluster has been picking up the new BFF (BFF dispatches via WorkflowRun), and the orphaned `app-factory-remote-worker` Deployment is idle. Now safe to delete:

8. Delete `wso2cloud-local/domains/developers/namespaces/wso2cloud/projects/app-factory/components/app-factory-remote-worker/` (component.yaml + kustomization.yaml).
9. Delete `wso2cloud-local/domains/platform/namespaces/wso2cloud/release-bindings/app-factory/app-factory-remote-worker/` if present.
10. Remove `app-factory-remote-worker` from the parent `kustomization.yaml`.

**Effect after PR 3b merge + Flux reconcile**: orphaned Deployment + Service + ReleaseBinding are deleted from dev cluster.

#### Why PR 3a before PR 3b (the merge ordering rationale)

If PR 3b were merged first, Flux would delete the remote-worker Deployment **while BFF is still POSTing to `/dispatch`**. New task dispatches would fail with connection-refused until BFF redeploys (minutes-to-hours window). Reverse ordering (PR 3a first) means the worst case during the merge gap is "old Deployment idle and consuming resources" — nothing breaks.

The window between PR 3a and PR 3b is intentionally short (one engineer's working session) but not strictly time-bounded. A few hours is fine.

**Validation in dev** (gated by §5.1 prerequisites):

- After PR 1 + PR 2 are merged and Flux has reconciled, ClusterWorkflow + new runner image are deployed; existing remote-worker Deployment is unchanged.
- Merge PR 3a; wait for BFF redeploy.
- Dispatch a test task; verify the end-to-end flow listed in §5.2 below.
- Once §5.2 passes, merge PR 3b; verify Flux deletes the orphaned Deployment.
- Iterate fixes if needed (small follow-up PRs, not a coexistence period).

**Rollback**:
- If a problem surfaces between PR 3a and PR 3b: `git revert PR 3a`, BFF redeploys to the prior dispatch path, the still-deployed remote-worker Deployment picks up dispatches again. Recovery time = BFF deploy time.
- If a problem surfaces after PR 3b: `git revert PR 3b` first (Flux re-creates the Deployment), then `git revert PR 3a` (BFF re-deploys to old dispatch path). Recovery time = Flux reconcile + BFF deploy. Acceptable on dev cluster.

### 5.1 Prerequisites that must be answered BEFORE merging PR 3a

- **Cross-plane network reachability.** WSO2 Cloud Platform has confirmed the canonical pattern for a workflow pod on WorkflowPlane to reach `app-factory-git-service` on DataPlane. If the URL differs from `app-factory-git-service.wso2cloud.svc.cluster.local:3300`, BFF passes that URL in the `gitService.url` parameter.
- **Image registry access from WorkflowPlane.** Verified imagePullSecrets / cluster credentials are in place for the runner image's registry.
- **WorkflowPlane RBAC.** Confirmed whether tenant users can read `WorkflowRun.spec.workflow.parameters` (which would expose the bearer JWT). If yes, do the per-task Secret follow-up (§6.8) BEFORE merging PR 3a to prod-equivalent envs (still acceptable in dev for v1).
- **Namespace placement.** Confirmed `asdlc-user-projects` is acceptable for non-build coding-agent WorkflowRuns alongside today's build WorkflowRuns.

### 5.2 Validation criteria in dev (after PR 3a merges and BFF redeploys)

- `kubectl get workflowruns -n asdlc-user-projects -l app-factory.openchoreo.dev/coding-agent-task` shows the run for a freshly dispatched task.
- Argo pod created on WorkflowPlane; resolves and reaches `app-factory-git-service` for `/credentials/refresh`.
- Pod runs the agent; commits; pushes; runs `gh pr ready`.
- GitHub webhook fires; BFF transitions task to `ready_for_review`.
- WorkflowRun terminates `Succeeded`; `WorkflowRunRef.phase` updated by the watcher.
- Failure path: induce an agent crash; pod exits non-zero; Argo marks `Failed`; task stays `in_progress` (functional parity preserved); metric emitted; `WorkflowRunRef.phase` reflects `Failed`.

### 5.3 Promotion to other environments (if/when they exist)

App Factory currently lives only on `local-app-factory`. When stage/prod environments exist (post broader-migration Phase 1), the same PR-1 / PR-2 / PR-3a / PR-3b sequence lands on the canonical branches with the same merge ordering. No interim coexistence; just promote each merged change in the same order.

---

## 6. Open questions / prerequisites

These need answers before specific phases ship. None block Phase 1–3 (which can proceed against documented assumptions).

1. **§3.4 / Phase 4 — WorkflowPlane → BFF/git-service network reachability.** In `local-app-factory`'s k3d setup, WorkflowPlane and DataPlane are the same cluster, and in-cluster DNS works. In multi-cluster prod (separate WorkflowPlane), can a workflow pod resolve `app-factory-git-service.wso2cloud.svc.cluster.local:3300` (and `app-factory-api.wso2cloud:8080` if needed for callback in future iterations)? If not, what's the canonical pattern — service mesh, gateway URL, mTLS proxy?
2. **§3.7 — Namespace placement.** WorkflowRuns today land in the BFF's "OC project namespace" (which is currently `asdlc-user-projects` via `PLATFORM_API_NAMESPACE_OVERRIDE`). Verify with WSO2 Cloud Platform that creating WorkflowRuns in this namespace is acceptable for non-build workloads.
3. **§5 Phase 1 — `${parameters.bff.bearer}` substitution.** The bearer is sensitive; passing it as a plain workflow parameter means it appears in `WorkflowRun.spec.workflow.parameters` (visible to anyone with read on WorkflowRuns) and in Argo Workflow `arguments.parameters` (visible to anyone with read on Argo Workflows in the WorkflowPlane). For this v1, accept the exposure (matches today's HTTP POST exposure inside the cluster). Long-term: use `valueFrom.secretKeyRef` and have BFF write the JWT to a per-task Secret with TTL, referenced by name. Out of scope for v1.
4. **§5 Phase 1 — Image registry access from WorkflowPlane.** Verify the WorkflowPlane has imagePullSecrets / cluster-level registry credentials for `ghcr.io/wso2/...` (or wherever the runner image is pushed).
5. **§3.6 — Argo `parallelism` cap.** None set in v1. If the BFF dispatches 50 tasks in 5 seconds, Argo will spawn 50 pods. Acceptable for dev; verify with WSO2 Cloud Platform whether stage/prod WorkflowPlane needs an explicit cap (ResourceQuota or Argo workflow-controller config).
6. **`gh pr ready` semantics on the workflow pod.** Verify `gh` wrapper + credential helper continue to work identically when the pod runs as a one-shot Argo container vs as a request handler in remote-worker. Most likely identical (same image, same wrapper), but worth Phase 4 testing.
7. **Failure detection (out of scope for v1; explicit follow-up ticket required).** Today: agent crash → task stuck `in_progress`. After this refactor: same behavior. A follow-up — to be filed as a ticket BEFORE this refactor's Phase 6 cleanup — should add a `TaskEventCodingAgentFailed` event triggered when WorkflowRun terminates failed AND no commit was pushed to the branch (heuristic: check via git-service whether the branch has new commits since dispatch). Explicitly out of scope here to maintain the functional parity contract, but the WorkflowRun terminal-failure signal we now have is strictly more information than today, and turning it into a state transition is the obvious next iteration.

8. **Bearer JWT in WorkflowRun parameters — v1 risk acceptance.** Round-1 critique flagged that the bearer JWT (RS256, 24h) is passed as a plain ClusterWorkflow parameter and ends up in `WorkflowRun.spec.workflow.parameters`, in the rendered Argo Workflow's `spec.arguments.parameters`, and in the pod's env. Anyone with read access on either CR can see it. **For v1, this matches today's exposure** (HTTP POST inside the cluster sends the JWT in the request body; anyone able to sniff/intercept that traffic sees it). The risk is bounded by:
   - WorkflowPlane RBAC restricting `get/list` on `workflowruns` and `workflows.argoproj.io` to operators only (verified in Phase 4 prereq).
   - JWT TTL = 24h; pod TTL = 1h (`activeDeadlineSeconds: 3600`); blast radius is one task's git-service `/credentials/refresh` calls.
   - **Mandatory follow-up** (filed as a ticket before Phase 6 cleanup): replace bearer parameter with `valueFrom.secretKeyRef`. BFF writes the JWT to a per-task `Secret` (TTL 24h, GC by name match) before WorkflowRun creation; ClusterWorkflow references the Secret via `secretKeyRef`. Out of scope for this refactor's v1.

9. **WorkflowRun labels and OC `allowedWorkflows` validation.** OC's WorkflowRun controller validates the workflow against the target Component's `ClusterComponentType.allowedWorkflows` list **whenever the run carries the `openchoreo.dev/component` label**. The user's app component is `ClusterComponentType: deployment/service`, whose allowed list is the four builder ClusterWorkflows — `app-factory-coding-agent` is not on it. The run gets rejected with `ComponentValidationFailed` before any pod is scheduled.

    **v1 picked Option A — drop the `openchoreo.dev/component` / `openchoreo.dev/project` labels on coding-agent WorkflowRuns** (use sibling `app-factory.openchoreo.dev/component`/`project` labels instead). The agent run is a per-task platform action that doesn't produce build artifacts for the component, doesn't deploy it, and isn't queried via OC's "list runs for component X" surface — the BFF tracks it via `ComponentTask.LastCodingAgentRunName` directly. Smallest change; honest about semantics. Cost: tooling that filters WorkflowRuns by `openchoreo.dev/component` won't see these runs (use the af-prefixed label).

    **Option B — add `app-factory-coding-agent` to each user-facing ClusterComponentType's `allowedWorkflows`** (`deployment/service`, `worker`, `web-application`, `scheduled-task`, …). Keeps the OC-native label semantics. Cost: those resources live in `wso2cloud-deployment` (upstream-owned), so we'd be editing platform GitOps for every supported ComponentType, and the semantics is misleading — the coding-agent isn't a workflow that "operates on" the component the way builders do. Considered and deferred; we may switch to it later if cross-tooling visibility under `openchoreo.dev/component` becomes important.

---

## 7. What's intentionally out of scope

These are part of the broader migration plan (`docs/design/oc-native-migration.md`) and explicitly NOT in this refactor:

- Per-tenant OC Namespaces (`aft-{tenantSlug}`) — Migration plan Phase 2.
- Replacing `RemoteWorkerService.Dispatch` callsites elsewhere (e.g., re-dispatch on PR rejection, manual retries) — covered transparently by the feature flag.
- Per-tenant SecretReference for `github-credentials` — Migration plan Phase 4.
- Custom `ComponentType: app-factory-generated-service` — Migration plan Phase 1 / Phase 6.
- Moving the App Factory Project from `local-app-factory` branch to canonical `wso2cloud-deployment@main` — Migration plan Phase 1.
- Helm chart packaging (`wso2-app-factory-platform-resources-extension`) — Migration plan Phase 1. The ClusterWorkflow added in this refactor is a flat YAML in `local-app-factory`; it migrates into the chart later.
- OTEL trait attachment to App Factory's own services — Migration plan Phase 5.
- BFF→OC M2M client migration from shared `openchoreo-system-app` to `APP_FACTORY_BFF` — Migration plan Phase 1. WorkflowRun creation uses today's existing OC client (with today's existing M2M client); no new OAuth client needed for this refactor.

---

## 8. Risks and mitigations

| Risk | Mitigation |
|---|---|
| Per-task pod startup latency (~30–60s) added to every dispatch | Acceptable — coding tasks already run 5–30 minutes. Document in user-facing docs if it causes noticeable UX change. Future: Argo pod warm-pool. |
| Workflow pod cannot reach git-service (cross-cluster DNS) | Verify in Phase 4 dev test. If broken, fall back to flag=false in stage/prod and resolve §6.1 before proceeding. |
| Bearer JWT visible in WorkflowRun spec | Document; acceptable risk (matches current in-cluster HTTP POST exposure). Long-term fix: per-task Secret. Out of scope for v1. |
| Burst dispatches cause Argo pod storm | Document and monitor; add Argo `parallelism` or ResourceQuota if observed. |
| Image build/publish for new runner image not in CI | Add to CI in Phase 2; can run alongside existing remote-worker image build. |
| In-flight tasks at the moment of PR 3a merge | Idempotent dispatch ensures safety; tasks dispatched pre-merge stay `in_progress` and continue receiving GitHub-webhook-driven status transitions regardless of which code path created the workspace. |
| Rollback (post-cutover bug) | `git revert` PR 3b first (Flux re-creates Deployment), then PR 3a (BFF redeploys with prior dispatch path). ~5 min recovery on dev cluster. Acceptable because App Factory is pre-production. |
| **Image-tag mutability hazard (PR 2)** — if reused image name, retagged image gets pulled into existing Deployment causing CrashLoopBackOff | **v3.1 fix:** runner image uses a separate name (`app-factory-coding-agent-runner`), not `app-factory-remote-worker`. Existing Deployment is unaffected. CI publishes both images during the PR-2-to-PR-3a window. |
| **Multi-repo merge ordering hazard (PR 3 spans two repos)** — if GitOps Component deletion lands before BFF dispatch swap, BFF dispatches fail with connection-refused while the gap window stays open | **v3.1 fix:** PR 3 is split into PR 3a (BFF, lab-app-factory) and PR 3b (GitOps, wso2cloud-deployment), with explicit ordering: PR 3a first, BFF redeploys, then PR 3b. Worst case during the gap is "old Deployment idle and consuming resources" — nothing breaks. |
| Watcher hot loop wastes API quota | Match build watcher's cadence (10s). Use label selector to scope to coding-agent runs only. |
| WorkflowRun spec.parameters too large (prompt can be ~10KB) | Argo accepts reasonably-sized parameters. If prompt grows beyond a few hundred KB, switch to ConfigMap+volumeMount. v1 keeps the inline prompt. |

---

## 9. Sequencing

```
PR 1 (ClusterWorkflow + Template in GitOps@local-app-factory) ─┐
                                                               │
PR 2 (NEW one-shot runner image, separate name)  ──────────────┤
                                                               │
                                                               ▼
                                          §5.1 prereqs cleared
                                                               │
                                                               ▼
                                  PR 3a (lab-app-factory: BFF dispatch swap + source delete)
                                                               │
                                                               ▼
                                          BFF redeploys; validate §5.2 in dev
                                                               │
                                                               ▼
                                  PR 3b (wso2cloud-deployment@local-app-factory: delete Component)
                                                               │
                                                               ▼
                                          Flux reconciles; orphaned Deployment removed
                                                               │
                                                               ▼
                                  Promote (when stage/prod exist) by re-merging same PR sequence
```

Estimated total: ~1.5–2 person-weeks engineering, ~2 weeks elapsed. No burn-in calendar time.

---

## 10. Validation criteria

- **PR 1 done when:** ClusterWorkflow + ClusterWorkflowTemplate apply cleanly in dev cluster; `kubectl get clusterworkflows app-factory-coding-agent` returns the resource.
- **PR 2 done when:** new `app-factory-coding-agent-runner` image builds + pushes; smoke test passes locally including `gh pr ready` against a real test PR; existing `app-factory-remote-worker` Deployment in dev still running unaffected (different image name → no rolling restart).
- **PR 3a done when:** §5.2 validation criteria all green in dev; BFF dispatches via WorkflowRun; `git grep -i RemoteWorkerService` and `git grep -i "remote-worker"` (excluding this design doc and Changelog) return zero matches in `lab-app-factory`; `asdlc-bff-to-remote-worker` removed from `setup.sh`.
- **PR 3b done when:** Flux reconciliation of dev cluster shows the `app-factory-remote-worker` Deployment, Service, and ReleaseBinding deleted; `kubectl get components -n wso2cloud -l openchoreo.dev/project=app-factory` no longer lists `app-factory-remote-worker`.

---

## Changelog

- **v3.1 (2026-05-06)** — Round-2 closure on v3. Both critics flagged two concrete cutover hazards in v3's "atomic single-PR replace" framing:
  - **Image-tag mutability hazard.** v3 said the new one-shot runner published under the existing `app-factory-remote-worker` image name. On any pod restart of the existing Deployment between PR 2 and PR 3, the retagged image would be pulled and the one-shot would exit immediately, causing CrashLoopBackOff. **v3.1 fix:** runner image uses a separate name (`app-factory-coding-agent-runner`); existing Deployment is unaffected because it pulls a different image. CI publishes both during the gap; PR 3a deletes the old CI job.
  - **Multi-repo merge ordering hazard.** v3 called PR 3 "atomic" but it spans `lab-app-factory` + `wso2cloud-deployment` (two separate repos, two separate merges). v3.1 splits PR 3 into PR 3a (lab-app-factory) and PR 3b (wso2cloud-deployment) with explicit ordering: **PR 3a first**, BFF redeploys (now dispatching via WorkflowRun, ignoring the orphaned Deployment), validate in dev, **then PR 3b** deletes the orphaned Deployment. Worst case during the merge gap is "old Deployment idle and consuming resources" — nothing breaks. Reverse ordering would cause connection-refused dispatches.
  - **Smoke test (§5 PR 2) tightened** to require `gh pr ready` against a real test PR (round-2 nit).
  - **Code-review gate (§5 PR 3a)** explicitly requires `git grep RemoteWorkerService` and `git grep "remote-worker"` (excluding this doc) to return zero matches.
  - **§8 risks table:** added two rows for the v3.1 hazards above.
  - **§9 sequencing diagram + §10 validation criteria** reformatted around PR 3a / PR 3b split.
  - **§5.3 promotion** updated to note the same PR 1 → PR 2 → PR 3a → PR 3b ordering when promoting to other envs.
- **v3 (2026-05-06)** — Pushback on v2's production-style cutover machinery. App Factory is pre-production today; the v2 plan's feature flag, dual-image CI publish, and 2-week stage/prod burn-in were imported from production-migration habit and aren't justified here. **Major changes:**
  - **Dropped `USE_WORKFLOW_CODING_AGENT` feature flag** (§3.9 rewritten as straight-replace with `git revert` rollback).
  - **Phases 1–6 collapsed to PR 1–3** (§5 fully rewritten). Total scope drops from ~4–5 weeks elapsed (~2.5 person-weeks engineering) to ~2 weeks elapsed (~1.5–2 person-weeks engineering); calendar time savings come entirely from removing burn-in.
  - **Image keeps the same name** (`app-factory-remote-worker`) — it's a behavior swap (HTTP server → one-shot), not a new image. CI continues publishing under the existing tag. PR 3 deletes the legacy entrypoint code in the same atomic change as deleting the Component.
  - **Cutover happens in PR 3 atomically**: BFF dispatch swap, source deletion, GitOps Component deletion, Thunder client deletion, doc updates — all in one PR. Rollback is `git revert`.
  - **§5.1 prerequisites preserved** from v2's Phase 4 prereq list — these still gate PR 3 (cross-plane DNS, image registry, RBAC, namespace placement). Same content, repackaged.
  - **§5.3 promotion** noted: when stage/prod environments exist post broader-migration Phase 1, the same three PRs land on canonical branches. No interim coexistence; just promote merged change.
  - **§8 risks**: dropped "Two-image CI burden" (no longer applies — same image name); reframed "in-flight tasks during cutover" and "rollback" rows around `git revert` semantics.
  - **§9 sequencing diagram + §10 validation criteria** reformatted around PR 1–3.
- **v2 (2026-05-06)** — Round-1 OC Design and WSO2 Cloud Platform critique closure. Both: APPROVED CONDITIONAL on these items, all addressed:
  - **§5 Phase 1 image tag** — Helm `{{ .Values.runnerImageTag }}` replaced with literal `:dev` tag (matching pattern of existing ClusterWorkflows in the same kustomize tree). Comment added explaining future Helm-chart packaging path.
  - **§5 Phase 4 — Phase 4 prerequisite checklist added** for cross-plane DNS reachability, image registry access, WorkflowPlane RBAC for bearer exposure, and namespace placement sign-off. Phase 4 doesn't start until these clear.
  - **§5 Phase 5 branch target clarified** — App Factory currently only on `local-app-factory`; Phase 5 stays there until broader migration's Phase 1 promotes to canonical branches.
  - **§5 Phase 2 — smoke-test step added** for the one-shot container before Phase 4 dev integration test.
  - **§3.10 retry semantics reworded** for clarity (Argo retry vs app-level task retry as two distinct layers).
  - **§6 questions 7, 8 expanded** — failure detection deferred but mandatory follow-up ticket required before Phase 6 cleanup; bearer JWT exposure explicitly accepted as v1 risk with concrete TTL/blast-radius bounds and mandatory per-task Secret follow-up before Phase 6.
- v1 (2026-05-06) — Initial draft.
