# Auth & runtime config — implementation spec

Status: implementation-ready
Scope: the canonical specification for how the platform generates a
webapp + API pair, how auth is wired, how runtime values reach the SPA
and the API, and how API management is published. Treat the contents
of §3 (stage-by-stage emission spec) as authoritative — if the
implementation diverges from §3, the implementation is wrong.

This document is the source of truth for the refactor. Anything
labelled "today" describes code we're removing; anything labelled
"emits", "writes", or "MUST" describes the target end state.

---

## 1. Principle and references

The cleanup is driven by a single principle: **stop teaching the LLMs
about plumbing that OpenChoreo already models**. Architect, tech-lead,
and worker prompts today carry overlapping HOW-instructions for OIDC,
nginx, env-var prefixes, and CORS — several hundred lines of fragile
glue duplicated across three layers.

The target shape is lifted from **`wso2cloud-deployment`** — WSO2
Cloud's GitOps repo, which deploys the same stack we run (OpenChoreo
+ Thunder + kgateway + WSO2 API Platform) and ships a finished,
production-tested version of every pattern this doc proposes.

Two OC primitives carry the load:

- **`api-configuration` ClusterTrait** — `cors` + `jwt-auth` v1 +
  `basic-ratelimit` + `add-headers` at the gateway. Attached to API
  endpoints only. Source:
  `controlplane/.../traits/api-configuration.yaml:214-218`.
- **`ReleaseBinding.workloadOverrides.container.files`** — literal
  per-env runtime config materialised by the OC reconciler at the
  declared mountPath. No envsubst, no nginx templates, no in-container
  substitution. Source:
  `developers/.../cloud-console/release-binding.yaml:18-56`.

### The agentic twist

In wso2cloud-deployment a *human platform engineer* hand-authors the
per-env `ReleaseBinding`. In our flow there is no human in that loop
— **the BFF plays the platform-engineer role**, emitting `Component`
+ `ReleaseBinding` deterministically from architect intent + project
state + env config. The agent never sees an OIDC client_id, an
upstream URL, a `.env` file, or a redirect URI.

### Scope assumption: agent verifies build only

The dev loop does **not** call the API from the coding-agent sandbox.
The agent verifies up to build (`go build`, `npm install` + `tsc
--noEmit` + `npm run build`). End-to-end verification happens after
deploy, in a browser via Playwright.

### Reference scenario

Throughout this doc: a webapp + API pair.

- `todo-web` — React SPA, signed-in user manages their own todos.
- `todo-api` — Go service, persists per-user todos.

Both `visibility: external`, fronted by the WSO2 API Platform
gateway. CORS + JWT at the gateway via the `api-configuration` trait
on the API.

### Dry-run validation (already done)

We hand-authored both halves (producer code + platform manifests) and
applied them to a fresh ASDLC cluster on branch `cleanup-auth`. The
OC primitives compose as documented:

- `workloadOverrides.container.files` materialises `env-config.js` at
  the declared mountPath; stock nginx serves it as
  `application/javascript`; the React bundle sees `window._env_`
  populated before its first render.
- Cross-origin SPA→API works: the `api-configuration` CORS policy
  echoes the SPA origin (not wildcard) on the preflight.
- The gateway 401s without a token, 200s with a valid Thunder JWT,
  injects `X-User-Id` from the `sub` claim, and **strips
  `Authorization` before forwarding upstream** — defense in depth
  for free.
- Per-user isolation works.

One platform finding (already fixed): the ASDLC fork of
`deployment/service` and `deployment/web-application` in
`setup-asdlc.sh` originally stripped the OC `configurations.*` CEL
helpers, silently dropping every `workload.container.{env,files}`
declaration and every `ReleaseBinding.workloadOverrides.*` override.
Fixed by mirroring `agent-manager`'s `agent-api` ComponentType.
Landed on `cleanup-auth`.

Artefacts kept after the dry run:

- `deployments/scripts/setup-asdlc.sh` — both CCTs now
  configurations-complete.

The hand-authored apps/todo-{api,web}/ + deployments/manifests/poc-
runtime-config/all.yaml fixtures used during the dry run were removed
once the agent-driven flow was verified to produce the same shape end-
to-end (see Phase 1+2 E2E runs in branch history).

---

## 2. Layer separation and timeline

| Layer | What it is | Authored by | Resolved at |
|---|---|---|---|
| `workload.yaml` declarations | endpoints, `configurations.env` defaults | Coding agent | coding time |
| Image content | bundle + Dockerfile + static `nginx/default.conf` | Coding agent | build time |
| `Component` CR + `ReleaseBinding` | `traits:` (services only), `traitEnvironmentConfigs`, `workloadOverrides.container.files` (env-config.js), `workloadOverrides.container.env` (secrets) | BFF | dispatch time |
| `Deployment`, `Service`, `HTTPRoute`, `Backend`, `RestApi` | k8s + WSO2 resources | OC controllers | reconcile time |

The image is **identical across every environment**. Per-env values
arrive via `ReleaseBinding`, mounted as a literal `env-config.js`
file.

### End-to-end timeline

