---
name: asdlc
description: Load when working a component task dispatched by the ASDLC platform. The cwd is a clone of the project's repo on its default branch; the task is anchored by a GitHub issue passed in your prompt. You create your own working branch and open the PR. Defines the workflow, the mandatory `Closes #N` PR-body link, constraints, deny-list, project-structure conventions, the URL-as-build-constant dependency pattern, the verify-before-PR step, and the OpenChoreo workload.yaml format. Authentication is handled at the workspace level — run `git` and `gh` normally.
---

# ASDLC component task

You are working a single component task on the ASDLC platform. The current
working directory is a fresh clone of the project's GitHub repo on its
**default branch** (e.g. `main`); `git` and `gh` are already authenticated
for that repo. The platform passes you the issue URL in your prompt —
start there.

You don't need to handle authentication. `git push` and `gh ...` work
because the workspace is preconfigured (credential helper for `git`,
wrapper for `gh`). Don't try to `gh auth login`, set tokens, or change
`.git/config`'s credential helper — the platform writes those at
provisioning and refreshes them on every call.

> **Local-flow developers**: install this plugin into your own Claude Code
> (`claude plugin install <repo>/remote-worker/plugin`), then use your own
> `gh auth login`. The workflow below is identical.

## Find the issue

The platform passes you the GitHub issue URL in the user prompt — read it
WITH ITS COMMENTS:

```bash
gh issue view <url> --comments
```

The body has the task-specific spec (rationale, Overview, Scope,
Acceptance criteria, References, Task dependencies, Component Reference
card). The **comment trail** is also load-bearing: the platform posts a
`## Dependency endpoint resolved` comment on this issue for every
upstream component your task depends on, the moment that upstream
reaches `deployed`. Those comments are the single source of truth for
upstream URLs — they are NOT in your prompt. See the "Dependency
endpoints" section below for how to harvest them.

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

1. **Read the issue with its comments** (`gh issue view <url> --comments`).
   The body is the spec; the comments carry the
   `## Dependency endpoint resolved` blocks for any upstream URLs you
   need. Capture the issue number — you'll need it in your PR body.
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
4. **If the issue comments include any `## Dependency endpoint resolved`
   blocks**, bake each URL into your component as a **build-time
   constant** (see "Dependency endpoints" below). Do this before you
   write any code that calls the upstream.
5. **Edit, commit, push.** Standard `git add`, `git commit -m "..."`,
   `git push -u origin HEAD`. The committer identity is already set in
   `.git/config` — don't override it. The first push creates the remote
   branch.
6. **Build verification** (see "Build verification" below). Run the
   local toolchain check for your stack (Go: `go mod tidy && go build
   -o /dev/null ./...`; Node/React: `npm install && npx tsc --noEmit`).
   This catches lockfile hash mismatches, missing imports, syntax
   errors, and type errors BEFORE the platform tries to build the PR.
   If the check fails, read the error, fix the source, re-commit, and
   rerun. Only proceed once the toolchain check exits 0.
