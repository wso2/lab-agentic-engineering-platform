---
name: react-webapp
description: How to build a React SPA on the platform — Vite project layout, multi-stage Dockerfile → nginx:alpine runtime, synchronous /env-config.js load before the bundle, the authoritative window._env_ key set, throw-on-missing-key rule, and stock static-only nginx config (no envsubst, no proxy). Apply to every web-app component.
metadata:
  asdlc.version: "1"
---

# React Webapp

## What this skill does

The platform deploys React (Vite + TS) SPAs as a `nginx:alpine` image
serving a built static bundle. This skill tells the architect what to
expect and the coding agent exactly how to wire the bundle so per-env
values flow in at request time via `window._env_` — not at build time.

## Platform facts

- Web-app components have `componentType: web-app`, `entrypoint:
  deployment/web-application`, `buildpack: docker`, default port 9090.
- They do NOT get an OpenAPI spec — `set_openapi` for a web-app is
  rejected.
- They do NOT carry `exposesAPI` — that toggle is for backend API
  enforcement only. Web-apps express auth via `callerIdentity` instead
  (see `thunder-authentication`).
- The image is **identical across every environment**. Per-env values
  (API URLs, OIDC config, feature flags) arrive at request time via
  `window._env_`, populated by `/env-config.js`.
- The platform mounts `/env-config.js` into `/usr/share/nginx/html/`
  via the SPA's ReleaseBinding (`services/runtime_config_service.go:
  EmitForComponent`). The agent never generates or commits this file.
- The set of keys the platform emits into `window._env_` is
  **hardcoded in BFF code** (`runtime_config_service.go`). Inventing a
  new key in the SPA produces a runtime error at module load because
  the value is `undefined`.

### Authoritative `window._env_` keys

Use these EXACT spellings — do not invent new keys:

| Key | Set when | Meaning |
|---|---|---|
| `API_BASE_URL` | this web-app `dependsOn` a service sibling | external gateway URL of the primary upstream service in this project |
| `<UPSTREAM>_URL` | this web-app `dependsOn` `<upstream>` | external gateway URL of that sibling (`<UPSTREAM>` = upstream component name in `UPPER_SNAKE_CASE`, e.g. `todo-api` → `TODO_API_URL`) |
| `<NAME>_URL` | this web-app `dependentApis` includes `<name>` | external gateway URL of that external dependent API (same UPPER_SNAKE convention, e.g. `employee-api` → `EMPLOYEE_API_URL`) |
| `THUNDER_*` | this web-app has `callerIdentity.mode: end-user` | OIDC config keys (`THUNDER_URL`, `THUNDER_CLIENT_ID`, `THUNDER_REDIRECT_URI`, `THUNDER_SCOPES`, `THUNDER_AFTER_SIGN_IN_URL`) — owned by the `thunder-authentication` skill; see it for the per-key meanings and wiring |
| `<NAME>` (any) | the agent declared it in `workload.yaml` `configurations.env` | app-config default (per-env override possible) |

## Recommended practice

### Architect

- One web-app component per user-facing surface; do NOT split a frontend
  into "ui-shell" + "ui-pages" — every SPA is one component, one
  task, one bundle.
- For every backend in the web-app's `dependsOn`, the architect MUST
  include an instruction line in `componentAgentInstructions`:
  `Upstream <name>: read the URL from window._env_.<NAME_UPPER_SNAKE>_URL via src/env.ts. Throw (no ?? "" fallback) on missing.`
- Do NOT write anything about `VITE_*`, `REACT_APP_*`,
  `NEXT_PUBLIC_*`, `.env` files, build-time substitution, or
  "Dependency endpoint resolved" comments. Those mechanisms are
  deprecated — runtime config is the ONLY supported path.

### Tech-lead — issue body bullets

For every web-app task whose `dependsOn` is non-empty, include one
Scope bullet per upstream:

- "Wire upstream `<name>`: Read the URL from
  `window._env_.<NAME_UPPER_SNAKE>_URL` via `src/env.ts`. The platform
  writes per-env values into `/env-config.js` on the SPA's
  ReleaseBinding — no build-time configuration is required."

And one Acceptance criteria bullet:

- "The SPA's API client (`src/api.ts` or equivalent) reads each upstream
  URL from `window._env_.<UPSTREAM>_URL` via the typed `src/env.ts` shim
  and throws on missing value — no silent `?? \"\"` fallback. The
  platform's `env-config.js` is loaded synchronously before the bundle
  so the value is always populated when modules evaluate."