```
[CODING TIME — agent writes into the repo]
  workload.yaml      — endpoints + configurations.env (defaults)
  nginx/default.conf — pure static
  index.html         — <script src="/env-config.js"></script> BEFORE bundle
  src/env.ts         — typed read of window._env_
  src/auth.ts        — oidc-client-ts wired to env.THUNDER_*
  src/api.ts         — fetch(`${env.API_BASE_URL}/...`) with Bearer

         ↓ git push, CI runs

[BUILD TIME — OC CI builds the image]
  docker build       — static bundle baked into image
                       IMAGE IDENTICAL ACROSS ALL ENVIRONMENTS

         ↓ PR merge → BFF dispatches deploy

[DISPATCH TIME — BFF emits the platform half]
  Thunder ConfigMap  — per-project OAuth client (CLOUD_CONSOLE-shape),
                       one-time per project on first SPA dispatch
  Component CR       — services: with traits: [api-configuration]
                       webapps: no traits (plain deployment/web-application)
  ReleaseBinding     — per environment:
                        traitEnvironmentConfigs.<instance>:
                          cors.allowedOrigins  — computed from sibling SPAs
                          jwtAuth.enabled      — from exposesAPI.auth
                        workloadOverrides.container.files:
                          env-config.js — LITERAL per-env values
                                          (API_BASE_URL, THUNDER_*, app config)
                        workloadOverrides.container.env:
                          secrets via secretKeyRef

         ↓ OC reconciles

[DEPLOY TIME — OC materialises]
  Deployment         — env-config.js mounted at /usr/share/nginx/html/
  HTTPRoute          — patched by api-configuration trait (services only)
  WSO2 RestApi       — created with cors + jwt-auth policies (services only)
  kgateway Backend   — points HTTPRoute at WSO2 router (services only)

         ↓ pod schedules

[POD START — stock nginx]
  nginx serves       — /env-config.js as plain static
                       index.html + bundle from /usr/share/nginx/html/
                       NO entrypoint magic, NO envsubst

         ↓ request flows

[REQUEST TIME — browser]
  SPA loads          — index.html, env-config.js evaluates window._env_
  oidc-client-ts     — initiates PKCE against env.THUNDER_URL
                       with per-project env.THUNDER_CLIENT_ID
  Thunder authn      — 302 back to env.THUNDER_REDIRECT_URI?code=…
  Token exchange     — bundle posts code to Thunder /oauth2/token (cross-origin)
  API call           — fetch(env.API_BASE_URL+"/todos", Bearer <jwt>)
  Gateway            — jwt-auth v1 validates JWT against Thunder JWKS,
                       strips Authorization, injects X-User-Id from sub claim,
                       cors policy echoes the SPA origin
  todo-api           — reads X-User-Id, scopes query by user
```

---

## 3. Stage-by-stage emission spec

This section is the canonical reference for what each stage produces.

### 3.1 Architect

**Input:** project requirements (`specs/requirements/*.md`).
**Output:** `specs/design/design.md` + `specs/design/components/<name>/design.md` (one per component).

**Component design.md frontmatter — schema:**

```yaml
---
name: todo-web
componentType: web-app | service | scheduled-task | manual-task | api-proxy
language: TypeScript / React | Go | Python | ...
appPath: todo-web
dependsOn: [todo-api]
callerIdentity:
  mode: end-user | service-account | none
exposesAPI:                       # only for components that expose an API
  managed: true                   # routed through WSO2 API Platform gateway
  auth: end-user-required | service-required | none
  userContext: X-User-Id          # header to inject on upstream
buildpack: nodejs | go | ...
entrypoint: ...
---
```

**Example — webapp:**

```yaml
---
name: todo-web
componentType: web-app
language: TypeScript / React
appPath: todo-web
dependsOn: [todo-api]
callerIdentity:
  mode: end-user
---

Todo App SPA: signed-in users see their own todos and create / complete / delete them.
```

**Example — API:**

```yaml
---
name: todo-api
componentType: service
language: Go
appPath: todo-api
dependsOn: []
exposesAPI:
  managed: true
  auth: end-user-required
  userContext: X-User-Id
---

Todo App API: persists per-user todos. Gateway validates the JWT and
injects X-User-Id before requests reach the service.
```

**Architect MUST emit:**

- One frontmatter file per component matching the schema above.
- `dependsOn` listing every sibling component this one calls.
- 1–2 sentences of WHAT the component does. Nothing more.

**Architect MUST NOT emit:**

- ❌ `auth.kind: oidc-spa` — replaced by `callerIdentity.mode: end-user`.
- ❌ `api.security: required` — replaced by `exposesAPI.auth: end-user-required`.
- ❌ `componentAgentInstructions` — replaced by tech-lead-generated task bodies.
- ❌ Any URL literal, port, env-var name, client_id, redirect URI, gateway host, in-cluster FQDN, OIDC client, `.env` reference, nginx config, envsubst hint, `internalProxyPass`, `/oidc/token`.
- ❌ Hardcoded service URLs for external dependencies (e.g. Secret Santa). External deps go through a BFF-side catalog (Phase 1 step 10).

**Files to change:** `agents/src/agents/architect/{prompt.ts, schema.ts}`.

### 3.2 Tech-lead

**Input:** architect's `specs/design/components/<name>/design.md`.
**Output:** ComponentTask record in PostgreSQL + GitHub issue with the task body.

**Task body template — webapp:**

```markdown
## Overview
Build `todo-web`: a React SPA for the Todo App project. Users sign
in, see their own todos, and create / complete / delete them. Calls
the sibling `todo-api`.

## Scope
- Implement the screens described in `specs/requirements/`.
- Show the signed-in user's name. The platform provides login config
  via `window._env_.THUNDER_*` — use `oidc-client-ts` against those
  values.
- Call `todo-api` at `${env.API_BASE_URL}` with `Authorization: Bearer
  <token>`. The runtime config is loaded before the bundle from
  `/env-config.js`; the platform writes per-env values into that file.

## Acceptance criteria
- Unauthenticated visit redirects to platform sign-in and returns signed in.
- Each user only sees their own todos.

## References
- Upstream contract: `specs/design/components/todo-api/openapi.yaml`
- Runtime config keys: `SKILL.md` § Runtime config via `window._env_`

## Task dependencies
None.
```

**Task body template — API:**

```markdown
## Overview
Build `todo-api`: a Go service backing the Todo App project.
Persists per-user todos. Externally exposed via the platform's API
gateway, which validates the JWT and injects `X-User-Id` before the
request reaches the service.

## Scope
- Implement the OpenAPI contract at
  `specs/design/components/todo-api/openapi.yaml`.
- Read `X-User-Id` from every request; reject 401 when missing.
  `/health` is exempt.
- Persist to embedded SQLite at `/data/todos.db` (driver:
  `modernc.org/sqlite`).

## Acceptance criteria
- All endpoints in `openapi.yaml` work and pass OpenAPI shape checks.
- Per-user isolation: every row carries `user_id = X-User-Id`;
  queries filter on it.
- `/health` returns 200 without auth.

## References
- `specs/design/components/todo-api/openapi.yaml`

## Task dependencies
None.
```

