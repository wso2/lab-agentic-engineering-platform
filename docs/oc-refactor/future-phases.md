# Future Phases (post-M1) — deferred scope

> **Out of scope for the current Phase 0–1 (M1, self-hosted) effort.** The main docs (`00`/`10`/`20`)
> cover M1 only; everything here is fast-follow. Kept so M1 doesn't paint us into a corner.
> Gap IDs (G4–G10) and AM refs (`/Users/wso2/repos/agent-manager-analysis/`) as before.

M1 reaches a working, secret-clean, self-hosted launch on the `openbao` provider. Deferred:
**Phase 2** LLM governance · **Phase 3** WSO2 Cloud packaging + `secret-manager-api` · **Phase 4**
lifecycle. *If any becomes a hard launch requirement, pull the specific workstream into M1.*

---

## Cloud secret architecture (the rule that drives Phase 3)

At M1 the local `openbao` backend is **ReadWrite**, so the control plane can read a tenant secret
(e.g. the per-org GitHub PAT) in-process for its own GitHub calls — fine. **On WSO2 Cloud this
changes:** the platform `secret-manager-api` is **WriteOnly** (`GetSecretWithValue → ErrNotSupported`,
`agent-platform/.../secrets/provider.go:62,428`). ESO still materializes values into pods (it reads
Vault directly via the ClusterSecretStore, not the API), but **the control plane can never read a
tenant secret back.**

**The rule (verified against AM):** split secrets by *who consumes them*.
- **Control-plane-consumed credentials are *platform/bootstrap* secrets** — provisioned in Vault by
  PE/GitOps, surfaced as `SecretReference`s, materialized into the CP's **env/file mounts** by ESO
  (readable). AM does exactly this for `THUNDER_CLIENT_SECRET`, DB password, encryption key, and JWT
  signing keys (every `secretKeyRef` in AM's cloud ReleaseBinding). These are *not* read back from the
  WriteOnly tenant API.
- **Tenant secrets consumed by pods/gateway** (LLM keys) → WriteOnly write-path + ESO-into-pods; CP
  never reads them.
- **GitHub:** AM uses a **server-wide PAT** (platform secret, config) and reads **per-org git creds
  from a *separate readable* OpenBao** (`WORKFLOW_PLANE_OPENBAO_URL`), never the WriteOnly store; build
  creds go via `GitSecret`. (`agent-manager` `repository_service.go:53-56`, `git_credentials_service.go:55-62`.)

**Consequence for labs on cloud:** the control plane must get GitHub auth *without* reading a tenant
PAT back. Two AM-grounded options:
1. **GitHub App (recommended):** hold the App private key as a *platform* secret (ESO-mounted file,
   like AM's signing keys) and mint short-lived per-org installation tokens in-process. → makes
   **App mode (G8) a Phase-3 cloud prerequisite, not a Phase-4 afterthought.**
2. **AM's literal pattern:** keep per-org PATs in a *separate readable* OpenBao the CP reads directly
   (not the WriteOnly `secret-manager-api`).

---

## Phase 2 — LLM governance & model-agnostic (G4)

- **WS2.1 — Interpose the API-Platform AI gateway.** Route the TS `agents` + the coding-agent runner
  through the gateway (`deployments/manifests/api-platform/`); agents get a **proxy key**, the
  provider key becomes the gateway's **upstream auth**. Remove direct `createAnthropic({apiKey})`
  (`create-agent.ts:62`). (AM `02`/`07`.)
- **WS2.2 — Per-org / per-stage model + provider selection.** Replace the global `AGENT_MODEL`
  (`analysis/04`) with per-org config, applied as a gateway route.
- **WS2.3 — Delete the `effective-key` endpoint.** Agents now use the proxy key. (Note: the WriteOnly
  cloud backend can't serve `effective-key` anyway → **gateway is also a cloud prerequisite**.)
- **WS2.4 — (optional) usage/cost flow-back.**
- **Test gate 2:** no long-lived service holds a provider key; `effective-key` gone; LLM calls
  traverse the gateway; per-org model routing works.

---

## Phase 3 — WSO2 Cloud packaging + `secret-manager-api` (G5/G10)

- **WS3.1 — Publish images + platform helm chart** (the platform helm chart (name TBD): Postgres + api + agents
  + console + jobs) into a CP app-tier namespace. (AM `05`.)
- **WS3.2 — Extension charts** (convert `setup-asdlc.sh` heredocs): platform-resources (CP), secrets
  (OpenBao + store + roles from WS1.3), ai-gateway (DP), observability (OP). Reuse `.cicd/` ECR build;
  fix stale `workload.yaml` (port `8080`→`9090`). (AM `05`.)
- **WS3.3 — Private overlay: port the `secret-manager-api` provider (open-core split, option (b)).**
  Separate module/repo importing the public `asdlc-service` core, with its **own `main.go`**
  constructing `secrets.NewProvider(...)` — mirroring `agent-platform` over OSS `agent-manager`. Port
  the provider from `/Users/wso2/repos/agent-platform/agent-manager-service/secrets/` (~470 LOC, same
  interfaces, `WriteOnly`, `ManagesSecretReferences()=true`, JWT-from-context auth, REST `/secrets`).
  Config: `SECRET_MANAGER_PROVIDER=secret-manager-api` + `SECRET_MANAGER_API_URL=…` (no
  `OPENBAO_TOKEN`); bootstrap secrets as Vault `SecretReference`s (shape per `09`). Also implement the
  control-plane GitHub auth resolution above (App tokens). *Additive overlay + config — not "same
  binary."*
- **WS3.4 — Cross-cluster plane separation (G10):** split the four planes across cluster/namespace
  boundaries; fix DNS hacks. (Single-host hardening was already done in M1 WS1.6.)
- **Test gate 3:** helm install on a cloud-like cluster; CRDs land in their planes; with
  `secret-manager-api` the overlay build runs and tenant secrets resolve via the platform service;
  bootstrap secrets from Vault `SecretReference`s; no compose / heredocs / host kubeconfig.

---

## Phase 4 — Lifecycle & deploy correctness (G6–G9)

- **WS4.2 — Environments + promotion (G6):** `Environment` + `DeploymentPipeline`; promote via
  `UpdateReleaseBinding` state instead of dev-only AutoDeploy. (AM `01`.)
- **WS4.3 — Register the coding agent as a proper component Workflow (G7):** stop omitting
  `openchoreo.dev/component` (`component_client.go:1020`); list it in `allowedWorkflows`. (AM `04`.)
- **WS4.4 — Remove OC API creds from the build pod (G9):** CP mutates the Workload instead of the
  build pod `curl`-ing the API server (`docker-build-workflow.yaml:473-575`); drop the hardcoded
  `oauth-client-secret`.
- **WS4.5 — GitHub App mode (G8):** **promote to Phase 3** if going to cloud (it's the cloud
  GitHub-auth resolution above); otherwise fix/remove the dead App paths.
- **Test gate 4:** promotion dev→prod via binding-state; coding-agent workflow runs through the
  allow-list; build pod holds no OC API creds.

---

## Cloud-relevant risk notes
- **WS3.3 is additive, not "config-only"** — the `secret-manager-api` provider is a portable artifact
  in `agent-platform` (~470 LOC), ported into a private overlay with its own `main.go`. Open-core,
  two build targets, like AM (validation BLOCKER-1).
- The provider selection is at the **composition root** (each build's `main.go`), not a runtime env
  switch — `registry.go` exists but AM's `main.go` doesn't use it.
