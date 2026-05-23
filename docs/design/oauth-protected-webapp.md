# OAuth-Protected Webapp Scenario

> **Superseded by [`auth-and-runtime-config-refactor.md`](auth-and-runtime-config-refactor.md).**
> That doc is the canonical specification for the OIDC-SPA flow. Highlights of
> what changed:
>
> - **Runtime config**: per-env values reach the SPA via `window._env_`
>   (BFF writes `env-config.js` into the SPA's ReleaseBinding at
>   `/usr/share/nginx/html/`). No nginx envsubst, no `/etc/nginx/templates/`,
>   no `window.__ENV__`, no `.env` file at build time.
> - **Per-project OAuth client**: `EnsureProjectOAuthClient` declares one
>   public PKCE-required client in Thunder per project (id = project name).
>   The legacy single shared `USER_APPS_OIDC_*` client is gone, along with
>   the `## OIDC client provisioned` issue-comment channel.
> - **Cross-origin token exchange**: Thunder's gateway returns proper CORS
>   headers, so the SPA POSTs `/oauth2/token` cross-origin directly — no
>   same-origin `/oidc/` nginx proxy, no `internalProxyPass`.
> - **Schema**: `auth.kind: oidc-spa` → `callerIdentity.mode: end-user`;
>   `api.security: required` → `exposesAPI.auth: end-user-required`. The
>   legacy fields remain as backwards-compat aliases.
>
> The historical narrative below is preserved for context. Anything it says
> about envsubst, `.env`, `## OIDC client provisioned`, or `window.__ENV__`
> is no longer the platform behaviour.

How App Factory generates a webapp + API where authentication is handled
end-to-end by the API Platform (gateway) and the IDP (Thunder), with **zero
manual configuration** from the user.

Companion to [`api-platform-integration.md`](api-platform-integration.md) —
that doc covers the trait + org publisher; this doc covers the user-facing
SPA login flow that closes the loop.

## End-to-end test goal

> A user creates a Todo project with authentication. App Factory generates,
> builds, and deploys the apps. The user opens the webapp URL, signs in via
> the platform IDP, and creates a todo. **At no point does the user touch
> OAuth client config, redirect URIs, JWT validation, or backend auth code.**

The same image deploys across dev/stage/prod (per-env OAuth client config
is injected at pod start, not baked in).

## Architecture

```
┌──────────────┐    1. /login redirect      ┌──────────────┐
│              │ ─────────────────────────▶ │              │
│   todo-web   │                            │   Thunder    │
│   (SPA)      │ ◀───── 2. code+id_token ── │   (IDP)      │
│              │                            │              │
└──────┬───────┘                            └──────────────┘
       │ 3. token exchange (PKCE)                  ▲
       │                                           │ JWKS
       │ 4. fetch /api/todo-api/todos              │
       │    Authorization: Bearer <jwt>            │
       ▼                                           │
┌──────────────────────────────────────────────────┴───┐
│  API Platform Gateway (validates JWT, injects        │
│  X-User-Id header from sub claim)                    │
└──────┬───────────────────────────────────────────────┘
       │ 5. forwarded with X-User-Id
       ▼
┌──────────────┐
│   todo-api   │   trusts X-User-Id; owns todos table
└──────────────┘
```

**Auth provisioning (one-time per project, automatic):**

- **Org publisher app** (`asdlc-publisher-<org>`) — already exists; for
  service-to-service tokens.
- **Project SPA client** (`asdlc-spa-<org>-<project>`) — NEW; public client
  with PKCE, redirect URIs synced from the discovered webapp HTTPRoute(s).
  Audience pinned to `asdlc-project-<projectID>`. Stored in a new
  `project_oauth_clients` table.

## Decisions

| Decision                | v2 target                                              | v1 (MVP, this branch)                                       |
|-------------------------|--------------------------------------------------------|-------------------------------------------------------------|
| OAuth client scope      | Per project (`asdlc-spa-<org>-<project>`)              | **Single shared client** — reuse existing `APP_FACTORY_CONSOLE` (already SPA-shaped with PKCE) |
| Redirect URI registration | BFF provisions on first webapp dispatch                | **Manual via `scripts/register-user-app-callback.sh`** after first deploy (Thunder has no REST API for inbound_auth_config edits; sqlite-patch from BFF is out of scope for v1) |
| SPA config delivery     | Runtime: nginx envsubst → `/env-config.js` → `window.__ENV__` | Same — runtime envsubst; the comment hands values to the agent which bakes them into `workload.yaml` env block |
| User identity to API    | Gateway injects `X-User-Id` from JWT `sub`             | **Backend decodes Bearer JWT itself** (gateway has already validated; no re-validation needed). Switching to gateway-injection requires `claimMappings` support in the `api-configuration` ClusterTrait template — submodule change + `platform-design-expert` review, deferred to v2 |
| API auth endpoints      | None — Thunder owns issuance; API has no `/auth/*`     | Same                                                        |

## Component shapes the architect must emit

```yaml
# components/todo-api/design.md
type: service
api:
  security: required   # → api-configuration trait, JWT enforced at gateway

# components/todo-web/design.md
type: web-app
dependsOn: [todo-api]
auth:
  kind: oidc-spa
  upstream: todo-api
```

The architect must NOT emit `/auth/login` or `/auth/register` in the API's
OpenAPI when this pattern is used.

## Stage-by-stage

### Requirements (business-analyst)
- Allow auth as a first-class feature ("users sign in"). Do not invent
  username/password UX — the platform owns identity.

### Architect
- **Schema** (`agents/src/agents/architect/schema.ts`): add
  `auth: { kind: "oidc-spa", upstream: string }` to `web-app` SlimComponent.
- **Prompt** (`agents/src/agents/architect/prompt.ts`):
  - Remove the "no OAuth, no SSO, no external IDPs" ban.
  - Remove the "fold `/auth/login` into the API service" rule for
    OAuth-shaped specs.
  - Add: when the spec implies users sign in, set `api.security: required`
    on the API AND `auth.kind: oidc-spa` on the SPA. API has no auth
    endpoints. SPA reads `window.__ENV__.OIDC_*` at runtime.
- **Classifier** (`security-classifier.ts`): keyword rubric unchanged; only
  the *consequence* changes (now also flips `auth.kind` on the SPA).

### Tech-lead
- Drop the "seed admin/admin123" treatment.
- For `web-app` with `auth.kind: oidc-spa`: scope bullet wiring the OIDC
  login flow via `window.__ENV__`, acceptance bullet for redirect-on-401
  and `Authorization: Bearer` on outbound fetches.
- For `service` with `api.security: required` whose sibling SPA is OIDC:
  scope bullet "no `/auth/*`; read user from `X-User-Id`".

