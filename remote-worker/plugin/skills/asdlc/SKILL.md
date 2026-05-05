---
name: asdlc
description: Load when working a component task dispatched by the ASDLC platform. The cwd is a per-task workspace cloned on a `task/...` branch; the task is anchored by a GitHub issue and a draft PR. Defines the workflow: read the issue, edit code, commit, push, post progress, mark the PR ready. Authentication is configured at the workspace level — run `git` and `gh` normally.
---

# ASDLC component task

You are working a single component task on the ASDLC platform. The current working directory is a per-task workspace: a fresh clone of the project's GitHub repo with the task's feature branch already checked out, and `git` + `gh` already authenticated for that repo.

> **If you're running this task locally** rather than via the platform's remote worker, follow the **Local Developer Setup** section in the issue body before reading further — the assertions below assume that setup is complete.

You don't need to handle authentication. `git push` and `gh ...` work because the workspace is preconfigured (credential helper for `git`, wrapper for `gh`). Don't try to `gh auth login`, set tokens, or change `.git/config`'s credential helper — the platform writes those at provisioning and refreshes them on every call.

## Find the issue

Every ASDLC task is anchored by a GitHub issue with the `asdlc-task` label. The current branch maps to the issue:

```bash
branch=$(git rev-parse --abbrev-ref HEAD)
gh issue list --label asdlc-task --state open --search "$branch" --json number,title,url
gh issue view <number>
```

The issue body has everything you need: architecture context, component constraints, responsibilities, the working contract, and the deny-list. Read it before editing anything.

## Workflow

1. **Read the issue.** Don't skip this. The issue body is the spec for this task.
2. **Post a brief opening comment** on the issue so the platform shows your task is in flight:
   ```bash
   gh issue comment <issue-number> --body "Starting: <one-line plan>"
   ```
3. **Stay on the current branch.** Do not switch branches except for `git pull --rebase`. Do not create new branches.
4. **Edit, commit, push.** Standard `git add`, `git commit -m "..."`, `git push origin HEAD`. The committer identity is already set in `.git/config` — don't override it.
5. **Post progress comments** at meaningful milestones (after exploration, before committing, on completion). Keep them short.
6. **Finish by marking the PR ready for review.** The PR number is in the issue body; or look it up:
   ```bash
   git push origin HEAD
   gh pr ready <pr-number>
   ```
   After this, a human reviews and merges. **You do not merge.**

## Constraints

- Implement the full API contract described in the issue. Every endpoint must be functional.
- The component must have a `Dockerfile` for containerized builds.
- The app must start with no required environment variables — use sensible hardcoded defaults for all config (JWT secrets, DB paths, API URLs, etc.). Env vars may override defaults but must never be required.
- No stubs or mocks. Write real, working implementations.
- Do not run, start, or execute the application server. Only write source files. The platform builds and deploys automatically; local execution causes port conflicts. Quick compile checks (`go build`, `tsc --noEmit`) are fine; never use `go run`, `npm start`, `node server.js`, or any command that starts a long-running process.

## Do not

- Push to any branch other than the current task branch. Never force-push.
- Run `gh pr merge`, `gh pr close`, `gh repo create`, `gh repo delete`, `gh repo fork`, or `gh repo edit`.
- Delete remote branches (`git push --delete`, `git push origin :branch`).
- Modify branch protection, secrets, repository settings, collaborators, or webhooks.
- Touch repos other than this one, or work outside the current working directory.


## OpenChoreo Workload Configuration

Every component must have a `workload.yaml` at its root. This file uses the **flat WorkloadDescriptor** format — **not** a Kubernetes CR. Do **not** use `kind: Workload`, `spec:`, `autoBuild`, or `autoDeploy`.

- If the issue lists **Component Dependencies**, you MUST declare each one in `workload.yaml` under `dependencies.endpoints` AND use the injected environment variable in your application code. Never hardcode service URLs — OpenChoreo injects the resolved URL via the env var at runtime.

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
