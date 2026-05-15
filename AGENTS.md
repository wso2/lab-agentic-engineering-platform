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
├── ui-components/          → pnpm workspace packages: `explorer` (markdown explorer), `md-editor`, `excalidraw-editor` + `excalidraw-dsl` (wireframe / domain-model canvas)
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
- `2-requirements.md` — Multi-document requirements creation and management scenarios
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
| `openbao` | HashiCorp Vault fork | 8200 | Secret store. Holds `ANTHROPIC_API_KEY`, per-org GitHub credentials at `secret/asdlc/{ouHandle}/github/pat`, GitHub webhook secret, BFF task-signing PEM. |

**GitHub webhook delivery (local dev)**: the BFF `/webhooks/github` endpoint
has no public ingress. A host-side bridge — `deployments-v2/scripts/webhook-relay.sh`
— runs `kubectl port-forward` + `npx smee-client` together, forwarding the
`smee.io` channel in `.env` (`GITHUB_WEBHOOK_DELIVERY_URL`) to the BFF service.
**Run it in a terminal that stays open**; the script's supervisor only
restarts the two child processes, so a closed shell or long laptop sleep
takes the whole relay down and webhook-driven task transitions
(`pull_request.ready_for_review` / `merged`, `push`) silently stop landing.
The cluster-health pre-flight in `docs/operations/cluster-health.md`
covers relay liveness — see Detect step (4) and Recover step (4).

Collab-server (collaborative editing in the console) is **deferred**; an
in-cluster `smee-client` Workload to replace the host-side relay is also
deferred.

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
`ANTHROPIC_API_KEY`. **Optional (local-dev only):** `LOCAL_DEV_ADMIN_GITHUB_PAT`
+ `LOCAL_DEV_ADMIN_GITHUB_OWNER` are consumed exclusively by
`scripts/lib/seed-admin-github.sh`, which calls the public Connect API to
pre-connect the admin org. The platform binary does not read these vars —
no env var, manifest, or seed names an org. Auto-generated on first run:
`GITHUB_WEBHOOK_SECRET`, `OAUTH_STATE_SIGNING_KEY`, `GITHUB_WEBHOOK_DELIVERY_URL`
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

**Per-org GitHub connection**: each OC org connects via the console (Settings → GitHub Integration) using either GitHub App (preferred) or a Personal Access Token. There is no platform-wide PAT — git-service holds per-org credentials in OpenBao and routes every operation through the resolver. The local-dev admin shortcut (`LOCAL_DEV_ADMIN_GITHUB_*` in `.env`) routes through the same Connect endpoint via `seed-admin-github.sh`; otherwise navigate to `/organizations/{org}/settings/github` after login.

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

The TypeScript AI service backing every BFF AI flow. Built on the **Vercel AI SDK v6** (`ai` + `@ai-sdk/anthropic`) and runs in Docker on port **3400**. Authenticates with `ANTHROPIC_API_KEY` (standard API key — no Claude CLI / keychain dependency). Exposes one route per agent: business-analyst (SSE streaming spec generation), architect (SSE streaming design), task-generator (JSON), wireframe (JSON; the resulting Excalidraw scene renders in the `excalidraw-editor` UI component).

Structure:
- `src/shared/` — `createAgent` factory wrapping `streamText`, shared config and types
- `src/tools/` — shared tools (`readFile`, `listDirectory`) auto-attached to every agent
- `src/skills/` — composable skills bundling tools + prompt instructions (e.g. `codebase-exploration`)
- `src/agents/{agent-name}/` — each agent has its own directory with `schema.ts` (Zod I/O), `prompt.ts` (system + user prompt builder), `index.ts` (wires it via the factory)
- `src/server/` — Express app + per-agent HTTP routes

Requires `ANTHROPIC_API_KEY`. Run `npm install && npx tsc --noEmit` in `agents/` to verify.

#### Coding-agent runner deployment

