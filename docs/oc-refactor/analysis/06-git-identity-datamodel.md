# 06 — Git Service, Identity/Auth, and Data Model (labs-agentic-engineer)

Source verified: `/Users/wso2/repos/labs-agentic-engineer` (branch `cleanup-auth`,
checked against `main`). All citations are `file:line` relative to the repo root.

Reference baseline (Agent Manager, "AM"):
- `/Users/wso2/repos/agent-manager-analysis/06-identity-secrets-git.md`
- `/Users/wso2/repos/agent-manager-analysis/00-overview.md`

---

## Summary

The platform ("ASDLC" / WSO2 Labs Agentic Engineer) splits its GitHub plumbing
across two Go services and one Node BFF surface:

1. **`git-service/`** owns all GitHub authentication and all git/GitHub
   operations: repo creation, clone/commit/push/tag (shell `git`), and the full
   GitHub REST surface (issues, branches, PRs, contents, trees, webhooks, App
   installation lifecycle). It is the **only** holder of GitHub credentials and
   the only minter of the GitHub App JWT. Every external op is parametrised by
   `ocOrgID` through a polymorphic `credentials.Resolver` seam
   (`git-service/pkg/credentials/credential.go:32`).

2. **`asdlc-service/`** (the "BFF") owns the **inbound** GitHub webhook receiver
   (`asdlc-service/controllers/webhook_controller.go`), the inbound user/Thunder
   JWT auth, the org/project/task data model, and the connect/disconnect UX
   surface that proxies to git-service's `/internal/credentials/...` API.

3. Webhooks arrive from GitHub via a **smee.io relay** in local dev
   (`deployments/docker-compose.yml:279` `smee-client`), forwarding to the BFF's
   `/webhooks/github`.

The biggest structural divergence from AM: **secret values do NOT live in
OpenBao here**. The active credential store is **Postgres + AES-256-GCM**
(`git-service/pkg/credentials/db_store.go`, table `org_secrets`,
`git-service/database/migrations/org_secrets.go`). The OpenBao implementation
still exists in-tree (`openbao_store.go`) but is **not wired** — and as a result
**GitHub App mode is effectively dead** (see "App mode is dead" below). The
platform runs PAT-mode only in practice. Build/agent credentials are delivered
not as OC `GitSecret`/`SecretReference` CRDs but as **plain Kubernetes Secrets
SSA-applied directly into the workflow-plane namespace `workflows-<ocOrgID>`**.

---

## 1. git-service + GitHub integration

### 1.1 What it exposes (operations)

On-disk git operations via **shell `git`** (`os/exec`), not go-git, not the API:
`git-service/services/git_ops_service.go`.

| Op | Func | Mechanism |
|---|---|---|
| Commit | `Commit` `git_ops_service.go:343` | `git add` + `git commit --author` + `GIT_COMMITTER_*` env |
| Push | `Push` `git_ops_service.go:437` | `git push origin <branch>` |
| Pull | `Pull` `git_ops_service.go:495` | `git pull origin <branch>` |
| Status | `Status` `git_ops_service.go:546` | `git status --porcelain` |
| Tag (annotated, push) | `CreateTag` `git_ops_service.go:588` | `git tag -a` + `git push origin <tag>` |
| List tags | `ListTags` `git_ops_service.go:672` | `git fetch --tags` + `git tag -l` |
| File at tag | `GetFileAtTag` `git_ops_service.go:749` | `git show <tag>:<path>` |
| Clone (async) | `performClone` `repo_service.go:208` | `git clone` |
| Non-destructive re-clone | `cloneIntoPath` `git_ops_service.go:201` | clone into `.tmpclone-*`, atomic rename |

GitHub REST operations via **net/http** (no SDK), `services/github_client.go`
(interface `GitHubClient` `github_client.go:23`):

- **Repo creation**: `CreateOrgRepo` `github_client.go:291` → `POST /orgs/{owner}/repos`,
  falls back to `POST /user/repos` on 404 (`createUserRepo` `github_client.go:355`).
  Repo name is `slugify(project)+rand3digits` with 5-retry name-conflict loop
  (`repo_service.go:122`). Owner = `cred.RepoOwner()` (never ambient config).
- **Issues (tasks)**: `CreateIssue` `github_client.go:390`, `ListIssues` :554,
  `CloseIssue` :463, `CommentIssue` :523, `EditIssueBody` :494, `EnsureLabel` :432.
