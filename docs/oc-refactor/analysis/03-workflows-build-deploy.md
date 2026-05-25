# Workflow / Build / Deploy subsystem — code-grounded analysis

Scope: the coding-agent runner pod (`remote-worker/`), the workflow manifests in
`deployments/manifests/`, and the dispatch / build / deploy mechanics in
`asdlc-service/`. Cross-referenced against the verified Agent-Manager (AM) →
OpenChoreo (OC) reference model in `/Users/wso2/repos/agent-manager-analysis/`.

All paths below are under `/Users/wso2/repos/labs-agentic-engineer/` unless noted.

---

## Summary

The subsystem is **already on the OC WorkflowRun model — and it is closer to the
"right way" than AM is in two important respects.** Both the coding-agent run and
the build run are dispatched as OC **`WorkflowRun`s** through the OC OpenAPI client
(`CreateWorkflowRunWithResponse`), exactly the AM `builds.go` mechanism. Neither is a
hand-applied k8s `Job`, a raw Argo `Workflow`, nor a `kubectl apply`.

Three workflow manifests, three OC objects on the **Workflow Plane**:

| Manifest | Kind | Role |
|---|---|---|
| `app-factory-coding-agent.yaml` | `openchoreo.dev/v1alpha1` **`ClusterWorkflow`** (`:2`) | runs the Claude-Agent-SDK runner pod (one-shot) for one task |
| `docker-build-workflow.yaml` (tail) | `openchoreo.dev/v1alpha1` **`ClusterWorkflow`** `dockerfile-builder` (`:642-645`) | builds the merged app image and posts a Workload |
| `docker-build-workflow.yaml` (head) | 4× `argoproj.io/v1alpha1` **`ClusterWorkflowTemplate`** (`checkout-source` `:20-22`, `containerfile-build` `:184-186`, `publish-image` `:272-274`, `generate-workload` `:357-359`) | the Argo build steps the ClusterWorkflow's `runTemplate` references |

This is structurally identical to AM's platform-resources extension (which ships
`amp-dockerfile-builder` ClusterWorkflow + `checkout-source`/`publish-image`/
`generate-workload` ClusterWorkflowTemplates — `agent-manager-analysis/05-deployment-topology.md:93-96`).

**Two positive divergences from AM:**

1. **Generic ClusterComponentTypes.** Components are created as
   `deployment/service` or `deployment/web-application`
   (`services/design_service.go:24-32` `ocEntrypoint`), NOT AM's two hard-coded
   `deployment/agent-api` / `proxy/external-agent-api` types
   (AM `client/constants.go:47-48`). This is the concept model's intent — a
   Component is a generic Component.
2. **Build image flows via the SAME annotation AM uses.** The `generate-workload`
   step writes the Workload CR into the WorkflowRun annotation
   `openchoreo.dev/workload` (`docker-build-workflow.yaml:602,610`), matching AM's
   `workflowRunWorkloadAnnotationKey` (`agent-manager-analysis/01-agent-deployment.md:146`).

**Main divergence:** the build workflow **posts the Workload to the OC API server
itself** (from inside the Argo pod, via `curl` with a `client_credentials` token —
`docker-build-workflow.yaml:430-575`), rather than the control-plane service mutating
the Workload as AM's `Deploy()` does (`agent-manager-analysis/01-agent-deployment.md:97-106`).
Deploy is then fully delegated to OC's AutoDeploy controller (`AutoDeploy=true`,
`services/dispatch_service.go:635`). The BFF never calls a "deploy" API — it only
marks the task `deployed` when the build WorkflowRun completes.

---

## Coding-agent runner (`remote-worker/`) — what / how

**Image:** `docker.io/xlight05/app-factory-coding-agent-runner:latest`
(`app-factory-coding-agent.yaml:135`), built from `remote-worker/Dockerfile`
(`FROM node:22-alpine`, `USER asdlc`, `ENTRYPOINT ["npx","tsx","src/oneshot.ts"]`
— `Dockerfile:1,38,39`). The ClusterWorkflow re-states `command:["npx"]` /
`args:["tsx","src/oneshot.ts"]` (`app-factory-coding-agent.yaml:142-143`) per the
OC/Argo emissary-executor convention.

