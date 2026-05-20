# Git Integration — Component Design (Phase 0)

This doc reflects the implementation reality after the Phase 0 GitHub-native refactor. The architectural truth lives in `github-integration-evolution.md`; the implementation-level engineering plan lives in `github-integration-phase0.md`. Read those first if you're trying to understand *why* the system is shaped this way.

## Overview

`git-service` (Go) owns all GitHub-provider interactions. Project repositories are provisioned by the platform — users do not supply a repo URL or PAT. Phase 0 uses a single platform-level GitHub PAT shared across the deployment; Phase 2 introduces per-org credentials (App-installation or per-org user-PAT) without changing call sites.

## Credential boundary

git-service is the **only** place GitHub credentials live. The BFF orchestrates but never sees a token; agents reach credentials via `/credentials/refresh` on git-service, never directly. `pkg/credentials.Resolver` is the single seam; every code path that touches GitHub routes through `Resolver.Resolve(ocOrgID)`. The resolver refuses an empty `ocOrgID` — multi-tenant invariant from day one.

Phase 0 PAT scopes (Classic): `repo` + `admin:org` + `admin:repo_hook`.
Fine-grained equivalents: `Administration: Write`, `Contents: Write`, `Issues: Write`, `Pull requests: Write`, `Webhooks: Write`.

The legacy `GET /repos/{projectId}/credentials` endpoint that returns the PAT cleartext to the BFF (used to provision an OC `GitSecret` for build workflows) is tagged `// PHASE-2-REMOVE`. Phase 2 replaces it with OpenBao + `SecretReference`.

## Repo provisioning flow

`POST /api/v1/repos` (BFF → git-service) drives:

1. Resolve the org's credential via the resolver.
2. Generate a slugged repo name with a 3-digit suffix; retry on name collision.
3. `POST /orgs/{owner}/repos` (or `/user/repos` for user accounts).
4. Persist a `git_repositories` row with the repo URL.
5. Clone the repo to `${REPO_BASE_PATH}/{orgID}/{projectID}` for spec/design tag operations.
6. Register a webhook on the repo (delivery URL = `GITHUB_WEBHOOK_PROXY_URL`, HMAC = `GITHUB_WEBHOOK_SECRET`). Hook ID stored on the repo record so cleanup can deregister.

Repo-record cleanup deregisters the webhook and removes the on-disk clone. The GitHub repo itself is left in place by design — no accidental destruction of user work.

## Per-task GitHub artifacts

Each `ComponentTask` maps 1:1:1:1 to a GitHub issue + feature branch + draft PR, all created idempotently at dispatch (§12.1 in the phase0 doc):

| Artifact | Created by | Idempotent on |
|---|---|---|
| Issue | `POST /repos/.../issues` | task ID — re-dispatch finds the persisted IssueNumber and skips |
| Branch `task/<slug>-<short8>` | `POST /repos/.../git/refs` | branch name — existing ref returns existing tip SHA |
| Draft PR | `POST /repos/.../pulls` (`draft: true`) | (head, base) — existing open PR returned as-is |

The agent works on the feature branch inside a per-task workspace at `~/asdlc-workspace/<orgId>/<projectId>/<taskId>/`. Workspace credentials are file-scoped (chmod 600 bearer + chmod 700 git credential helper + chmod 755 `gh` wrapper). No tokens cross via env. The agent is told (in the issue body) to `gh issue comment` for progress and `gh pr ready <prNumber>` when done — never to merge.

## HTTP surface

Existing repo + git-ops + tag + issue endpoints are retained. New in Phase 0:

| Method | Path | Purpose |
|---|---|---|
| `POST` | `/api/v1/repos/{projectId}/branches` | Create a feature branch from a base ref |
| `POST` | `/api/v1/repos/{projectId}/pulls` | Open a draft PR |
| `POST` | `/api/v1/repos/{projectId}/webhooks` | Register the per-repo webhook |
| `DELETE` | `/api/v1/repos/{projectId}/webhooks` | Deregister |
| `POST` | `/api/v1/credentials/refresh` | Workspace credential helper — bearer-authed, returns `{token, expiresAt, identity}` |

The credentials-refresh endpoint sits on its own sub-mux behind a per-task bearer middleware (`middleware/task_bearer.go`); the bearer is HS256-signed by the BFF at dispatch with claims `{taskId, ocOrgId, iat, exp}` and a 24h TTL.

## Inbound webhooks (BFF)

Single endpoint `POST /webhooks/github` on the BFF, mounted outside JWT middleware. HMAC-validated against the org-resolved secret list. Dedup via `INSERT … ON CONFLICT DO NOTHING` on `webhook_deliveries.delivery_id`. Synchronous processing in Phase 0 (durable queue is a §9.1 hardening item).

Routing: parse `installation.id` (App-mode) or `repository.full_name` (PAT-mode) → resolve `ocOrgID` → HMAC-validate against that org's secrets → dedup → dispatch. Phase 0 returns the constant `"platform"` for `ocOrgID`; Phase 2 fills in real lookups without changing the pipeline.

Per-event handlers:
- `pull_request.ready_for_review` → projector applies `pr.ready_for_review`
- `pull_request.closed merged=true` → projector applies `pr.merged`, records `MergeCommitSHA`
- `pull_request.closed merged=false` → projector applies `pr.rejected`
- `push` to default branch → BFF creates `WorkflowRun` with SHA pinned at `params.repository.revision.commit`; advances merged tasks to `building`
- `issue_comment`, `pull_request.opened`, `pull_request.reopened`, others — persisted, no-op

State transitions are declarative — every change goes through `services.ApplyTaskEvent` against the transition table in `services/task_state.go`. The projector (`services/webhook/projector.go`) is the only writer of `ComponentTask.Status` outside dispatch. Per-task and per-project Postgres advisory locks (`pg_advisory_xact_lock(hashtext('task:'||id))`, `…('project:'||id)`) keep concurrent handlers serialised. Cross-handler rendezvous via `project_default_pushes` (composite PK `(project_id, sha)`) handles arbitrary push/PR ordering.

## Build trigger model

The BFF drives builds. OC `Component`s are created at dispatch with `AutoBuild: false` and `AutoDeploy: true`. The push handler creates a `WorkflowRun` with the merge SHA pinned at trigger time (mirrors agent-manager's `clients/openchoreosvc/client/builds.go:71-85`); idempotent on `(componentName, sha)` via `ComponentTask.LastBuildSHA`. Build status comes from a 10s polling sweep (`services/webhook/build_watcher.go`) using `FOR UPDATE SKIP LOCKED` so multi-replica BFFs split the work safely.

## Local-dev webhook delivery

Local-dev uses **smee.io** as a webhook proxy. `setup-asdlc.sh` provisions a fresh channel per developer and persists the URL into `deployments/.env`. The `smee-client` container forwards `https://smee.io/<channel>` to `http://asdlc-api:9090/webhooks/github`. Production deployments expose the endpoint via ingress and skip smee entirely.

## Spec/design artifact tags (unchanged)

Spec and design artifacts remain stored as files in the project's cloned repo (`specs/spec.md`, `specs/design.json`). Tagging is done on a single shared clone owned by git-service, serialised by an in-process project lock. Per-task workspaces never touch this clone — they're for agent code work only.

## See also
- `github-integration-evolution.md` — architectural truth (trust model, three-phase evolution).
- `github-integration-phase0.md` — implementation-level engineering plan.
