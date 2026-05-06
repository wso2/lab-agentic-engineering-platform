# ASDLC Platform

Spec-driven AI-enhanced software development lifecycle platform built on OpenChoreo.

## Project Structure

```
asdlc/
├── console/                → React frontend (Vite + Oxygen UI)
├── asdlc-service/          → Go backend API (BFF) + GitHub webhook receiver
├── git-service/            → Go microservice for git operations (clone, commit, push, tag)
├── agents/                 → TypeScript agents service (Vercel AI SDK; BA, Architect, TaskGen, Wireframe)
├── remote-worker/          → TS one-shot runner image for the `app-factory-coding-agent` ClusterWorkflow (Argo per-task pod)
├── deployments-v2/         → CANONICAL local setup — k3d + WSO2 Cloud (OpenChoreo + Thunder + OpenBao + ESO + kgateway)
│   ├── README.md           → Quick-start, env reference, troubleshooting
│   ├── .env.example        → Template; setup.sh prompts for missing values
│   ├── manifests/
│   │   └── env-overlays/   → Per-component env + file mounts (5 files); postgres comes from the submodule kustomize
│   ├── scripts/
│   │   ├── setup.sh        → One-shot bring-up (cluster + platform + asdlc)
│   │   ├── dev-cycle.sh    → Content-hashed rebuild + image patch per component
│   │   ├── teardown.sh     → Removes asdlc; --all destroys cluster
│   │   ├── logs.sh         → Stern-prefixed log tailer (per-component or all)
│   │   ├── components.sh   → Component registry (single source of truth)
│   │   └── lib/            → env / submodule / cluster / platform / asdlc / images / workload / ui
│   └── wso2cloud-deployment/  → Git submodule: wso2-enterprise/wso2cloud-deployment @ local-app-factory
├── deployments/            → DEPRECATED — Docker Compose + k3d setup. Being removed; do not extend.
├── tests/                  → E2E (Playwright) + API integration tests
├── docs/design/            → Architecture and component design docs
├── requirements/           → User scenario specifications
└── CLAUDE.md
```

## Project Documentation

### Design Docs — `docs/design/`
- `architecture.md` — Overall system architecture, service diagram, data ownership
- `console.md` — Frontend component design (React + Oxygen UI)
- `api-service.md` — Go backend API service design (BFF, PostgreSQL, OC proxy)
- `agent-orchestrator.md` — TypeScript AI agents-service (Vercel AI SDK; BA, Architect, TaskGen, Wireframe)
- `git-integration.md` — Git provider integration design
- `openchoreo-client.md` — OpenChoreo client layer design
- `testing.md` — Testing strategy (Playwright E2E + API integration; runs against the cluster brought up by `deployments-v2/scripts/setup.sh`)

**Keep these docs updated** as the implementation evolves. They should always reflect the current high-level architecture, not be overfitted to specific tasks.

### Requirements — `requirements/`
- `1-project-management.md` — Project CRUD scenarios
- `2-specification.md` — Spec creation and management scenarios
- `3-design.md` — AI design generation scenarios
- `4-implementation.md` — AI implementation scenarios
- `5-build-deploy.md` — Build and deploy scenarios
- `6-manage-observe.md` — Runtime management scenarios

**Keep requirements updated** with every feature change. Each feature we develop must have a corresponding user scenario. These scenarios double as the basis for end-to-end tests.

## Development Workflow

### Services

All services run as OpenChoreo `Workload` CRs in the local k3d cluster (no
`docker compose`). Ports below are in-cluster service ports — the only
URL the user hits in the browser is the console HTTPRoute, discovered at
the end of `setup.sh` and printed in the login banner.

