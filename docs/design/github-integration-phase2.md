# GitHub Integration — Phase 2 Implementation Design

This is the concrete, implementation-level design for **Phase 2** of `github-integration-evolution.md`. The evolution doc is the architectural truth; this doc is the engineering plan: schemas, endpoints, file layouts, lifecycle ordering, migration steps. Read the evolution doc first, and read `github-integration-phase0.md` second — Phase 2 is *additive on the seams Phase 0 deliberately left in place*, not a rewrite. Anywhere this doc is silent, Phase 0's contract still holds.

The chunk replaces the single shared platform PAT with **per-org credentials in two peer modes** (GitHub App installation, User-PAT), folds in **OpenBao** as the platform's secret store, completes the **credential resolver** so call sites stop being single-tenant in practice, and lands the **OC `SecretReference` migration** that retires the legacy `GetCredentials` bridge. App and PAT mode ship as first-class peers per evolution-doc §6.1 — neither is a fallback. Mode is fixed at connect time per §6.4.

The chunk is intentionally large. Splitting it leaves the platform half-multitenant — half the call graph parametrised by `ocOrgId`, the other half still reading `cfg.GitHub.PlatformPAT`. There is no useful intermediate state.

---

## 0. Implementation status

Phase 2 complete. All four §15.4 PRs (A–D) shipped on the `github-app` branch:

- **PR A** — per-org credential foundation (commit `a534a20`).
- **PR B** — connect/disconnect + console UI + multi-tenant webhook routing (commit `b68cced`).
- **PR C** — `SecretReference` migration + `GetCredentials` excision + bearer `org` claim runtime tripwire (commit `f821581`).
- **PR D** — reach reconciliation cascade + 24h credential validator + workspace `update-git-identity` hook + build auth-failure retry budget + ops cleanup (commit `c54b6b0`).

After PR D, the §2.1 invariant holds: every Bearer header is built inside `git-service/services/github_client.go`, no token-bearing value crosses the BFF↔git-service boundary, and every operational scenario named in §6.6, §6.8, §6.10, and §9.3 has a concrete recovery path.

Items in §1 "Out (deferred follow-ups)" remain deferred — Phase 2 is closed.

---

## 1. Scope

### In

1. **Per-org credential records.** `org_credentials` becomes the canonical per-org table. Each org has one active row. The Phase 0 single-tenant `'platform-pat'` row is **deleted**, the kind retired, and the `DEPLOYMENT_TIER` gate's special-case for `'platform-pat'` removed.
2. **`org_credentials` table relocates from asdlc-service to git-service.** Today the table lives in the BFF Postgres (Phase 0 deviated from evolution-doc §3.3 here). Phase 2 honours the architectural rule: the credential record lives in the same service that holds the credential. The BFF reads non-sensitive projections via `GET /internal/credentials/orgs/{ocOrgId}` and never sees a token. (See §14.2 for the migration steps.)
3. **Two new credential kinds**, both implementing the `credentials.Credential` interface from Phase 0:
   - `app-installation` — GitHub App installation tokens, minted on demand, ~1 h TTL, platform-level webhook delivery, App's bot identity.
   - `user-pat` — Per-org PAT supplied by the user, encrypted at rest in OpenBao, long-lived, per-repo webhook delivery, PAT owner's identity.