- **Branches/refs/contents/trees/commits**: `CreateBranch` :641, `GetBranchSHA` :612,
  `PutFileOnBranch` :855, plus the artifact-store-v2 low-level git data API
  (`GetContents`, `PutContents`, `CreateBlob`, `CreateTree`, `CreateCommit`,
  `UpdateRef`, `CreateTagObject`, `CreateTagRef`, `ListMatchingRefs`,
  `github_client.go:105-152`).
- **PRs**: `CreateDraftPR` `github_client.go:681` → `POST /repos/.../pulls` with
  `draft:true`, idempotent via `findPullByHead` :723.
- **Per-repo webhooks**: `RegisterWebhook` `github_client.go:752` → `POST
  /repos/.../hooks` with `config.secret = hmacSecret` (HMAC), `DeregisterWebhook` :834.
- **App lifecycle**: `GetAppInstallation` :958, `ListAppInstallations` :1026,
  `DeleteInstallation` :992, `ExchangeOAuthCode` :1095, `GetUserInstallations` :1145.

So this single repo does **repo creation, commits, issue creation, PR creation,
and webhook registration** — much broader than AM's read-only browse surface.

### 1.2 How it authenticates to GitHub

The single seam is `credentials.Credential` (`pkg/credentials/credential.go:32`):
`Token(ctx) → (token, expiresAt, err)`, `Identity()`, `RepoOwner()`,
`WebhookStrategy()`. Callers MUST NOT type-switch (`credential.go:31`). The only
place a `Authorization: Bearer` header is built for the REST client is
`authHeaders` (`github_client.go:280`), which calls `cred.Token(ctx)` fresh each
call. On-disk git auth uses a throwaway `GIT_ASKPASS` script echoing the token
(`createAskPassScript` `repo_service.go:286`).

Two credential kinds (`org_resolver.go:115`):

- **`user-pat`** → `userPATCred` (`pkg/credentials/user_pat.go:21`). `Token()`
  reads the PAT from the store at key **`github/pat`** (`user_pat.go:33`) with
  singleflight; never expires (zero `expiresAt`). `RepoOwner()` =
  `github_login` chosen at connect. `WebhookStrategy()` = **WebhookPerRepo**.
- **`app-installation`** → `appInstallationCred`
  (`pkg/credentials/app_installation.go:17`). `Token()` delegates to
  `AppTokenMinter.MintForInstallation` (installation access token).
  `RepoOwner()` = the install's account login. `WebhookStrategy()` =
  **WebhookPlatform** (App delivers via its single configured callback, no
  per-repo hook).

### 1.3 Where the credential comes from

**The active store is Postgres, not OpenBao.** `main` wires
`credentials.NewDBStore(db, credKey)` (`cmd/git-service/main.go:129`), logging
`"credential store: postgres (aes-256-gcm)"` (:134). `dbStore`
(`pkg/credentials/db_store.go`) implements the `OpenBaoStore` interface but reads
`org_secrets(oc_org_id, key, value)` with AES-256-GCM seal/open (`db_store.go:40-94`).
The 32-byte key is `CREDENTIAL_ENCRYPTION_KEY` (base64), required
(`config/config.go:42`, `main.go:124`). The PAT is `Put` at connect
(`credential_service.go:284` `store.Put(ctx, ocOrgID, "github/pat", ...)`).

The `ocOrgID` passed in is the **OC org handle** (the `{orgHandle}` path param on
the BFF, derived from the Thunder `ouHandle` claim — see §4), forwarded verbatim
to git-service's `/internal/credentials/orgs/{ocOrgId}` API
(`asdlc-service/clients/gitservice/credentials.go:94`).

---

## 2. GitHub App: private key load + installation-token mint

The `deployments/github-app-private-key.pem` file is **0 bytes** (empty) on this
checkout — App mode is not provisioned locally.

- **Minter**: `AppTokenMinter` (`pkg/credentials/app_token_minter.go:39`) is the
  *only* consumer of the App RSA private key. `parseRSAPrivateKey`
  (`app_token_minter.go:231`) accepts PKCS#1 and PKCS#8.
