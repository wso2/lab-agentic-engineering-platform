# App Factory — v1 local setup (pure OpenChoreo + Docker Compose)

A lighter alternative to `deployments-v2/` (which uses WSO2 Cloud's Flux/kustomize layered model). v1 runs the same code, but:

- **Long-lived services** (BFF, git-service, agents-service, console, postgres, smee-client) run in `docker compose`.
- **Coding-agent** runs as one-shot pods via the same `app-factory-coding-agent` ClusterWorkflow as v2 (`manifests/app-factory-coding-agent.yaml`).
- **Builds** use the `dockerfile-builder` ClusterWorkflow (`manifests/docker-build-workflow.yaml`); the build pod's `generate-workload-cr` step exchanges OAuth tokens at Thunder via the `openchoreo-workload-publisher-client` we bootstrap.
- **OpenChoreo + Thunder + OpenBao + ESO + kgateway** are installed via direct `helm install`s — no Flux.

This setup mirrors `agent-manager`'s docker-compose pattern (`/Users/wso2/repos/agent-manager/deployments/docker-compose.yml`) where it can; departures are commented in the manifests.

## Quick start

```bash
# 1. One-shot bring-up — k3d cluster + prereqs + OpenChoreo + Thunder + ASDLC infra
bash scripts/setup.sh

# 2. Edit .env to set ANTHROPIC_API_KEY (and optional GITHUB_APP_* values)
$EDITOR .env

# 3. Start the long-lived compose stack
bash scripts/start.sh
# → http://localhost:8090 (admin / admin)
```

## Architecture (host-side compose ↔ in-cluster OC)

```
┌─────────────────────── docker compose ───────────────────────┐
│ console (nginx)  asdlc-api  git-service  agents-service       │
│        :8090         :9090       :3300           :3400        │
│                                                               │
│ postgres :5433  smee-client (relays smee.io → asdlc-api)      │
└───────────────────────────┬───────────────────────────────────┘
                            │  same docker network: k3d-openchoreo
                            ▼
┌──────────────────────── k3d cluster ──────────────────────────┐
│ OC Control / Data / Workflow planes                           │
│ Thunder IDP   OpenBao   ESO   kgateway                        │
│                                                               │
│ ClusterWorkflow: app-factory-coding-agent  ← BFF dispatches   │
│ ClusterWorkflow: dockerfile-builder        ← BFF dispatches   │
└───────────────────────────────────────────────────────────────┘
```

Key wiring:

- `git-service` uses **host KUBECONFIG** (seeded by `start.sh` from `k3d kubeconfig get … --internal`) to write per-WorkflowRun Secrets into `workflows-default`. Mirrors agent-manager's `KUBECONFIG=/app/.kube/config` env knob.
- The coding-agent pod reaches `git-service` and `asdlc-api` (running on the host) via `host.k3d.internal`, which we pin to the **docker bridge gateway** in CoreDNS NodeHosts. Pods → host.
- `OPENBAO_ADDR=host.docker.internal:8200` — OpenBao's `NodePort` 30820 is exposed on host port 8200 by `k3d-local-config.yaml`.
- Thunder OAuth apps (`asdlc-console-client`, `asdlc-api-client`, BFF→service triplets, **`openchoreo-workload-publisher-client`**) are bootstrapped via Thunder helm pre-install scripts (`single-cluster/values-thunder.yaml`), same pattern as agent-manager's `wso2-amp-thunder-extension`.

## What was removed from the previous v1

- `database-service` / `mysql` — no longer in the architecture (deprecated).
- `collab-server` — collaborative editing is deferred.
- Long-lived `remote-worker` container — coding agent is now a one-shot pod via `ClusterWorkflow: app-factory-coding-agent`.

## Files

| Path | Purpose |
|---|---|
| `scripts/setup.sh` | One-shot chain: k3d → prereqs → OpenChoreo → ASDLC infra |
| `scripts/setup-k3d.sh` | k3d cluster + CoreDNS |
| `scripts/setup-prerequisites.sh` | cert-manager + ESO + kgateway + OpenBao |
| `scripts/setup-openchoreo.sh` | Control Plane + Data Plane + Workflow Plane + Thunder |
| `scripts/setup-asdlc.sh` | ClusterWorkflows + ClusterComponentTypes + Environment + AuthzRoleBindings + `.env` |
| `scripts/start.sh` | Refresh DNS, seed kubeconfig, `docker compose up` |
| `scripts/stop.sh` | `docker compose down` (cluster stays) |
| `manifests/docker-build-workflow.yaml` | `dockerfile-builder` ClusterWorkflow (Argo CWTs) |
| `manifests/app-factory-coding-agent.yaml` | Coding-agent one-shot pod template (mirrors v2 exactly) |
| `single-cluster/values-thunder.yaml` | Thunder helm values + bootstrap scripts (users, OAuth apps) |
| `single-cluster/values-cp.yaml` | OC Control Plane helm values |
| `single-cluster/values-dp.yaml` | OC Data Plane helm values |

## Credentials

The Thunder default admin (`admin` / `admin`) is in the **Administrators** group. `setup-asdlc.sh` binds that group to the OC `admin` ClusterAuthzRole.

For GitHub repo provisioning, connect a PAT (or GitHub App) at **Settings → GitHub Integration**.
For AI generation, connect an Anthropic key at **Settings → Anthropic Integration** (or rely on the platform fallback `ANTHROPIC_API_KEY` from `.env`).

## Tear down

```bash
bash scripts/stop.sh                # stops compose; cluster stays
k3d cluster delete openchoreo       # destroy cluster (loses all OC state)
```