### BFF (asdlc-service)
- New `ProjectIDPService` + `models.ProjectOAuthClient` + migration
  (mirrors `IDPService` / `OrganizationIDPProfile`).
- `EnsureProjectSPAClient(projectID, webappURL)` — idempotent; called on
  first dispatch of any web-app with `auth.kind: oidc-spa`.
- Inject env on the SPA's Workload container:
  `OIDC_ISSUER`, `OIDC_CLIENT_ID`, `OIDC_REDIRECT_URI`, `OIDC_SCOPES`.
- Extend `DesiredAPIConfigurationTraitWithIssuers` to emit
  `jwtAuth.audience: ["asdlc-project-<projectID>"]` and a
  `claimMappings: { sub: "X-User-Id" }` block.
- Trait Sync drift watcher: keep redirect URIs in Thunder in sync with the
  webapp's current HTTPRoute hostname(s).

### Platform (`wso2cloud-deployment` submodule)
- Verify the `api-configuration` ClusterTrait template supports
  `claimMappings`. If not, add it (sub → `X-User-Id`). **Requires
  `platform-design-expert` review** per CLAUDE.md.

### Coding-agent (`remote-worker/plugin/skills/asdlc/SKILL.md`)
- OIDC-SPA section: nginx `default.conf.template` renders `/env-config.js`;
  `index.html` loads it before the SPA bundle; sample PKCE flow snippet
  (oidc-client-ts or equivalent); `fetch` wrapper attaches
  `Authorization: Bearer`.
- Backend section: trust the gateway; read `X-User-Id`; no JWT parsing,
  no `/auth/*`, no CORS middleware (project pattern V4 still applies).

### Console (small)
- Architecture page chip: "OIDC client: `<client_id>`" when a web-app has
  `auth.kind: oidc-spa`.
- `POST /projects/{id}/oauth-client/rotate` mirror of the org publisher
  rotation endpoint.

## What ships in v1 (this branch)

Done:
1. ✅ Architect schema (`agents/src/agents/architect/schema.ts`) — adds
   `auth: { kind: "oidc-spa", upstream: string }` on web-app
   SlimComponent.
2. ✅ Architect prompt (`agents/src/agents/architect/prompt.ts`) — drops
   the "no OAuth, no SSO" ban; teaches the IDP-delegated pattern;
   updates the api.security rubric so OIDC-shaped specs flip both the
   API's `api.security` and the SPA's `auth.kind` together.
3. ✅ Tech-lead prompt (`agents/src/agents/tech-lead/prompt.ts`) —
   replaces the sample-user "seed admin/admin123" block with OIDC
   treatments for both the SPA (PKCE login, `window.__ENV__`) and the
   protected service (no `/auth/*`, JWT-from-Bearer).
4. ✅ SKILL.md (`remote-worker/plugin/skills/asdlc/SKILL.md`) — adds the
   OIDC-SPA section (nginx envsubst template, `/env-config.js`, PKCE
   flow reference) and the JWT-trusting-backend section.
5. ✅ Go DesignComponent (`asdlc-service/models/design.go`) — adds the
   `Auth` field so the BFF can read the design's auth block.
6. ✅ BFF config (`asdlc-service/config/{config.go,config_loader.go}`) —
   loads `USER_APPS_OIDC_ISSUER` / `_CLIENT_ID` / `_SCOPES`.
7. ✅ BFF dispatch hook (`asdlc-service/services/dispatch_service.go`) —
   posts `## OIDC client provisioned` on web-app issues with
   `auth.kind: oidc-spa`, with values from BFF config.
8. ✅ Env overlay (`deployments-v2/manifests/env-overlays/app-factory-api.yaml`) —
   points BFF at the existing `APP_FACTORY_CONSOLE` Thunder OAuth
   client (already SPA-shaped with PKCE).
9. ✅ Per-app redirect URI helper
   (`deployments-v2/scripts/register-user-app-callback.sh`) — appends
   a new webapp's `/callback` URL to `APP_FACTORY_CONSOLE.redirect_uris`
   via sqlite on the Thunder pod, run manually after first deploy.

## v2 follow-ups

1. **Per-project OAuth client** — provision `asdlc-spa-<org>-<project>`
   in Thunder via REST API on first webapp dispatch. Requires extending
   `clients/thundersvc` to create SPA-style apps (authorization_code +
   PKCE + redirect_uris). Persist client_id in a new
   `project_oauth_clients` table.
2. **Automated redirect URI sync** — replace the manual
   `register-user-app-callback.sh` step with BFF-driven Thunder app PUT
   on URL discovery (drift watcher reconciles).
3. **Gateway claim → header** — extend `api-configuration` ClusterTrait
   template (`wso2cloud-deployment` submodule) to map JWT `sub` → request
   header `X-User-Id`. Backend then never touches the JWT. Requires
   `platform-design-expert` review.
4. **Per-project audience pinning** — set `jwtAuth.audience` on the
   trait to `asdlc-project-<projectID>` so tokens issued for one project
   can't be replayed against another.
5. **Console UX** — chip on the architecture page when a webapp has
   `auth.kind: oidc-spa` showing client_id + a "Register callback URL"
   button (one-click trigger for the helper script equivalent).

## Reference artifact — validation log

The reference artifact lives at `manual-todo/` in the repo. Each line below
records a piece of the stack we've manually proven works against the live
cluster (so the AI prompts can confidently mirror it).

| Confirmed | Detail |
|-----------|--------|
| ✅ | nginx:alpine envsubst renders `${OIDC_*}` into `/etc/nginx/conf.d/default.conf` at pod start from pod env. `curl /env-config.js` returns `window.__ENV__ = { OIDC_ISSUER: "...", OIDC_CLIENT_ID: "APP_FACTORY_CONSOLE", ... };`. The `Dockerfile` is bare (`FROM nginx:alpine` + `COPY default.conf.template /etc/nginx/templates/...`), no CMD override needed. |
| ✅ | SPA reads `window.__ENV__.OIDC_*` from `/env-config.js` returned by an nginx `location = /env-config.js` block with `default_type application/javascript`. |
| ✅ | API Platform gateway validates JWT via the `api-configuration` ClusterTrait when `api.security: required` is set. Unauthenticated calls return `401 {"error":"Unauthorized","message":"Authentication failed."}`. |
| ✅ | Project + Component + Workload CRs deployable by hand: `kubectl apply -f project.yaml; component-*.yaml; workload-*.yaml`. OC autoDeploy creates RenderedRelease → ReleaseBinding → pod from the Workload spec. |
| ✅ | SPA Workload baked the gateway URL into env (`API_BASE_URL`); SPA's `app.js` reads `window.__ENV__.API_BASE_URL` and uses absolute URLs for `fetch`. No CORS middleware in the SPA; the gateway's CORS policy lets the cross-origin call through. |
| _pending_ | `register-user-app-callback.sh` appends the SPA's URL to `APP_FACTORY_CONSOLE.redirect_uris` and reconciles Thunder CORS — needs browser test |
| _pending_ | Authorization-Code + PKCE against Thunder's `/oauth2/authorize` + `/oauth2/token` returns a JWT — needs browser test |
| _pending_ | Go backend reads `Authorization: Bearer` from the validated request, decodes the JWT payload (no re-validation), extracts `sub`, and keys data on it — needs end-to-end test |

