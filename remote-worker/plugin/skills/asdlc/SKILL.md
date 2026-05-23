---
name: asdlc
description: Load when working a component task dispatched by WSO2 Labs Agentic Engineer. The cwd is a clone of the project's repo on its default branch; the task is anchored by a GitHub issue passed in your prompt. You create your own working branch and open the PR. Defines the workflow, the mandatory `Closes #N` PR-body link, constraints, deny-list, project-structure conventions, the URL-as-build-constant dependency pattern, the verify-before-PR step, and the OpenChoreo workload.yaml format. Authentication is handled at the workspace level — run `git` and `gh` normally.
---

# WSO2 Labs Agentic Engineer component task

You are working a single component task on the WSO2 Labs Agentic Engineer platform. The current
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
card). You do NOT need to harvest upstream URLs from issue comments —
sibling URLs reach the running SPA at request time via `window._env_`
(see "Runtime config via `window._env_`" below). The agent's job is to
write code that *reads* `window._env_` via `src/env.ts`; no URLs are
ever hand-copied into source.

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

1. **Read the issue** (`gh issue view <url>`). The body is the spec.
   Capture the issue number — you'll need it in your PR body. (Sibling
   URLs and OIDC config are NOT in the issue — they reach the running
   pod via `window._env_`. See "Runtime config via `window._env_`".)
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
4. **Wire upstreams via `window._env_`.** Sibling URLs, OIDC config,
   and feature flags reach the running pod via an `env-config.js` file
   mounted at `/usr/share/nginx/html/` by the platform. Your code reads
   them at runtime through a typed `src/env.ts` shim — see "Runtime
   config via `window._env_`" below. No URLs are hand-copied into
   source; no `.env` file; no `import.meta.env.VITE_*`.
5. **Edit, commit, push.** Standard `git add`, `git commit -m "..."`,
   `git push -u origin HEAD`. The committer identity is already set in
   `.git/config` — don't override it. The first push creates the remote
   branch.
6. **Build verification** (see "Build verification" below). Run the
   local toolchain check for your stack (Go: `go mod tidy && go build
   -o /dev/null ./...`; Node/React: `npm install && npx tsc --noEmit
   && npm run build`). This catches lockfile hash mismatches, missing
   imports, syntax errors, and type errors BEFORE the platform tries
   to build the PR. If the check fails, read the error, fix the source,
   re-commit, and rerun. Only proceed once the toolchain check exits 0.
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

## Runtime config via `window._env_`

The image you produce is **identical across every environment**. All
per-env values — sibling API URLs, OIDC client config, app feature
flags — arrive at request time through a single file: `/env-config.js`,
mounted by the platform into `/usr/share/nginx/html/` via the SPA's
`ReleaseBinding`. The browser loads it synchronously before the bundle
evaluates, so `window._env_` is always populated when modules run.

You do **not** generate `env-config.js`. You do **not** put values in a
`.env`. You do **not** read `import.meta.env.VITE_*`. The platform
emits the file at dispatch time; your code reads `window._env_` via a
small typed shim and that's it.

### Authoritative keys

These are the keys the platform writes. Use them verbatim — do not
invent new spellings (e.g. `THUNDER_ISSUER`, `OIDC_CLIENT_ID`,
`API_URL`). Inventing a key produces a runtime error at module load
because the value will be `undefined`.

| Key | Set when | Meaning |
|---|---|---|
| `API_BASE_URL` | this web-app `dependsOn` a service sibling | external gateway URL of the primary upstream service in this project |
| `<UPSTREAM>_URL` | this web-app `dependsOn` `<upstream>` | external gateway URL of that sibling (`<UPSTREAM>` = upstream component name in `UPPER_SNAKE_CASE`, e.g. `todo-api` → `TODO_API_URL`) |
| `THUNDER_URL` | this web-app has `callerIdentity.mode: end-user` | OIDC issuer / authority for `oidc-client-ts` |
| `THUNDER_CLIENT_ID` | same | per-project Thunder OAuth client id |
| `THUNDER_REDIRECT_URI` | same | absolute URL of this SPA's `/callback` route |
| `THUNDER_SCOPES` | same | space-separated OIDC scopes (e.g. `openid profile email`) |
| `THUNDER_AFTER_SIGN_IN_URL` | same | absolute URL to land on after sign-in (usually the SPA root) |
| `<NAME>` (any) | the agent declared it in `workload.yaml` `configurations.env` | app-config default (per-env override possible) |