| Service | Tech | Port | Purpose |
|---------|------|------|---------|
| `app-factory-console` | React, Oxygen UI, nginx | 8080 | Frontend SPA (Thunder auth via PKCE) |
| `app-factory-api` | Go, GORM, PostgreSQL | 8080 | BFF — CRUD, OC proxy, GitHub webhook receiver, build trigger |
| `app-factory-git-service` | Go, GORM, PostgreSQL | 3300 | Git operations — clone, commit, push, tag; resolves per-org credentials from OpenBao |
| `app-factory-agents-service` | TypeScript, Vercel AI SDK | 3400 | AI agents: BusinessAnalyst (spec), Architect (design), TaskGenerator, Wireframe |
| `app-factory-coding-agent-runner` | TypeScript, Claude Agent SDK | n/a | One-shot runner image referenced by ClusterWorkflow `app-factory-coding-agent`. Argo creates one ephemeral pod per ComponentTask; pod exits when the agent finishes. Authed via `ANTHROPIC_API_KEY` (per-WorkflowRun ExternalSecret from OpenBao). |
| `app-factory-postgresql` | PostgreSQL 16 | 5432 | Component tasks, git repository records (`wso2cloud` namespace) |
| `thunder` | WSO2 Thunder IDP | 8080/8090 | Identity provider; user auth (PKCE) + service-to-service `client_credentials`. Browser URL: `http://thunder.openchoreo.localhost:8080` |
| `openbao` | HashiCorp Vault fork | 8200 | Secret store. Holds `ANTHROPIC_API_KEY`, `GITHUB_PLATFORM_PAT`, GitHub webhook secret, BFF task-signing PEM. |

Smee-client and collab-server are **deferred** — webhook delivery requires a
public URL or manual smee tunnel; collaborative editing in the console is
non-functional until collab-server lands.

### Running Locally

```bash
# First-time setup (~10–15 min — k3d cluster + WSO2 Cloud platform + asdlc workloads)
# Idempotent: re-run after any failure, it picks up where it left off.
bash deployments-v2/scripts/setup.sh

# Iterate after source changes — content-hashes each component, rebuilds + patches what changed
bash deployments-v2/scripts/dev-cycle.sh                 # all components
bash deployments-v2/scripts/dev-cycle.sh app-factory-api # one component

# Stream logs (requires `stern` for prettier output; falls back to kubectl logs)
bash deployments-v2/scripts/logs.sh                       # all components, follow
bash deployments-v2/scripts/logs.sh api --tail 50         # last 50 lines of BFF
bash deployments-v2/scripts/logs.sh agents --since 10m    # agents-service, last 10 min

# Tear down asdlc (cluster stays, can re-run setup.sh quickly)
bash deployments-v2/scripts/teardown.sh

# Destroy everything (cluster + all platform state)
bash deployments-v2/scripts/teardown.sh --all
```

**Login (printed in the setup banner):** `admin@openchoreo.dev` / `Admin@123`.
Console URL is auto-discovered (the `app-factory-console` HTTPRoute hostname,
e.g. `http://http-app-factory-c-development-…openchoreoapis.localhost:19080`).

`setup.sh` is the one-shot path: bootstrap env → ensure submodule on
`local-app-factory` branch → create k3d cluster → apply WSO2 Cloud platform
in layered kustomize order → seed OpenBao → build + import + apply 5 asdlc
Workloads → register the discovered console URL in Thunder. The cluster
persists across reboots; only `setup.sh` brings up the platform — every
day-to-day cycle is just `dev-cycle.sh`.

**Required env values** (`deployments-v2/.env`, prompted on first run):
`ANTHROPIC_API_KEY`. **Optional:** `GITHUB_PLATFORM_PAT` + `GITHUB_REPO_OWNER`
auto-seed the dev-tier "default" org's credentials so users can skip the
"Settings → GitHub Integration" step. Auto-generated on first run:
`GITHUB_WEBHOOK_SECRET`, `OAUTH_STATE_SIGNING_KEY`, `GITHUB_WEBHOOK_PROXY_URL`
(smee.io channel), and `keys/task-signing.pem` (RSA). See
`deployments-v2/.env.example` for the full template.