**Key bugs discovered + fixed while building this:**

1. **`api-configuration` ClusterTrait emitted `version: "v0"` for cors/jwt-auth policies**, but the api-platform gateway loaded `v1.0.1`/`v1.0.2`. Symptom: RestApi creation failed with `policy 'cors' major version 'v0' not found in loaded policy definitions`. The trait template needs to emit `v1`. Fixed at `deployments-v2/wso2cloud-deployment/wso2cloud-local/domains/platform/namespaces/wso2cloud/definitions/traits/api-configuration.yaml` (also renamed `add-headers` → `set-headers` to match the loaded policy name). **This is a platform-level fix that should be sent to `platform-design-expert` and upstreamed to wso2cloud-deployment.**

2. **ReleaseBinding `traitEnvironmentConfigs` must be patched manually for hand-applied Components.** When the BFF dispatches a component it calls `TraitSyncService.SyncComponentTraits` which writes `traitEnvironmentConfigs.<instance>` on every ReleaseBinding. Without it, the trait's `policies` expression evaluates to `[]` (no policies → gateway has no chain → 404). Manual recipe (see `manual-todo/`): `kubectl patch releasebinding <comp>-development --type=merge -p '{"spec":{"traitEnvironmentConfigs":{"<comp>-http":{"cors":{"enabled":true},"jwtAuth":{"enabled":true,"issuers":[],"audience":[]}}}}}'`.

3. **Thunder helm-chart `0.28.0` added a `consent` DB**, which expects a `consent-db-password` key in the `thunder-db-credentials` secret. Pre-existing clusters miss this key → CreateContainerConfigError on the new pod, HelmRelease stalls. Workaround: `kubectl patch secret -n thunder thunder-db-credentials --type=merge -p='{"data":{"consent-db-password":"YXNndGh1bmRlcg=="}}'` (base64 of `asgthunder`). **Upstream a fix to the chart or a setup.sh migration step.**

4. **`Workload` CRD `spec.container.env` uses `key:` not `name:`** — small but easy-to-miss vs. the k8s Pod spec. Working shape: `env: [{key: "OIDC_ISSUER", value: "http://..."}]`.

5. **Thunder 0.28.0 changed the `APP_OAUTH_INBOUND_CONFIG` schema** — the old columns `CLIENT_ID`, `CLIENT_SECRET`, `OAUTH_CONFIG_JSON` are gone; the new schema is `(DEPLOYMENT_ID, APP_ID, OAUTH_CONFIG)` with the client_id absent from the JSON entirely (the `APPLICATION.ID` UUID *is* the client_id). This breaks:
   - `deployments-v2/scripts/lib/asdlc.sh::register_console_redirect_uri` (uses old `OAUTH_CONFIG_JSON` column → fails silently)
   - `register_service_oauth_clients` (INSERTs columns that no longer exist)
   - The layer-2/thunder.yaml seed script for `APP_FACTORY_CONSOLE` (uses old field names) — and so the console OAuth app is never seeded
   - The manual `register-user-app-callback.sh` we just wrote (uses old `OAUTH_CONFIG_JSON` column)

   This is the **blocker for completing the manual E2E**: with no `APP_FACTORY_CONSOLE` client in Thunder, the SPA's `/oauth2/authorize` call fails. Two fix paths:
   - **(short term)** Rewrite the sqlite-patch helpers to use the new schema (`OAUTH_CONFIG` column, client_id = APP_ID), and create our user-apps OAuth client by INSERTing into `APPLICATION` + `APP_OAUTH_INBOUND_CONFIG`. Requires figuring out how `APPLICATION.AUTH_FLOW_ID` etc. resolve (FK to FLOW table).
   - **(better)** Drive client creation through Thunder's REST API `/applications` POST instead of sqlite. Requires a system access token (the asdlc-system-client app, which itself uses the broken seed path).

6. **Manual `Component` deploys need a `ReleaseBinding.spec.traitEnvironmentConfigs` patch** to populate the trait config — see bug 2 above. Manual recipe:
   ```bash
   kubectl patch releasebinding <comp>-development --type=merge \
     -p '{"spec":{"traitEnvironmentConfigs":{"<comp>-http":{"cors":{"enabled":true},"jwtAuth":{"enabled":true,"issuers":[],"audience":[]}}}}}'
   ```

   And after patching the trait template (bug 1), trigger re-render:
   ```bash
   kubectl delete renderedrelease -n default <comp>-development
   ```

**Current state of the manual artifact:**
- `manual-todo/todo-api` deploys, gateway protects it, unauthenticated returns 401. ✅
- `manual-todo/todo-web` deploys, serves SPA, `/env-config.js` renders correct values, `index.html` reachable. ✅
- Browser login flow blocked at Thunder side because `APP_FACTORY_CONSOLE` OAuth client is not seeded in this Thunder version (bug 5).
- Next step before any AI-flow work: fix bug 5 by writing a Thunder-schema-aware client-registration helper (in `register-user-app-callback.sh` and/or the `asdlc.sh` setup library).