### The reference shape — three files

`index.html` — `<script src="/env-config.js">` is **synchronous**, BEFORE the bundle. No `async`, no `defer`, no `type="module"` on this tag.

```html
<!DOCTYPE html>
<html>
  <head>
    <meta charset="utf-8" />
    <title>App</title>
    <script src="/env-config.js"></script>
  </head>
  <body>
    <div id="root"></div>
    <script type="module" src="/src/main.tsx"></script>
  </body>
</html>
```

`src/env.ts` — typed read of `window._env_`. Throws if the file is
missing (which means a config bug, not a missing key default).

```ts
type Env = {
  API_BASE_URL: string;
  // Plus one <UPSTREAM>_URL per dependsOn entry, if any.
  // OIDC keys — present iff this SPA has callerIdentity.mode: end-user.
  THUNDER_URL: string;
  THUNDER_CLIENT_ID: string;
  THUNDER_REDIRECT_URI: string;
  THUNDER_SCOPES: string;
  THUNDER_AFTER_SIGN_IN_URL: string;
};

declare global {
  interface Window { _env_: Env }
}

if (!window._env_) {
  throw new Error(
    "window._env_ not set — /env-config.js failed to load. " +
    "The platform mounts this file via ReleaseBinding; if you see " +
    "this locally, host /env-config.js from your dev server.",
  );
}

export const env: Env = window._env_;
```

`src/api.ts` — read the upstream URL at module top-level; throw on
missing. Do NOT write `?? ""` or any other silent default — that
fallback produced the v0 `405 Method Not Allowed` bug where every
fetch became a relative URL hitting the SPA's own nginx.

```ts
import { env } from "./env";

const BASE_URL = env.API_BASE_URL; // or env.TODO_API_URL for a specific upstream
if (!BASE_URL) {
  throw new Error("API_BASE_URL not set in window._env_");
}

export async function listTodos(headers: HeadersInit = {}) {
  const res = await fetch(`${BASE_URL}/todos`, { headers });
  return res.json();
}
```

### Don't

- ❌ Write a `.env` file in the app path. The agent's bundle must not
  carry per-env values.
- ❌ Read `import.meta.env.VITE_*` (or `process.env.REACT_APP_*`,
  `process.env.NEXT_PUBLIC_*`). Those are build-time mechanisms — the
  platform doesn't use them.
- ❌ Add `envsubst`, `/etc/nginx/templates/`, `NGINX_ENVSUBST_*`, or any
  custom `/docker-entrypoint.d/` script. Stock `nginx:alpine` serves the
  static bundle + `/env-config.js` as-is.
- ❌ Generate or commit your own `env-config.js`. The platform owns it.
- ❌ Use `?? ""` or any silent default when reading a `window._env_`
  key. A missing key must throw at module load.