**Adding env vars to a service**: edit
`deployments-v2/manifests/env-overlays/<component>.yaml` (per-component
key/value list under `env:` plus optional `files:` for mounted secrets). Any
`${VAR}` placeholder is `envsubst`-substituted from the loaded `.env` at apply
time. Run `dev-cycle.sh <component>` to roll the change.

**Adding/changing platform-level config** (CoreDNS, gateway policies, Thunder
OAuth apps, ReleaseBindings, project structure): land it in the
`deployments-v2/wso2cloud-deployment` submodule on the `local-app-factory`
branch — that repo is the source of truth for everything kustomized in
`init/layer-{0,1,2,3}` and `domains/{platform,developers}`. Avoid imperative
`kubectl patch` in the deployments-v2 scripts; if you find yourself wanting
one, the right home is upstream.

**Optional GitHub App env values** (only needed if you want App-mode connect to work — PAT mode connect is fully self-contained from the console UI): `GITHUB_APP_ID`, `GITHUB_CLIENT_ID`, `GITHUB_CLIENT_SECRET`, `GITHUB_APP_SLUG`, `GITHUB_APP_PRIVATE_KEY_PATH`. See `docs/operations/github-app.md` for the App registration runbook.

**Per-org GitHub connection**: each OC org connects via the console (Settings → GitHub Integration) using either GitHub App (preferred) or a Personal Access Token. There is no platform-wide PAT — git-service holds per-org credentials in OpenBao and routes every operation through the resolver. If `GITHUB_PLATFORM_PAT` + `GITHUB_REPO_OWNER` are set in `.env`, the "default" org is seeded automatically on first boot; otherwise navigate to `/organizations/{org}/settings/github` after login.

**PAT scopes** (when connecting via PAT):
- Classic PAT: `repo` + `admin:org` + `admin:repo_hook`
- Fine-grained PAT: `Administration: Write` + `Contents: Write` + `Issues: Write` + `Pull requests: Write` + `Webhooks: Write`

See `docs/design/github-integration-evolution.md` for the full credential trust model and `docs/design/github-integration-phase2.md` for the implementation design.

**Start order**: `setup.sh` is one-shot — it brings up the cluster, the platform layers in dependency order (Flux → Gateway-API → ESO/cert-manager/OpenBao → Thunder/CP/WP → asdlc workloads), then registers the discovered console URL with Thunder. There is no separate `start.sh`; daily iteration is just `dev-cycle.sh`.

**Public URLs**: `PUBLIC_THUNDER_URL` (default `http://thunder.openchoreo.localhost:8080`) is in `deployments-v2/.env`. The console URL is auto-discovered from the OC HTTPRoute and registered with Thunder dynamically — there is no `PUBLIC_CONSOLE_URL` to set. To share over ngrok or another public URL, override `PUBLIC_THUNDER_URL` and re-run `setup.sh` (idempotent — it re-applies platform kustomize and re-registers Thunder CORS/redirect entries).

In-cluster service DNS (k3d, namespace `default` for asdlc workloads, `wso2cloud` for postgres):
- Console (browser, discovered): `http://http-app-factory-c-development-<hash>.openchoreoapis.localhost:19080`
- Thunder (browser): `http://thunder.openchoreo.localhost:8080`
- Login: `admin@openchoreo.dev` / `Admin@123`
- BFF inside cluster: `http://app-factory-api:8080`
- Agents service: `http://app-factory-agents-service:3400`
- Git service: `http://app-factory-git-service:3300`
- Coding-agent: no Service — Argo creates one pod per WorkflowRun in the WorkflowPlane namespace
- Postgres: `app-factory-postgresql.wso2cloud:5432`
- GitHub webhook: BFF `/webhooks/github` (HMAC-authed; delivered via smee.io — channel auto-provisioned at first setup)

#### Agents service (`agents/`)

The TypeScript AI service backing every BFF AI flow. Built on the **Vercel AI SDK v6** (`ai` + `@ai-sdk/anthropic`) and runs in Docker on port **3400**. Authenticates with `ANTHROPIC_API_KEY` (standard API key — no Claude CLI / keychain dependency). Exposes one route per agent: business-analyst (SSE streaming spec generation), architect (SSE streaming design), task-generator (JSON), wireframe (JSON).

