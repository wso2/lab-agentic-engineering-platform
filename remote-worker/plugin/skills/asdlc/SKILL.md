---
name: asdlc
description: Load when working a component task dispatched by WSO2 Labs Agentic Engineer. The cwd is a clone of the project's repo on its default branch; the task is anchored by a GitHub issue passed in your prompt. You create your own working branch and open the PR. Defines the workflow, the mandatory `Closes #N` PR-body link, constraints, deny-list, project-structure conventions, the verify-before-PR step, and the OpenChoreo workload.yaml format. Stack-specific conventions (Go, React, Thunder OIDC, API Management) live in separate project skills the platform also preloads — apply them. Authentication is handled at the workspace level — run `git` and `gh` normally.
---

# WSO2 Labs Agentic Engineer component task

You are working a single component task on the WSO2 Labs Agentic Engineer
platform. The current working directory is a fresh clone of the
project's GitHub repo on its **default branch** (e.g. `main`); `git` and
`gh` are already authenticated for that repo. The platform passes you
the issue URL in your prompt — start there.

You don't need to handle authentication. `git push` and `gh ...` work
because the workspace is preconfigured (credential helper for `git`,
wrapper for `gh`). Don't try to `gh auth login`, set tokens, or change
`.git/config`'s credential helper — the platform writes those at
provisioning and refreshes them on every call.

> **Local-flow developers**: install this plugin into your own Claude Code
> (`claude plugin install <repo>/remote-worker/plugin`), then use your own
> `gh auth login`. The workflow below is identical.

## Active project skills

In addition to this `asdlc` skill, the platform preloads **project-attached
skills** at startup — they carry the stack/auth/runtime conventions for
this project. They appear in your context alongside this body and you
should consult them whenever their concern is relevant. Examples (the
exact set depends on which skills the architect attached to this
project):

- `go` — Dockerfile base image pin, `modernc.org/sqlite` driver, layout, port.
- `react-webapp` — Vite + nginx layout, `/env-config.js` + `window._env_`.
- `thunder-authentication` — OIDC + PKCE, `THUNDER_*` runtime keys.
- `api-management` — gateway JWT validation, `X-User-Id` header, CORS.

When the issue body's Scope section says something like "Wire upstream
X via window._env_.X_URL", that's a `react-webapp` requirement — read
that skill's body for the exact pattern. When it says "Use modernc.org/sqlite",
that's a `go` requirement. The skills are the authoritative source for
those conventions — do not re-derive them from training data.

## Find the issue

The platform passes you the GitHub issue URL in the user prompt — read it
WITH ITS COMMENTS:

```bash
gh issue view <url> --comments
```

The body has the task-specific spec (rationale, Overview, Scope,
Acceptance criteria, References, Task dependencies, Component Reference
card).

**The platform does NOT pre-create your branch or your PR — you create
both.**

If you ever need to discover the issue from scratch (e.g. running
locally without a prompt), the issue is labelled `asdlc` +
`implementation`:

```bash
gh issue list --label asdlc --label implementation --state open \
  --json number,title,url
```

## Workflow

1. **Read the issue** (`gh issue view <url> --comments`). The body is the
   spec. Capture the issue number — you'll need it in your PR body.
2. **Post a brief opening comment** so the platform shows your task is
   in flight:
   ```bash
   gh issue comment <issue-number> --body "Starting: <one-line plan>"
   ```
3. **Create a feature branch with a descriptive, kebab-case name.** Do
   NOT work on the default branch.
   ```bash
   git checkout -b feature/<short-slug>      # e.g. feature/hello-api-endpoint
   ```
4. **Apply the project's attached skills.** Patterns for `window._env_`,
   OIDC, protected handlers, etc. live in the per-skill bodies — see
   "Active project skills" above. The base `asdlc` skill carries
   workflow + workload.yaml grammar + the deny-list; everything stack-
   specific is in another skill.