4. **OpenBao client + access wrapper.** The Phase 0 `OpenBaoStore` interface gains its implementation in `git-service/pkg/credentials/openbao_store.go`. Single platform-wide policy; per-org isolation enforced by path-namespacing inside the wrapper. The Phase 0 import fence stays.
5. **App private-key custody.** Stored in OpenBao at `secret/asdlc/_platform/github/app/private_key`. Loaded once at git-service startup; cached in process. Two active keys supported for rotation (GitHub allows two; the rotator picks the most recently added).
6. **Connect / status / disconnect surface** on the BFF, scoped to `ocOrgId`. PAT-mode connect/replace is a single idempotent `POST` (existing row → replace; no row → create). App-mode connect is the redirect dance with a signed JWT carrying the connect state — **no DB row** for in-flight connects.
7. **Webhook receiver multi-tenancy.** Phase 0's pipeline shape is preserved; Phase 2 fills in the routing data. Receiver: parse routing key (`installation.id` for App, `repository.full_name` for PAT) → resolve `ocOrgId` against connection records → HMAC-validate against THAT org's secrets (App: `_platform` list; PAT: row's list) with miss-then-refetch on mismatch.
8. **New webhook event handlers** for the App-mode lifecycle: `installation.created`, `installation.deleted`, `installation.suspend`, `installation.unsuspend`, `installation_repositories.added`, `installation_repositories.removed`.
9. **OC build credential migration.** The legacy `GetCredentials` flow is deleted at every call site (the doc enumerates them in §1.10 below). OC `GitSecret` is replaced by **OpenBao + `SecretReference` CR**, mirroring agent-manager's pattern. One `SecretReference` per repo, named `git-{ocOrgId}-{repoSlug}`, created at repo provision. The BFF mints a fresh token via git-service `POST /internal/credentials/orgs/{ocOrgId}/mint-build` immediately before each `WorkflowRun`; git-service writes the token to OpenBao and returns only the `secretRefName`. The BFF never sees the token.
10. **`GetCredentials` excision.** Three call sites consume the legacy bridge today:
    - `asdlc-service/services/task_service.go` — the `PHASE-2-REMOVE` site flagged in Phase 0 §11. Already vestigial; deleted.
    - `asdlc-service/services/remote_worker_service.go::DispatchTasks` (around line 84) — the **active** consumer, which today reads `creds.PAT, creds.RepoURL, creds.DefaultBranch, creds.Identity` from `GetCredentials`. The PAT use is replaced by the new `mint-build` flow at the dispatch boundary; the non-secret fields move to a new identity-only endpoint `GET /internal/credentials/orgs/{ocOrgId}/identity` (returns `{name, email, login, repoUrl, defaultBranch}` — no token).
    - `asdlc-service/clients/openchoreo/component_client.go::CreateGitSecret` — deleted along with `GitSecret` itself.
    A CI grep fails the build if `gitservice.GetCredentials` survives anywhere outside an interface deletion commit.
11. **Per-task bearer carries `org` claim.** The Phase 0 bearer signs `{tid, exp}`; Phase 2 adds `{org}`. `/credentials/refresh` rejects when `claims.org != ComponentTask.OrgID` for the `tid` — a tripwire against task-row mutability becoming part of the credential trust path.
12. **In-process App-token cache** in git-service: per-installation, deadline-keyed with a 5-minute safety margin on the GitHub-supplied `expires_at`. `singleflight.Group` deduplicates concurrent mints.
13. **PAT reads use `singleflight` only**, no plaintext cache. The 30-min cache from the prior draft was a security trade the doc itself flagged as undesirable; OpenBao reads are sub-10ms and `singleflight` collapses bursts. PAT replace mid-flight on one git-service replica therefore takes effect on every replica's next read, with no invalidation step.
14. **OpenBao reachability gate** on git-service startup. The pod refuses ready-state until OpenBao is reachable. Combined with rolling-deploy `maxSurge=1, maxUnavailable=0`, this prevents new pods coming online with empty App-token caches during an OpenBao outage (evolution-doc §9.13).
15. **Webhook secret rotation.** PAT-mode: per-org list (N-of-M) on `org_credentials.webhook_secrets`, with admin routes to append and drop. App-mode: App-wide list at `secret/asdlc/_platform/github/app/webhook_secret` (also N-of-M). Receiver consults whichever via the unified `SecretProvider`.

### Out (deferred follow-ups)

1. **Migration of pre-Phase-2 projects.** Per evolution-doc §8. In dev: TRUNCATE existing data; the platform-PAT row, the legacy GitSecret, and any leftover `git_repositories` rows are wiped together (§14.2 step 3). In production: Phase 2 cuts over against an empty deployment.
2. **Mode switching after connect.** Fixed at connect time. The `POST` connect endpoint refuses when an active row already exists under a different kind; disconnecting and reconnecting in the other mode is refused while any repos exist under the first mode.
3. **Multi-installation per OC org.** One credential per org. A second `installation_id` for the same `oc_org_id` is rejected at INSERT.
4. **Per-installation rate-limit budgeting** (evolution-doc §9.5). Token cache + 401 backoff is the Phase 2 envelope; per-installation token bucket and saturation metric land with hardening.
5. **Janitor for `superseded` / `abandoned`** (already deferred from Phase 0). Phase 2 introduces two new abandon causes — `org.disconnected` and `repo.unselected` — but the resource-cleanup contract for them is part of the existing janitor follow-up.
6. **Audit-query UI.** Identity drift, connect, disconnect, and webhook-secret rotation are recorded as columns/timestamps on `org_credentials`. A dedicated audit table and UI is out.
7. **Branch protection** (still deferred from Phase 0).
8. **App private-key rotation runbook automation.** Manual `docs/operations/github-app-rotation.md` runbook is sufficient for Phase 2. A CLI or BFF endpoint to drive the swap is out.

---

## 2. Permission boundaries

The four-scope structure from Phase 0 (org / project / component / project-secretref / task) carries forward unchanged in shape. What changes is what's *concrete* at the org scope.

| Scope | Phase 0 representation | Phase 2 representation |
|---|---|---|
| **Org** | One implicit row, `kind='platform-pat'`, env-derived | Real per-org `org_credentials` row, `kind ∈ {app-installation, user-pat}`, OpenBao-backed, lives in git-service |
| **Project** | Repo + per-repo webhook ID | Same shape; webhook strategy chosen by `Credential.WebhookStrategy()` (App: `WebhookPlatform`; PAT: `WebhookPerRepo`) |
| **Component** | Created at dispatch, idempotent on `(ocOrgId, project, componentName)` | Unchanged |
| **Project (secretref)** | OC `GitSecret` per project (legacy) | OC `SecretReference` per project, name `git-{ocOrgId}-{repoSlug}`, OpenBao-backed |
| **Task** | Issue + branch + PR + workspace + per-task bearer (`{tid, exp}`) | Bearer adds `{org}` claim; everything else unchanged |

Cross-cutting rules tighten:

- **git-service is the sole holder of GitHub credentials**, with no exception. Phase 0 had one bridge (`GetCredentials`) where the BFF held a PAT in cleartext at dispatch; Phase 2 deletes every call site (§1.10). After Phase 2, no Bearer header is built outside `git-service/services/github_client.go`, and no token-bearing value crosses the BFF↔git-service boundary in either direction.
- **The OpenBao wrapper is the multi-tenant enforcement boundary.** Path construction is internal; `ocOrgID` mandatory; the `_platform/...` namespace reachable only from one named loader. Build-time fence backs all of this.
- **`mint-build` validates ownership server-side.** The BFF supplies `repoSlug`; git-service rejects if no `git_repositories` row matches `(ocOrgId, repoSlug)` with `status='active'`. The slug is treated as untrusted input. This is the explicit fence preventing a cross-org token write through a buggy join.
- **No call site branches on credential kind.** The four observables on `Credential` are sufficient. A type-switch on the interface outside `pkg/credentials/` fails code review.

---

## 3. Service responsibilities (post-Phase-2)

```
                    ┌──────────────────────────────────────────────┐
                    │ trusted internal network                     │
   GitHub ─webhook──┤▶ asdlc-service (BFF)                         │
                    │     · /webhooks/github (multi-tenant routing)│
                    │     · /api/v1/orgs/{ocOrgId}/github/*        │
                    │       connect / status / disconnect          │
                    │     · task state machine + projector         │
                    │     · workflowrun service: mint-then-trigger │
                    │     · NO github creds (no exception)         │
                    │                                              │
                    │   git-service                                │
                    │     · sole holder of github credentials      │
                    │     · pkg/credentials                        │
                    │         · Resolver (per-org lookup)          │
                    │         · platform_pat.go (DELETED)          │
                    │         · app_installation.go (NEW)          │
                    │         · user_pat.go (NEW)                  │
                    │         · openbao_store.go (NEW impl)        │
                    │         · app_token_minter.go (NEW)          │
                    │     · org_credentials, audits as columns     │
                    │     · /internal/credentials/orgs/{ocOrgId}   │
                    │       (BFF-only)                             │
                    │     · /internal/credentials/orgs/.../mint-build (BFF-only)
                    │     · /api/v1/credentials/refresh            │
                    │       (workspace, per-task bearer w/ org)    │
                    │                                              │
                    │   OpenBao                                    │
                    │     · secret/asdlc/{ocOrgId}/github/pat      │
                    │     · secret/asdlc/{ocOrgId}/git/{repoSlug}  │
                    │     · secret/asdlc/_platform/github/app/...  │
                    │                                              │
                    │   remote-worker (host) — UNCHANGED           │
                    │     identity-drift hook now checks bearer    │
                    │     `tid` echo from /credentials/refresh     │
                    └──────────────────────────────────────────────┘
```

What **moves out** of asdlc-service: `org_credentials` table and model, `EnvSecretProvider`, the legacy `GetCredentials` consumers, `CreateGitSecret`, the `DEPLOYMENT_TIER` gate's `'platform-pat'` special case.

What **moves in** to asdlc-service: connect/disconnect controllers, the App-callback handler (signed-JWT state verification), routing-key resolver, pre-WorkflowRun mint-then-write call.

What **moves in** to git-service: the OpenBao client + store implementation, two new credential implementations, the App token minter, App-token cache, three new HTTP routes (`/internal/credentials/orgs/...`, `/.../mint-build`, `/.../identity`), webhook-secret CRUD routes, the OpenBao reachability health probe.

`remote-worker` is unchanged in service shape; its credential-helper script gains a `tid` echo check (§5.3), and its `gh` wrapper handles short-lived tokens that were never exercised under Phase 0.

---

## 4. Data model changes

### 4.1 `org_credentials` (git-service Postgres)

The Phase 0 row is dropped (`'platform-pat'` retired). The Phase 2 schema with full constraints:

```sql
CREATE TABLE org_credentials (
  oc_org_id          TEXT PRIMARY KEY,
  kind               TEXT NOT NULL CHECK (kind IN ('app-installation', 'user-pat')),
  github_login       TEXT NOT NULL,
  identity_name      TEXT NOT NULL,
  identity_email     TEXT NOT NULL,
  identity_login     TEXT NOT NULL,
  installation_id    BIGINT,                                   -- App mode only
  selected_repos     JSONB,                                    -- App mode only: ["owner/repo", ...]
  pat_secret_ref     TEXT,                                     -- PAT mode: opaque pointer (e.g. 'v1'); not the token
  webhook_secrets    JSONB,                                    -- PAT mode: list of {secret, added_at}; App mode: NULL
  status             TEXT NOT NULL DEFAULT 'active'
                       CHECK (status IN ('active','suspended','disconnected')),
  -- Inline audit columns (replace separate audit table — only one row per concept ever needed)
  connected_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
  last_validated_at  TIMESTAMPTZ,
  identity_changed_at TIMESTAMPTZ,                             -- last identity drift; NULL means identity stable
  prev_identity_login TEXT,                                    -- the login replaced; NULL until first drift
  CONSTRAINT secrets_shape_per_kind CHECK (
    (kind = 'user-pat'         AND webhook_secrets IS NOT NULL AND jsonb_array_length(webhook_secrets) >= 1)
    OR (kind = 'app-installation' AND webhook_secrets IS NULL)
  ),
  CONSTRAINT app_fields CHECK (
    (kind = 'user-pat'         AND installation_id IS NULL AND selected_repos IS NULL)
    OR (kind = 'app-installation' AND installation_id IS NOT NULL)
  )
);

CREATE UNIQUE INDEX ux_org_credentials_github_login_active
  ON org_credentials (github_login)
  WHERE status = 'active';

CREATE UNIQUE INDEX ux_org_credentials_installation_active
  ON org_credentials (installation_id)
  WHERE status = 'active' AND installation_id IS NOT NULL;
```

Three constraints carry the architectural rules into the schema:

- `secrets_shape_per_kind` enforces the App-vs-PAT scoping: App-mode rows have no per-row webhook secrets (delivery is App-wide via `_platform`); PAT-mode rows have at least one. Eliminates the "duplicate App secret per row" model contradiction.
- `app_fields` makes `installation_id` and `selected_repos` belong to App mode and nothing else.
- Both unique-active indexes enforce the §3.3 "one OC org : one GitHub org" property.

`webhook_secrets` is read whole on every event (≤5 entries in practice); no value-index needed.

### 4.2 No separate audit table

Identity drift is captured by `identity_changed_at` + `prev_identity_login`. Connect timestamp is `connected_at`. Disconnect is implicit in `status='disconnected'`. This is sufficient for the banners the UI shows. A dedicated audit-query UI is deferred (§1 Out 6); when it lands, write a richer `org_credential_events` table at that point. Don't ship empty plumbing.

### 4.3 No `org_connect_attempts` table

The App-mode connect's CSRF state lives in a **signed JWT** carried in the `state` query param. Claims: `{ocOrgId, actor, exp}`. TTL: 15 minutes. Verified on callback by the BFF using a startup-loaded HMAC key (`OAUTH_STATE_SIGNING_KEY`). Replay within 15 min by anyone but the original GitHub-admin user is benign — they'd have to be an admin on the target GitHub org *and* hold the JWT *and* act within the window. No DB row, no janitor, no expiry sweep.

### 4.4 No `pending_installations` table

If `installation.created` arrives without a matching `org_credentials` row (the BFF callback hasn't completed, e.g. user closed the tab), the receiver acks 200 and does nothing. The install exists on GitHub but is unbound to any OC org. Recovery: the user clicks "Connect GitHub App" again in the console. GitHub recognises the existing install for that account, presents "Configure" instead of "Install," and on confirm fires a fresh callback with the same `installation_id` — the BFF binds it. No race surface, no UI step, no table.

### 4.5 `webhook_deliveries.oc_org_id` becomes meaningful

Phase 0 records this column with the constant `'platform'`. Phase 2 fills it in from the routing-key resolver. No schema change. Existing dev rows (`'platform'`) are wiped along with the migration (§14.2 step 3).

### 4.6 `git_repositories` (git-service Postgres)

```sql
ALTER TABLE git_repositories ADD COLUMN oc_secret_ref_name TEXT;
```

Persists the deterministic name `git-{ocOrgId}-{repoSlug}` at provisioning. The `webhook_id` column from Phase 0 stays nullable: populated for PAT-mode repos, NULL for App-mode (platform-level delivery).

### 4.7 GitHub-side artifacts

| Artifact | App mode | PAT mode |
|---|---|---|
| Webhook delivery | App-wide callback URL | Per-repo, registered at provision |
| Identity (commits/comments/PR actions) | App's bot identity, fetched via `GET /app` once at App-key load | PAT owner's identity, fetched via `GET /user` at connect; refreshed on PAT replace |
| Repo creation owner | The App install's `account.login` (resolved at INSERT via `GET /app/installations/{id}`) | The user-supplied `githubLogin`, validated for reach |

GitHub does not emit an event when a user/org is renamed. App-mode `github_login` can therefore drift. Mitigation: the periodic validator (§6.9) re-fetches `GET /app/installations/{id}` daily and updates `github_login` in place. PAT mode handles the same risk via the user-driven Replace flow.

---

## 5. HTTP surface changes

### 5.1 BFF — connect/status/disconnect (per-org)

All paths scope to `ocOrgId` and require console JWT plus an org-membership check. The App callback is unscoped because GitHub Apps support exactly one configured callback URL.

| Method | Path | Purpose |
|---|---|---|
| `POST` | `/api/v1/orgs/{ocOrgId}/github/app/start` | App: signs a JWT `state {ocOrgId, actor, exp=15m}`, returns `{ installUrl }`. |
| `GET`  | `/api/v1/github/app/callback?installation_id&state` | Verifies JWT, calls git-service `POST /internal/credentials/orgs/{ocOrgId}` with `{kind:'app-installation', installationId}`, redirects to settings. |
| `POST` | `/api/v1/orgs/{ocOrgId}/github/pat` | PAT: idempotent. Body `{pat, githubLogin}`. No row → create. Existing PAT row → replace (recompute identity, run reach validation). Existing App row → 409. |
| `GET`  | `/api/v1/orgs/{ocOrgId}/github` | Projection: `{kind, identity, githubLogin, status, connectedAt, lastValidatedAt, identityChangedAt, selectedRepos?}`. **Never returns the token.** |
| `DELETE` | `/api/v1/orgs/{ocOrgId}/github` | Disconnect. Cascades non-terminal tasks to `abandoned`. |

### 5.2 git-service — internal credential routes (BFF-only)

All `/internal/...` routes require an internal-bearer header (production: network policy + the existing internal shared secret).

| Method | Path | Purpose |
|---|---|---|
| `POST` | `/internal/credentials/orgs/{ocOrgId}` | Create or replace. Body: `{kind, ...kind-specific}`. App: `{installationId}`. PAT: `{pat, githubLogin}`. PAT replace recomputes identity and re-runs the validation chain (§6.5). Returns the projection (no token). 409 on cross-mode change. |
| `GET`  | `/internal/credentials/orgs/{ocOrgId}` | Projection only. Used by BFF status. |
| `GET`  | `/internal/credentials/orgs/{ocOrgId}/identity` | Identity-only fields plus `repoUrl`/`defaultBranch` per project. The dispatch path's replacement for the dead `GetCredentials.Identity` field. |
| `DELETE` | `/internal/credentials/orgs/{ocOrgId}` | Disconnect path: status flip, OpenBao GC of `secret/asdlc/{ocOrgId}/...`. Idempotent. |
| `GET`  | `/internal/credentials/orgs/{ocOrgId}/webhook-secrets` | PAT mode: returns `{secrets}` from row, current-first. App mode: returns the `_platform/github/app/webhook_secret` list. The receiver's `GitServiceSecretProvider` consumes this with a 30 s cache + miss-then-refetch (Phase 0 §6.4). |
| `POST` | `/internal/credentials/orgs/{ocOrgId}/webhook-secrets` | PAT only. Append a new secret. App-mode call → 409 (rotation lives in `_platform`). |
| `DELETE` | `/internal/credentials/orgs/{ocOrgId}/webhook-secrets/{secret}` | PAT only. Drop a secret. |
| `POST` | `/internal/credentials/orgs/{ocOrgId}/mint-build` | Body `{repoSlug}`. **Validates `repoSlug` belongs to `ocOrgId`** (rejects 404 if no active `git_repositories` row matches); mints a fresh token; writes to `secret/asdlc/{ocOrgId}/git/{repoSlug}`; returns `{secretRefName, expiresAt}`. The BFF calls this immediately before `CreateWorkflowRun`. |

`mint-build` error contract:

| Status | Cause | BFF behaviour |
|---|---|---|
| 200 | Token minted, written | Proceed to `CreateWorkflowRun` |
| 404 | `(ocOrgId, repoSlug)` doesn't match any active repo | Skip this component; log; do not retry |
| 409 | Org `status='disconnected'` or `'suspended'` | Skip; mark task as `abandoned` (disconnect) or leave `pending` (suspend) |
| 429 | App rate limit | Backoff per `Retry-After`, retry up to 3x within build watcher's retry budget |
| 503 | OpenBao unreachable | Backoff + retry; surfaces to `failed` after 3 attempts |
| 500 | Other | Treat as transient; retry once, then `failed` |

### 5.3 git-service — workspace credential refresh

Unchanged shape from Phase 0. Two contract additions:

- The bearer's claims now include `org`. The handler refuses if `claims.org != ComponentTask.OrgID` for the `tid`.
- The response echoes `tid` in the body. The workspace `update-git-identity` hook refuses to rewrite `.git/config` if `response.tid != $ASDLC_TASK_ID` (an env var written into the workspace at provision). Belt and suspenders against bearer mix-ups.

### 5.4 GitHub App configuration (operational)

Recorded in `docs/operations/github-app.md`, not in code:

- Permissions: `Administration: Write`, `Contents: Write`, `Issues: Write`, `Pull requests: Write`, `Metadata: Read`. (No `Webhooks: Write` — App-mode delivery is App-wide.)
- Subscribe to events: `Installation`, `Installation repositories`, `Pull request`, `Push`, `Issue comment`.
- Webhook URL: `${PLATFORM_BFF_URL}/webhooks/github`. Callback URL: `${PLATFORM_BFF_URL}/api/v1/github/app/callback`.
- Webhook secret stored at `secret/asdlc/_platform/github/app/webhook_secret` as a JSONB list `[{secret, added_at}]` for rotation.
- App ID, client ID, private key at `secret/asdlc/_platform/github/app/{app_id, client_id, private_key}`.

---

## 6. Credential modes — implementation

The Phase 0 `Credential` interface (4 observables) is the only seam through which kind-specific behaviour reaches call sites. Phase 2 introduces two implementations.

### 6.1 App mode

```go
// git-service/pkg/credentials/app_installation.go
type appInstallationCred struct {
    appID          int64
    installationID int64
    accountLogin   string                   // resolved at INSERT, refreshed daily by the validator
    botIdentity    Identity                 // App's bot identity, loaded once at startup
    selectedRepos  []string                 // synced via installation_repositories events
    minter         *appTokenMinter
}

func (c *appInstallationCred) Token(ctx context.Context) (string, time.Time, error) {
    return c.minter.MintForInstallation(ctx, c.installationID)
}
func (c *appInstallationCred) Identity() Identity              { return c.botIdentity }
func (c *appInstallationCred) RepoOwner() string               { return c.accountLogin }
func (c *appInstallationCred) WebhookStrategy() WebhookStrategy { return WebhookPlatform }
```

The `appTokenMinter` (one instance per git-service process, in `git-service/pkg/credentials/app_token_minter.go`) owns:

- The parsed `*rsa.PrivateKey`, the **only** consumer in the codebase. The signing function is `(*appTokenMinter).signAppJWT(now time.Time) (string, error)` — RS256, 10-minute exp, `iss=appID`. Cached in process and re-signed on expiry.
- The per-installation token cache: `map[int64]appTokenEntry{token, expiresAt}`, deadline-keyed with a 5-minute safety margin (`time.Until(expiresAt) > 5*time.Minute` to hit). Concurrent mints serialised by `singleflight.Group` keyed by `installationID`. On 401 from a cached token (rare; e.g. install just suspended), the entry is evicted and re-minted once.

### 6.2 User-PAT mode

```go
// git-service/pkg/credentials/user_pat.go
type userPATCred struct {
    ocOrgID     string
    githubLogin string
    identity    Identity
    flight      *singleflight.Group         // shared across all userPATCred instances
    store       OpenBaoStore
}

func (c *userPATCred) Token(ctx context.Context) (string, time.Time, error) {
    v, err, _ := c.flight.Do(c.ocOrgID, func() (any, error) {
        return c.store.Get(ctx, c.ocOrgID, "github/pat")
    })
    if err != nil { return "", time.Time{}, err }
    return string(v.([]byte)), time.Time{}, nil    // long-lived; no expiry
}
func (c *userPATCred) Identity() Identity              { return c.identity }
func (c *userPATCred) RepoOwner() string               { return c.githubLogin }
func (c *userPATCred) WebhookStrategy() WebhookStrategy { return WebhookPerRepo }
```

No plaintext cache. `singleflight` collapses concurrent reads to one OpenBao call; OpenBao at-rest encryption + KV v2 transit handle the protection. PAT replace overwrites the OpenBao value; the next `Token()` call sees the new PAT immediately, on every replica, with no cross-process invalidation step.

### 6.3 The Resolver

```go
// git-service/pkg/credentials/resolver.go
func (r *orgResolver) Resolve(ctx context.Context, ocOrgID string) (Credential, error) {
    if ocOrgID == "" {
        return nil, ErrOrgIDRequired                  // multi-tenant invariant
    }
    rec, err := r.loadOrgCredential(ctx, ocOrgID)
    if err != nil { return nil, err }
    if rec.Status != "active" {
        return nil, &OrgNotActiveError{ocOrgID, rec.Status}
    }
    switch rec.Kind {
    case "app-installation":
        return &appInstallationCred{
            appID:          r.minter.appID,
            installationID: rec.InstallationID,
            accountLogin:   rec.GithubLogin,
            botIdentity:    r.botIdent,
            selectedRepos:  rec.SelectedRepos,
            minter:         r.minter,
        }, nil
    case "user-pat":
        return &userPATCred{
            ocOrgID:     ocOrgID,
            githubLogin: rec.GithubLogin,
            identity:    Identity{rec.IdentityName, rec.IdentityEmail, rec.IdentityLogin},
            flight:      r.patFlight,
            store:       r.store,
        }, nil
    default:
        return nil, fmt.Errorf("unknown credential kind: %s", rec.Kind)
    }
}
```

The Phase 0 `PlatformPATResolver` and `platform_pat.go` are deleted.

### 6.4 Connect — App mode

```
BFF: POST /api/v1/orgs/{ocOrgId}/github/app/start
  · validate user is org admin
  · sign JWT state{ocOrgId, actor:userId, exp:now+15m} with OAUTH_STATE_SIGNING_KEY
  · build installUrl = https://github.com/apps/{slug}/installations/new?state=<jwt>
  · return { installUrl }

User → GitHub install → GitHub redirects to:
GET /api/v1/github/app/callback?installation_id=...&state=<jwt>

BFF callback:
  · verify JWT (sig, exp); refuse on invalid
  · acquire pg_advisory_xact_lock(hashtext('install:'||installation_id))   // §6.4 race fix
  · POST git-service /internal/credentials/orgs/{ocOrgId} {kind:'app-installation', installationId}
  · 302 → console settings page

git-service handler (still inside the lock from caller? — no, the lock is BFF-side):
  · git-service acquires its own pg_advisory_xact_lock(hashtext('install:'||installationId))
  · check existing org_credentials WHERE installation_id = ?
      · row exists with same oc_org_id and status='active' → return projection (idempotent)
      · row exists with different oc_org_id → 409
      · no row → mint App JWT, fetch GET /app/installations/{id} for account.login,
                 INSERT org_credentials with App identity from r.botIdent
  · return projection

Concurrent webhook installation.created:
  · receiver acquires pg_advisory_xact_lock(hashtext('install:'||installation_id))
  · re-checks org_credentials WHERE installation_id = ?
  · if found → ack 200, no-op (callback won the race)
  · if not found → ack 200, no-op (no `pending_installations` table; recovery is "click Connect again")
```

The `install:` advisory lock keys both the BFF callback's git-service call and the webhook handler. Both serialize through Postgres; whichever commits first, the other reads the committed row and no-ops.

### 6.5 Connect — PAT mode (single idempotent POST)

```
BFF: POST /api/v1/orgs/{ocOrgId}/github/pat  body {pat, githubLogin}
  · validate user is org admin
  · forward to git-service POST /internal/credentials/orgs/{ocOrgId} {kind:'user-pat', pat, githubLogin}

git-service handler:
  · acquire pg_advisory_xact_lock(hashtext('org:'||ocOrgId))
  · check existing row:
      · row exists with kind='app-installation' and status='active' → 409 (mode-fixed)
      · row exists with kind='user-pat' and status='active' → REPLACE flow (re-validate, recompute identity, audit)
      · no row → CREATE flow
  · validate PAT (run for both create and replace):
      · GET https://api.github.com/user with the new PAT — extract identity {login, name, email}
      · IF githubLogin != identity.login: GET /user/memberships/orgs/{githubLogin}
          · membership state must be 'active'
          · refuse 400 if 404 or non-active
      · permission probe: GET /repos/{githubLogin}/{ANY_OWNED_REPO_ON_GITHUB_LOGIN}
          · resolves "PAT can read repos under githubLogin"
          · if no repos exist yet (fresh org): skip probe, accept; first real repo create surfaces failure
      · refuse 400 with specific cause string if any check fails
  · CREATE only:
      · generate 32-byte random webhook secret
      · openBaoStore.Put(ctx, ocOrgId, "github/pat", pat)
      · INSERT org_credentials with identity_changed_at=NULL
  · REPLACE only:
      · IF identity.login != stored identity_login:
          · UPDATE org_credentials SET prev_identity_login=stored, identity_changed_at=now(),
                  identity_login, identity_name, identity_email = new values
      · openBaoStore.Put(ctx, ocOrgId, "github/pat", pat)        // overwrites; next Token() reads new value
      · UPDATE org_credentials SET last_validated_at=now()
  · return projection
```

No temporary repo create-and-delete probe — it leaves visible artifacts on the user's account. The membership and repo-read probes are sufficient signal; PAT scope-failures on actual create surface at first project provisioning with a clear error.

The cleartext PAT is never logged, never returned over the wire after this call, and never stored outside OpenBao.

### 6.6 Identity drift on PAT replace

If `identity.login` changed:
- `prev_identity_login` and `identity_changed_at` are recorded as columns on `org_credentials` (no separate audit table — §4.2).
- Console settings shows `IdentityDriftBanner`: "Identity changed from `<prev>` to `<current>` on `<date>`."
- In-flight tasks: the `update-git-identity` hook in the workspace credential helper detects drift on the next `credhelper.sh` invocation and rewrites `.git/config` user fields. The hook also requires `response.tid == $ASDLC_TASK_ID` (the workspace's bound task) before rewriting — preventing identity rewrites from a misrouted bearer.
- A commit currently mid-`git commit` keeps the old identity for that one commit. Best-effort.

### 6.7 Disconnect

```
BFF: DELETE /api/v1/orgs/{ocOrgId}/github

Phase A — soft-stop (no lock, runs in milliseconds):
  · UPDATE org_credentials SET status='disconnecting' WHERE oc_org_id=? AND status='active'
    (one-row UPDATE; if it returns 0 rows, treat as already-disconnected and 200 idempotent)
  · returns immediately to user; subsequent dispatch refuses on status check

Phase B — best-effort comments (no lock; bounded work):
  · enumerate ComponentTask in non-terminal status under projects of this org
  · for each, post `gh issue comment` "abandoned: org disconnected" using the still-active credential
    (resolver still works; status='disconnecting' is treated as 'active' by the resolver's
     transition view but refused for new dispatches at the controller layer)
  · failures logged, not retried

Phase C — cascade (per-task transactions, each holds only the task lock):
  · for each non-terminal ComponentTask, in its own transaction:
      · pg_advisory_xact_lock(hashtext('task:'||id))
      · projector.Apply(task, "org.disconnected") → status='abandoned'
      · workspace cleanup queued

Phase D — finalize (org-scoped advisory lock, short critical section):
  · pg_advisory_xact_lock(hashtext('org:'||ocOrgId))
  · UPDATE org_credentials SET status='disconnected'
  · openBaoStore.Delete loop: secret/asdlc/{ocOrgId}/github/* and secret/asdlc/{ocOrgId}/git/*
```

The four-phase split avoids holding the org lock during slow GitHub calls (Phase B) or while iterating thousands of tasks (Phase C). The intermediate `'disconnecting'` status gates new dispatches without blocking event-handler reads. The lock-acquire order is single-scope-per-phase: org → never wait on task; task → never wait on org. This is the same lock-discipline rule Phase 0 §8.2 established for the per-event handlers.

The transition table in `services/task_state.go` gains:

```go
{TaskStatusInProgress,     TaskStatusAbandoned, "org.disconnected"},
{TaskStatusReadyForReview, TaskStatusAbandoned, "org.disconnected"},
{TaskStatusPending,        TaskStatusAbandoned, "org.disconnected"},
{TaskStatusInProgress,     TaskStatusAbandoned, "repo.unselected"},
{TaskStatusReadyForReview, TaskStatusAbandoned, "repo.unselected"},
{TaskStatusPending,        TaskStatusAbandoned, "repo.unselected"},
```

### 6.8 Reach reconciliation — App mode

`installation_repositories.added`/`removed` events update `org_credentials.selected_repos`. Same two-phase shape as disconnect:

```
Phase A — JSON merge (org lock, short critical section):
  · pg_advisory_xact_lock(hashtext('org:'||ocOrgId))
  · UPDATE org_credentials SET selected_repos = <merged> WHERE oc_org_id=?
  · UPDATE last_validated_at
  · COMMIT — release lock

Phase B — confirm + cascade (no org lock; per-task transactions):
  · ON `removed` ONLY: confirm via GitHub before cascading.
      · mint App token, GET /installation/{id}/repositories
      · take the intersection of "currently selected" with "tasks targeting"
      · forged-event blast radius: a leaked App webhook secret cannot abandon
        tasks in any org without GitHub agreeing the install no longer reaches them.
  · for each affected ComponentTask in non-terminal status, in its own transaction:
      · pg_advisory_xact_lock(hashtext('task:'||id))
      · projector.Apply(task, "repo.unselected") → status='abandoned'
```

Phase B's `GET /installation/{id}/repositories` round-trip is the `M1` mitigation against forged App webhook secret events triggering mass abandonments. It costs one API call per `removed` event; rare event, cheap fix.

### 6.9 Suspend / unsuspend

`installation.suspend`:
- `UPDATE org_credentials SET status='suspended'`.
- New dispatches refuse with `ErrOrgSuspended` (controller layer).
- In-flight tasks **do not transition**. Their next token mint may fail; if it does, the agent's run aborts and the projector advances `* → failed`. This is operator-visible and recoverable — matching the operational shape (operator suspended the install for a reason; tasks should not silently abandon).

`installation.unsuspend`:
- `UPDATE org_credentials SET status='active'`.
- Failed tasks remain `failed`; they are re-dispatched as new tasks, not auto-resurrected.

### 6.10 Periodic validator

A small ticker (every 24h) in git-service:

- For each `org_credentials` row with `status='active'`:
  - **App mode:** mint a token, `GET /app/installations/{id}` — refresh `account.login` if changed (rename drift), `last_validated_at = now()`. On 404/410/auth error: flip to `'disconnected'` via the same disconnect cascade as `installation.deleted`.
  - **PAT mode:** `GET /user` with the cached PAT. On 401: flip to `'disconnected'` (the lazy revocation path from evolution-doc §6.2). On 200 with changed identity: reuse the drift mechanism (`identity_changed_at`, banner). Otherwise update `last_validated_at`.

Single-flight via `pg_advisory_xact_lock(hashtext('validator'))` so multiple replicas don't double-validate.

---

## 7. OpenBao integration

### 7.1 The wrapper

Phase 0 declared the interface; Phase 2 implements it.

```go
// git-service/pkg/credentials/openbao_store.go
type openBaoStore struct {
    client *vault.Client
    mount  string                                     // "secret"
    owner  string                                     // "asdlc-git-service" — KV v2 metadata tag
}

var orgIDValidator = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`)

func validateOrgID(ocOrgID string) error {
    if ocOrgID == "" {
        // Programmer error — caller forgot to thread the parameter.
        // Panic so the bug surfaces in dev/CI, not as a silent cross-org.
        panic("ocOrgID required (multi-tenant invariant)")
    }
    if ocOrgID == "_platform" || strings.HasPrefix(ocOrgID, "_") {
        return ErrOrgIDInvalid     // forbid escape into _platform/ namespace
    }
    if !orgIDValidator.MatchString(ocOrgID) {
        return ErrOrgIDInvalid     // DNS-label shape, matches OC namespace constraints
    }
    return nil
}

func (s *openBaoStore) path(ocOrgID, key string) (string, error) {
    if err := validateOrgID(ocOrgID); err != nil { return "", err }
    return fmt.Sprintf("asdlc/%s/%s", ocOrgID, key), nil
}

func (s *openBaoStore) platformPath(key string) string {
    return fmt.Sprintf("asdlc/_platform/%s", key)
}
```

Two design choices:

- **Empty `ocOrgID` panics** because it's a programmer error — call sites that fail to thread the parameter are bugs, and a panic surfaces in CI rather than in production as silent cross-org behaviour. **Malformed `ocOrgID` returns an error** because that's a runtime input failure (a misconfigured caller, a malformed webhook payload that escaped earlier validation, a corrupted DB row) — the receiver should answer 400/500, not crash git-service.
- **`_platform` and underscore-prefixed values are explicitly rejected** so a per-org code path cannot escape into the platform namespace. The `_platform` paths are constructed only via `platformPath`, which has no per-org caller surface — it is reachable only from the App-key startup loader. The Phase 0 import fence backs this with compile-time enforcement.

### 7.2 PAT storage

Per-org user PATs live at `secret/asdlc/{ocOrgId}/github/pat`. KV v2 metadata records `managed-by: "asdlc-git-service"` and `kind: "user-pat"` (mirroring agent-manager `secretmanagersvc/providers/openbao/client.go` line 32). Deletion is ownership-gated.

### 7.3 App private-key custody

Stored at:

- `secret/asdlc/_platform/github/app/app_id`
- `secret/asdlc/_platform/github/app/client_id`
- `secret/asdlc/_platform/github/app/private_key` (PEM RSA)
- `secret/asdlc/_platform/github/app/webhook_secret` (JSONB list `[{secret, added_at}]`)

git-service startup sequence (idempotent, runs once before health-check ready):

1. Open vault client; verify reachability (§7.5).
2. `platformPath("github/app/private_key")` → read PEM → parse `*rsa.PrivateKey`.
3. Same for `app_id`, `client_id`, `webhook_secret`.
4. `appTokenMinter.signAppJWT(now)` → `GET /app` → fetch and cache the App's bot identity as `Identity{Name, Email, Login}`.
5. Health-check passes.

Rotation: GitHub permits two active App private keys. Operational runbook (`docs/operations/github-app-rotation.md`) — write the new key to OpenBao, restart git-service (rolling deploy), revoke the old key on GitHub.

### 7.4 The token cache

One cache: `appTokenCache` (App-installation tokens), per-installation, deadline-keyed. PAT mode has no plaintext cache; reads go through `singleflight` to OpenBao.

```go
// git-service/pkg/credentials/token_cache.go
type appTokenCache struct {
    mu      sync.Mutex
    entries map[int64]appTokenEntry
    flight  *singleflight.Group
}
type appTokenEntry struct { token string; expiresAt time.Time }
```

Cache hit if `time.Until(entry.expiresAt) > 5*time.Minute`. Miss serialises via `singleflight` keyed by `installationID`. On 401 from a cached token, the entry is evicted and re-minted once.

Process-local. Restart drops the cache. Multi-replica git-service has each replica's cache populated independently (small overhead, accepted for simplicity over distributed cache).

### 7.5 OpenBao reachability gate on startup

Per evolution-doc §9.13. The Kubernetes readiness probe holds the pod from receiving traffic until OpenBao is reachable. Combined with rolling-deploy `maxSurge=1, maxUnavailable=0`, this is the architectural property — not a runbook discipline — that prevents new pods coming online with empty App-token caches during an OpenBao outage. For local dev, git-service polls OpenBao at startup and fails fast (with retry over 30s) if unreachable.

### 7.6 OpenBao downtime behaviour

- **App mode:** in-flight installation tokens (cached, ≤1 h) keep working. New mints fail. Tasks hitting auth on a fresh-mint path fail; webhook redelivery retries.
- **PAT mode:** every read goes to OpenBao. An outage immediately fails new operations. The 30-min in-process cache from the prior draft was deliberately dropped — a longer retention bought outage tolerance at a non-trivial security cost (process-memory exposure window) and a complexity cost (cache invalidation on PUT). Honest behaviour is "OpenBao up = system up; OpenBao down = system blocks PAT operations." The reachability gate (§7.5) makes deploys-during-outage a non-event.
- **Build pods:** a pod that starts during an OpenBao outage cannot resolve its `SecretReference`. The `WorkflowRun` controller surfaces a build failure; the BFF observes it and retries with a fresh mint (3-attempt budget, exponential backoff, then `failed`).

### 7.7 GC contract

Two ownership classes:
- `secret/asdlc/{ocOrgId}/github/pat` — written by `POST /internal/credentials/orgs/{ocOrgId}` PAT mode, deleted by `DELETE`.
- `secret/asdlc/{ocOrgId}/git/{repoSlug}` — written by `mint-build`, deleted by repo-deletion hook and by `DELETE`.

Both are best-effort and idempotent; failure is logged. A periodic GC sweep (hourly) lists `secret/asdlc/{ocOrgId}/...` for orgs whose `status='disconnected'` and removes any keys missed by the disconnect path.

---

## 8. Webhook receiver — multi-tenant routing

The Phase 0 receiver pipeline is preserved; Phase 2 fills in the routing data.

### 8.1 Routing key extraction

```go
// asdlc-service/services/webhook/routing.go (Phase 0 has routing_key.go; this extends it)
func extractRoutingKey(event string, payload []byte) (RoutingKey, error) {
    switch event {
    case "installation", "installation_repositories":
        var p struct{ Installation struct{ ID int64 } }
        json.Unmarshal(payload, &p)
        return RoutingKey{Kind: "installation", InstallationID: p.Installation.ID}, nil
    case "pull_request", "push", "issue_comment", "issues":
        var p struct{ Repository struct{ FullName string } }
        json.Unmarshal(payload, &p)
        return RoutingKey{Kind: "repository", RepoFullName: p.Repository.FullName}, nil
    default:
        return RoutingKey{Kind: "platform"}, nil    // ignored; Phase 0 audit-only
    }
}
```

### 8.2 ocOrgId resolution

```go
func (r *resolver) ResolveOrgID(ctx context.Context, key RoutingKey) (string, error) {
    switch key.Kind {
    case "installation":
        return r.gitservice.OrgIDByInstallationID(ctx, key.InstallationID)
    case "repository":
        repo, err := r.gitservice.RepoByFullName(ctx, key.RepoFullName)
        if err != nil { return "", err }
        return repo.OrgID, nil
    case "platform":
        return "", ErrNoRoutingKey                     // misrouted; receiver returns 404
    }
}
```

### 8.3 HMAC validation against per-org secrets

The Phase 0 `SecretProvider` interface is unchanged. Phase 2 swaps the implementation to a `GitServiceSecretProvider`:

```go
func (p *GitServiceSecretProvider) Secrets(ctx context.Context, ocOrgID string, force bool) ([][]byte, error) {
    return p.cache.Get(ctx, ocOrgID, force)
}
// cache misses fetch from /internal/credentials/orgs/{ocOrgID}/webhook-secrets
// git-service's handler branches by kind: PAT mode reads org_credentials.webhook_secrets;
// App mode reads _platform/github/app/webhook_secret. Both return as ["<hex>", ...].
```

The kind branch lives **inside git-service**, not in the receiver. The receiver always calls `provider.Secrets(ctx, ocOrgID, force)`. Miss-then-refetch on HMAC mismatch (Phase 0 §6.4) carries forward unchanged.

### 8.4 Receiver rate-limit on forced refetch

Per evolution-doc §9.12: token-bucket rate-limit the `force=true` refetch path per `(routingKey, sourceIP)`: 1 refetch per second, burst of 5. Legitimate rotation uses one refetch per sender; a forged stream is bounded.

### 8.5 New event handlers

| Event | Action | Handler effect |
|---|---|---|
| `installation` | `created` | Re-check `org_credentials` under `install:`-keyed advisory lock; ack 200, no-op if found, ack 200 no-op if absent (callback recovery is "click Connect again"). |
| `installation` | `deleted` | Trigger the disconnect cascade (§6.7) via the same code path as `DELETE /api/v1/orgs/.../github`. |
| `installation` | `suspend` | `UPDATE status='suspended'`. In-flight tasks remain in current status; new dispatches refuse. |
| `installation` | `unsuspend` | `UPDATE status='active'`. |
| `installation_repositories` | `added` | Phase A only (atomic JSON merge under org lock). No cascade. |
| `installation_repositories` | `removed` | Two-phase: Phase A merge, Phase B confirm via `GET /installation/{id}/repositories`, cascade to `'abandoned'`. |

All `installation.*` handlers acquire `pg_advisory_xact_lock(hashtext('install:'||installation_id))` for the `org_credentials` mutation. The org-scoped lock (`hashtext('org:'||ocOrgId)`) is reserved for Phase A of multi-row org-state updates (selected_repos JSON merge, status flips from disconnect).

---

## 9. OC build credentials & App-token rotation

### 9.1 SecretReference per repo

- Name: `git-{ocOrgId}-{repoSlug}`. Slug = `strings.ToLower(strings.ReplaceAll(repoFullName, "/", "-"))`, max 63 chars (DNS label); length-trimmed via SHA-256 suffix if needed.
- Created at repo provisioning, in OC namespace `{ocOrgId}` (one OC namespace per OC org). Idempotent on `(ocOrgId, repoSlug)`.
- The `Component`'s `repository.secretRef` is set to this name at dispatch and never changes.
- The CR's `vaultPath` field is `secret/asdlc/{ocOrgId}/git/{repoSlug}`. The OC `SecretReference` controller resolves this on K8s `Secret` materialisation.

**SecretReference CRD presence** is verified at deployment time, not runtime: §14.2 step 1.5 adds `secretReferences.enabled=true` to OC helm values if not already set, and the setup script's preflight checks `kubectl get crd secretreferences.openchoreo.dev` before continuing.

### 9.2 Pre-WorkflowRun mint-then-write

```go
// asdlc-service/services/workflowrun_service.go::TriggerForPush — extended
for _, c := range components {
    if c.LastBuildSHA == sha { continue }

    mint, err := s.gitservice.MintBuildToken(ctx, c.OcOrgID, c.RepoSlug)
    switch {
    case err == nil:
        // proceed
    case errors.Is(err, gitservice.ErrRepoNotInOrg):           // 404 from server-side ownership check
        s.log.Warnw("mint-build refused: repo/org mismatch", ...)
        continue
    case errors.Is(err, gitservice.ErrOrgDisconnected):        // 409
        s.markTaskAbandoned(ctx, c, "org disconnected mid-trigger")
        continue
    default:
        s.log.Errorw("mint-build transient", ...); continue    // build watcher's retry budget covers this
    }

    runName := fmt.Sprintf("%s-%d", c.Name, time.Now().UnixMilli())
    if err := s.ocClient.CreateWorkflowRun(ctx, c.OcOrgID, ...); err != nil {
        continue
    }
    s.componentRepo.UpdateLastBuildSHA(ctx, c.ID, sha)
}
```

`MintBuildToken` inside git-service:

1. Validate `(ocOrgId, repoSlug)` against `git_repositories` — refuse 404 if no active row.
2. Resolve credential via the org resolver; refuse 409 if `status != 'active'`.
3. `cred.Token(ctx)` → fresh token.
4. `openBaoStore.Put(ctx, ocOrgId, "git/"+repoSlug, []byte(token))`.
5. Return `{secretRefName: "git-"+ocOrgId+"-"+repoSlug, expiresAt}`.

The BFF only sees `secretRefName` and `expiresAt`. The token itself never crosses the boundary.

### 9.3 Build retry on auth failure

Per evolution-doc §6.3.1. The build watcher classifies `WorkflowRun` failures: `git_clone_failed_auth` triggers re-mint and re-create of the run. Retry budget: 3 attempts. After 3, the task transitions to `'failed'` with `ErrorMessage = "build auth retry budget exceeded"`.

The retry's mint uses the App's current installation token, which has its own ~1h TTL. If OC queue depth + build runtime exceeds the App-token TTL chronically, that's an OC-side capacity concern, not an ASDLC token-rotation concern; the 3-attempt bound makes the failure mode honest rather than infinite-loop.

### 9.4 Concurrent builds against the same repo

Multiple components in the same project pushed in the same merge call `MintBuildToken` concurrently. The OpenBao path `secret/asdlc/{ocOrgId}/git/{repoSlug}` is overwritten by each; tokens are interchangeable within their TTL. No locking needed.

The `singleflight.Group` in `appTokenMinter` (keyed by `installationID`) prevents redundant mints; the OpenBao writes are individual but cheap.

---

## 10. Console UI — org GitHub settings

GitHub connection lives under **Organization → Settings → Integrations → GitHub**. One settings surface per `ocOrgId`. Phase 2 ships only this single integration tab; the hub is shaped to take more later (members, billing) without re-flow.

### 10.1 Information architecture

Route: `/organizations/{orgId}/settings/github`. Reached from a "Settings" entry in the org-level sidebar (the `Settings` icon is already imported in `console/src/layouts/AsdlcLayout.tsx`, just unused). Settings is a left-rail hub inside the page; "Integrations → GitHub" is the only entry in Phase 2.

Permission: org admin only. Non-admins see the panel **read-only** — status visible; connect/replace/disconnect buttons hidden. The org-membership check the BFF applies on the connect/disconnect routes is the source of truth; the UI hide is convenience, not enforcement.

The page reads everything from `GET /api/v1/orgs/{ocOrgId}/github` (§5.1) which returns `{kind, identity, githubLogin, status, connectedAt, lastValidatedAt, identityChangedAt, selectedRepos?}`. The endpoint **never** returns the token — there is no UI surface that could leak it.

### 10.2 Mode choice — App vs PAT

Two modes, **GitHub App (preferred)** and **PAT**. Mode is fixed at connect time (§5.1, §6.5). The UI mirrors that hard rule:

- **Not connected** — both modes render side by side. App is the bigger, primary card; PAT is the smaller secondary card with the form inline (§10.3).
- **Connected** — only the active mode's panel renders. The inactive mode collapses to a single line "Switch to ..." link that opens the disconnect dialog (since switching = disconnect + reconnect).

### 10.3 Sketch — not connected

```
┌─ Settings ────────┐ ┌─ Integrations → GitHub ───────────────────────────┐
│ Profile           │ │                                                    │
│ Integrations    ● │ │ GitHub Integration                                 │
│   GitHub          │ │                                                    │
│ Members           │ │ Connect this organization to GitHub so ASDLC can   │
│                   │ │ provision repos, open issues, and run agents on    │
│                   │ │ your behalf.                                       │
│                   │ │                                                    │
│                   │ │ Choose a connection method:                        │
│                   │ │                                                    │
│                   │ │ ╔══════════════════════════════════════════════╗   │
│                   │ │ ║ ◉ GitHub App                  recommended    ║   │
│                   │ │ ║                                              ║   │
│                   │ │ ║ • Per-repo access — you choose which repos   ║   │
│                   │ │ ║ • Bot identity (asdlc-bot[bot]) on commits   ║   │
│                   │ │ ║ • Tokens auto-rotate hourly                  ║   │
│                   │ │ ║ • App-wide webhook delivery                  ║   │
│                   │ │ ║                                              ║   │
│                   │ │ ║                  [ Connect GitHub App → ]    ║   │
│                   │ │ ╚══════════════════════════════════════════════╝   │
│                   │ │                                                    │
│                   │ │ ┌──────────────────────────────────────────────┐   │
│                   │ │ │ ○ Personal Access Token                      │   │
│                   │ │ │                                              │   │
│                   │ │ │ For users who can't install a GitHub App     │   │
│                   │ │ │ (org policy, personal accounts).             │   │
│                   │ │ │                                              │   │
│                   │ │ │ • Commits authored as the PAT owner          │   │
│                   │ │ │ • Per-repo webhook at provisioning           │   │
│                   │ │ │ • Scopes: repo, admin:org, admin:repo_hook   │   │
│                   │ │ │                                              │   │
│                   │ │ │ GitHub login / org  [____________________]   │   │
│                   │ │ │ Personal access token [__________________]   │   │
│                   │ │ │                                              │   │
│                   │ │ │                              [ Connect ]     │   │
│                   │ │ └──────────────────────────────────────────────┘   │
└───────────────────┘ └────────────────────────────────────────────────────┘
```

### 10.4 Sketch — connected via GitHub App

```
┌─ Integrations → GitHub ───────────────────────────────────────────┐
│                                                                   │
│ GitHub Integration                                                │
│                                                                   │
│ ✓ Connected via GitHub App                       [ Disconnect ]   │
│                                                                   │
│   Account            myorg                                        │
│   Identity           asdlc-bot[bot]                               │
│                      (commits, comments, PRs use this identity)   │
│   Installation       #12345678                                    │
│   Connected          2026-04-12                                   │
│   Last validated     2026-04-28 (3 min ago)                       │
│                                                                   │
│ Repositories accessible (5)            [ Manage on GitHub → ]     │
│   myorg/service-a                                                 │
│   myorg/service-b                                                 │
│   myorg/web                                                       │
│   myorg/data-pipeline                                             │
│   myorg/infra-tools                                               │
│                                                                   │
│   Reach is managed on GitHub. ASDLC reflects whatever the         │
│   install has access to.                                          │
│                                                                   │
│ ─ Need to use a PAT instead?       [ Switch to PAT → ]            │
│   Disconnects the App and abandons in-flight tasks.               │
└───────────────────────────────────────────────────────────────────┘
```

### 10.5 Sketch — connected via PAT (with identity drift banner)

```
┌─ Integrations → GitHub ───────────────────────────────────────────┐
│                                                                   │
│ GitHub Integration                                                │
│                                                                   │
│ ⚠ Identity changed from "alice" to "alice-new" on 2026-04-22.     │
│   New commits and PRs will use the new identity.   [ Dismiss ]    │
│                                                                   │
│ ✓ Connected via Personal Access Token            [ Disconnect ]   │
│                                                                   │
│   GitHub login       myorg                                        │
│   Identity           alice-new                                    │
│                      Alice Doe <alice@example.com>                │
│   Connected          2026-04-12                                   │
│   Last validated     2026-04-28 (12 hours ago)                    │
│                                                                   │
│ [ Replace PAT ]    Replaces the stored token; re-validates        │
│                    scopes. If the new PAT belongs to a different  │
│                    user, identity drift is recorded.              │
│                                                                   │
│ ─ Switch to GitHub App?            [ Connect GitHub App → ]       │
│   Disconnects the PAT and abandons in-flight tasks.               │
└───────────────────────────────────────────────────────────────────┘
```

### 10.6 Connect — App flow

1. User clicks **Connect GitHub App**.
2. Console calls `POST /api/v1/orgs/{ocOrgId}/github/app/start` and receives `{ installUrl }`. The signed JWT `state` (§4.3) is opaque to the UI; it's already embedded in `installUrl`.
3. Console performs a full-page redirect to `installUrl`. (Not a popup — GitHub's install page can't be iframed and a popup adds focus/state-loss complexity for negligible UX win.)
4. User completes the install on GitHub.com, including the **repository-selection step**. ASDLC does not pre-select repos; reach is whatever the org admin grants.
5. GitHub redirects to `/api/v1/github/app/callback?installation_id&state` (§5.1). The BFF verifies the JWT, calls git-service to persist the credential (§6.4), and 302s to the settings page with `?connected=app`.
6. The settings page reads the projection and renders §10.4.

If the user closes the GitHub tab without completing, no row exists (§4.4). On the next visit they click **Connect** again — GitHub recognises any partial install and lets the flow finish; if not, the user re-installs cleanly.

### 10.7 Connect — PAT flow

1. User pastes `githubLogin` and `pat`. The PAT input is `type="password"` with a one-shot reveal toggle.
2. Console calls `POST /api/v1/orgs/{ocOrgId}/github/pat`. The validation chain (§6.5) runs server-side; on failure the UI surfaces the specific cause string returned by git-service inline next to the affected field — examples:
   - `"PAT is not a member of org 'myorg' (404)"` → field-level error on the GitHub login input.
   - `"PAT scope check failed: cannot read repos under 'myorg'"` → field-level error on the PAT input.
3. On success the panel re-renders to the connected state.

The PAT is **never echoed back** after submit — the projection endpoint does not return it. **Replace** opens the same form (label changes to "New personal access token") and posts to the same endpoint, which idempotently replaces (§6.5).

### 10.8 Repository list (App mode)

The connected-App panel lists `selectedRepos`. The list is informational only; ASDLC does not control reach — that lives in GitHub's "Configure" page for the install. The "Manage on GitHub →" link opens `https://github.com/organizations/{login}/settings/installations/{installation_id}` in a new tab.

The list updates via webhook → projection. The page polls `GET /api/v1/orgs/{ocOrgId}/github` every 30 s while open; SSE is not worth the plumbing for an event the user looks at maybe twice a year.

### 10.9 Banners

- **`IdentityDriftBanner`** (PAT mode only): renders above the panel when `identityChangedAt IS NOT NULL`. Text: "Identity changed from `<prevIdentityLogin>` to `<identityLogin>` on `<date>`. New commits and PRs will use the new identity." Dismissed via `localStorage` keyed on `ocOrgId + identityChangedAt` ISO string — re-triggers on the next drift event without needing server-side dismissal state.
- **`ReachReconciliationBanner`** (App mode only): renders for ≤24 h after the most recent `repo.unselected` cascade. Text: "Repository selection changed on GitHub. `<N>` in-flight tasks were abandoned." A "View tasks" link opens a dialog listing the abandoned tasks (`GET /api/v1/orgs/{ocOrgId}/tasks?status=abandoned&cause=repo.unselected&since=<24h>`). Same `localStorage` dismissal pattern.

### 10.10 Disconnect

Click **Disconnect** opens a confirmation dialog:

> Disconnecting will abandon `<N>` in-flight tasks for this organization. Builds already running will continue. You can reconnect afterwards in either mode.
>
> [ Cancel ]   [ Disconnect ]

`<N>` is fetched from the projection endpoint (count of non-terminal tasks under this org). On confirm, console calls `DELETE /api/v1/orgs/{ocOrgId}/github`. The dialog stays open with a progress indicator until the call returns; Phase A of the cascade (§6.7) is sub-second, so user-perceived latency is fine. Phases B–D run async server-side.

After success the panel returns to the not-connected state (§10.3). The "Switch to ..." entry in the connected panel piggybacks on this dialog with the wording "Disconnect and switch to PAT" (resp. App) — same call, same cascade, then the user clicks **Connect** in the other mode.

### 10.11 Read-only and error states

- **Suspended** (App mode only, `status='suspended'`): banner "GitHub App is suspended on the GitHub side. New work is blocked until the install is unsuspended on GitHub." No connect/disconnect buttons; "Manage on GitHub →" link visible.
- **Disconnected** (`status='disconnected'`): rare — the row is wiped at the end of the disconnect cascade (§6.7 Phase D). If the page lands here, treat as not-connected.
- **Token revoked / 401 from validator**: banner "GitHub credential is no longer valid (revoked or expired). Reconnect to resume." Buttons: **Reconnect** (re-runs the connect flow for the current mode), **Disconnect**.
- **OpenBao unreachable** (`GET .../github` returns 503): banner "Settings temporarily unavailable. Try again in a few minutes." with a **Retry** button. No mutating buttons exposed.
- **Non-admin user**: banner "You do not have permission to manage GitHub integration for this organization." Status block visible; all buttons hidden.

### 10.12 Components and routes

| File | Purpose |
|---|---|
| `console/src/pages/OrgSettingsLayout.tsx` | Settings hub shell — left rail with section list, `<Outlet/>` for active section. Phase 2 ships one entry. |
| `console/src/pages/OrgGitHubSettings.tsx` | The integration page. Reads projection; renders §10.3, 10.4, or 10.5 depending on `kind` + `status`; orchestrates connect/disconnect/replace flows. |
| `console/src/components/ConnectAppButton.tsx` | Triggers `POST /github/app/start`, redirects to `installUrl`. |
| `console/src/components/ConnectPATForm.tsx` | The PAT form (create + replace). Renders field-level errors from the validation chain. |
| `console/src/components/RepoListPanel.tsx` | App-mode reach list with the "Manage on GitHub →" link. |
| `console/src/components/IdentityDriftBanner.tsx` | §10.9. |
| `console/src/components/ReachReconciliationBanner.tsx` | §10.9. |
| `console/src/services/api/orgGithub.ts` | Typed client for `/api/v1/orgs/{ocOrgId}/github*`. |

Routes added in `App.tsx`:

```tsx
<Route path="/organizations/:orgId/settings" element={<OrgSettingsLayout />}>
  <Route index element={<Navigate to="github" replace />} />
  <Route path="github" element={<OrgGitHubSettings />} />
</Route>
```

The "Settings" entry in `AsdlcLayout`'s sidebar links to `/organizations/{orgId}/settings`.

---

## 11. Local-flow plugin

Phase 0 repurposed `remote-worker/plugin/` as a Claude Code plugin carrying the `asdlc` workflow skill. Phase 2 keeps it as-is — credential-blind by construction (developer's `gh auth` provides the credential), small surface, low-risk version drift. Distribution remains ad-hoc (the repo path); a versioned release flow lands when the SKILL contract first changes.

The console "Implement Locally" dialog is unchanged from Phase 0.

---

## 12. Code organisation (deltas from Phase 0)

```
asdlc-service/
├── api/
│   ├── app.go                                  # CHANGED: + /api/v1/orgs/{ocOrgId}/github/* + unscoped /callback
│   └── org_github_routes.go                    # NEW
├── controllers/
│   └── org_github_controller.go                # NEW: connect/status/disconnect handlers (incl. signed-JWT state)
├── services/
│   ├── webhook/
│   │   ├── routing.go                          # CHANGED: extract + ResolveOrgID against git-service
│   │   ├── secrets.go                          # CHANGED: GitServiceSecretProvider replaces EnvSecretProvider
│   │   └── installation_handlers.go            # NEW
│   ├── workflowrun_service.go                  # CHANGED: TriggerForPush calls MintBuildToken pre-create
│   ├── task_state.go                           # CHANGED: + abandoned transitions
│   ├── remote_worker_service.go                # CHANGED: DispatchTasks no longer reads cleartext PAT;
│   │                                            uses GET /internal/credentials/orgs/{ocOrgId}/identity for non-secret fields
│   └── tier_gate.go                            # CHANGED: drop platform-pat special case (kind retired)
├── models/                                     # CHANGED: org_credential.go DELETED (table moved to git-service)
└── (asdlc-service/services/webhook/EnvSecretProvider DELETED)
└── (asdlc-service/clients/openchoreo/component_client.go::CreateGitSecret DELETED)
```

```
git-service/
├── api/
│   ├── credentials_routes.go                   # CHANGED: + /internal/credentials/orgs/{ocOrgId} (POST/GET/DELETE),
│   │                                                       /identity, /webhook-secrets (GET/POST/DELETE), /mint-build
│   └── middleware/
│       └── internal_only.go                    # NEW: shared-secret check on /internal/* routes
├── pkg/
│   └── credentials/
│       ├── credential.go                       # UNCHANGED
│       ├── platform_pat.go                     # DELETED
│       ├── app_installation.go                 # NEW
│       ├── user_pat.go                         # NEW
│       ├── resolver.go                         # CHANGED: orgResolver replaces PlatformPATResolver
│       ├── app_token_minter.go                 # NEW
│       ├── token_cache.go                      # NEW: app-only; PAT mode uses singleflight directly
│       ├── openbao_store.go                    # CHANGED: implementation lands; was interface-only in Phase 0
│       ├── validator.go                        # NEW: 24h periodic ticker (App: GET /app/installations/{id}; PAT: GET /user)
│       └── import_fence_test.go                # CHANGED: + assert no callers of platformPath outside startup loader
├── services/
│   ├── github_client.go                        # CHANGED: every Bearer header reads from resolver, ocOrgId-routed
│   ├── repo_service.go                         # CHANGED: webhook strategy from Credential.WebhookStrategy()
│   └── credential_service.go                   # NEW: HTTP layer; mint-build's repo/org ownership check
├── models/
│   └── org_credential.go                       # NEW (with the §4.1 schema + CHECK constraints)
```

```
asdlc-service/clients/openchoreo/
├── component_client.go                         # CHANGED: CreateGitSecret DELETED
└── secretref_client.go                         # NEW

asdlc-service/clients/gitservice/
└── client.go                                   # CHANGED: + MintBuildToken, OrgIDByInstallationID, RepoByFullName,
                                                #            GetIdentity. GetCredentials DELETED.
```

```
console/
├── src/
│   ├── App.tsx                                 # CHANGED: + /organizations/:orgId/settings/* routes (see §10.12)
│   ├── pages/
│   │   ├── OrgSettingsLayout.tsx               # NEW: settings hub shell (left rail + Outlet)
│   │   └── OrgGitHubSettings.tsx               # NEW: integration page (§10)
│   ├── layouts/AsdlcLayout.tsx                 # CHANGED: + "Settings" sidebar entry
│   ├── components/
│   │   ├── ConnectAppButton.tsx                # NEW
│   │   ├── ConnectPATForm.tsx                  # NEW
│   │   ├── RepoListPanel.tsx                   # NEW: App-mode reach list
│   │   ├── IdentityDriftBanner.tsx             # NEW
│   │   └── ReachReconciliationBanner.tsx       # NEW
│   └── services/api/orgGithub.ts               # NEW

docs/operations/
├── github-app.md                               # NEW
├── github-app-rotation.md                      # NEW
└── openbao-deployment.md                       # NEW
```

---

## 13. Maintainability principles

These extend Phase 0's principles. Every Phase 0 rule still holds.

1. **Org-scope is the active scope.** Every `Resolver.Resolve(ocOrgID)` is a real lookup. Empty `ocOrgID` panics (programmer error); malformed errors (caller-input failure).
2. **No call site branches on credential kind.** A type-switch on `credentials.Credential` outside `pkg/credentials/` fails code review. The four observables suffice; if a caller wants more, extend the interface — don't peek behind it.
3. **The OpenBao wrapper is the multi-tenant boundary.** Path construction internal; `ocOrgID` mandatory; `_platform` reachable only from one named loader; the Phase 0 import fence enforces all of this at build time. ASDLC deliberately diverges from agent-manager here, which imports the vault SDK directly in `git_credentials_service.go`.
4. **git-service is the only token holder** — no exception, after Phase 2. The `GetCredentials` legacy bridge is deleted at every call site (§1.10). After Phase 2, no Bearer header is built outside `git-service/services/github_client.go`, no PAT bytes cross the BFF↔git-service boundary, and no OC `GitSecret` references the platform PAT.
5. **Mode is fixed at connect time.** API-level: `POST` connect refuses cross-mode change with 409. Code-level: the resolver does not transition kinds within a record's lifetime.
6. **Webhook secret scoping branches inside git-service, not in the receiver.** PAT-mode rows carry their own list; App-mode rows are NULL. The `GitServiceSecretProvider` reconciles by kind. The receiver always calls `provider.Secrets(ctx, ocOrgID, force)`.
7. **Cache TTL is a constant.** The App-token 5-minute safety margin lives as a named constant in `pkg/credentials/`, with a comment pointing at evolution-doc §9.10. Don't promote to config without a real complaint.
8. **The pre-WorkflowRun mint step is in the BFF, not in OC.** This preserves §2.1's "git-service is the sole token holder" rule (the BFF sees only `secretRefName`). It also colocates skip-irrelevant-paths filtering and retry-budget logic with the trigger.
9. **Lock discipline.** Never hold two scope-levels at once. Org-scoped (`hashtext('org:'||x)`) writes finish in a short critical section; per-task (`hashtext('task:'||id)`) work runs in separate transactions. The disconnect cascade and reach-reconciliation cascade both honour this.
10. **Bearer carries `org`.** `/credentials/refresh` rejects when `claims.org != ComponentTask.OrgID`. The workspace's identity-rewrite hook also requires `response.tid == $ASDLC_TASK_ID`. Two tripwires; one for credential mix-ups, one for bearer mix-ups.
11. **Log redaction at the boundary.** Tokens and PATs are scrubbed at the log writer (regex for `gh[a-z]_*`, `Bearer ...`, JWT shapes). Audit lines that *must* reference a credential cite its `oc_org_id + kind`, never the token bytes. New mint-build paths are the highest-risk loggers — assert this in code review.

---

## 14. Migration plan

### 14.1 Pre-migration verification

Confirm the Phase 0 seams are present and behaving as documented before starting. Each is named because Phase 2 extends it; if any is missing, fix as a Phase 0 follow-up first.

- `git-service/pkg/credentials/credential.go` — `Credential` and `Resolver` interfaces.
- `git-service/pkg/credentials/openbao_store.go` — `OpenBaoStore` interface.
- `git-service/pkg/credentials/import_fence_test.go` — passes; build-time fence on OpenBao SDK imports.
- `asdlc-service/services/webhook/secrets.go` — `SecretProvider` is the only place HMAC secrets are read.
- `asdlc-service/services/webhook/routing_key.go` — Phase 0's routing-key shape.
- `webhook_deliveries.oc_org_id` column exists on the BFF DB.
- `org_credentials` table exists on the BFF DB with one row, `kind='platform-pat'`, `oc_org_id='platform'`.
- `DEPLOYMENT_TIER` startup gate refuses `tier != dev` for `'platform-pat'`.

### 14.2 Landing order

The chunk lands as one PR sequence. Each step compiles and runs against the dev stack.

1. **OpenBao deployment.** docker-compose adds an OpenBao service; setup script unseals it; access policy granted to git-service.
1.5. **SecretReference CRD installation.** Verify (or enable via `secretReferences.enabled=true` in the OC helm values for the dev k3d cluster). Setup script preflight: `kubectl get crd secretreferences.openchoreo.dev`.
2. **OpenBao client + reachability gate** in git-service. No callers yet; readiness probe holds the pod until OpenBao is reachable.
3. **Schema migrations + dev wipe.** Three writes, in order:
   a. **TRUNCATE** `git_repositories`, `component_tasks`, `webhook_deliveries`, `webhook_payloads` in the BFF DB. Phase 0 already established dev wipes are acceptable; the platform-PAT row's `oc_org_id='platform'` doesn't match real `git_repositories.OrgID` values, so the resolver swap (step 5) breaks every existing dev row otherwise.
   b. **DROP** `org_credentials` from the BFF DB.
   c. **CREATE** `org_credentials` (with §4.1 schema + CHECK constraints) and `git_repositories.oc_secret_ref_name` in the **git-service** DB. Update BFF's projection-read paths and `tier_gate.go` to call `GET /internal/credentials/orgs/{ocOrgId}` instead of reading the local table.
4. **`appTokenMinter` + token cache** in `pkg/credentials/`. Loads the App private key from OpenBao at startup; mints installation tokens on demand. Unit-testable in isolation. The §16 q4 calendar-driven rotation runbook lands as `docs/operations/github-app-rotation.md` here.
5. **Two new credential implementations + resolver swap.** `app_installation.go`, `user_pat.go`, `orgResolver` replaces `PlatformPATResolver`; `platform_pat.go` deleted. Every existing call site routes through `Resolver.Resolve(ocOrgID)` — the Phase 0 invariant means this step changes the kind set without rippling. **CI must turn green from this point with no call-site edits**, except for the `GetCredentials`-bridge sites enumerated in §1.10, which are explicit deletion candidates in step 9.
6. **Connect/disconnect HTTP routes.** `/internal/credentials/orgs/...` on git-service; `/api/v1/orgs/{ocOrgId}/github/*` on the BFF. Tests exercise the full PAT and App connect flows against a mocked GitHub.
7. **Webhook receiver multi-tenancy.** `routing.go` extension; `GitServiceSecretProvider`. Receiver per-event handlers unchanged; the routing/resolution/secret-lookup chain in front of them is the only delta.
8. **New event handlers.** `installation.*` and `installation_repositories.*`. Reach-reconciliation cascade in the projector. Disconnect cascade as the `installation.deleted` path.
9. **`SecretReference` migration + `GetCredentials` excision.** `secretref_client.go`, `MintBuildToken`, `TriggerForPush` change. The three call sites from §1.10 are deleted. CI grep fails the build if `gitservice.GetCredentials` survives.
10. **Bearer `org` claim.** Update Phase 0's `bearer_service.go` to include `org`; update `/credentials/refresh` middleware to verify; update workspace `update-git-identity` hook to check `tid` echo. Existing tasks survive (the verifier accepts old bearers without `org` for a 24h grace, then rejects).
11. **Console — Org Settings → Integrations → GitHub** (§10). Two-mode connect/replace/disconnect surface, repo list (App mode), identity-drift and reach-reconciliation banners. Also adds a "Settings" entry to the org sidebar.
12. **Operational docs.** `github-app.md`, `github-app-rotation.md`, `openbao-deployment.md`.
13. **Remove `DEPLOYMENT_TIER` gate's platform-pat special case** and the Phase-0 dev banner.

Steps 1–5 are independently testable. Steps 6+ exercise the full per-org flow end-to-end.

### 14.3 Production cutover

Per evolution-doc §8: production cutover does not migrate platform-PAT projects. Phase 2 production deployments start with zero rows in `org_credentials`. The first action of any new org is to connect.

### 14.4 Implementation discipline — stop-and-ask points

Phase 2's correctness depends on GitHub-side config that is not in this repo (App permissions, event subscriptions, webhook URL, callback URL, webhook secret). Per `CLAUDE.md`, the rule is "do the proper fix, no hacks." If implementation hits one of the config failure modes below, **stop and ask the operator to update the App config** rather than working around it in code.

## 15. Test strategy

### 15.1 Unit

- `appTokenMinter`: signs JWTs (RS256, 10-min exp, `iss=appID`); cache hit/miss/eviction; `singleflight` deduplicates concurrent mints.
- `userPATCred`: `singleflight` collapses concurrent reads; OpenBao failure surfaces as error.
- `openBaoStore`: empty `ocOrgID` panics; malformed (`_platform`, leading `_`, non-DNS-label) returns `ErrOrgIDInvalid`; valid input produces deterministic path.
- `orgResolver.Resolve`: empty → `ErrOrgIDRequired`; suspended/disconnected → `OrgNotActiveError`; App row → `appInstallationCred`; PAT row → `userPATCred`.
- `mint-build` server-side ownership: `(orgA, orgB-repoSlug)` returns 404 even if `orgB-repoSlug` exists for orgB.
- HMAC validation with miss-then-refetch: hit-valid → ok; hit-invalid + refetch-valid → ok with one OpenBao read; hit-invalid + refetch-invalid → 401.
- `task_state.Apply`: new `'org.disconnected'` and `'repo.unselected'` transitions allowed from non-terminal; refused from terminal.
- Bearer verifier: `claims.org != ComponentTask.OrgID` → reject; legacy bearer without `org` → accept within grace, reject outside.

### 15.2 Integration (against docker-compose stack with OpenBao)

- **Connect — App mode.** Spoof a callback (`?installation_id=...&state=<jwt>`); BFF persists credential; subsequent `Resolve(ocOrgId)` returns `app-installation`.
- **Connect — PAT mode.** POST a PAT against a fake GitHub mock that returns `/user`; verify identity stored, OpenBao key written, projection returned without the token.
- **Replace — PAT mode, identity drift.** First connect with PAT-A (login `alice`); replace with PAT-B (login `bob`); verify `prev_identity_login`, `identity_changed_at` set; banner served by API.
- **Replace — narrower scope.** Replace a working PAT with one missing `Administration: Write` for the org; verify the validation chain refuses with a specific error.
- **Disconnect.** Connect, dispatch a task, disconnect; verify task transitions to `abandoned`, OpenBao keys deleted, `status='disconnected'`, issue receives the abandonment comment.
- **Reach reconciliation.** Connect App; inject `installation_repositories.removed` for a repo with an in-flight task; verify `GET /installation/{id}/repositories` confirmation call; verify task cascades to `abandoned`.
- **Pre-WorkflowRun mint-then-write.** Trigger a `push`; verify `MintBuildToken` is called; OpenBao key written; `WorkflowRun` created with `secretRef = git-{ocOrgId}-{repoSlug}`.
- **Cross-org `mint-build` rejection.** `MintBuildToken(orgA, orgB-repoSlug)` returns 404; no OpenBao write.
- **Build retry on auth failure.** Inject `WorkflowRun` failure with `git_clone_failed_auth`; verify build watcher mints fresh token, recreates run; after 3 attempts task → `failed`.
- **Webhook secret rotation (PAT mode).** Append a second secret; send an event signed with new secret; verify cache miss → refetch → accept. Drop the old secret; old-signed event rejected.
- **OpenBao outage.** Stop OpenBao; PAT-mode reads fail; App-mode in-flight tokens (cache) keep working until expiry; readiness probe fails.
- **Log redaction.** Provoke a token-touching log line at `mint-build`; verify the token bytes are scrubbed.

### 15.3 E2E

Two new scenarios:

- **App-mode happy path.** Click "Connect GitHub App" → install on a test org → callback → connect → create project → generate tasks → dispatch → commits show App's bot identity → PR ready → human merge → build with App-token rotation → deployed.
- **PAT-mode happy path.** Paste a PAT → connect → create project → generate tasks → dispatch → commits show PAT owner's identity → PR ready → merge → build → deployed.

The Phase 0 E2E remains as a regression check on resolver polymorphism (PAT-mode behaviour at the user-visible level should be indistinguishable from Phase 0's flow).

### 15.4 Per-phase E2E test flows

Each PR ships with a self-contained E2E that drives the **full project lifecycle** end-to-end and adds phase-specific assertions on top. The lifecycle is the invariant — every PR must take a fresh project from creation through deploy. If a phase's E2E doesn't reach `deployed`, the phase hasn't earned a merge.

**Standard lifecycle** (used as the body of every flow below):

1. Login as `admin@openchoreo.dev`.
2. Create a new project. → triggers `git-service` repo provision on `asdlc-repos` via the active credential.
3. Generate spec via the BusinessAnalyst agent.
4. Click **Save & Proceed** → BFF commits `.asdlc/spec.md`, pushes, creates annotated tag `spec-v1`. **Verify**: GitHub repo at `https://github.com/asdlc-repos/{repo}` shows the file at the tagged commit.
5. Generate design via the Architect agent.
6. **Save & Proceed** on design → BFF commits `.asdlc/design.json`, creates `design-v1` tag with `source-spec: spec-v1` in the tag message. **Verify**: tag and message present on GitHub.
7. Generate tasks → one GitHub issue per component, each with a feature branch + draft PR `Closes #N`. **Verify**: issues and draft PRs visible on GitHub.
8. Click **Start Implementation** → remote-worker dispatches; agent commits, pushes, runs `gh pr ready`.
9. Webhook `pull_request.ready_for_review` → task transitions to `ready_for_review`.
10. Human merges PR → `pull_request.closed merged=true` → task `merged`; `push` to default branch → BFF triggers `WorkflowRun` → `building`.
11. Build watcher polls OC → `succeeded` → task `deployed`.

**Phase-specific assertions** layer on top per PR.

#### PR A — Foundation

Pre-condition: dev stack restarted post-migration. OpenBao reachable. `org_credentials` table moved to git-service. One row seeded from `GITHUB_PLATFORM_PAT` with `kind='user-pat'`, `github_login='asdlc-repos'`. `secret/asdlc/{ocOrgId}/github/pat` populated.

Lifecycle: run unchanged through deploy. No UI changes; existing user flow is identical.

Phase-specific verification:
- BFF code grep: zero matches for `cfg.GitHub.PlatformPAT`, `EnvSecretProvider`, `GetCredentials` in non-deletion paths.
- BFF DB: `org_credentials` table does not exist (`\dt` shows no row).
- git-service DB: `SELECT * FROM org_credentials` returns one row with the seeded values.
- OpenBao: `bao kv get secret/asdlc/{ocOrgId}/github/pat` returns the PAT bytes; metadata shows `managed-by: asdlc-git-service`, `kind: user-pat`.
- Repo provisioning at step 2 of the lifecycle was authenticated by `Resolver.Resolve(ocOrgID)` (assert via debug log line `credentials.resolved kind=user-pat ocOrgId=...`).
- Per-repo webhook registered at step 2 (PAT mode → `WebhookStrategy() = WebhookPerRepo`).

#### PR B — Connect/disconnect + console UI + webhook routing

Pre-condition: dev stack on PR A. Seed row from PR A present.

Pre-lifecycle UI verification:
- Navigate to `/organizations/{orgId}/settings/github` → connected (PAT) panel renders with seeded identity.
- Click **Disconnect** → confirmation dialog → confirm. Phase A of cascade returns sub-second; panel renders the not-connected state with both connect cards.
- Reconnect via PAT form → projection re-renders. (Validates idempotent connect.)
- Disconnect again. Click **Connect GitHub App** → redirect to GitHub install → install on `asdlc-repos` → callback → settings page shows App mode with `selectedRepos` list and bot identity.
- POST `/api/v1/orgs/{ocOrgId}/github/pat` while App-mode is active → 409. (Mode-fix.)
- Fire signed `installation.suspend` webhook → status flips to `suspended` in projection. Fire `installation.unsuspend` → back to `active`.
- Fire signed event with bad HMAC → receiver returns 401 (after miss-then-refetch).

Lifecycle: **run twice** — once in App mode (after the App connect above), once in PAT mode (disconnect App, connect via PAT form).

Phase-specific verification (App mode):
- Step 4's commit on the repo shows the App's bot identity (`asdlc-platform[bot]`). Verify via `git log --format=%an <spec-v1-sha>` on the cloned repo.
- The repo has **no per-repo webhook** registered (App-mode → WebhookPlatform). Verify via `gh api repos/asdlc-repos/{repo}/hooks` — empty list.
- All issue/PR/comment activity in the lifecycle attributes to the bot identity.

Phase-specific verification (PAT mode):
- Commits attribute to the PAT owner's identity.
- Per-repo webhook registered.

Phase-specific verification (multi-tenancy):
- Manually insert a second `ocOrgId` user-pat row → push events on its repo route correctly via `repository.full_name → ocOrgId` resolver. Push events on the App-mode repo route via `installation.id → ocOrgId`.

Note: the build step at lifecycle step 11 still flows through the legacy `GetCredentials` bridge under PR B; the bridge is augmented to handle App-mode tokens for this PR, then deleted in PR C.

#### PR C — SecretReference migration + GetCredentials excision + bearer org claim

Pre-condition: dev stack on PR B. Both modes connectable.

Lifecycle: **run twice** — once in App mode, once in PAT mode.

Phase-specific verification (per mode):
- Lifecycle step 11's build now flows through `MintBuildToken`. Verify git-service log line `mint-build ocOrgId=... repoSlug=...`.
- After the mint, `kubectl get secretreference -n {ocOrgId} git-{ocOrgId}-{repoSlug}` exists with `vaultPath=secret/asdlc/{ocOrgId}/git/{repoSlug}`.
- The materialised K8s `Secret` is consumed by the `WorkflowRun`; build clones successfully.
- OpenBao key `secret/asdlc/{ocOrgId}/git/{repoSlug}` exists post-mint.
- Cross-org refusal: `curl -XPOST .../mint-build {ocOrgId:orgA, repoSlug:orgB-repo}` → 404, no OpenBao write.
- Bearer org claim: decode the workspace JWT (`cat ~/asdlc-workspace/.../bearer.jwt | jwt decode`) → `org` claim equals the project's ocOrgId. Spoof a bearer with mismatched `org` against `/credentials/refresh` → 403.
- CI grep gate: `rg 'gitservice.GetCredentials|CreateGitSecret'` returns zero matches outside delete commits.
- BFF process audit: tracing on, run lifecycle, dump all outbound HTTP headers — zero `Authorization: Bearer gh*_*` headers originate from BFF.

#### PR D — Reach reconciliation + validator + ops cleanup

Pre-condition: dev stack on PR C. Both modes work end-to-end through deploy.

Lifecycle: run once in App mode through deploy.

Phase-specific verification (reach reconciliation, App mode):
- After lifecycle reaches `in_progress` on a task, configure the App install on `asdlc-repos` to **deselect** the project's repo via GitHub UI.
- `installation_repositories.removed` fires → BFF Phase A merges `selected_repos`; Phase B confirms via `GET /installation/{id}/repositories` → projector cascades the in-flight task to `abandoned` with `cause='repo.unselected'`.
- The GitHub issue receives "abandoned: repo unselected" comment.
- Settings page renders `ReachReconciliationBanner` with "1 in-flight task abandoned". "View tasks" link opens the dialog with the abandoned task.
- Re-select the repo → `installation_repositories.added` updates `selected_repos`; no cascade.

Phase-specific verification (PAT identity drift):
- Disconnect App, connect with PAT-A (login `alice`) → identity stored as alice.
- Run lifecycle through `Save & Proceed` on spec → first commit shows `alice`.
- **Replace** PAT-A with PAT-B (login `bob`) via the same connect form → projection updates; `IdentityDriftBanner` appears: "Identity changed from alice to bob on `<date>`."
- Continue lifecycle: design publish + tasks dispatch → new commits show `bob`.
- For a task dispatched pre-replace and mid-flight: the workspace `update-git-identity` hook rewrites `.git/config` user fields on the next `credhelper.sh` call. **Verify**: `cat ~/asdlc-workspace/.../{taskId}/.git/config | grep user` shows bob.

Phase-specific verification (validator):
- Force-tick the periodic validator (test-only endpoint `POST /internal/credentials/_validator/tick`) on a fresh row → `last_validated_at` updates.
- Mock GitHub to return 401 on `GET /user` for the PAT row → validator flips status to `disconnected` → disconnect cascade fires → `IdentityDriftBanner` replaced by "GitHub credential is no longer valid" banner.

Phase-specific verification (build auth-failure retry — moved from PR C):
- Auth-failure retry: kill OpenBao briefly during a build → `WorkflowRun` fails with `git_clone_failed_auth` → watcher re-mints + recreates → succeeds. Force >3 attempts (delete the OpenBao key between retries) → task `failed` with `ErrorMessage = "build auth retry budget exceeded"`. Requires the build watcher to gain a `git_clone_failed_auth` classifier (parses `WorkflowRun.Status.Tasks` outputs) plus a 3-attempt re-mint budget per evolution-doc §6.3.1. Originally listed under PR C; deferred to PR D so PR C stays scoped to the credential migration.

Phase-specific verification (cleanup):
- Restart BFF with `DEPLOYMENT_TIER=staging` and PAT-mode org → boots cleanly (no platform-pat special case).
- Code audit: `tier_gate.go` does not reference `'platform-pat'`. `EnvSecretProvider` is gone from the codebase. Phase 0 dev banner removed from console.

---

## 16. Open questions / decisions still outstanding

1. **App-mode webhook secret per-installation vs. App-wide.** App-wide. GitHub Apps don't support per-install secrets natively; the leak surface (forged events targeting any App-mode org) is mitigated for `installation_repositories.removed` by §6.8 Phase B's confirming round-trip. Other forged events have a smaller blast radius; accept the property and document.
2. **PAT scope-validation depth at connect.** `GET /user` + membership check + repo-read permission probe. No temp create-and-delete. Decision is final; revisit only if connect-error reports indicate scope issues are silently masked.
3. **App private-key rotation cadence.** Calendar-driven, 6 months recommended; manual runbook for Phase 2. Automation deferred to a follow-up.
4. **Cache TTL knobs.** App-token 5-minute safety margin is a constant (§13 rule 7). Promote to config only on a real complaint.
5. **Multi-replica PAT-replace propagation.** PAT mode has no plaintext cache, so propagation is implicit (next read on any replica sees the new value). App-mode token cache is per-replica, deadline-keyed; a stale cached token survives at most ~55 minutes after replace, but App tokens aren't replaced — they expire. So propagation is a non-issue for both modes by construction. Worth checking off as a settled question.
6. **`installation_repositories.added` validation.** Phase 2 trusts the event (no confirming round-trip on `added`). The asymmetric trust — confirm on `removed` because the cascade is destructive, trust on `added` because over-permissive reach is a soft failure — is a deliberate choice. A forged `added` event grants no actual GitHub access; the App's real install state is what GitHub enforces at API call time.
