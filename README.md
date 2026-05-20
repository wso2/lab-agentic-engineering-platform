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

Today, the only way to try this is **locally** — see [Running it locally](#running-it-locally)
below. A hosted version on **WSO2 Cloud** is planned; this section will get the URL
once it lands.

Feedback (bug reports, "this is confusing," "this is broken") is welcome via GitHub
issues on the repo.

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
| `database-service/`, `collab-server/`, `local-dispatcher/` | Legacy components, kept for reference |

> **Naming.** Service names and code identifiers still use `asdlc-*` (e.g.
> `asdlc-api`, `asdlc-console`) — that's the original internal codename. The
> product itself is now **WSO2 Labs Agentic Engineer**.

This `README.md` is the on-ramp. `AGENTS.md` (symlinked as `CLAUDE.md`) is the
day-to-day contributor manual.

## Running it locally

Everything starts from [`deployments/`](./deployments) — that's the local
setup. It brings up a k3d cluster (OpenChoreo + Thunder + OpenBao + ESO + kgateway)
and a Docker Compose stack for the long-lived services.

### Prerequisites

- Docker + `docker compose` (Colima works too on macOS — `setup-k3d.sh` auto-adjusts for it)
- [`k3d`](https://k3d.io/), `kubectl`, `helm`
- An Anthropic API key (for the AI agents)
- Optional: a GitHub App (for end-to-end repo provisioning + webhooks)

### Bring-up

```bash
# 1. One-shot bring-up: k3d cluster + OpenChoreo + Thunder + platform infra
bash deployments/scripts/setup.sh

# 2. Set your Anthropic key (and optional GITHUB_APP_* values)
$EDITOR deployments/.env

# 3. Start the Docker Compose stack (BFF, agents, git-service, console, db, smee)
bash deployments/scripts/start.sh
```

Open **http://localhost:8090** and sign in with `admin` / `admin` (the default
Thunder admin in the `Administrators` group).

### Main scripts (under `deployments/scripts/`)

| Script | What it does |
|---|---|
| `setup.sh` | One-shot bring-up — k3d cluster + OpenChoreo + Thunder + platform infra. |
| `start.sh` | Starts the Docker Compose stack (BFF, agents, git-service, console, db, smee). |
| `stop.sh` | `docker compose down`. **Cluster stays up.** |
| `teardown.sh` | Destroys the cluster (loses all OpenChoreo state). |

`setup.sh` chains several `setup-*.sh` step scripts (k3d, prerequisites, OpenChoreo,
ASDLC infra, observability, Thunder clients). You normally don't run these directly.

For architecture wiring, env vars, and the API Platform PoC details, see
[`deployments/README.md`](./deployments/README.md).

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
# Rebuild & restart one service after editing it
cd deployments && docker compose up -d --build asdlc-api

# Tail logs
docker compose logs -f asdlc-api
```

Use `stop.sh` / `teardown.sh` from the scripts table above to shut down.

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