5. **Edit, commit, push.** Standard `git add`, `git commit -m "..."`,
   `git push -u origin HEAD`. The committer identity is already set in
   `.git/config` — don't override it. The first push creates the remote
   branch.
6. **Build verification** (see "Build verification" below). Run the
   local toolchain check for your stack BEFORE opening the PR. If the
   check fails, read the error, fix the source, re-commit, and rerun.
   Only proceed once the toolchain check exits 0.
7. **Post progress comments** at meaningful milestones (after
   exploration, before committing, on completion). Keep them short.
8. **Open the PR with `Closes #<issue-number>` in the body.** This is
   how the platform links your PR back to the task — without it, the
   task is orphaned and never moves out of `in_progress`.
   ```bash
   gh pr create \
     --title "<short PR title>" \
     --body $'Closes #<issue-number>\n\n<short summary of changes>'
   ```
   `gh pr create` opens the PR ready-for-review by default. Pass
   `--draft` only if you genuinely have more work to do; in that case
   you must come back later and run `gh pr ready <pr-number>` yourself.
   After the PR is open and ready, **a human reviews and merges. You
   do not merge.**

## Build verification

Before opening the PR, you MUST verify your component compiles +
lockfile-resolves with the local language toolchain. The runner
sandbox ships `go`, `node` + `npm`, and the standard alpine
toolchain. This catches the failure modes that would otherwise burn a
PR + merge + dispatch round-trip:

- Hallucinated `go.sum` / `package-lock.json` hashes
- Missing imports, syntax errors, unresolved type errors
- Bad `import` paths, missing referenced files
- `go mod tidy` / `npm install` revealing wrong dep declarations

The exact verification commands for each stack are in the stack's
project skill — e.g. the `go` skill's "Build verification" section,
the `react-webapp` skill's "Build verification" section.

### If verification keeps failing

You have discretion to give up after a reasonable number of attempts
(suggested: **3 tries** for a given root cause). If verification
still fails:

1. Open the PR as a **draft** with `--draft` and a title prefix
   `[build-failed]`:
   ```bash
   gh pr create --draft \
     --title "[build-failed] <short title>" \
     --body $'Closes #<issue-number>\n\n**⚠️ Build verification failed.** The agent ran the local toolchain check but exhausted its retry budget. Pasting the last error output below for operator review.\n\n## Error\n```\n<tail of the failing command output, ~40 lines>\n```\n\n## What the agent tried\n- <bullet 1: what was attempted>\n- <bullet 2>'
   ```
2. Post the same diagnostic on the issue:
   ```bash
   gh issue comment <issue-number> --body "Build verification failed after N attempts. PR opened as draft for operator review. See PR #<n> for log."
   ```
3. Do NOT call the platform's `/verification-failed` endpoint — that
   path is for the dependency-integration verifier, not the
   self-build verifier. The draft PR + issue comment is the operator
   signal here.

## Project structure