- **JWT mint**: `signAppJWT` (`app_token_minter.go:163`) builds an RS256 JWT
  (`iss=appID`, `iat-60s`, `exp+9m`), signed `rsa.SignPKCS1v15`.
- **Installation token**: `mintInstallationToken` (`app_token_minter.go:190`) →
  `POST https://api.github.com/app/installations/{id}/access_tokens` with the App
  JWT; result cached per-installation with a 5-min safety margin
  (`MintForInstallation` :94, singleflight-collapsed).
- **Key load at startup**: `LoadAppKeyFromOpenBao` (`app_token_minter.go:408`)
  reads `secret/asdlc/_platform/github/app/private_key` + `app_id`. **It
  type-asserts `store.(*openBaoStore)` (`app_token_minter.go:409`) and returns
  `(nil, nil)` for any other store type.**
- **Dev seed**: `seed.AppPlatformFromEnv` (`internal/seed/app_platform.go:30`)
  reads `GITHUB_APP_PRIVATE_KEY_PATH` and writes via
  `AsPlatformSeeder(store)` — which **also type-asserts `*openBaoStore`**
  (`openbao_store.go:232`) and bails for any other store.

### App mode is dead with the Postgres store (key finding)

Because `main.go` wires `dbStore`, **not** `openBaoStore`:
- `AsPlatformSeeder` returns `(nil,false)` → `AppPlatformFromEnv` logs "store is
  not the real OpenBao implementation; skipping" (`app_platform.go:69`) and never
  seeds the key.
- `LoadAppKeyFromOpenBao` returns `(nil,nil)` → minter constructs in
  **"no app configured"** mode (`main.go:164-166`).
- Therefore `connectApp` rejects with `"GitHub App not configured on this
  deployment"` (`credential_service.go:386`), and `appInstallationCred.Token`
  would return `ErrAppNotConfigured`.

Net: the App code path (`appInstallationCred`, the OAuth discover-then-bind flow,
platform-wide webhook delivery) is fully implemented but **unreachable** under
the current Postgres wiring. The platform operates **PAT-only** today. (This is
a regression from the documented design, which assumed the OpenBao-backed
`_platform` namespace.)

---

## 3. Webhooks: receipt, verification, smee relay, secret storage

### 3.1 Receipt + verification (inbound, in the BFF)

`asdlc-service/controllers/webhook_controller.go:67` `Receive`:
1. Read raw body, read `X-GitHub-Delivery` / `X-GitHub-Event` /
   `X-Hub-Signature-256` headers (:77).
2. **Route to org** before verifying: `webhook.ResolveOcOrgID`
   (`services/webhook/routing_key.go:74`) extracts the routing key —
   `installation.id` for `installation`/`installation_repositories`,
   `repository.full_name` for `pull_request`/`push`/`issue_comment`/`issues`
   (`routing_key.go:30-51`) — then resolves to `ocOrgId` via git-service
   (`OrgIDByInstallationID` / `OrgIDByRepoFullName`, 60s in-process cache).
3. **HMAC verify**: `Verifier.VerifyWithKey` (`services/webhook/verifier.go:62`)
   computes `HMAC-SHA256(body, secret)` and compares against `sha256=<hex>`
   (`matchesAny` :95, constant-time `hmac.Equal`). On mismatch it does a
   **rate-limited forced refetch** of the secret list (rotation handling,
   :82-91), 1/s burst 5 keyed on `(ocOrgID, sourceIP)` (`verifier.go:47`).
4. Dedup INSERT into `webhook_deliveries` (`deliveries.Persist` :129); idempotent
   on `X-GitHub-Delivery`.
5. Dispatch to per-event handler (`router.Dispatch` :147); handler error → 5xx so
   GitHub retries; success → 200.

The handlers (`services/webhook/handlers.go`, `projector.go`,
`build_watcher.go`, `coding_agent_watcher.go`, etc.) drive the **ComponentTask**
state machine (see §5) — e.g. PR opened → `ready_for_review`, merged → `merged`,
build → `building`/`deployed`.

### 3.2 Webhook secret storage (per-kind)

