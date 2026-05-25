# BFF Orchestrator (`asdlc-service`) — Code-Grounded Analysis

> Source verified: `/Users/wso2/repos/labs-agentic-engineer/asdlc-service` (branch `main`, working tree at analysis time). Reference model: WSO2 Agent Manager (AM), `/Users/wso2/repos/agent-manager-analysis/{00-overview,01-agent-deployment,05-deployment-topology}.md`. All `file:line` citations are into `asdlc-service/` unless noted.

---

## Summary

`asdlc-service` is the Go BFF for WSO2 Labs Agentic Engineer. It is, like AM, a **control-plane-on-control-plane**: console-facing REST API (`api/`, `controllers/`), orchestration in `services/`, persistence in Postgres (`models/`, `repositories/`, GORM), and a **generated OpenAPI client of the OpenChoreo (OC) API** (`clients/openchoreo/gen` + hand-written wrappers). It self-describes as mirroring AM in many places — the OC transport, the `X-Use-OpenAPI: true` header, the `AuthProvider` contract, and the WorkflowRun-as-build pattern are explicit copies (`clients/openchoreo/transport.go:32-35`, `component_client.go:94-97`).

The BFF's distinctive job vs. AM: it is also a **GitHub webhook receiver** (`controllers/webhook_controller.go`, `services/webhook/`) and runs a **task state machine** (`services/task_state.go`) that drives the spec→requirements→design→tasks→PR→build→deploy lifecycle. The signature divergence from AM is that the BFF dispatches a **coding agent as an OC `WorkflowRun`** of ClusterWorkflow `app-factory-coding-agent` (`component_client.go:1030-1065`) — AM has no equivalent (its only WorkflowRun-driven automation is eval jobs).

OpenChoreo integration discipline is actually **closer to "textbook OC" than AM in one respect** (it uses the real ClusterComponentTypes `deployment/service` and `deployment/web-application` rather than two hard-coded agent types) but **weaker in others**: it relies entirely on OC's AutoDeploy controller, never authors Workload/ComponentRelease/ReleaseBinding/SecretReference itself (grep-confirmed: no `CreateWorkload`/`CreateReleaseBinding`/`CreateComponentRelease`/`CreateSecretReference`/`GenerateRelease` callers anywhere outside `gen/`), has no environment/promotion model (single dev env only), and deliberately **does not use SecretReference** for build credentials — it stages raw K8s Secrets out-of-band via git-service (`component_client.go:953-963`, `dispatch_service.go:594-599`).

---

## Current architecture

### Directory map

| Dir | Role |
|---|---|
| `cmd/asdlc-api/main.go` | Composition root. Builds OC clients, token providers, services, webhook router, and spawns 4 background watcher goroutines (`main.go:553-556`). |
| `api/` | Route registration (chi-style). One `*_routes.go` per resource (tasks, projects, components, design, webhook, idp, org_github, org_anthropic, board, requirements…). `api/app.go` assembles. |
| `controllers/` | HTTP handlers; thin, delegate to services. `webhook_controller.go` is the GitHub receiver. |
| `services/` | Orchestration. Key files: `dispatch_service.go` (task dispatch + ensureOCComponent), `workflowrun_service.go` (build + coding-agent WorkflowRun triggers), `task_state.go` (state machine), `webhook/` (event projector + watchers), `trait_sync.go`, `runtime_config_service.go`, `component_service.go`, `design_service.go`. |
| `clients/openchoreo/` | **The OC integration.** Generated client in `gen/`; wrappers `component_client.go`, `project_client.go`, `namespace_client.go`; `transport.go` (auth/retry); `constants.go`, `oc_names.go`, `helpers.go`, `errors.go`. |
| `clients/` (other) | `gitservice/` (repo, credentials, Anthropic key, build-secret staging), `agents/` (asdlc-agents-service: BA/architect/tech-lead), `observer/` + `observability/` (OC Observer read path for progress logs), `thundersvc/`, `oauth/` (client_credentials token cache), `oidc/`, `requests/` (retryable HTTP), `httpx/` (correlation-id). |
| `models/` | GORM models + DTOs. `component_task.go` (the lifecycle row), `tasks.go` (TaskStatus enum), `component.go`, `project.go`, `organization.go`, `spec.go`, `design.go`. |
| `repositories/` | `task_repository.go`, `config_repository.go`. |
| `middleware/` | `auth_token.go` (JWT extraction into ctx), `correlation_id.go`, `panic_recover.go`, `progress_rate_limit.go`. |
| `config/` | `config.go` (typed config struct), `config_loader.go` (env-var reader). |
| `database/` | GORM + Postgres bootstrap and migrations. |
| `skills/` | Embedded `asdlc` agent skill (`embed.go`) bootstrapped at startup (`main.go:126`). |
| `workload.yaml` (root) | OC Workload manifest for the BFF itself (`app-factory-api`): one project-visibility HTTP endpoint on 8080; project-visibility dependency endpoints to `app-factory-git-service` and `app-factory-agents-service` with env bindings `GIT_SERVICE_BASE_URL` / `AGENTS_SERVICE_BASE_URL`. |