7. **Verify integration with each dependency endpoint** (see "Verify
   before PR" below). If verification fails, follow the recovery steps —
   do NOT mark the PR ready-for-review.
8. **Post progress comments** at meaningful milestones (after
   exploration, before committing, on completion). Keep them short.
9. **Open the PR with `Closes #<issue-number>` in the body.** This is
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

## Dependency endpoints

Upstream URLs arrive through the issue's comment trail, not through your
prompt. Every time an upstream component this task depends on reaches
`deployed`, the platform posts a comment on this task's issue in the
following shape:

```
## Dependency endpoint resolved

- **todo-api**: http://http-todo-api-development-abc123.openchoreoapis.localhost:19080/

Posted by the platform when `todo-api` reached `deployed`. Bake this URL
into your component as a build-time constant (Vite/React:
`VITE_<UPSTREAM>_URL`; other stacks: the idiomatic equivalent). If a
later comment resolves the same component, use the most recent.
```

**How to harvest**

1. `gh issue view <url> --comments` — comments print after the body,
   oldest-first.
2. Scan all comments whose heading is `## Dependency endpoint resolved`.
3. For each upstream component name, keep the URL from the **most recent
   matching comment** — if the upstream redeployed, an earlier comment
   may carry a stale URL. The freshest comment always wins.

If the issue has no `## Dependency endpoint resolved` comments at all,
this task has no dependencies — skip this section.

Treat each resolved URL as **authoritative** and bake it into your
component as a **build-time constant**. Do not use any runtime injection
mechanism for v1 (`dependencies.endpoints` in `workload.yaml`,
env-var-at-pod-startup, configmaps, etc.) — those are deferred until the
platform supports service-to-service runtime injection.

Build-time-constant patterns per stack:

| Stack | Where to put it | How to read it |
|---|---|---|
| **Vite + React/TS** | `.env` at the app-path root: `VITE_<UPSTREAM>_URL=http://...` | `import.meta.env.VITE_<UPSTREAM>_URL` |
| **Create React App** | `.env` at the app-path root: `REACT_APP_<UPSTREAM>_URL=http://...` | `process.env.REACT_APP_<UPSTREAM>_URL` |
| **Next.js** | `.env` or `next.config.js`: `NEXT_PUBLIC_<UPSTREAM>_URL=http://...` | `process.env.NEXT_PUBLIC_<UPSTREAM>_URL` |
| **Other build-time stack** | The framework's idiomatic build-time-constant mechanism | n/a |

`<UPSTREAM>` is the dependency's component name in upper snake case
(e.g. `todo-api` → `TODO_API`).

The URL is baked into the JS bundle the browser downloads. The browser
makes the HTTP call directly to the upstream's external URL — no nginx
proxy, no runtime substitution. (Earlier docs described an
`envsubst`-templated nginx proxy; that pattern is no longer required and
you should not add it.)

**Empty value / silent-fallback prohibition.** Do NOT write code like
`const BASE_URL = import.meta.env.VITE_<UPSTREAM>_URL ?? "";` — the
fallback masks a misconfigured build. A missing or empty env var
**must** surface as a hard module-load error (throw at module
top-level) so the SPA visibly fails to mount and the agent's
verify-before-PR smoke test catches it. The silent same-origin
fallback was the v0 bug that produced `405 Method Not Allowed` from
the SPA's own nginx in production. See "Reading the URL" in the SPA
section below for the canonical pattern.

> **Why not runtime env vars?** Vite (and similar bundlers) freeze env
> variables into the production JS bundle at `npm run build` time. A pod
> env variable set at deploy time would have no effect on the served
> bundle. Baking the URL at build time is the only correct option for
> Web App components in v1.

## Build verification

Before opening the PR, you MUST verify your component compiles +
lockfile-resolves with the local language toolchain. The runner
sandbox ships `go`, `node` + `npm`, and the standard alpine
toolchain. Run the appropriate verification commands BELOW for your
component's stack. This catches the failure modes that would
otherwise burn a PR + merge + dispatch round-trip:

- Hallucinated `go.sum` / `package-lock.json` hashes
- Missing imports, syntax errors, unresolved type errors
- Bad `import` paths, missing referenced files
- `go mod tidy` / `npm install` revealing wrong dep declarations

### Go services

```bash
cd <component-app-path>
go mod tidy 2>&1 | tail -20   # regenerate go.sum from real checksums
go build -o /dev/null ./...   # compile everything; fails on any error
```

After `go mod tidy` succeeds, COMMIT the updated `go.sum` along with
your source. Without it, the build pipeline will fail on the next
`go mod download` step because lockfile entries are missing.

**SQLite driver — use `modernc.org/sqlite` (pure-Go), NOT `mattn/go-sqlite3` (CGO).**

The coding-agent pod and the build pod ship Go + alpine but the build path
is CPU-throttled. CGO compilation of the SQLite amalgamation
(`sqlite3-binding.c`, ~3 MB of C) takes 10–20 minutes on a throttled core
and frequently blocks the agent's verification step. The pure-Go driver
compiles in ~30 seconds and has the same `database/sql` interface:

```go
import (
    "database/sql"
    _ "modernc.org/sqlite"
)

db, err := sql.Open("sqlite", "/data/todos.db")
```

Use the literal driver name `"sqlite"` (not `"sqlite3"`) when calling
`sql.Open`. Performance is comparable to `mattn` for typical CRUD
workloads; the only loss is FTS3/FTS5 which the platform's todo-shaped
services don't need.

### React / Node SPAs

```bash
cd <component-app-path>
npm install 2>&1 | tail -30   # regenerates package-lock.json
npx tsc --noEmit              # type-check without emitting JS
# Optional but recommended: actually build
npm run build 2>&1 | tail -20
```

Commit the resulting `package-lock.json`. **Do not** commit the
`node_modules/` directory (add it to `.gitignore` if it isn't
already).

### Other stacks (Python, Rust, etc.)

The runner only has Go + Node toolchains installed today. For
unsupported stacks, commit `go.sum`/`package-lock.json`/etc. ONLY if
you can regenerate them via some path you trust — never invent
checksums. When in doubt, commit only the manifest (`pyproject.toml`,
`Cargo.toml`, etc.) and let the build pipeline regenerate the
lockfile.

### If verification keeps failing

You have discretion to give up after a reasonable number of attempts
(suggested: **3 tries** for a given root cause). If verification
still fails:

1. Open the PR as a **draft** with `--draft` and a title prefix
   `[build-failed]`:
   ```bash
   gh pr create --draft \
     --title "[build-failed] <short title>" \
     --body $'Closes #<issue-number>\n\n**⚠️ Build verification failed.** The agent ran the local toolchain check (`go build` / `npm install` / `tsc --noEmit`) but exhausted its retry budget. Pasting the last error output below for operator review.\n\n## Error\n```\n<tail of the failing command output, ~40 lines>\n```\n\n## What the agent tried\n- <bullet 1: what was attempted>\n- <bullet 2>'
   ```
2. Post the same diagnostic on the issue:
   ```bash
   gh issue comment <issue-number> --body "Build verification failed after N attempts. PR opened as draft for operator review. See PR #<n> for log."
   ```
3. Do NOT call the platform's `/verification-failed` endpoint — that
   path is for the dependency-integration verifier, not the
   self-build verifier. The draft PR + issue comment is the operator
   signal here.

## Verify before PR

Before running `gh pr ready` (or opening a non-draft PR), curl each
dependency URL from your local environment and confirm:

1. **Reachability**: a basic `GET <url>` returns HTTP 2xx (or another
   documented status — e.g. some health endpoints return 200; some root
   paths return 404 and that's fine if `/<spec'd path>` returns 200).
2. **A happy-path operation** for each resource group described in the
   upstream's OpenAPI (`.asdlc/design/components/<upstream>/openapi.yaml`).
   For a CRUD api, that's typically `POST` + `GET` + `DELETE` against one
   resource path. Don't try to enumerate every endpoint — pick one
   canonical operation per resource.
3. **The response shape loosely matches the OpenAPI**: HTTP status code
   in the right family, top-level JSON fields present.

Save the curl transcripts as you go — you'll paste them into a comment
on the issue if anything fails.

```bash
# example for todo-api
TODO_API="http://http-todo-api-development-abc123.openchoreoapis.localhost:19080"
curl -sS -i "$TODO_API/todos"             | head -20
curl -sS -i -X POST "$TODO_API/todos" -H 'Content-Type: application/json' \
  -d '{"title":"verify"}'                 | head -20
curl -sS -i -X DELETE "$TODO_API/todos/1" | head -5
```

> **Local-k3d caveat (libc `.localhost` short-circuit).** When the URL
> hostname ends in `.localhost`, glibc / musl resolves it to `127.0.0.1`
> per RFC 6761 — bypassing CoreDNS's rewrite — and curl will see
> "Connection refused". Resolve the host to the in-cluster data-plane
> gateway service IP using `getent`, then force curl to use it via
> `--resolve`:
>
> ```bash
> if [[ "$TODO_API" == *.openchoreoapis.localhost:* ]]; then
>   HOST="${TODO_API#http://}"; HOST="${HOST%%:*}"
>   PORT="${TODO_API##*:}"; PORT="${PORT%%/*}"
>   GW_IP="$(getent hosts gateway-default.openchoreo-data-plane.svc.cluster.local | awk '{print $1; exit}')"
>   if [ -n "$GW_IP" ]; then
>     RESOLVE="--resolve $HOST:$PORT:$GW_IP"
>   fi
> fi
> curl $RESOLVE -sS -i "$TODO_API/todos" | head -20
> ```
>
> In production (real DNS for the public hostname) the `$RESOLVE` flag
> stays empty and the snippet behaves identically.

### If verification succeeds

Proceed to `gh pr ready` (or `gh pr create` without `--draft`). Note
verification passed in your final issue comment ("Verification: api
reachable, POST/GET/DELETE round-trip succeeded").

### If verification fails

**Do not mark the PR ready-for-review.** Recovery:

1. **Keep the PR a draft** (do not run `gh pr ready`).
2. **Post a "Dependency verification failed" comment** on the issue with
   the diagnostic — include the URL, the exact curl command, and the
   response (status + body):
   ```bash
   gh issue comment <issue-number> --body "$(cat <<EOF
   Dependency verification failed.

   URL: $TODO_API/todos
   Command: curl -sS -i "$TODO_API/todos"
   Response:
   \`\`\`
   <paste status line + first 10 lines of body>
   \`\`\`

   Action: the upstream api appears unreachable / returns the wrong
   shape. Holding PR as draft pending operator action.
   EOF
   )"
   ```
3. **Notify the platform** by calling its verification-failed endpoint
   with your per-task bearer. The runner has already exported
   `ASDLC_PLATFORM_URL`, `ASDLC_TASK_ID`, and `ASDLC_BEARER_FILE` (a
   file path holding the bearer — read it at call time so the token
   never lands in shell history):
   ```bash
   curl -sS -X POST \
     "${ASDLC_PLATFORM_URL}/api/v1/tasks/${ASDLC_TASK_ID}/verification-failed" \
     -H "Authorization: Bearer $(cat "$ASDLC_BEARER_FILE")" \
     -H 'Content-Type: application/json' \
     -d "{\"diagnostic\":\"GET $TODO_API/todos returned 503\"}"
   ```
   Skip this call if `ASDLC_PLATFORM_URL` is empty (local-flow / older
   platform); the issue comment is still the durable signal.
   The platform transitions the task to `verification_failed` and
   surfaces it on the board with a Retry button. When the operator
   clicks Retry, the platform dispatches the task again with a fresh
   workspace and prompt.

If the platform endpoint call fails (network error, 4xx/5xx), STILL
keep the PR a draft and leave the issue comment — the comment is the
durable signal. Do not delete the branch or close the PR.

## Project structure

Create a production-ready project structure under your component's
**App Path** (from the issue's Component Reference card). The App Path
is a **folder name** relative to the repo root (e.g. `user-api`,
`services/auth`) — it is NOT an HTTP route. All of this component's
files (source, `Dockerfile`, `workload.yaml`) must live under that
directory and nowhere else; the platform watches that path to decide
which component to rebuild on a push, so a file committed outside it
will not trigger your build. Match the language/stack:

- **Go**: `go.mod` with proper module path; `cmd/` or `main.go` entry
  point; `Dockerfile` (multi-stage build); internal packages as needed
  (`handlers/`, `services/`, `models/`).
- **TypeScript / Node**: `package.json` with dependencies and scripts;
  `tsconfig.json`; `src/` with entry point; `Dockerfile` (multi-stage
  with `node:alpine`).
- **React (SPA)**: `package.json`; `tsconfig.json`; `src/` with App
  component and entry point; `vite.config.ts`; `.env` with
  `VITE_<UPSTREAM>_URL` constants if you have dependencies;
  `Dockerfile` (build + nginx for serving — see SPA section below).
- **Python**: `requirements.txt` or `pyproject.toml`; `src/` or `app/`
  directory with entry point; `Dockerfile`.
- **Other**: appropriate dependency manifest, clear entry point,
  `Dockerfile` for containerised builds.

Every component must have a `workload.yaml` at the root of its app path
(format below). The platform commits, pushes, builds, and deploys for
you.

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
- **Never hand-write or guess dependency lockfile checksums.** The
  runner sandbox ships `go` and `npm` — always generate `go.sum` /
  `package-lock.json` via `go mod tidy` / `npm install` and commit
  the result. Hand-writing checksums causes the build pipeline to
  fail with `checksum mismatch ... SECURITY ERROR`. See
  "Build verification" below — running the local toolchain check is
  the *only* approved way to populate a lockfile.
- **Every service component with dependents MUST declare at least one
  HTTP endpoint with `visibility: external` in its `workload.yaml`** —
  this is what makes the deployed URL reachable for the dependent's
  browser AND for the platform's `## Dependency endpoint resolved`
  comment that downstream tasks read. Project-visibility-only services
  break the gating path for the v1 platform.
- **Backend service components MUST NOT include CORS middleware in
  source code.** The platform's gateway attaches an Envoy CORS filter
  to every `visibility: external` HTTPRoute via the ClusterComponentType
  definition. Adding `Access-Control-Allow-*` headers in Go/Node
  middleware doubles them and breaks browsers (the browser sees two
  `Access-Control-Allow-Origin` values and rejects the response).
  No `corsMiddleware` function, no `cors.New(...)`, no manual headers.

## Do not

- Push directly to the default branch (`main`). Always work on the
  feature branch you created. Never force-push (`git push --force`).
- Open a PR without `Closes #<issue-number>` in the body — the platform
  uses that to link your PR to the task.
- Open more than one PR for this task.
- Run `gh pr merge`, `gh pr close`, `gh repo create`, `gh repo delete`,
  `gh repo fork`, or `gh repo edit`.
- Add a `dependencies.endpoints` block to `workload.yaml` (the
  consumer-side OC runtime-injection wiring). Bake the dependency URL
  into the artifact as a build-time constant instead — see "Dependency
  endpoints" above. (OC's `dependencies.endpoints.visibility` enum is
  `{project, namespace}` only — externally-reachable URLs cannot be
  injected via envBindings, so the consumer-side wiring is the wrong
  primitive for a browser-bundled SPA anyway. Keep build-time bake.)
- Add CORS middleware in any service component. The gateway handles
  CORS for `visibility: external` HTTPRoutes — adding it in code
  doubles headers and breaks browsers. See the constraint above.
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
in v1; the platform posts each upstream URL on the issue as a
`## Dependency endpoint resolved` comment and you bake it in as a
build-time constant.

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
upstream URL at pod startup via an env var. The v1 ASDLC platform does
**not** use it (frontend → backend is the only audited topology, and
Vite-style bundlers can't read pod env at runtime anyway). When the
platform later supports service-to-service runtime injection, this
section will be re-introduced with a working example. Do NOT add it
preemptively.

---

### SPA / Frontend components (React + nginx)

SPAs run in the browser. Static files are built with `npm run build`
(URLs baked in as `import.meta.env.*`) and served by nginx. There is NO
nginx-side reverse proxy in v1; the browser calls each upstream's
external URL directly using the baked-in constant.

#### workload.yaml

```yaml
apiVersion: openchoreo.dev/v1alpha1
metadata:
  name: <web-component-name>

endpoints:
  - name: http
    type: HTTP
    port: 9090
    visibility:
      - external
```

#### .env (at the SPA's app-path root)

```ini
VITE_TODO_API_URL=http://http-todo-api-development-abc123.openchoreoapis.localhost:19080
```

The value comes from the latest `## Dependency endpoint resolved` comment
on this task's GitHub issue (see "Dependency endpoints" above).

#### nginx.conf (static-only, no /api/ proxy)

```nginx
server {
    listen 9090;
    server_name _;
    root /usr/share/nginx/html;
    index index.html;

    location /health {
        access_log off;
        return 200 'OK';
        add_header Content-Type text/plain;
    }

    location / {
        try_files $uri $uri/ /index.html;
    }
}
```

#### Dockerfile (SPA)

```dockerfile
FROM node:20-alpine AS builder
WORKDIR /app
COPY package*.json ./
RUN npm i
COPY . .
RUN npm run build

FROM nginx:alpine
COPY --from=builder /app/dist /usr/share/nginx/html
COPY nginx.conf /etc/nginx/conf.d/default.conf
EXPOSE 9090
CMD ["nginx", "-g", "daemon off;"]
```

#### Reading the URL (`src/api.ts`)

```ts
// Hard fail on missing — the silent same-origin fallback (`?? ""`) shipped a
// production bug where every fetch became a relative URL and hit the SPA's
// own nginx with `405 Method Not Allowed`. Throw at module top-level instead
// so a misconfigured deploy is visible (the SPA fails to mount) rather than
// silently degraded.
const BASE_URL = import.meta.env.VITE_TODO_API_URL as string | undefined;
if (!BASE_URL) {
  throw new Error(
    "VITE_TODO_API_URL not set. Fill .env with the URL from this issue's `## Dependency endpoint resolved` comment BEFORE `npm run build`.",
  );
}
```

**Do NOT use `?? ""` or any silent default for these URL env vars.** A
missing or empty value must surface as a script-load error, not as a
relative-URL fetch.

---

### SPA with OIDC sign-in (`auth.kind: oidc-spa`)

When the component's `design.md` frontmatter has `auth.kind: oidc-spa`,
the SPA is an OIDC relying party. The platform posts a comment on this
issue with the OIDC client config:

```
## OIDC client provisioned

- **issuer**: http://thunder.openchoreo.localhost:8080
- **clientId**: asdlc-console-client
- **scopes**: openid profile
- **host**: thunder.openchoreo.localhost:8080
- **internalProxyPass**: http://thunder-service.thunder.svc.cluster.local:8090/oauth2/
```

**All values are baked at build time** — same pattern as the
dependency-URL section above. The first four values plus `API_BASE_URL`
from the upstream's `## Dependency endpoint resolved` comment go into
`<appPath>/.env` BEFORE `npm run build`. The `internalProxyPass` value
goes into `nginx/default.conf` as the literal `proxy_pass` target at
PR time. There is NO runtime substitution: no envsubst, no
`/etc/nginx/templates/`, no `/env-config.js`, no `window.__ENV__`, and
NO `configurations.env` in `workload.yaml`. The Docker image carries
the final config; `workload.yaml` only declares the `endpoints` block.

> **Why two URLs?** The `issuer` is the public hostname the browser
> uses for the cross-origin `authorize` redirect (it MUST be the same
> URL Thunder is registered at, so JS-side PKCE works). The
> `internalProxyPass` is what the SPA's own nginx uses to reach
> Thunder *from inside the cluster* — the public hostname
> `*.openchoreo.localhost` does NOT resolve from pod DNS, so using it
> in `proxy_pass` makes nginx fail to start with "host not found in
> upstream". The two values point at the same Thunder; the difference
> is who's doing the lookup (browser vs. pod).

> **Why a same-origin proxy?** Posting `POST /oauth2/token` directly to
> Thunder is cross-origin, and kgateway's CORS filter currently drops
> the response body even on a 200. Routing the POST through the SPA's
> own nginx (`/oidc/token` → Thunder's `/oauth2/token`) keeps it
> same-origin. The browser still does the `authorize` redirect
> cross-origin (no CORS — it's a top-level navigation).

#### workload.yaml (SPA with OIDC) — no env block

```yaml
apiVersion: openchoreo.dev/v1alpha1
metadata:
  name: <web-component-name>

endpoints:
  - name: http
    type: HTTP
    port: 9090
    visibility:
      - external
```

The `configurations.env` block is **deliberately absent**. All OIDC and
API values are bundled at build time. If you find yourself wanting to
add `configurations.env`, re-read this section — runtime env injection
is not supported on SPAs in v1.

#### `.env` (at the SPA's app-path root) — fill BEFORE `npm run build`

Five values, all from issue comments. The `VITE_` prefix is required so
Vite exposes them via `import.meta.env.VITE_*` in the bundle. Other
frameworks: CRA uses `REACT_APP_*`, Next.js uses `NEXT_PUBLIC_*`; the
substance is identical.

```ini
# from `## OIDC client provisioned` comment on this issue
VITE_OIDC_ISSUER=http://thunder.openchoreo.localhost:8080
VITE_OIDC_CLIENT_ID=asdlc-console-client
VITE_OIDC_SCOPES=openid profile
VITE_OIDC_HOST=thunder.openchoreo.localhost:8080

# from `## Dependency endpoint resolved` comment for the upstream service
VITE_API_BASE_URL=http://development-default.openchoreoapis.localhost:19080/<upstream-component-slug>
```

The token-exchange URL (`/oidc/token`) is a SAME-ORIGIN relative path
and lives in `src/auth.ts` as a constant — it does NOT need an env var
because every SPA serves it from its own nginx.

#### `nginx/default.conf` (static — no template, no envsubst)

```nginx
server {
    listen 9090;
    server_name _;
    root /usr/share/nginx/html;
    index index.html;

    location /health {
        access_log off;
        return 200 'OK';
        add_header Content-Type text/plain;
    }

    # Same-origin proxy for OIDC token / userinfo / etc.
    # Browser POST /oidc/token → nginx → Thunder /oauth2/token.
    # The proxy_pass URL below is the LITERAL `internalProxyPass` value
    # from the `## OIDC client provisioned` comment — an in-cluster
    # Service FQDN. DO NOT substitute the public `issuer` hostname here:
    # `*.openchoreo.localhost` does not resolve from pod DNS and nginx
    # will fail to start with "host not found in upstream".
    # NO envsubst, NO ${OIDC_*} placeholders.
    location /oidc/ {
        proxy_pass http://thunder-service.thunder.svc.cluster.local:8090/oauth2/;
        proxy_pass_request_headers on;
    }

    location / {
        try_files $uri $uri/ /index.html;
    }
}
```

#### Dockerfile (SPA with OIDC) — same as the plain SPA Dockerfile

```dockerfile
FROM node:20-alpine AS builder
WORKDIR /app
COPY package*.json ./
RUN npm i
COPY . .
RUN npm run build

FROM nginx:alpine
COPY --from=builder /app/dist /usr/share/nginx/html
COPY nginx/default.conf /etc/nginx/conf.d/default.conf
EXPOSE 9090
CMD ["nginx", "-g", "daemon off;"]
```

No `/etc/nginx/templates/`, no envsubst step. The bundle has the OIDC
values baked in; nginx has the issuer URL baked in.

#### `index.html` — standard, no `/env-config.js`

```html
<!doctype html>
<html>
  <head><title>App</title></head>
  <body>
    <div id="root"></div>
    <script type="module" src="/src/main.tsx"></script>
  </body>
</html>
```

#### Reference: PKCE flow (`src/auth.ts`)

The SPA does Authorization Code + PKCE directly. Minimal, no
heavyweight library required — but `oidc-client-ts` is a reasonable
choice if the spec asks for refresh tokens / silent renew.

```ts
// Values baked into the bundle at `npm run build` time from .env.
// Hard-fail at module top-level if any is missing — silent fallback is
// what produced the v0 405-Method-Not-Allowed bug.
const OIDC_ISSUER = import.meta.env.VITE_OIDC_ISSUER as string | undefined;
const OIDC_CLIENT_ID = import.meta.env.VITE_OIDC_CLIENT_ID as string | undefined;
const OIDC_SCOPES = import.meta.env.VITE_OIDC_SCOPES as string | undefined;
const API_BASE_URL = import.meta.env.VITE_API_BASE_URL as string | undefined;

if (!OIDC_ISSUER || !OIDC_CLIENT_ID || !OIDC_SCOPES || !API_BASE_URL) {
  throw new Error(
    "OIDC config missing. Ensure VITE_OIDC_ISSUER, VITE_OIDC_CLIENT_ID, " +
    "VITE_OIDC_SCOPES, and VITE_API_BASE_URL are set in `.env` BEFORE " +
    "`npm run build` (values from the issue's `## OIDC client provisioned` " +
    "and `## Dependency endpoint resolved` comments).",
  );
}

// Same-origin proxy path — served by the SPA's own nginx (see
// nginx/default.conf). This is a constant, NOT an env var: every SPA
// forwards /oidc/* to its own Thunder regardless of the cluster.
const OIDC_TOKEN_URL = "/oidc/token";

const REDIRECT_URI = window.location.origin + "/callback";

// 1. Kick off login: generate PKCE code_verifier, store in sessionStorage,
//    redirect to `${OIDC_ISSUER}/oauth2/authorize?response_type=code&...`.
//    (Top-level navigation — cross-origin, no CORS.)
// 2. /callback: read ?code=, exchange via OIDC_TOKEN_URL (same-origin
//    /oidc/token proxy) with code_verifier, store access_token in
//    sessionStorage.
// 3. fetch wrapper attaches Authorization: Bearer <access_token>.
// 4. On 401 from any upstream, clear token + restart step 1.

export async function startLogin() {
  const verifier = randomBase64Url(64);
  const challenge = await sha256Base64Url(verifier);
  sessionStorage.setItem("pkce_verifier", verifier);
  const params = new URLSearchParams({
    response_type: "code",
    client_id: OIDC_CLIENT_ID!,
    redirect_uri: REDIRECT_URI,
    scope: OIDC_SCOPES || "openid profile",
    code_challenge: challenge,
    code_challenge_method: "S256",
  });
  window.location.assign(`${OIDC_ISSUER}/oauth2/authorize?${params}`);
}

export async function completeLogin(code: string) {
  const verifier = sessionStorage.getItem("pkce_verifier")!;
  // MUST go through OIDC_TOKEN_URL (same-origin /oidc/token proxy).
  // POSTing directly to `${OIDC_ISSUER}/oauth2/token` triggers the
  // kgateway CORS bug — the browser sees an empty response body.
  const res = await fetch(OIDC_TOKEN_URL, {
    method: "POST",
    headers: { "Content-Type": "application/x-www-form-urlencoded" },
    body: new URLSearchParams({
      grant_type: "authorization_code",
      code,
      redirect_uri: REDIRECT_URI,
      client_id: OIDC_CLIENT_ID!,
      code_verifier: verifier,
    }),
  });
  const json = await res.json();
  sessionStorage.setItem("access_token", json.access_token);
}

// Helpers: randomBase64Url, sha256Base64Url — standard PKCE crypto.
// fetch wrapper — attach Bearer to every upstream call:
export async function authFetch(path: string, init: RequestInit = {}) {
  const token = sessionStorage.getItem("access_token");
  const headers = new Headers(init.headers);
  if (token) headers.set("Authorization", `Bearer ${token}`);
  const res = await fetch(API_BASE_URL + path, { ...init, headers });
  if (res.status === 401) {
    sessionStorage.removeItem("access_token");
    startLogin();
  }
  return res;
}
```

**Do NOT** add a username/password form that POSTs to the API. The API
has no `/auth/*` endpoints when this pattern is used — Thunder owns
token issuance.

> **Why no runtime env injection?** Vite (and similar bundlers) freeze
> `import.meta.env.VITE_*` at `npm run build` time. A pod env var set
> at deploy time would have no effect on the served bundle. Baking at
> build time is the only correct option for SPA components in v1, and
> it eliminates a whole class of platform bugs (env not propagated to
> pod, envsubst variable typos, missing `/env-config.js`, etc.). When
> the platform adds multi-environment promotion, this section will be
> revisited.

---

### Backend service with `api.security: required`

When the component's `design.md` frontmatter has `api.security: required`:

- The API Platform gateway validates the JWT before requests reach
  your service. **Do not validate JWTs yourself; do not implement
  `/auth/login` or `/auth/register`.**
- The gateway maps three JWT claims into request headers via the
  `api-configuration` trait's `jwt-auth` `claimMappings`:
  - `sub` → `X-User-Id` (the canonical caller identifier — REQUIRED)
  - `username` → `X-User-Name` (display; OPTIONAL)
  - `ouHandle` → `X-User-Ou` (multi-tenant scoping; OPTIONAL)
- Read `X-User-Id` on every request and reject (401) when missing.
  Treat `X-User-Name`/`X-User-Ou` as informational only.
- Per-user data MUST be keyed on `X-User-Id`. For a todo service,
  every row stores `user_id = X-User-Id` and every query filters by it.
- You do NOT need the JWT itself, the OIDC issuer, or any signing
  keys — the gateway has already validated the token and the
  Authorization header may not be forwarded.

Reference (Go) — every protected handler reads `X-User-Id` and every
SQL statement gates on it. Forgetting either filter leaks data between
users:

```go
func mustUserID(w http.ResponseWriter, r *http.Request) (string, bool) {
    uid := r.Header.Get("X-User-Id")
    if uid == "" {
        http.Error(w, `{"error":"missing X-User-Id"}`, http.StatusUnauthorized)
        return "", false
    }
    return uid, true
}

func listTodos(w http.ResponseWriter, r *http.Request) {
    uid, ok := mustUserID(w, r); if !ok { return }
    rows, err := db.QueryContext(r.Context(),
        `SELECT id, title, done FROM todos WHERE user_id = ? ORDER BY id DESC`, uid)
    /* ... */
}

func updateTodo(w http.ResponseWriter, r *http.Request) {
    uid, ok := mustUserID(w, r); if !ok { return }
    id := r.PathValue("id")
    // AND user_id = ? — both filters are mandatory. A bare `WHERE id = ?`
    // would let a caller toggle any user's row by guessing its id.
    res, _ := db.ExecContext(r.Context(),
        `UPDATE todos SET done = 1 - done WHERE id = ? AND user_id = ?`, id, uid)
    if n, _ := res.RowsAffected(); n == 0 {
        http.NotFound(w, r); return // returns 404 for both "not found" and "not yours"
    }
    w.WriteHeader(http.StatusNoContent)
}
```

`/health` should remain exempt (no `mustUserID` call) so the platform's
readiness probe can reach it without auth.

Storage: an embedded SQLite database is the canonical choice for per-user
data on a `service` component. **Use the pure-Go `modernc.org/sqlite`
driver, not `mattn/go-sqlite3`** — see the "Go services" build-verification
section above for the rationale. Keep the DB file under `/data/` so the
container's persistent volume (if any) captures it; every per-user row
must carry a `user_id TEXT NOT NULL` column populated from `X-User-Id`
and every query must filter on it.

---

### Common pitfalls

| Symptom | Cause | Fix |
|---|---|---|
| SPA throws on load (`VITE_<UPSTREAM>_URL not set`) | `.env` was missing, empty, or built before being filled | Write the URL from the `## Dependency endpoint resolved` comment into `<appPath>/.env` BEFORE `npm run build`. The hard-fail is intentional — silent fallback to a relative URL is what produced the v0 `405` bug. |
| Browser POST hits the SPA's host and returns `405 Method Not Allowed` | The SPA shipped with the silent `?? ""` fallback so a missing env var produced relative-URL fetches against its own nginx | Replace `?? ""` with `throw new Error(...)` in `src/api.ts` (see SPA section). Then fill `.env` correctly. |
| CORS error in browser when calling upstream | Backend wrongly ships its own CORS middleware (doubled headers), OR upstream's `workload.yaml` lacks `visibility: external` | **Backends MUST NOT add CORS middleware.** The platform's gateway attaches an Envoy CORS filter to every `visibility: external` HTTPRoute via the ClusterComponentType. Remove the middleware. Confirm `visibility: external` on the upstream. |
| Issue has no `## Dependency endpoint resolved` comment for an upstream you need | Upstream's `workload.yaml` lacks `visibility: external` (the platform won't post a comment for an upstream with no external URL) | Add `external` to the upstream component's visibility list and re-deploy; the platform re-posts the comment when the upstream lands `deployed` again |
| Endpoint URL injected by OC but bundle still uses old value | Vite bakes env vars at build time; pod env has no effect | Update `.env`, push, rebuild — runtime injection is not supported for SPA bundles |
| `workload.yaml` was modified to add `dependencies.endpoints` | Following stale docs | Remove the block. v1 uses build-time constants only. OC's `dependencies.endpoints.visibility` enum is `{project, namespace}` only, so envBindings cannot inject an externally-reachable URL into the bundle anyway. |
