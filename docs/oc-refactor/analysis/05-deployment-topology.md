# WSO2 Labs Agentic Engineer — Deployment / Plane Topology Analysis

Scope: `/Users/wso2/repos/labs-agentic-engineer/deployments/` (docker-compose, scripts, manifests, single-cluster values, k3d config) plus the per-service `workload.yaml` files at the repo roots. Goal: explain how the platform's own services and the OpenChoreo (OC) planes are wired *today* and what must change to run on WSO2 Cloud with correct plane separation.

Reference model (verified): WSO2 Agent Manager (AM) — `/Users/wso2/repos/agent-manager-analysis/05-deployment-topology.md` and `00-overview.md`. Citations to those files are written as `AM-05:§` / `AM-00:§`.

---

## Summary

The platform ("App Factory" / ASDLC) runs in **two halves on one machine**, exactly the same split AM uses for local dev (AM-05:§Summary item 1), but **it never graduated to AM's in-cluster install path**:

1. **Docker Compose stack** (`deployments/docker-compose.yml`) — the long-lived platform services: `postgres`, `asdlc-api` (the Go BFF), `agents-service`, `git-service`, `smee-client`, `console`. These run as plain Docker containers on a bridge network, joined to the **external k3d network `k3d-openchoreo`** (`docker-compose.yml:320-321`) so they can reach into the cluster.
2. **Single k3d cluster** (`deployments/k3d-local-config.yaml`, cluster name `openchoreo`, `env.sh:3`) hosting **all four OC planes co-located** (Control / Data / Workflow / Observability) plus Thunder (IDP), OpenBao (secrets), External Secrets Operator (ESO), kgateway, cert-manager, and the WSO2 API Platform gateway-operator.

The platform services reach OC through the **k3d serverlb on host port 8080**, routed by Host header. The integration glue is the env var pair **`PLATFORM_API_SERVICE_BASE_URL=http://k3d-openchoreo-serverlb:8080`** + **`PLATFORM_API_SERVICE_HOST=api.openchoreo.localhost`** (`.env`; `docker-compose.yml:51-52`) — the App-Factory analogue of AM's `OPEN_CHOREO_BASE_URL` (AM-05:§1).

**The decisive gap vs AM:** AM ships the platform tier itself as a **Helm chart (`wso2-agent-manager` → namespace `wso2-amp`)** plus **six extension charts**, each registering OC CRDs into the specific plane it extends (AM-05:§Summary item 2, §2). App Factory has **zero Helm charts of its own** (no `Chart.yaml` exists anywhere in the repo), **no `deployments-v2/` directory** (the README at `README.md:3` references one as the "WSO2 Cloud Flux/kustomize layered model", but it does not exist), and its OC extension resources are applied as **ad-hoc inline `kubectl apply` heredocs inside `setup-asdlc.sh`** rather than versioned chart templates. So the platform has no packaging artifact to deploy on WSO2 Cloud, and no plane discipline — everything lands in `default`, the `thunder`/`openbao` namespaces, or compose.

The per-service `workload.yaml` files (`asdlc-service/workload.yaml`, `agents/workload.yaml`, `git-service/workload.yaml`, `console/workload.yaml`, `database-service/workload.yaml`) **look like** an intent to run the platform services as OC-managed Workloads, but **they are not applied by any deploy path** — they are aspirational (see §4).

---

## 1. docker-compose topology (local dev)

File: `deployments/docker-compose.yml`. Networks: `asdlc` (bridge) + `k3d-openchoreo` (external, `:317-321`). Volume `pgdata`.

### Services

| Service | Image / build | Ports (host:container) | Notes |
|---|---|---|---|
| `postgres` (`asdlc-db`) | `postgres:16-alpine` (`:6`) | `5433:5432` (`:9`) | DB/user/pass all `asdlc` (`:11-13`); shared by asdlc-api + git-service |
| `asdlc-api` | build `../asdlc-service` (`:30-31`) | `9090:9090` (`:34`) | Go BFF + GitHub webhook receiver |
| `agents-service` | build `../agents` (`:159-160`) | `3400:3400` (`:163`) | AI BA/Architect/TaskGen agents (Vercel AI SDK) |
| `git-service` | build `../git-service` (`:192`) | `3300:3300` (`:195`) | Git ops + org-credential store; needs **K8s API access** |
| `smee-client` | `deltaprojects/smee-client:latest` (`:280`) | — | Relays `smee.io` webhooks → `asdlc-api:9090/webhooks/github` (`:284`) |
| `console` | build `..` / `console/Dockerfile` (`:296-298`) | `8090:3000` (`:301`) | React SPA via nginx |

