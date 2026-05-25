# OpenChoreo Refactor — Executive Summary

> **What this is:** a synthesis of the six subsystem analyses in `analysis/`, assessing
> WSO2 Labs Agentic Engineer against OpenChoreo (OC) plane/secret/construct discipline,
> using **WSO2 Agent Manager (AM)** as the verified reference for "the right way."
> AM reference docs: `/Users/wso2/repos/agent-manager-analysis/` (files 00–08).
> Per-subsystem detail with `file:line` citations: see `analysis/01`–`analysis/06`.

---

## 1. The headline

The platform is **already structurally closer to idiomatic OpenChoreo than Agent Manager is
in several respects** — but it **violates OC's single most important invariant (secret
reference-only crossing) on every tenant secret**, and it has **no WSO2 Cloud deployment
path at all**. The refactor is therefore less "rebuild on OC" and more "finish the OC
adoption that was started, and fix the secret plane that was faked."

### Where it is already right (keep these — do not regress)

| Area | Status | Evidence | vs AM |
|---|---|---|---|
| OC API client | ✅ Generated OpenAPI client, Thunder `client_credentials` Bearer + `X-Use-OpenAPI`, 401→invalidate+retry | `asdlc-service/clients/openchoreo/` | Mirrors AM `clients/openchoreosvc` (AM `00`) |
| Component model | ✅ **Generic** ClusterComponentTypes `deployment/service`, `deployment/web-application` | `design_service.go:25` | **Better** than AM's 2 hardcoded agent types (AM `01`) |
| Workflow dispatch | ✅ Coding-agent + build are real OC `WorkflowRun`s of `ClusterWorkflow`s | `component_client.go:1030`; `analysis/03` | Same as AM builds (AM `01`/`04`) |
| Build→image | ✅ Image published; the **Workload CR** (containing the built image at `spec.container.image`) is written to the `openchoreo.dev/workload` annotation | `docker-build-workflow.yaml:602` | Same annotation key as AM (AM `01`) |
| Deploy | ✅ Delegated to OC `AutoDeploy=true`; runtime env on ReleaseBinding `workloadOverrides` | `analysis/03` | Same as AM (AM `01`); **better** (env on RB, not Workflow params) |
| Identity | ✅ Near-verbatim port of AM `jwtassertion` (Thunder JWKS, RS256, issuer/aud), org path/claim split, own Task-JWT JWKS | `middleware/jwt/jwt.go`; `analysis/06` | Same as AM (AM `06`) |
| WP namespacing | ✅ `workflows-<orgID>` targeting mirrors OC | `wp_naming.go:26` | Same intent as AM |

### Where it is wrong (the refactor surface)

| # | Gap | Severity | AM does it how | Detail |
|---|---|---|---|---|
| G1 | **Tenant secrets in Postgres-AES, not OpenBao; values physically pushed into planes as plaintext K8s Secrets every dispatch. No SecretReference, no ESO, no GitSecret.** OpenBao is **entirely unwired** (`NewOpenBaoStore` has zero callers; the `_platform/*` paths are dead code). | 🔴 P0 | Central OpenBao + `SecretReference`/`GitSecret`, ESO re-fetches with reader role; only the KV-path ref crosses (AM `06`/`07`/`08`) | `analysis/02`, `VERIFY-secrets` |
| G2 | **AES master key defaults to 32 zero-bytes**, never overridden in deployments → effectively plaintext at rest | 🔴 P0 | Chart-generated strong, stable `ENCRYPTION_KEY` (AM `07` Mech 4) | `config_loader.go:45` |
| G3 | **`deployed` inferred from build-WorkflowRun success, not ReleaseBinding `Ready`** — task can show deployed while pod crash-loops | 🔴 P0 | Polls `ReleaseBinding.Status.Conditions[Ready]` (AM `01`, `deployments.go:313`) | `analysis/01`/`03` |
| G4 | **No governed LLM gateway** — both agent runtimes hold the real Anthropic key and call `api.anthropic.com` directly; no metering/cost/rate policy; "model-agnostic" not implemented (one global `AGENT_MODEL`, hardcoded Anthropic) | 🟠 P1 | API-Platform gateway, per-agent proxy keys, provider key as upstream auth agents never see, token/cost metering in DP (AM `02`/`07`) | `analysis/04` |
| G5 | **No Helm packaging; the WSO2 Cloud path is partial & build-only.** A `.cicd/` pipeline builds the four first-party services to ECR and references `workload.yaml` descriptors in a separate `controlplane` GitOps repo (project `app-factory` / ns `wso2cloud`) — so app-tier-as-OC-Components is intended, but: OC extension resources are a **mix** of inline `setup-asdlc.sh` heredocs (ComponentTypes, Environment, Pipeline, AuthzRoleBindings) + loose manifest files (ClusterTrait, the two ClusterWorkflows) with **no versioned charts and no plane discipline**, and the `workload.yaml` descriptors are **stale** (e.g. port `8080` vs runtime `9090`). | 🟠 P1 | Main chart + 6 extension charts, each registering CRDs into the plane it extends (AM `05`) | `analysis/05`, `VERIFY-topology` |
| G6 | **Single environment, no promotion** — dev-only AutoDeploy, no `UpdateReleaseBinding` state transition | 🟡 P2 | `UpdateDeploymentState` toggles binding state for promotion (AM `01`) | `analysis/01` |
| G7 | **Coding-agent WorkflowRun is a label-hacked detached run** that omits `openchoreo.dev/component` to dodge the ComponentType allow-list | 🟡 P2 | Registered component/eval Workflow CR with `allowedWorkflows` (AM `04`) | `analysis/01`/`03` |
| G8 | **GitHub App mode is broken** (active `dbStore` fails the `*openBaoStore` type assertion) → PAT-only in practice; App private-key file is 0 bytes | 🟡 P2 | App-installation tokens via OpenBao-held key (AM `06`) | `analysis/06` |
| G9 | **Build pod itself POSTs the Workload to the OC API** holding API-server creds; hardcoded `oauth-client-secret` default in build manifest | 🟡 P2 | Control-plane service mutates the Workload (AM `01`) | `analysis/03` |
| G10 | **Infra hardening**: OpenBao in dev mode (root token `root`), git-service uses host kubeconfig + root token, all four planes co-located, smee.io webhook relay | 🟠 P1 (prod) | Sealed OpenBao, scoped ServiceAccounts, split planes, real ingress (AM `05`) | `analysis/05`/`02` |