**It is a one-shot OC-WorkflowRun step, NOT a hand-applied pod.** The entrypoint
docstring is explicit: *"The Argo Workflow renders a pod from
app-factory-coding-agent ClusterWorkflow, passing the dispatch payload via ASDLC_*
env vars"* (`src/oneshot.ts:1-7`). Exit codes 0/1/2 map to Argo step
success/failure (`oneshot.ts:8-14`).

**Parameterization — all via `ASDLC_*` env, no HTTP body, no token in args.** The
ClusterWorkflow substitutes each `${parameters.task.*}` / `${parameters.repository.*}`
etc. into Argo workflow params, then into pod env
(`app-factory-coding-agent.yaml:79-180`). `oneshot.ts:readDispatchFromEnv`
(`:34-67`) reads `ASDLC_TASK_ID`, `ASDLC_ORG_ID`, `ASDLC_PROJECT_ID`,
`ASDLC_COMPONENT_NAME`, `ASDLC_REPO_URL`, `ASDLC_BEARER`, `ASDLC_GIT_SERVICE_URL`,
`ASDLC_PROMPT`, `ASDLC_IDENTITY_*`, `ASDLC_PLATFORM_URL`, `ASDLC_CORRELATION_ID`,
and validates UUID/slug shapes. `ANTHROPIC_API_KEY` is injected via
`secretKeyRef` from a per-org Secret `{{anthropic-secret-ref}}` in
`workflows-<orgID>` (`app-factory-coding-agent.yaml:176-180`), materialised by
git-service's `ApplyAnthropicWPSecret` pre-flight, NOT via a per-run ExternalSecret
(`:197-202`).

**End-to-end runner flow:**
1. `provisionWorkspace(req)` (`src/lib/workspace.ts:135`): wipes + `git clone`s the
   project repo on its **default branch** into
   `<WORKSPACE_BASE_PATH>/<orgId>/<projectId>/<taskId>/` (`:57-58,180-181`). The PAT
   is fetched at clone time from git-service `POST /api/v1/credentials/refresh`
   using the task bearer (`resolvePATForClone:75-125`) and embedded once in the
   clone URL; `.git/config` then gets a credential helper + a `gh` wrapper so no
   token crosses via process env (`:183-219`, `credhelper.ts`).
2. Optionally pulls per-task skills from the BFF (`pullTaskSkills`) and materialises
   an AgentSkills plugin (`oneshot.ts:104-131`).
3. `runClaudeQuery(req, layout, …)` (`src/lib/runner.ts:44`): invokes
   `@anthropic-ai/claude-agent-sdk` `query()` with `cwd=workspace`, the bundled
   `asdlc` plugin (`PLUGIN_PATH = ../../plugin`, `:11,77-79`), `permissionMode:
   "bypassPermissions"`, `allowedTools: ["Read","Write","Edit","Bash","Glob","Grep"]`
   (`:15,91-105`). The agent itself creates the feature branch, commits, and runs
   `gh pr ready` / opens a PR with `Closes #<issue>` (per `plugin/skills/asdlc/SKILL.md`).
4. Progress events are emitted; a `result` message of subtype `success` → exit 0,
   else exit 1 (`runner.ts:115-130`).

**Dev override:** `app-factory-coding-agent.dev-patch.yaml` `yq`-splices a `hostPath`
mount of `plugin/` onto `/app/plugin` so SKILL.md edits land without an image rebuild
(`:22-31`).

---

## Workflow dispatch mechanics (file:line)

The BFF dispatches a coding-agent run as an **OC `WorkflowRun`**, not a k8s Job /
Argo object / `kubectl`:

- `DispatchService.dispatchOne` (`services/dispatch_service.go:220`):
  - `ensureOCComponent` (`:234,571`) — creates the OC Component (see Build).
  - `taskTokens.Issue(...)` mints a per-task RS256 JWT bearer (`:244`).
  - `resolveDependencyEndpoints` — F2 deploy-gate: every `dependsOn` component must
    expose a non-empty external URL via `ListDeployments` (ReleaseBinding read)
    (`:266,521-548`); else revert to `on_hold` and retry.
  - `gitClient.ApplyAnthropicWPSecret(orgID)` SSA-refreshes the per-org WP Secret
    (`:310`).
  - `wfRunService.TriggerCodingAgent(CodingAgentTrigger{…})` (`:322-333`).
