---
name: asdlc
description: Load when working a component task dispatched by the ASDLC platform. The cwd is a clone of the project's repo on its default branch; the task is anchored by a GitHub issue passed in your prompt. You create your own working branch and open the PR. Defines the workflow, the mandatory `Closes #N` PR-body link, constraints, deny-list, project-structure conventions, and the OpenChoreo workload.yaml format. Authentication is handled at the workspace level — run `git` and `gh` normally.
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
with `gh issue view <url>` and start there. The issue body has the
task-specific spec: rationale, Overview, Scope, Acceptance criteria,
References, Task dependencies, the Component Reference card, and any
Component Dependencies wiring. **The platform does NOT pre-create your
branch or your PR — you create both.**

If you ever need to discover the issue from scratch (e.g. running
locally without a prompt), the issue is labelled `asdlc` +
`implementation`:

```bash
gh issue list --label asdlc --label implementation --state open \
  --json number,title,url
```

## Workflow

1. **Read the issue.** It is the spec for this task. Capture the issue
   number — you'll need it in your PR body.
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
4. **Edit, commit, push.** Standard `git add`, `git commit -m "..."`,
   `git push -u origin HEAD`. The committer identity is already set in
   `.git/config` — don't override it. The first push creates the remote
   branch.
5. **Post progress comments** at meaningful milestones (after
   exploration, before committing, on completion). Keep them short.
6. **Open the PR with `Closes #<issue-number>` in the body.** This is
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
  component and entry point; `vite.config.ts`; `Dockerfile` (build +
  nginx for serving — see SPA section below).
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

## Do not

- Push directly to the default branch (`main`). Always work on the
  feature branch you created. Never force-push (`git push --force`).
- Open a PR without `Closes #<issue-number>` in the body — the platform
  uses that to link your PR to the task.
- Open more than one PR for this task.
- Run `gh pr merge`, `gh pr close`, `gh repo create`, `gh repo delete`,
  `gh repo fork`, or `gh repo edit`.
- Delete remote branches (`git push --delete`, `git push origin :branch`).
- Modify branch protection, secrets, repository settings, collaborators,
  or webhooks.
- Touch repos other than this one, or work outside the current working
  directory.


## OpenChoreo Workload Configuration

Every component must have a `workload.yaml` at its root. This file uses
the **flat WorkloadDescriptor** format — **not** a Kubernetes CR. Do
**not** use `kind: Workload`, `spec:`, `autoBuild`, or `autoDeploy`.

- If the issue lists **Component Dependencies**, you MUST declare each
  one in `workload.yaml` under `dependencies.endpoints` AND use the
  injected environment variable in your application code. Never
  hardcode service URLs — OpenChoreo injects the resolved URL via the
  env var at runtime.

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
      - external                # one or more: project | namespace | internal | external

dependencies:
  endpoints:
    - component: <target-component-name>   # logical name only — no project prefix
      name: <target-endpoint-name>
      visibility: project                  # project | namespace
      project: <target-project>            # optional; omit if same project
      envBindings:
        address: <ENV_VAR_NAME>            # injected as full URL: scheme://host:port/basePath
```

### Endpoint visibility levels

| Level | Accessible from |
|---|---|
| `project` | Same OpenChoreo project (implicit — always enabled) |
| `namespace` | Any component in the same Kubernetes namespace (cross-project) |
| `internal` | Across all namespaces in the cluster |
| `external` | Public internet via the ingress gateway |

### Dependencies — envBindings keys

| Key | Injected value |
|---|---|
| `address` | Full URL: `scheme://host:port/basePath` |
| `host` | Hostname only |
| `port` | Port as string |
| `basePath` | URL path from the target endpoint's `basePath` |

For `gRPC`, `TCP`, `UDP`: `address` format is `host:port` (no scheme).

### Same-project dependency example

```yaml
dependencies:
  endpoints:
    - component: backend-api
      name: http
      visibility: project
      envBindings:
        address: BACKEND_API_URL
```

### Cross-project dependency example

```yaml
dependencies:
  endpoints:
    - component: auth-service
      name: http
      visibility: namespace
      project: platform-services
      envBindings:
        address: AUTH_SERVICE_URL
```

The target endpoint must declare `namespace` (or broader) visibility.

---

### SPA / Frontend components (React + nginx)

SPAs run in the browser and cannot reach in-cluster DNS names. Use the **nginx reverse proxy pattern**: the browser calls `/api/*`, nginx proxies to the injected in-cluster URL.

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

dependencies:
  endpoints:
    - component: <api-component-name>
      name: <api-endpoint-name>
      visibility: project
      envBindings:
        address: BACKEND_API_URL
```

#### nginx.conf (save as `nginx.conf` — template processed at startup)

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

    location /api/ {
        proxy_pass ${BACKEND_API_URL}/;
        proxy_set_header Host $http_host;
        proxy_set_header X-Real-IP $remote_addr;
        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
    }

    location / {
        try_files $uri $uri/ /index.html;
    }
}
```

#### entrypoint.sh

```sh
#!/bin/sh
# Strip trailing slash — OpenChoreo may inject "http://host:8080/"
BACKEND_API_URL="${BACKEND_API_URL%/}"

# Single-quoted arg protects nginx's own $variables from substitution
envsubst '$BACKEND_API_URL' \
  < /etc/nginx/conf.d/default.conf.template \
  > /etc/nginx/conf.d/default.conf

cat <<EOF > /usr/share/nginx/html/env.js
window.RUNTIME_BACKEND_API_URL = "/api";
EOF

exec "$@"
```

#### Dockerfile (SPA)

```dockerfile
FROM node:20-alpine AS builder
ARG VITE_API_BASE_URL=/api
ENV VITE_API_BASE_URL=$VITE_API_BASE_URL
WORKDIR /app
COPY package*.json ./
RUN npm i
COPY . .
RUN npm run build

FROM nginx:alpine
COPY --from=builder /app/dist /usr/share/nginx/html
COPY nginx.conf /etc/nginx/conf.d/default.conf.template
COPY entrypoint.sh /entrypoint.sh
RUN chmod +x /entrypoint.sh
EXPOSE 9090
ENTRYPOINT ["/entrypoint.sh"]
CMD ["nginx", "-g", "daemon off;"]
```

#### SPA index.html — load env.js before module scripts

```html
<head>
  <script src="/env.js"></script>
  <!-- other tags -->
</head>
```

#### API base URL resolution (`src/api.ts`)

```ts
const BASE_URL =
  (window as any).RUNTIME_BACKEND_API_URL ||
  import.meta.env.VITE_API_BASE_URL ||
  "http://localhost:8080";
```

---

### Common pitfalls

| Symptom | Cause | Fix |
|---|---|---|
| Dependency URL not injected | `spec:` wrapper used in workload.yaml | Use flat WorkloadDescriptor format — no `kind:`, no `spec:` |
| "0 resolved dependencies" | `visibility: external` in `dependencies[].visibility` | Use `project` or `namespace` only |
| "0 resolved dependencies" | Wrong component name | Use the exact logical component name (no project prefix) |
| nginx proxies to `//path` | Injected URL ends with `/`; template appends another `/` | Strip in entrypoint.sh: `BACKEND_API_URL="${BACKEND_API_URL%/}"` |
| `window.RUNTIME_BACKEND_API_URL` is undefined | `env.js` missing or placed after module scripts | Add plain `<script src="/env.js">` before any `<script type="module">` |