### Auth to OpenChoreo

`oauth.TokenProvider` (`clients/oauth/token_provider.go:14-33`) is a mutex-guarded cache of an OAuth2 **client_credentials** token (Thunder), with a 60s expiry buffer. It satisfies the `openchoreo.AuthProvider` interface (`transport.go:27-30`: `Token()` + `Invalidate()`). The OC transport stamps `Authorization: Bearer <token>`, `X-Use-OpenAPI: true`, and an optional `Host` header on every request via an oapi-codegen `RequestEditorFn` (`transport.go:82-91`). 401 → `AuthProvider.Invalidate()` + retry (`transport.go:67-76`). Falls back to the inbound user token from ctx if the service token can't be fetched (`transport.go:108-117`). This is a near-verbatim copy of AM's `client/client.go:135-158` (cited in `00-overview.md` §"How AMP talks to OpenChoreo").

---

## OpenChoreo integration (how it talks to OC today)

### It is a generated OpenAPI client — same family as AM

`clients/openchoreo/gen/client.gen.go` is oapi-codegen-generated (`oapi-codegen-client.yaml`) and exposes the **full** OC surface (~80 operations incl. Namespaces, Projects, Components, ComponentReleases, ReleaseBindings, Workloads, WorkflowRuns, SecretReferences, GitSecrets, Environments, DataPlanes, DeploymentPipelines, ClusterComponentTypes, Traits, Observability). This is the same approach AM uses (`01-agent-deployment.md` §1.1). **But the BFF wraps only a narrow slice of it.**