- `workflowRunService.TriggerCodingAgent` (`services/workflowrun_service.go:232`) →
  `ocClient.TriggerCodingAgent(CodingAgentParams{…})`.
- `componentClient.TriggerCodingAgent` (`clients/openchoreo/component_client.go:1030`):
  builds `gen.CreateWorkflowRunJSONRequestBody` with `Spec.Workflow =
  {Kind: ClusterWorkflow, Name: "app-factory-coding-agent", Parameters: codingAgentParameters(p)}`
  (`:1048-1062`), run name `coding-agent-<task8>-<unixMs>` (`:1040`), and calls
  `createWorkflowRun` → `c.oc.CreateWorkflowRunWithResponse(ctx, orgName, body)`
  (`:1104`). **This is the AM mechanism** (`agent-manager-analysis/04-evaluation-monitoring.md:14,40`).
- `codingAgentParameters` (`:1070-1098`) builds the nested `task/repository/bff/
  gitService/anthropic` map matching the ClusterWorkflow's `openAPIV3Schema`
  (`app-factory-coding-agent.yaml:14-68`).

**Deliberate label omission:** TriggerCodingAgent does NOT set
`openchoreo.dev/component` / `openchoreo.dev/project` labels, because OC would then
validate the ClusterWorkflow↔ClusterComponentType allowed-workflow pair and reject
`app-factory-coding-agent` (the user's component is `deployment/service`, allowed
only the builder workflows) (`:1020-1029`). Instead it sets `app-factory.*` catalog
labels (`:1042-1046`) the BFF watcher keys on. **Idempotency** is at the
control-plane DB level: `DispatchedAt` + `LastCodingAgentRunName`
(`dispatch_service.go:339-341`); there is no `retryStrategy` on the Argo template
(side-effecting agent, `app-factory-coding-agent.yaml:124-126`).

**Status:** poll-based, like AM. `CodingAgentWatcher` (`services/webhook/coding_agent_watcher.go:31`)
sweeps `in_progress` tasks every 10s via `GetWorkflowRun` and applies
`coding_agent.failed` on terminal Failed/Error. Success is webhook-driven
(`gh pr ready` → `pull_request.ready_for_review`), not polled (`:25-30`).

---

## Build mechanics

**Component creation (at dispatch, pre-merge).** `ensureOCComponent`
(`services/dispatch_service.go:571`) calls `componentSvc.CreateComponent` with:
- `Type: ocEntrypoint(comp.ComponentType)` → `deployment/service` or
  `deployment/web-application` (`:633`, `design_service.go:24-32`).
- `AutoBuild: false` (`:634`) — every build is BFF-driven from the merge webhook
  (not OC reacting to its own webhook).
- `AutoDeploy: true` (`:635`) — OC's controller creates the ReleaseBinding once the
  build posts the Workload (`:566-570`). Matches AM `Spec.AutoDeploy=true`
  (`agent-manager-analysis/01-agent-deployment.md:56-57`).
- `Workflow: {Kind:"ClusterWorkflow", Name:"dockerfile-builder", Parameters:{repository{url,secretRef:"",appPath,revision.branch}, docker{context,filePath}}}`
  (`:636-651`). The build spec rides on `Component.Spec.Workflow` — same place AM
  stores it (`agent-manager-analysis/01-agent-deployment.md:60,236`). `secretRef` is
  forced blank — credentials come from a pre-staged per-run K8s Secret instead
  (`:594-599`).

**Build trigger (on merge).** `Handler.PullRequestClosed` for `merged=true`
(`services/webhook/handlers.go:207,259`) reads `merge_commit_sha` (`:61-62`) and
calls `wfService.DispatchTaskBuild(task, mergeSHA)`. Build is dispatched **only on
`pull_request.closed merged=true`** (`handlers.go:22-23`).
- `workflowRunService.dispatchBuild` (`services/workflowrun_service.go:164`):
  generates `runName = NewBuildRunName(...)`, stages the per-run build Secret
  `<runName>-git-secret` in `workflows-<orgID>` (`gitClient.StageBuildSecret`, `:177`),
  then `ocClient.TriggerBuildAtCommit(...)` (`:195`).