The accepted HMAC keys come from git-service via
`/internal/credentials/orgs/{ocOrgId}/webhook-secrets`
(`asdlc-service/services/webhook/secrets.go:45` `GitServiceSecretProvider`, 30s
LRU). git-service answers in `CredentialService.WebhookSecrets`
(`git-service/services/credential_service.go:659`):
- **PAT mode**: from the row's `webhook_secrets` JSONB list
  (`org_credentials.webhook_secrets`, supports N-of-M rotation,
  `models/org_credential.go:99`). Seeded at connect from the platform-wide
  `GITHUB_WEBHOOK_SECRET` env (`credential_service.go:293`).
- **App mode**: from the platform-wide `_platform/github/app/webhook_secret`
  list (`credential_service.go:684`, via `minter.LoadAppWebhookSecrets`) — again
  **unreachable** under the Postgres store.

So the webhook HMAC secret is effectively the single platform-wide env value
`GITHUB_WEBHOOK_SECRET` (`deployments/.env:45`), copied into each PAT row's list.

### 3.3 Webhook registration (outbound) + smee

git-service registers the per-repo hook: `WebhookService.Register`
(`git-service/services/webhook_service.go`, controller
`controllers/webhook_controller.go:38`) calls `RegisterWebhook` with
`deliveryURL = GITHUB_WEBHOOK_DELIVERY_URL` and `secret = GITHUB_WEBHOOK_SECRET`
(`config/config_loader.go:36-37`). In App mode (`WebhookStrategy=Platform`) it
is a no-op returning `strategy:"platform"` (`controllers/webhook_controller.go:55-58`).

**smee relay** (local dev only): GitHub posts to the public smee.io channel
`GITHUB_WEBHOOK_PROXY_URL=https://smee.io/...` (`deployments/.env:50`), and the
`smee-client` compose service (`deployments/docker-compose.yml:279`) forwards to
the BFF's `/webhooks/github`. `GITHUB_WEBHOOK_DELIVERY_URL` is set to that smee
URL (`docker-compose.yml:216`), so registered hooks point at smee in dev and at
the BFF ingress in cloud.

---

## 4. Identity / Auth

### 4.1 Inbound request auth = Thunder JWT via JWKS (mirrors AM almost verbatim)

Both services carry a `jwtassertion` package nearly identical to AM's:
- `asdlc-service/middleware/jwtassertion/auth.go` and
  `git-service/middleware/jwtassertion/auth.go`. `Authenticator(cfg)`
  (`auth.go:67`) reads the `Authorization` header, strips `Bearer `, and
  validates via `validateJWT` (`auth.go:152`):
  - With a `JWKS` cache: parse with `*TokenClaims`, require **RSA** signing
    (`auth.go:157`), match `kid` to `JWKS.PublicKeyForKid` (`auth.go:164`).
  - Else if `IsLocalDevEnv`: decode claims **without signature** check
    (`extractClaimsUnverified` `auth.go:284`) — the same local-dev bypass AM has.
  - Else fail closed ("JWKS not configured" `auth.go:187`).
  - Then `issuers.match` + `audiences.match` (prefix `*` supported, bare `*`
    rejected — `auth.go:190-194`, `compileAudiences` :232).
- The JWKS URL is Thunder's (`git-service/config/config.go:84` `JWKSURL`,
  "Thunder JWKS endpoint"). This is the same RFC-9728 / WWW-Authenticate
  challenge shape AM uses (`auth.go:74,141`).

So **request authentication = Thunder-issued OIDC JWT validated via JWKS** —
identical mechanism to AM 06 §Identity. Local dev signs in `admin/admin`.

### 4.2 Claims and org/user context derivation

`TokenClaims` (`asdlc-service/middleware/jwtassertion/auth.go:22`): `Sub`,
`Scope`, **`OuId`**, **`OuName`**, **`OuHandle`**, `ClientID`, plus
**custom Task-JWT claims** `OcOrgID`, `TaskID`, `ProjectID` (`auth.go:29-32`).

Org context is derived from the claim, then a **path param**:
- `jwt.ResolveOuHandle(claims)` (`asdlc-service/middleware/jwt/jwt.go:33`) picks
  the org handle with precedence **`ouHandle` → `ouName` → `ouId`** (mirrored in
  `console/src/utils/orgClaims.ts`).
- `orgensure.Middleware` (`asdlc-service/middleware/orgensure/orgensure.go:31`)
  takes `ResolveOuHandle`, verifies the OC **namespace** exists, and caches the
  local `Organization` UUID. It does **not** create namespaces — best-effort
  passthrough.
