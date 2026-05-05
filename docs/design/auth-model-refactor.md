# Auth Model Refactor

Single PR/milestone that establishes the platform's auth model. Closes the bypass paths in BFF / agents-service / remote-worker, replaces the shared internal secret, hardens task bearers, and lands correlation IDs end-to-end.

**Reference implementation:** `agent-manager` — `/Users/wso2/repos/agent-manager`. Every pattern below has a corresponding file in that repo. When in doubt, copy the agent-manager shape rather than inventing a new one.

## Goals

- Every service authenticates every inbound request. No "trust the network" or "trust an upstream gateway."
- Service-to-service auth uses the same primitive everywhere (signed JWTs verified against JWKS).
- Task bearers are issued by the BFF only; verifiers hold a public key and cannot mint tokens.
- One correlation ID flows across every service for the entire test pass.

## Non-goals (explicit out-of-scope)

- DB collapse / `organizations` sidecar removal (H4) — separate PR.
- Async webhook processing (H6).
- Generated OpenChoreo client (M1).
- Remote-worker HA / containerization (M2). Remote-worker stays host-bound.
- ESO migration. OpenBao usage stays as-is.

## Target Auth Model

Three token types. Nothing else.

| Token | Issuer | Algo | Audience | Lifetime |
|---|---|---|---|---|
| **User JWT** | Thunder/Asgardeo | RS256 | `asdlc-bff` | per IDP |
| **Service JWT** | Thunder (client_credentials) | RS256 | target service | ~1h, cached |
| **Task JWT** | BFF (private key) | RS256 | `git-service` | task duration (≤24h) |

Every service runs the same JWKS-based middleware. Issuer + audience + signature checked. No shared symmetric secrets anywhere.

```
console ──User JWT──▶ BFF ──Service JWT──▶ git-service
                       │                     ▲
                       ├──Service JWT──▶ agents-service
                       │
                       └──Service JWT──▶ remote-worker
                                              │
                                       (workspace holds Task JWT)
                                              │
                                              ▼
                                         git-service /credentials/refresh
                                         (verifies Task JWT)
```

## Reference Patterns from agent-manager

These are the canonical shapes to copy. Cite them in PR review.

### JWKS-based JWT validation (Go)

**Source:** `agent-manager-service/middleware/jwtassertion/auth.go`

Behaviour to mirror exactly:
- JWKS fetched from a configured URL (`KEY_MANAGER_JWKS_URL` in agent-manager → `JWKS_URL` for us).
- Cached in-process with TTL (`jwksCacheTTL = 1 * time.Hour`, lines ~84).
- Refresh protected by `singleflight.Group` (line ~86) — concurrent cache misses collapse to one fetch.
- Lookup by `kid` (line ~226). On miss, **refresh once and retry** (lines ~232–250) before failing — handles key rotation gracefully.
- Public key construction: parse JWK n/e fields → `rsa.PublicKey` (`convertJWKToPublicKey`, line ~439).
- Issuer + audience validated. Claims struct includes `ouId` (line ~43) — keep this exact name; the console already emits it.
- 401 responses include `WWW-Authenticate: Bearer realm="...", resource_metadata="<RFC 9728 URL>"` (see `auth_test.go:46-54` for the exact format). This makes the auth surface discoverable and matches Asgardeo's expectations.

For TypeScript services, port the same behaviour using `jose` (preferred) or `jsonwebtoken` + `jwks-rsa`. Keep the cache TTL, single-flight, and kid-refresh semantics identical.

### OAuth2 client_credentials (Service JWT acquisition)

**Source:** `agent-manager-service/clients/openchoreosvc/auth/auth.go`

- `AuthProvider` struct holds `accessToken` + `expiresAt` under `sync.RWMutex` (lines 49–58).
- `expiryBuffer = 30 * time.Second` — refresh 30s before actual expiry.
- `GetToken(ctx)` does double-checked locking (read-lock fast path, write-lock refresh path, re-check after upgrade). Lines 78–96.
- One provider instance per remote target (BFF will hold three: one for git-service, one for agents-service, one for remote-worker), or — simpler — one provider with the union audience and let the receiver check it.

### RS256 Task JWT signing + JWKS publication

**Source:** `agent-manager-service/services/agent_token_manager.go` (signing) + `agent-manager-service/api/agent_token_routes.go` (JWKS endpoint).

