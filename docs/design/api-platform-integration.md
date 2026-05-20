# API Platform integration — production implementation plan (v6)

**Status:** Phases 1, 2, 3, 4, 5, 6, 7 ✅ implemented. Substrate verified (`deployments/POC-API-PLATFORM.md`).

**Branch:** `poc-api-platform` (tracking `upstream/design-revamp`).

**Audience:** the next person (likely Claude after compaction) who picks this up. Read this end-to-end before opening editors. The two files you must read first are this doc and the POC log.

**Revision history:**
- v1: initial proposal post-POC. Phase order `1→2→3→4→5→6→7`. Per-component OAuth clients. Build-workflow as the trait emitter. Audience as opaque ULID written into `aud` claim.
- v2: architect review applied. Phase order `1→2→3→6→4→5→7`. Per-org OAuth clients (agent-manager precedent). BFF as the trait emitter. Audience = `client_id`-as-discriminator (C8). Added Trust-root matrix, Observability, Failure modes.
- v3: architect round-2 blockers applied. Phase 2 trigger model reworked — `services/trait_sync.go` + 2 write sites + watcher. First-deploy race documented. §10 rotation corrected. §12 split. Component deletion added. 4th trust root.
- v4: architect round-3 residuals applied. R1 mutex, R2 autoDeploy:false for protected, R3 cleanup fallback, R4 bidirectional drift.
- v5: architect round-4 cleanups. Singleflight removed from R1 spec. R2 gains a prerequisite-spike clause.
- v6 (this version): IMPLEMENTATION-COMPLETE writeup. autoDeploy:true confirmed for protected components (manual-deploy UI would be needed for false; deferred). R3 dropped as unnecessary — OC's RenderedRelease finalizer handles trait-emitted resource GC via Status.Resources tracking. Phase 6 reframed: edit our own vendored trait + gateway-config in `deployments/manifests/api-platform/` (no `wso2cloud-deployment` dependency — `deployments/` is pure OC). Prometheus observability dropped — structured slog logs are sufficient for v1.

---

## 1. Goal

Let App Factory users mark any component's HTTP endpoint as **authenticated** or **public** from `design.md`, and have that flow all the way through to a deployed component whose API is enforced by the WSO2 API Platform gateway, with tokens validated against the organisation's IDP (Thunder v1; Asgardeo / custom OIDC v2).

### Truth table (already passing in POC, must remain green throughout):

|                | with valid token | without token |
|----------------|------------------|---------------|
| **protected**  | 200              | 401 at AP gateway |
| **public**     | 200 (token ignored) | 200 |

---

## 2. Non-goals (v1)

- **No multi-IDP per cluster** until Phase 6 lands. Every component v1 trusts the cluster's single keymanager (Thunder).
- **No per-endpoint or per-operation granularity.** One toggle per component.
- **No OpenAPI-derived operation matrix** — trait's `operations[]` defaults to `* /*` for all methods.
- **No PKCE termination at the gateway.** Thunder remains the OIDC AS; AP only validates resulting JWTs.
- **No backend-level audience scoping enforced by the platform.** End-user/per-org authz is the user's app's responsibility (matches agent-manager). The gateway answers "is this JWT signed by a configured keymanager?" — that's it.

---

## 3. What the POC proved

(Captured in detail in `deployments/POC-API-PLATFORM.md`; this is the must-know summary.)

- AP operator (`gateway-operator` v0.4.0, runtime image v0.9.0) installs via plain `helm install`. Folded into `deployments/scripts/setup-prerequisites.sh` step 6.
- Canonical `api-configuration` ClusterTrait reconciles correctly — produces a kgateway `Backend (Static)` → AP router, a `RestApi` CR, and patches the OC-generated `HTTPRoute`'s backendRef + URL rewrite. Vendored at `deployments/manifests/api-platform/api-configuration-trait.yaml`.
- `service` ClusterComponentType in `deployments/scripts/setup-asdlc.sh` patched to add `allowedTraits: [api-configuration]`.
- Reconciled resources land in `dp-<env>-<project>-<hash>` namespace.
- `gateway-config` ConfigMap already has Thunder JWKS keymanager (`PlatformIDPKeyManager`, issuer `http://thunder.openchoreo.localhost:8080`, JWKS `http://thunder-service.thunder.svc.cluster.local:8090/oauth2/jwks`) under `jwtauth_v0`. Trait emits `version: v0` — matches.
- Tokens from ANY confidential client registered in Thunder validate (`jwt-auth v0` emits no `audience` or `issuers` filter).

### Hard constraints (C1–C7 from POC)

| # | Constraint |
|---|---|
| C1 | `Component.spec.componentType.name` MUST be `deployment/service` (with workloadType prefix). Bare `service` fails CRD validation. |
| C2 | Trait-produced resources live in `dp-<env>-<project>-<hash>` namespace. Discover via `ReleaseBinding.status.endpoints[].serviceURL.host`. |
| C3 | AP router 404s on bare context path without trailing slash. Always append `/` before handing URLs to callers. |
| C4 | Thunder admin API auth from outside the image's bootstrap path is solved by the `client_credentials` + `scope=system` pattern (agent-manager precedent — see D7 below). |
| C5 | `jwt-auth v0` policy accepts any token from any keymanager registered cluster-wide. Per-RestApi `issuers` filter requires `v1` policy → Phase 6. |
| C6 | AP gateway-runtime in chart v0.9.0 bundles policy-engine in-process — no separate Deployment. |
| C7 | Trait hardcodes backend target `api-platform-default-gateway-gateway-runtime.openchoreo-data-plane:8080`. APIGateway rename or namespace change requires trait edit. Single-pod runtime = SPOF for every protected component on the cluster (operational note, see D1). |

### Thunder constraint (NEW, post-architect review)

| # | Constraint |
|---|---|
| **C8** | Thunder hardcodes JWT `aud` claim to the literal string `"application"` (per `wso2-amp-thunder-extension/values.yaml:118`). Custom-per-app audience is NOT a Thunder feature. The per-app discriminator that ends up in the JWT is `client_id` (visible as `sub` and `client_id` claims). All audience-based filtering in this plan refers to `client_id`-as-discriminator, not the actual `aud` claim. |

---

## 4. Production architecture

### 4.1 Request path