The `remote-worker/` directory is a **one-shot runner image** referenced by the OpenChoreo `ClusterWorkflow: app-factory-coding-agent` (defined in `deployments-v2/wso2cloud-deployment/.../cluster-workflows/app-factory-coding-agent.yaml`). It is **not** a long-lived Workload. On each task dispatch the BFF creates a `WorkflowRun`; Argo schedules one ephemeral pod on the WorkflowPlane that runs `npx tsx src/oneshot.ts`, calls `provisionWorkspace()` and `runClaudeQuery()`, and exits. Workspace lives inside the pod's `emptyDir` (`/home/asdlc/asdlc-workspace/<orgId>/<projectId>/<taskId>/`) and is discarded with the pod. `ANTHROPIC_API_KEY` flows in via a per-WorkflowRun ExternalSecret backed by the same OpenBao path the agents-service uses (`secret/apps/anthropic`).

The runner image is published to Docker Hub at `docker.io/xlight05/app-factory-coding-agent-runner:latest` (built for `linux/amd64` via `docker buildx --push`). The ClusterWorkflow uses `imagePullPolicy: Always`, so every pod start pulls fresh — iterate by rebuilding + pushing `:latest`, no YAML edits required. There is no `k3d image import` path for this image anymore; local k3d also pulls from Docker Hub. When stabilising a long-lived dev/stage, pin to an immutable SHA tag and switch to `imagePullPolicy: IfNotPresent`. New runner images (other than the coding agent) can still be added to `deployments-v2/scripts/components.sh`'s `RUNNER_IMAGES` array.

#### Implementation execution flow (Phase 0 — GitHub-native)

When the user clicks "Start Implementation" in the console:

1. BFF creates `ComponentTask` records from the design (one per component) via `/tasks/generate`. Each task gets a GitHub issue with full context.
2. Per task, BFF idempotently provisions: feature branch `task/<slug>-<short-id>` off the default branch, a draft PR linking back to the issue (`Closes #N`), and a per-task bearer (HS256 JWT, 24h TTL) for the agent's credential helper.
3. BFF creates a `WorkflowRun` of `ClusterWorkflow: app-factory-coding-agent` via the OC REST API (`/api/v1/namespaces/<ns>/workflowruns`). Labels include `app-factory.openchoreo.dev/coding-agent-task: <taskId>`. The Local flow skips dispatch — the user runs Claude Code locally; if they install the ASDLC plugin (`remote-worker/plugin/`) they get the same workflow skill the cluster pod loads.
4. Argo schedules an ephemeral pod on the WorkflowPlane. The pod's entrypoint is `npx tsx src/oneshot.ts`, which reads ASDLC_* env vars (substituted from `{{workflow.parameters.*}}`), provisions the workspace under an `emptyDir` mount (`/home/asdlc/asdlc-workspace/<orgId>/<projectId>/<taskId>/`), clones the feature branch, configures `.git/config`, writes `gh` config + bearer file, and starts an Agent SDK `query()` with `cwd = workspace`, the ASDLC plugin loaded (so the `asdlc` skill is available), and no tokens in env.
5. Agent reads the issue (via `gh issue view`), edits code, runs `git commit` / `git push origin HEAD`, posts `gh issue comment` for progress, and runs `gh pr ready <prNumber>` when done. The SDK is credential-blind — `git` and `gh` authenticate via the workspace's credential helper / `gh` wrapper, both of which fetch fresh tokens from `git-service /api/v1/credentials/refresh`. **The agent does not merge.**
6. Webhooks drive every state transition. The BFF's `/webhooks/github` (HMAC-validated, delivery-ID-deduped) processes:
   - `pull_request.ready_for_review` → task `in_progress` → `ready_for_review`
   - `pull_request.closed merged=true` → task `* → merged`, records merge SHA, **and dispatches the build directly** (creates an OC `WorkflowRun` with `params.repository.revision.commit` pinned to the merge SHA). This is the only entry point that dispatches builds — relying on the platform invariant that `main` only moves via merged PRs (branch protection + the `asdlc` agent skill both forbid direct pushes to `main`).
   - `pull_request.closed merged=false` → task `* → rejected`
   - `push` to default branch → audit-only log line. Does NOT dispatch builds; the `pr.closed merged=true` handler already did.

   There is **no polling fallback** — if the relay is down (see "GitHub
   webhook delivery (local dev)" above), the projector never fires and
   the task is stuck until the missed event is redelivered from the
   smee.io channel or the GitHub Recent Deliveries panel.
7. The build watcher (10s sweep) polls OC `WorkflowRun` status and applies `build.{succeeded,failed}` via the projector → task `building → deployed | failed`. A parallel coding-agent watcher (also 10s) polls coding-agent WorkflowRuns and applies `coding_agent.failed` on terminal pod failure → task `in_progress → failed` (success transitions ride the GitHub `pr.ready_for_review` webhook instead).

**Task lifecycle**: `pending → in_progress → ready_for_review → merged → building → deployed | rejected | failed`.

State transitions are declarative (single transition table in `services/task_state.go`); the projector (`services/webhook/projector.go`) is the only writer of `ComponentTask.Status` outside dispatch. Per-task advisory locks (`pg_advisory_xact_lock(hashtext('task:'||id))`) keep concurrent webhook handlers serialised.

#### Artifact storage and versioning

Specs and designs are stored as files in the `.asdlc/` directory within each project's cloned git repo (not in PostgreSQL). The layout per project is:
- `.asdlc/requirements/` — multi-document requirements directory
  - `requirements.md` — main requirements doc (always present, cannot be deleted)
  - `functional-requirements.md`, `non-functional-requirements.md`, `user-stories.md` — optional sibling docs derived from `requirements.md` via document-generation skills
  - `wireframes.dsl` + `wireframes.excalidraw`, `domain-model.dsl` + `domain-model.excalidraw` — optional canvases. The `.dsl` is the agent-readable source; the `.excalidraw` is the rendered scene the user views.
- `.asdlc/design/` — multi-file architecture tree
  - `design.md` — system-level overview (root)
  - `components/<name>/design.md` — per-component design (YAML frontmatter — `type`, `language`, `dependsOn`, `buildpack`, `appPath`, `entrypoint` — plus a Markdown body)
  - `components/<name>/openapi.yaml` — OpenAPI 3.0.3 contract, present for `service` components only (web-apps have no OpenAPI)

The BFF reads/writes these files via `ArtifactStore` and commits/pushes changes via `git-service`. `ComponentTask` and `ComponentConfig` live in PostgreSQL; generated tasks also surface as GitHub issues on the project repo.

**Git tag-based versioning**: Artifact versions are tracked via annotated git tags. The scheme is:
- Requirements: `v<N>` (`v1`, `v2`, …). One save bumps to the next version and tags the whole `.asdlc/requirements/` directory snapshot.
- Design: `v<N>-<M>` where N is the source requirements version and M is the design revision under that N (`v1-1`, `v1-2`, `v2-1`). Saving design requires a `v<N>` tag to exist (otherwise 409). The lineage of a design version is encoded in its tag name itself — no `source-*:` annotation lines.

A new tag is only created if the working tree differs from the content at the latest tag. Status is derived: if any tag exists for the artifact, it is "approved"; otherwise "draft". Versioning helpers live in `git-service/services/artifact_versioning.go`.

**Document-generation skills**: Each non-main requirement type (functional / NFR / user stories) has an associated agents-service skill that generates its content from sibling files. Skills are isolated under `agents/src/skills/document-generation/<id>.ts` so the prompts are easy to tweak. The doc-type registry on the console (`console/src/lib/documentTypes.ts`) maps each type to a `generationSkillId` + `generationSourceFiles`; the BFF route `POST /requirements/files/{name}/generate` resolves the skill, fetches sources from the working tree, posts to agents-service, and writes the streamed result back.

**Version API endpoints**:
- Requirements: `GET/PUT/DELETE /requirements/files/{name}`, `POST /requirements/save|discard`, `GET /requirements/versions`, `GET /requirements/versions/{tag}` (e.g. `tag=v2`).
- Design: `GET/PUT /design`, `POST /design/save|discard`, `GET /design/versions`, `GET /design/versions/{tag}` (e.g. `tag=v1-2`).

**Console components**: `VersionSelector` (dropdown to browse/switch versions, shows unsaved-changes warning with discard option) and `LineageLabel` (chip showing upstream artifact versions, e.g., "Based on requirements v2" — decoded from the design tag name).

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
- **Coding-Agent Runner**: TypeScript, Claude Agent SDK (one-shot pod per ClusterWorkflow run, authed with `ANTHROPIC_API_KEY` from OpenBao via per-WorkflowRun ExternalSecret)
- **Agent Protocol**: GitHub itself (issue + branch + PR), driven by webhooks. The agent uses `git` and `gh` directly inside a per-task workspace.
- **Testing**: Playwright (E2E), vitest (API integration)
- **Infrastructure**: k3d (local) + OpenChoreo + Thunder + OpenBao + ESO + kgateway, brought up by `deployments-v2/`


## Design Review (OpenChoreo + WSO2 Cloud compliance)

This repo ships one specialised reviewer subagent under `.claude/agents/`. It is a pure subject-matter expert — not aware of App Factory's specifics — and you query it with the relevant design context:

- **`platform-design-expert`** — answers questions and reviews designs from the combined OpenChoreo + WSO2 Cloud platform perspective. WSO2 Cloud is the deployment platform built on OpenChoreo, so every answer reconciles BOTH surfaces: OC primitive correctness AND WSO2 Cloud hostability (GUIDELINES.md compliance, controlplane/dataplane layout, GitOps promotion, secrets, auth, gateway). It verifies against `/Users/wso2/repos/wso2cloud-deployment` (all branches), the `agent-manager` reference at `/Users/wso2/repos/agent-manager`, and the OC docs at `/Users/wso2/openchoreo-sources/openchoreo.github.io`. It falls back to OC source code at `/Users/wso2/openchoreo-sources/openchoreo` only when the docs don't cover the area or deeper detail is needed.

**When to invoke it (do this proactively):**

Any task that meaningfully touches one of the following must be reviewed by `platform-design-expert` before the change is considered complete:

- New / changed OC primitives: Project, Component, Workload, ComponentType, Trait, Workflow, ReleaseBinding, SecretReference, ClusterWorkflow.
- Changes to `asdlc-service/clients/openchoreo/` or `services/workflowrun_service.go`.
- New env vars, secrets, or credential flows on any of the 5 long-lived services or the coding-agent runner.
- New ingress / HTTPRoute / gateway exposure, new auth flow, new service-to-service call.
- Changes to `deployments-v2/manifests/env-overlays/*.yaml` or the `deployments-v2/wso2cloud-deployment/` submodule.
- Architectural / design docs in `docs/design/*.md`.
- Anything described as "extending the platform", "new component pattern", "promotion / hosting / multi-tenancy".

**How to run the review:**

1. Read the design / change locally and identify the OC and cloud surfaces it touches.
2. Send one `Agent` call to `platform-design-expert`. The prompt should describe the design in self-contained terms (do not assume the agent knows App Factory) and point at the relevant App Factory files for the agent to read. The agent will return a combined review covering OC primitive mapping, WSO2 Cloud layout placement, GUIDELINES.md compliance, anti-patterns, and recommendations in one pass.
3. Apply the feedback: hard violations must be fixed; soft concerns should be discussed; if the agent flags a tension between OC-idiomatic and WSO2 Cloud-idiomatic, surface that to the user explicitly.
4. Compare against `agent-manager` (the reference implementation) for any choice the agent flags as ambiguous.

Skip the review only for purely local code-quality changes that have no OC or WSO2 Cloud surface (e.g. lint-only refactors inside a single function, frontend cosmetic tweaks).

## Practices
- **Important**: Always do the proper fix, stick to patterns used by agent manager and integration platform. No hacks.
- **Important**: If you come across a bug, fix it even if its not releted to your current task.
- **Important**: Any change touching OpenChoreo primitives, the OC client, or WSO2 Cloud hosting (deployments, secrets, ingress, auth, env overlays, the `deployments-v2/wso2cloud-deployment` submodule) MUST go through the `platform-design-expert` review described above. See the **Design Review** section.
- **Important**: Cluster health pre-flight (local dev). Before any operation that talks to the local k3d cluster — `kubectl`, `dev-cycle.sh`, BFF / OC API calls, dispatching a task, running tests — **delegate the check to an isolated subagent** (e.g. `general-purpose`) with a prompt that says: "Read [`docs/operations/cluster-health.md`](docs/operations/cluster-health.md), run the **Detect** block, and if anything trips run **Recover** until the cluster is clean. Report back in under 100 words: healthy / what was wrong / what you did." Run it in a subagent (not in the main context) so the kubectl/event output doesn't pollute the parent. Only proceed with the requested action after the subagent reports healthy. The laptop's sleep/wake cycle frequently leaves pods transiently unhealthy and OC's mutating webhook serving 502s; without this check the agent will surface those as confusing `INTERNAL_ERROR`s instead of waiting them out.