No image is published to a registry — every platform service is a **local `build:` context**, so there is no consumable image tag for a cloud deploy (contrast AM's `ghcr.io/wso2/amp-*` images, AM-05:§2a).

### asdlc-api wiring (env, `:42-150`) — quoted, load-bearing

- **DB**: `DATABASE_URL=postgres://asdlc:asdlc@postgres:5432/asdlc?sslmode=disable` (`:46`).
- **OpenChoreo API** (the AM `OPEN_CHOREO_BASE_URL` analogue): `PLATFORM_API_SERVICE_BASE_URL` (from `.env` = `http://k3d-openchoreo-serverlb:8080`) and `PLATFORM_API_SERVICE_HOST=api.openchoreo.localhost` (`:51-52`). Routed by Host header through the k3d serverlb; `api.openchoreo.localhost` is also pinned via `extra_hosts → host-gateway` (`:148`).
- **Sister services**: `AGENTS_SERVICE_BASE_URL=http://agents-service:3400` (`:55`), `GIT_SERVICE_BASE_URL=http://git-service:3300` (`:56`).
- **Coding-agent reach-back** (passed verbatim into the WorkflowRun the runner pod reads, since the pod runs *in-cluster* and must call back to compose services on the host): `AGENT_GIT_SERVICE_URL=http://host.k3d.internal:3300` (`:62`), `AGENT_PLATFORM_URL=http://host.k3d.internal:9090` (`:63`).
- **GitHub webhooks**: `GITHUB_WEBHOOK_SECRET`, `GITHUB_WEBHOOK_PROXY_URL` (the `smee.io` channel), `OAUTH_STATE_SIGNING_KEY` (`:66-68`).
- **Inbound JWT (Thunder)**: `JWKS_URL=http://thunder.openchoreo.localhost:8080/oauth2/jwks` (`:78`), `JWT_ISSUER=http://thunder.openchoreo.localhost:8080` (`:79`), `JWT_AUDIENCE=asdlc-*` (`:80`).
- **Thunder admin (per-org publisher OAuth app lifecycle)**: `THUNDER_ADMIN_URL=http://thunder.openchoreo.localhost:8080`, `THUNDER_SYSTEM_CLIENT_ID=asdlc-system-client`, `THUNDER_SYSTEM_CLIENT_SECRET=asdlc-system-client-secret` (`:86-88`); `PLATFORM_IDP_ISSUER` (`:89`) and the in-cluster `PLATFORM_IDP_JWKS_URL=http://thunder-service.thunder.svc.cluster.local:8090/oauth2/jwks` (`:90`).
- **Outbound Service JWTs** (one client_credentials principal per audience): token URL `http://k3d-openchoreo-serverlb:8080/oauth2/token` with Host header `thunder.openchoreo.localhost`; clients `asdlc-api-client`, `asdlc-bff-to-git-service`, `asdlc-bff-to-agents-service` and their secrets (`:105-116`).
- **Task JWT signing** (RS256, runner→git-service): PEM mounted at `BFF_TASK_SIGNING_KEY_PATH=/app/keys/task-signing.pem` (`:121`, volume `:142`), issuer `asdlc-bff`, audience `git-service` (`:122-123`).
- **Observer (live progress)**: `OBSERVER_URL=http://observer.openchoreo.localhost:8080` (`:136`), with OAuth via `openchoreo-observer-resource-reader-client` (`:137-140`). Routed through the *main* kgateway (the obs-plane's own :11080 gateway is not exposed) — `extra_hosts` maps `observer.openchoreo.localhost → host-gateway` (`:150`).

There is **no OpenBao env on asdlc-api** — unlike AM (which writes secrets directly to OpenBao, AM-00:§matrix), here **git-service owns the OpenBao path** (see below). This is an architectural divergence worth flagging.

### agents-service wiring (`:164-180`)
`PORT=3400`; inbound JWT verify against Thunder JWKS, `JWT_AUDIENCE=asdlc-bff-to-agents-service` (`:169-171`). **No `ANTHROPIC_API_KEY`** — the Anthropic key is resolved per-call via `GIT_SERVICE_URL=http://git-service:3300` (`:176`) `/internal/anthropic/effective-key` (org key or platform fallback).

### git-service wiring (`:200-272`)
- DB same as asdlc-api (`:203`).
- **OpenBao (credential store)**: `OPENBAO_ADDR=http://host.docker.internal:8200`, `OPENBAO_TOKEN=root` (`:222-223`). OpenBao's in-cluster `NodePort 30820` is mapped to host port 8200 by `k3d-local-config.yaml:38`.
- **K8s API access**: `KUBECONFIG=/app/.kube/config` (`:258`), mounting an **internal kubeconfig** (server rewritten to `k3d-openchoreo-server-0:6443`, seeded by `start.sh:106-135`). git-service does SSA writes of per-WorkflowRun Secrets into `workflows-<orgID>` (build credential + Anthropic key). This is the AM `KUBECONFIG=/app/.kube/config` pattern (AM-05:§1 amp-api wiring item k8s).
- **GitHub App + webhooks**: `GITHUB_APP_ID/CLIENT_ID/CLIENT_SECRET/APP_SLUG`, private key mounted at `/etc/github-app/private-key.pem` (`:226-230`, `:263`); `GITHUB_WEBHOOK_DELIVERY_URL` = the smee URL (`:216`).
- **Inbound auth intentionally OFF in v1**: `JWKS_URL`/`JWT_AUDIENCE` commented (`:240-241`) — BFF mints+sends Service JWTs but git-service does not verify them (mirrors AM's `IS_LOCAL_DEV_ENV=true`). Task JWT verification on the credential-refresh path stays on: `BFF_JWKS_URL=http://asdlc-api:9090/auth/external/jwks.json` (`:243`).
- **Anthropic platform fallback**: `ANTHROPIC_PLATFORM_KEY=${ANTHROPIC_API_KEY}` (`:250`).
- Repo storage volume `${HOME}/specs/repos:/data/repos` (`:260`).

### smee-client (`:279-287`)
`deltaprojects/smee-client:latest`, command relays `${GITHUB_WEBHOOK_PROXY_URL}` (a `smee.io` channel auto-provisioned by `setup-asdlc.sh:643-651`) to `http://asdlc-api:9090/webhooks/github`. The README notes this **"would be replaced by a real ingress to the BFF"** in production (`docker-compose.yml:276-277`).

### console (`:295-312`)
nginx-served React SPA. `ASDLC_API_PROXY_URL=http://asdlc-api:9090` (`:305`, same-origin proxy), `VITE_THUNDER_URL=${PUBLIC_THUNDER_URL}` (`:306`), `VITE_THUNDER_CLIENT_ID=asdlc-console-client` (`:307`), sign-in/out redirects to `${PUBLIC_CONSOLE_URL}` (`:309-310`). Browser hits Thunder directly at `thunder.openchoreo.localhost`.

**Dev data flow:** browser → console:8090 → (nginx proxy) → asdlc-api:9090 → {OC kgateway `:8080` by Host header, Thunder `:8080`, Observer `:8080`} into k3d; git-service → OpenBao `:8200` + K8s API; agents-service → git-service for keys. Coding-agent + build pods run **in-cluster** and call back to the host BFF/git-service via `host.k3d.internal`.

---

## 2. Setup-script / plane-install breakdown

Orchestration: `setup.sh` runs, in order (`setup.sh:20-32`): `setup-k3d.sh` → `setup-prerequisites.sh` → `setup-openchoreo.sh` → `setup-observability.sh` → `setup-asdlc.sh`. Then the operator edits `.env` and runs `start.sh` (compose). Versions pinned in `env.sh`: `OPENCHOREO_VERSION=1.0.1-hotfix.1`, `THUNDER_VERSION=0.34.0`.

### setup-k3d.sh + utils.sh (cluster + DNS)
Creates the single k3d cluster from `k3d-local-config.yaml` (Traefik disabled, `:49`; `--tls-san=host.k3d.internal`, `:43`). `utils.sh` installs CoreDNS custom server blocks: `host.k3d.internal → docker bridge gateway` (`ensure_host_k3d_internal_in_coredns`, `utils.sh:145-192`) and a rewrite of `*.openchoreo.localhost` / `*.openchoreoapis.localhost → gateway-default.openchoreo-data-plane.svc` (`ensure_openchoreo_localhost_in_coredns`, `utils.sh:209-235`). These are local-only hacks with no WSO2 Cloud analogue.

### setup-prerequisites.sh — cluster-wide infra (no plane discipline)
Installs into mixed namespaces (`setup-prerequisites.sh`):
- Gateway API CRDs `v1.4.1` (`:28-29`); **cert-manager** `v1.19.4` → `cert-manager` ns (`:34`); **ESO** `2.0.1` → `external-secrets` ns (`:42`); **kgateway-crds + kgateway** `v2.2.1` → **`openchoreo-control-plane`** ns (`:50-54`).
- **OpenBao** `0.25.6` → `openbao` ns, **dev mode, root token `root`** (`:65-69`, `values-openbao.yaml:18-22`), `NodePort 30820`. The `postStart` hook (`values-openbao.yaml:23-95`) enables k8s auth and seeds policies/roles: `openchoreo-secret-{reader,writer}-{policy,role}` (reader bound to `dp*` namespaces, writer to `openbao`/`openchoreo-workflow-plane`), a narrow `asdlc-secret-reader` over `secret/data/asdlc/*`, plus seed KV (backstage, observer OAuth secret, opensearch creds, `asdlc/system/thunder-admin`).
- **ESO `ClusterSecretStore` named `default`** → vault at `http://openbao.openbao.svc:8200`, k8s auth role `openchoreo-secret-writer-role` (`:86-110`). (Note: AM names its store `amp-openbao-store`; App Factory uses the OC stock name `default` — AM-05:§2c.)
- **WSO2 API Platform gateway-operator** `0.6.0` (chart runtime `1.0.1`) → **`openchoreo-data-plane`** ns (`:123-128`, `operator-values.yaml`). Generates an AES-GCM controller key Secret (`:139-150`), applies `gateway-config.yaml` (Thunder keymanager ConfigMap), `rbac.yaml` (lets `cluster-agent-dataplane` SA reconcile `RestApi`), and `api-gateway.yaml` — an **`APIGateway` CR `api-platform-default`** in `openchoreo-data-plane` (`api-platform/api-gateway.yaml:1-23`). This is App Factory's analogue of AM's `wso2-amp-ai-gateway-extension` (AM-05:§2g), but applied as raw manifests, not a chart.

### setup-openchoreo.sh — the four planes + Thunder
- **Control Plane** → `openchoreo-control-plane`, chart `oci://ghcr.io/openchoreo/helm-charts/openchoreo-control-plane@1.0.1-hotfix.1`, `values-cp.yaml` (API host `api.openchoreo.localhost`, kgateway httpPort 8080, OIDC issuer `${PUBLIC_THUNDER_URL}`, jwks at `thunder-service.thunder.svc:8090`) (`setup-openchoreo.sh:45-57`; `values-cp.yaml:1-29`). Backstage enabled (`values-cp.yaml:16`); pre-creates `backstage-secrets` (`setup-openchoreo.sh:31-39`).
- **Data Plane** → `openchoreo-data-plane`, `values-dp.yaml` (gateway httpPort 19080, TLS off) (`:69-73`). Registered as **`ClusterDataPlane/default`** with `secretStoreRef: default`, external ingress host `openchoreoapis.localhost:19080` (`utils.sh:84-114`).
- **Thunder** `0.34.0` → `thunder` ns, `values-thunder.yaml` (`:95-99`). Its own HTTPRoute attaches to `openchoreo-control-plane/gateway-default`, hostnames `thunder.openchoreo.localhost` + `${PUBLIC_THUNDER_HOST}` (`values-thunder.yaml:6-14`). Setup patches a **CORS filter** onto that HTTPRoute (`:117-169`) and overrides Backstage's token URL to the in-cluster Thunder svc (`:175-181`). Thunder's setup Job runs the bootstrap scripts in `values-thunder.yaml`: **`59-asdlc-oauth-apps.sh`** registers all OAuth clients (`openchoreo-workload-publisher-client`, `openchoreo-observer-resource-reader-client`, `asdlc-api-client`, the three `asdlc-bff-to-*` clients, `asdlc-local-dev-seeder`, `asdlc-system-client`, and the public PKCE `asdlc-console-client`) (`values-thunder.yaml:115-128`); **`60-asdlc-system-role.sh`** binds `asdlc-system-client` to Thunder's Administrator role (`:315-384`).
- **Workflow Plane** → `openchoreo-workflow-plane` + a `docker-registry` (twuni chart) (`:194-202`). Registered as **`ClusterWorkflowPlane/default`** with `secretStoreRef: default` (`utils.sh:117-135`).

### setup-observability.sh — Observability Plane
→ `openchoreo-observability-plane` (`setup-observability.sh`): chart `openchoreo-observability-plane@1.0.1-hotfix.1` (Observer + cluster-agent + RCA, its own :11080 gateway **disabled**, `:127-129`) and `observability-logs-opensearch@0.3.11` (OpenSearch + Fluent Bit). ExternalSecrets `opensearch-admin-credentials` + `observer-secret` pull from OpenBao via `ClusterSecretStore default` (`:58-96`). A **cross-namespace HTTPRoute `observer-mainkgw`** on `openchoreo-control-plane/gateway-default` exposes `observer.openchoreo.localhost` on the main :8080 gateway so the compose BFF can reach it (`:188-211`). Registers **`ClusterObservabilityPlane/default`** (`observerURL: http://observer.openchoreo.localhost:11080`, `:256-270`). Adds `ClusterAuthzRole app-factory-observer-reader` (+ binding for `openchoreo-observer-resource-reader-client`) granting `logs:view`/`workflowrun:view` etc. (`:223-250`).

### setup-asdlc.sh — App Factory's OC "extension", but as inline kubectl
This is the closest thing to AM's `wso2-amp-platform-resources-extension` (AM-05:§2d) — but it is **not a chart**; it is a script that `kubectl apply`s heredocs into namespace **`default`** (after labeling it `openchoreo.dev/control-plane=true`, `:98`). It registers:

| Resource | Kind / apiVersion | Where | Cite |
|---|---|---|---|
| `dockerfile-builder` | **ClusterWorkflow** (`openchoreo.dev/v1alpha1`) + Argo CWTs | (CP-scoped) | `setup-asdlc.sh:57`; `manifests/docker-build-workflow.yaml` |
| `app-factory-coding-agent` | **ClusterWorkflow** | (CP-scoped) | `setup-asdlc.sh:66-88`; `manifests/app-factory-coding-agent.yaml:1-4` |
| `service` | **ClusterComponentType** (`workloadType: deployment`, allowedWorkflows `dockerfile-builder`, allowedTraits `api-configuration`) | `default` | `:110-324` |
| `web-application` | **ClusterComponentType** (subdomain routing) | `default` | `:338-528` |
| `api-configuration` | **ClusterTrait** | `default` | `:332`; `manifests/api-platform/api-configuration-trait.yaml` |
| `development` | **Environment** (dataPlaneRef `ClusterDataPlane/default`) | `default` | `:531-541` |
| `default` | **DeploymentPipeline** (single env) | `default` | `:545-556` |
| `asdlc-api-client-binding`, `administrators-group-binding` | **ClusterAuthzRoleBinding** (sub `asdlc-api-client` → admin; group `Administrators` → admin) | cluster | `:564-592` |
| `workflows-default` namespace | label `openchoreo.dev/workflow-plane-name=default` | — | `:105-106` |

It also **generates `.env`** (`:670-735`): `PLATFORM_API_SERVICE_BASE_URL`, public URLs, the smee.io channel, webhook/HMAC secrets, the RS256 Task JWT keypair, and preserves operator-supplied `ANTHROPIC_API_KEY`/GitHub App creds across re-runs. So in App Factory the `.env` is **machine-generated config** the compose stack reads — there is no equivalent of AM's Helm `values.yaml` for the platform tier.

`setup-thunder-client.sh` is **POC-only** (the `poc-api-platform` branch): it `kubectl exec`s into the Thunder pod to create the `poc-api-platform-client` confidential OAuth app for `verify-api-platform.sh`'s truth-table test (`setup-thunder-client.sh:14-138`). Not part of the main runtime.

---

## 3. Manifests + single-cluster values + k3d config — what's registered where

- **`manifests/docker-build-workflow.yaml`** — the `dockerfile-builder` ClusterWorkflow (Argo ClusterWorkflowTemplates: checkout, generate-workload-cr, build, publish-image). The `generate-workload-cr` step reads a **`workload.yaml` descriptor from the user's repo** (`:406-456`) via `ghcr.io/openchoreo/openchoreo-cli`; exchanges tokens at Thunder via `openchoreo-workload-publisher-client`. Build runs `ghcr.io/openchoreo/podman-runner:v1.1` (`:385`), publishes to the workflow-plane registry.
- **`manifests/app-factory-coding-agent.yaml`** (+ `.dev-patch.yaml`) — ClusterWorkflow that runs a Claude Agent SDK session in an ephemeral Workflow-Plane pod for one ComponentTask (`workflowPlaneRef: ClusterWorkflowPlane/default`, `:1-12`). The dev patch overlays a hostPath mount of the host's `remote-worker/plugin` onto `/app/plugin` for live skill edits (`setup-asdlc.sh:78-87`).
- **`manifests/api-platform/`** — `operator-values.yaml`, `gateway-config.yaml` (Thunder keymanager), `rbac.yaml`, `api-gateway.yaml` (`APIGateway/api-platform-default` in `openchoreo-data-plane`), `api-configuration-trait.yaml` (the ClusterTrait). Applied by setup-prerequisites/setup-asdlc.
- **`manifests/poc-api-platform/`** — branch-scoped POC only: two hello-world Components (`poc-public`, `poc-protected`) using `mendhak/http-https-echo`, with Workload + Component + ReleaseBinding manifests. **Not part of the platform runtime.**
- **`single-cluster/values-{cp,dp,thunder,openbao}.yaml`** — the OC plane + Thunder + OpenBao Helm values (detailed in §2). These are the *only* versioned, declarative deploy artifacts in the repo, and they configure **OC/third-party charts, not App Factory services**.
- **`k3d-local-config.yaml`** — single server, agents 0; port map exposes CP 8080/8443, DP 19080/19443, WP 10081/10082, OP 11080/11081→5601/11082→9200/11085, and OpenBao 8200→30820 (`:10-39`). All planes share one node — the antithesis of plane separation.

---

## 4. `workload.yaml` status — aspirational, not used by the deploy

The five root-level `workload.yaml` files are **partial OC Workload descriptors** that suggest an intent to run the platform's own services as OC-managed Workloads:

- `asdlc-service/workload.yaml` — `metadata.name: app-factory-api`, endpoint `http:8080` (visibility `project`), dependencies on `app-factory-git-service` + `app-factory-agents-service` with `envBindings` (`address → GIT_SERVICE_BASE_URL` / `AGENTS_SERVICE_BASE_URL`).
- `agents/workload.yaml` — `app-factory-agents-service`, endpoint `http:3400` (project).
- `git-service/workload.yaml` — `app-factory-git-service`, endpoint `http:3300` (visibility **internal**, with a comment about the coding-agent pod reaching it via the OC-generated NetworkPolicy).
- `console/workload.yaml` — `app-factory-console`, endpoint `http:3000` (**external**), dependency on `app-factory-api` (`address → ASDLC_API_PROXY_URL`).
- `database-service/workload.yaml` — the only one shaped as a full CR (`kind: Workload`, `spec.owner`, `spec.endpoints`), but `database-service` was **removed from the architecture** (`README.md:55`), so it is dead.

**Are they used by the deploy? No.** Grep for `workload.yaml` across `deployments/` shows the only consumer is the **build pod's `generate-workload-cr` step** (`docker-build-workflow.yaml:406-443`), which reads a `workload.yaml` *from a user's application repo* to build a *user app's* Workload CR — it never reads these platform-service files. No script `kubectl apply`s them; no Component/ReleaseBinding exists for the platform services; their ports/visibilities don't even match the runtime (e.g. asdlc-api runs on 9090 in compose, the descriptor says 8080). They are **design-time artifacts / aspirational** for a future where App Factory dogfoods OC to host itself, consistent with `docs/design/oc-native-migration.md` (referenced in the repo grep). Today the platform services are **compose-only**.

---

## 5. Plane topology table — current vs WSO2-Cloud target

| Deployable | Source today | Runs today | Should run on WSO2 Cloud (plane) |
|---|---|---|---|
| `asdlc-api` (BFF) | compose `build: ../asdlc-service` | Docker container (host) | Control-plane app tier (own ns, e.g. `app-factory`), as OC-managed Workload or hosted control-plane service — published image + Helm/Flux |
| `agents-service` | compose `build: ../agents` | Docker container | Control-plane app tier (same ns) |
| `git-service` | compose `build: ../git-service` | Docker container (needs K8s API + OpenBao) | Control-plane app tier; its K8s-write + secret-write role must become a scoped ServiceAccount/role, not host kubeconfig + root token |
| `console` | compose `build: console/Dockerfile` | Docker container | Control-plane app tier (external ingress) |
| `postgres` | compose `postgres:16-alpine` | Docker container | Managed Postgres (cloud DB) or in-ns StatefulSet; not a dev container |
| `smee-client` | compose `deltaprojects/smee-client` | Docker container | **Removed** — replaced by a real ingress/webhook endpoint to the BFF |
| OC Control Plane | `openchoreo-control-plane@1.0.1-hotfix.1` | `openchoreo-control-plane` ns (shared node) | **Control Plane** cluster/namespace |
| OC Data Plane | `openchoreo-data-plane` | `openchoreo-data-plane` ns (shared node) | **Data Plane** (separate cluster on WSO2 Cloud) |
| OC Workflow Plane + registry | `openchoreo-workflow-plane` + twuni registry | `openchoreo-workflow-plane` ns (shared node) | **Workflow Plane** (separate) |
| OC Observability Plane + OpenSearch/Fluent Bit | `openchoreo-observability-plane@1.0.1-hotfix.1` + `observability-logs-opensearch@0.3.11` | `openchoreo-observability-plane` ns | **Observability Plane** (separate) |
| Thunder IDP | `asgardeo/helm-charts/thunder@0.34.0` (SQLite) | `thunder` ns | Shared IDP (control-side); SQLite → managed DB; HA |
| OpenBao (secrets) | `openbao@0.25.6` **dev mode, root token** | `openbao` ns | Production secret backend (HA, sealed, real auth) — control-side |
| ESO + `ClusterSecretStore default` | `external-secrets@2.0.1` | `external-secrets` ns; store reads OpenBao | Data Plane–side ESO materializing into agent/user-app namespaces |
| kgateway | `kgateway@2.2.1` | `openchoreo-control-plane` ns | Per OC chart placement (CP gateway) |
| API Platform gateway-operator + `APIGateway` CR | `wso2/api-platform/gateway-operator@0.6.0` + `api-gateway.yaml` | `openchoreo-data-plane` ns | **Data Plane** (managed by operator) |
| ClusterComponentTypes `service`/`web-application`, ClusterTrait `api-configuration`, Env/Pipeline, AuthzRoleBindings | inline heredocs in `setup-asdlc.sh` → `default` | CP scope | **Control Plane** via a versioned **extension chart** (AM pattern) |
| ClusterWorkflows `dockerfile-builder`, `app-factory-coding-agent` + Argo CWTs | `manifests/*.yaml` | CP-scoped, run on WP | **Control Plane** registration → execute on **Workflow Plane** |
| User app workloads + coding-agent/build pods | OC Components / WorkflowRuns | **Data Plane** / **Workflow Plane** (`workflows-default`) | Same — this part already follows OC discipline |

---

## 6. Gap vs AM / OC (cite AM-05 / AM-00)

1. **No platform Helm chart.** AM packages its app tier as `wso2-agent-manager` → `wso2-amp` (Postgres + amp-api + console + jobs + RBAC) with published `ghcr.io/wso2/amp-*` images (AM-05:§2a, §Summary item 2). App Factory has **no `Chart.yaml` at all** and **only local `build:` contexts** — there is no deployable artifact for the platform services. This is the single biggest blocker for a WSO2 Cloud install.

2. **No extension charts; CRDs applied as inline kubectl.** AM ships **six extension charts**, each installed into the plane it extends and registering OC CRDs there (ComponentType/Trait/ClusterWorkflow/Pipeline/Environment/Project/AuthzRoleBinding) (AM-05:§2b–2g, §4 table). App Factory's equivalent CRDs are `kubectl apply` heredocs inside `setup-asdlc.sh` (§2 here), all landing in `default`. There is no chart to version, template per-environment, or install into a remote plane.

3. **Everything is co-located on one node; no real plane separation.** `k3d-local-config.yaml` runs `servers:1, agents:0` with all four planes' ports on one loadbalancer. AM's reference is the same single-cluster shape *for local dev* (AM-05:§3), but AM cleanly separates concerns by **namespace + chart per plane**, which is the seam you split across clusters on WSO2 Cloud. App Factory has the namespaces (because the OC charts create them) but bolts its own resources into `default`, `thunder`, `openbao`.

4. **The platform tier is host-Docker, not in-cluster.** Both AM and App Factory use compose for local dev (AM-05:§1; App Factory README). But AM *also* has a **fully in-cluster path** (`quick-start/install.sh`, AM-05:§3 "Two bring-up paths", §5) that deploys amp-api/console into `wso2-amp`. App Factory has **only** the compose path — `start.sh` just does `docker compose up --build` (`start.sh:165`). The README's promised `deployments-v2/` (Flux/kustomize, WSO2 Cloud) **does not exist**.

5. **Secret discipline differs / dev-mode OpenBao.** OpenBao runs in **dev mode with root token `root`** (`values-openbao.yaml:18-22`, `OPENBAO_TOKEN=root` in compose `:223`). AM uses ESO + `SecretReference` so plaintext never crosses the OC control-plane API (AM-00:§7 item 7). App Factory does use ESO + the `${dataplane.secretStore}` ExternalSecret pattern in its ComponentTypes (`setup-asdlc.sh:230-279`) — good — but the credential-write path goes through **git-service holding a host kubeconfig + root OpenBao token**, which has no WSO2 Cloud-safe form.

6. **Local-only DNS/network hacks.** `host.k3d.internal` CoreDNS injection, `*.openchoreo.localhost` rewrites (`utils.sh:145-235`), `host-gateway` extra_hosts, the smee.io webhook relay, and the internal-kubeconfig rewrite (`start.sh:106-135`) are all artifacts of "compose-on-host talking to k3d". None survive a move to WSO2 Cloud; they must be replaced by in-cluster Service DNS + a real ingress.

7. **Workload self-hosting is aspirational.** The `workload.yaml` files (§4) show the *intent* to run platform services as OC Workloads (the cleanest WSO2 Cloud target), but nothing applies them. AM-00:§"Lessons" endorses exactly this: be a REST client of the OC API and emit Project/Component/Workload/SecretReference CRs.

---

## 7. What must change for WSO2 Cloud (with OC plane discipline)

In rough priority order:

1. **Produce a platform Helm chart** (analogue of `wso2-agent-manager`) for `asdlc-api` + `agents-service` + `git-service` + `console` (+ Postgres dependency or external DB), deployed into a dedicated control-plane-side namespace (e.g. `app-factory`). Requires first **publishing images** to a registry (replace compose `build:` with `ghcr.io/wso2/app-factory-*` tags). All the `.env` wiring becomes Helm `values.yaml` keys; `PLATFORM_API_SERVICE_BASE_URL` becomes the in-cluster `http://openchoreo-api.openchoreo-control-plane.svc:8080` (AM's `agentManagerService.config.openChoreo.baseURL`, AM-05:§2a).
2. **Convert `setup-asdlc.sh`'s inline CRDs into a versioned extension chart** (ComponentTypes `service`/`web-application`, ClusterTrait `api-configuration`, the two ClusterWorkflows + Argo CWTs, Environment, DeploymentPipeline, ClusterAuthzRoleBindings), installed into the **Control Plane** and registering build/agent workflows for execution on the **Workflow Plane** (AM-05:§2d, §2f).
3. **Wrap the API Platform gateway pieces** (`api-gateway.yaml`, `gateway-config.yaml`, `rbac.yaml`, AES key, trait) into a **Data-Plane extension chart** (analogue of `wso2-amp-ai-gateway-extension`, AM-05:§2g) instead of raw `kubectl apply`s in setup-prerequisites.
4. **Split planes across clusters/namespaces** as WSO2 Cloud provides them: CP (OC control plane + Thunder + OpenBao + platform app tier + extension CRDs), DP (data plane + gateway-operator + ESO materialization), WP (workflow plane + registry), OP (observability plane + OpenSearch). Register DataPlane/WorkflowPlane/ObservabilityPlane via their cluster-agent CRs (already done in `utils.sh`/`setup-observability.sh`, just across real boundaries).
5. **Harden secrets**: OpenBao out of dev mode (HA, real auth, no root token); git-service's K8s writes via a scoped ServiceAccount/Role (drop the host kubeconfig); keep the ESO `SecretReference` pattern.
6. **Replace local-only glue**: smee.io relay → a real ingress to the BFF's `/webhooks/github`; `host.k3d.internal` / `*.localhost` CoreDNS hacks → in-cluster Service FQDNs + cloud DNS; `extra_hosts`/internal-kubeconfig seeding → removed.
7. **Decide the self-hosting model**: either (a) finish the aspirational path and run the platform services as **OC-managed Workloads** (apply the `workload.yaml` descriptors via real Components — the dogfood model, AM-00:§Lessons), or (b) run them as an **externally-hosted control-plane app tier** via the Helm chart in step 1 (AM's actual model). AM chose (b); App Factory's `workload.yaml` files hint at (a). This choice should be made explicitly before building the chart.
8. **Create the missing `deployments-v2/`** (or rename) so the README's "WSO2 Cloud Flux/kustomize layered model" is real — the layered Flux/kustomize structure is the natural home for the charts in steps 1–3 across the four planes.