**Tech-lead MUST NOT include:**

- ❌ PKCE walkthroughs, OIDC config harvest, `## OIDC client provisioned` references.
- ❌ `.env BEFORE npm run build` instructions.
- ❌ `internalProxyPass` literals, in-cluster FQDNs.
- ❌ References to nginx templates, envsubst, `NGINX_ENVSUBST_*`.
- ❌ References to `## Dependency endpoint resolved` issue comments.

**Files to change:** `agents/src/agents/tech-lead/prompt.ts`.

### 3.3 Coding worker — webapp (SPA)

The agent writes a **single image that runs unchanged in every
environment**. All per-env values come from `window._env_`, written
by the BFF into `env-config.js` via `ReleaseBinding`.

#### Files emitted

| Path | Purpose |
|---|---|
| `workload.yaml` | endpoints + `configurations.env` defaults |
| `Dockerfile` | multi-stage: node build → nginx serve |
| `nginx/default.conf` | static-file serving + SPA fallback |
| `index.html` | loads `/env-config.js` BEFORE the bundle (synchronous) |
| `package.json` | deps include `oidc-client-ts`, `react`, `vite` |
| `src/env.ts` | typed read of `window._env_` |
| `src/auth.ts` | `oidc-client-ts` configured against `env.THUNDER_*` |
| `src/api.ts` | `fetch(`${env.API_BASE_URL}/...`)` with Bearer token |
| `src/main.tsx`, `src/App.tsx`, ... | App code |

#### `workload.yaml`

```yaml
apiVersion: openchoreo.dev/v1alpha1
metadata:
  name: todo-web

endpoints:
  http:
    type: HTTP
    port: 8080
    visibility:
      - external

# Optional: agent-authored defaults for runtime config keys.
# These become entries in window._env_ via the BFF (with per-env
# overrides from the ReleaseBinding). Safe defaults only.
configurations:
  env:
    - name: FEATURE_NEW_DASHBOARD
      value: "false"
    - name: SUPPORT_EMAIL
      value: support@example.com
```

`endpoints` uses the **map form** keyed by name (matches the trait
contract at `api-configuration.yaml:33`). No
`dependencies.endpoints`. No URL placeholders. No OIDC config in the
Workload — that's all in the `ReleaseBinding`.