```
end-user
   │
   ▼
Public DNS → Kgateway listener
   │
   ▼  HTTPRoute (generated by `service` ClusterComponentType,
   │              patched by `api-configuration` trait)
   │  match:   PathPrefix /<comp>-<endpoint>
   │  rewrite: /<env>-<ns>-<comp>-<endpoint>
   │  backend: kgateway Backend (Static) → AP router
   ▼
AP gateway-runtime (Envoy + in-process policy-engine — SINGLE POD, see C7/D1)
   │  match: RestApi.context = /<env>-<ns>-<comp>-<endpoint>
   │  policies: cors / jwt-auth(v0 in v1, v1 from Phase 6) / rate-limit / add-headers
   │  jwt-auth validates against PlatformIDPKeyManager (Thunder JWKS)
   ▼
Component Service in dp-<env>-<project>-<hash>.svc.cluster.local
   ▼
Pod
```

### 4.2 Where things live

| Layer | Resource | Owner | File / location |
|---|---|---|---|
| Cluster | AP operator (`gateway-operator` v0.4.0) | platform (`setup-prerequisites.sh` step 6) | helm install |
| Cluster | AP runtime config ConfigMap | platform | `deployments/manifests/api-platform/gateway-config.yaml` |
| Cluster | `APIGateway` CR | platform | `deployments/manifests/api-platform/api-gateway.yaml` |
| Cluster | `api-configuration` ClusterTrait | platform | `deployments/manifests/api-platform/api-configuration-trait.yaml` |
| Cluster | RBAC for OC dataplane SA → RestApi | platform | `deployments/manifests/api-platform/rbac.yaml` |
| Cluster | `service` ClusterComponentType + `allowedTraits` | platform | inline in `deployments/scripts/setup-asdlc.sh` |
| Cluster | Thunder bootstrap of system + publisher clients | platform | `deployments/single-cluster/values-thunder.yaml` (CONFIDENTIAL_APPS list + new bootstrap scripts) |
| Per-org | IDP profile (kind, issuer, jwks_url, etc.) | App Factory BFF | new PostgreSQL table |
| Per-org | OAuth publisher client (`asdlc-publisher-<orgHandle>`) | App Factory BFF → Thunder | created at org provisioning |
| Per-org | Publisher client secret | OpenBao | `secret/asdlc/<orgId>/idp/publisher` |
| Per-component | `design.md` frontmatter `api.security` | user (via console / agent) | `specs/design/components/<name>/design.md` |
| Per-component | `Component.traits[]` | App Factory BFF (`componentService.CreateComponent` / `ensureOCComponent`) | written at component create/update |
| Per-component (per env) | `ReleaseBinding.traitEnvironmentConfigs.*` | App Factory BFF (`config_service.go` "Mirror onto each environment's ReleaseBinding") | written at deploy time |

---

## 5. Data model changes

### 5.1 `design.md` frontmatter (NEW)

Today: `type, language, dependsOn, buildpack, appPath, entrypoint` (Go struct at `asdlc-service/services/artifact_store.go:226-233`; Zod schema at `agents/src/agents/architect/schema.ts:5-53`).

Add:

```yaml
api:
  security: required | none   # required → trait sets jwtAuth.enabled=true in every env's RB
```

- `api` is optional. Absent → `security: none`.
- **No `audience` field.** Per C8, audience-as-discriminator is `client_id`, set by Thunder, not by us. The org's publisher client carries the org identity; per-component scoping is the user-app's responsibility.

### 5.2 PostgreSQL — `organization_idp_profiles`

```sql
CREATE TABLE organization_idp_profiles (
  id            UUID PRIMARY KEY,
  org_id        UUID NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
  kind          TEXT NOT NULL CHECK (kind IN ('platform', 'asgardeo', 'custom')),
  issuer        TEXT NOT NULL,
  jwks_url      TEXT NOT NULL,
  admin_creds_secret_ref TEXT,                     -- OpenBao path; NULL for kind=platform
  publisher_client_id   TEXT,                      -- e.g. asdlc-publisher-<orgHandle>; populated after Thunder app creation
  publisher_secret_ref  TEXT,                      -- OpenBao path to publisher client_secret
  created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
  CONSTRAINT one_profile_per_org UNIQUE (org_id)
);
```

- v1: every row has `kind='platform'`, `issuer='http://thunder.openchoreo.localhost:8080'` (read from cluster config at org provisioning), publisher client created on demand at first protected-component deploy.
- v2: console UI lets org admins choose `kind`, providing the IDP-specific fields.

### 5.3 OAuth client model — per-ORG, not per-component

**Per agent-manager precedent** (`amp-publisher-<orgName>` at `agent-manager-service/clients/thundersvc/client.go:205`):

- One `client_credentials` app per org in Thunder, name `asdlc-publisher-<orgHandle>`.
- Created idempotently by the BFF (`EnsurePublisherApp` mirroring `client.go:39-54`).
- Client_secret persisted in OpenBao at `secret/asdlc/<orgId>/idp/publisher`.
- Per-env separation in v1 is **not exposed at the IDP level** — same client across dev/stage/prod. Per-env clients are a Phase 7 refinement when the impact analysis says it's worth the rotation complexity.

Per-component identity (when a user app wants to do its own authz) lives in the JWT's `client_id` / `sub` claim, which equals the publisher app's clientId. App-level authz inside the user's app reads this claim. The gateway does NOT distinguish.