Each row flips to ✅ as the corresponding piece is verified end-to-end.
Failures + fixes get recorded inline (e.g. "Thunder rejects `*` in
redirect_uris — must enumerate concrete URLs").

## ✅ Working recipe (verified end-to-end against the live cluster)

This is the **exact** set of artifacts + platform configuration that
together produce a working OIDC-protected webapp + API. The AI prompts +
SKILL must reproduce this shape verbatim. Source at `manual-todo/`.

### Stack of fixes the platform itself needs (one-time, NOT generated)

These are platform-side and not in scope for what the AI emits — they
should land in setup.sh / wso2cloud-deployment (separate workstream,
upstream review required):

1. **Thunder chart 0.28 → 0.34.** 0.28's storage layer silently drops
   string `client_id` values, breaking the entire "client lookup by
   stable string id" assumption baked into the BFF and console.
   `deployments-v2/scripts/thunder-upgrade.sh` does this in-place
   (patches HelmRelease chart version + image tag, wipes PVC, ensures
   `configuration.consent` block exists for sqlite). Live patch:
   `kubectl patch hr/thunder -n thunder --type=json -p='[{"op":"replace","path":"/spec/chart/spec/version","value":"0.34.0"},{"op":"add","path":"/spec/values/image","value":{"repository":"ghcr.io/asgardeo/thunder","tag":"0.34.0","pullPolicy":"IfNotPresent"}},{"op":"add","path":"/spec/values/configuration/consent","value":{"enabled":true,"baseUrl":"http://localhost:9090/api/v1","timeout":5,"maxRetries":3,"server":{"port":9090,"hostname":"localhost"},"database":{"type":"sqlite","sqlitePath":"repository/database/consentdb.db","sqliteOptions":"_pragma=journal_mode(WAL)&_pragma=cache_size(-16000)"}}}]'`.

2. **Thunder OAuth seed (camelCase REST against running pod with
   `THUNDER_SKIP_SECURITY=true`).** Required apps (clientId → grant):
   - `asdlc-system-client` (client_credentials) + Administrator role
   - `asdlc-bff-to-git-service`, `asdlc-bff-to-agents-service` (client_credentials)
   - `openchoreo-workload-publisher-client`, `openchoreo-observer-resource-reader-client` (client_credentials)
   - **`asdlc-console-client`** (authorization_code + PKCE + public; redirect_uris pre-include `http://localhost:19080`, `http://localhost:19080/callback`, and a per-user-app entry added at dispatch time — see step 5 below).
   - User schema `openchoreo-user` + at least one user `admin@openchoreo.dev / Admin@123` for browser sign-in.
   - **Mechanism**: `kubectl set env deployment/thunder-deployment -n thunder THUNDER_SKIP_SECURITY=true`, run REST POSTs from inside pod via `kubectl exec ... curl http://localhost:8090/...`, then `kubectl set env deployment/thunder-deployment THUNDER_SKIP_SECURITY-` to revert. Full payloads are in `manual-todo/k8s/notes.md` and `deployments/single-cluster/values-thunder.yaml` (use as reference for camelCase shapes).

3. **`api-configuration` ClusterTrait template — three fixes:**
   (a) Policy versions `v0` → `v1` (loaded versions are cors v1.0.1,
       jwt-auth v1.0.2, basic-ratelimit v1.0.2).
   (b) Rename `add-headers` → `set-headers` (loaded policy name).
   (c) **Emit `claimMappings` on jwt-auth policy** so the gateway
       injects user identity headers downstream. Without this the
       gateway strips the Authorization header and the backend has no
       way to identify the caller.
   Final policies expression:
   ```
   + (environmentConfigs.jwtAuth.enabled ? [{"name": "jwt-auth", "version": "v1", "params": {"claimMappings": {"sub": "x-user-id", "username": "x-user-name", "ouHandle": "x-user-ou"}}}] : [])
   ```
   File: `deployments-v2/wso2cloud-deployment/wso2cloud-local/domains/platform/namespaces/wso2cloud/definitions/traits/api-configuration.yaml`.
   Live patch + force re-render: `kubectl patch clustertrait api-configuration --type=json -p ...; kubectl delete renderedrelease -n default <comp>-development`.

4. **Thunder HTTPRoute needs a CORS filter** so `/oauth2/token`
   preflights succeed. kgateway's CORS filter handles OPTIONS itself;
   without it Thunder returns `405 Method Not Allowed` on preflight
   and the actual POST response body comes back empty to the browser
   even though Thunder responds correctly to direct curl. Live patch:
   ```bash
   kubectl patch httproute -n thunder thunder-httproute --type=json -p='[{"op":"add","path":"/spec/rules/0/filters","value":[{"type":"CORS","cors":{"allowOrigins":["http://*.openchoreoapis.localhost:19080","http://localhost:19080"],"allowMethods":["GET","POST","PUT","PATCH","DELETE","OPTIONS"],"allowHeaders":["Content-Type","Authorization","Accept","Origin"],"allowCredentials":true,"maxAge":3600}}]}]'
   ```

5. **Per-webapp redirect URI registration on `asdlc-console-client`** at
   dispatch time. Two URIs to append: `<webapp-origin>/callback` and
   `<webapp-origin>`. Implementation: BFF GETs `/applications/{id}`,
   appends to `inboundAuthConfig[0].config.redirectUris`, PUTs back.
   No sqlite. See `register-user-app-callback.sh` (now obsolete — needs
   rewriting against Thunder REST, not sqlite). The Workload itself
   carries the webapp's external URL via OC's HTTPRoute discovery, same
   path as `## Dependency endpoint resolved`.

6. **`ReleaseBinding.spec.traitEnvironmentConfigs.<comp>-http` must be
   patched on every protected component.** Default-empty configs
   silently produce an empty `policies: []` list (gateway has no
   routes and returns 404). Recipe:
   ```bash
   kubectl patch releasebinding <comp>-development --type=merge \
     -p '{"spec":{"traitEnvironmentConfigs":{"<comp>-http":{"cors":{"enabled":true},"jwtAuth":{"enabled":true,"issuers":[],"audience":[]}}}}}'
   ```
   This is what `TraitSyncService.SyncComponentTraits` would do via
   the OC client — when dispatched through the BFF this is automatic.

### The exact artifacts the AI must generate

Reference implementation lives at `manual-todo/`. Verified flow:

```
URL: http://http-todo-oauth-to-development-default-bac88680.openchoreoapis.localhost:19080
  → click "Sign in" → redirect to Thunder /gate/signin
  → enter admin@openchoreo.dev / Admin@123 → submit
  → redirect back to /callback?code=… → SPA exchanges code via /oidc/token (nginx proxy)
  → SPA stores access_token in sessionStorage
  → SPA fetches todo-api (via gateway, JWT-validated) → renders todos
  → create "Wire up OIDC end-to-end" → POST returns 201, todo appears ✓
```

#### Project layout

```
<repo-root>/
├── todo-api/
│   ├── main.go               # Go HTTP server
│   ├── go.mod
│   ├── Dockerfile
│   └── workload.yaml
└── todo-web/
    ├── default.conf.template # nginx + envsubst
    ├── Dockerfile
    ├── workload.yaml
    └── (dist/ — built SPA assets)
```

#### `todo-api/main.go` — JWT-trusting backend (canonical shape)

The agent must produce this skeleton:

```go
// Trust the gateway. Do NOT validate JWTs here. Do NOT implement /auth/*.
// Read identity from gateway-injected headers (set by the api-configuration
// ClusterTrait's jwt-auth policy claimMappings):
//   X-User-Id   (from JWT sub claim, the canonical caller identifier)
//   X-User-Name (from JWT username claim, for display)
//   X-User-Ou   (from JWT ouHandle claim, for multi-tenant scoping)
//
// Per-user data MUST be keyed on X-User-Id. Reject (401) when missing.
func withAuth(next http.Handler) http.Handler {
    return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        uid := r.Header.Get("X-User-Id")
        if uid == "" {
            http.Error(w, `{"error":"missing X-User-Id"}`, http.StatusUnauthorized)
            return
        }
        ctx := context.WithValue(r.Context(), ctxUserID, uid)
        next.ServeHTTP(w, r.WithContext(ctx))
    })
}
```

Storage: SQLite at `/data/todos.db`, key on `user_id` column. `/health`
endpoint exempt from `withAuth`.

#### `todo-api/Dockerfile`

```dockerfile
FROM golang:1.22-alpine AS builder
WORKDIR /src
COPY go.mod ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags='-s -w' -o /out/todo-api .

FROM alpine:3.20
RUN mkdir -p /data && apk add --no-cache ca-certificates
COPY --from=builder /out/todo-api /usr/local/bin/todo-api
EXPOSE 9090
CMD ["/usr/local/bin/todo-api"]
```

#### `todo-api/workload.yaml`

```yaml
apiVersion: openchoreo.dev/v1alpha1
kind: Workload
metadata:
  name: todo-oauth-todo-api-workload
  namespace: default
  labels:
    openchoreo.dev/component: todo-oauth-todo-api
    openchoreo.dev/project: todo-oauth
spec:
  owner:
    projectName: todo-oauth
    componentName: todo-oauth-todo-api
  container:
    image: <will be set by build pipeline>
  endpoints:
    http:
      basePath: /
      port: 9090
      type: HTTP
      visibility:
        - external      # MUST be external — the gateway sits in front
```

Companion `Component` CR carries `traits: [{instanceName: <comp>-http,
kind: ClusterTrait, name: api-configuration, parameters: {endpointName: http}}]`.
BFF emits this when `api.security: required` is in design.md.

#### `todo-web/default.conf.template` — nginx, envsubst, same-origin OIDC proxy

This is the CRITICAL file. Three things it does:

1. Serves the SPA + `/index.html` fallback.
2. Renders `/env-config.js` at pod start using envsubst — SPA reads
   `window.__ENV__` at runtime (no values baked into bundle).
3. **Proxies `/oidc/*` → Thunder's `/oauth2/*`** so the SPA's
   `POST /oidc/token` is same-origin (no CORS preflight). Without
   this nginx proxy the token exchange POST body is dropped by
   the kgateway CORS filter — a real platform bug we've documented
   above; the proxy is the workaround until the platform fixes it.

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

    location = /env-config.js {
        default_type application/javascript;
        return 200 'window.__ENV__ = { OIDC_ISSUER: "${OIDC_ISSUER}", OIDC_TOKEN_URL: "/oidc/token", OIDC_CLIENT_ID: "${OIDC_CLIENT_ID}", OIDC_SCOPES: "${OIDC_SCOPES}", API_BASE_URL: "${API_BASE_URL}" };';
    }

    # Same-origin proxy for OIDC token / userinfo / etc.
    location /oidc/ {
        proxy_pass ${OIDC_ISSUER}/oauth2/;
        proxy_set_header Host ${OIDC_HOST};
        proxy_pass_request_headers on;
    }

    location / {
        try_files $uri $uri/ /index.html;
    }
}
```

The SPA uses `cfg.OIDC_TOKEN_URL` (relative `/oidc/token`) for the
**token exchange** but uses **absolute** `cfg.OIDC_ISSUER` for the
authorize redirect (browser `location.assign` — no CORS involved).

#### `todo-web/Dockerfile`

```dockerfile
FROM nginx:alpine
COPY dist/ /usr/share/nginx/html/
COPY default.conf.template /etc/nginx/templates/default.conf.template
EXPOSE 9090
# nginx:alpine's stock entrypoint envsubst's /etc/nginx/templates/
# → /etc/nginx/conf.d/ at container start. No CMD override needed.
```

#### `todo-web/workload.yaml`

```yaml
apiVersion: openchoreo.dev/v1alpha1
kind: Workload
metadata:
  name: todo-oauth-todo-web-workload
  namespace: default
  labels:
    openchoreo.dev/component: todo-oauth-todo-web
    openchoreo.dev/project: todo-oauth