- ❌ Invent a key name not in the authoritative table above. The
  platform only writes the keys it owns; anything else is `undefined`.

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
  component, `src/env.ts` (typed read of `window._env_`), and entry
  point; `vite.config.ts`; `index.html` loads `/env-config.js`
  synchronously before the bundle; `nginx/default.conf` (static-only,
  no proxy block); `Dockerfile` (multi-stage build + stock
  `nginx:alpine` for serving — see SPA section below). NO `.env`
  file. NO `VITE_*` env vars.
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
  this is what makes the deployed URL reachable for the dependent SPA's
  browser AND lets the BFF resolve the URL into `window._env_` for any
  sibling web-app that `dependsOn` this service. Project-visibility-only
  services break the gating path for the v1 platform.
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
  consumer-side OC runtime-injection wiring). Sibling URLs reach the
  SPA at request time via `window._env_` — the BFF computes them at
  dispatch time and writes them into `/env-config.js` on the SPA's
  `ReleaseBinding`. See "Runtime config via `window._env_`" above.
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
in v1. Sibling URLs reach the SPA at request time via `window._env_`
(the BFF computes them at dispatch and writes them into
`/env-config.js` via the SPA's `ReleaseBinding`). See "Runtime config
via `window._env_`" above.

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
upstream URL at pod startup via an env var. The v1 WSO2 Labs Agentic Engineer platform does
**not** use it (frontend → backend is the only audited topology, and
Vite-style bundlers can't read pod env at runtime anyway). When the
platform later supports service-to-service runtime injection, this
section will be re-introduced with a working example. Do NOT add it
preemptively.

---

### SPA / Frontend components (React + nginx)

The SPA bundle is **identical across every environment**. Per-env
values (API URLs, OIDC config, feature flags) arrive at request time
via `window._env_`, populated by `/env-config.js` which the platform
mounts at `/usr/share/nginx/html/` via the SPA's `ReleaseBinding`. The
agent does not generate `env-config.js`, does not write a `.env`, does
not use `import.meta.env.VITE_*`, and does not run `envsubst`.

See "Runtime config via `window._env_`" earlier for the authoritative
key list + reference `src/env.ts` / `src/api.ts`.

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

# Optional: agent-authored defaults that become entries in window._env_.
# Safe defaults only — secrets and per-env values come from the platform.
configurations:
  env:
    - name: SUPPORT_EMAIL
      value: support@example.com
    - name: FEATURE_NEW_DASHBOARD
      value: "false"
```

#### nginx/default.conf — pure static, no proxy

```nginx
server {
    listen 9090;
    server_name _;
    root /usr/share/nginx/html;
    index index.html;

    location = /health {
        access_log off;
        return 200 'OK';
        add_header Content-Type text/plain;
    }

    location / {
        try_files $uri $uri/ /index.html;
    }
}
```

No `/oidc/` proxy block. No `proxy_pass` to in-cluster services. No
envsubst, no `${VAR}` placeholders. The platform-mounted
`/env-config.js` is served by this same static config as plain JS.

#### Dockerfile — stock nginx:alpine

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

No `/etc/nginx/templates/`. No `/docker-entrypoint.d/*.sh`. No
`NGINX_ENVSUBST_*`. The image is byte-identical across all
environments.

#### index.html — synchronous `/env-config.js` BEFORE the bundle

```html
<!DOCTYPE html>
<html>
  <head>
    <meta charset="utf-8" />
    <title>App</title>
    <script src="/env-config.js"></script>
  </head>
  <body>
    <div id="root"></div>
    <script type="module" src="/src/main.tsx"></script>
  </body>
</html>
```

The `<script src="/env-config.js">` tag MUST be synchronous (no
`async`, no `defer`, no `type="module"`). This guarantees
`window._env_` is populated before any ES module evaluates.

---

### SPA with OIDC sign-in (`callerIdentity.mode: end-user`)

When the component's `design.md` frontmatter has
`callerIdentity.mode: end-user`, the SPA is an OIDC relying party.
The platform additionally populates the `THUNDER_*` keys in
`window._env_` (per-project OAuth client + redirect URIs). The
agent's job is to wire `oidc-client-ts` against `env.THUNDER_*`.

There is NO `## OIDC client provisioned` issue comment. There is NO
`.env` file. There is NO same-origin `/oidc/` proxy in nginx — the
SPA posts directly to `${env.THUNDER_URL}/oauth2/token` cross-origin.

#### `src/auth.ts` — `oidc-client-ts` wired to `env.THUNDER_*`

```ts
import { UserManager, WebStorageStateStore } from "oidc-client-ts";
import { env } from "./env";

export const userManager = new UserManager({
  authority: env.THUNDER_URL,
  client_id: env.THUNDER_CLIENT_ID,
  redirect_uri: env.THUNDER_REDIRECT_URI,
  post_logout_redirect_uri: env.THUNDER_AFTER_SIGN_IN_URL,
  response_type: "code",
  scope: env.THUNDER_SCOPES,
  userStore: new WebStorageStateStore({ store: window.sessionStorage }),
  loadUserInfo: false,
});

export async function signIn()       { await userManager.signinRedirect(); }
export async function signOut()      { await userManager.signoutRedirect(); }
export async function handleCallback() { return userManager.signinRedirectCallback(); }

export async function getAccessToken(): Promise<string | null> {
  const user = await userManager.getUser();
  return user?.access_token ?? null;
}
```

Add a `/callback` route in your router that calls `handleCallback()`
once on mount and then navigates to `/`.

#### `src/api.ts` — attach `Authorization: Bearer <token>` and redirect on 401

```ts
import { env } from "./env";
import { getAccessToken, signIn } from "./auth";

async function authHeaders(): Promise<HeadersInit> {
  const token = await getAccessToken();
  return token ? { Authorization: `Bearer ${token}` } : {};
}

export async function listTodos() {
  const res = await fetch(`${env.API_BASE_URL}/todos`, {
    headers: await authHeaders(),
  });
  if (res.status === 401) { await signIn(); return []; }
  return res.json();
}
```

**Do NOT** write a `/login` form that POSTs credentials to your API —
the API has no `/auth/*` endpoints under this pattern; Thunder owns
token issuance. **Do NOT** invent `THUNDER_ISSUER` — the key is
`THUNDER_URL`. **Do NOT** add a same-origin `/oidc/` proxy — the
browser posts directly to `${env.THUNDER_URL}/oauth2/token`.

---

### Backend service with `exposesAPI.auth: end-user-required`

When the component's `design.md` frontmatter has
`exposesAPI.auth: end-user-required`:

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
| SPA throws on load: `window._env_ not set` | `/env-config.js` failed to load (path wrong, served as 404, or `<script>` was `defer`/`async`) | Confirm `<script src="/env-config.js">` is **synchronous** (no `async`, no `defer`, no `type="module"`) and appears in `<head>` BEFORE the bundle's `<script type="module">`. Confirm nginx is serving `/usr/share/nginx/html/env-config.js` (the platform mounts it via `ReleaseBinding`). |
| SPA throws on load: `<KEY> not set in window._env_` | The agent invented a key not in the authoritative table (e.g. `THUNDER_ISSUER` instead of `THUNDER_URL`) | Use the exact spellings from the "Authoritative keys" table. The platform only writes the keys it owns; everything else is `undefined`. |
| Browser POST hits the SPA's own host and returns `405 Method Not Allowed` | Code used a silent `?? ""` fallback so a missing key produced relative-URL fetches against nginx | Replace `?? ""` with `throw new Error(...)` in `src/env.ts` / `src/api.ts`. A missing key is a config bug — fail loud at module load. |
| CORS error in browser when calling upstream | Backend wrongly ships its own CORS middleware (doubled headers), OR upstream's `workload.yaml` lacks `visibility: external` | **Backends MUST NOT add CORS middleware.** The platform's gateway attaches an Envoy CORS filter to every `visibility: external` HTTPRoute via the ClusterComponentType. Remove the middleware. Confirm `visibility: external` on the upstream. |
| `nginx: [emerg] host not found in upstream "thunder-service..."` at pod start | Legacy `/oidc/` proxy block in `nginx/default.conf` pointed at an in-cluster Service FQDN | Delete the `/oidc/` location block. The browser posts to `${env.THUNDER_URL}/oauth2/token` cross-origin — no same-origin proxy needed. |
| Agent generated a `.env` file with `VITE_*` lines | Following stale docs / training data | Delete it. The bundle must not carry per-env values. Read `window._env_` via `src/env.ts` instead. |
| `workload.yaml` was modified to add `dependencies.endpoints` | Following stale docs | Remove the block. Sibling URLs flow through `window._env_`; the BFF computes them at dispatch time. |
