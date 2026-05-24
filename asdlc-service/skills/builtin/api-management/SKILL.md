---
name: api-management
description: How the platform's API gateway validates JWTs, injects X-User-Id from the sub claim, attaches CORS, and how to design + write services and consumers that match. Apply to any service with exposesAPI.auth set, and to any consumer (sibling component OR external dependentApi) that calls a protected API.
metadata:
  asdlc.version: "1"
---

# API Management

## What this skill does

The platform fronts every service with `exposesAPI.auth` set through an
API gateway that validates JWTs, injects user-identity headers, and
attaches CORS. This skill tells the agent how to design and write code
that matches the gateway's contract, and how to call sibling protected
APIs as well as external dependent APIs from a consumer component.

## Platform facts

The following statements describe cluster behaviour. Editing them in
this skill does not change the cluster; it only desyncs your agent's
output from reality.

- The gateway sits in front of every service whose `exposesAPI.auth` is
  `end-user-required` or `service-required`.
- The gateway validates JWTs against the org's IDP. Your service does
  NOT validate JWTs.
- The gateway injects identity headers (lowercase claim → mixed-case
  header):
  - `sub → X-User-Id` (canonical caller identifier — REQUIRED, always present on protected requests)
  - `username → X-User-Name` (display, optional)
  - `ouHandle → X-User-Ou` (multi-tenant, optional)
- The gateway attaches an Envoy CORS filter to every `visibility: external`
  HTTPRoute via the `api-configuration` ClusterTrait. Your service does
  NOT add CORS middleware. Doubling produces two `Access-Control-Allow-Origin`
  headers and browsers reject the response.
- The agent does NOT see the gateway's `client_id`, JWT signing keys, or
  the IDP's discovery URL. Those live in BFF code.
- For consumers of a sibling protected API, the BFF injects the upstream
  URL into the consuming workload's runtime config:
  - Web-app consumer: read `window._env_.<NAME>_URL` (e.g.
    `TODO_API_URL`) via `src/env.ts`.
  - Backend consumer: read `os.Getenv("<NAME>_URL")` (Go) or
    `process.env.<NAME>_URL` (Node).
  Never hardcode the URL.
- For consumers of **external** dependent APIs (declared via
  `dependentApis` on the component), the BFF pins the URL on the
  consuming workload's ReleaseBinding env as `<NAME>_URL`. Same read
  pattern as sibling URLs.
- API error responses should use `application/problem+json` with a
  top-level `type`, `title`, `status` so the gateway can pass them
  through unchanged.

## Recommended practice

### Architect

- Set `exposesAPI.auth: end-user-required` on a `service` component when
  the spec OR its description implies caller authentication is needed.
  Use the keyword rubric in the base architect prompt to decide.
- Set `exposesAPI.auth: service-required` for machine-to-machine APIs.
- Omit `exposesAPI` entirely for public APIs (landing pages, health,
  status hello-worlds).
- When a `service` is `end-user-required` AND a sibling `web-app` signs
  in to it, that web-app MUST also carry `callerIdentity: { mode: end-user }`.
  The `thunder-authentication` skill owns this pairing rule (and its
  rationale) — apply it.
- For external upstreams that already exist outside the project, declare
  them as `dependentApis` on the consuming component. Use **name-only**
  declarations (`{ "name": "employee-api", "description": "..." }`) for
  catalog-known APIs — the platform resolves the URL from its in-cluster
  catalog at design-load time. Use full `{ name, url, description,
  authentication }` for arbitrary third-party APIs.
- Every component with a `dependentApi` MUST also carry an instruction
  line in `componentAgentInstructions` of the form:
  `Upstream external API <name>: env var <NAME_UPPER_SNAKE>_URL (auth: <authentication>). <description>. Read via os.Getenv / process.env / window._env_, call with standard HTTP client.`
- Every component with a sibling `dependsOn` on a backend service MUST
  also carry an instruction line of the form:
  `Upstream <name>: read the URL from <NAME_UPPER_SNAKE>_URL via the runtime-config shim.`
- Protected `service` `componentAgentInstructions` MUST say (verbatim or close):
  `No /auth/* endpoints. The API Platform gateway validates the JWT and the api-configuration trait's jwt-auth policy injects X-User-Id (from JWT sub claim) on every request. Read X-User-Id to identify the caller; reject (401) when missing. Per-user records MUST be keyed on X-User-Id. Do NOT validate JWTs yourself; do NOT add CORS middleware (the gateway handles CORS).`