spec:
  owner:
    projectName: todo-oauth
    componentName: todo-oauth-todo-web
  container:
    image: <will be set by build pipeline>
    env:
      - key: OIDC_ISSUER
        value: "<from BFF dispatch comment>"
      - key: OIDC_CLIENT_ID
        value: "<from BFF dispatch comment>"
      - key: OIDC_SCOPES
        value: "<from BFF dispatch comment>"
      - key: OIDC_HOST
        value: "<hostname-only of OIDC_ISSUER, for nginx Host header>"
      - key: API_BASE_URL
        value: "<from ## Dependency endpoint resolved comment>"
  endpoints:
    http:
      basePath: /
      port: 9090
      type: HTTP
      visibility:
        - external
```

**Note**: `key:` (not `name:`) — `Workload` CRD uses k8s-style key/value
under `container.env`.

#### `todo-web/dist/app.js` — PKCE flow (canonical)

The SPA does Authorization Code + PKCE directly. Pattern:

```js
const cfg = window.__ENV__;  // throw on missing OIDC_ISSUER / OIDC_CLIENT_ID
const REDIRECT_URI = window.location.origin + '/callback';

// Login: random verifier → S256 challenge → redirect to authorize.
async function startLogin() {
  const verifier = randBase64Url(48);
  sessionStorage.setItem('pkce_verifier', verifier);
  const challenge = await sha256Base64Url(verifier);
  window.location.assign(`${cfg.OIDC_ISSUER}/oauth2/authorize?` + new URLSearchParams({
    response_type: 'code', client_id: cfg.OIDC_CLIENT_ID, redirect_uri: REDIRECT_URI,
    scope: cfg.OIDC_SCOPES || 'openid profile',
    code_challenge: challenge, code_challenge_method: 'S256',
    state: randBase64Url(16),
  }));
}

// Callback: exchange code via SAME-ORIGIN proxy (cfg.OIDC_TOKEN_URL = /oidc/token).
async function completeLogin(code) {
  const verifier = sessionStorage.getItem('pkce_verifier');
  const res = await fetch(cfg.OIDC_TOKEN_URL || `${cfg.OIDC_ISSUER}/oauth2/token`, {
    method: 'POST',
    headers: {'Content-Type': 'application/x-www-form-urlencoded'},
    body: new URLSearchParams({
      grant_type: 'authorization_code', code,
      redirect_uri: REDIRECT_URI, client_id: cfg.OIDC_CLIENT_ID, code_verifier: verifier,
    }),
  });
  const json = await res.json();
  sessionStorage.setItem('access_token', json.access_token);
  history.replaceState({}, '', '/');
}

