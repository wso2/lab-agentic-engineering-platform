# ASDLC Platform

Spec-driven AI-enhanced software development lifecycle platform built on OpenChoreo.

## Project Structure

```
asdlc/
├── console/            → React frontend (Vite + Oxygen UI)
├── asdlc-service/      → Go BFF + GitHub webhook receiver
├── git-service/        → Go microservice for git ops (clone, commit, push, tag)
├── agents/             → TS agents service (Vercel AI SDK; BA, Architect, TaskGen, Wireframe)
├── remote-worker/      → TS one-shot runner image for `app-factory-coding-agent` ClusterWorkflow
├── ui-components/      → pnpm workspace: explorer, md-editor, excalidraw-editor + dsl
├── deployments/        → CANONICAL local setup — k3d cluster + Docker Compose stack
├── tests/              → E2E (Playwright) + API integration (vitest)
├── docs/design/        → Architecture and component design docs
└── requirements/       → User scenario specifications
```

## Project Documentation

### Design Docs — `docs/design/`
- `architecture.md` — Overall system architecture, service diagram, data ownership
- `console.md` — Frontend (React + Oxygen UI)
- `api-service.md` — BFF (Go, PostgreSQL, OC proxy)
- `agent-orchestrator.md` — agents-service (Vercel AI SDK)
- `git-integration.md` — Git provider integration
- `openchoreo-client.md` — OpenChoreo client layer
- `oauth-protected-webapp.md` — OIDC-SPA webapp pattern (Thunder + API Platform gateway)
- `testing.md` — Testing strategy

Keep design docs current. They reflect the high-level architecture, not specific tasks.

### Requirements — `requirements/`
- `1-project-management.md` … `6-manage-observe.md` — User scenarios for each lifecycle stage.

Every feature must have a corresponding user scenario. Scenarios double as the basis for E2E tests.

## Local Setup (`deployments/`)

The `deployments/` directory is the canonical local setup — a k3d cluster (OpenChoreo + Thunder + OpenBao + ESO + kgateway) plus a Docker Compose stack for the long-lived ASDLC services. The coding-agent runs as one-shot pods on the WorkflowPlane.

### Bring-up

```bash
bash deployments/scripts/setup.sh    # one-shot: k3d cluster + OC + Thunder + observability + ASDLC infra
$EDITOR deployments/.env             # set ANTHROPIC_API_KEY + optional GITHUB_APP_*
bash deployments/scripts/start.sh    # start the Docker Compose stack (BFF, agents, git-service, console)
```

**Console:** http://localhost:8090 · **Login:** `admin` / `admin` (default Thunder admin in the `Administrators` group; see `deployments/single-cluster/values-thunder.yaml`).

### Services

| Service | Tech | Where | Port |
|---|---|---|---|
| `asdlc-console` | React + Oxygen + nginx | compose | 8090 |
| `asdlc-api` (BFF) | Go, GORM, PostgreSQL | compose | 9090 |
| `asdlc-git-service` | Go, GORM | compose | 3300 |
| `asdlc-agents-service` | TS, Vercel AI SDK | compose | 3400 |
| `asdlc-db` (PostgreSQL 16) | — | compose | 5433 (host) |
| `asdlc-smee-client` | smee.io → BFF webhook relay | compose | — |
| `thunder` | WSO2 Thunder IDP | k3d | 8080/8090 (`thunder.openchoreo.localhost`) |
| `openbao` | Vault fork | k3d | 8200 (host) |
| `app-factory-coding-agent` | TS Claude Agent SDK | k3d (one-shot pod per task) | n/a |

The coding-agent runner image is `docker.io/xlight05/app-factory-coding-agent-runner:latest` (linux/amd64, `imagePullPolicy: Always`).

### Daily cycle

- **Iterate on a service:** `cd deployments && docker compose up -d --build <name>` (e.g. `asdlc-api`)
- **Tail logs:** `docker compose logs -f <name>`
- **Stop everything:** `bash deployments/scripts/stop.sh`
- **Teardown cluster:** `bash deployments/scripts/teardown.sh`
- **Rebuild + push runner image:** `cd remote-worker && docker buildx build --platform linux/amd64 -t docker.io/xlight05/app-factory-coding-agent-runner:latest --push .`

### Adding env vars

Edit the service's `environment:` block in `deployments/docker-compose.yml` and `docker compose up -d --force-recreate <name>`. For values that also need to be in the k3d cluster (Thunder seeds, OC clients), edit the relevant file under `deployments/single-cluster/` and re-run the setup step.

### GitHub webhooks

The BFF's `/webhooks/github` is reached via a smee.io channel (`GITHUB_WEBHOOK_PROXY_URL` in `.env`) and the `asdlc-smee-client` compose service. Smee occasionally reconnects and drops in-flight events — redeliver from GitHub's "Recent Deliveries" panel when state seems stuck.

## Artifact Storage and Versioning