Structure:
- `src/shared/` — `createAgent` factory wrapping `streamText`, shared config and types
- `src/tools/` — shared tools (`readFile`, `listDirectory`) auto-attached to every agent
- `src/skills/` — composable skills bundling tools + prompt instructions (e.g. `codebase-exploration`)
- `src/agents/{agent-name}/` — each agent has its own directory with `schema.ts` (Zod I/O), `prompt.ts` (system + user prompt builder), `index.ts` (wires it via the factory)
- `src/server/` — Express app + per-agent HTTP routes

Requires `ANTHROPIC_API_KEY`. Run `npm install && npx tsc --noEmit` in `agents/` to verify.

#### Coding-agent runner deployment

The `remote-worker/` directory is a **one-shot runner image** referenced by the OpenChoreo `ClusterWorkflow: app-factory-coding-agent` (defined in `deployments-v2/wso2cloud-deployment/.../cluster-workflows/app-factory-coding-agent.yaml`). It is **not** a long-lived Workload. On each task dispatch the BFF creates a `WorkflowRun`; Argo schedules one ephemeral pod on the WorkflowPlane that runs `npx tsx src/oneshot.ts`, calls `provisionWorkspace()` and `runClaudeQuery()`, and exits. Workspace lives inside the pod's `emptyDir` (`/home/asdlc/asdlc-workspace/<orgId>/<projectId>/<taskId>/`) and is discarded with the pod. `ANTHROPIC_API_KEY` flows in via a per-WorkflowRun ExternalSecret backed by the same OpenBao path the agents-service uses (`secret/apps/anthropic`).

For local k3d the runner image is built and imported by `dev-cycle.sh` under the static tag `asdlc.local/app-factory-coding-agent-runner:local`; the ClusterWorkflow pins this name with `imagePullPolicy: Never` so each new pod uses the freshly imported image. Add new runner images to `deployments-v2/scripts/components.sh`'s `RUNNER_IMAGES` array.

#### Implementation execution flow (Phase 0 — GitHub-native)

When the user clicks "Start Implementation" in the console:

1. BFF creates `ComponentTask` records from the design (one per component) via `/tasks/generate`. Each task gets a GitHub issue with full context.
2. Per task, BFF idempotently provisions: feature branch `task/<slug>-<short-id>` off the default branch, a draft PR linking back to the issue (`Closes #N`), and a per-task bearer (HS256 JWT, 24h TTL) for the agent's credential helper.
3. BFF creates a `WorkflowRun` of `ClusterWorkflow: app-factory-coding-agent` via the OC REST API (`/api/v1/namespaces/<ns>/workflowruns`). Labels include `app-factory.openchoreo.dev/coding-agent-task: <taskId>`. The Local flow skips dispatch — the user runs Claude Code locally; if they install the ASDLC plugin (`remote-worker/plugin/`) they get the same workflow skill the cluster pod loads.
4. Argo schedules an ephemeral pod on the WorkflowPlane. The pod's entrypoint is `npx tsx src/oneshot.ts`, which reads ASDLC_* env vars (substituted from `{{workflow.parameters.*}}`), provisions the workspace under an `emptyDir` mount (`/home/asdlc/asdlc-workspace/<orgId>/<projectId>/<taskId>/`), clones the feature branch, configures `.git/config`, writes `gh` config + bearer file, and starts an Agent SDK `query()` with `cwd = workspace`, the ASDLC plugin loaded (so the `asdlc` skill is available), and no tokens in env.
5. Agent reads the issue (via `gh issue view`), edits code, runs `git commit` / `git push origin HEAD`, posts `gh issue comment` for progress, and runs `gh pr ready <prNumber>` when done. The SDK is credential-blind — `git` and `gh` authenticate via the workspace's credential helper / `gh` wrapper, both of which fetch fresh tokens from `git-service /api/v1/credentials/refresh`. **The agent does not merge.**
6. Webhooks drive every state transition. The BFF's `/webhooks/github` (HMAC-validated, delivery-ID-deduped) processes:
   - `pull_request.ready_for_review` → task `in_progress` → `ready_for_review`
   - `pull_request.closed merged=true` → task `* → merged`, records merge SHA
   - `pull_request.closed merged=false` → task `* → rejected`
   - `push` to default branch → BFF creates an OC `WorkflowRun` with `params.repository.revision.commit` pinned to the merge SHA. Filters components by changed paths.