- Private key loaded from a PEM file (`config_loader.go:148` → `JWT_SIGNING_PRIVATE_KEY_PATH`, default `keys/private.pem`). Supports both PKCS#1 (`x509.ParsePKCS1PrivateKey`) and PKCS#8 (`x509.ParsePKCS8PrivateKey`) — copy this fallback (`agent_token_manager.go:127–138`).
- `KeyPair` struct carries `KeyID`, `Algorithm`, `PrivateKey`, `PublicKey` (lines 71–76). Token `kid` header set to `KeyID` so verifiers can pick the right key during rotation.
- JWKS endpoint registered as `GET /auth/external/jwks.json`, **no auth required** (`agent_token_routes.go:33–36`). We will use the same path on the BFF.
- Custom claims struct (lines 60–67): `RegisteredClaims` + domain-specific (`OrgId`, `ComponentUid`, etc.). For Task JWT we'll use `ocOrgId`, `taskId`, `projectId`.

### Internal-server pattern (host-bound services)

**Source:** `agent-manager-service/server/internal_server.go`

- Separate `http.Server` instance for internal traffic, distinct port, optional TLS with self-signed cert (`rsa.GenerateKey(rand.Reader, 2048)`, lines 134–166).
- Bound to a configurable interface; mounted with API-key middleware for /api/internal routes.
- For `remote-worker`, we adopt the binding (`127.0.0.1`-only) but skip the self-signed TLS for now — Service JWT alone is enough on a loopback interface.

### Middleware wiring (per-route auth selection)

**Source:** `agent-manager-service/api/monitor_publisher_routes.go:30`

- Pattern: wrap the handler explicitly with the middleware: `handler := jwtassertion.PublisherClientAuthMiddleware()(http.HandlerFunc(ctrl.PublishScores))`.
- Different routes can use different middleware variants (user JWT vs. publisher client vs. internal API key). Use this same per-route wrap pattern in our `git-service` so `/credentials/refresh` can require Task JWT while `/api/v1/repos/*` requires Service JWT.

### Correlation ID middleware

**Source:** `agent-manager-service/middleware/correlation_id.go`

- Reads `X-Correlation-ID` from request, generates a UUID if absent, attaches to context + response header.
- Logger middleware pulls it from context and includes in every log line.
- Outbound HTTP clients pull it from context and re-inject as `X-Correlation-ID` header.

ASDLC has the inbound half (`asdlc-service/middleware/correlation_id.go`). We need the outbound half across all four HTTP clients.

### Claims shape

**Source:** `agent-manager-service/middleware/jwtassertion/auth.go:43`

```go
type TokenClaims struct {
    Sub      string
    OuId     string `json:"ouId"`     // <-- already used by ASDLC console
    OuHandle string `json:"ouHandle"`
    Scope    string
    // ...
}
```

Use the same field names. The console (`useUserClaims.ts`) and Thunder both already emit them.

## Per-service changes

### asdlc-service (BFF)

- Replace `middleware/jwt/jwt.go:ParseUnverified` with JWKS-backed validation. Port `agent-manager-service/middleware/jwtassertion/auth.go` verbatim, adjust package paths.
- Rename `Claims.ProductType` → `ClientID`. Drop the stale "API gateway validates tokens" comment.
- Add an `AuthProvider` per outbound client, modeled on `agent-manager-service/clients/openchoreosvc/auth/auth.go`. Used for git-service, agents-service, remote-worker calls.
- Add a `TaskTokenManager` modeled on `agent-manager-service/services/agent_token_manager.go`. Loads RSA private key from `BFF_TASK_SIGNING_KEY_PATH`, signs Task JWTs with `kid` header, supports key rotation.
- Expose `GET /auth/external/jwks.json` (no auth) returning the public key. Mirror `agent-manager-service/api/agent_token_routes.go:33-36`.
- Drop `INTERNAL_SHARED_SECRET` plumbing. `middleware/internal_only.go` and the matching env var go away.
- Drop the `git-service → BFF /internal/tasks/{taskId}/org` callback handler (no longer needed once Task JWT carries verifiable `ocOrgId`).
- Add `X-Correlation-ID` propagation to every outbound HTTP client (`clients/gitservice`, `clients/agents`, `clients/remoteworker`, `clients/openchoreo`). Mirror `agent-manager-service/middleware/correlation_id.go`.
- Validate `ocOrgId`, `projectId`, `taskId` as strict UUIDs at every handler boundary before they reach storage paths or shell templates.

### git-service