// fetch wrapper attaches Authorization: Bearer to every upstream call.
async function authFetch(path, init = {}) {
  const tok = sessionStorage.getItem('access_token');
  const headers = new Headers(init.headers);
  if (tok) headers.set('Authorization', `Bearer ${tok}`);
  return fetch(cfg.API_BASE_URL + path, { ...init, headers });
}
```

**Boot path** at the bottom: check `window.location.pathname === '/callback'`,
call `completeLogin(code)`. Otherwise render based on
`!!sessionStorage.getItem('access_token')`.

### What the architect agent must emit

For a "todo with users" spec, the architect's structured output must
have exactly this shape (verified to deploy):

```yaml
# specs/design/components/todo-api/design.md frontmatter
type: service
language: Go
appPath: todo-api
buildpack: docker
entrypoint: deployment/service
api:
  security: required

# (no /auth/* anywhere in openapi.yaml — only /todos, /todos/{id}, /health)
```

```yaml
# specs/design/components/todo-web/design.md frontmatter
type: web-app
language: TypeScript / React (or vanilla JS — whatever is simplest)
appPath: todo-web
dependsOn: [todo-api]
buildpack: docker
entrypoint: deployment/web-application
auth:
  kind: oidc-spa
  upstream: todo-api
```

`componentAgentInstructions` for the API:
> "JWT-protected via the API Platform gateway. Do NOT implement
> `/auth/login`, `/auth/register`, or any token endpoint. Read the
> authenticated subject from the `X-User-Id` request header (the
> gateway injects it from the JWT's `sub` claim via the trait's
> `jwt-auth` policy `claimMappings`). Reject (401) when missing.
> Per-user data (todos) MUST be keyed on `X-User-Id`. Storage: SQLite
> at `/data/todos.db`. Do NOT add CORS middleware."

`componentAgentInstructions` for the SPA:
> "OIDC PKCE login against the platform IDP. Read OIDC config from
> `window.__ENV__.OIDC_*` (rendered by nginx envsubst from pod env;
> see SKILL.md OIDC-SPA section). Token exchange MUST use
> `window.__ENV__.OIDC_TOKEN_URL` (a same-origin nginx proxy to
> Thunder's `/oauth2/token` — bypasses a kgateway CORS bug). Attach
> `Authorization: Bearer <access_token>` to every `${API_BASE_URL}`
> call. Upstream `todo-api`: env var `VITE_TODO_API_URL` (legacy
> name kept for validator compatibility; actually delivered as
> `API_BASE_URL` in `window.__ENV__`). Workload env MUST include
> `OIDC_ISSUER`, `OIDC_CLIENT_ID`, `OIDC_SCOPES`, `OIDC_HOST`,
> `API_BASE_URL` with values from the `## OIDC client provisioned`
> and `## Dependency endpoint resolved` issue comments."

### What the tech-lead agent must emit in the issue body

For the **service** component task:

> ## Scope
> - Implement the OpenAPI contract (see `specs/design/components/todo-api/openapi.yaml`).
> - Do NOT implement `/auth/login`, `/auth/register`, or any auth endpoint. The API Platform gateway validates JWTs and injects `X-User-Id` from the JWT `sub` claim.
> - Read `X-User-Id` from every request; reject (401) when missing.
> - Per-user data (todos) MUST be keyed on `X-User-Id`.
> - Persistence: SQLite, file at `/data/todos.db`.
> - Do NOT add CORS middleware (the gateway handles CORS).
>
> ## Acceptance criteria
> - GET `/todos` with a valid `X-User-Id` returns only that user's todos.
> - Requests missing `X-User-Id` return 401.
> - Two different `X-User-Id` values see fully isolated lists.
> - `/health` returns 200 without auth.

For the **web-app** component task:

> ## Scope
> - Implement OIDC Authorization Code + PKCE against the platform IDP.
>   Read OIDC config from `window.__ENV__` at runtime (rendered by
>   nginx envsubst — see `asdlc` SKILL's OIDC-SPA section for the
>   nginx template and PKCE flow snippet).
> - **Token exchange MUST go through the same-origin nginx proxy**
>   at `window.__ENV__.OIDC_TOKEN_URL` (= `/oidc/token`). DO NOT call
>   Thunder's `/oauth2/token` directly — the kgateway CORS filter
>   drops the response body on cross-origin POSTs.
> - The Workload's container env MUST declare `OIDC_ISSUER`,
>   `OIDC_CLIENT_ID`, `OIDC_SCOPES`, `OIDC_HOST`, `API_BASE_URL`.
>   Copy values verbatim from the `## OIDC client provisioned` and
>   `## Dependency endpoint resolved` comments on this issue.
> - Attach `Authorization: Bearer <access_token>` to every upstream
>   API fetch.
>
> ## Acceptance criteria
> - Unauthenticated load redirects to the OIDC authorize endpoint.
> - After sign-in, the user lands back on the app with a token in
>   sessionStorage.
> - The app calls the upstream API with the token; the API returns
>   per-user data.
> - Reload keeps the user signed in.

### What the BFF must post on the web-app issue at dispatch time

A `## OIDC client provisioned` comment with these four fields (already
implemented in `dispatch_service.go::announceOIDCConfigIfApplicable` —
but needs `OIDC_HOST` added):

```markdown
## OIDC client provisioned

- **issuer**: http://thunder.openchoreo.localhost:8080
- **clientId**: asdlc-console-client
- **scopes**: openid profile
- **host**: thunder.openchoreo.localhost

Bake these four values verbatim into this SPA's `workload.yaml`
container env block as `OIDC_ISSUER`, `OIDC_CLIENT_ID`, `OIDC_SCOPES`,
`OIDC_HOST`. nginx envsubst renders them at pod start; the SPA reads
`window.__ENV__` at runtime. The token exchange MUST go through the
same-origin `/oidc/token` proxy (see the `asdlc` SKILL).
```

### Open gaps (must address before AI-flow E2E)

1. **`announceOIDCConfigIfApplicable`** in `dispatch_service.go` currently
   includes only issuer/clientId/scopes — add `host` (extracted from
   `OIDC_ISSUER` URL via `net/url.Parse` → `.Host`).