7. The build watcher (10s sweep) polls OC `WorkflowRun` status and applies `build.{succeeded,failed}` via the projector → task `building → deployed | failed`. A parallel coding-agent watcher (also 10s) polls coding-agent WorkflowRuns and applies `coding_agent.failed` on terminal pod failure → task `in_progress → failed` (success transitions ride the GitHub `pr.ready_for_review` webhook instead).

**Task lifecycle**: `pending → in_progress → ready_for_review → merged → building → deployed | rejected | failed`.

State transitions are declarative (single transition table in `services/task_state.go`); the projector (`services/webhook/projector.go`) is the only writer of `ComponentTask.Status` outside dispatch. Per-task advisory locks (`pg_advisory_xact_lock(hashtext('task:'||id))`) keep concurrent webhook handlers serialised.

#### Artifact storage and versioning

Specs and designs are stored as files in the `.asdlc/` directory within each project's cloned git repo (not in PostgreSQL). The file layout per project is:
- `.asdlc/spec.md` — Specification content
- `.asdlc/design.json` — Architecture design (components, overview, requirements)

The BFF reads/writes these files via `ArtifactStore` and commits/pushes changes via `git-service`. `ComponentTask` and `ComponentConfig` live in PostgreSQL; generated tasks also surface as GitHub issues on the project repo.

**Git tag-based versioning**: Artifact versions are tracked via annotated git tags instead of a status file. Each artifact type has a tag prefix (`spec-v`, `design-v`). When a user clicks "Save & Proceed", the BFF commits the artifact, pushes, then creates an annotated tag (e.g., `spec-v1`, `design-v3`) and pushes it. A new tag is only created if the working copy differs from the content at the latest tag. Status is derived: if any tag exists for the artifact, it is "approved"; otherwise "draft". The versioning logic lives in `asdlc-service/services/versioning.go`.

**Lineage tracking**: Downstream artifacts record which upstream version they were generated from. When a design is tagged, its tag message includes `source-spec: spec-v2`. This enables the UI to show provenance (e.g., "from spec-v2") and detect when upstream artifacts have changed since the downstream was generated.

**Version API endpoints** (per artifact: spec, design):
- `POST .../save` — commit, push, and tag the artifact
- `POST .../discard` — revert working copy to last tagged version
- `GET .../versions` — list all versions
- `GET .../versions/{version}` — retrieve content at a specific version

**Console components**: `VersionSelector` (dropdown to browse/switch versions, shows unsaved-changes warning with discard option) and `LineageLabel` (chip showing upstream artifact versions, e.g., "from spec-v2, design-v1").

#### Colima restart caveat (k3d IP swap)

If the Docker daemon is restarted (Colima stop/start, Docker Desktop restart, machine reboot), the k3d server (`k3d-openchoreo-server-0`) and loadbalancer (`k3d-openchoreo-serverlb`) containers can come back with **swapped IPs** on the `k3d-openchoreo` bridge network. Docker assigns IPs by startup order, and there's nothing pinning each container to a specific IP.

When this happens, k3s crashes on startup with:

```
level=fatal msg="Failed to start networking: unable to initialize network policy controller:
                  error getting node subnet: failed to find interface with specified node ip"
```

The cause: k3s' embedded kube-router reads the node IP from the persisted Node object in kine SQLite (`/var/lib/rancher/k3s/server/db/state.db`), which still has the *original* IP. The interface now has a different IP. Mismatch → fatal → container restart loop.