**Note on agent-manager analogues we deliberately don't replicate:** agent-manager registers four Thunder clients (`amp-system-client`, `amp-publisher-client` template, `amp-console-client`, plus per-org `amp-publisher-<org>`). App Factory only needs two: `asdlc-system-client` (for admin API) and per-org `asdlc-publisher-<orgHandle>` (for user-app outbound). We skip the console-client analogue because App Factory's console talks to its own BFF (not Thunder directly for application protection — Thunder is only for App Factory's own login PKCE flow which already has `asdlc-console-client` configured separately). We skip the publisher-template-client because we mint per-org dynamically rather than from a static template app.

---

## 6. Implementation phases

**Order: `0 (done) → 1 → 2 → 3 → 6 → 4 → 5 → 7`.**

Critical: **Phase 6 must land before Phase 4**, because Phase 4 (UI) is what makes it possible for an org admin to plug in a second IDP — and `C5` means a second IDP without `jwt-auth v1` filtering = every component on the cluster trusts every IDP.

### Phase 0 — Substrate (DONE)

- [x] AP install in `setup-prerequisites.sh`
- [x] Trait + `allowedTraits` patch in `setup-asdlc.sh`
- [x] POC manifests + `verify-api-platform.sh` proves the truth table

**Acceptance**: `bash deployments/scripts/verify-api-platform.sh` exits 0.

### Phase 1 — design.md schema + read path + URL helper ✅

**Goal:** App Factory parses `api.security` without breaking existing components, and exposes a canonical "render this endpoint URL" helper everywhere.

**Changes:**
- `asdlc-service/services/artifact_store.go:226-233` — `componentFrontmatter` gains `Api *apiConfig` with `Security string`. `nil` Api block ⇒ `security="none"` semantics.
- `agents/src/agents/architect/schema.ts:5-53` — `SlimComponent` gains optional `api: { security: 'required' | 'none' }`.
- `asdlc-service/services/api_security.go` (new) — `func ResolveAPISecurityEnabled(fm componentFrontmatter) bool`. Single source of truth.
- **NEW: `asdlc-service/services/external_url.go` (new) — `func NormalizeExternalURL(rawPath string) string`** appends `/` if missing. Mandatory for any code that reads `ReleaseBinding.status.endpoints[].externalURLs.http.path` (C3). Console-side equivalent in `console/src/lib/externalUrl.ts`.
- Tests: round-trip frontmatter with + without `api`; URL normaliser table.

**Acceptance**: BFF returns `api.security` on GET `/components/<name>`; rendered URLs always end in `/`; no regression to components lacking the field.

### Phase 2 — BFF trait emission via `trait_sync` service ✅

**Architect round-2 corrected. v2's location was right (BFF, not build workflow) but the *trigger* was wrong.** `config_service.go:68` runs ONLY on env-var edits — it is not a per-deploy hook. There is no single existing hook that fires "on every state change that affects trait shape." We have to add one.

**Two write triggers, one shared emitter — under a per-component mutex:**

1. **New service: `asdlc-service/services/trait_sync.go`** (single-responsibility):
   ```go
   func SyncComponentTraits(ctx, orgID, projectID, componentName string) error
   ```
   Steps inside (ORDER MATTERS — R1 fix for write-write races between Site 1, Site 2, and the watcher):
   1. **Acquire per-component mutex** keyed `(orgID, projectID, componentName)`. Use an in-process `sync.Map` of `*sync.Mutex` — **NOT `singleflight`**. Singleflight coalesces duplicate calls (returns the in-flight call's result to later callers and skips their work), which is wrong here: a design PUT that lands while the watcher is running must trigger its own re-read after the watcher finishes, not piggyback on the watcher's stale read. Lock is held only for the duration of one Sync call.
   2. Read `design.md` frontmatter via `ArtifactStore.GetDesignFile` **AFTER lock acquisition** — never read design before locking. Otherwise a concurrent edit on Site 2 can write a newer version while we're mid-PATCH with stale read.
   3. Resolve desired state: `enabled := ResolveAPISecurityEnabled(fm)`.
   4. Read existing Workload to get endpoint names + ports.
   5. Build the desired `traits[]` slice (one `api-configuration` trait instance per HTTP endpoint when enabled; empty slice when disabled — explicit empty is critical for the "unprotect" case).
   6. PATCH the OC Component CR with the desired traits slice (idempotent — keyed by `instanceName`). Use `resourceVersion` from the read for optimistic concurrency — retry on 409.
   7. For every existing ReleaseBinding for this component (`ListDeployments`), PATCH `traitEnvironmentConfigs[<inst>]` per env. **Skip without erroring if no RBs exist yet** — that's the first-deploy race (see R2 below); the watcher catches up. Log a structured event for the reconciler.

2. **Call site 1 — first deploy / dispatch path** (`dispatch_service.go:712`, `ensureOCComponent`):
   - **R2 fix — for protected components, do NOT use `autoDeploy: true`.** The race is unavoidable with autoDeploy because OC creates the RB with empty `traitEnvironmentConfigs` and the trait's schema default for `jwtAuth.enabled` is `false` (`api-configuration-trait.yaml:90`) — leaving a 10s+ window where the component is "protected in design" but the gateway has no JWT validation.
   - For protected components: create Component with `autoDeploy: false` + `traits[]` set, then **synchronously create one ReleaseBinding per environment** with `traitEnvironmentConfigs.<inst>.jwtAuth.enabled=true` populated from the start. No window exists where an RB lacks the env config.
   - For unprotected components: keep `autoDeploy: true` (today's behaviour).
   - Mode change at runtime (security `none → required` on an existing component) is rare and OK to absorb via the watcher's 10s convergence — it's a deliberate user action, not first-deploy.

   - **PREREQUISITE SPIKE (must run before Phase 2 implementation)**: confirm OC's controller reconciles a BFF-created ReleaseBinding for an `autoDeploy: false` Component end-to-end (i.e. that the Deployment, Service, HTTPRoute, RestApi, Backend all appear correctly in the dp-namespace). The POC verified the trait path with `autoDeploy: true` only. Run the spike against `verify-api-platform.sh` modified to use `autoDeploy: false` + an explicit RB. If OC requires `autoDeploy: true` for project→environment→RB binding logic and reconcile fails, **fall back**: use `autoDeploy: true` even for protected components AND accept the 10s+ exposure window AND raise the `missing_protection` drift metric threshold from "any persistent non-zero" to "non-zero for > 30s" so the watcher catches up before alerting. Document the fallback decision (with the OC behaviour evidence) inline in `dispatch_service.go` so future readers understand why the codepath looks the way it does.

3. **Call site 2 — design edit path** (`designService.UpdateDesignFile`):
   - AFTER `design.md` is persisted to git, fire `SyncComponentTraits` synchronously (acquires the mutex). Errors are logged but not propagated to the user — design tree is canonical and the watcher backstop ensures convergence.
   - If the user is toggling `none → required` AND the component currently has `autoDeploy: true` (because it was created as unprotected), `SyncComponentTraits` keeps `autoDeploy` as-is and patches `traits[]` + per-env RBs. The next dispatch picks up the new shape.

4. **Reconciler — `asdlc-service/services/webhook/trait_sync_watcher.go`** (new):
   - 10s cadence sweep. For every Component with `api.security` in design, compare desired vs actual state on the Component CR AND every RB.
   - Re-runs `SyncComponentTraits` for drift cases. Convergent.
   - Per-component retry budget: 5 consecutive failures pauses that component for 5min to avoid pinning a permanently-broken design.

5. **NOT inside `config_service.go`** — that path is env-var-only.

**First-deploy race against OC autoDeploy (R2-closed):**

The race exists only for `autoDeploy: true` Components. By using `autoDeploy: false` + BFF-managed RBs for protected components, the trait_sync emitter has full ownership of when and how the RB exists. No 10-second exposure window.

**Component deletion (R3 — explicit cleanup fallback):**

The architect flagged that the trait template (`api-configuration-trait.yaml`) does NOT stamp `ownerReferences` on the `creates` resources (Backend, RestApi). Cascade-delete via OC owner refs is therefore NOT guaranteed end-to-end. Implementation MUST verify at integration-test time AND provide a fallback:

- Hook from `designService.DeleteComponent`:
  1. Call `componentService.DeleteComponent` against OC API to remove the OC Component CR.
  2. List `Backend` and `RestApi` resources in the dp-namespace whose name matches `<component>-<endpoint>-api-gw-backend` / `<component>-<endpoint>` patterns from the trait template (`api-configuration-trait.yaml:206,224`). **Explicitly delete any that remain** after waiting 5s for OC cascade. Idempotent — 404s are fine.
  3. Track the dp-namespace via `ReleaseBinding.status.endpoints[0].serviceURL.host` parsing (cached on Component metadata if available).
- If `EnsureOrgPublisher` is no longer needed for this org (no remaining protected components), do NOT auto-revoke — publisher is per-org, leaves alive until org delete.
- Audit: log component delete + cascade outcome (cascaded-by-oc vs cleaned-up-by-bff) into `idp_audit_events`. Drift detection over time tells us whether owner refs are actually working.

**Concrete file changes:**

- `asdlc-service/services/trait_sync.go` (new) — `SyncComponentTraits` + helpers.
- `asdlc-service/services/dispatch_service.go:712` — invoke `SyncComponentTraits` after `CreateComponent`.
- `asdlc-service/services/design_service.go` (write paths around `UpdateDesignFile`) — invoke `SyncComponentTraits` after persisting design.
- `asdlc-service/services/design_service.go` (`DeleteComponent`) — invoke `componentService.DeleteComponent`.
- `asdlc-service/services/webhook/trait_sync_watcher.go` (new) — periodic reconciler.
- `asdlc-service/clients/openchoreo/types.go` — confirm Component type carries `traits[]`; regenerate from OC OpenAPI if not present.
- `tests/api/baseline_diff_test.go` (new) — no-`api` component yields bit-for-bit identical Component+RB to pre-Phase-2 baseline.
- `tests/api/trait_sync_test.go` (new) — toggle `security:none→required` via design PUT → assert trait appears on Component CR and on every RB within one watcher cycle.

**Acceptance**: Component with `api.security: required` deploys end-to-end. Toggling via design PUT propagates within one watcher cycle. `verify-api-platform.sh` (extended to use BFF-created components) passes truth table. Baseline-diff test passes for unprotected components.

### Phase 3 — Per-org OAuth client lifecycle (with system-client bootstrap) ✅

**Architect-corrected naming and pattern. Mirrors agent-manager `client.go:71-251` verbatim.**

**Pre-work — Thunder system client bootstrap:**

- Add row to `deployments/single-cluster/values-thunder.yaml` CONFIDENTIAL_APPS:
  ```
  "ASDLC System|ASDLC platform-level system client for Thunder admin API|asdlc-system-client|asdlc-system-client-secret"
  ```
- Add a new bootstrap script `60-asdlc-system-role.sh` (similar to existing 53/55 in agent-manager) that **assigns Thunder's `Administrator` role to `asdlc-system-client`**. CRITICAL: this is Thunder-side role, NOT OpenChoreo's `ClusterAuthzRoleBinding`. The v1 plan got these confused — fixed here. ClusterAuthzRoleBinding gates OC API access; Thunder admin needs a Thunder role assignment.
- Store `asdlc-system-client-secret` in OpenBao at `secret/asdlc/system/thunder-admin` via the existing `values-openbao.yaml` postStart bootstrap (extend the seeded paths list).

**Changes:**
- `asdlc-service/clients/thundersvc/client.go` (new package, modelled on `agent-manager-service/clients/thundersvc/client.go`):
  - `getSystemToken(ctx)` — singleflight-cached client_credentials token mint with `scope=system`. Token reused until expiry-leeway.
  - `EnsurePublisherApp(ctx, orgHandle, ouId) (clientId, clientSecret, created, err)` — idempotent. Find by name `asdlc-publisher-<orgHandle>`; create if absent; return existing on conflict (clientSecret only returned on creation, see `client.go:213`).
  - `DeletePublisherApp(ctx, orgHandle) (deleted, err)` — for org deprovisioning.
  - `RegenerateClientSecret(ctx, orgHandle) (newSecret, err)` — emergency rotation.
- `asdlc-service/services/idp_service.go` (new):
  - `EnsureOrgPublisher(ctx, orgId) (clientId, secretRef, err)` — calls `EnsurePublisherApp`, writes secret to OpenBao at `secret/asdlc/<orgId>/idp/publisher`, returns the OpenBao path. Stamps `publisher_client_id` + `publisher_secret_ref` onto `organization_idp_profiles`.
  - `RevokeOrgPublisher(ctx, orgId) error` — calls `DeletePublisherApp`, deletes the OpenBao path.
- Hook into existing org-creation path so `EnsureOrgPublisher` runs on first protected-component deploy (lazy — most orgs won't have protected components).
- **Re-protection (D-new)**: when a component flips `security: none → required` later, no IDP-side change needed (the org publisher already exists). When the org is deleted, `RevokeOrgPublisher` runs.
- **Audit log**: every Ensure / Revoke / Regenerate writes a row to a new `idp_audit_events` table (`org_id, action, actor, timestamp, before_state, after_state`).

**Acceptance**: First org's first protected component triggers `EnsureOrgPublisher`; OpenBao contains `secret/asdlc/<orgId>/idp/publisher`; verify script can mint a token via that client and pass the truth table.

### Phase 6 — `api-configuration` trait v1 extension (in-repo, no upstream PR) ✅

**Reframed from v2 of this doc.** The `deployments/` setup vendors its own copy of the trait and `gateway-config.yaml` (no `wso2cloud-deployment` submodule); we edit those files directly. Coupled upgrade: trait schema + `gateway-config.yaml` `jwtauth_v1` block + AP runtime image bump (v0.9.0 → v1.0.0). **Cannot be split** — partial application leaves the cluster broken.

**Changes (all in-repo):**
- `deployments/manifests/api-platform/api-configuration-trait.yaml` — extend `jwtAuth` schema with `issuers: []string`, `audience: []string`. CEL emission switched to `version: v1`, forwards both into `params`.
- `deployments/manifests/api-platform/gateway-config.yaml` — added `jwtauth_v1:` block alongside `jwtauth_v0`, same keymanager list. Image tags bumped 0.9.0 → 1.0.0.
- `deployments/scripts/setup-prerequisites.sh` — bumped `API_PLATFORM_OPERATOR_VERSION` 0.4.0 → 0.6.0 and added `gateway.helm.chartVersion=1.0.1` override (agent-manager precedent).

**Acceptance**: two orgs with different `issuer` values in their IDP profiles can coexist; tokens from one org's IDP don't unlock the other's components.

### Phase 4 — Console UI ✅

**Goal:** users see and edit `api.security` from the console; org admins see the IDP profile.

**Changes:**
- `console/src/pages/ComponentApiSecurityPanel.tsx` (new) on the component detail page:
  - Status badge: 🔒 Protected (issuer + publisher client_id) | 🌐 Public (no AP hop)
  - Toggle — writes back to `design.md` frontmatter via PUT `/design/components/<name>`.
- `console/src/pages/OrganizationSettingsPage.tsx` — IDP profile section:
  - v1 read-only ("Using platform IDP — Thunder").
  - v2 editable form (kind picker + per-kind fields).
- `console/src/lib/designSchema.ts` (or equivalent) — extend type.
- All API URL displays use `NormalizeExternalURL` (Phase 1 helper).

**Acceptance**: console correctly reflects current state; toggling persists; next deploy hits protected path; no IDP picker until Phase 7.

### Phase 5 — Agents ✅

**Goal:** architect agent decides `api.security` from the user's description.

**Changes:**
- `agents/src/agents/architect/prompt.ts` — security-classification rubric. Default `none` when ambiguous; default `required` when terms like "login", "OAuth", "JWT", "protected", "private", "customer", "billing" appear.
- Add 3–5 golden tests in `agents/__tests__/architect-security-classification.test.ts`.

**Acceptance**: golden tests pass; humans still override via console (the agent is a suggestion engine, not a gatekeeper).

### Phase 7 — Asgardeo / BYO-IDP ✅ (v1 minimum)

**Builds on Phase 6.**

**v1 shipped:**
- Console IDP-profile picker editable (kind dropdown + issuer + JWKS URL + Auto-discover button).
- `asdlc-service/clients/oidc/` — minimal OIDC discovery helper (`/.well-known/openid-configuration` → issuer + jwks_uri).
- BFF `PUT /api/v1/organizations/{orgId}/idp-profile` + `GET /api/v1/idp/discover` endpoints.
- Trait emits `issuers: [profile.Issuer]` when the org's profile kind is non-platform — protected APIs only trust the org's IDP.
- Kind switch invalidates the existing publisher app (cleared on PUT); next protected-component reconcile provisions a fresh one in the new IDP.

**v2 follow-ups (documented, not shipped):**
- DCR (Dynamic Client Registration) for BYO-IDP — `clients/asgardeo/` (Management API) and full OIDC DCR in `clients/oidc/`. v1 requires the org admin to pre-register the App Factory publisher app in their IDP and feed credentials back via PUT idp-profile.
- Automated keymanager registration — today the BFF stores the profile but a platform admin must manually add the matching `keymanager` entry to `deployments/manifests/api-platform/gateway-config.yaml`'s `jwtauth_v1` block + re-run `setup-prerequisites.sh`. v2 will move this into the BFF (k8s API writeback to the ConfigMap + AP gateway-runtime reload). The console flow surfaces this requirement with a warning Alert on non-platform profiles.
- Migration policy: org changing profile kind cannot retroactively migrate components — existing protected components must be deleted + re-created. Documented in console flow.

**Acceptance (v1)**: an org admin can configure a BYO-IDP profile via the console; the trait emits the per-org issuer; once the operator adds the keymanager, tokens minted against that IDP unlock that org's protected components only.

---

## 7. Files to touch (concrete change list)

| Phase | File / area | Change |
|---|---|---|
| 0 ✓ | `deployments/scripts/setup-prerequisites.sh` | Step 6: helm install AP operator + apply config/RBAC/APIGateway |
| 0 ✓ | `deployments/scripts/setup-asdlc.sh` | Add `allowedTraits` to `service` CCT; apply api-configuration trait |
| 0 ✓ | `deployments/manifests/api-platform/` | Vendored from `deployments-v2/wso2cloud-deployment/.../` |
| 1 | `asdlc-service/services/artifact_store.go:226-233` | `componentFrontmatter.Api *apiConfig` |
| 1 | `agents/src/agents/architect/schema.ts:5-53` | Add `api` to `SlimComponent` |
| 1 | `asdlc-service/services/api_security.go` (new) | `ResolveAPISecurityEnabled` helper |
| 1 | `asdlc-service/services/external_url.go` (new) | `NormalizeExternalURL` helper for C3 |
| 1 | `console/src/lib/externalUrl.ts` (new) | Same helper, TS side |
| 2 | `asdlc-service/services/trait_sync.go` (new) | `SyncComponentTraits()` — single shared emitter under per-component mutex (R1); reads design AFTER lock acquisition; PATCHes Component CR + every existing per-env RB; logs RB-skipped events for the watcher |
| 2 | `asdlc-service/services/dispatch_service.go:712` | In `ensureOCComponent`: for protected components create with `autoDeploy: false` + explicit per-env RB creation with `traitEnvironmentConfigs` populated (R2 — closes first-deploy exposure window). For unprotected components keep `autoDeploy: true`. Invoke `SyncComponentTraits` after Component create either way |
| 2 | `asdlc-service/services/design_service.go` (around `UpdateDesignFile`) | Invoke `SyncComponentTraits` after persisting design.md — best-effort, log on failure |
| 2 | `asdlc-service/services/design_service.go` (`DeleteComponent`) | Call `componentService.DeleteComponent` against OC API. After 5s grace, explicitly delete remaining `Backend` and `RestApi` resources in the dp-namespace matching the trait's name patterns (R3 fallback — trait's `creates` don't stamp owner refs, cascade NOT guaranteed) |
| 2 | `asdlc-service/services/webhook/trait_sync_watcher.go` (new) | Periodic reconciler (10s cadence, matches build-watcher + coding-agent-watcher). Emits bidirectional `trait-sync drift` metric — `direction=missing_protection` AND `direction=stale_protection` (R4) |
| 2 | `asdlc-service/clients/openchoreo/types.go` | Confirm generated Component type carries `traits[]`; regenerate from OC OpenAPI if absent |
| 2 | `tests/api/baseline_diff_test.go` (new) | Components without `api` block produce identical YAML to pre-Phase-2 baseline |
| 2 | `tests/api/trait_sync_test.go` (new) | Toggle design `security: none → required` → trait+RB env config appear within one watcher cycle |
| 3 | `deployments/single-cluster/values-thunder.yaml` | Add `asdlc-system-client` CONFIDENTIAL_APP + new bootstrap script `60-asdlc-system-role.sh` to assign Thunder Administrator role |
| 3 | `deployments/single-cluster/values-openbao.yaml` | Seed `secret/asdlc/system/thunder-admin` path |
| 3 | `asdlc-service/clients/thundersvc/` (new) | Mirror agent-manager `clients/thundersvc/client.go` — getSystemToken, EnsurePublisherApp, Delete, Regenerate |
| 3 | `asdlc-service/services/idp_service.go` (new) | `EnsureOrgPublisher`, `RevokeOrgPublisher`, audit-log writes |
| 3 | `asdlc-service/db/migrations/NNN_organization_idp_profiles.sql` (new) | Table + seed for platform profile + `idp_audit_events` table |
| 6 | `wso2cloud-deployment` upstream PR | Trait schema + ConfigMap `jwtauth_v1` block + runtime image bump |
| 6 | `deployments/manifests/api-platform/*.yaml` | Re-vendor after upstream PR merges |
| 4 | `console/src/pages/ComponentApiSecurityPanel.tsx` (new) | Toggle + status badge |
| 4 | `console/src/pages/OrganizationSettingsPage.tsx` | IDP profile section (read-only v1) |
| 5 | `agents/src/agents/architect/prompt.ts` | Security classification rubric |
| 5 | `agents/__tests__/architect-security-classification.test.ts` (new) | 3–5 golden tests |
| 7 | `asdlc-service/clients/asgardeo/` (new) | Asgardeo Management API client |
| 7 | `asdlc-service/clients/oidc/` (new) | Generic OIDC DCR client |
| 7 | `wso2cloud-deployment` upstream PR | `IdpKeymanager` CR + reconciler |
| 7 | `console/src/pages/OrganizationSettingsPage.tsx` | IDP picker becomes editable |

---

## 8. Carve-outs (paths that MUST NOT go behind AP)

| Path / service | Why kept on direct kgateway / Service DNS |
|---|---|
| BFF `/webhooks/github` | HMAC-authed; GitHub never sends JWTs |
| BFF `/oauth/callback`, `/.well-known/oauth-protected-resource` | App Factory's own login plumbing; AP isn't an OIDC RP |
| **Entire BFF (`app-factory-api`)** | The runner pod calls BFF API via in-cluster Service DNS with no Thunder token (per `CLAUDE.md`'s "Implementation execution flow"). Putting BFF behind AP would break the runner-to-BFF path. v2 milestone to move all internal traffic through AP with service tokens. |
| Console static assets | No auth needed; SPA fetches its config from BFF separately |
| OC Platform API (`api.openchoreo.localhost`) | OC's own internal flow; BFF authenticates with Thunder client_credentials directly |
| Thunder OAuth endpoints | App Factory's own login terminates at Thunder |
| Liveness/readiness probes | Container-level, not HTTPRoute-exposed |
| coding-agent runner ↔ git-service ↔ BFF | In-cluster pod-to-Service traffic; no JWT chain established for these |
| Smee-client | Inbound from smee.io; HMAC at BFF endpoint |

**v1 stance: all-or-nothing per component.** Components needing mixed public+private split into two components. `publicPaths` frontmatter is a v2 refinement (Section 9 D4).

---

## 9. Open decisions / resolved (vs v1)

| # | Decision | v1 recommendation | v2 resolution |
|---|---|---|---|
| D1 | Always-through-AP or only-when-protected? | Only-when-protected (latency) | **Only-when-protected (SPOF).** Real rationale: C7 single-pod runtime SPOF for all protected components. Adding unprotected components would 2x the blast radius. |
| D2 | Audience format opaque vs readable? | `org_<ulid>_comp_<ulid>_<env>` opaque | **Dropped.** C8: Thunder hardcodes `aud="application"`. Discriminator is `client_id` (per-org publisher app), set by Thunder, not configurable per request. |
| D3 | Per-component vs per-org OAuth client? | per-(component, env) | **Per-org** (`asdlc-publisher-<orgHandle>`). Agent-manager precedent at `clients/thundersvc/client.go:205`. N×M cardinality avoided; per-app authz is the user-app's responsibility, not the platform's. |
| D4 | `publicPaths` carve-out in v1? | v2 | Confirmed v2. Split-component is the v1 escape hatch. |
| D5 | OpenAPI-derived `operations[]`? | v2 | Confirmed v2. Default `* /*` works. |
| D6 | Component rename → audience stable? | Store audience in frontmatter on first save | **Moot.** D2 dropped audience-in-frontmatter. Per-org client_id is naturally stable across component renames. |
| **D7** | How does BFF drive Thunder admin? | Generic admin client | **Per agent-manager: `asdlc-system-client` bootstrap row in `values-thunder.yaml`, `scope=system` request, Thunder Administrator role (NOT ClusterAuthzRoleBinding), singleflight-cached system token.** |
| D8 | Migration kind=platform→asgardeo? | No migration, recreate | Confirmed. Document in Phase 7 console flow. |
| D9 | Backfill component ULIDs? | Phase 1 migration | Moot — D2 dropped the component-ULID-in-audience requirement. ULIDs not needed for security. |
| **D-new** | What feature flag gates emission? | `FEATURE_API_SECURITY` | **`FEATURE_EMIT_API_TRAIT`** (architect rename — more precise). AP install itself is unconditional. |
| **D-new** | Re-protection (security: required → none → required)? | not specified | Idempotent. Org publisher already exists from first protected component; no IDP change needed; trait toggle is purely declarative. |

---

## 10. Trust-root matrix (architect round-2 — adds 4th root + corrected rotation order)

The platform holds **four** distinct trust roots after Phase 3 (v2 had three; the build-workflow's `openchoreo-workload-publisher-client` is a pre-existing 4th root not in v2's table).

| Trust root | Use | Holder | Rotation owner | OpenBao path / file |
|---|---|---|---|---|
| Thunder JWKS | Validating inbound user JWTs at AP gateway AND at BFF middleware | Thunder (issuer) | Platform / Thunder upgrades | n/a (fetched live, 5min cache) |
| `task-signing.pem` | Signing outbound per-task JWTs for the coding-agent credential helper | BFF | App Factory dev/SRE | `deployments/keys/task-signing.pem` (mounted via secret) |
| `openchoreo-workload-publisher-client` creds (**pre-existing, not new in this plan**) | Build workflow's `generate-workload-cr` step authenticating to OC Platform API to write the Workload CR | Build pod (env via ExternalSecret) | Platform | `secret/openchoreo/workload-publisher` |
| `asdlc-system-client` creds (**new — Phase 3**) | Minting Thunder admin tokens (`scope=system`) for `/applications` calls (publisher app lifecycle) | BFF (singleflight cache) | Platform | `secret/asdlc/system/thunder-admin` |
| `asdlc-publisher-<orgHandle>` creds (**new — Phase 3, per-org**) | (Optional) User-app outbound calls to other protected APIs from this org | User app pod (env via SecretReference) | App Factory per-org rotate | `secret/asdlc/<orgId>/idp/publisher` |

**Rotation order — leaves first** (architect-corrected; v2 had this backwards):

For a coordinated "rotate everything" event:

1. **Leaves**: `task-signing.pem` AND every per-org publisher cred (`asdlc-publisher-*`). These are leaves — nothing else depends on them. Per-org publisher rotation requires the system-client to still be live (it's how we mint the new publisher creds).
2. **`openchoreo-workload-publisher-client`**: rotate next. Build workflows that retry within the rotation window must tolerate transient 401s. Coordinated rotation usually pauses dispatch.
3. **`asdlc-system-client`**: rotate last with a grace window where both old and new system creds are valid simultaneously (Thunder supports a rotation by adding a second secret on the same app, deprovisioning the old once everything has switched). The system client gates all publisher operations, so doing it last avoids the deadlock where rotating publisher depends on a system token that's just been invalidated.

For a single-cell compromise, rotate ONLY that cell. Don't fan out.

Thunder JWKS compromise is a Thunder platform incident, not an App Factory rotation. Procedure is to rotate Thunder's signing key + clear JWKS cache on the AP gateway-controller pod.

---

## 11. Observability (slog-only — Prometheus deferred)

v1 ships **structured logging only**. Prometheus instrumentation was considered (R4 drift metric, 401-rate-per-RestApi, etc.) but rejected for v1 — too heavy for the value at this stage. Operators run grep / log-aggregator queries against the lines emitted below; v2 can promote any of them to time-series metrics if the search load justifies it.

**What lands in the BFF log stream today:**

- `trait_sync: reconciled` — INFO, every successful `SyncComponentTraits` call. Carries `orgID`, `projectID`, `componentName`, `apiSecurityEnabled`. Stream this to confirm convergence.
- `trait_sync: component deleted; OC RenderedRelease finalizer GCs trait resources` — INFO, on every `DeleteComponentCascade` call. Audit trail for component removal.
- `trait_sync watcher started tick=10s failureBudget=5 pauseFor=300000000000` — INFO, BFF boot. Confirms the drift sweep is running.
- `trait_sync watcher: pausing tuple after consecutive failures` — WARN, when a single (org/project/component) hits the 5-failure budget. Investigate this — usually points to a stuck design or missing OC permissions.
- `idp_service: EnsureOrgPublisher / RevokeOrgPublisher / RegenerateClientSecret / UpdateProfile` — INFO, every per-org publisher lifecycle action.
- `Thunder publisher app created` — INFO, only on actual creation (idempotent re-runs skip this).
- `Thunder client secret regenerated` — INFO, every rotation.

**What lands in the AP gateway-runtime log stream:**

- 401 rejections from the policy engine carry the structured JSON `{"error":"Unauthorized","message":"Authentication failed."}` per `gateway-config.yaml:124`. Spike correlates with misconfigured clients / JWKS staleness / IDP outage.
- Pod is labelled `openchoreo.dev/system-component=api-gateway` (per `gateway-config.yaml:310`) so the log pipeline can route gateway logs separately from app logs.

**Audit log** — `idp_audit_events` table. Surface as a console page for org admins ("publisher rotated 3d ago by …") in v2.

**Manual recovery for operational issues** — see §12 below. The "search logs for X" patterns substitute for what would be pager-grade alerts in a Prometheus deployment.

---

## 12. Failure modes / runbook hooks (architect round-2 — split + component deletion added)

| Failure | Effect | Runbook |
|---|---|---|
| **Thunder JWKS endpoint down (Thunder up otherwise)** | AP gateway has cached JWKS for 5min (`gateway-config.yaml:112`). After expiry, JWKS-fetch retries fail; ALL protected APIs return 401 (`onfailurestatuscode: 401`, `validateissuer: true` — no fail-open). | Restore Thunder JWKS endpoint. Cached JWKS may already cover the request — verify by checking AP gateway-controller logs. Same restore steps as Thunder pod recovery in `docs/operations/cluster-health.md`. |
| **Thunder is completely down (deployment crashed)** | Same 401 effect after 5min JWKS cache. ALSO blocks `asdlc-system-client` token mint → publisher lifecycle frozen. NEW protected components cannot register. Existing protected components keep working until cache expires. | Restore Thunder deployment. Token-mint resumes immediately. AP gateway JWKS cache may need manual refresh by restarting gateway-controller pod. |
| **AP gateway-runtime pod down (the SPOF — C7/D1)** | ALL protected APIs return 503 (kgateway has no upstream available — the Backend resolves to a dead Service endpoint). Public APIs untouched. JWKS validation is irrelevant — traffic never reaches the policy engine. | Pager. The "flip jwtAuth.enabled=false" emergency does NOT help here — the gateway pod is dead, not the policy. Real fix: restart the gateway-runtime pod. Or, as a fallback for critical apps only, hand-edit the affected components' HTTPRoute backendRef back to the component's Service (reverses the trait patch) — audit-logged emergency only. |
| **AP gateway gateway-controller down** | Existing RestApis keep serving (config is pushed once via xDS and cached by the runtime). New RestApi CRs don't reach the runtime. **Protected components keep working; new protected components don't.** | Restart gateway-controller. |
| **`asdlc-system-client` creds compromised** | Attacker can register / delete OAuth clients in Thunder. | Revoke client in Thunder admin; rotate to a freshly bootstrapped system client; redeploy BFF. Audit log gets the actor=attacker entries. |
| **Publisher creds for one org leak** | User app's identity is impersonable for that org. | `RegenerateClientSecret(orgHandle)`; rotate ExternalSecret; redeploy that org's protected components. |
| **Component deletion (user-initiated)** | NEW row. User deletes a component via design.md or console. | `designService.DeleteComponent` removes design tree + invokes `componentService.DeleteComponent` against OC API. OC owner refs cascade-delete the `Component → ReleaseBinding → RestApi/Backend/HTTPRoute/Deployment/Service` chain in the dp-namespace. Publisher creds for the org are NOT revoked (per-org, may have other components). Audit log entry recorded. |
| **Trait reconciliation stuck** | Component has `api.security: required` in design but the OC Component CR lacks the trait, or the RB lacks jwtAuth config. Gateway 404 or token-not-validated. | Check `trait-sync drift` metric (both `missing_protection` and `stale_protection`). Inspect `trait_sync_watcher` logs. Common causes: (a) BFF mutex contention with a stuck Sync call — check goroutine dump; (b) IF the implementation fell back to `autoDeploy: true` per Phase 2's prerequisite-spike clause, the first-deploy window itself can show as transient drift — should self-heal within one watcher cycle; (c) `kubectl describe component <comp>` for Component reconciler errors; (d) verify `allowedTraits` on ClusterComponentType. |

---

## 13. Test plan

### Unit
- `artifact_store_test.go` — round-trip frontmatter ±`api`.
- `api_security_test.go` — security resolution.
- `external_url_test.go` — trailing-slash normaliser.
- `thundersvc_test.go` — mock Thunder, idempotent EnsurePublisherApp, system-token singleflight.

### Integration
- `tests/api/baseline_diff_test.go` — Phase 2 non-regression: no-api component yields identical YAML.
- `tests/api/api_security_integration_test.go` — toggle on → Component has traits, RB has env config; toggle off → both stripped.
- `tests/api/trait_sync_test.go` — covers three scenarios: (1) design-edit path: PUT `design.md` flipping `security: none → required` triggers sync within one watcher cycle on an existing autoDeploy=true component; (2) protected first-deploy path: BFF creates Component with `autoDeploy: false` + per-env RBs, gateway enforces JWT immediately (no exposure window); (3) bidirectional drift: a stale `enabled: true` RB on a component whose design says `none` is detected by `direction=stale_protection` and corrected within one watcher cycle.
- `tests/api/oauth_lifecycle_test.go` — first protected component creates publisher in Thunder, OpenBao path exists; org delete revokes.
- `tests/api/component_delete_test.go` — design-delete cascades: removes design tree, deletes OC Component CR, OC owner refs cascade-delete RestApi/Backend/RB/Deployment/Service; publisher creds NOT revoked.

### E2E (Playwright)
- `tests/e2e/protected-component.spec.ts` — full flow: console toggle → deploy → curl truth table.
- Re-run existing E2E suite to confirm zero regression for unprotected components.

### POC verifier
- `verify-api-platform.sh` stays green throughout. Extend in Phase 2 to use BFF-created components instead of static manifests.

---

## 14. Rollout

- Phases 1–3 ship behind `FEATURE_EMIT_API_TRAIT=true` on the BFF. Off in prod by default until verified on dev.
- Phase 1 fully backward compat (read-only).
- Phase 2 needs the baseline-diff test green on a corpus of existing components before flipping the flag in prod.
- Phase 3 is gated by D7 prereqs (system client bootstrap shipped).
- **Phase 6 requires a scheduled maintenance window.** The coupled upgrade includes an AP runtime image bump (v0.9.0 → v1.0.0+) which forces a pod restart. Because the runtime is a single-pod SPOF (C7), this causes a ~30-60s outage for ALL protected components on the cluster. Schedule out-of-hours; pre-announce to customers; ensure rollback plan (old image tag pinned and tested).
- Phase 4 cannot ship until Phase 6 is live on the target cluster (no multi-IDP footgun).
- Phases 5 and 7 are additive.

---

## 15. References

- POC log: `deployments/POC-API-PLATFORM.md` (must-read)
- POC manifests: `deployments/manifests/poc-api-platform/`
- Vendored AP install: `deployments/manifests/api-platform/`
- **agent-manager Thunder client (the precedent for Phase 3)**: `/Users/wso2/repos/agent-manager/agent-manager-service/clients/thundersvc/client.go`
- **agent-manager Thunder bootstrap (the precedent for D7)**: `/Users/wso2/repos/agent-manager/deployments/helm-charts/wso2-amp-thunder-extension/values.yaml:204-225` (system client + publisher client rows)
- **agent-manager AP gateway config (the precedent for Phase 6)**: `/Users/wso2/repos/agent-manager/deployments/values/api-platform-operator-full-config.yaml:74-101`
- ~~wso2cloud canonical trait~~ — REMOVED. The `deployments/` setup is pure OC; our vendored trait at `deployments/manifests/api-platform/api-configuration-trait.yaml` is authoritative.
- ~~wso2cloud canonical gateway config~~ — REMOVED. Same: our `deployments/manifests/api-platform/gateway-config.yaml` is authoritative.
- App Factory's existing JWT validation: `asdlc-service/middleware/auth_middleware.go`
- App Factory's existing Component CR writer: `asdlc-service/services/component_service.go:95` (`CreateComponent`)
- App Factory's existing per-env config mirroring: `asdlc-service/services/config_service.go:68`
- App Factory's existing Workload writer (the build workflow — context only, not changed): `deployments/manifests/docker-build-workflow.yaml:355-567`
- Thunder constraint (C8) source: `/Users/wso2/repos/agent-manager/deployments/helm-charts/wso2-amp-thunder-extension/values.yaml:118` (`audience: "application"`)

---

## 16. Quick-start for the next session

1. **Read `deployments/POC-API-PLATFORM.md`** end-to-end. Captured discovery.
2. **Read this doc** end-to-end. Plan + decisions.
3. **Run `bash deployments/scripts/verify-api-platform.sh`** — must still be green. If not, re-run `setup-prerequisites.sh` and `setup-asdlc.sh`.
4. **Pick Phase 1** to start. Smallest, most isolated, unblocks every later phase.
5. **Before Phase 3**: confirm `asdlc-system-client` row is in `values-thunder.yaml` AND its Administrator role is assigned via the new bootstrap script. Without these, Phase 3 cannot mint tokens.
6. **Do NOT touch the upstream trait** until Phase 6 is staged as a coupled upgrade. Vendoring + patch-only on the trait until then.

---

**Document maintenance**: when a phase ships, mark it ✓ in section 6 and link the PR. When a decision in section 9 resolves further, append the resolution and the date. This doc is the source of truth; deviate from it only after updating it.