For every web-app task whose component has `callerIdentity.mode: end-user`,
also add this Scope bullet (covered fully in `thunder-authentication`):

- "`nginx/default.conf` is a stock static-file config — no proxy block,
  no envsubst, no custom entrypoint scripts. The image is identical
  across every environment; per-env values arrive at request time via
  the mounted `/env-config.js`."

### Coding agent — implementation

Project layout (Vite + TS):

```
<app-path>/
├── package.json
├── tsconfig.json
├── vite.config.ts
├── index.html
├── src/
│   ├── main.tsx
│   ├── App.tsx
│   ├── env.ts        # typed window._env_ shim
│   ├── api.ts        # fetch helpers
│   ├── auth.ts       # only if callerIdentity.mode: end-user — see thunder-authentication
│   └── pages/
├── nginx/
│   └── default.conf
└── Dockerfile
```

`index.html` — `<script src="/env-config.js">` is **synchronous**,
BEFORE the bundle. No `async`, no `defer`, no `type="module"` on this
tag. This guarantees `window._env_` is populated before any ES module
evaluates.

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
missing (which means a config bug, not a missing key default):

```ts
type Env = {
  API_BASE_URL: string;
  // Plus one <UPSTREAM>_URL per dependsOn entry, if any.
  // If this SPA has callerIdentity.mode: end-user, the THUNDER_* OIDC
  // keys are also present — extend this type with them per the
  // thunder-authentication skill, which owns the auth wiring.
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
fallback produces the v0 `405 Method Not Allowed` bug where every
fetch becomes a relative URL hitting the SPA's own nginx. The example
below is the unauthenticated client; if this SPA has
`callerIdentity.mode: end-user`, attach `Authorization: Bearer <token>`
to each fetch instead — see the `thunder-authentication` skill for the
auth'd client.

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

`nginx/default.conf` — pure static, no proxy:

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

`Dockerfile` — multi-stage build + stock `nginx:alpine`:

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

`workload.yaml` for a web-app:

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
```

Build verification (run BEFORE opening the PR):

```bash
cd <app-path>
npm install 2>&1 | tail -30   # regenerates package-lock.json
npx tsc --noEmit              # type-check without emitting JS
npm run build 2>&1 | tail -20 # actually build
```

Commit the resulting `package-lock.json`. Do not commit `node_modules/`.

### Don't

- ❌ Write a `.env` file in the app path.
- ❌ Read `import.meta.env.VITE_*` (or `process.env.REACT_APP_*`,
  `process.env.NEXT_PUBLIC_*`). Build-time mechanisms — the platform
  doesn't use them.
- ❌ Add `envsubst`, `/etc/nginx/templates/`, `NGINX_ENVSUBST_*`, or any
  custom `/docker-entrypoint.d/` script.
- ❌ Generate or commit your own `env-config.js`. The platform owns it.
- ❌ Use `?? ""` or any silent default when reading a `window._env_`
  key. A missing key must throw at module load.
- ❌ Invent a key name not in the authoritative table above.
- ❌ Add `exposesAPI` to a web-app — the toggle is for backends only.
- ❌ Add a separate `auth` / `login` component — Thunder owns sign-in
  (see `thunder-authentication`).

### Common pitfalls

| Symptom | Cause | Fix |
|---|---|---|
| SPA throws on load: `window._env_ not set` | `/env-config.js` failed to load (path wrong, served as 404, or `<script>` was `defer`/`async`) | Confirm `<script src="/env-config.js">` is **synchronous** (no `async`, no `defer`, no `type="module"`) and appears in `<head>` BEFORE the bundle's `<script type="module">`. |
| SPA throws on load: `<KEY> not set in window._env_` | The agent invented a key not in the authoritative table | Use the exact spellings; the platform only writes the keys it owns. |
| Browser POST hits the SPA's own host and returns `405 Method Not Allowed` | Code used a silent `?? ""` fallback so a missing key produced relative-URL fetches against nginx | Replace `?? ""` with `throw new Error(...)`. |
| `nginx: [emerg] host not found in upstream "thunder-service..."` at pod start | Legacy `/oidc/` proxy block in `nginx/default.conf` | Delete the `/oidc/` location block. Browser posts cross-origin. |
| Agent generated a `.env` file with `VITE_*` lines | Stale docs / training data | Delete it. Read `window._env_` via `src/env.ts`. |
