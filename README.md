# WSO2 Labs: Agentic Engineer

> 🧪 **Early lab project** — APIs, data models, and features are still evolving,
> and subsystems may change shape between commits. Try it, break it, tell us what
> you think — just don't build on it as a stable surface yet.

Repository: [`wso2/labs-agentic-engineer`](https://github.com/wso2/labs-agentic-engineer)

An experimental, open-source platform that explores what **agent-driven software
engineering** looks like when the agents work inside an enterprise platform instead
of a blank editor. It's an early WSO2 lab project, built on top of
[OpenChoreo](https://github.com/openchoreo/openchoreo) and shared in the spirit of
"let's see what works."

## The premise

Agentic coding tools have made greenfield code generation fast and accessible. But
enterprise software isn't bottlenecked on typing — it's bottlenecked on requirements,
integrations, identity, deployment, and architectural conformance. The bet behind
this project is that to push productivity further, agents need to operate **inside a
platform that already understands those concerns**, so they can produce systems that
slot into the existing ecosystem rather than ignore it.

OpenChoreo already handles API management, identity, deployments, observability, and
policy enforcement. The agents in this repo build on that foundation, so what they
produce lands in an environment that enforces enterprise concerns automatically.

## What the platform does

It treats the SDLC as a chain of stages — **Specification → Design → Implementation
→ Build → Deploy → Manage** — and gives each stage a specialized agent with only the
tools and skills it needs. The flow:

- A **business owner** describes the solution they want; a chat agent guides
  requirements elicitation.
- A **shared workspace** lets BAs, designers, and engineers collaborate on the same
  artifacts, each with a view suited to them.
- Everything is captured as **spec files in a Git repository** (`specs/requirements/`,
  `specs/design/`, wireframes, domain models). Those specs become the contract
  downstream agents work against.
- Coding agents pick up tasks from those specs, work via **GitHub issues + branches
  + PRs** (no merge without human review), and the platform watches webhooks to drive
  each task through `pending → in_progress → ready_for_review → merged → building → deployed`.
- Because agents share context across artifacts, the system stays internally
  consistent — change a requirement and the wireframes, design, and tasks move with it.

## Design principles worth calling out

- **Spec-driven, Git-native.** Specs live in the project's Git repo, not a
  proprietary database. Engineers can drop into the code at any stage without
  leaving familiar tools.
- **Human-in-the-loop is explicit.** Agents open PRs; they don't merge. State
  transitions are driven by GitHub events.
- **Skills are the customization surface.** Organizations encode their own
  conventions — naming, architecture patterns, security baselines, the approved
  service catalog — as skills, so standards become something the agents actually
  apply.
- **Model-agnostic.** You can pick the LLM behind each agent and mix providers
  across the lifecycle.

## Demo

https://github.com/user-attachments/assets/9723e5a5-c187-49b5-886e-50c217da6c28

## What's in the repo

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
> now **WSO2 Labs Agentic Engineer**.

`AGENTS.md` (symlinked as `CLAUDE.md`) is the day-to-day operations cheat sheet for
contributors. The root `README.md` you're reading is the on-ramp; `AGENTS.md` is the
detailed manual.

## Trying it out

You can try it out locally — clone the repo and run the scripts under
[`deployments/`](./deployments). The setup brings up a k3d cluster (OpenChoreo +
Thunder + OpenBao + ESO + kgateway) and a Docker Compose stack for the long-lived
services (BFF, agents, git-service, console, database, smee relay). Once it's up,
you sign in to the console at http://localhost:8090 and drive a project end-to-end
through the agents.

A hosted version on **WSO2 Cloud** is planned so people can try it without standing
up the local stack — watch this section for the URL once it lands.

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

## Status and feedback

This is an **early lab project**, and the whole point of putting it out now is to
learn from people working through similar problems: where agent boundaries should
sit, how skills map to your conventions, what felt natural, what got in the way.
Feedback goes via GitHub issues on
[`wso2/labs-agentic-engineer`](https://github.com/wso2/labs-agentic-engineer).

## License

Apache 2.0 — see [`LICENSE`](./LICENSE).