2. **`USER_APPS_OIDC_CLIENT_ID` env var** in `manifests/env-overlays/app-factory-api.yaml`
   currently says `APP_FACTORY_CONSOLE`. Change to `asdlc-console-client`
   (the actual seed in the running Thunder).
3. **Per-app redirect URI registration helper** — replace
   `register-user-app-callback.sh` with a REST-API version (GET app
   by clientId, append to inboundAuthConfig[0].config.redirectUris,
   PUT back).
4. **Manual Workload env-var injection** is needed today because
   `Workload.spec.container.env` is owned by the agent (not the BFF).
   The BFF posts the values via comment; the AGENT must bake them
   into the Workload it commits. Tech-lead prompt must state this
   explicitly (see "What the tech-lead agent must emit").
5. **Platform fixes (Thunder upgrade, trait policies v1, jwt-auth
   claimMappings, Thunder HTTPRoute CORS filter, traitEnvironmentConfigs
   patch via TraitSyncService)** must persist across `setup.sh`
   re-runs — currently they're live-patched and Flux will revert them.
   Land them upstream in `wso2cloud-deployment` submodule (separate
   workstream, `platform-design-expert` review).

## E2E test plan (Playwright CLI — 2026-05-18)

This is the **executable** flow for the first AI-driven attempt. The goal
isn't a polished test file yet — it's to drive the AI-flow end-to-end
through the browser and capture what breaks.

### Pre-flight: deploy + verify env (no AI yet)

Run before any browser work. If any step reports a gap, fix it and rerun
that step until clean.

| # | Action | Pass condition |
|---|--------|----------------|
| P1 | `bash deployments-v2/scripts/dev-cycle.sh app-factory-api` | New BFF pod rolls; `kubectl exec … -- env \| grep USER_APPS_OIDC_CLIENT_ID` shows `asdlc-console-client` |
| P2 | `bash deployments-v2/scripts/dev-cycle.sh app-factory-agents-service` | New agents pod rolls; container starts cleanly |
| P3 | Rebuild + push runner image: `cd remote-worker && docker buildx build --platform linux/amd64 -t docker.io/xlight05/app-factory-coding-agent-runner:latest --push .` | Docker Hub shows fresh `last_pushed` for `latest` |
| P4 | Re-apply Thunder HTTPRoute CORS filter (Flux reverted it): `kubectl patch httproute -n thunder thunder-httproute --type=json -p='[{"op":"replace","path":"/spec/rules/0/filters","value":[{"type":"CORS","cors":{"allowOrigins":["http://*.openchoreoapis.localhost:19080","http://localhost:19080"],"allowMethods":["GET","POST","PUT","PATCH","DELETE","OPTIONS"],"allowHeaders":["Content-Type","Authorization","Accept","Origin"],"allowCredentials":true,"maxAge":3600}}]}]'` | `kubectl get httproute -n thunder thunder-httproute -o jsonpath='{.spec.rules[0].filters[0].type}'` returns `CORS` |
| P5 | Confirm `api-configuration` trait still has `claimMappings` and policy versions `v1`: `kubectl get clustertrait api-configuration -o yaml \| grep claimMappings` | Returns the `x-user-id`/`x-user-name`/`x-user-ou` block |
| P6 | Confirm `asdlc-console-client` exists in Thunder: `kubectl exec -n thunder deployment/thunder-deployment -- curl -s -X POST http://localhost:8090/oauth2/token -d "grant_type=authorization_code&client_id=asdlc-console-client&code=x&redirect_uri=x"` | Returns `{"error":"invalid_grant"}` (NOT `invalid_client`) — the client is recognised |

### AI-flow E2E walkthrough (browser-driven via `playwright-cli`)

For each phase: take a `playwright-cli snapshot` after the action, save it
under `manual-todo/.playwright-cli/e2e-<phase>.yml` as a checkpoint. Each
checkpoint that diverges from expectation becomes a follow-up edit on the
prompts / SKILL.

1. **Login.** `playwright-cli open <console-URL>` → sign in as
   `admin@openchoreo.dev` / `Admin@123`.
2. **Create project.** Click "+ Project", name `todo-oauth-ai-1`, language
   stack auto. Wait until repo provisioning lands (the GH repo URL chip
   appears on the overview page).
3. **Type the spec & generate requirements.** In the requirements chat:
   *"A todo app where users sign in with their account and manage their own
   private todos. Multiple users; isolated lists. No public read."*
   → Generate → Save (v1).
4. **Verify requirements doc.** `playwright-cli snapshot` should show
   "Save" greyed out and a `v1` version chip.
5. **Generate design.** Click "Generate design".
   - **Architect assertion (in the rendered design tree):**
     `components/todo-api/design.md` frontmatter must contain
     `api: { security: required }`; `components/todo-web/design.md`
     frontmatter must contain `auth: { kind: oidc-spa, upstream: todo-api }`.
     If either is missing, capture the rendered design, edit the architect
     prompt, redeploy agents-service, regenerate.
   - **API assertion:** `todo-api/openapi.yaml` must NOT contain `/auth/*`
     paths (only `/todos`, `/todos/{id}`, `/health`).
   - Save (v1-1).
