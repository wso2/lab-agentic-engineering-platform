# Target Architecture — Agentic Engineer on OpenChoreo / WSO2 Cloud

> The end-state design, modeled on the verified Agent Manager (AM) architecture
> (`/Users/wso2/repos/agent-manager-analysis/`). This describes *where things should
> land*; `20-refactor-roadmap.md` describes *how to get there*. Gap IDs (G1–G10) refer
> to `00-executive-summary.md`. This is a **clean refactor** — no back-compat constraints.
>
> **Reference sources to port from (AM ships as open-core + enterprise superset):**
> - **OSS open-core** — `/Users/wso2/repos/agent-manager/agent-manager-service/` — the
>   `clients/secretmanagersvc/` package: interfaces (`provider.go`), `registry.go`, high-level
>   `client.go`, `types.go`, and the **`openbao` provider** (`providers/openbao/`). *(Note: the
>   local clone is stale at ~`amp-0.7.0`; deployed cloud is `v0.11.0`. Pin a specific tag when
>   porting.)*
> - **Enterprise superset** — `/Users/wso2/repos/agent-platform/agent-manager-service/` — vendors the
>   OSS module and **adds the `secret-manager-api` provider** at `secrets/provider.go` + `secrets/types.go`.
>   This is the cloud secret backend (see §3) and the artifact Phase 3 ports.
> - **Verified analysis** — `/Users/wso2/repos/agent-manager-analysis/` (`00`–`09`).

---

## 0. Service topology (the structural decision)

**Diagnosis.** `git-service` today is two unrelated things glued together: (a) a real
GitHub/git capability, and (b) a secret store that doesn't belong to it (the Anthropic key, the
`org_secrets` AES table, the `effective-key` endpoint, the SSA-plaintext applies). The root
problem is that **the secret store landed in the one service with no OpenChoreo client**, so the
only way to get values to pods was to push plaintext — that is the entire G1 mess.

**Decision (confirmed).** Organize boundaries by *plane role + who may hold a secret*, not by
history:

1. **Exactly one long-lived control-plane service** holds the OC client *and* the secret
   abstraction. → **Fold `git-service` into `asdlc-service`** — one Go service (keep the existing
   `asdlc-service` name; no platform rename — name not finalized),
   the direct analog of AM's `amp-api`. The blurred boundary vanishes by construction and the
   cross-service plaintext hop (`effective-key`) becomes an in-process call.
2. **No long-lived service holds a usable tenant LLM key.** Interactive agents call the gateway
   with a *proxy* key; the control plane only *writes* provider values to the store and never
   reads them back to use.
3. **Ephemeral pods are the only place tenant creds materialize** (via ESO from a
   `SecretReference`) **and the only place filesystem git happens**.
4. **The console holds nothing.**