Create a production-ready project structure under your component's
**App Path** (from the issue's Component Reference card). The App Path
is a **folder name** relative to the repo root (e.g. `user-api`,
`services/auth`) — it is NOT an HTTP route. All of this component's
files (source, `Dockerfile`, `workload.yaml`) must live under that
directory and nowhere else; the platform watches that path to decide
which component to rebuild on a push, so a file committed outside it
will not trigger your build.

Stack-specific layout, Dockerfile shape, and library choices live in
the relevant project skill (`go`, `react-webapp`, etc.) — do not
re-derive them.

Every component must have a `workload.yaml` at the root of its app
path (format below). The platform commits, pushes, builds, and deploys
for you.

## Constraints

- Implement the full API contract described in the issue. Every endpoint
  must be functional.
- The component must have a `Dockerfile` for containerized builds.
- The app must start with **no required environment variables** — use
  sensible hardcoded defaults for all config (JWT secrets, DB paths,
  API URLs, etc.). Env vars may override defaults but must never be
  required.
- No stubs or mocks. Write real, working implementations.
- Do not run, start, or execute the application server. Only write
  source files. The platform builds and deploys automatically; local
  execution causes port conflicts. Quick compile checks (`go build`,
  `tsc --noEmit`) are fine; never use `go run`, `npm start`,
  `node server.js`, or any command that starts a long-running process.
- **Never hand-write or guess dependency lockfile checksums.** Always
  regenerate the lockfile with your stack's dependency tool and commit
  the result — the exact command is in the relevant project skill's
  "Build verification" section (e.g. `go`, `react-webapp`). Hand-writing
  checksums causes the build pipeline to fail with
  `checksum mismatch ... SECURITY ERROR`.
- **Every service component with dependents MUST declare at least one
  HTTP endpoint with `visibility: external` in its `workload.yaml`** —
  this is what makes the deployed URL reachable for the dependent SPA's
  browser AND lets the BFF resolve the URL into `window._env_` for any
  sibling web-app that `dependsOn` this service.

## Do not

- Push directly to the default branch (`main`). Always work on the
  feature branch you created. Never force-push (`git push --force`).
- Open a PR without `Closes #<issue-number>` in the body — the platform
  uses that to link your PR to the task.
- Open more than one PR for this task.
- Run `gh pr merge`, `gh pr close`, `gh repo create`, `gh repo delete`,
  `gh repo fork`, or `gh repo edit`.
- Add a `dependencies.endpoints` block to `workload.yaml` (the
  consumer-side OC runtime-injection wiring). Sibling URLs reach the
  SPA at request time via `window._env_` (see the `react-webapp` skill).
- Add CORS middleware in any service component (see the `api-management`
  skill).
- Delete remote branches (`git push --delete`, `git push origin :branch`).
- Modify branch protection, secrets, repository settings, collaborators,
  or webhooks.
- Touch repos other than this one, or work outside the current working
  directory.

## OpenChoreo Workload Configuration

Every component must have a `workload.yaml` at its root. This file uses
the **flat WorkloadDescriptor** format — **not** a Kubernetes CR. Do
**not** use `kind: Workload`, `spec:`, `autoBuild`, or `autoDeploy`.

For v1, **declare only `endpoints` (provider-side)**. Do **not** declare
a `dependencies` block — consumer-side runtime URL injection is not used
in v1. Sibling URLs reach SPAs at request time via `window._env_`; Go
service consumers read sibling URLs from env vars set on the
ReleaseBinding (see the `api-management` skill).

### Format

```yaml
apiVersion: openchoreo.dev/v1alpha1
metadata:
  name: <component-name>        # logical name — no project prefix

endpoints:
  - name: <endpoint-name>
    type: HTTP                  # HTTP | GraphQL | Websocket | TCP | UDP | gRPC
    port: <port>
    basePath: /                 # optional; root path for API services
    visibility:
      - external                # REQUIRED for v1 service components with dependents
```

### Endpoint visibility levels

| Level | Accessible from |
|---|---|
| `project` | Same OpenChoreo project (implicit — always enabled) |
| `namespace` | Any component in the same Kubernetes namespace (cross-project) |
| `internal` | Across all namespaces in the cluster |
| `external` | Public internet via the ingress gateway |

For v1, service components that other components depend on MUST list
`external` (in addition to or instead of `project`) so the deployed URL
is mintable and reachable from the dependent's browser. The platform
will fail loudly with a §1.3 invariant error at the dependent's dispatch
time if a deployed dep has no external URL.

### Service-to-service runtime injection (legacy / deferred)

The OpenChoreo `dependencies.endpoints` block with `envBindings:` is a
real and supported primitive — it lets a Go/Node backend receive an
upstream URL at pod startup via an env var. The v1 WSO2 Labs Agentic
Engineer platform handles consumer-side env injection through a
different path: the BFF emits `<NAME>_URL` env vars onto the consuming
workload's ReleaseBinding directly (no `dependencies.endpoints` block
required). When the platform later supports the native runtime
injection grammar, this section will be updated. Do NOT add a
`dependencies.endpoints` block preemptively.