- Add JWKS-based middleware (same code path as BFF). Per-route wiring like `monitor_publisher_routes.go:30`:
  - `/api/v1/*` → Service JWT (audience `git-service`, JWKS = Thunder).
  - `/api/v1/credentials/refresh` → Task JWT (audience `git-service`, JWKS = BFF's `/auth/external/jwks.json`). Verify `taskId` + `ocOrgId` claims; reject if `ocOrgId` doesn't match the URL.
- Delete `middleware/internal_only.go` and `INTERNAL_SHARED_SECRET`. The `/internal/*` route prefix can stay as a path convention but is now JWT-gated like everything else.
- Delete `clients/asdlcservice` (the BFF callback client) and `ASDLC_API_INTERNAL_URL` env. Trust the Task JWT's `ocOrgId` claim instead.
- Drop `BEARER_SIGNING_KEY` (HS256). The new Task JWT validator pulls BFF's public key via JWKS at startup, caches with TTL + single-flight (same pattern as Thunder JWKS).
- Validate `ocOrgId` as strict UUID before any OpenBao path construction.

### agents-service

- Add Express middleware that requires a Service JWT (audience `agents-service`). Port the agent-manager `jwtassertion` behaviour to TS using `jose`. Reject unauthenticated requests with 401 + `WWW-Authenticate` header.
- JWKS cache + single-flight + kid-refresh semantics must match the Go implementation.
- Add `X-Correlation-ID` middleware (read or generate, attach to logs).

### remote-worker

- Bind to `127.0.0.1` only. Stays host-bound — no other architectural change.
- Add Express middleware (same TS port) requiring a Service JWT (audience `remote-worker`) on `/dispatch` and `/status`.
- Add a concurrency cap on the task registry (semaphore in `taskRegistry.ts`). Reject above-cap dispatches with 429 so the BFF can backpressure. Default cap from env, e.g. `REMOTE_WORKER_MAX_CONCURRENT_TASKS=8`.
- Validate `ocOrgId` / `projectId` / `taskId` as strict UUIDs before they touch the workspace path or `credhelper.sh` template.
- Add `X-Correlation-ID` middleware. Forward the ID into `credhelper.sh` calls to git-service.

### console

- No code change. Already sends User JWT.

## Key & secret plumbing

- BFF Task JWT private key: mounted as a file (`BFF_TASK_SIGNING_KEY_PATH=/app/keys/task-signing.pem`). Not in env. Same convention as agent-manager (`config_loader.go:148`, default `keys/private.pem`).
- BFF exposes `GET /auth/external/jwks.json` (no auth) so git-service can fetch the public key. Same path as agent-manager.
- New env (added):
  - `JWKS_URL` (Thunder) — every service. Mirrors agent-manager's `KEY_MANAGER_JWKS_URL`.
  - `SERVICE_AUTH_AUDIENCE` — per service (`asdlc-bff`, `git-service`, `agents-service`, `remote-worker`).
  - `SERVICE_AUTH_TOKEN_URL`, `SERVICE_AUTH_CLIENT_ID`, `SERVICE_AUTH_CLIENT_SECRET` — already present in BFF; replicate to any service that becomes an outbound caller.
  - `BFF_TASK_SIGNING_KEY_PATH` — BFF only.
  - `BFF_JWKS_URL` — git-service only (for Task JWT verification).
  - `REMOTE_WORKER_MAX_CONCURRENT_TASKS` — remote-worker.
- Env removed: `INTERNAL_SHARED_SECRET` (both services), `BEARER_SIGNING_KEY` (both services), `ASDLC_API_INTERNAL_URL` (git-service).
- Thunder OAuth2 clients registered for each service (BFF, git-service callers, agents-service callers, remote-worker callers). Done by `setup.sh`.

## Test plan

One end-to-end pass covers the full bundle.

1. **Unit / middleware**: each service rejects unsigned, expired, wrong-audience, wrong-issuer tokens. Reuse agent-manager's test shape — see `agent-manager-service/middleware/jwtassertion/auth_test.go` for the table-driven cases (lines 46–121 cover the 401 header format).
2. **E2E happy path** (Playwright): login → create project → generate spec (agents-service) → generate design → start implementation (BFF→remote-worker→git-service `/credentials/refresh` with Task JWT) → push → webhook → build. Single correlation ID visible across all service logs.
3. **Negative**: hit `agents-service:3400` and `remote-worker:3200` directly without a token → 401. Hit BFF with a forged HS256 token → 401. Replay a Task JWT after task completion → 401 (verify expiry).
4. **Boundary validation**: project/task creation with non-UUID IDs in path → 400 before any storage I/O.
5. **Backpressure**: spam `/dispatch` past the concurrency cap → 429.
6. **Key rotation**: rotate BFF Task signing key, confirm git-service refreshes JWKS on `kid` miss and accepts the new token without restart (covers `auth.go:232-250` behaviour).

## Rollout

Single PR, single deploy. No flag gating — the old shared-secret path and the unauthenticated endpoints must not coexist with the new ones (defeats the purpose).

Order of merge inside the PR:
1. JWKS middleware + `AuthProvider` + `TaskTokenManager` (libraries first, no behaviour change yet).
2. BFF, git-service, agents-service, remote-worker switch over together.
3. Delete `INTERNAL_SHARED_SECRET`, `BEARER_SIGNING_KEY`, `ASDLC_API_INTERNAL_URL`, callback handler, callback client.
4. Update `setup.sh` to register Thunder clients and generate the BFF signing key.

## What this closes

C1, C2, C3, H1, H2, H3, M6, the input-validation half of H5, L2.
