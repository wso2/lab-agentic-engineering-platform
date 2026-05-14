# App Factory → OpenChoreo-Native Migration Plan

**Status:** Draft v6.2 — incorporates rounds 1–6 of critic findings. **OC Design: APPROVED. WSO2 Cloud Platform: APPROVED.** The `local-app-factory` branch and `lab-app-factory` codebase are treated as **our work-in-progress** (not authoritative reference). Authoritative reference is canonical Agent-Manager on WSO2 Cloud (i.e. how `agent-manager` is deployed inside `wso2cloud-deployment@main`/`local`/`dev`/`stage`/`prod`), NOT Agent-Manager's standalone setup.

**Audience:** App Factory engineering, OC Platform team, WSO2 Cloud Platform Engineers.

**Completed work (not in this plan anymore):** the worker-model migration that this plan originally labelled "Phase 3" landed via a smaller stand-alone refactor — see `docs/design/remote-worker-refactor.md`. `app-factory-remote-worker` (Component, Workload, ReleaseBinding, source) has been removed; the per-task agent runs as `WorkflowRun` of `ClusterWorkflow: app-factory-coding-agent` in the WorkflowPlane today, with a `coding-agent watcher` complementing the GitHub-webhook path. References below in §0.3 / §3.2 / §7 reflect the post-refactor state.

---

## 0. References, current state, and the standalone-vs-WSO2-Cloud distinction

### 0.1 The deployment-mode insight that drives this plan

Agent-Manager has **two deployment modes**, and v4 conflated them. Round-4 critique surfaced this:

- **Standalone** (`agent-manager:deployments/scripts/setup-openchoreo.sh`): brings up cluster + OC planes + Agent-Manager's own Thunder (via `wso2-amp-thunder-extension`) + own OpenBao (via `wso2-amp-secrets-extension`) + own Postgres (Bitnami subchart in `wso2-agent-manager`). Used for local dev / single-cluster demos.
- **On WSO2 Cloud** (the canonical production mode, defined in `wso2cloud-deployment@main:controlplane/common/domains/developers/namespaces/wso2cloud/projects/agent-manager/`): adds only Project + Components + ReleaseBindings. Reuses WSO2 Cloud's existing **platform-idp Thunder**, **shared secret-manager-api**, and **platform-managed Postgres**. Verified by reading `agent-manager-service/release-binding.yaml` on `origin/main`:
  - `KEY_MANAGER_JWKS_URL: "https://platform-idp-${environment}.gateway.${cloud_base_domain}/oauth2/jwks"` — user JWT validation against platform-idp.
  - `IDP_TOKEN_URL: "https://platform-idp-${environment}.gateway.${cloud_base_domain}/oauth2/token"` — M2M token issuance from platform-idp.
  - `THUNDER_BASE_URL: "https://platform-idp-${environment}.gateway.${cloud_base_domain}"` — all Thunder API calls go to platform-idp.
  - `SECRET_MANAGER_API_URL: "https://secret-manager-api.openchoreo.dp.${cloud_base_domain}"` — uses shared platform secret-manager.
  - `DB_HOST: "${database_host}"` — substituted to a platform-managed Postgres at deploy time.
  - **No reference to `amp-thunder-extension-service`**, `amp-secrets-openbao`, or any AMP-specific infrastructure URL. The `wso2-amp-*` extension charts are not in the deploy path on WSO2 Cloud.

**Implication for App Factory's WSO2 Cloud deployment**: it needs **zero own Thunder, zero own OpenBao, zero own Postgres**. The `wso2-app-factory-thunder-extension` / `wso2-app-factory-secrets-extension` / parent `wso2-app-factory` charts proposed in v4 — all dropped. Only the `wso2-app-factory-platform-resources-extension` (cluster-scoped CRs) survives as a chart.

For local-dev convenience, the `lab-app-factory:deployments-v2/scripts/setup.sh` script can still bring up a standalone-style stack (its own Postgres etc.), but the **production target** matches Agent-Manager's WSO2 Cloud deployment shape.

### 0.2 Canonical references (the spec)

| Reference | What it tells us |
|---|---|
| `wso2cloud-deployment@main:controlplane/common/domains/developers/namespaces/wso2cloud/projects/agent-manager/` | The exact GitOps placement App Factory mirrors: Project + Components + ReleaseBindings under `wso2cloud` namespace. |
| `agent-manager:agent-manager-service/clients/openchoreosvc/client/components.go` and `infrastructure.go` | Per-tenant OC namespace pattern: `orgName` from BFF API path is passed as the OC namespace parameter to `CreateComponent`. `GetOrganization` calls `GetNamespaceWithResponse(ctx, orgName)`. |
| `agent-manager:agent-manager-service/services/agent_manager.go` `buildCreateTraitRequests` + `AttachTraits` (lines 280–325, 710) | Programmatic per-Component Trait attachment by BFF (NOT via static ComponentType embedding). |
| `agent-manager:deployments/helm-charts/wso2-amp-platform-resources-extension/templates/component-types/agent-api.yaml` | Template for `app-factory-service` ComponentType: workloadType, allowedWorkflows, embedded HPA ClusterTrait. |
| `agent-manager:deployments/helm-charts/wso2-amp-platform-resources-extension/templates/component-workflows/amp-gcp-buildpack-builder.yaml` | Template for the coding-agent ClusterWorkflow + ClusterWorkflowTemplate. |
| `wso2cloud-deployment@main:controlplane/common/init/layer-2/thunder/setup-scripts/` (numbered scripts 50–59) | Canonical mechanism for OAuth client registration in platform-idp Thunder. New apps add a new numbered script (e.g., `60-app-factory-apps.sh`). |
| `wso2cloud-deployment@main:org-default-resources/dev/shared/bootstrap/v1.0/` | BootstrapManifest pattern for per-tenant OC namespace bootstrap. Applied by `platform-api`. |
| `/Users/wso2/openchoreo-sources/openchoreo` source | OC contracts: CRD schemas (`api/v1alpha1/`), controllers (`internal/controller/`), REST API (`openapi/openchoreo-api.yaml`), autobuild semantics (`internal/openchoreo-api/services/autobuild/`). |

### 0.3 Current state of our work-in-progress (treat as movable, not authoritative)

The `wso2cloud-deployment@local-app-factory` branch and `lab-app-factory` codebase contain commits by `kaje94` and `xlight05` made to get the platform running locally. **These are not the canonical pattern and most of them will be replaced.** Inventory with disposition:

| Today (local-app-factory branch / lab-app-factory) | Canonical equivalent | Disposition |
|---|---|---|
| App Factory lives only on `local-app-factory` branch fork | Project + Components added to canonical `wso2cloud-deployment@main`, then promoted via WSO2 Cloud's standard branch flow | **Replace.** Phase 1 lifts App Factory into canonical branches; Phase 7 deletes the fork. |
| 4 backend Components: `app-factory-api`, `app-factory-console`, `app-factory-git-service`, `app-factory-agents-service` | `agent-manager` has 2 (`agent-manager-service`, `agent-manager-console`) + evaluation as Argo workflow templates | **Done (worker refactor).** `app-factory-remote-worker` already replaced by `ClusterWorkflow: app-factory-coding-agent`. Remaining 4 Components survive. |
| `PLATFORM_API_NAMESPACE_OVERRIDE` env var collapsing all tenants into one shared namespace | Per-tenant OC Namespace (= `orgName`, mirroring Agent-Manager's `orgName = OC Namespace` pattern) | **Replace.** Phase 2. |
| `asdlc-user-projects` namespace declared in `app-factory/namespace.yaml` | Per-tenant namespace `aft-{tenantSlug}` (the `aft-` prefix is App-Factory's convention; Agent-Manager uses raw `orgName` because its ops mode owns those orgs) | **Delete.** Phase 2. |
| Postgres as raw `Deployment+Service+Secret+PVC` in `postgresql.yaml` | **Platform-managed Postgres** referenced via `${database_host}` substitution (mirrors agent-manager-service ReleaseBinding's `DB_HOST: ${database_host}`). For local-dev, the standalone setup script provisions a Postgres just like `agent-manager`'s setup script does. | **Replace.** Phase 1. |
| App Factory's `app-factory-api` uses **shared OpenChoreo Thunder** (`thunder.openchoreo.localhost`) for both user auth and BFF→OC M2M, via the platform-shared `openchoreo-system-app` OAuth client | **Use platform-idp Thunder for all Thunder needs**, via App-Factory-specific OAuth clients registered as a new numbered Thunder bootstrap script (mirrors how cloud-console / Customer Portal / etc. are registered in `controlplane/.../setup-scripts/`). v4's "Two-Thunder split" is dropped — Agent-Manager on WSO2 Cloud has no own Thunder in the deploy path. | **Replace.** Phase 1. |
| `APP_FACTORY_CONSOLE` OAuth client registered inside the `thunder-idp.yaml` ComponentType modification (a kaje hack) | Registered via a new bootstrap script `controlplane/common/init/layer-2/thunder/setup-scripts/60-app-factory-apps.sh` (and `dataplane/.../setup-scripts/60-app-factory-apps.sh`), using the same `curl POST /applications` pattern as `52-default-apps.sh` | **Replace.** Phase 1. |
| Inter-service URLs claimed (in inline ReleaseBinding comments) to be auto-injected from `Workload.spec.dependencies.endpoints.envBindings` | **Hardcoded in ReleaseBindings** — Agent-Manager pattern (`agent-manager:.../release-binding.yaml` lines 135–167) | **Replace.** Phase 1. |
| `setup.sh` runtime registration of `asdlc-bff-to-agents-service`, `asdlc-bff-to-git-service` OAuth clients (the third, `asdlc-bff-to-remote-worker`, was removed by the worker refactor) | All s2s clients registered declaratively in the Thunder bootstrap scripts (§3.4) | **Replace.** Phase 1. |
| OpenBao seeded by `setup.sh` (`secret/apps/anthropic`, `secret/apps/github-platform-pat`, `secret/apps/github-webhook`, `secret/apps/bff-task-signing-key`) | Use WSO2 Cloud's **shared secret-manager-api** (`https://secret-manager-api.openchoreo.dp.${cloud_base_domain}` per agent-manager-service ReleaseBinding `SECRET_MANAGER_API_URL`). Secrets stored in the platform-shared OpenBao under App-Factory-scoped paths, exposed via SecretReference CRs in the App Factory's own namespace. | **Replace.** Phase 1. |
| 5 `SecretReference` CRs hand-written in GitOps for App Factory's own services | Same shape, but moved into `wso2-app-factory-platform-resources-extension`'s templates and rendered with values | **Restructure.** Phase 1. |
| Components use cluster-shared `deployment/service` ComponentType | App Factory defines its own `app-factory-service` `ComponentType` (in `default` namespace, mirroring agent-manager's `agent-api` which is also `kind: ComponentType` namespace-scoped to `default` — round-5 verified, NOT `ClusterComponentType`), and `app-factory-generated-service` for components in user-generated apps | **Add.** Phase 1, in `wso2-app-factory-platform-resources-extension`. |
| Worker tasks dispatched in-process by `app-factory-remote-worker` (Phase 0); `WorkflowRunService.TriggerForPush` (Phase 1 wiring) handles only build WorkflowRuns | Coding agent runs as `WorkflowRun` of `ClusterWorkflow: app-factory-coding-agent` on the WorkflowPlane | **Done.** Landed via `docs/design/remote-worker-refactor.md` — BFF dispatches via `WorkflowRunService.TriggerCodingAgent`; one-shot runner image referenced by the ClusterWorkflow; coding-agent watcher applies `coding_agent.failed` on terminal pod failure. `WorkflowRunService.TriggerForPush` retained for the build path. |
| BFF orchestrates GitHub `push` → `WorkflowRunService.TriggerForPush` → builds | OC's `POST /api/v1alpha1/autobuild` consumes `push` webhooks directly; Components have `autoBuild: true` | **Replace.** Phase 4. |
| Task JWT (RS256, public key at `/auth/external/jwks.json`) | Same pattern, no change | **Keep.** |
| `claim.ouHandle` used directly as project owner key | Tenant model in BFF Postgres, with `Tenant.id` as immutable PK and `ouHandle` as lookup; per-tenant OC Namespace `aft-{tenantSlug}` | **Replace.** Phase 2. |

### 0.4 What survives from our codebase

- The general division of services (Console, BFF, Agents, Git). git-service stays because GitHub App credential management is a distinct concern.
- BFF Postgres schema for App-Factory-native concepts (Spec, Design, ComponentTask, WebhookDelivery — gain `taskSetId` FK).
- RS256 task JWT, dual-key rotation, `BFF_TASK_*` env vars.
- Webhook ingestion + projection (idempotency via `webhook_deliveries`, advisory locks per task).

---

## 1. Why this plan exists

The App Factory today is shaped by what was needed to bring it up locally, not by how Agent-Manager is canonically deployed on WSO2 Cloud. The result: the runtime works, but the deployment, identity, secret management, and worker model differ from how every other OC-native SaaS app on WSO2 Cloud is structured. The migration converges App Factory onto Agent-Manager's WSO2 Cloud deployment shape.

Three concrete drivers (the third is now resolved — see status note at the top):

- **Multi-tenancy is currently fake.** Every signed-in user collapses into one `asdlc-user-projects` OC Namespace via a string-replacement env var. Cannot ship to external customers.
- **The deployment story is a fork.** App Factory exists only on a `local-app-factory` branch. To reach dev/stage/prod, it must be rebuilt against canonical patterns and merged into canonical branches.
- **~~Worker model leaks resource isolation.~~ Resolved** by the remote-worker refactor. Per-task Argo `WorkflowRun`s on the WorkflowPlane today; no shared in-process pool.

---

## 2. Target architecture (one paragraph)

App Factory becomes a SaaS Project named `app-factory` under WSO2 Cloud's `wso2cloud` namespace, alongside `agent-manager`, `core` (cloud-console, billing-service, platform-api-service, platform-idp), etc. Its components — **app-factory-api** (Go BFF), **app-factory-console** (React SPA), **app-factory-agents-service** (TS planning agents), **app-factory-git-service** (Go GitHub abstraction) — are deployed via canonical wso2cloud-deployment GitOps (PRs against `main` → standard branch promotion). End-users authenticate via the existing `platform-idp` Thunder; the BFF validates JWTs against its JWKS. App Factory's BFF→OC M2M, BFF→agents/git s2s, and coding-agent→BFF callbacks all use OAuth clients registered in the **same platform-idp Thunder** via a new numbered Thunder bootstrap script (`controlplane/common/init/layer-2/thunder/setup-scripts/60-app-factory-apps.sh`). Secrets (Anthropic API key, GitHub credentials, etc.) live in WSO2 Cloud's **shared secret-manager-api** (OpenBao), surfaced into App Factory's namespace via `SecretReference` CRs. App Factory's BFF Postgres uses **WSO2 Cloud's platform-managed Postgres** (referenced via `${database_host}` substitution, same as agent-manager-service). The **only Helm chart** App Factory introduces to WSO2 Cloud is `wso2-app-factory-platform-resources-extension` (mirrors `wso2-amp-platform-resources-extension`), holding: `ComponentType: app-factory-service`, `ComponentType: app-factory-generated-service`, `ComponentType: app-factory-generated-web` (all `kind: ComponentType` namespace-scoped to `default`, mirroring agent-manager's `agent-api`), plus cluster-scoped `ClusterTrait: app-factory-needs-managed-database`, `ClusterWorkflow: app-factory-coding-agent`, `ClusterWorkflowTemplate: app-factory-claude-agent-runner`, and `Project: app-factory-internal` (mirrors `Project: amp` in agent-manager's chart). Per App Factory tenant, the BFF creates an OC `Namespace` (`aft-{tenantSlug}`) and applies the BootstrapManifest sequence; per user-project, an OC `Project` inside that namespace; per service in their generated app, a `Component` whose Traits the BFF programmatically attaches via `Component.spec.traits` (mirroring Agent-Manager's `buildCreateTraitRequests`+`AttachTraits` pattern). Coding-agent tasks run as `WorkflowRun`s of `ClusterWorkflow: app-factory-coding-agent`. Builds run as autobuild-triggered `WorkflowRun`s. Source of truth: OC etcd for everything OC understands; BFF Postgres for App-Factory-native concepts (Tenant, User, Spec/SpecRevision, Design/DesignRevision, TaskSet, Task, WebhookDelivery, WorkflowRunRef); Git for source artifacts; platform-idp Thunder for identity. Observability flows through WSO2 Cloud's existing OTLP pipeline via `python-otel-instrumentation-trait` and `instrumentation-trait-env-injection` attached at Component level.

---

## 3. Resource model

### 3.1 BFF-owned (App Factory–native, in Postgres)

(Unchanged from v4.)

| Resource | Notes |
|---|---|
| `Tenant` | One per signing-up org (keyed by `ouHandle` from platform-idp JWT). Holds `id` (immutable UUID), `ouHandle` (mutable lookup), `ocNamespaceName` (= `aft-{tenantSlug}`), `displayName`, `quota`. |
| `User` | One per Thunder user, FK to Tenant. |
| `Project` (App Factory's) | One per user-project. |
| `Spec`, `SpecRevision` / `Design`, `DesignRevision` | Spec/Design = mutable head pointers; revisions = immutable, content-addressed. |
| `TaskSet`, `Task`, `WorkflowRunRef`, `WebhookDelivery` | Same as v4. |

### 3.2 OC-owned (in OC etcd, never duplicated)

| OC Resource | Where | Created by |
|---|---|---|
| App Factory's own: `Project: app-factory` + 4 Components + Workloads + ReleaseBindings (dev/stage/prod) | `wso2cloud` namespace | Canonical wso2cloud-deployment GitOps (Flux) |
| Per-tenant Namespace: `aft-{tenantSlug}` | (top-level) | BFF on tenant signup via OC API + BootstrapManifest sequence |
| Per-user-project OC Project: `proj-{projectSlug}` | `aft-{tenantSlug}` | BFF on user-project create |
| Per-component-of-user-app: `Component` (using `app-factory-generated-service`) + auto-created Workload + ComponentRelease + ReleaseBinding | `aft-{tenantSlug}`, scoped to per-user-project Project | BFF after Architect agent emits design |
| Coding-agent WorkflowRun (`ClusterWorkflow: app-factory-coding-agent`) | `aft-{tenantSlug}` (today: `asdlc-user-projects` until Phase 2) | BFF `DispatchService` (already wired; replaces former `RemoteWorkerService`) |
| Build WorkflowRun (kind = `dockerfile-builder` etc.) | `aft-{tenantSlug}` | OC `/api/v1alpha1/autobuild` (NOT BFF) |
| Per-component runtime SecretReference (e.g. `github-credentials`, app secrets) | `aft-{tenantSlug}` | BFF (per-tenant resolution to OpenBao paths via shared secret-manager-api) |

### 3.3 Helm chart inventory: ONE chart on WSO2 Cloud

Round-4 critique exposed that v4 over-mirrored Agent-Manager's standalone setup. On WSO2 Cloud, Agent-Manager actually uses ONLY the platform-resources extension; everything else (its own Thunder, OpenBao, Postgres) is standalone-only.

| App Factory chart | Status | Contents |
|---|---|---|
| `wso2-app-factory-platform-resources-extension` | **Mirrors** `wso2-amp-platform-resources-extension`. The single chart App Factory ships to WSO2 Cloud. | App-Factory-specific OC platform CRs: `ComponentType: app-factory-service`, `ComponentType: app-factory-generated-service`, `ComponentType: app-factory-generated-web` (all `kind: ComponentType` in `default` namespace, mirroring agent-manager's `agent-api` ComponentType — round-5 verified that agent-manager uses ComponentType not ClusterComponentType, which lets `allowedTraits` reference both Trait and ClusterTrait); `ClusterTrait: app-factory-needs-managed-database`; **`ClusterWorkflow: app-factory-coding-agent`** (already authored — see `docs/design/remote-worker-refactor.md`; lives in the `local-app-factory` submodule today, moves into this chart in Phase 1); `Project: app-factory-internal` (mirrors agent-manager's `Project: amp`). |
| `wso2-app-factory-thunder-extension` | **DROPPED.** | Agent-Manager doesn't deploy its own Thunder on WSO2 Cloud (verified: agent-manager-service ReleaseBinding references only `platform-idp-${env}.gateway.${cloud_base_domain}`). App Factory uses platform-idp the same way. |
| `wso2-app-factory-secrets-extension` | **DROPPED.** | Agent-Manager uses shared `secret-manager-api.openchoreo.dp.${cloud_base_domain}` on WSO2 Cloud (verified: ReleaseBinding `SECRET_MANAGER_API_URL`). App Factory follows. |
| `wso2-app-factory` parent (Bitnami Postgres) | **DROPPED.** | Agent-Manager uses `${database_host}` platform-managed Postgres on WSO2 Cloud (verified: ReleaseBinding `DB_HOST`). App Factory follows. |

For **local-dev convenience**, `lab-app-factory:deployments-v2/scripts/setup.sh` may continue to bring up its own Postgres / OpenBao / Thunder for offline work — but the production target on WSO2 Cloud uses none of these.

### 3.4 Thunder OAuth clients (registered via new bootstrap script)

App Factory adds new numbered Thunder bootstrap scripts at `wso2cloud-deployment@main:controlplane/common/init/layer-2/thunder/setup-scripts/` (and matching copies in `dataplane/.../setup-scripts/`), using the same `curl POST /applications` pattern as the existing `52-default-apps.sh`.

**Round-5 question on granularity:** Existing canonical scripts are mostly one-client-per-script (`51-backstage-app.sh`, `53-rca-agent-client.sh`, `54-cli-app.sh`, `55-system-app.sh`, `56-user-mcp-app.sh`, `57-service-mcp-app.sh`, `58-workload-publisher-app.sh`, `59-openchoreo-observer-app.sh`), with `52-default-apps.sh` as a multi-client exception. Default to one-script-per-client to match the dominant convention:

- `60-app-factory-console-app.sh` — APP_FACTORY_CONSOLE (PKCE)
- `61-app-factory-bff-client.sh` — APP_FACTORY_BFF (client_credentials)
- `62-app-factory-system-client.sh` — APP_FACTORY_SYSTEM (client_credentials, Administrator role)
- `63-app-factory-coding-agent-client.sh` — APP_FACTORY_CODING_AGENT (client_credentials)

Confirm next-available numbering with WSO2 Cloud Platform team (§10 Q12) — 60–63 is a guess. Clients registered:

| Client ID | Type | Used by |
|---|---|---|
| `APP_FACTORY_CONSOLE` | OAuth2 PKCE (browser) | app-factory-console (replaces today's kaje hack via `thunder-idp.yaml` ComponentType modification) |
| `APP_FACTORY_BFF` | client_credentials | BFF → OC API M2M, BFF → agents-service, BFF → git-service |
| `APP_FACTORY_SYSTEM` | client_credentials, Administrator role | BFF → platform-api for tenant namespace provisioning (mirrors agent-manager's `AGENT_MANAGER_SYSTEM_APP`) |
| `APP_FACTORY_CODING_AGENT` | client_credentials | coding-agent WorkflowRun pods → BFF callback |

All client secrets stored in shared OpenBao under `secret/data/app-factory/clients/{client-id}-secret`, surfaced into `wso2cloud` namespace via SecretReference CRs (mirrors agent-manager-service's `platform-idp-agent-manager-client-secret` reference).

**Round-5 nit (Administrator role for APP_FACTORY_SYSTEM):** The bootstrap script registers the OAuth client. Administrator role assignment is a separate Thunder admin API call (existing scripts like `55-system-app.sh` do this; verify the exact pattern when authoring `62-app-factory-system-client.sh`).

---

## 4. Tenancy model

### 4.1 Identity and authentication

**End-users** authenticate against platform-idp Thunder (same as cloud-console). BFF validates JWTs against `https://platform-idp-${environment}.gateway.${cloud_base_domain}/oauth2/jwks` with audience `APP_FACTORY_CONSOLE`. No App-Factory-specific Thunder.

**Service-to-service** uses `client_credentials` flow against the same platform-idp Thunder, with App-Factory-specific OAuth clients (§3.4).

This matches Agent-Manager's WSO2 Cloud deployment exactly (verified: agent-manager-service ReleaseBinding references only platform-idp URLs).

### 4.2 Per-tenant OC Namespace

This pattern is **App-Factory-specific in design choice but Agent-Manager-aligned in mechanism.** Round-4 critique correctly noted that Agent-Manager runs single-namespace in WSO2 Cloud's deployment mode (because WSO2 Cloud-internal teams are its only users), but the **OC API surface** Agent-Manager uses (`CreateComponent(orgName, projectName, ...)` mapping `orgName` directly to namespace) supports this pattern. App Factory leans into it because it has many external customers, not just internal teams.

- New tenant signup flow:
  1. End-user authenticates with platform-idp.
  2. BFF receives JWT with `ouHandle`.
  3. BFF JIT-creates `Tenant` row with `tenantSlug = sluggify(ouHandle)` and `ocNamespaceName = aft-{tenantSlug}`.
  4. BFF calls OC REST: `POST /api/v1/namespaces` body `{name: "aft-{tenantSlug}"}`.
  5. BFF triggers BootstrapManifest application — **mechanism TBD** (§10 Q5): either (a) BFF directly invokes `platform-api` orchestration (using `APP_FACTORY_SYSTEM` client), or (b) `platform-api` watches for new namespaces and auto-applies, or (c) bootstrap is implicit when an Org/OU CR is created via a different API. To be confirmed before Phase 2 ships.
  6. BFF stores tenant.
- Per-user-project: BFF creates child OC `Project` `proj-{projectSlug}` inside `aft-{tenantSlug}`.
- Per-component-of-user-app: BFF creates `Component` referencing `ComponentType: app-factory-generated-service` (in `default` namespace).

**Naming:** `aft-` prefix is App-Factory's convention to namespace-collision-protect against any other multi-tenant SaaS that might also colonize OC namespaces in the same cluster. (Agent-Manager has no equivalent prefix because it owns its orgs centrally.)

### 4.3 Trait attachment: programmatic, not declarative

Round-4 critique exposed two related issues:
1. `ClusterComponentType.allowedTraits` only accepts `ClusterTraitRef` (per `clustercomponenttype_types.go:42-47`), so namespace-scoped Traits like `python-otel-instrumentation-trait` cannot be listed there.
2. Agent-Manager doesn't use `allowedTraits` for these — it attaches Traits programmatically per Component (verified: `agent-manager-service/services/agent_manager.go:280-325` `buildCreateTraitRequests` + line 710 `AttachTraits`).

App Factory's `app-factory-service` and `app-factory-generated-service` are **`kind: ComponentType`** (namespace-scoped to `default`), NOT `kind: ClusterComponentType`. Round-5 verified that agent-manager's `agent-api` follows this pattern. ComponentType's `allowedTraits` accepts both `ClusterTraitRef` and `TraitRef`, unlike ClusterComponentType which is ClusterTrait-only.

- Embed `ClusterTrait: horizontal-pod-autoscaler` in `spec.traits` list (always-applied).
- List in `allowedTraits` (per-Component selectable): `Trait: python-otel-instrumentation-trait`, `Trait: instrumentation-trait-env-injection`, `Trait: api-configuration` — mirroring agent-manager's `agent-api` ComponentType exactly.
- BFF chooses which `allowedTraits` to actually attach per Component via `Component.spec.traits` based on the component's language/role (mirrors `buildCreateTraitRequests` logic in `agent-manager-service/services/agent_manager.go:280-325`). For example: Python services get `python-otel-instrumentation-trait`; non-Python get `instrumentation-trait-env-injection`; HTTP services exposing APIs get `api-configuration`.

The "programmatic attachment" is therefore not a workaround for a schema constraint (round-4's interpretation) but the canonical Agent-Manager pattern: declare what's allowed in the ComponentType, decide per-Component which to attach in the BFF.

---

## 5. Migration phases

### Phase 1 — Lift App Factory into canonical wso2cloud-deployment, drop standalone-only infra (3–4 weeks)

**Goal:** App Factory ships as Project + Components in canonical wso2cloud-deployment + ONE Helm chart (`wso2-app-factory-platform-resources-extension`) for cluster-scoped CRs. All standalone-only chart bits dropped.

1. **Add Thunder bootstrap script** `wso2cloud-deployment@main:controlplane/common/init/layer-2/thunder/setup-scripts/60-app-factory-apps.sh` (and `dataplane/.../60-app-factory-apps.sh`) registering the 4 OAuth clients (§3.4). Pattern follows existing `52-default-apps.sh` (`curl POST /applications`). Client secrets stored in WSO2 Cloud's OpenBao under `secret/data/app-factory/clients/`.
2. **Create `wso2-app-factory-platform-resources-extension` Helm chart**, mirroring `agent-manager:wso2-amp-platform-resources-extension`. **All ComponentTypes are `kind: ComponentType` in `default` namespace**, mirroring agent-manager's `agent-api`. **All Traits referenced in `allowedTraits` already exist** as namespace-scoped Traits at `org-default-resources/dev/shared/bootstrap/v1.0/cp/` (`python-otel-instrumentation-trait`, `instrumentation-trait-env-injection`, `api-configuration`). App Factory's chart only adds:
   - `ComponentType: app-factory-service` — for App Factory's own backend services. `workloadType: deployment`, `allowedWorkflows: [{kind: ClusterWorkflow, name: dockerfile-builder}, {kind: ClusterWorkflow, name: paketo-buildpacks-builder}]`. `allowedTraits: [{kind: Trait, name: python-otel-instrumentation-trait}, {kind: Trait, name: instrumentation-trait-env-injection}, {kind: Trait, name: api-configuration}]`. Embedded `traits: [{kind: ClusterTrait, name: horizontal-pod-autoscaler, instanceName: ${meta.componentName}-hpa, environmentConfigs: {...}}]`.
   - `ComponentType: app-factory-generated-service` — same shape, used by BFF for service components in user-generated apps.
   - `ComponentType: app-factory-generated-web` — for SPA front-ends in user-generated apps.
   - `ClusterTrait: app-factory-needs-managed-database` — cluster-scoped (since it represents app-factory-platform infrastructure semantics, not platform-shared semantics). `creates`: per-component SecretReference + provisioner Job. `patches`: Deployment with init container that blocks on Job completion. Parameters: `engine`, `size`, `version`.
   - `ClusterWorkflow: app-factory-coding-agent` — **already implemented** (see `docs/design/remote-worker-refactor.md`). Today it lives in the `local-app-factory` submodule under `domains/platform/cluster-shared/cluster-workflows/app-factory-coding-agent.yaml`. Phase 1 moves this YAML, verbatim or lightly adjusted (Helm-templated image tag), into this chart. Current parameter schema: `task` (id, orgId, projectId, componentName, branchName, prompt, correlationId), `repository` (url, identity{name,email,login}), `bff` (bearer), `gitService` (url). The `observability` parameter group is added in Phase 5.
   - ~~`ClusterWorkflowTemplate: app-factory-claude-agent-runner`~~ — **not used.** The current ClusterWorkflow uses an inline `runTemplate.templates[].container` (matches Agent-Manager's `cluster-workflow-monitor-evaluation` pattern), not a separate ClusterWorkflowTemplate. Decision can be revisited in Phase 1 if step-based composition becomes useful.
   - `Project: app-factory-internal` (mirrors agent-manager's `Project: amp`) in `default` namespace.
3. **Lift App Factory runtime into canonical wso2cloud-deployment GitOps** (PRs against `wso2cloud-deployment@main`). **Path layout follows the canonical agent-manager structure exactly** (round-5 verified):
   - `controlplane/common/domains/developers/namespaces/wso2cloud/projects/app-factory/`:
     - `project.yaml` (canonical shape, `deploymentPipelineRef: standard`).
     - `kustomization.yaml` listing the components.
     - `components/app-factory-api/`:
       - `component.yaml` referencing `ComponentType: app-factory-service`, `ClusterWorkflow: dockerfile-builder`, repo URL `wso2-incubator/lab-app-factory@main`.
       - `workload.yaml` (per agent-manager precedent — separate from component.yaml).
       - `release-binding.yaml` — **single file, with `metadata.name: app-factory-api-${environment}` and `spec.environment: ${environment}`**. ${environment} is substituted at deploy time by the GitOps reconciliation, producing one ReleaseBinding per env (dev/stage/prod). Inter-service URLs hardcoded; Thunder URLs point at `platform-idp-${environment}.gateway.${cloud_base_domain}`; `IDP_CLIENT_ID=APP_FACTORY_BFF`; `DB_HOST=${database_host}`; `SECRET_MANAGER_API_URL=https://secret-manager-api.openchoreo.dp.${cloud_base_domain}` — all mirroring `agent-manager-service/release-binding.yaml` exactly.
       - `kustomization.yaml`.
     - Same structure for `app-factory-console`, `app-factory-git-service`, `app-factory-agents-service`. **`app-factory-remote-worker` Component is already dropped** (replaced by `ClusterWorkflow: app-factory-coding-agent`); when Phase 1 lands, the ClusterWorkflow YAML moves out of the submodule into this chart's templates.
   - **Round-5 critical fix:** there is NO `controlplane/common/domains/platform/namespaces/wso2cloud/release-bindings/` hierarchy in canonical wso2cloud-deployment. Release-bindings live next to their component, not in a separate platform-side directory. v5's separate-directory structure was wrong (carried over from local-app-factory's hack layout).
   - Production Environment CR if missing (`05-production_environment.yaml` mirroring `04-staging_environment.yaml`).
4. **Drop everything standalone-only from local-app-factory**:
   - `app-factory/postgresql.yaml` (raw Postgres) → use `${database_host}` platform Postgres. For local-dev, the standalone setup script provisions one.
   - `app-factory/namespace.yaml` (`asdlc-user-projects` namespace) → replaced by per-tenant namespaces in Phase 2.
   - `thunder-idp.yaml` modification registering APP_FACTORY_CONSOLE inline → replaced by `60-app-factory-apps.sh`.
   - `app-factory-coredns-rewrites.yaml` → not needed when using platform-idp's existing DNS.
   - `setup.sh` Thunder client registration (runtime curl) → bootstrap script handles it.
   - `setup.sh` per-service Deployment apply → canonical Flux handles it.
5. **Rewrite `lab-app-factory:deployments-v2/scripts/setup.sh`** for the standalone local-dev mode. Mirrors `agent-manager:deployments/scripts/setup-openchoreo.sh` shape:
   - Bring up cluster + OC planes (existing behavior).
   - Apply `wso2-app-factory-platform-resources-extension` chart.
   - For local-dev only: provision a Postgres + apply local-only ReleaseBindings overlays. (Production deploys land on WSO2 Cloud which already has these.)
   - Register Thunder bootstrap script's clients in the local Thunder.

**Done when:**
- `wso2-app-factory-platform-resources-extension` chart applies cleanly in dev cluster.
- `kubectl get projects -n wso2cloud` returns `core`, `agent-manager`, `app-factory`.
- `kubectl get components -n wso2cloud -l openchoreo.dev/project=app-factory` returns 4 (api, console, git-service, agents-service).
- `kubectl get clustercomponenttypes` includes `app-factory-service`, `app-factory-generated-service`, `app-factory-generated-web`.
- `kubectl get clusterworkflows` includes `app-factory-coding-agent`.
- platform-idp Thunder lists APP_FACTORY_CONSOLE, APP_FACTORY_BFF, APP_FACTORY_SYSTEM, APP_FACTORY_CODING_AGENT clients.
- App Factory PRs merged to canonical `wso2cloud-deployment@main`; reconciled in dev by Flux.

### Phase 2 — Per-tenant OC Namespace + Tenant model (2–3 weeks)

**Goal:** Replace `PLATFORM_API_NAMESPACE_OVERRIDE` with first-class per-tenant namespaces.

1. Add `Tenant`, `User` tables to BFF Postgres; backfill `default-tenant` for existing users.
2. JIT tenant creation on first sign-in (§4.2 flow).
3. **Resolve §10 Q5 BootstrapManifest application mechanism** — round-5 critique flagged this as a Phase 1 / Phase 2 prereq, not just Phase 2. Without this answer, the per-tenant signup flow cannot complete. Current best guess: BFF (using `APP_FACTORY_SYSTEM` client) calls platform-api's tenant-provisioning API. **Must be confirmed with WSO2 Cloud Platform team during Phase 1**, not deferred to Phase 2 kickoff.
4. BFF middleware: load Tenant per request, stamp `ocNamespaceName` onto OC client calls, reject if claim/resource tenant mismatch.
5. All OC resources gain `openchoreo.dev/app-factory-tenant: {tenantId}` label (App-Factory-namespaced label key).
6. Per-user-project: `proj-{projectSlug}` inside `aft-{tenantSlug}`.
7. Remove `PLATFORM_API_NAMESPACE_OVERRIDE` from all ReleaseBindings.

**Tenant identity stability:** `Tenant.id` UUID is immutable. `Tenant.ouHandle` is a lookup key; if it changes (org rename), BFF surfaces an error and admin migration script reconciles. `ocNamespaceName` is also immutable once set.

**Done when:**
- Two test tenants in two distinct OC Namespaces; cross-tenant access denied at all layers.
- `PLATFORM_API_NAMESPACE_OVERRIDE` unset everywhere.

### ~~Phase 3 — Replace `remote-worker` with `app-factory-coding-agent` ClusterWorkflow~~ — **DONE**

Landed as a stand-alone refactor; full plan, decisions, and validation criteria live in `docs/design/remote-worker-refactor.md`. Outcome (vs the original Phase 3 spec):

- ClusterWorkflow `app-factory-coding-agent` defined and applied (currently in the `local-app-factory` submodule under `domains/platform/cluster-shared/cluster-workflows/`; moves into `wso2-app-factory-platform-resources-extension` when Phase 1 lands).
- One-shot runner image (`asdlc.local/app-factory-coding-agent-runner:local`) replaces the long-lived `app-factory-remote-worker` Workload. Built + imported via `dev-cycle.sh` for local k3d.
- BFF: `RemoteWorkerService` → `DispatchService` (issue/branch/PR/Component idempotency unchanged); calls `WorkflowRunService.TriggerCodingAgent` to create the WorkflowRun. `clients/remoteworker/` and `services/remote_worker_service.go` deleted.
- New `coding_agent_watcher` (10s sweep, mirrors `build_watcher`) emits `coding_agent.failed` on terminal pod failure; success transitions still ride the GitHub `pr.ready_for_review` webhook.
- Auth/tokens/credentials unchanged: RS256 task JWT, `gh` wrapper, git credential helper, `/credentials/refresh`. Bearer is still passed as a workflow parameter (out-of-scope follow-up: per-task Secret with `valueFrom.secretKeyRef`).
- Two integration deltas the refactor surfaced and fixed: (a) drop `openchoreo.dev/component` label on coding-agent runs to avoid OC's `ClusterComponentType.allowedWorkflows` validator (use `app-factory.openchoreo.dev/component` instead — see refactor doc §6 for the Option B alternative), and (b) added a supplementary NetworkPolicy in the data-plane namespace permitting cross-namespace ingress to `app-factory-git-service` from `workflows-asdlc-user-projects`.
- Items still owed by the broader migration: callback-style `bff.callbackURL` + `taskJWT` semantics in the parameter schema (today the runner uses `git-service /credentials/refresh` directly, which keeps PR creation in the agent rather than in a BFF callback); `observability` parameter group; promotion of the ClusterWorkflow YAML out of the submodule and into the platform-resources Helm chart. These ride along when Phase 1 + Phase 5 land.

### Phase 4 — Move build trigger to OC autobuild (1–2 weeks)

(Same as v4 with prereq emphasis.)

1. Generated user-app Components: `autoBuild: true`, `workflow.parameters.repository.secretRef: github-credentials`.
2. **Webhook secret:** OC autobuild reads from `git-webhook-secrets` Secret in `openchoreo-control-plane` namespace (verified). Mechanism for App-Factory tenant webhooks to share this secret — §10 Q4. Phase 4 prereq.
3. GitHub repo webhooks: `push` → OC autobuild URL; `pull_request` + `issues` → BFF webhook URL. Same HMAC secret.
4. **Image-update flow open** (§10 Q2): does `dockerfile-builder` auto-update Workload `container.image` via ComponentRelease/RenderedRelease, or must BFF observe and patch? Phase 4 prereq.
5. Delete `WorkflowRunService.TriggerForPush` and BFF push handler.
6. **Rollout gate:** Phase 4 feature-flagged off above 100 active user-projects.

**Done when:**
- `git push` to a generated app's repo creates a `WorkflowRun` in OC without BFF involvement.

### Phase 5 — Observability (1 week, parallel)

1. Modify the 4 App Factory Component CRs to attach `instrumentation-trait-env-injection` per-Component via `Component.spec.traits`. Endpoint: `obs-gateway-gateway-gateway-runtime.openchoreo-data-plane.svc.cluster.local:22893/otel` (mirror agent-manager's value).
2. BFF programmatically attaches `python-otel-instrumentation-trait` for Python user-app components, `instrumentation-trait-env-injection` for non-Python.
3. Coding-agent WorkflowRun receives OTEL config via `observability` parameter group; Argo template substitutes into runner env.
4. Optional: dedicated OpenSearch dashboard.

### Phase 6 — Custom ComponentTypes / Traits adoption (1 week, mostly already done in Phase 1)

(Same as v4.)

1. Update App Factory's own Components to use `app-factory-service` instead of `deployment/service`.
2. BFF defaults user-app Components to `app-factory-generated-service` / `app-factory-generated-web`.
3. BFF attaches `app-factory-needs-managed-database` Trait when design declares managed DB requirement. Old `database-service/` deleted.

### Phase 7 — Cleanup local-app-factory fork (1 week)

1. Update `lab-app-factory:deployments-v2/scripts/lib/submodule.sh` to point at canonical branch (`local`), not `local-app-factory`.
2. Delete `local-app-factory` branch from wso2cloud-deployment (after the ClusterWorkflow YAML and the cross-namespace NetworkPolicy from `deployments-v2/manifests/coding-agent-network-policy.yaml` have moved into the platform-resources chart).
3. Delete `lab-app-factory:deployments/` (already DEPRECATED).
4. Delete `lab-app-factory:database-service/` (replaced by Trait, Phase 6).
5. ~~Delete `lab-app-factory:remote-worker/`.~~ **Already pruned to one-shot runner** in the worker refactor; the directory now contains only the runner image source (Dockerfile, `src/oneshot.ts`, shared `lib/`, plugin) and is built/imported by `dev-cycle.sh`. Keep as-is unless we move runner image publishing to GHCR.
6. Update `CLAUDE.md`, `AGENTS.md`, `docs/design/architecture.md`.
7. Audit setup script for stragglers — every env var injection should map to a ReleaseBinding entry or shared-secret-manager-injected secret.

---

## 6. Specific design decisions (kept from prior versions)

- **WorkflowRun vs Component for coding agent:** WorkflowRun directly.
- **PR creation:** in BFF callback (git-service holds GitHub App). Idempotent on branch name.
- **Task JWT:** RS256, dual-key rotation, revocation table.
- **Postgres:** WSO2 Cloud platform-managed via `${database_host}` (NOT Bitnami subchart).
- **Secrets:** WSO2 Cloud shared `secret-manager-api` (NOT own OpenBao).
- **Thunder:** WSO2 Cloud platform-idp (NOT own Thunder).
- **OC namespace placement:** App Factory's own services in `wso2cloud`; user-tenant resources in per-tenant `aft-{tenantSlug}` namespaces.
- **Hostnames:** auto-generated by OC.
- **Branch promotion:** App Factory PRs flow `main` → `local`/`dev`/`stage`/`prod` per WSO2 Cloud's standard process.

---

## 7. What gets deleted

| Removed | Replacement |
|---|---|
| `local-app-factory` branch of wso2cloud-deployment | Canonical wso2cloud-deployment branches |
| `app-factory/postgresql.yaml` (raw Deployment) | Platform-managed Postgres via `${database_host}` |
| `app-factory/namespace.yaml` (`asdlc-user-projects`) | Per-tenant `aft-{tenantSlug}` namespaces |
| `thunder-idp.yaml` modification (APP_FACTORY_CONSOLE inline) | Thunder bootstrap script `60-app-factory-apps.sh` |
| `app-factory-coredns-rewrites.yaml` | Not needed (use platform-idp's existing DNS) |
| `setup.sh` runtime Thunder client registration | Bootstrap script (declarative) |
| `setup.sh` per-service Deployment apply | Canonical wso2cloud-deployment Flux |
| `PLATFORM_API_NAMESPACE_OVERRIDE` env var | Per-tenant namespace + Tenant model |
| ~~`app-factory-remote-worker` Component~~ — **DONE** | `ClusterWorkflow: app-factory-coding-agent` |
| ~~`lab-app-factory:remote-worker/` HTTP server~~ — **DONE** | `remote-worker/` directory remains, but as a one-shot runner image source (Dockerfile + `src/oneshot.ts` + `src/lib/`). The Express server, routes, JWT middleware, and `taskRegistry` are deleted. |
| ~~`asdlc-service/services/remote_worker_service.go` + `clients/remoteworker/`~~ — **DONE** | `services/dispatch_service.go` + `clients/openchoreo/component_client.go:TriggerCodingAgent`. |
| `lab-app-factory:database-service/` | `ClusterTrait: app-factory-needs-managed-database` |
| `WorkflowRunService.TriggerForPush` | OC `/api/v1alpha1/autobuild` (Phase 4) — note: the build path stays on `TriggerForPush` until then. |
| `lab-app-factory:deployments/` (Docker Compose) | Already DEPRECATED |
| App Factory's own OpenBao seeded by setup.sh | Shared `secret-manager-api` + SecretReference CRs |

---

## 8. Risks and mitigations

| Risk | Mitigation |
|---|---|
| Phase 1 changes touch shared platform layer (Thunder bootstrap scripts, secret-manager paths) | All changes go via canonical wso2cloud-deployment PRs with WSO2 Cloud Platform review. No App-Factory-only shortcuts. |
| BFF coupling to `openchoreo-system-app` shared client must be replaced with `APP_FACTORY_BFF` | Configure via env vars; keep existing path behind a flag during cutover; remove after Phase 1 stable. |
| Per-tenant namespace bootstrap mechanism is undefined (§10 Q5) | **Phase 1 + Phase 2 prereq** (round-5: signup flow can't function without it). Likely platform-api invocation; confirm during Phase 1. |
| Per-tenant SecretReference for `github-credentials` per tenant | BFF creates SecretReference at tenant signup pointing to `secret/data/app-factory/tenants/{tenantId}/github-credentials` in shared OpenBao. Per-tenant secret rotation handled by BFF admin tooling. |
| OC autobuild webhook secret cross-namespace constraint | §10 Q4 — Phase 4 prereq. |
| OC autobuild O(N) scan at scale | Rollout gate at 100 user-projects; revisit. |
| Coding-agent WorkflowRun in tenant namespace must reach git-service for `/credentials/refresh` | Today: BFF passes a cross-namespace FQDN (`AGENT_GIT_SERVICE_URL`) and a supplementary NetworkPolicy in the data-plane namespace permits ingress from `workflows-*`. When per-tenant namespaces land in Phase 2, the NetworkPolicy template generalises to `from: namespaceSelector: matchExpressions: [{key: kubernetes.io/metadata.name, operator: In, values: [workflows-aft-...]}]` (or just match on a workflow-plane label). |
| ~~Migration of in-flight tasks during Phase 3 cutover~~ — N/A | Phase 3 already cut over (no flag — straight replace per refactor doc). |
| Disaster recovery for runtime CRs | BFF rehydrate runbook; runtime artifacts (built images, in-flight WorkflowRuns) NOT BFF-recoverable, rebuild on next push. |
| Tenant.id immutability if `ouHandle` changes | Refuse login on mismatch; admin migration script. |
| Trait Job + Deployment race | Init container blocks on Job. |
| Local-dev environment needs its own Thunder/OpenBao/Postgres | The standalone setup script (`lab-app-factory:deployments-v2/scripts/setup.sh`) brings these up locally. Production deployments on WSO2 Cloud do not. |

---

## 9. Sequencing

```
[Phase 3 — workflow worker]  ✅ DONE (delivered ahead via remote-worker-refactor.md; ClusterWorkflow today lives in the local-app-factory submodule and migrates into the platform-resources chart in Phase 1)

Phase 1 (canonical GitOps + platform-resources chart + Thunder bootstrap) ─┬─→ Phase 2 (per-tenant namespaces)
                                                                          │
                                                                          ├─→ Phase 4 (autobuild) ── (gated on §10 Q2 + Q4)
                                                                          │
                                                                          ├─→ Phase 5 (observability) ── parallel
                                                                          │
                                                                          └─→ Phase 6 (CT/Trait adoption) ── parallel after Phase 1

Phase 7 (cleanup) ─── after 1, 2, 4 green in stage
```

---

## 10. Open questions for OC and WSO2 Cloud teams

**OC team:**

1. **Per-tenant namespace scaling.** Hundreds of OC Namespaces, ~10 Components each. Tested at this scale? Per-cluster ceilings?
2. **Image-update flow after autobuild WorkflowRun completes.** Does the build pipeline (`dockerfile-builder` etc.) automatically update the Workload's `container.image` via ComponentRelease/RenderedRelease, or must BFF observe completion and patch? **Phase 4 prereq.**
3. **Autobuild O(N) component scan.** Acceptable at 100+ user-projects? Petition for namespace-scoped lookup?
4. **ESO cross-namespace targeting for autobuild webhook secret.** Can a SecretReference produce a Secret in `openchoreo-control-plane` namespace from a different namespace? **Phase 4 prereq.**
5. **Per-tenant BootstrapManifest application mechanism.** What's the supported way for a SaaS BFF (using a system OAuth client) to invoke per-tenant namespace bootstrap? Is there a `platform-api` API endpoint, or does it watch for new namespaces? **Phase 2 prereq.**
6. **Trait cross-resource patching.** Confirm a Trait can `patches` resources created by the ComponentType (e.g., the Deployment), not just Trait-`creates`'d resources. Agent-Manager does this for `python-otel-instrumentation-trait`.
7. **Per-tenant SecretReference provisioning.** Does Agent-Manager (or any other multi-tenant SaaS on WSO2 Cloud) have a documented pattern for per-tenant SecretReference creation, or is this each app's responsibility?

**WSO2 Cloud Platform team:**

8. **Branch promotion timing.** How long does it take for a Project added to `wso2cloud-deployment@main` to promote to `local`/`dev`/`stage`/`prod`? Any review/CAB gates?
9. **Production Environment CR.** `staging` exists in `org-default-resources/04-staging_environment.yaml`; production does not. Is this an oversight?
10. **App Factory as a peer Project.** Any objection to App Factory living as a Project under `wso2cloud` namespace, alongside `core` and `agent-manager`?
11. **Per-tenant OUs on platform-idp.** Long-term position on per-tenant OUs? If activated, App Factory inherits.
12. **Thunder bootstrap script numbering.** What's the next available number for a new app's Thunder bootstrap script in `controlplane/common/init/layer-2/thunder/setup-scripts/`? (60 in v5 is a guess; current scripts go 50–59.)
13. **App-factory-specific OpenBao paths.** Confirm pattern: `secret/data/app-factory/tenants/{tenantId}/...` for per-tenant secrets, `secret/data/app-factory/clients/...` for App-Factory-platform clients. Acceptable to WSO2 Cloud's secret-manager-api governance?

---

## Changelog

- **v6.2 (2026-05-06)** — Worker refactor (formerly Phase 3) landed. Replaced full Phase 3 spec with a stub linking to `docs/design/remote-worker-refactor.md` and a short outcome list. Updated §0.3 inventory (4 backend Components, not 5; remote-worker row marked Done), §1 drivers (third driver resolved), §3.2 OC-owned (DispatchService wired today), §3.3 chart contents (ClusterWorkflowTemplate dropped — the current implementation uses an inline runTemplate, matching agent-manager's `cluster-workflow-monitor-evaluation`), §5 Phase 1 (ClusterWorkflow YAML moves out of submodule into chart at Phase-1-time), §7 deletion table (clarified what's already removed vs what stays), §8 risks (Phase 3 cutover risk N/A; cross-namespace git-service reachability captured), §9 sequencing diagram (Phase 3 marked done). Two non-trivial deviations from the original Phase 3 plan are now noted: (a) the ClusterWorkflow uses `app-factory.openchoreo.dev/component` labels instead of `openchoreo.dev/component` to bypass `allowedWorkflows` validation (Option B alternative documented in the refactor doc), and (b) PR creation is still done by the agent in-pod (`gh pr ready`) rather than via a BFF callback — `bff.callbackURL` + `taskJWT` callback semantics from the v4 schema are not currently in use.
- **v6 (2026-05-06)** — Round-5 critique closure. **Critical structural corrections** the WSO2 Cloud critic surfaced (after both critics had previously approved through round-3 against incorrect baseline assumptions):
  - **§5 Phase 1 item 3 ReleaseBinding paths corrected.** v5 proposed `controlplane/.../domains/platform/.../release-bindings/app-factory/{name}/{name}-{env}.yaml` (carried over from local-app-factory's hack layout). Canonical pattern verified: ReleaseBindings live INSIDE component directories at `controlplane/.../domains/developers/namespaces/wso2cloud/projects/{project}/components/{component}/release-binding.yaml`. ONE file per component, with `name: {component}-${environment}` and `spec.environment: ${environment}` substituted at deploy time.
  - **§3.3 / §4.3 ComponentType correction.** v5 said `ClusterComponentType` for `app-factory-service` etc. Round-5 verified that agent-manager's `agent-api` is `kind: ComponentType` (namespace-scoped to `default`), not ClusterComponentType. ComponentType's `allowedTraits` accepts both `kind: Trait` (namespace-scoped) and `kind: ClusterTrait`. App Factory follows this pattern — uses `ComponentType`, lists `python-otel-instrumentation-trait`, `instrumentation-trait-env-injection`, `api-configuration` (namespace-scoped Traits) in `allowedTraits` declaratively. BFF's per-Component attachment via `Component.spec.traits` is the canonical Agent-Manager pattern (mirrors `buildCreateTraitRequests`), not a workaround.
  - **§3.4 Thunder script granularity decision.** v5 proposed one multi-client script (`60-app-factory-apps.sh`). Round-5 noted canonical convention is one-script-per-client (`51-backstage-app.sh`, `53-rca-agent-client.sh` etc.). v6 splits into 4 scripts (`60-app-factory-console-app.sh`, `61-app-factory-bff-client.sh`, `62-app-factory-system-client.sh`, `63-app-factory-coding-agent-client.sh`).
  - **§3.4 Administrator role clarification.** Bootstrap script registers OAuth client; role assignment is a separate Thunder admin API call. Verify pattern against existing `55-system-app.sh` when authoring `62-app-factory-system-client.sh`.
  - **§5 Phase 2 item 3 + §8 Risks: BootstrapManifest mechanism is now a Phase 1 prereq**, not just Phase 2. Without it, the per-tenant signup flow cannot function.
- **v5 (2026-05-06)** — Round-4 critique closure. **Major insight:** Agent-Manager has TWO deployment modes (standalone via setup-openchoreo.sh; on WSO2 Cloud via wso2cloud-deployment GitOps). The on-WSO2-Cloud mode uses **none** of the wso2-amp-thunder/secrets/parent-Postgres charts — it uses platform-idp Thunder, shared secret-manager-api, and platform-managed Postgres (verified by reading `agent-manager-service/release-binding.yaml` on `origin/main`). **Major changes vs v4:**
  - **§3.3 Helm chart inventory shrinks from 4 to 1.** Only `wso2-app-factory-platform-resources-extension` survives. `wso2-app-factory-thunder-extension`, `wso2-app-factory-secrets-extension`, parent `wso2-app-factory` (Bitnami Postgres) all dropped.
  - **§4.1 Two-Thunder split removed.** Use platform-idp for everything Thunder-related.
  - **§3.4 Thunder OAuth clients registered via new bootstrap script** `controlplane/common/init/layer-2/thunder/setup-scripts/60-app-factory-apps.sh`, not via a separate App-Factory chart.
  - **§4.3 Trait attachment is programmatic, not declarative.** `allowedTraits` only accepts ClusterTraits per OC schema; namespace-scoped traits (python-otel etc.) are attached per-Component by BFF, mirroring Agent-Manager's `buildCreateTraitRequests`+`AttachTraits`.
  - **§4.2 BootstrapManifest application mechanism** flagged as Phase 2 prereq (§10 Q5).
  - **§4.2 Per-tenant namespace pattern correctly framed as App-Factory-specific design choice** (Agent-Manager's WSO2 Cloud deployment is single-namespace; the OC API surface supports multi-namespace which we use).
  - **§3.4 + §10 Q12** Thunder bootstrap script numbering opened as question.
  - **§4.2 + §10 Q13** OpenBao path conventions opened as question.
  - **§5 Phase 1 fully restructured** around the simplified one-chart inventory + canonical PR plan + Thunder bootstrap script.
- **v4 (2026-05-06)** — Reframed against canonical Agent-Manager + wso2cloud-deployment patterns. Acknowledged local-app-factory branch is our WIP (not authoritative). Proposed 4-chart inventory (later corrected to 1 in v5).
- **v3.1 (2026-05-05)** — Round-3 closure. OTEL parameter schema, hostname example, BFF code change clarification.
- **v3 (2026-05-05)** — Round-2 critique. Trait scope, hostname pattern, production Environment, namespace scaling, autobuild rollout gate, Tenant identity stability, disaster recovery.
- **v2 (2026-05-05)** — Round-1 critique. Label namespace, dropped App-Factory-specific Thunder (later reversed in v4 then re-dropped in v5), task JWT RS256, autobuild secret location, image-update gap, dropped LLM-quota trait.
- **v1 (2026-05-05)** — Initial draft (incorrect assumption about App Factory services not being in GitOps).