6. **Generate tasks.** Two tasks expected: one per component. Each task's
   GitHub issue body must:
   - For `todo-api`: include the `X-User-Id` Scope bullet and the
     "no `/auth/*`" Scope bullet (the tech-lead prompt's literal text).
   - For `todo-web`: include the `OIDC_TOKEN_URL same-origin` Scope bullet
     and the five-env-var `configurations.env` Scope bullet.
   - **Assertion:** open both issues on GitHub (via the issue link) — copy
     the body, grep for `OIDC_TOKEN_URL`, `X-User-Id`, `configurations.env`.
7. **Start implementation.** Click "Start implementation".
   - **BFF assertion (no UI):** within ~5s, the `todo-web` issue gets a
     `## OIDC client provisioned` comment with **four** bullet points
     including `host:` (sanity check the new BFF code is live). Use
     `gh issue view <web-issue> --comments` if the console doesn't surface
     comments yet.
   - **Append redirect URI to `asdlc-console-client`.** The platform
     doesn't do this yet (open gap #3). Once the SPA's external URL is
     discoverable (after its first deploy reaches `deployed`), append
     `<url>` and `<url>/callback` to `inboundAuthConfig[0].config.redirectUris`
     in Thunder via REST: `kubectl exec deployment/thunder-deployment -n thunder
     -- curl -s -X PUT http://localhost:8090/applications/019e369d-2bb1-7862-8cdc-74202b912eba -H 'Content-Type: application/json' -d @-` with the existing JSON appended.
8. **Wait for build + deploy of `todo-api`.** Watch the task board. When
   `todo-api` lands `deployed`, the `todo-web` issue gets a
   `## Dependency endpoint resolved` comment.
9. **Wait for build + deploy of `todo-web`.** The board shows both tasks
   `deployed`. Discover the webapp URL via the console's "Deploy" page or
   the architecture page.
10. **Browser login + create todo.**
    `playwright-cli goto <webapp-URL>` → snapshot (should show
    "Sign in" button). Click it. After Thunder login → callback → todos
    list. Type "ai-flow proves out e2e" → Enter. Snapshot shows the new
    todo. Reload — todo still there.
11. **Isolation check.** Open a private context (or `playwright-cli -s=u2 open <webapp-URL>` — separate session). Sign in as a different
    seeded user; assert the first user's todo is NOT visible.
12. **Tear down + record the run.** Save the final snapshot,
    `git status` to capture any prompts/SKILL/config edits made along
    the way, and append a row to the "Reference artifact — validation
    log" table above for each newly-confirmed piece.

### First-pass attempt — 2026-05-18 (blocked)

I ran the pre-flight + first browser pass and hit a pre-existing platform
issue not covered in the original plan. Documenting here so the next
attempt picks up from a known state.

**What worked on the first pass:**
1. P1–P6 pre-flight all passed after these in-place fixes:
   - `dev-cycle.sh app-factory-api` rebuilt the BFF image, but only the
     image tag got patched (not env). To pick up the env-overlay
     change, I had to re-`apply_workload app-factory-api asdlc-service`
     (with `BFF_TASK_SIGNING_KEY` exported via `ensure_env_loaded`).
   - **Workload spec changes do NOT propagate to a running `ReleaseBinding`**
     automatically — OC controller leaves the existing `spec.workloadOverrides`
     untouched. Direct `kubectl patch releasebinding ...` was required to
     get the new env onto the pod.
   - Same dance was needed for `app-factory-console` after fixing
     `VITE_THUNDER_CLIENT_ID` from `APP_FACTORY_CONSOLE` →
     `asdlc-console-client`.
   - Thunder HTTPRoute CORS filter re-patched (Flux had reverted it).

2. Sign-in flow against the platform IDP works:
   - Console → `/oauth2/authorize` → Thunder sign-in screen → submit
     `admin@openchoreo.dev / Admin@123` → callback → console dashboard.

**New gaps surfaced (not in the original plan):**

3. **Console OAuth client_id was wrong** (`APP_FACTORY_CONSOLE`) — fixed by
   updating env overlay + RB to `asdlc-console-client`.

4. **Thunder 0.34 sets JWT `aud` = client_id.** The BFF's
   `JWT_AUDIENCE` default is `asdlc-bff`, so user-side tokens
   (`aud=asdlc-console-client`) get rejected with "invalid audience".
   Fix: env overlay now sets `JWT_AUDIENCE=asdlc-bff,asdlc-console-client`.
   The console JWT now passes BFF middleware.

5. **`asdlc-console-client` redirect URIs did not include the console
   host.** Only the previously-registered `manual-todo` URLs were there.
   Appended `<console-origin>` and `<console-origin>/callback` via
   `THUNDER_SKIP_SECURITY=true` + REST PUT. Need a platform-level
   bootstrap so this auto-populates on `setup.sh`.

6. **BFF → OC API service-token fetch BROKEN (pre-existing).** Logs:
   ```
   service token fetch failed: token endpoint returned 401:
   {"error":"invalid_client","error_description":"Invalid client credentials"}
   ```
   The BFF env has `SERVICE_AUTH_CLIENT_ID=openchoreo-system-app`, but
   no Thunder application has that clientId. The real candidates in
   Thunder 0.34 are `openchoreo-workload-publisher-client`,
   `asdlc-system-client`, or `asdlc-api-client`. The BFF then falls back
   to the user JWT, which OC API rejects with `forbidden`. End-user
   symptom: console shows "failed to create project". **This is a
   platform setup gap unrelated to OIDC-SPA work — Project creation has
   been broken since the Thunder 0.34 upgrade.** Until this is fixed,
   the AI-flow E2E cannot get past step 2 ("Create project").

**Next steps (separate workstreams):**
- a) Identify the correct OC-API service client + secret in Thunder 0.34
  and update `SERVICE_AUTH_CLIENT_ID`/`SERVICE_AUTH_CLIENT_SECRET` in
  the BFF env overlay. Needs `platform-design-expert` review (changing
  this affects every project-creation / dispatch path).
- b) Land the AI-flow E2E afterward, picking up at the documented
  step 3 ("Type spec & generate requirements").
- c) Persist the live fixes upstream: console env-overlay client_id,
  BFF env-overlay client_id + JWT_AUDIENCE, Thunder HTTPRoute CORS
  filter, `asdlc-console-client` redirect URI bootstrap, RB sync of
  Workload env changes.

### Iteration loop

The first end-to-end pass is unlikely to be clean. When a phase fails:

- **Architect omitted `auth.kind`** → architect prompt edit + `dev-cycle.sh app-factory-agents-service` + regenerate design from console.
- **Tech-lead issue body missed an OIDC Scope bullet** → tech-lead prompt
  edit + agents redeploy + regenerate tasks.
- **Agent baked `containers.main.env` into workload.yaml** → SKILL.md
  edit + runner image rebuild + push + redispatch task (Retry on the
  failed task card).
- **BFF didn't post `## OIDC client provisioned`** → log into the BFF
  pod, check the issue post log, verify `USER_APPS_OIDC_CLIENT_ID` env.

Each failure is a real prompt/recipe gap. Capture it in this doc's
"Reference artifact — validation log" table before moving on, so the
next pass doesn't repeat it.

## E2E test (Playwright + API)

`tests/e2e/oauth-todo.spec.ts`:

1. Log into console as `admin@openchoreo.dev`.
2. Create project "todo-oauth-e2e".
3. Type requirements: *"A todo app where users sign in to manage their own
   todos."* Generate requirements → save.
4. Generate design → assert `todo-api` has `api.security: required` and
   **no** `/auth/*` paths in its `openapi.yaml`; assert `todo-web` has
   `auth.kind: oidc-spa`.
5. Generate tasks → start implementation → wait for both PRs merged →
   wait for both builds deployed.
6. Open the discovered webapp URL in a fresh browser context.
7. Assert redirect to Thunder login; sign in as a seeded test user.
8. Assert redirect back to webapp; create a todo; assert it persists
   across reload.
9. Open a second browser context, sign in as a different test user;
   assert isolation (the first user's todos are not visible).
10. **Never write a `client_id`, redirect URI, or backend auth code by hand.**

The assertion in step 10 is the test of the whole point: the platform did
the OAuth wiring end-to-end.