---

## 2. Plane mapping — current vs target

> **Structural decision (clean refactor):** **fold `git-service` into `asdlc-service`** — one Go
> control-plane service (= AM's `amp-api`); **keep the existing `asdlc-service` name (no platform
> rename — name not finalized)**. The control plane does git via the **GitHub Git Data API** (no shell
> git / workspace in long-lived services). See `10-target-architecture.md` §0.

| Actor | Today | Target (WSO2 Cloud, AM-style) |
|---|---|---|
| `asdlc-service` + `git-service` | two docker-compose Go services; git-service owns OpenBao path + host kubeconfig + DB-AES + the Anthropic key (blurred boundary) | **Merged → `asdlc-service`**: one control-plane app tier (helm chart) owning OC client + `secretmanagersvc` + GitHub (REST/Git Data API) — like `amp-api` |
| `agents` (interactive spec agents) | docker-compose, holds plaintext key | App tier; **LLM via gateway proxy key**, holds no provider key |
| Secret values + SecretReference authoring | git-service (no OC client) → plaintext pushed | in `asdlc-service` (has OC client): writes values to OpenBao/secret-manager-api, authors `SecretReference`/`GitSecret` in-process |
| `remote-worker` (coding agent) | OC `WorkflowRun` → Argo pod (✅) | **Keep**; key via ESO-materialized Secret from `SecretReference`; only place filesystem git happens |
| `dockerfile-builder` | OC `WorkflowRun` (✅) | **Keep**; build pod stops holding OC API creds (CP service mutates Workload) |
| Generated user apps | OC Component + AutoDeploy (✅) | **Keep**; add env/promotion + RB-Ready gating |
| Tenant secret values | Postgres-AES (zero-key) | **Central OpenBao**; only KV-path refs cross planes |
| OC extension CRDs | mix of `setup-asdlc.sh` heredocs + loose manifests; cluster-scoped types un-namespaced, Environment/Pipeline in `default` | **Versioned extension helm charts**, CRDs into their plane |
| Console | docker-compose nginx | OC web-application Component or app-tier static host |

---

## 3. The one mental model to carry through the refactor

From AM `08` (cross-plane secret flow), the invariant the whole refactor turns on:

> **Every secret that reaches a workload must cross the plane boundary as a *reference*
> (a `SecretReference`/`GitSecret` carrying only a KV path), and the consuming plane
> re-fetches the value locally via ESO using a read-only role. The control-plane process
> must never decrypt a tenant secret and hand the plaintext across a boundary.**

The platform today does the opposite for every tenant secret (G1). AM has *one* deliberate
exception to this rule — the LLM gateway's upstream provider key (AM `07` Mechanism 2 /
`08` Class 3) — and only because that gateway lives outside OC. This platform should adopt
the rule and, ideally, avoid even that exception by keeping the LLM gateway inside OC (G4).

---

## 4. Recommended sequencing (detail in `20-refactor-roadmap.md`)