- `componentClient.triggerBuildInner` (`component_client.go:887`) GETs the
  Component, lifts its `Spec.Workflow` via `buildWorkflowFromComponent` (injecting
  `repository.revision.commit = mergeSHA`, `:903,982-994`), blanks
  `repository.secretRef` (`:963,971-977`), and POSTs a `WorkflowRun` with labels
  `openchoreo.dev/component` + `openchoreo.dev/project` (`:911-921`) →
  `createWorkflowRun`. The commit-injection mirrors AM `builds.go` exactly
  (`workflowrun_service.go:22` comment cites it).

**What the build produces / how the image reaches deploy.** The `dockerfile-builder`
ClusterWorkflow's `runTemplate` (`docker-build-workflow.yaml:736-812`) runs an Argo
`Workflow` with steps `checkout-source → containerfile-build → publish-image →
generate-workload` (all `clusterScope: true`, `:782-812`). Image name =
`${namespaceName}-${project}-${component}` tag `v1` (`:769-772`), pushed to the
WP registry. The `generate-workload-cr` step (`:362`) then, from inside the pod:
1. runs `openchoreo-cli workload create --image $IMAGE` to synth a Workload CR
   (from `workload.yaml` descriptor if present, else default) (`:430-456`);
2. obtains a `client_credentials` token from Thunder and **POSTs/PUTs the Workload
   to the OC API server** `/api/v1/namespaces/<ns>/workloads` (`:473-575`); for an
   auto-generated workload that already exists, it merges only `spec.container.image`
   (`:524-560`);
3. **annotates the WorkflowRun with `openchoreo.dev/workload` = the Workload JSON**
   (+ `openchoreo.dev/workload-from-source`) (`:577-631`).

So the produced image lands on a **Workload**, and is exposed through the
WorkflowRun annotation `openchoreo.dev/workload` — **identical to AM**
(`agent-manager-analysis/01-agent-deployment.md:146`,
`builds.go:imageIDFromWorkflowRunWorkloadAnnotation`). The key difference: in AM the
**control-plane service** mutates the Workload (`Deploy()` → `UpdateWorkload`,
`01-agent-deployment.md:97-106`); here the **Argo build pod** posts it directly to
the API server. The BFF never reads the annotation to deploy — AutoDeploy does the
rest.

**Build status:** `BuildWatcher` (`services/webhook/build_watcher.go:35`) sweeps
`building` tasks every 10s, `GetWorkflowRun`, and `classifyRun`
(`:213-225`) reads `run.Completed` + `run.Status` (the `WorkflowCompleted`
condition Reason) → `build.succeeded`/`build.failed`. There is a git-clone-auth
retry budget (default 3, `:54,128-142`) that re-stages a fresh build Secret and
re-creates the run for the same SHA (`RetryAuthFailedBuild`,
`workflowrun_service.go:270`).

---

## Deploy / auto-deploy mechanics

**Deploy is fully delegated to OC AutoDeploy — there is no explicit deploy call.**
- `Component.Spec.AutoDeploy=true` (`dispatch_service.go:635`). When the build's
  `generate-workload-cr` step POSTs the Workload, OC's controller hashes it, cuts a
  **ComponentRelease**, and (because AutoDeploy) creates a **ReleaseBinding** into the
  first environment of the project's DeploymentPipeline (development) with empty
  `ComponentTypeEnvironmentConfigs`; schema defaults on the `service`
  ClusterComponentType supply replicas/resources/imagePullPolicy
  (`dispatch_service.go:562-570`). This is the same Component → Workload →
  ComponentRelease → ReleaseBinding chain the concept model + AM use
  (`agent-manager-analysis/01-agent-deployment.md:15-20`).
- The generated app's **Components live in the user's OC project/namespace**
  (scoped `<project>-<component>`, `ScopedComponentName`), created one-per-task by
  `ensureOCComponent`.