**Recovery (preserves the cluster and all OpenChoreo state):**

Find what IP the persisted state expects by grepping the early k3s logs for `listener.cattle.io/cn-172.18.` — the IP listed there is the original server IP. Then swap the containers back:

```bash
# Replace 172.18.0.3 below with the original server IP from the logs
docker stop k3d-openchoreo-serverlb k3d-openchoreo-server-0
docker network disconnect k3d-openchoreo k3d-openchoreo-serverlb
docker network disconnect k3d-openchoreo k3d-openchoreo-server-0
docker network connect --ip 172.18.0.3 k3d-openchoreo k3d-openchoreo-server-0
docker network connect --ip 172.18.0.4 k3d-openchoreo k3d-openchoreo-serverlb
docker start k3d-openchoreo-server-0
# Wait ~15s for k3s API server to come up, then:
docker start k3d-openchoreo-serverlb
```

This works because the serverlb's nginx config uses the container DNS name (`k3d-openchoreo-server-0:6443`), not a hardcoded IP, so the loadbalancer transparently follows the new IP.

**Last resort** — if recovery fails or the cluster state is otherwise corrupted, delete and recreate (loses all OpenChoreo deployments):

```bash
bash deployments-v2/scripts/teardown.sh --all
bash deployments-v2/scripts/setup.sh
```

### Milestones

Work is organized into milestones — significant sets of features. Each milestone is broken down into smaller, testable features.

### Feature Development Process

1. **Break down**: Divide the milestone into small, independently testable features
2. **Implement**: Build the feature (frontend + backend + agents-service as needed)
3. **Debug logging**: BFF and agents-service log each request/response at DEBUG level. `LOG_LEVEL=debug` is the default in `deployments-v2/manifests/env-overlays/*.yaml`. Stream logs with `bash deployments-v2/scripts/logs.sh <component>`.
4. **Manual verification**: Use Playwright (via `/playwright` skill) to verify the feature works end-to-end in the browser
5. **Write tests**: Once verified working, write Playwright tests that simulate the exact scenario verified manually
6. **Tests = requirements**: The test suite should ultimately cover all scenarios in `requirements/`

### Testing

Tests run against the real cluster brought up by `deployments-v2/scripts/setup.sh` — no mocked infrastructure.

```bash
bash deployments-v2/scripts/setup.sh    # one-shot bring-up (idempotent)
cd tests && npm test                    # run all tests
cd tests && npx playwright test         # run E2E tests only
cd tests && npx vitest run api/         # run API integration tests only
bash deployments-v2/scripts/teardown.sh # remove asdlc workloads (cluster stays)
```

- **E2E tests** (`tests/e2e/`): Playwright drives browser against full stack
- **API integration tests** (`tests/api/`): Direct HTTP against API service + real DB
- DB is reset between test suites via `POST /api/v1/_test/reset` (only when `TEST_MODE=true`)
- See `docs/design/testing.md` for full strategy

### Tech Stack

- **Frontend**: React 19, TypeScript, Vite, Oxygen UI, React Query, React Router
- **Backend (BFF)**: Go 1.25, net/http, GORM, PostgreSQL
- **Agent Service**: TypeScript, Node.js, Claude Agent SDK
- **Agents Package**: TypeScript, Vercel AI SDK v6 (`ai` + `@ai-sdk/anthropic`), Zod v4
- **Remote Worker**: TypeScript, Claude Agent SDK (containerised on cluster with `ANTHROPIC_API_KEY`)
- **Agent Protocol**: GitHub itself (issue + branch + PR), driven by webhooks. The agent uses `git` and `gh` directly inside a per-task workspace.
- **Testing**: Playwright (E2E), vitest (API integration)
- **Infrastructure**: k3d (local) + OpenChoreo + Thunder + OpenBao + ESO + kgateway, brought up by `deployments-v2/`


## Practices
- **Important**: Always do the proper fix, stick to patterns used by agent manager and integration platform. No hacks.
- **Important**: If you come across a bug, fix it even if its not releted to your current task.