- In the OpenAPI you author for a protected `service`, document the
  injected `X-User-Id` header under `parameters` so consumers know it's
  required-but-injected (the gateway adds it; clients don't set it). The
  generic OpenAPI conventions are in your base design instructions.

### Tech-lead — issue body bullets

For every task targeting a `service` with `exposesAPI.auth: end-user-required`:

- Scope: "Do NOT implement `/auth/login`, `/auth/register`, or any
  token-issuance endpoint. The platform gateway validates the JWT and
  the `api-configuration` trait's `jwt-auth` policy injects `X-User-Id`
  (from JWT `sub` claim) on every request. Read `X-User-Id`; reject
  (401) when missing. Per-user records MUST be keyed on `X-User-Id`."
- Scope: "Do NOT validate JWTs in code; do NOT add CORS middleware. The
  gateway handles both."
- Acceptance criteria: "Every protected endpoint rejects requests
  missing `X-User-Id` with 401; with a valid `X-User-Id`, returns only
  data owned by that subject. `/health` is exempt and returns 200
  without auth."

For every task whose component has a non-empty `dependentApis` array,
add **one Scope bullet per entry** of the form:

- "External upstream `<name>`: `<METHOD or 'GET'>` `<url>` —
  <description>. Authentication: <authentication>. Read the URL from
  env var `<NAME_UPPER_SNAKE>_URL` (already wired in the component's
  design instructions) and call with a standard HTTP client."

And one Acceptance criteria bullet per entry:

- "Calls to external upstream `<name>` use the URL from env var
  `<NAME_UPPER_SNAKE>_URL` (default `<url>`) and handle non-2xx
  responses without crashing. <auth-specific expectation: `none` → no
  Authorization header; `bearer` → caller's `Authorization` header
  forwarded; `api-key` → static key from env.>"

Use the literal URL, description, and authentication string from the
`dependentApis` entry — do not invent values.

For service components (NOT web-apps), always add a Scope bullet: "Do
NOT add CORS middleware. The platform's gateway attaches an Envoy CORS
filter to every `visibility: external` HTTPRoute via the
ClusterComponentType; doubled CORS headers break browsers."

### Coding agent — implementation

Read `X-User-Id` from every protected handler; reject 401 when missing.
Per-user rows MUST be keyed on `X-User-Id`.

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
        http.NotFound(w, r); return
    }
    w.WriteHeader(http.StatusNoContent)
}
```

Gate per-user queries with `AND user_id = ?`. Do NOT validate JWTs in
code. Do NOT add CORS middleware. Errors as `application/problem+json`
with a top-level `type`, `title`, `status`.

`/health` should remain exempt (no `mustUserID` call) so the platform's
readiness probe can reach it without auth.

For Go consumers of sibling APIs OR external dependent APIs, read the
URL from env at startup (NOT per-request); fail fast if missing:

```go
upstreamURL := os.Getenv("EMPLOYEE_API_URL")
if upstreamURL == "" {
    log.Fatal("EMPLOYEE_API_URL not set")
}
```

When forwarding caller auth to an upstream `bearer` API, propagate the
inbound `Authorization` header verbatim — do NOT re-issue a token.

For consumers of dependent APIs from a service component, the URL env
is set on the workload's ReleaseBinding at dispatch time; there is no
build-time URL injection. The agent's Dockerfile must not bake in any
URL.

The generic OpenAPI authoring conventions (3.0.3, `/health` on every
service, cross-component contract agreement) live in the architect's
design instructions — they are not restated here. The auth-specific
addition is in the Architect sub-section above (document the injected
`X-User-Id` header in the spec).

### Common pitfalls

| Symptom | Cause | Fix |
|---|---|---|
| CORS error in browser when calling upstream | Backend wrongly ships its own CORS middleware (doubled headers), OR upstream's `workload.yaml` lacks `visibility: external` | Remove the middleware; confirm `visibility: external` on upstream's `workload.yaml`. |
| Every protected request 401s in test | Test calls don't carry `X-User-Id`; in production the gateway sets it, in test you set it manually | In integration tests, set `X-User-Id` directly on the request; don't try to mint a JWT. |
| `/health` returns 401 | Handler accidentally went through `mustUserID` middleware | Carve out `/health` (and any other public path) before the auth gate. |
