# WSO2 Labs Agentic Engineer

> ⚠️ ** EXPERIMENTAL — PROOF OF CONCEPT**
>
> This repository is a research/PoC effort. It is **not production-ready**, APIs and
> data models change without notice, and entire subsystems may be ripped out between
> commits. There are no stability guarantees. Do not deploy this as-is.

Repository: [`wso2/labs-agentic-engineer`](https://github.com/wso2/labs-agentic-engineer)

An AI-driven, spec-driven engineering platform built on top of
[OpenChoreo](https://github.com/openchoreo/openchoreo). It explores what an
end-to-end "describe a project in markdown, get a deployed system" workflow could
look like, with AI agents driving every stage:

```
Specification → Design → Implementation → Build → Deploy → Manage
```

## Trying it out

Today, the only way to try this is **locally** — clone the repo and follow the
[Running it locally](#running-it-locally) section below. We're actively looking for
feedback: bug reports, ideas, "this is confusing," "this is broken" — all welcome
via GitHub issues on the repo.

A hosted version is planned: this will eventually be **deployed on WSO2 Cloud** so
anyone interested can try it without standing up the local stack. Until then,
local is the only path. Watch this section for the hosted URL once it lands.

## What's in here

| Path | Role |
|---|---|
| [`console/`](./console) | React + Oxygen UI frontend (the web app) |
| [`asdlc-service/`](./asdlc-service) | Go BFF — REST API, GitHub webhook receiver, OpenChoreo proxy |
| [`git-service/`](./git-service) | Go microservice for git ops (clone, commit, push, tag, version) |
| [`agents/`](./agents) | TypeScript agents service — BA, Architect, TaskGen, Wireframe (Vercel AI SDK) |
| [`remote-worker/`](./remote-worker) | One-shot coding-agent runner image (Claude Agent SDK, runs as a k8s pod) |
| [`ui-components/`](./ui-components) | pnpm workspace packages used by the console |
| [`deployments/`](./deployments) | **Local setup** — k3d cluster + Docker Compose stack |
| [`tests/`](./tests) | Playwright (E2E) + vitest (API integration) |
| [`docs/design/`](./docs/design) | Architecture & component design docs |
| [`requirements/`](./requirements) | User scenarios that drive features and E2E tests |
| [`schemas/`](./schemas) | Shared JSON/YAML schemas |
| `database-service/`, `collab-server/`, `local-dispatcher/` | Older/legacy components, kept for reference |

> **A note on naming.** Service names and code identifiers still use `asdlc-*`
> (e.g. `asdlc-service`, `asdlc-api`, `asdlc-console`). That's the original
> internal codename and changing it is a separate cleanup. The product itself is
> now the **Agentic Engineering**.

`AGENTS.md` (symlinked as `CLAUDE.md`) is the day-to-day operations cheat sheet for
contributors. The root `README.md` you're reading is the on-ramp; `AGENTS.md` is the
detailed manual.

## Running it locally

Everything starts from [`deployments/`](./deployments) — that's the local
setup. It brings up a k3d cluster (OpenChoreo + Thunder + OpenBao + ESO + kgateway)
and a Docker Compose stack for the long-lived services.

### Prerequisites

- Docker + `docker compose`
- [`k3d`](https://k3d.io/), `kubectl`, `helm`
- An Anthropic API key (for the AI agents)
- Optional: a GitHub App (for end-to-end repo provisioning + webhooks)

### Three commands

```bash
# 1. One-shot bring-up: k3d cluster, OpenChoreo, Thunder, observability, platform infra
bash deployments/scripts/setup.sh

# 2. Set your Anthropic key (and optional GITHUB_APP_* values)
$EDITOR deployments/.env

# 3. Start the Docker Compose stack (BFF, agents, git-service, console, db, smee)
bash deployments/scripts/start.sh
```

Then open **http://localhost:8090** and sign in with `admin` / `admin`
(the default Thunder admin in the `Administrators` group).

### Useful scripts (all under `deployments/scripts/`)

| Script | What it does |
|---|---|
| `setup.sh` | One-shot bring-up. Chains the steps below. |
| `setup-k3d.sh` | Creates the k3d cluster and patches CoreDNS. |
| `setup-prerequisites.sh` | cert-manager, ESO, kgateway, OpenBao, API Platform gateway. |
| `setup-openchoreo.sh` | Installs OpenChoreo (Control / Data / Workflow planes) + Thunder. |
| `setup-asdlc.sh` | ClusterWorkflows, ClusterComponentTypes, AuthzRoleBindings, generates `.env` (platform services). |
| `setup-observability.sh` | Observability stack. |
| `setup-thunder-client.sh` | Bootstraps Thunder OAuth clients (idempotent). |
| `start.sh` | Refreshes DNS, seeds the host kubeconfig, runs `docker compose up`. |
| `stop.sh` | `docker compose down`. **Cluster stays up.** |
| `teardown.sh` | Destroys the cluster (loses all OpenChoreo state). |
| `seed-dev.sh` | Seeds dev data into the BFF. |
| `seed-test-users.sh` | Idempotently seeds Thunder test users. |
| `verify-api-platform.sh` | Runs the API Platform + Thunder-JWT PoC truth table. |

For deeper details on the architecture, wiring, env vars, and the API Platform PoC,
see [`deployments/README.md`](./deployments/README.md).

### Service ports (when the stack is up)

| Service | Port | URL |
|---|---|---|
| Console (frontend) | 8090 | http://localhost:8090 |
| BFF (`asdlc-api`) | 9090 | http://localhost:9090 |
| `git-service` | 3300 | http://localhost:3300 |
| `agents-service` | 3400 | http://localhost:3400 |
| PostgreSQL | 5433 | `localhost:5433` |
| OpenBao (Vault fork) | 8200 | http://localhost:8200 |
| Thunder IDP | 8080 | http://thunder.openchoreo.localhost:8080 |

### Daily cycle

```bash
# Rebuild & restart a single service after editing it
cd deployments && docker compose up -d --build asdlc-api

# Tail logs
docker compose logs -f asdlc-api

# Stop everything (cluster stays up)
bash deployments/scripts/stop.sh

# Full teardown
bash deployments/scripts/teardown.sh
```

## Architecture (one-pager)

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
│ OpenChoreo (Control / Data / Workflow planes)                 │
│ Thunder IDP   OpenBao   ESO   kgateway                        │
│                                                               │
│ ClusterWorkflow: app-factory-coding-agent  ← BFF dispatches   │
│ ClusterWorkflow: dockerfile-builder        ← BFF dispatches   │
└───────────────────────────────────────────────────────────────┘
```

Deeper docs live in [`docs/design/`](./docs/design) — start with
[`architecture.md`](./docs/design/architecture.md).

## Testing

Tests run against the **real** local cluster — no mocks.

```bash
bash deployments/scripts/setup.sh && bash deployments/scripts/start.sh
cd tests
npm test                   # all
npx playwright test        # E2E only (headed: --headed)
npx vitest run api/        # API integration only
```

See [`tests/README.md`](./tests/README.md) for details.

## Tech stack

- **Frontend** — React 19, TypeScript, Vite, Oxygen UI (MUI), React Query, React Router
- **BFF** — Go 1.25, net/http, GORM, PostgreSQL
- **Agents** — TypeScript, Vercel AI SDK v6 (`ai` + `@ai-sdk/anthropic`), Zod v4
- **Coding-agent runner** — TypeScript, Claude Agent SDK (one-shot pod, key from OpenBao)
- **Agent protocol** — GitHub issue + branch + PR, driven by webhooks
- **Infra** — k3d (cluster) + Docker Compose (long-lived services)

## License

Apache 2.0 — see [`LICENSE`](./LICENSE).

Again: this is a **proof of concept**. Don't ship it.