#### `Dockerfile`

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
EXPOSE 8080
```

No `/etc/nginx/templates/`. No envsubst. No `/docker-entrypoint.d/`
overlays. No `NGINX_ENVSUBST_*` env vars.

#### `nginx/default.conf`

```nginx
server {
    listen 8080;
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

No proxy block, no env-var references. nginx serves the static bundle
and `/env-config.js` (mounted into `/usr/share/nginx/html/` by the
`ReleaseBinding`) as plain static files.

#### `index.html`

```html
<!DOCTYPE html>
<html>
  <head>
    <meta charset="utf-8" />
    <title>Todo</title>
    <script src="/env-config.js"></script>
  </head>
  <body>
    <div id="root"></div>
    <script type="module" src="/assets/index.js"></script>
  </body>
</html>
```

`<script src="/env-config.js">` is **synchronous** — the bundle is
guaranteed to see `window._env_` populated before any module runs.

#### `src/env.ts`

```ts
type Env = {
  API_BASE_URL: string;
  THUNDER_URL: string;
  THUNDER_CLIENT_ID: string;
  THUNDER_REDIRECT_URI: string;
  THUNDER_SCOPES: string;
  THUNDER_AFTER_SIGN_IN_URL: string;
  SUPPORT_EMAIL: string;
  FEATURE_NEW_DASHBOARD: boolean;
};

declare global {
  interface Window { _env_: Env }
}

export const env: Env = window._env_;
```

#### `src/auth.ts`

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

export async function signIn() {
  await userManager.signinRedirect();
}

export async function handleCallback() {
  return userManager.signinRedirectCallback();
}

export async function getAccessToken(): Promise<string | null> {
  const user = await userManager.getUser();
  return user?.access_token ?? null;
}
```

#### `src/api.ts`

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

#### What the agent MUST NOT emit

- ❌ `.env` file (with `VITE_*` or otherwise).
- ❌ `import.meta.env.VITE_*` references.
- ❌ Any URL literal pointing at Thunder, the API, the gateway, or any in-cluster Service FQDN.
- ❌ `internalProxyPass`, `proxy_pass` to in-cluster services.
- ❌ `envsubst`, `/etc/nginx/templates/`, `NGINX_ENVSUBST_*`.
- ❌ Custom `/docker-entrypoint.d/` scripts.

#### What happens after the agent commits

1. CI builds the image (OC build pipeline triggered by PR merge).
2. Image pushed to the configured registry.
3. BFF dispatches a `ReleaseBinding` to OC for each target env (see §3.5).

### 3.4 Coding worker — API

#### Files emitted

| Path | Purpose |
|---|---|
| `workload.yaml` | endpoints (external, schemaFile link) + `configurations.env` |
| `openapi.yaml` | OpenAPI 3 contract (linked by Workload) |
| `Dockerfile` | language-specific build |
| `main.go` (or equivalent) | reads `X-User-Id` from header |
| `go.mod`, `go.sum`, ... | language files |

#### `workload.yaml`

```yaml
apiVersion: openchoreo.dev/v1alpha1
metadata:
  name: todo-api

endpoints:
  http:
    type: HTTP
    port: 9090
    basePath: /
    visibility:
      - external           # gateway-fronted; api-configuration trait handles CORS + JWT
    schemaFile: openapi.yaml

configurations:
  env:
    - name: DB_PATH
      value: /data/todos.db
```

The agent does NOT write the `traits:` block — that's a `Component`
CR field, emitted by the BFF from architect intent.

#### `main.go` (header read — unchanged)

```go
func mustUserID(w http.ResponseWriter, r *http.Request) (string, bool) {
    uid := r.Header.Get("X-User-Id")
    if uid == "" {
        http.Error(w, `{"error":"missing X-User-Id"}`, http.StatusUnauthorized)
        return "", false
    }
    return uid, true
}
```

The `api-configuration` trait's `jwt-auth` v1 policy validates the
JWT at the gateway and injects `X-User-Id` via claim mapping. The
service reads the header.

#### What the agent MUST NOT emit

- ❌ Any `/auth/*` endpoint, login flow, token-validation library import.
- ❌ Direct calls to Thunder.
- ❌ Hand-written CORS handling. The gateway's `api-configuration` trait owns CORS.

#### What happens after the agent commits

Same as §3.3: CI builds, BFF dispatches a Component (with `traits:`)
+ ReleaseBinding (with `traitEnvironmentConfigs`) per env (§3.5).

### 3.5 BFF — dispatch time

The BFF is the platform engineer. It emits the platform half
deterministically from architect intent + project state + env config.

#### Per-project one-time bootstrap: `EnsureProjectOAuthClient`

On the **first web-app dispatch in a project**, declare an OAuth
client in Thunder's `thunder-idp` ConfigMap. Entry shape (mirroring
`wso2cloud-deployment` `thunder-idp.yaml:369-383`):

```yaml
id: <stable-uuid-derived-from-project>
ou_id: <ou-id-from-cluster-config>
name: "<project-name>"
inbound_auth_config:
  - type: oauth2
    config:
      client_id: "<project-name>"   # e.g. "todo-app"
      redirect_uris:
        - "<spa-gateway-url>"
        - "<spa-gateway-url>/callback"
      grant_types: [authorization_code, refresh_token]
      response_types: [code]
      pkce_required: true
      public_client: true
      token_endpoint_auth_method: none
      token:
        issuer: "platform_idp"
        access_token:
          validity_period: 3600
          user_attributes: [given_name, family_name, email, name, sub]
        id_token:
          validity_period: 3600
          user_attributes: [given_name, family_name, email, name]
allowed_user_types: [Customer]
```

Store the `client_id` in BFF project state (`Project.OAuthClientID`).
On project delete: revoke the ConfigMap entry (Open Question §6.2).

#### Per-component, per-environment emission — service

`Component` CR:

```yaml
apiVersion: openchoreo.dev/v1alpha1
kind: Component
metadata:
  name: todo-api
spec:
  owner: { projectName: todo-app }
  componentType: { kind: ClusterComponentType, name: deployment/service }
  workflow: ...

  traits:                            # emitted iff exposesAPI.managed: true
    - instanceName: todo-api-http    # BFF-chosen, stable per (component, endpoint)
      kind: ClusterTrait
      name: api-configuration
      parameters:
        endpointName: http
```

`ReleaseBinding` per env:

```yaml
apiVersion: openchoreo.dev/v1alpha1
kind: ReleaseBinding
metadata:
  name: todo-api-development
spec:
  environment: development
  owner: { projectName: todo-app, componentName: todo-api }

  traitEnvironmentConfigs:
    todo-api-http:
      cors:
        enabled: true
        allowedOrigins:
          # Computed: every external web-app component in this project.
          - http://todo-web-todo-web-development-todo-app-<hash>.openchoreoapis.localhost:19080
          - http://localhost:3000        # local dev (toggle in env config)
        allowedMethods: [GET, POST, PUT, DELETE, PATCH, OPTIONS]
        allowedHeaders: [Authorization, Content-Type, Accept, Origin]
        allowCredentials: true
      jwtAuth:
        enabled: true                     # from exposesAPI.auth: end-user-required
        forwardedTokenHeader: Authorization
```

#### Per-component, per-environment emission — webapp

`Component` CR:

```yaml
apiVersion: openchoreo.dev/v1alpha1
kind: Component
metadata:
  name: todo-web
spec:
  owner: { projectName: todo-app }
  componentType: { kind: ClusterComponentType, name: deployment/web-application }
  workflow: ...
  # No traits. SPAs are plain web-applications — matching
  # cloud-console / sample-console / app-factory-console in
  # wso2cloud-deployment, which carry no api-configuration trait.
```

`ReleaseBinding` per env:

```yaml
apiVersion: openchoreo.dev/v1alpha1
kind: ReleaseBinding
metadata:
  name: todo-web-development
spec:
  environment: development
  owner: { projectName: todo-app, componentName: todo-web }

  workloadOverrides:
    container:
      files:
        - key: env-config.js
          mountPath: /usr/share/nginx/html/
          value: |
            window._env_ = {
              // Sibling API — context derived from api-configuration trait formula
              API_BASE_URL: "http://development-todo-app.openchoreoapis.localhost:19080/todo-api-http",
              // Thunder OIDC — per-project client from EnsureProjectOAuthClient
              THUNDER_URL: "http://platform-idp.openchoreoapis.localhost:19080",
              THUNDER_CLIENT_ID: "todo-app",
              THUNDER_REDIRECT_URI: "http://todo-web.openchoreoapis.localhost:19080/callback",
              THUNDER_SCOPES: "openid profile email",
              THUNDER_AFTER_SIGN_IN_URL: "http://todo-web.openchoreoapis.localhost:19080",
              // App-config defaults from agent's workload.yaml configurations.env
              SUPPORT_EMAIL: "support@example.com",
              FEATURE_NEW_DASHBOARD: false
            };
```

#### URL formulas

| Value | Formula | Source |
|---|---|---|
| Service API URL | `http://<env>-<ns>.openchoreoapis.localhost:19080/<comp>-<endpoint>` | RestApi `context` (`api-configuration.yaml:210`) + gateway host |
| SPA URL | `http://<spa-dns-label>.openchoreoapis.localhost:19080` | `deployment/web-application` HTTPRoute |
| Thunder URL | `http://platform-idp.openchoreoapis.localhost:19080` | per-cluster BFF env config |

#### Sibling-derived CORS — dispatch rule

`cors.allowedOrigins` is computed at dispatch time:

```
origins = []
for each web-app component in same project:
  origins.append(compute_spa_url(web_app, environment))
if local_dev_allowed:
  origins.append("http://localhost:3000")
```

**Trigger rule:** any dispatch in a project re-emits **all sibling
API `ReleaseBinding`s** in that project (not just the changed
component's). Otherwise stale `allowedOrigins` silently break
preflight for newly added SPAs.

**Files to change:**
- `asdlc-service/internal/services/dispatch_service.go` — drive emission.
- `asdlc-service/internal/services/release_binding_service.go` (new) — materialise the bindings.
- `asdlc-service/internal/services/thunder_app_service.go` (new) — `EnsureProjectOAuthClient` + revoke.

### 3.6 OC reconciliation

OC controllers materialise:

| Resource | When | Notes |
|---|---|---|
| `Deployment` | always | Pod template with `env-config.js` mounted at `/usr/share/nginx/html/` for webapps |
| `Service` | always | ClusterIP per endpoint |
| `HTTPRoute` | always | External routing |
| `Backend` (kgateway) | services with trait | Points HTTPRoute at WSO2 router |
| `RestApi` (WSO2) | services with trait | Registers API with cors + jwt-auth policies |

For service components with `api-configuration` attached, the trait
**patches** the HTTPRoute `backendRef` to route through the WSO2
router and rewrites the URL prefix to the API context
(`/<env>-<ns>-<comp>-<endpoint>`).

### 3.7 Request runtime

| Request | Handled by | Action |
|---|---|---|
| `GET /` to SPA | nginx in SPA pod | Serves `index.html` |
| `GET /env-config.js` | nginx in SPA pod | Serves the literal file from `ReleaseBinding` |
| `GET /assets/index.js` | nginx in SPA pod | Serves the static bundle |
| PKCE → Thunder | bundle (`oidc-client-ts`) | Browser-driven, cross-origin to `env.THUNDER_URL` |
| `POST /todos` to API | gateway + service | `jwt-auth` v1 validates JWT, strips `Authorization`, injects `X-User-Id` from `sub` claim; CORS echoes SPA origin |
| 401 from API | bundle | `signIn()` → redirect to Thunder |

---

## 4. Implementation phases

Each phase is self-contained and reversible. Verification is
end-to-end via Playwright + log assertions.

The verification gate baseline (run before every phase to establish
"works" as the comparison point):

```bash
bash deployments/scripts/setup.sh
bash deployments/scripts/start.sh
# Open http://localhost:8090
# Sign in admin / admin
# Create a project, run agent flow end-to-end
# Confirm both components deploy
# Open the generated SPA, sign in, create a todo, reload
```

### Phase 0 — Foundations (no behaviour change)

**Goal:** bring in wso2cloud-deployment primitives in shadow mode.

**Code changes:**

1. **Adopt `api-configuration.yaml` from wso2cloud-deployment.**
   Replace `deployments/manifests/api-platform/api-configuration-trait.yaml`.
   Diff and reconcile schema differences (the current trait at line
   260 has `jwt-auth v1` with claim mapping `sub → x-user-id` that
   the dry run depends on — preserve that). Verify what
   `dispatch_service.go` emits today still validates.
2. **Verify ASDLC CCTs implement `configurations.*` contract.**
   Already landed on `cleanup-auth` (`deployments/scripts/setup-asdlc.sh`).
   Cross-check that both `service` and `web-application` CCTs
   reference `toContainerEnvFrom / toContainerVolumeMounts / toVolumes
   / toContainerEnvs(dependencies)` on the container template, plus
   four `forEach` resources (`env-config`, `file-config`,
   `secret-env-external`, `secret-file-external`).

**E2E test (Playwright):** `tests/e2e/phase-0-smoke.spec.ts`

- Run the baseline verification gate.
- Confirm an existing webapp+API project still deploys and works as before.
- Sign in, create a todo, reload, verify persistence.

**Log assertions:** `tests/lib/log-asserts.ts` greps:

- `docker compose logs asdlc-api` — no errors mentioning unknown trait fields.
- `kubectl get restapi -A` — RestApi resources still emitted on `api.security: required` services.
- `kubectl get clustertrait api-configuration -o yaml | grep "name:"` — matches wso2cloud-deployment policies.

**Rollback:** revert the trait file; no other code touched.

### Phase 1 — Runtime config via `env-config.js`

**Goal:** image becomes portable across environments. SPA reads
runtime config from `window._env_`. Sibling-derived CORS, no more
`## Dependency endpoint resolved` channel.

**Code changes:**

3. **Update `SKILL.md`** — add "Runtime config via `window._env_`"
   section with copy-pasteable `index.html` / `src/env.ts` / `src/api.ts`
   snippets. Drop the `/etc/nginx/templates/` discussion and the
   legacy build-time bake section. Drop the dependency-comment-harvest
   discussion (`SKILL.md:104-172`).
4. **Update agent webapp generation** (`SKILL.md` + worker prompt):
   - Stock `nginx/default.conf` (static, no proxy).
   - `<script src="/env-config.js">` in `index.html` before bundle.
   - `src/env.ts` typing `window._env_`.
   - `${env.API_BASE_URL}` in `src/api.ts`.
   - Drop `.env`. Drop `VITE_*` env vars from generated code.
   - Add `configurations.env` defaults to `workload.yaml` where safe.
5. **BFF emits `env-config.js`** into
   `ReleaseBinding.workloadOverrides.container.files`:
   - `API_BASE_URL` computed from RestApi context formula.
   - Other keys from `configurations.env` defaults + per-env BFF overrides.
6. **BFF computes `cors.allowedOrigins`** from sibling web-app
   components. Implement the re-emit-all-siblings rule (§3.5).
7. **Delete `AnnounceDependencyDeployed` comment posting** in
   `dispatch_service.go`. URL flows through `ReleaseBinding`.
8. **Drop `FEATURE_EMIT_API_TRAIT`** (`trait_sync.go:50, 84-89`).
9. **Architect prompt** — drop the "DO NOT use envsubst /
   configurations.env" line. Replace with one sentence on
   `window._env_`.
10. **Move Secret Santa hardcoded URL** out of the architect prompt
    (`prompt.ts:69-78`) into a BFF-side catalog (DB table or
    in-process config), keyed by intent.
11. **Tech-lead prompt** — drop `.env BEFORE npm run build` bullets.

**E2E test (Playwright):** `tests/e2e/phase-1-smoke.spec.ts`

- Run baseline.
- Create a fresh project; drive the agent flow to deploy a SPA + API.
- Open SPA URL; assert `window._env_.API_BASE_URL` is non-empty
  (`page.evaluate(() => window._env_)`).
- Assert the SPA fetches the API and renders the todo list.
- **Portability test:** edit the ReleaseBinding to change the
  `API_BASE_URL` value (e.g. point at a mock); re-apply with
  `kubectl apply`; force-restart the SPA pod; reload — assert
  `window._env_.API_BASE_URL` reflects the new value. The image was
  not rebuilt.
- **Sibling-CORS test:** add a second SPA to the same project via
  the agent flow; assert the API's `ReleaseBinding`
  `cors.allowedOrigins` now contains both SPA hostnames.

**Log assertions:**

- `docker compose logs asdlc-api` — contains line(s) like
  `emitting env-config.js (component=todo-web env=development API_BASE_URL=...)`.
- `docker compose logs asdlc-api` — does NOT contain
  `## Dependency endpoint resolved` (channel deleted).
- `kubectl get releasebinding todo-web-development -o yaml` — has
  `workloadOverrides.container.files[0].key: env-config.js` and a
  `value:` containing `window._env_ = {`.
- `kubectl exec <spa-pod> -- cat /usr/share/nginx/html/env-config.js`
  — matches the binding content.
- Browser console — no `import.meta.env` errors, no missing-var
  warnings.
- GitHub task issue — no `## Dependency endpoint resolved` comment.

**Rollback:** revert prompt + SKILL changes; revert BFF
`releaseBindingService` calls. Existing apps were not migrated; new
flow is on by default for new projects.

### Phase 2 — OIDC config to runtime, per-project OAuth clients

**Goal:** stop baking OIDC config into the bundle. PKCE stays in
SPA; the BFF writes Thunder config into `env-config.js` and declares
a per-project OAuth client in Thunder. No new gateway policy, no
upstream WSO2 contribution.

**Code changes:**

12. **Architect schema change** — replace `auth: { kind: ... }` with
    `callerIdentity: { mode: ... }` (`schema.ts:84-100`); replace
    `api: { security: ... }` with
    `exposesAPI: { auth, userContext }`.
13. **`EnsureProjectOAuthClient`** — new BFF service
    (`thunder_app_service.go`). On first web-app dispatch in a
    project, declare an OAuth client in Thunder's `thunder-idp`
    ConfigMap (CLOUD_CONSOLE-shape; see §3.5). Store `client_id` in
    BFF project state.
14. **BFF extends `env-config.js`** with `THUNDER_URL`,
    `THUNDER_CLIENT_ID`, `THUNDER_REDIRECT_URI`, `THUNDER_SCOPES`,
    `THUNDER_AFTER_SIGN_IN_URL`. Values per env.
15. **Delete `announceOIDCConfigIfApplicable`** and all callers
    (`dispatch_service.go:919-1009`). The issue-comment channel is
    gone.
16. **Replace `RegisterUserAppRedirectURI`** (`dispatch_service.go:589-656`)
    with `EnsureProjectOAuthClient`. Stop appending to
    `asdlc-console-client`.
17. **Drop `USER_APPS_OIDC_*` config plumbing** (`config.go:152-157`,
    `config_loader.go:54-57`). Thunder URL + scopes come from
    per-cluster BFF config; client_id is per-project.
18. **Agent webapp generation:**
    - Keep `oidc-client-ts` in the bundle. Wire to `env.THUNDER_*`
      (template in §3.3: `src/auth.ts`).
    - Delete `.env` from the generated SPA entirely.
    - Delete the `internalProxyPass` / `/oidc/token` proxy block
      from `nginx/default.conf` — Thunder is reachable cross-origin
      on its own gateway hostname.
19. **`SKILL.md`** — rewrite the OIDC section (lines 609-863) down
    to ~30 lines: "platform writes Thunder config into `window._env_`;
    use `oidc-client-ts` against `env.THUNDER_*`; PKCE stays in the
    bundle." Keep the redirect-on-401 idiom.
20. **Tech-lead prompt** — delete the OIDC block (lines 294-360).
21. **Drop architect prompt's `auth.kind: oidc-spa` enforcement
    block** (`prompt.ts:112-129`).
22. **Verify Thunder cross-origin reachability.** Confirm Thunder's
    `oauth2/*` endpoint accepts cross-origin requests from SPA
    gateway hostnames after the kgateway upgrade. If not, attach
    `api-configuration` to Thunder's external endpoint with allowed
    origins. **Spike this before starting Phase 2** (Open Question
    §6.1).

**E2E test (Playwright):** `tests/e2e/phase-2-smoke.spec.ts`

- Run baseline.
- Create a fresh project; drive the agent flow.
- Open SPA URL **unauthenticated**:
  - Assert navigation to `env.THUNDER_URL/oauth2/authorize?...`
    (with `client_id=<project-name>`, `code_challenge=...`,
    `response_type=code`).
- Sign in `admin / admin` at Thunder.
- Assert redirect to `<spa-url>/callback?code=...`.
- Assert SPA exchanges code (network: POST to `<thunder>/oauth2/token`).
- Assert signed-in landing page shows the user.
- Create a todo; assert the network request to the API includes
  `Authorization: Bearer <jwt>`.
- Open a second incognito context; sign in as a different user;
  create a todo; assert the original session does not see the
  second user's todo.

**Log assertions:**

- `docker compose logs asdlc-api` — does NOT contain
  `OIDC client provisioned` (channel deleted).
- `docker compose logs asdlc-api` — contains
  `EnsureProjectOAuthClient project=todo-app` on first SPA dispatch.
- GitHub task issue — no `## OIDC client provisioned` comment.
- `kubectl get cm -n thunder thunder-idp -o yaml` — contains an
  entry with `client_id: todo-app`, `pkce_required: true`,
  `public_client: true`.
- `kubectl get releasebinding todo-web-development -o yaml` —
  `env-config.js` contains `THUNDER_URL`, `THUNDER_CLIENT_ID`, etc.
- Browser network panel — `/oauth2/authorize` request goes
  cross-origin to `env.THUNDER_URL`, succeeds. `/oauth2/token`
  exchange succeeds (no `/oidc/token` same-origin proxy hit).
- SPA bundle — does NOT contain `internalProxyPass` (`grep` the
  built bundle).

**Rollback:** feature-flag the new schema fields at project level;
keep architect's old fields as deprecated aliases for one release.
Per-project OAuth clients are additive — existing shared-client
SPAs continue working until cut over.

### Phase 3 — Documentation cleanup

**Goal:** zero traces of the old patterns in the docs.

**Code changes:**

23. **`CLAUDE.md`** — reflect the new generated app shape.
24. **`docs/design/architecture.md`, `oauth-protected-webapp.md`,
    `agent-orchestrator.md`** — update to describe the
    runtime-config flow.
25. **`requirements/`** — update any scenarios that reference the
    old `.env` / `## OIDC client provisioned` flow.

**Test gate:** none — no behaviour change. CI link-check + spell-check.

**Rollback:** trivial git revert.

### Cross-phase verification helpers

Both helpers live under `tests/`:

- `tests/lib/log-asserts.ts` — `expectLog(service, pattern)` /
  `expectNotInLog(service, pattern)`. Wraps `docker compose logs
  <service>` + regex; usable from any Playwright spec.
- `tests/lib/kube-asserts.ts` — `expectResource(kind, name, jsonpath,
  matcher)`. Wraps `kubectl get <kind> <name> -o jsonpath=...`.

Each phase's spec runs in CI as a separate test stage gated on the
previous phase passing.

---

## 5. Per-owner checklist

Cross-reference with the phase steps. `[x]` already landed.

### OpenChoreo / WSO2 API Platform (verify or contribute)

- [ ] Phase 0: Confirm `workloadOverrides.container.files` materialises arbitrary content at the declared mountPath. **Verified** via dry run on `cleanup-auth`.
- [ ] Phase 0: Adopt `api-configuration.yaml` from wso2cloud-deployment (or align our copy with theirs).
- [ ] Phase 2: Verify Thunder's external endpoint accepts SPA cross-origin requests. If not, attach `api-configuration` to Thunder's `oauth2/*` endpoint.

### BFF (`asdlc-service`)

- [x] Phase 0: ASDLC CCTs (`service` + `web-application` in `setup-asdlc.sh`) opt into the OC `configurations.*` contract. Landed on `cleanup-auth`.
- [ ] Phase 0: replace `api-configuration-trait.yaml` with the wso2cloud-deployment version.
- [ ] Phase 1: add `release_binding_service.go` materialising a `ReleaseBinding` per `(component, environment)`.
- [ ] Phase 1: emit `env-config.js` content into `ReleaseBinding.workloadOverrides.container.files` for every webapp.
- [ ] Phase 1: compute `API_BASE_URL` from the `api-configuration` trait's context formula.
- [ ] Phase 1: copy `configurations.env` defaults from agent's `workload.yaml` into `env-config.js` keys for SPAs.
- [ ] Phase 1: compute `cors.allowedOrigins` deterministically from sibling web-app components; implement the re-emit-all-siblings rule.
- [ ] Phase 1: delete `AnnounceDependencyDeployed` comment posting.
- [ ] Phase 1: drop `FEATURE_EMIT_API_TRAIT` (`trait_sync.go:50, 84-89`).
- [ ] Phase 1: move Secret Santa URL to a BFF-side catalog.
- [ ] Phase 2: add `thunder_app_service.go` with `EnsureProjectOAuthClient` (declares per-project ConfigMap entry) + `RevokeProjectOAuthClient` (on project delete).
- [ ] Phase 2: extend `env-config.js` with `THUNDER_*` keys (per env).
- [ ] Phase 2: delete `announceOIDCConfigIfApplicable` and callers (`dispatch_service.go:919-1009`).
- [ ] Phase 2: drop `RegisterUserAppRedirectURI` (`dispatch_service.go:589-656`).
- [ ] Phase 2: drop `USER_APPS_OIDC_*` config (`config.go:152-157`, `config_loader.go:54-57`).
- [ ] Phase 2: do **not** emit `api-configuration` on SPA components.

### Architect (`agents/src/agents/architect/`)

- [ ] Phase 1: drop the "DO NOT use envsubst / configurations.env" line; replace with one sentence on `window._env_`.
- [ ] Phase 1: remove Secret Santa hardcode (`prompt.ts:69-78`).
- [ ] Phase 2: schema change — `auth: { kind: ... }` → `callerIdentity: { mode: ... }` (`schema.ts:84-100`).
- [ ] Phase 2: schema change — `api: { security: ... }` → `exposesAPI: { auth, userContext }`.
- [ ] Phase 2: prompt — remove the 700-char OIDC-SPA instructions string (`prompt.ts:47`).
- [ ] Phase 2: prompt — remove the `auth.kind: oidc-spa` enforcement block (`prompt.ts:112-129`).

### Tech-lead (`agents/src/agents/tech-lead/`)

- [ ] Phase 1: drop per-task `.env BEFORE npm run build` bullets (`prompt.ts:362-388`).
- [ ] Phase 2: delete the "Auth endpoints — IDP-delegated OIDC" block (`prompt.ts:294-360`).

### SKILL (`remote-worker/plugin/skills/asdlc/SKILL.md`)

- [ ] Phase 1: add "Runtime config via `window._env_`" section with copy-pasteable `index.html` + `src/env.ts` + `src/api.ts`.
- [ ] Phase 1: drop the build-time-bake section AND every reference to nginx templates / envsubst.
- [ ] Phase 1: delete the Dependency-endpoints comment-harvest section (lines 104-172).
- [ ] Phase 2: rewrite the OIDC-SPA section (lines 609-863) to ~30 lines: `oidc-client-ts` against `env.THUNDER_*`; PKCE stays in the bundle.
- [ ] Keep: the `X-User-Id` section for service components (lines 866-931).

### Coding worker — generated artefact contract

The agent emits these and only these, per §3.3 / §3.4.

**Service:**

- [ ] `workload.yaml` with `visibility: external`, `schemaFile: openapi.yaml`, `configurations.env` for app config. Endpoints in map form.
- [ ] `openapi.yaml`.
- [ ] `Dockerfile` + language artefacts.
- [ ] `main.go` (or equivalent) reads `X-User-Id` from headers; rejects 401 when missing.

**Webapp (after Phase 2):**

- [ ] `workload.yaml`: `visibility: external`, optional `configurations.env` for default runtime values. No `dependencies.endpoints`. No URL placeholders.
- [ ] `Dockerfile`: stock `nginx:alpine`, copies static bundle. No envsubst, no templates, no custom entrypoint.
- [ ] `nginx/default.conf`: pure static, no proxy block.
- [ ] `index.html`: loads `/env-config.js` synchronously before the bundle.
- [ ] `src/env.ts`: typed read of `window._env_`.
- [ ] `src/auth.ts`: `oidc-client-ts` UserManager wired to `env.THUNDER_*`.
- [ ] `src/api.ts`: `${env.API_BASE_URL}/...` calls; Bearer header from `getAccessToken()`. No `import.meta.env.VITE_*`.

---

## 6. Open questions (resolve before the phase that needs them)

1. **Phase 2 blocker — Thunder cross-origin reachability.**
   wso2cloud-deployment fronts Thunder at
   `platform-idp-<env>.gateway.<base>` via the API Platform gateway,
   which presumably handles CORS for SPA origins. Our local setup
   uses a same-origin proxy in the SPA pod (`/oidc/token` → in-cluster
   Thunder) as a workaround for a kgateway CORS bug (`SKILL.md:646-650`).
   Spike before Phase 2 starts: does Thunder's `oauth2/*` endpoint
   accept cross-origin requests after the kgateway upgrade, or do we
   need explicit CORS config on Thunder's HTTPRoute via an
   `api-configuration` trait?

2. **Phase 2 blocker — per-project OAuth client lifecycle.** When a
   project is deleted, the per-project Thunder ConfigMap entry leaks
   unless we add `RevokeProjectOAuthClient` to the project-delete
   path. Decision: add it in Phase 2 step 13. Don't ship Phase 2
   without the revoke hook.

3. **Defer — per-app audience scoping on `jwtAuth`.** Today any
   Thunder-signed token passes JWT validation at any API gateway.
   wso2cloud-deployment uses per-app `audience` claims. Out of scope
   for this refactor; track as follow-up.

4. **Defer — defense-in-depth JWT validation in services.**
   wso2cloud-deployment services validate JWT independently of the
   gateway. Out of scope for this refactor.

5. **Resolved — Asgardeo / external IDP.** IDP swap is a per-env
   `ReleaseBinding` change to `THUNDER_URL` / `THUNDER_CLIENT_ID` +
   a `gateway-config.yaml` keymanagers JWKS issuer update. Generator
   stays IDP-agnostic.

6. **Resolved — `oauth2-redirect` policy on `api-configuration`.**
   Verified against wso2cloud-deployment: no such policy exists.
   `api-configuration` emits exactly `cors`, `jwt-auth` v1,
   `basic-ratelimit`, `add-headers` (`api-configuration.yaml:214-218`).
   SPAs in wso2cloud-deployment carry no trait; PKCE happens in the
   bundle, fed by `window._env_`. This refactor follows that pattern.

---

## 7. Summary

The current pipeline conflates three concerns the LLM-driven layers
shouldn't have to know about:

- **What** the spec asks for → architect.
- **Where** runtime values come from → BFF emitting a `ReleaseBinding`,
  NOT the agent harvesting issue comments.
- **How** OIDC config reaches the SPA → BFF writing a per-env
  `env-config.js`, NOT a `.env` baked at build time.

The proposed flow keeps each concern in its proper layer:

- **Architect** emits intent only (`callerIdentity`, `exposesAPI`).
- **Coding agent** writes code + Workload + Dockerfile — a single
  image identical across all environments. The SPA still carries
  `oidc-client-ts` and drives PKCE, but reads its OIDC config from
  `window._env_`.
- **BFF** emits the `Component` CR (`traits:` block for services
  only, from architect intent), plus a per-env `ReleaseBinding`
  (`traitEnvironmentConfigs` for service policy,
  `workloadOverrides.container.files` for `env-config.js`,
  `workloadOverrides.container.env` for secrets). Also declares a
  per-project OAuth client in Thunder's `thunder-idp` ConfigMap.
- **OC controllers** reconcile to Deployment + Service + HTTPRoute +
  kgateway Backend + WSO2 RestApi.
- **WSO2 API Platform gateway** validates JWTs on API calls,
  enforces CORS, injects `X-User-Id`. (No OIDC termination — that
  stays in the SPA, matching wso2cloud-deployment.)
- **SPA** reads `window._env_` for both API URL and OIDC config;
  **API** only reads `X-User-Id`.

Two production-tested primitives from `wso2cloud-deployment` carry
the load: `api-configuration` ClusterTrait + `ReleaseBinding`
`workloadOverrides.container.files`. The BFF plays the
platform-engineer role manual in wso2cloud-deployment.

Rolled out in three phases (foundations → runtime config → gateway
auth) plus a docs cleanup phase. Each phase has a Playwright +
log-assertion gate that proves the change works before the next
phase starts.