- The BFF treats build success as deployment: state machine transition
  `building → deployed` on `TaskEventBuildSucceeded` (`services/task_state.go:110`).
  **No ReleaseBinding-Ready wait gates this transition** — the task is `deployed` as
  soon as the build WorkflowRun completes. ReleaseBinding readiness is only consulted
  later, lazily, when a *dependent* task dispatches (`resolveDependencyEndpoints` →
  `ListDeployments`, `dispatch_service.go:521-548`).

**Per-env config is on ReleaseBindings (the right place, unlike AM's build params).**
Runtime env vars and SPA `env-config.js` are written to each ReleaseBinding's
`spec.workloadOverrides.container.env` / `.files`
(`component_client.go:401-465,465+`, `UpdateComponentWorkflowEnvVars` /
`UpdateComponentWorkflowFiles`), and `api-configuration` traits are reconciled via
`TraitSyncService` (`dispatch_service.go:626-672`). Deployment status/URLs are read
back from ReleaseBindings: `deploymentFromReleaseBinding`
(`component_client.go:254`) pulls `externalURLs[http]` into `EndpointURL`. This is
the AM `GetDeployments`/`determineDeploymentStatus` read pattern
(`agent-manager-analysis/01-agent-deployment.md:108-124`).

---

## Plane mapping table (current vs should-be)

| Piece | Current plane | Should-be | Status |
|---|---|---|---|
| BFF `asdlc-service` (dispatch, watchers, state machine, OC client) | Control | Control | ✅ correct |
| Coding-agent `ClusterWorkflow` `app-factory-coding-agent` + its Argo `Workflow`/runner pod | **Workflow Plane** (`workflowPlaneRef: ClusterWorkflowPlane/default`, `app-factory-coding-agent.yaml:10-12`) | Workflow Plane | ✅ correct |
| Build `ClusterWorkflow` `dockerfile-builder` + 4 `ClusterWorkflowTemplate`s + build pod | **Workflow Plane** (`docker-build-workflow.yaml:651-653`) | Workflow Plane | ✅ correct |
| WorkflowRun creation (coding-agent + build) | Control→Workflow boundary via OC `CreateWorkflowRun` | same | ✅ correct (matches AM) |
| Image registry | Workflow Plane registry (`image-name`, `publish-image`) | Workflow/Data registry | ✅ correct |
| Generated app Component / Workload / ComponentRelease / ReleaseBinding | Control (CRs) → **Data Plane** (running pods) | Control + Data | ✅ correct |
| Workload POST to API server | **From inside the Workflow-Plane build pod** (`docker-build-workflow.yaml:502-575`) | Arguably Control (a service should own the Workload mutation, as AM's `Deploy()` does) | ⚠️ divergent (see Gaps) |
| Anthropic key Secret | per-org Secret in `workflows-<orgID>` (Workflow Plane) | Workflow Plane (ESO/OpenBao would be cleaner) | ⚠️ partial |
| `oauth-client-secret` for workload POST | **hardcoded default in the build manifest** (`docker-build-workflow.yaml:376-377`) | a mounted Secret | ❌ wrong (TODO acknowledged at `:375`) |
| Deploy trigger | implicit (OC AutoDeploy) | implicit (OC AutoDeploy) — same as AM | ✅ correct |

---

## Gap vs AM / OC

1. **Where the Workload is written.** AM mutates the Workload from the
   **control-plane service** (`Deploy()` → `ListWorkloads`/`UpdateWorkload`,
   `agent-manager-analysis/01-agent-deployment.md:97-106`) and reads the produced
   image back from the WorkflowRun annotation. Here, the **Workflow-Plane build pod
   itself** authenticates to the OC API server and POSTs/PUTs the Workload
   (`docker-build-workflow.yaml:430-575`). Functionally both end at "Workload updated
   → AutoDeploy cuts a ReleaseBinding", and both still write the
   `openchoreo.dev/workload` annotation (`:602,610`) — but the labs build pod holds
   API-server credentials and applies a control-plane mutation from the workflow
   plane. AM keeps that mutation on the control plane.

2. **Hardcoded OAuth client secret in the build manifest.** The `generate-workload`
   step defaults `oauth-client-secret: openchoreo-workload-publisher-secret`
   (`docker-build-workflow.yaml:376-377`), with an inline TODO to mount it from a
   Secret (`:375`). AM's equivalent credentials flow through ESO/OpenBao
   (`agent-manager-analysis/05-deployment-topology.md:78,115`). This is a real
   secret-handling gap.

3. **`deployed` is asserted at build-completion, not ReleaseBinding-Ready.** AM
   derives deployment status from `ReleaseBinding.Status.Conditions[type=Ready]`
   (`agent-manager-analysis/01-agent-deployment.md:118-124`). The labs state machine
   flips `building → deployed` purely on the build WorkflowRun's `WorkflowCompleted`
   condition (`task_state.go:110`, `build_watcher.go:213-225`) — it never waits for
   the ReleaseBinding to become Ready. The ReleaseBinding is only checked lazily when
   a dependent task needs the upstream's external URL
   (`dispatch_service.go:521-548`). A task can therefore be reported `deployed`
   before its pods are actually Ready.

4. **No `WorkflowRun` deletion / TTL parity quirk.** Both manifests set
   `ttlAfterCompletion: "1d"` (`app-factory-coding-agent.yaml:13`,
   `docker-build-workflow.yaml:654`); AM fakes delete via `ExpireWorkflowRun`
   (`04-evaluation-monitoring.md:115`). Labs has no delete path either — consistent,
   not better/worse.

5. **Coding-agent run is intentionally un-bound to the Component** (no
   `openchoreo.dev/component` label, `component_client.go:1020-1029`). AM's
   eval/build WorkflowRuns are component- or monitor-scoped. This is a sensible
   workaround for OC's allowed-workflow validation but means the coding-agent run is
   not discoverable through the standard component→WorkflowRun listing; the BFF
   relies on its own `app-factory.*` catalog labels + the DB
   (`LastCodingAgentRunName`) instead.

**Positive deltas vs AM (already "more right"):** generic ClusterComponentTypes
(`deployment/service` / `deployment/web-application`) instead of two hardcoded agent
types; runtime env vars on the **ReleaseBinding** (`workloadOverrides.container.env`,
`component_client.go:446`) rather than on `Component.Spec.Workflow.Parameters`
(AM's unusual location, `01-agent-deployment.md:77-78,289`).

---

## What must change

1. **Move the Workload mutation off the build pod onto the control plane.** Mirror
   AM: have the build WorkflowRun produce the image + annotation only, and let
   `asdlc-service` read `openchoreo.dev/workload` and call
   `UpdateWorkload`/`CreateWorkload` through the OC client (it already speaks that
   API). Removes API-server credentials from the workflow pod.
   (`docker-build-workflow.yaml:473-575` → a new control-plane step.)

2. **Mount the workload-publisher client secret from a K8s Secret / OpenBao**, per
   the manifest's own TODO (`docker-build-workflow.yaml:375-377`), instead of the
   plaintext default.

3. **Gate `deployed` on ReleaseBinding Ready.** Add a deploy watcher (or extend
   `BuildWatcher`) that, after `build.succeeded`, polls
   `ListReleaseBindings`/`deploymentFromReleaseBinding` for
   `Conditions[type=Ready]=True` before transitioning `building → deployed` —
   reusing the read path that already exists in `component_client.go:254`. This
   closes the "deployed but not actually Ready" gap that the dependent-task
   deploy-gate (`dispatch_service.go:266`) currently papers over with a 2-minute
   defer loop.

4. **Pin the runner image to an immutable tag** for stage/long-lived envs (the
   manifest itself flags `:latest` + `imagePullPolicy: Always` as a dev-only
   trade-off, `app-factory-coding-agent.yaml:128-136`).

5. **Optional: register the coding-agent ClusterWorkflow against a ClusterComponentType**
   (or have OC relax allowed-workflow validation for non-build component workflows)
   so the run can carry the standard `openchoreo.dev/component` label and be
   discoverable through OC's component→WorkflowRun listing, removing the bespoke
   `app-factory.*` label catalog (`component_client.go:1020-1046`).