Wrapped/used operations (the BFF's actual OC contract):

| BFF method | OC operation(s) | CRD |
|---|---|---|
| `NamespaceClient.{List,Get}Namespaces` (`namespace_client.go:34,59`) | `ListNamespaces`, `GetNamespace` | Namespace = **Organization** |
| `ProjectClient.{List,Get,Create,Delete}Project` (`project_client.go`) | `*Project*` | Project |
| `ComponentClient.CreateComponent` (`component_client.go:371`) | `CreateComponentWithResponse` | Component |
| `ComponentClient.{Get,List,Delete}Component`, `UpdateComponentTraits` | `Get/List/Delete/UpdateComponent` | Component / Trait |
| `ComponentClient.UpdateComponentWorkflowEnvVars` (`:411`) | `ListReleaseBindings` + `UpdateReleaseBinding` | **ReleaseBinding** (`spec.workloadOverrides.container.env`) |
| `ComponentClient.UpdateComponentWorkflowFiles` (`:475`) | same | ReleaseBinding (`...container.files`) |
| `ComponentClient.UpdateComponentTraitEnvironmentConfigs` (`:618`) | same | ReleaseBinding (`spec.traitEnvironmentConfigs`) |
| `ComponentClient.ListDeployments` (`:842`) | `ListReleaseBindings` | ReleaseBinding (read status + endpoint URL) |
| `ComponentClient.TriggerBuild / TriggerBuildAtCommit` (`:868,872`) | `GetComponent` + `CreateWorkflowRun` | WorkflowRun (build) |
| `ComponentClient.TriggerCodingAgent` (`:1030`) | `CreateWorkflowRun` | WorkflowRun (`app-factory-coding-agent`) |
| `ComponentClient.{Get,List}WorkflowRun(s)` (`:1124,1157`) | `Get/ListWorkflowRuns` | WorkflowRun (status polling) |

### CRD mapping

- **Organization ⇒ OC Namespace** (`namespace_client.go:14-20`). No separate Org CRD; the BFF side-cars a UUID per namespace in its own table. (Matches AM, `01-agent-deployment.md` §1.8.)
- **Project ⇒ Project** with `DeploymentPipelineRef` (`project_client.go:116-156`).
- **Component ⇒ Component**, ComponentType is a **real ClusterComponentType**: `deployment/service` or `deployment/web-application` chosen by `ocEntrypoint()` (`design_service.go:25-32`), Kind = `ClusterComponentType` (`component_client.go:688`). This is the key contrast with AM, which hard-codes `deployment/agent-api` / `proxy/external-agent-api` (`00-overview.md` divergence #1).
- **Deployment ⇒ ReleaseBinding** (read-only via `ListDeployments`, write only for env/file/trait overrides). Same "Deployment == ReleaseBinding" identity AM uses (`01-agent-deployment.md` §1.2).
- **Build ⇒ WorkflowRun** — no Build CR; the build spec lives on `Component.Spec.Workflow` (ClusterWorkflow `dockerfile-builder`, `dispatch_service.go:636-651`) and a WorkflowRun is created from it (`component_client.go:887-924`). Same as AM (`01-agent-deployment.md` §1.9).
- **SecretReference: present in the generated client but never used by the BFF.** (See divergences.)

### Component creation (`dispatch_service.ensureOCComponent`, `dispatch_service.go:571-690`)

Per task component, the BFF creates an OC Component with:
- `Type = ocEntrypoint(comp.ComponentType)` → `deployment/service` | `deployment/web-application`.
- `AutoBuild = false` (`:634`) — **every build is BFF-driven** from the merge webhook; OC's own auto-build is intentionally off.
- `AutoDeploy = true` (`:635`) — OC's Component controller owns Workload→ComponentRelease→ReleaseBinding fan-out after the build's `generate-workload-cr` step posts the Workload (`component_client.go:19-28`).
- `Workflow = {Kind: ClusterWorkflow, Name: dockerfile-builder, Parameters: {repository{url,secretRef:"",appPath,revision.branch}, docker{context,filePath}}}`.
- `Traits` derived from `design.md`'s `exposesAPI.auth` → optional `api-configuration` trait (`dispatch_service.go:626-627`, `trait_sync.go`).

Idempotent on 409 (refetch + return, `component_client.go:381-389`). Runtime env vars are **not** stamped on the Component (unlike AM, which puts them on `Spec.Workflow.Parameters.environmentVariables`); they ride on each **ReleaseBinding's `workloadOverrides.container.env`** post-deploy (`component_client.go:33-43`, `:411-463`). This is a cleaner placement than AM's coupling of runtime env to build params (AM divergence #4, `01-agent-deployment.md`).

---

## Workflow / build / deploy mechanics

### Coding-agent dispatch (the AM-less path)

`DispatchService.dispatchOne` (`dispatch_service.go:220-362`) per pending task:
1. Require a GitHub issue (`:228`).
2. `ensureOCComponent` (`:234`) — create the OC Component (AutoBuild=false, AutoDeploy=true).
3. Mint a per-task RS256 JWT bearer (`taskTokens.Issue`, `:244`) — published at `/auth/external/jwks.json` for git-service to verify.
4. `resolveDependencyEndpoints` (`:266`) — F2 deploy-gating: every `dependsOn` component must already be `deployed` with a non-empty external URL, else defer to `on_hold` (`:266-295`).
5. `gitClient.ApplyAnthropicWPSecret` (`:310`) — git-service materialises the per-org Anthropic key as a K8s Secret in `workflows-<orgID>`; returns `SecretRefName`.
6. `wfRunService.TriggerCodingAgent` → `ComponentClient.TriggerCodingAgent` → **`CreateWorkflowRun` of ClusterWorkflow `app-factory-coding-agent`** (`component_client.go:1030-1065`). Params packed at `component_client.go:1070-1098`: `task.{id,orgId,projectId,componentName,prompt}`, `repository.{url,identity}`, `bff.{bearer,platformUrl}`, `gitService.url`, `anthropic.secretRef`.
7. Persist `DispatchedAt` + `LastCodingAgentRunName`, set `Status=in_progress`, move GitHub board item to "In Progress" (`:339-354`).

**Key OC-discipline note** (`component_client.go:1020-1029`): the coding-agent WorkflowRun deliberately omits the `openchoreo.dev/component` / `openchoreo.dev/project` labels and instead uses `app-factory.openchoreo.dev/*` labels — because OC validates the ClusterWorkflow↔ClusterComponentType allow-list when `openchoreo.dev/component` is present, and `deployment/service` only allows the builder workflows, not `app-factory-coding-agent`. So the coding agent is run as a **"detached" WorkflowRun not owned by any Component**. This is a workaround, not a sanctioned pattern.

### Build dispatch (on merge)

Driven exclusively by the **`pull_request.closed merged=true`** webhook (`webhook/handlers.go:207-269`), NOT by the push event (`Push` is audit-only, `:271-300`). On merge:
1. Projector advances task `ready_for_review → merged` and records `MergeCommitSHA` (`handlers.go:224-230`).
2. `wfService.DispatchTaskBuild(task, mergeSHA)` (`handlers.go:259`).
3. `workflowRunService.dispatchBuild` (`workflowrun_service.go:164-219`): precompute `runName` via `NewBuildRunName` → `gitClient.StageBuildSecret` stages `<runName>-git-secret` in `workflows-<orgID>` → `ocClient.TriggerBuildAtCommit` (pins `parameters.repository.revision.commit = mergeSHA`, blanks `repository.secretRef`, `component_client.go:953-963`) → `projector.MarkBuilding` atomically transitions `merged → building` and stores `LastBuildSHA`/`LastBuildRunName` (`projector.go:160-181`).

Build credential injection is **out-of-band**: the per-WorkflowRun K8s Secret is staged directly by git-service; `repository.secretRef` is forced empty so OC's dockerfile-builder skips its SecretReference/ExternalSecret synth (`component_client.go:953-963`, design doc `build-credential-injection.md`).

### Build status & deploy

There is **no explicit deploy call**. AutoDeploy on the Component does the work: build's `generate-workload-cr` posts the Workload → OC controller creates ComponentRelease + ReleaseBinding into the project's first pipeline environment (`dispatch_service.go:562-570`). The BFF observes outcome by **polling**:
- `BuildWatcher` (`webhook/build_watcher.go`): 10s sweep over `building` tasks, `FOR UPDATE SKIP LOCKED`, `GetWorkflowRun` → `classifyRun` gates terminal on `run.Completed` (`WorkflowCompleted` condition True) → `build.succeeded`/`build.failed`, plus a git-clone auth-retry budget (default 3, `build_watcher.go:128-152`).
- `CodingAgentWatcher` (`webhook/coding_agent_watcher.go`): 10s sweep over `in_progress` tasks; only acts on terminal **failure** (`coding_agent.failed`). Success rides the GitHub `pull_request.ready_for_review` webhook (`coding_agent_watcher.go:24-30`).
- `TraitSyncWatcher` and `OnHoldWatcher` (`main.go:452,546`): reconcile per-env trait configs and unblock `on_hold` tasks once deps deploy.

So `building → deployed` is asserted from the build WorkflowRun succeeding — there is effectively **no read-back of the ReleaseBinding Ready condition to confirm the pod is actually live** before declaring `deployed`. (`ListDeployments` is only used at dispatch time to resolve dependency URLs, `dispatch_service.go:521-548`.)

---

## State model

`models.TaskStatus` (`models/tasks.go`): `pending → in_progress → ready_for_review → merged → building → deployed`, plus `rejected`, `failed`, `abandoned`, `on_hold`, `verification_failed`. Transition table is the single source of truth in `services/task_state.go:103-149`; `ApplyTaskEvent` (`:162-172`) is a pure function, terminal states absorb late events.

All transitions outside the dispatch path go through `webhook.Projector` (`webhook/projector.go`), which serialises per-task via **Postgres advisory locks** (`pg_advisory_xact_lock`, `:339-341`) — multi-replica-safe by construction, same philosophy as the watchers' `FOR UPDATE SKIP LOCKED`. On landing `deployed`, a post-commit `DispatchHook.OnTaskDeployed` fires the dependent-unblock cascade (`projector.go:240-246`).

**Webhook ingestion** (`controllers/webhook_controller.go:30-53`): read body → parse routing key → resolve `ocOrgID` via git-service (60s cache) → **HMAC-validate** against that org's secret → dedup INSERT into `webhook_deliveries` → dispatch handler → ack 200/5xx (GitHub redelivers ~9h). Handlers registered in `webhook/handlers.go:25-43`.

### Data model (Postgres, GORM)

- `component_tasks` — the lifecycle row: `Status`, `LifecycleStatus`, `IssueNumber`/`IssueURL`, `PullRequestNumber`/`PullRequestURL`/`BranchName`, `MergeCommitSHA`, `DispatchedAt`, `LastCodingAgentRunName`, `LastBuildSHA`, `LastBuildRunName`, `BuildAuthRetryCount`, `DependsOnComponents`, `Cause`, `ErrorMessage`, `LastEventAt`, `DispatchDeferredAt`.
- `git_repositories` — repo_url ↔ project_id (webhook routing, `handlers.go:310-325`).
- `webhook_deliveries` — dedup/audit (`models/webhook_delivery.go`).
- `organization_idp_profiles` — per-org IDP issuer/jwks (`models/idp_profile.go`).
- Org/project/spec/design/requirements artifacts (`models/{organization,project,spec,design}.go`) — but **Projects/Components/Orgs themselves live in OC**, not duplicated in Postgres beyond the UUID side-car.
- Secrets are **NOT** persisted (no OpenBao client here; Anthropic key + GitHub creds are owned by git-service and materialised as WP K8s Secrets out-of-band).

### Config / env wiring (`config/config_loader.go`)

OC: `PLATFORM_API_SERVICE_BASE_URL` (required, `:31`), `PLATFORM_API_SERVICE_HOST` (k3d Host header, `:32`). OC service auth: `SERVICE_AUTH_{TOKEN_URL,CLIENT_ID,CLIENT_SECRET,HOST_HEADER}` (`:72-75`). Per-target service JWTs: `SERVICE_AUTH_GIT_*` (`:78-81`), `SERVICE_AUTH_AGENTS_*` (`:84-87`). Thunder admin: `THUNDER_ADMIN_URL`/`THUNDER_SYSTEM_CLIENT_{ID,SECRET}` (`:44-46`). Platform IDP: `PLATFORM_IDP_{ISSUER,JWKS_URL}` (`:50`). Observer (progress read path): `OBSERVER_URL` + `OBSERVER_OAUTH_*` (`:60-64`). Downstream services: `GIT_SERVICE_BASE_URL` (`:90`), `AGENTS_SERVICE_BASE_URL` (`:67`), `DATABASE_SERVICE_BASE_URL`. Agent-pod-reachable URLs: `AGENT_GIT_SERVICE_URL` (`:69`), `AGENT_PLATFORM_URL`. Inbound JWT verify: `JWKS_URL`/`JWT_*`. Task-token signing: `BFF_TASK_SIGNING_KEY[_PATH]`. Webhook HMAC: `GitHubWebhookSecret`. **No OpenBao env vars** (vs AM's two OpenBao instances, `05-deployment-topology.md` §1).

---

## Gap vs OpenChoreo + Agent Manager

1. **No SecretReference usage — build creds bypass the OC control plane the "wrong" way.** AM's clean pattern is `{path,key}` SecretReference transiting the CP while the value goes AM→OpenBao→ESO→K8s Secret, so plaintext never crosses the OC API (`00-overview.md` divergence #7, `01-agent-deployment.md` §2.1). The BFF instead has git-service **stage raw K8s Secrets directly into the workflow-plane namespace** and blanks `repository.secretRef` (`component_client.go:953-963`, `workflowrun_service.go:176-193`). The generated client *has* `CreateSecretReference` but the BFF never calls it (grep-confirmed). This is a deliberate, documented choice, but it means the BFF owns a second out-of-band secret-delivery path that OC has no visibility into — and runtime secret env vars (DB creds etc.) have **no mechanism at all** yet (env vars on ReleaseBindings are plaintext `value`, `component_client.go:815-838`).

2. **Single environment, no promotion model.** AM resolves the pipeline and deploys to the lowest env, with explicit promotion via `UpdateDeploymentState` toggling ReleaseBinding `Spec.State` (`01-agent-deployment.md` §2.4, divergence #5). The BFF deploys only into "the project's first environment (development)" via AutoDeploy (`dispatch_service.go:568-570`) and has **no promote/undeploy/suspend path at all** — no `UpdateReleaseBinding Spec.State`, no DeploymentPipeline env-ordering logic. For a real multi-env app platform this is a hard gap.

3. **`deployed` is inferred from build success, not from ReleaseBinding Ready.** AM polls `ReleaseBinding.Status.Conditions[Ready]` to confirm the data-plane actually reached Ready (`01-agent-deployment.md` §5, `deployments.go:313-348`). The BFF's `BuildWatcher` transitions `building → deployed` purely on the **build WorkflowRun** completing Succeeded (`build_watcher.go:213-225`); it never reads back the ReleaseBinding Ready condition before declaring deploy success. `ListDeployments` reads RB status (`component_client.go:842-864`) but only for dependency-URL resolution at dispatch, not as the deploy ACK. The platform can mark a task `deployed` while the pod is still crash-looping.

4. **Coding-agent WorkflowRun is a detached, label-hacked workaround.** It runs an automation pod through OC's Workflow Plane but cannot be a Component-owned WorkflowRun because of OC's ClusterWorkflow↔ComponentType allow-list (`component_client.go:1020-1029`). AM's only Workflow-Plane automation (evals) uses a properly-registered OC `Workflow` CR + ClusterWorkflowTemplate with a parameter schema, dispatched as a Component-independent `monitor-evaluation-workflow` (`05-deployment-topology.md` §2f). The BFF's coding agent has no OC `Workflow` CR wrapper / schema registered in this repo — it directly targets a ClusterWorkflow by name with an ad-hoc parameters map. This is functional but is not the sanctioned "register a Workflow, run it" pattern.

5. **No Workload / ComponentRelease / ReleaseBinding authoring — total AutoDeploy reliance, same as AM but with less control.** Like AM (`00-overview.md` divergence #2), the BFF never authors the release chain (grep-confirmed no `CreateWorkload`/`CreateReleaseBinding`/`CreateComponentRelease`/`GenerateRelease` callers). But AM at least keeps explicit promotion via `UpdateDeploymentState`; the BFF gives up *all* binding-state control and only patches `workloadOverrides` (env/files/traitEnvConfigs) on already-existing RBs, with soft no-ops when RBs don't exist yet (`component_client.go:429-433`). The "retry once the deploy chain catches up" contract is spread across watchers rather than being a first-class reconcile.

6. **Status is 100% pull-based; no event/ack channel from OC.** Three polling watchers (build, coding-agent, trait-sync) at 10s ticks. AM is also pull-based for agents (`01-agent-deployment.md` §5) so this matches AM — but it inherits AM's latency/efficiency cost and adds a third poller for the coding agent that OC could in principle signal.

7. **GitHub is a first-class control surface OC doesn't model.** The entire `merged → building` trigger, PR linkage (`Closes #N` parsing, `handlers.go:82-98`), and board-status sync live in the BFF + GitHub, mirroring AM's "own DB + push channel for things OC doesn't model" lesson (`00-overview.md` final lesson). This is legitimate, but it means the source of truth for "code landed" is a GitHub webhook, not an OC event — fragile if webhooks are missed (mitigated only by GitHub's ~9h redelivery, `webhook_controller.go:42`).

---

## What must change (prioritized)

**P0 — Confirm deploy from the ReleaseBinding, not the build.** Make `building → deployed` gate on `ReleaseBinding.Status.Conditions[Ready]=True` (add a deploy watcher or extend `BuildWatcher` to chain into an RB-Ready poll, reusing `ListDeployments`/`deploymentFromReleaseBinding` which already reads conditions, `component_client.go:254-287`). Today a green build ≠ a live pod. Cite AM `01-agent-deployment.md` §5 / `deployments.go:313-348`.

**P0 — Adopt SecretReference for runtime secrets.** There is currently *no* path for application runtime secrets (DB passwords, API keys the generated app needs) — only plaintext env `value` on ReleaseBindings. Wire `CreateSecretReference` (already in `gen/`) + an OpenBao/ESO backend so secret env vars resolve via `valueFrom.secretKeyRef` (the model already supports it, `component_client.go:815-838`). Follow AM's `secret_references.go` pattern (`00-overview.md` divergence #7).

**P1 — Add an environment/promotion model.** Introduce DeploymentPipeline-aware env ordering and a promote/undeploy lever via `UpdateReleaseBinding Spec.State`, instead of dev-env-only AutoDeploy. Mirror AM's `UpdateDeploymentState` (`01-agent-deployment.md` §2.4).

**P1 — Register the coding agent (and dockerfile-builder) as proper OC `Workflow` CRs with parameter schemas**, and resolve the ClusterWorkflow↔ComponentType allow-list cleanly (e.g. a dedicated automation ComponentType or a sanctioned "detached workflow" mechanism) so `TriggerCodingAgent` isn't relying on label-omission to dodge OC validation (`component_client.go:1020-1029`). Parallel: AM's eval `Workflow` CR (`05-deployment-topology.md` §2f).

**P2 — Reduce out-of-band secret staging.** The `<runName>-git-secret` direct K8s Secret staging (`workflowrun_service.go:176-193`) couples the BFF/git-service to workflow-plane namespace internals. Once SecretReference is adopted for runtime secrets, evaluate folding build credentials into the same GitSecret/SecretReference path OC already supports (`CreateGitSecret` exists in `gen/`).

**P2 — Consider an OC event/watch channel** to replace or supplement the three 10s pollers, reducing deploy-latency and load (acknowledging AM is also pull-based, so this is an improvement over the reference, not just parity).

**P3 — Harden the GitHub-as-source-of-truth path** with a reconciliation sweep (periodically diff OC ReleaseBinding/WorkflowRun reality against task state) so a missed webhook doesn't strand a task — the watchers already do this for `building`/`in_progress`; extend to `merged` (missed `pull_request.closed`).