**Git mechanism (confirmed).** The control plane does repo/issue/PR/**commit** purely via the
**GitHub REST + Git Data API** (blobs/trees/commits/refs) — **no git binary, no workspace volume
in any long-lived service**. Filesystem git is confined to the ephemeral coding-agent runner pod,
which genuinely needs a working tree. This makes `asdlc-service` a clean REST/orchestration
service exactly like `amp-api` (which has no filesystem git).

### 0.1 Target components

| Component | Lang / lifetime | Owns | Secrets |
|---|---|---|---|
| **`asdlc-service`** (with `git-service` folded in) | Go, long-lived control plane | OC client; **`secretmanagersvc`** (AM port); **`github`** pkg (REST + Git Data API + App-token minting); task state machine; webhook ingestion; dispatch | writes values to OpenBao/secret-manager-api and authors `SecretReference`/`GitSecret`; **never hands plaintext across a boundary** |
| **`console`** | React | UI | none |
| **`agents`** | TS (Vercel AI SDK), long-lived | interactive spec agents (chat/SSE: BA, architect, taskgen, wireframe) | none — LLM via gateway **proxy** key |
| **`remote-worker`** (`remote-worker`) | TS (Claude Agent SDK), **ephemeral** OC `WorkflowRun` pod | one-shot code gen per task; the only filesystem clone | creds via **ESO-materialized Secret** from a `SecretReference`; LLM via gateway |
| build/workflow pods (`dockerfile-builder`, …) | ephemeral OC WorkflowRuns | build/deploy | scoped, ESO-materialized |

### 0.2 `asdlc-service` package layout (mirror `amp-api`)

```
asdlc-service/
├── clients/openchoreo/      # OC client + NEW SecretReference/GitSecret typed wrappers
├── clients/secretmanagersvc/# AM port: providers/openbao (local) + providers/secretmanagerapi (cloud)
├── clients/github/          # REST + Git Data API + App-installation-token minter
│                            #   (replaces git-service github_client + app_token_minter)
├── services/                # task state machine, dispatch, webhook projector
├── controllers/  models/  repositories/  config/  middleware/
```

**Deleted in the merge (clean, no back-compat):** git-service `pkg/credentials`
(dbStore/openBaoStore), `org_secrets` *value* storage, the `effective-key` endpoint, the WP
SSA-apply paths, and all server-side shell-git/workspace code. **Exception:** git-service's
`pkg/credentials/import_fence_test.go` (the AST test banning the Vault SDK outside that package) is
**ported and extended**, not deleted — see §0.3.

> **Note — `clients/github` is labs-original, not an `amp-api` mirror.** `amp-api` has no `github`
> package and does not author Git Data API commits. This package (App-key handling, token minting,
> multi-file commits) is the most leak-prone new code and gets explicit import rules in §0.3.

### 0.3 Code-level boundaries (enforced, not by convention)

The merge fuses ~64k + ~17k LOC into one module; without enforced import rules it will re-blur the
boundary it exists to fix. So: **one binary, many enforced seams.** Put clients/services under
`internal/` (compiler-checked module edge) and split `services/` by domain. Full matrix + rationale
in **`30-architecture-validation.md` §4**; the load-bearing rules:

```
# PUBLIC build (asdlc-service) — openbao provider, runs locally on OpenBao
cmd/asdlc-service/   # composition root: constructs openbao.NewProvider()
internal/
├── clients/
│   ├── openchoreo/        # ONLY holder of the OC client; SecretReference/GitSecret wrappers
│   ├── secretmanagersvc/  # the secret abstraction (interfaces, registry, high-level client)
│   │   └── providers/openbao/   # ONLY place the Vault SDK is imported (Phase 0)
│   ├── github/            # REST + Git Data API + App-token minting (labs-original)
│   └── thunder/           # IdP client
├── services/{secrets,git,orchestration,webhook}/   # domain-split; orchestration only
├── controllers/  repositories/  models/

# PRIVATE overlay (separate module, Phase 3) — open-core split, option (b)
overlay/
├── cmd/                   # its own composition root: constructs secrets.NewProvider(...)
└── secrets/               # the secret-manager-api provider, ported from agent-platform
```

**Allowed-imports (the MUST-NOT edges that matter):**

| Package | MUST NOT import |
|---|---|
| `clients/openchoreo` | `secretmanagersvc`, `github`, any Vault SDK |
| `clients/secretmanagersvc/providers/openbao` | (Vault SDK allowed **only here**); OC client, `github` |
| **`clients/github`** | **`secretmanagersvc`, `clients/openchoreo`** (no secret backend, no OC client) |
| `services/git` | `secretmanagersvc`, `clients/openchoreo`, Vault SDK |
| `services/*`, `controllers/`, `repositories/` | any secret-backend SDK directly |

**The hard line (values vs references):**
- **A secret value enters exactly one package — `clients/secretmanagersvc`** (+ its providers). It is
  handed in via `PushSecret`/`Create` and **never returned across a plane boundary**.
- **`services/secrets` is the only service that touches `secretmanagersvc`**; it writes the value and
  (via the OC client) authors the `SecretReference`/`GitSecret`. Reference authoring is localized to
  `secretmanagersvc` + `clients/openchoreo` — never in `github` or general `services`.
- **`clients/github` never sees the secret store.** It receives an already-resolved token from
  `services/git` (which got it from `services/secrets`). App-private-key → installation-token minting
  is `services`-level orchestration: it *calls* `secretmanagersvc` for the App key and *calls*
  `github` to mint — the two never import each other.
- **`repositories/` stores references/metadata only — never values.**

**Enforcement (required, a Phase-0 gate):** port + extend the existing AST import-fence test to cover
every MUST-NOT edge above (Vault SDK confined to the openbao provider; OC client out of `github`;
secret backend out of `services/git`/`github`/`controllers`/`repositories`); use `internal/`;
optionally `depguard`/`go-arch-lint` in CI. **The merge is not "done" until the fences are green.**

---

## 1. Plane topology (target)

Mirror AM's split (AM `00`/`05`): the platform is a **control-plane application tier**
that *drives* OpenChoreo via its API; OpenChoreo's four planes do the heavy lifting.

```
┌──────────────── Control Plane (platform app-tier namespace) ───────────────────────────────┐
│  asdlc-service (OC client + secretmanagersvc + github) ─┐  agents (interactive)      │
│  Postgres (platform state)                                     ┤  console (web-application) │
│                                                                │  Thunder (IdP)  OpenBao    │
└─────────────────────────────────────────────────────────────────┼──────────────────────────┘
                                          │ OC OpenAPI (Bearer client_credentials)
                ┌─────────────────────────┼───────────────────────────┐
                ▼                         ▼                           ▼
   ┌── OpenChoreo Control Plane ──┐  ┌── Workflow Plane ──┐   ┌── Data Plane ──┐
   │ Project / Component / Workload│  │ ClusterWorkflow:    │   │ generated user │
   │ ReleaseBinding / SecretRef /  │  │  app-factory-coding │   │ app pods +     │
   │ GitSecret / WorkflowRun CRDs  │  │  -agent (runner pod)│   │ ESO-materialized│
   │ (extension chart registers    │  │ ClusterWorkflow:    │   │ Secrets         │
   │  ComponentTypes/Workflows)    │  │  dockerfile-builder │   │ LLM gateway(opt)│
   └───────────────────────────────┘  └─────────────────────┘   └─────────────────┘
                                          ESO reads values  ◄── central OpenBao (CP)
                                          via reader role
```

**Key principle (AM `08`):** values live in **central OpenBao** in the CP. Across any
boundary only a **reference** (SecretReference/GitSecret KV path) travels; ESO on the
consuming plane re-fetches with a read-only role.

---

## 2. Resource model (OC CRDs the platform should author)

Keep the parts already right; add the missing CRDs. Per generated user app:

| Concern | CRD the platform authors | Notes |
|---|---|---|
| App grouping | `Project` (per platform Project) | ✅ already done |
| Deployable unit | `Component` (generic `deployment/service` \| `deployment/web-application`) | ✅ keep generic types — better than AM |
| Build | `WorkflowRun` of `ClusterWorkflow dockerfile-builder` | ✅ keep (build-pod creds cleanup G9 → future) |
| Coding agent | dispatched as `WorkflowRun` of the coding-agent `ClusterWorkflow` | ✅ keep (proper allow-list registration G7 → future) |
| Deploy | `Component.AutoDeploy=true` → OC cuts ComponentRelease/ReleaseBinding | ✅ keep |
| Anthropic key → runner | **`SecretReference`** → ESO → K8s Secret → `secretKeyRef` | NEW M1 (G1) |
| Build credential (GitHub) | **`GitSecret`** bound to `ClusterWorkflowPlane` | NEW M1 (G1) — AM `06` `git_secrets.go` |
| Environments / promotion | `Environment` + `DeploymentPipeline` | *future (G6)* |

---

## 3. Secret architecture (target) — port Agent Manager's `secretmanagersvc` verbatim

**The decision: do not invent a secret layer — port AM's `secretmanagersvc` package 1:1**, and follow
AM's **open-core split** (DECISION: option (b), §3.3): the **public build** uses the `openbao`
provider chosen at its composition root; the **`secret-manager-api` provider lives in a private
overlay** with its own `main.go`. The shared abstraction + seam are reused unchanged across both
builds — this is exactly how AM ships (OSS `agent-manager` + enterprise `agent-platform`), see
`agent-manager-analysis/09` and `06`/`07`/`08`. Build this abstraction **first** (roadmap Phase 0);
everything else hangs off it.

### 3.1 Package structure to port (mirror AM `clients/secretmanagersvc/`)

Place it in the **OC-client-owning service — the BFF (`asdlc-service`)** — the analog of AM's
`amp-api`. (git-service relinquishes secret *storage*; see 3.4.)

Place it in the OC-client-owning service — **`asdlc-service`** (the merged service). For M1,
port **only** the interfaces + registry + high-level client + the **`openbao` provider**:

```
internal/clients/secretmanagersvc/
├── provider.go    # Provider, SecretsClient, SecretReferenceManager interfaces  (copy from OSS AM)
├── registry.go    # Register / GetProvider                                       (copy from OSS AM)
├── client.go      # SecretManagementClient: PushSecret + upsert SecretReference  (copy from OSS AM)
├── types.go       # StoreConfig, SecretLocation, SecretMetadata, OpenBaoConfig   (copy from OSS AM)
└── providers/openbao/   # local: raw Vault via hashicorp/vault/api, token auth (M1)
```

Source: OSS `/Users/wso2/repos/agent-manager/agent-manager-service/clients/secretmanagersvc/`. Copy
the interfaces verbatim, including the optional `SecretReferenceManager{ManagesSecretReferences() bool}`
seam (decides **who** creates the `SecretReference`) — even though M1 only ships `openbao` (which
doesn't implement it), keeping the seam makes the future cloud provider additive.

- **`SecretLocation` — fork to labs' domain (validation MAJOR-4):** AM's is `org/agent/config/entity`;
  labs is `org → project → task`. Fork to `{org, project, task, entity}` rather than overloading
  `AgentName`. Confirm `Namespace == OC org ID`.
- **The `secret-manager-api` provider is post-M1** (lives in enterprise `agent-platform`, ported into
  a private overlay in Phase 3) → **`future-phases.md`**.

### 3.2 Config surface (mirror AM `config.go` / `config_loader.go`)

| Env | Meaning |
|---|---|
| `SECRET_MANAGER_PROVIDER` | `openbao` (default, local) \| `secret-manager-api` (WSO2 Cloud) |
| `SECRET_MANAGER_API_URL` | base URL — *only used when provider is `secret-manager-api`* |
| `OPENBAO_URL` / `OPENBAO_TOKEN` / `OPENBAO_PATH` / `OPENBAO_VERSION` | *only used when provider is `openbao`* |
| `WORKFLOW_PLANE_OPENBAO_URL` / `_TOKEN` | second OpenBao for git/build creds (local), if mirroring AM's split |

### 3.3 Wiring seam (mirror AM `wire_gen.go:330-334`)

`ProvideSecretManagementClient` type-asserts the provider against `SecretReferenceManager`:
- **`openbao`** (does *not* implement it): the high-level client writes the value to OpenBao **and
  the BFF creates the `SecretReference`/`GitSecret` via the OC client**. (= AM standalone, `08` Class 1.)
- **`secret-manager-api`** (`ManagesSecretReferences()==true`): the OC client is **not** forwarded;
  the platform `secret-manager-api` service owns *both* the Vault write and the SecretReference
  creation. (= AM on WSO2 Cloud, `09`.)

The seam is why the design stays pluggable: who authors the `SecretReference` follows from which
provider is active. **For M1 only `openbao` exists** (it does *not* implement `SecretReferenceManager`,
so the merged service authors the `SecretReference`/`GitSecret` via its OC client — AM `08` Class 1).

> **Selection = composition root, open-core split (option (b)), mirroring AM — details in
> `future-phases.md`.** AM picks the provider in `main.go` (OSS → `openbao.NewProvider()`; enterprise
> `agent-platform` → `secrets.NewProvider(...)`), not via env. M1's public `asdlc-service`
> build constructs `openbao.NewProvider()` at its composition root and never imports a cloud provider.
> The cloud `secret-manager-api` provider is a **private overlay** (its own `main.go`) added post-M1.

### 3.4 Ownership (resolved by the §0 merge)

Because `git-service` is **merged into `asdlc-service`** (§0), `secretmanagersvc` and the OC
client live in the same process — exactly `amp-api`. There is no inter-service credential flow to
design and no delegation: GitHub-token minting, value persistence, and `SecretReference`/`GitSecret`
authoring are all in-process. (The interim `effective-key` endpoint the `agents` service uses for the
Anthropic key stays in-process at M1, JWT-authed; it's removed when the gateway lands post-M1.)

### 3.5 Secret taxonomy (which mechanism each secret uses — unchanged target)

| Secret | Level | Target mechanism | Crosses as | Consumer |
|---|---|---|---|---|
| GitHub PAT | org | value via `SecretManagementClient` → **`GitSecret`** (build) ; CP reads it from OpenBao in-process for GitHub API calls (M1) | KV-path ref | runner pod, control plane |
| Anthropic key | org | value via `SecretManagementClient` → `SecretReference` for the runner; `agents` via interim `effective-key` (gateway = post-M1) | KV-path ref (never plaintext push) | runner pod (via ESO), `agents` service |
| Coding-agent runner auth | per-run | per-org OAuth client-secret → per-run `ExternalSecret` → runner does `client_credentials` (AMP eval-job pattern, WS1.4) | stored cred via ESO | runner pod |
| Signing key, GitHub App key, webhook secret | platform | local: OpenBao/on-disk file mounts; only public/JWKS crosses | value stays in CP | control plane |
| AES master key (`CREDENTIAL_ENCRYPTION_KEY`) | platform | **eliminated** — Postgres-AES store removed (values now in OpenBao) | — | — |

### 3.6 The two tenant flows (built once, behind the abstraction)

All go through `SecretManagementClient` (the `openbao` provider at M1):

1. **Anthropic key → coding-agent pod (reference-only).** BFF calls
   `secretClient.Create(SecretLocation{org,…,entity:anthropic}, value)` → writes `org/<id>/anthropic/key`
   and the BFF creates an OC `SecretReference` in the WP-bound namespace; ESO (reader role bound to
   `workflows-<org>` SAs) materializes the Secret; the runner consumes it via `secretKeyRef`
   (`app-factory-coding-agent.yaml:176-180`). **Delete** `anthropic_credential_service.go` SSA-apply.
2. **GitHub build credential → build pod.** BFF creates an OC **`GitSecret`** bound to
   `ClusterWorkflowPlane/default` (AM `06` `git_secrets.go:30`). **Delete**
   `build_credentials_service.go` SSA-apply.
3. **Runner auth (callback to the BFF).** AMP eval-job pattern: per-org OAuth client-secret stored via
   `SecretManagementClient`, materialized per-run via `ExternalSecret`, runner does `client_credentials`
   (replaces the plaintext `ASDLC_BEARER` param — WS1.4).
4. **Anthropic key → `agents` service (M1 interim).** Served by the in-process JWT-authed
   `effective-key` endpoint (reads OpenBao). Moves to the gateway proxy key post-M1 (`future-phases.md`).

### 3.7 Org-wide secrets (GitHub PAT, Anthropic token) at M1

Ingestion: console → `asdlc-service` → `services/secrets` → `SecretManagementClient.Create`.
Postgres keeps only a reference/projection, never the value. At M1 (local `openbao`, ReadWrite):

| Secret | Who consumes it | Delivery |
|---|---|---|
| **Anthropic** | `agents` service (spec gen) + runner pod | runner via `SecretReference`→**ESO**→`secretKeyRef`; `agents` via the interim JWT-authed `effective-key` endpoint (reads OpenBao) until the gateway lands |
| **GitHub PAT** | runner pod + the **control plane** (repo/Git-Data-API/issues/PRs) | runner via `GitSecret`/ESO; the control plane **reads it from OpenBao in-process** for its GitHub calls (OpenBao is ReadWrite — fine) |

> **Cloud caveat (not M1):** on WSO2 Cloud the backend is **WriteOnly**, so the control plane cannot
> read a tenant secret back — control-plane GitHub auth must move to **GitHub App tokens** (App key =
> a platform secret, ESO-mounted), and `agents` must move to the gateway. Both are deferred — see
> **`future-phases.md`** ("Cloud secret architecture"). This does **not** affect M1.

---

## 4. Local dev (M1) — the public build on the local stack

M1 is a **single build target**: the public `asdlc-service` constructs the **`openbao`
provider** at its composition root and runs against the local stack labs already has — k3d with
OpenBao (dev mode at `host.docker.internal:8200`), Thunder, ESO, kgateway (`analysis/05`) +
docker-compose. Mirror AM's compose env (`SECRET_MANAGER_PROVIDER=openbao`, `OPENBAO_URL`,
`OPENBAO_TOKEN`, port-forward to `:8200`). **No new infra — the gap is purely code** (the ported
`secretmanagersvc` + openbao provider replacing the Postgres-AES store). The `secret-manager-api`
provider, the overlay build, helm/extension-chart packaging, and the LLM gateway are all post-M1 →
**`future-phases.md`**.

---

## 6. What explicitly stays the same

- The SDLC flow (spec→design→tasks→code→PR→build→deploy) and the GitHub-issue/PR/webhook
  protocol — these are the product and are orthogonal to OC plumbing.
- The generic ComponentType approach (do **not** copy AM's two hardcoded agent types).
- WorkflowRun-based dispatch and AutoDeploy reliance.
- The ported Thunder/JWKS identity model and the Task-JWT JWKS.

---

## 7. M1 invariants (acceptance criteria)

1. No platform process ever decrypts a tenant secret and writes the plaintext across a
   plane boundary. (Grep: zero SSA `Secret` applies of tenant values.)
2. Every tenant secret value lives only in OpenBao; only `SecretReference`/`GitSecret` KV-path
   references appear in OC CRDs; no secret values in Postgres.
3. A task reaches `deployed` only when its `ReleaseBinding.Status.Conditions[Ready]` is true.
4. No agent/runner pod holds a long-lived provider key unless it is an ESO-materialized
   reference or a gateway proxy key.
5. *(future — WSO2 Cloud install via helm; see `future-phases.md`)*
6. Exactly **one** long-lived control-plane service (`asdlc-service`) holds the OC client
   and `secretmanagersvc`; there is no separate `git-service`. (Grep: no second Go service with an
   OC client or a credential store.)
7. **No long-lived service contains a git binary or workspace volume.** Filesystem git exists only
   in the ephemeral coding-agent runner pod; the control plane uses the GitHub Git Data API.
8. **Internal import boundaries are enforced in CI** (§0.3): the Vault SDK appears only in the openbao
   provider; `clients/github` imports neither the secret backend nor the OC client; no
   `services`/`controllers`/`repositories` file imports a secret-backend SDK. The AST import-fence
   test is green. (Checks *intra-process* import direction, not just service count.)
9. **Secret/network failures fail loud and specific** (see `20` "Cross-cutting: error logging"): an
   uninjected secret or an unreachable backend (OpenBao/OC API/Thunder/GitHub) produces an actionable,
   contextual error in logs **and** on task/deployment status — never a silent hang. (Critical for the
   dev rollout.)