- The org-scoped routes use a **`{orgHandle}` path param**
  (`controllers/org_github_controller.go` route comments) which becomes the
  `ocOrgID` forwarded to git-service.

This is structurally the **same split AM has** (AM 06: "org context derived two
ways" — `ouId`/`ouHandle` claim vs the `{orgName}` path). Here the path segment
is `orgHandle` and the claim precedence is `ouHandle→ouName→ouId`; AM stamps
`ouId` into agent tokens, while ASDLC threads the resolved handle as `ocOrgID`.

### 4.3 ASDLC mints its own JWTs too (like AM's agent tokens)

git-service additionally accepts **Task JWTs** (issuer `asdlc-bff`, audience
`git-service`, `config/config.go:98-100`) for `/api/v1/credentials/refresh`, and
the BFF signs them (`asdlc-service/services/task_token_manager.go`,
`deployments/keys/task-signing.pem`, `api/jwks_routes.go` exposes a BFF JWKS).
This parallels AM's "AMP is its own issuer for agent runtime tokens" — a
**second JWKS / issuer** distinct from Thunder.

### 4.4 `cleanup-auth` branch context

The current branch `cleanup-auth` is an **auth & runtime-config refactor**
(commit `f3955cf` "Refactor auth & runtime config: BFF-owned ReleaseBinding
env-config.js", `9d25540` "Remove legacy auth-schema aliases and dead docs (no
back-compat)"; design `docs/design/auth-and-runtime-config-refactor.md`). The
diff vs `main` is concentrated in `asdlc-service/services/*` (dispatch, runtime
config, skills) and runner/skills plumbing — it does **not** touch the
`jwtassertion`/`jwt`/`orgensure` middleware or the git-service credential code,
so the identity findings above hold on both branches. The "cleanup" is about
runtime-config injection and a new skills system, not the inbound auth seam.

---

## 5. Data model (entities + how secrets are modeled)

### 5.1 Core entities

| Entity | File | Key fields | Owner |
|---|---|---|---|
| **Organization** | `asdlc-service/models/organization.go:15` | `UUID` (PK), `Name` (unique OC namespace handle), `DisplayName`, `CreatedBy` | BFF Postgres. A local UUID side-car for an OC namespace ("OpenChoreo has no Organization CRD — namespaces *are* the org boundary", :11). |
| **Project** | `asdlc-service/models/project.go:3` | `UID`, `Name`, `NamespaceName`, `DeploymentPipeline`, `Status` | Projection over an OC **Project** CRD (not a BFF-owned table — it's an API DTO). |
| **ComponentTask** | `asdlc-service/models/component_task.go:81` | `ID` (PK uuid), `ProjectID`, `OrgID`, `ComponentName`, `Title`, `Body`, `DependsOnComponents` (jsonb), `Status`, `LifecycleStatus`, `IssueURL`/`IssueNumber`, `BranchName`, `PullRequestNumber`/`URL`, `MergeCommitSHA`, `LastBuildRunName`, `LastCodingAgentRunName` | BFF Postgres. Maps **1:1:1:1 to a GitHub issue, feature branch, draft PR**. State driven by webhooks (`services/webhook/projector.go`). |
| **GitRepository** | `git-service/models/repository.go:10` | `ID`, `OrgID`, `ProjectID` (unique), `RepoURL`, `ClonePath`, `Status`, `WebhookID`, `RepoSlug`, `GithubProjectID` | git-service Postgres. The clone-on-disk + GitHub repo record. |
| **OrgCredential** | `git-service/models/org_credential.go:21` | `OcOrgID` (PK), `Kind` (`user-pat`/`app-installation`), `GitHubLogin`, identity triple, `InstallationID`, `SelectedRepos` (jsonb), `WebhookSecrets` (jsonb), `Status`, validation timestamps | git-service Postgres, table `org_credentials`. **One row per OC org.** |
| **OrgAnthropicCredential** | `git-service/models/org_anthropic_credential.go:11` | `OcOrgID` (PK), `KeyPrefix`, `KeyLast4`, `Status` | git-service Postgres. Non-secret **projection** only; the key bytes live in `org_secrets`. |

Task lifecycle/status enums: `TaskStatus` (`component_task.go:30`:
pending/in_progress/ready_for_review/merged/building/deployed/rejected/failed/
abandoned/on_hold/verification_failed) and `TaskLifecycleStatus`
(:9: gh_issue_waiting/syncing/created/failed). `TaskStatusAbandoned` is the
credential-disconnect cascade target.

### 5.2 How org-level secrets/credentials are modeled

Secret **values** are stored in **git-service Postgres**, encrypted at rest:

- Table **`org_secrets(oc_org_id TEXT, key TEXT, value TEXT, updated_at)`**, PK
  `(oc_org_id, key)` (`git-service/database/migrations/org_secrets.go:17`). The
  comment is explicit: *"Replaces the previous OpenBao-backed store."*
- `value` = base64(nonce ‖ AES-256-GCM ciphertext+tag) (`db_store.go:40-46`).
  Key = `CREDENTIAL_ENCRYPTION_KEY` (`config.go:42`).
- Known keys: **`github/pat`** (PAT, `user_pat.go:33` / `credential_service.go:284`)
  and **`anthropic/key`** (Anthropic API key, per
  `org_anthropic_credential.go:7`).
- The relational rows (`org_credentials`, `org_anthropic_credentials`) store only
  **non-secret projection metadata** (identity login/name/email, key prefix +
  last4, status) — never the token (`org_credential.go:25-27` mark identity
  email/name `json:"-"`; `PATSecretRef`/`WebhookSecrets` also `json:"-"`).
- **Webhook HMAC secrets** are an exception: stored **in the row itself**
  (`org_credentials.webhook_secrets` JSONB list, `org_credential.go:99`),
  plaintext, for PAT mode.

So: **no OpenBao, no SecretReference CRD, no ESO** in the live path. Secret refs
are not "references" at all — they're encrypted blobs in Postgres keyed by
`(ocOrgID, key)`.

### 5.3 How credentials reach the build/agent (workflow plane)

git-service does NOT emit OC `GitSecret`/`SecretReference` CRDs. Instead it
**SSA-applies plain K8s Secrets directly** into the org's workflow-plane
namespace `workflows-<ocOrgID>` (`models/wp_naming.go:26`):

- **Build credential**: `BuildCredentialsService.StageBuildSecret`
  (`services/build_credentials_service.go:114`) resolves the org credential,
  gets a fresh token, and SSAs a `kubernetes.io/basic-auth` Secret named
  `<workflowRunName>-git-secret` (`models.BuildSecretNameFor` `wp_naming.go:46`)
  with `{username, password=token}` (`build_credentials_service.go:176-192`).
  Username is `x-access-token` (App) or the PAT login (PAT)
  (`usernameForCredential` :246). The BFF passes `secretRef:""` to the
  WorkflowRun so OC's externalRefs resolver skips synth (`build_credentials_service.go:11-16`).
- **Anthropic key**: `AnthropicCredentialService` SSAs an Opaque Secret
  `anthropic-credentials` with `ANTHROPIC_API_KEY` into the same WP namespace
  (`anthropic_credential_service.go:354-368`, `wp_naming.go:60`).

The token therefore **does transit the git-service control-plane API path and is
written as a live K8s Secret** — it is not kept reference-only.

---

## 6. Gap vs Agent Manager / OpenChoreo

| Concern | Agent Manager (AM 06) | labs-agentic-engineer (this repo) | Gap |
|---|---|---|---|
| Inbound auth | Thunder JWT via JWKS, RS256, issuer/audience, local-dev bypass (`auth.go`) | **Same** `jwtassertion` (verbatim port) | **Aligned.** |
| Org identity | `ouId`/`ouHandle` claim + `{orgName}` path → namespace | `ouHandle→ouName→ouId` precedence + `{orgHandle}` path → `ocOrgID` | **Aligned in shape.** Same path-vs-claim split; no explicit check path==claim. |
| Own-issued tokens | AMP mints RS256 agent tokens, publishes own JWKS | BFF mints Task JWTs (`asdlc-bff`→`git-service`), own JWKS | **Aligned in shape.** |
| Secret values | **OpenBao KV v2** (control-plane), `managed-by` metadata | **Postgres `org_secrets` + AES-256-GCM** | **Diverged.** No OpenBao in the live path; OpenBao code is dead-wired. |
| Reference model | **OC `SecretReference` CRD** (path+keys, no value), ESO materialises on data plane via reader role | **None.** Plain K8s Secret SSA'd into `workflows-<ocOrgID>` with the raw token | **Diverged.** Value transits the service and lands as a live Secret; no ESO, no reader/writer role split. |
| Git **build** creds | OC **`GitSecret`** bound to `ClusterWorkflowPlane/default` (AM 06 §Git) | Plain `kubernetes.io/basic-auth` Secret `<run>-git-secret` in `workflows-<ocOrgID>` | **Diverged.** Bypasses the OC GitSecret/externalRefs machinery deliberately (`build_credentials_service.go:11-16`). |
| Git **browse** creds | Per-org basic-auth from a **separate workflow-plane OpenBao** (`:8201`) | git-service uses the org credential directly (same Postgres PAT) | **Diverged.** No second vault; no read-only browse path distinction. |
| GitHub auth modes | PAT or per-org publisher apps via Thunder admin | PAT or **GitHub App installation** (App designed but **dead** under Postgres store) | **Diverged + broken.** App mode unreachable because `LoadAppKeyFromOpenBao`/`AsPlatformSeeder` require `*openBaoStore`. |
| Provisioning into Thunder | AMP `amp-system-client` creates per-org publisher apps | No server-to-server Thunder provisioning here | **Diverged.** GitHub-App OAuth is the per-org provisioning analogue (and it's disabled). |

OpenChoreo alignment notes (per AM 00 plane model): the WP-namespace targeting
(`workflows-<ocOrgID>`, `wp_naming.go:26`) correctly mirrors OC's
`getWorkflowNamespace`. But the **delivery mechanism** (raw K8s Secret SSA vs OC
`GitSecret`/`SecretReference` + ESO) bypasses OC's "only the reference transits
the control plane" principle that AM upholds.

---

## 7. What must change to align with OpenChoreo + Agent Manager

1. **Reinstate a real secret backend and decouple it from a type-assert.**
   The App-key/platform-seed paths are gated on `store.(*openBaoStore)`
   (`app_token_minter.go:409`, `openbao_store.go:232`). Either (a) wire the
   `openBaoStore` (so `_platform` and App mode work) or (b) move platform-key
   custody behind an interface method so the Postgres `dbStore` can serve it.
   Today choosing Postgres silently kills GitHub App mode. Decide one store.

2. **Adopt OC `SecretReference` + ESO for value custody (AM parity).** Replace
   the direct K8s Secret SSA in `build_credentials_service.go:160` and
   `anthropic_credential_service.go:354` with the AM pattern: write the value to
   OpenBao (control plane), create an OC `SecretReference` carrying only the KV
   path+keys, and let ESO materialise the data-plane Secret via a reader role.
   This keeps the raw token off the control-plane request path.

3. **Use OC `GitSecret` for build credentials.** AM binds build creds to
   `ClusterWorkflowPlane/default` as a `GitSecret`
   (AM 06 §Git, `git_secrets.go`). The current per-WorkflowRun basic-auth Secret
   (`build_credentials_service.go`) was a deliberate shortcut
   (`docs/design/build-credential-injection.md`); aligning means delegating to
   OC's externalRefs/`GitSecret` resolver instead of `secretRef:""`.

4. **Split browse vs build credentials / consider a workflow-plane vault.** AM
   keeps a separate workflow-plane OpenBao (`:8201`) for git creds. If adopting
   OpenBao, mirror that separation; otherwise document that ASDLC intentionally
   collapses them.

5. **Decide GitHub App vs PAT as the org-onboarding model.** The App flow
   (OAuth discover-then-bind, platform-wide webhook delivery, installation-token
   minting) is fully built but unreachable. Either finish wiring it (item 1) or
   remove the dead code to avoid the false impression that App mode works.

6. **Per-org isolation as an architectural property.** The `OpenBaoStore`
   interface already enforces `ocOrgID`-namespaced paths and an import fence
   (`openbao_store.go:15-33`, `import_fence_test.go`). The Postgres `dbStore`
   keeps `(oc_org_id, key)` keying but loses the metadata/`managed-by` audit tag
   AM relies on; if staying on Postgres, add an ownership/audit column.

7. **Verify path-vs-claim org binding.** Like AM, neither service asserts the
   `{orgHandle}` path equals the token's `ouHandle`. Add an explicit check so a
   token for org A cannot operate on org B's path
   (`orgensure.go` is the natural seam).