**Guiding decision (per the build intent): port Agent Manager's `secretmanagersvc` interfaces + the
`openbao` provider + the env-selection seam FROM THE START**, so the secret backend is pluggable —
local runs the `openbao` provider; WSO2 Cloud later runs a `secret-manager-api` provider, the way AM
ships (`agent-manager-analysis/09`). Building the **seam** first makes the WSO2 Cloud move *additive*
(port + register one provider) rather than a restructure. (The `secret-manager-api` provider is a
separate artifact ported from the enterprise `agent-platform` repo, not part of the Phase-0 port —
validation BLOCKER-1.)

Each phase is a **testable milestone** (several changes batched, then verified against the local
k3d + docker-compose stack); **structural changes go first**. Full detail + per-phase Test gates in
`20-refactor-roadmap.md`.

- **Phase 0 — Structural consolidation (FIRST):** merge `asdlc-service`+`git-service` →
  `asdlc-service` under an `internal/` layout; port AM's `secretmanagersvc` interfaces +
  the `openbao` provider + the config/wiring **seam** (the `secret-manager-api` provider is ported
  from the enterprise `agent-platform` repo, deferred to Phase 3 — validation BLOCKER-1); migrate
  control-plane git to the GitHub Git Data API
  (delete shell-git); **port + extend the AST import-fence test** enforcing the §0.3 boundaries.
  *Test gate:* full local flow still works + abstraction tests pass + **import fences green**.
- **Phase 1 — Secret-plane cutover + go-live readiness — this is the M1 ship (self-hosted):** route
  Anthropic + GitHub creds through the abstraction; emit `SecretReference`/`GitSecret`; ESO
  materialization; delete the SSA-plaintext paths + Postgres-AES store (kills G1 + G2); **plus the
  two pulled-in go-live items** — WS1.5 gate `deployed` on ReleaseBinding `Ready` (G3) and WS1.6
  self-host hardening (seal OpenBao, scoped SA, real webhook ingress). *Test gate = the go-live gate.*
- **Beyond M1 (Phases 2–4) → `future-phases.md`:** LLM gateway (G4), WSO2 Cloud packaging + the
  `secret-manager-api` overlay (G5/G10), lifecycle/promotion/App-mode (G6–G9). Out of scope for this
  effort; pull a specific item into M1 only if it's a hard launch requirement.

> **Go-live boundary (confirmed):** M1 = **Phase 0 + Phase 1**, self-hosted. Phase 1 was widened so
> "0 and 1 done" is literally shippable. The main docs (`00`/`10`/`20`) cover M1 only; everything
> deferred lives in `future-phases.md`.
>
> **Validated** by an architecture review — verdict *Sound-with-required-changes*; the required
> corrections (cloud provider is a port from `agent-platform` into a **private overlay** — open-core
> split, option (b) — additive not "config-only"; ported import-fence enforcement; `github` isolation;
> `SecretLocation` fork; runner auth via **AMP's eval-job pattern** — per-org OAuth client-secret +
> per-run ExternalSecret + `client_credentials`, not a minted/injected token; re-enter secrets / no
> migration; webhook HMAC) are folded into the docs above. Full report + the enforced boundary spec:
> `30-architecture-validation.md`.

See `10-target-architecture.md` for the end-state design and `20-refactor-roadmap.md` for
the phased, workstream-level plan.

## 5. Feasibility note (from verification)

Good news for G1: the **generated** OC client (`asdlc-service/clients/openchoreo/gen/`)
**already exposes `SecretReference` (full CRUD) and `GitSecret` (Create/List/Delete)** — the
same surface AM uses — so no client regeneration is needed; only thin typed wrappers + mocks
(analogous to AM's `secret_references.go`/`git_secrets.go`) are missing. **Caveat (validation
BLOCKER-1, resolved):** the `secret-manager-api` provider is **not** in the OSS `agent-manager` repo —
it lives in the **enterprise `agent-platform` repo** (`agent-manager-service/secrets/`), a portable
~470-LOC JWT-authed artifact. So the WSO2 Cloud move is additive (port + register one provider), not
"config-only." **Caveat:** `GitSecret`
has no Get/Update (in both this client and AM), so read-back/update must use List or a spec
change. **Architectural wrinkle (resolved in the plan):** `git-service` (which performs all the
SSA-plaintext applies today) has **no OC API client at all** — it writes K8s Secrets via
controller-runtime. Phase 0 (the merge, WS0.1) resolves this by collapsing the two services into
`asdlc-service`, so `secretmanagersvc` + the OC client live in one process (= AM's `amp-api`)
— no inter-service hop, no delegation. SecretReference creation stays where the OC client lives.
Verdicts:
`analysis/VERIFY-secrets.md`, `VERIFY-orchestration.md`, `VERIFY-topology.md`.