Specs and designs are stored as files under `.asdlc/` inside each project's cloned git repo (not PostgreSQL):

- `.asdlc/requirements/` — `requirements.md` (required) + optional `functional-requirements.md`, `non-functional-requirements.md`, `user-stories.md`, `wireframes.{dsl,excalidraw}`, `domain-model.{dsl,excalidraw}`.
- `.asdlc/design/` — `design.md` (root) + `components/<name>/design.md` (YAML frontmatter: `type`, `language`, `dependsOn`, `buildpack`, `appPath`, `entrypoint`, optional `api.security`, optional `auth.kind`) + `components/<name>/openapi.yaml` (services only).

The BFF reads/writes via `ArtifactStore`; commits go through `git-service`. `ComponentTask` + `ComponentConfig` live in PostgreSQL.

**Git tag-based versioning:**
- Requirements: `v<N>` (e.g. `v1`).
- Design: `v<N>-<M>` where N is the source requirements version (e.g. `v1-2`). Saving design requires a `v<N>` tag to exist.

New tags are only created if the working tree differs from the latest tag. Versioning helpers live in `git-service/services/artifact_versioning.go`.

## Coding-Agent Flow (component task)

1. BFF creates `ComponentTask` records from the design (one per component) via `/tasks/generate`. Each gets a GitHub issue with full context.
2. BFF idempotently provisions: feature branch, draft PR linking back to the issue (`Closes #N`), and a per-task RS256 JWT.
3. BFF creates a `WorkflowRun` of `ClusterWorkflow: app-factory-coding-agent`. Argo schedules one pod which runs `npx tsx src/oneshot.ts`, provisions a workspace, loads the `asdlc` skill, and runs the Agent SDK.
4. Agent reads the issue (via `gh issue view`), edits code, commits, pushes, and runs `gh pr ready` when done. **The agent does not merge.**
5. Webhooks drive every state transition:
   - `pull_request.ready_for_review` → task `in_progress → ready_for_review`
   - `pull_request.closed merged=true` → task `* → merged` + **dispatches the build** (pinned to merge SHA)
   - `pull_request.closed merged=false` → `* → rejected`
   - `push` to default branch → audit-only (no build, the merge handler already dispatched)
6. Build watcher polls OC `WorkflowRun` status → applies `build.{succeeded,failed}` → task `building → deployed | failed`.
7. **Cascade hook fires when a task lands `deployed`:** posts `## Dependency endpoint resolved` on every dependent task's issue, registers webapp redirect URIs on Thunder (for `auth.kind: oidc-spa`), then re-evaluates `on_hold` siblings.

Task lifecycle: `pending → in_progress → ready_for_review → merged → building → deployed | rejected | failed`.

## Development Workflow

1. **Break down** a milestone into small, independently testable features.
2. **Implement** (frontend + BFF + agents-service as needed).
3. **Debug logs** — BFF/agents-service log at DEBUG by default. `docker compose logs -f <service>`.
4. **Manual verify** with the `/playwright` skill (browser).
5. **Write tests** that simulate the verified scenario.
6. Tests cover the scenarios in `requirements/`.

## Testing

Tests run against the real cluster from `deployments/scripts/setup.sh` — no mocked infra.

```bash
bash deployments/scripts/setup.sh && bash deployments/scripts/start.sh
cd tests && npm test                    # all
cd tests && npx playwright test         # E2E only
cd tests && npx vitest run api/         # API integration only
```

DB resets between suites via `POST /api/v1/_test/reset` (only with `TEST_MODE=true`).

## Tech Stack

- **Frontend** — React 19, TypeScript, Vite, Oxygen UI, React Query, React Router
- **BFF** — Go 1.25, net/http, GORM, PostgreSQL
- **Agents** — TypeScript, Vercel AI SDK v6 (`ai` + `@ai-sdk/anthropic`), Zod v4
- **Coding-agent runner** — TypeScript, Claude Agent SDK (one-shot pod, `ANTHROPIC_API_KEY` from OpenBao)
- **Agent protocol** — GitHub (issue + branch + PR) driven by webhooks
- **Testing** — Playwright (E2E), vitest (API)
- **Infra** — k3d (cluster) + Docker Compose (long-lived services), via `deployments/`

## Practices

- **Proper fixes only** — stick to patterns from agent-manager + integration platform. No hacks.
- **Fix bugs you find** even if unrelated to the current task.
- **Platform-touching changes** (OC primitives, OC client, deployments, secrets, ingress, auth) MUST go through `platform-design-expert` (see Design Review above).
- **Cluster-health pre-flight** — before any operation that talks to the local k3d cluster, delegate the check from `docs/operations/cluster-health.md` to an isolated subagent (`general-purpose`) so the kubectl noise doesn't pollute the parent. Only proceed when the subagent reports healthy. Sleep/wake cycles and OC's mutating webhook 502s show up as confusing `INTERNAL_ERROR`s without this check.
