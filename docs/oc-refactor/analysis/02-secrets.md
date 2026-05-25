# Secret Handling Today — Code-Grounded Analysis & Gap vs OpenChoreo / Agent Manager

> Source verified at `/Users/wso2/repos/labs-agentic-engineer`. Citations are `file:line`.
> Reference ("the right way"): Agent Manager analysis files
> `06-identity-secrets-git.md`, `07-secret-taxonomy.md`, `08-cross-plane-secret-flow.md`
> (`/Users/wso2/repos/agent-manager-analysis/`). The OC core principle being
> evaluated against: **the secret VALUE lives in one place (OpenBao); across plane
> boundaries only a REFERENCE travels (a `SecretReference` CR / KV path), and the
> consuming plane re-fetches the value locally via ESO using a read-only role. The
> control-plane API never carries plaintext.**

---

## Summary

The platform **does not follow OC's reference-only secret discipline for any tenant
secret.** Despite shipping a fully-built `OpenBaoStore` abstraction with per-org path
namespacing, an import fence, reader/writer-role rhetoric in the comments, and a
`SecretReference`-shaped story in the design docs, the **running code uses none of it
for per-org secrets**:

1. **Org-level secrets (GitHub PAT, Anthropic key) live AES-256-GCM-encrypted in the
   git-service Postgres `org_secrets` table** — *not* in OpenBao. The active store is
   `NewDBStore`, wired unconditionally in `cmd/git-service/main.go:129`. OpenBao
   (`NewOpenBaoStore`) is constructed for nothing per-org; it is reached only for the
   `_platform/*` GitHub-App namespace.
2. **The AES master key defaults to 32 zero-bytes** (`CREDENTIAL_ENCRYPTION_KEY` =
   `"AAAA…="`, `git-service/config/config_loader.go:45`) and the local deployment never
   overrides it (no `CREDENTIAL_ENCRYPTION_KEY` anywhere in `deployments/`). So in local
   dev the "encryption at rest" is a fixed, publicly-known key — effectively plaintext.
3. **Secret VALUES cross plane/process boundaries as plaintext, repeatedly:**
   - Anthropic key → git-service **decrypts** it and **SSA-applies a plaintext K8s
     `Secret` directly into the workflow-plane namespace** `workflows-<orgID>`
     (`anthropic_credential_service.go:347-380`). No `SecretReference`, no ESO.
   - GitHub credential → git-service mints/reads it and **SSA-applies a plaintext
     `kubernetes.io/basic-auth` Secret** into the same WP namespace
     (`build_credentials_service.go:160-203`). No `GitSecret`, no ESO.
   - Anthropic key → git-service serves it as **plaintext JSON over HTTP**
     (`/internal/anthropic/effective-key`) to agents-service, which holds it in a
     5-minute in-process LRU and passes it inline to `createAnthropic({apiKey})`
     (`anthropic-key-resolver.ts:121`, `create-agent.ts:62-63`). In the compose setup
     this endpoint is **unauthenticated** (`docker-compose.yml:232-241`).
   - Task JWT bearer → passed as a **plaintext Argo WorkflowRun parameter → env var**
     `ASDLC_BEARER` into the coding-agent pod (`app-factory-coding-agent.yaml:103-104,166-167`).
4. **The coding-agent pod's Anthropic key is the one place that looks OC-ish** — it is
   injected via `env.valueFrom.secretKeyRef` (`app-factory-coding-agent.yaml:176-180`).
   But the referenced Secret was hand-written by git-service via SSA, **not** materialized
   by ESO from a `SecretReference`. So it is a `secretKeyRef` over a control-plane-pushed
   plaintext Secret — the *shape* of Mechanism 1 without the *substance* (no central
   value store, no ESO, no reader role).

**Bottom line vs AM/OC:** AM keeps tenant values in central OpenBao and lets only a
`SecretReference` (KV path) cross to DP/WP, where ESO re-fetches with a read-only role
(AM `08`, Classes 1 & 2). This repo keeps tenant values in Postgres-AES and has the
**control-plane process (git-service) decrypt and physically push the plaintext** into
the consuming plane on every dispatch. It resembles AM's deliberate *exception*
(Mechanism 2 / Class 3, the gateway path) — except here it is applied to **every** tenant
secret, not the one component that lives outside OC. README claims the pod "gets its key
from OpenBao"; the code does not.

---

## Secret taxonomy table (mirrors AM `07`/`08`)

| # | Secret | Level | Where the VALUE lives | How it crosses to the consumer | Reference-only? | Citation |
|---|--------|-------|------------------------|--------------------------------|-----------------|----------|
| 1 | **Org GitHub PAT** | org | Postgres `org_secrets` (AES-256-GCM) | git-service mints/reads → SSA plaintext `basic-auth` K8s Secret into `workflows-<org>` for builds | ❌ value pushed | `db_store.go:81`, `build_credentials_service.go:160` |
| 2 | **Org Anthropic API key** | org | Postgres `org_secrets` (AES-256-GCM), key `anthropic/key` | (a) SSA plaintext K8s Secret into `workflows-<org>` for the coding pod; (b) plaintext HTTP `effective-key` → agents-service | ❌ value pushed + value over HTTP | `anthropic_credential_service.go:164,284,347` |
| 3 | **Platform Anthropic fallback key** | platform | env `ANTHROPIC_PLATFORM_KEY` (git-service) ← `ANTHROPIC_API_KEY` in `.env` | returned in plaintext via `effective-key`; flows like #2(b) | ❌ value over HTTP | `config.go:109`, `docker-compose.yml:250`, `anthropic_credential_service.go:293` |
| 4 | **Coding-agent pod Anthropic key** | pod | the K8s Secret SSA'd by #2(a) | `env.valueFrom.secretKeyRef` from git-service-written Secret (NOT ESO) | ⚠️ secretKeyRef shape, but value was CP-pushed | `app-factory-coding-agent.yaml:176-180` |
| 5 | **Task JWT bearer (pod→git-service/BFF auth)** | pod | minted per-dispatch from BFF signing key | plaintext Argo WorkflowRun param → `ASDLC_BEARER` env var | ❌ value in manifest/env | `dispatch_service.go:244`, `app-factory-coding-agent.yaml:166` |
| 6 | **GitHub App private key (PEM)** | platform | OpenBao `secret/asdlc/_platform/github/app/private_key` (KV v2) | never crosses; loaded into git-service process at startup | ✅ CP-only | `openbao_store.go:125`, `app_token_minter.go:408` |
| 7 | **GitHub App webhook secret / client_secret / app_id** | platform | OpenBao `secret/asdlc/_platform/github/app/*` | read in-process by git-service for HMAC verify / OAuth exchange | ✅ CP-only | `app_token_minter.go:268,306`, `app_platform.go:73-97` |
| 8 | **Task-signing RSA private key (BFF)** | platform | on-disk PEM bind-mounted `/app/keys/task-signing.pem` | never crosses; public half served as JWKS for verifiers | ✅ public-only crosses | `task_token_manager.go:39`, `docker-compose.yml:121,142` |
| 9 | **AES master key (`CREDENTIAL_ENCRYPTION_KEY`)** | platform | env var; **defaults to 32 zero-bytes** | never crosses; protects #1/#2 at rest | ✅ CP-only (but weak default) | `config_loader.go:45`, `main.go:124` |
| 10 | **Per-repo / App webhook HMAC secret** | org/platform | PAT mode: Postgres `org_credentials.webhook_secrets` JSONB (plaintext column); App mode: OpenBao `_platform/.../webhook_secret` | read in-process by BFF/git-service to verify webhook HMAC | ✅ CP-only (but plaintext DB col) | `credential_service.go:311,659-695` |
| 11 | **Backend service creds** (Thunder client secrets, OpenBao root token, DB creds) | platform | env / compose literals (some dev placeholders) | injected via env; standard config | n/a (config-injected) | `docker-compose.yml:88,107,223` |

Compare AM `07` table: AM has Mechanism 1 (OpenBao+SecretReference+ESO) carrying *all*
pod-injected tenant secrets, Mechanism 2 (Postgres-AES) only for the out-of-OC gateway,
Mechanism 4/5 for platform crypto. **This repo collapses #1–#4 into the AM-Mechanism-2
shape (Postgres-AES + decrypt-and-push) and uses OpenBao only for #6/#7 — the inverse of
AM's design intent.**

---

## Per-secret deep trace

### #1 — Org GitHub PAT (and App installation tokens for builds)

**Submission.** Console → BFF → git-service internal route
`POST /internal/credentials/orgs/{ocOrgId}` (`credential_service.go:151,214`). PAT mode
runs a GitHub validation chain (`/user`, membership, repo-read probe,
`credential_service.go:265-278`).

**Storage.** `connectPAT` writes the PAT via `s.store.Put(ctx, ocOrgID, "github/pat", …)`
(`credential_service.go:284`). `store` is the **Postgres `dbStore`** (`main.go:129`), so
`Put` AES-256-GCM-seals the value and upserts `org_secrets(oc_org_id, key, value)`
(`db_store.go:81-94`; schema `org_secrets.go:17-22`, `value TEXT NOT NULL`). The
`org_credentials` row stores only identity/metadata + the plaintext `webhook_secrets`
JSONB — never the token (`Projection` "never contains the token", `credential_service.go:165`).

**Read-back for builds.** BFF generates a WorkflowRun name and calls git-service
`StageBuildSecret` (`build_credentials_service.go:114`). It resolves the org credential
(`userPATCred.Token` → `store.Get` → AES-open, `user_pat.go:31-38`; App mode →
`AppTokenMinter.MintForInstallation`), then **SSA-applies a `kubernetes.io/basic-auth`
Secret named `<workflowRunName>-git-secret` with the plaintext token in `StringData`
into `workflows-<ocOrgID>`** (`build_credentials_service.go:176-202`). The BFF then POSTs
the WorkflowRun with `repository.secretRef == ""` so OC's ExternalSecret synth is
**deliberately skipped** (`dispatch_service.go:596-642`; file header
`build_credentials_service.go:11-17`). The build pod mounts the Secret by name.

### #2 — Org Anthropic API key

**Submission.** `POST /internal/credentials/orgs/{org}/anthropic`
(`anthropic_credential_service.go:5,137`), body `{apiKey}`. Validated by probing
`api.anthropic.com/v1/messages` (`anthropic_credential_service.go:442`).

**Storage.** `s.store.Put(ctx, ocOrgID, "anthropic/key", []byte(key))`
(`:164`) → same Postgres-AES `org_secrets` table. Metadata (prefix/last4/status) goes to
`org_anthropic_credentials` (`:178-189`).

**Read-back, path (a) — coding-agent pod (WP).** On every dispatch the BFF calls
git-service `ApplyAnthropicWPSecret(orgID)` (`dispatch_service.go:310`). That decrypts
the key (`store.Get`, `:327`) and **SSA-applies an Opaque Secret
`anthropic-credentials` carrying `ANTHROPIC_API_KEY` (plaintext `StringData`) into
`workflows-<orgID>`** (`anthropic_credential_service.go:347-380`). It returns
`SecretRefName` (`:341`), which the BFF threads into the WorkflowRun param
`anthropic.secretRef` (`dispatch_service.go:332`); the pod consumes it via
`secretKeyRef` (`app-factory-coding-agent.yaml:176-180`).

**Read-back, path (b) — agents-service (TS agents).** agents-service calls
`GET …/anthropic/effective-key` (`anthropic-key-resolver.ts:96`); git-service's
`EffectiveKey` **decrypts and returns the key as plaintext JSON**
(`anthropic_credential_service.go:279-297`). agents-service caches it 5 min and passes it
inline to `createAnthropic({apiKey: key})` (`create-agent.ts:62-63`). In compose this
endpoint is **unauthenticated** by design (`docker-compose.yml:172-176,232-241`).

### #3 — Platform Anthropic fallback

`cfg.AnthropicPlatformKey` (`config.go:102-109`) ← `ANTHROPIC_PLATFORM_KEY`
(`config_loader.go:62`) ← `${ANTHROPIC_API_KEY}` from `deployments/.env`
(`docker-compose.yml:250`). When an org has no active key, `EffectiveKey` returns
`{source:"platform", key: platformKey}` in plaintext (`anthropic_credential_service.go:293`).
The config comment itself admits the intended target state diverges:
"cloud deployment will use a SecretReference + ESO" (`config.go:106-108`) — i.e. the
reference-only path is a *non-goal today*.

### #6/#7 — GitHub App private key + webhook/client secrets (the only OpenBao users)

Dev seed reads the PEM from `GITHUB_APP_PRIVATE_KEY_PATH`
(`/etc/github-app/private-key.pem`, bind-mounted from `deployments/github-app-private-key.pem`,
`docker-compose.yml:230,263`) and writes it + `app_id`/`client_id`/`client_secret`/
`webhook_secret` into OpenBao under `secret/asdlc/_platform/github/app/*`
(`app_platform.go:42-103`). At startup `LoadAppKeyFromOpenBao` reads it back into the
git-service process (`app_token_minter.go:408-444`); the key never leaves the process and
is used only to sign App JWTs / mint installation tokens. This is the **one genuinely
OC-aligned, value-stays-put path** — though it uses a *single shared* OpenBao policy with
path-namespacing rather than per-org roles (the code comment itself flags this as
"code discipline" not an "architectural property", `openbao_store.go:15-28`).

### #8 — BFF task-signing key

PEM at `deployments/keys/task-signing.pem`, generated by `setup-asdlc.sh:629-634`
(`openssl genpkey … rsa:2048`), bind-mounted read-only into the BFF
(`docker-compose.yml:121,142`), loaded via `BFF_TASK_SIGNING_KEY_PATH`. Parsed once at
boot (`task_token_manager.go:59-108`); the public half is served as JWKS
(`/auth/external/jwks.json`) so git-service and the runner pod can verify. Private key
never crosses — only the public key (via JWKS) and the *signed token* do. This matches
AM's Class 5 platform-crypto pattern.

### #9 — AES master key (weakest link)

`CREDENTIAL_ENCRYPTION_KEY` decoded at `main.go:124` and required to be 32 bytes — but
its **default is `"AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA="` = 32 zero bytes**
(`config_loader.go:45`) and **nothing in `deployments/` overrides it** (grep: no
`CREDENTIAL_ENCRYPTION_KEY` in compose or `.env`). So #1, #2, and any future
`org_secrets` rows are "encrypted at rest" with a hardcoded, source-visible all-zeros key
in local dev — functionally plaintext. AM's equivalent (`ENCRYPTION_KEY`,
`07` Mechanism 4) is chart-generated `randAlphaNum 32 | sha256sum` and stable across
upgrades.

---

## Gap vs Agent Manager / OpenChoreo (per secret)

**#1 GitHub build credential.**
- *AM:* build creds become an OpenChoreo **`GitSecret`** bound to
  `ClusterWorkflowPlane/default` (`08` Class 4a; `06` "Build credentials flow"). OC's CI
  consumes it; AM never writes K8s Secrets itself.
- *Here:* git-service holds the K8s API client and **SSA-writes the plaintext Secret
  itself** into `workflows-<org>` (`build_credentials_service.go:194`), explicitly
  bypassing OC's secretRef/ExternalSecret synth (`:11-17`). The control plane process is
  the secret courier, and the value lands as a raw K8s Secret rather than a `GitSecret`
  CR managed by OC.

**#2/#4 Anthropic key for the coding pod.**
- *AM (Class 1, `08`):* value → central OpenBao; a `SecretReference` CR (KV path only)
  crosses to the plane; **ESO** materializes the K8s Secret using the **reader role**
  (`amp-secret-reader-role`, `read` on `secret/data/*`). Pod consumes via `secretKeyRef`.
  Plaintext hops are only AMP→OpenBao and ESO→OpenBao.
- *Here:* value → Postgres-AES; on each dispatch git-service **decrypts and SSA-pushes
  the plaintext Secret** to the WP namespace (`anthropic_credential_service.go:347`).
  There is no `SecretReference`, no central value store, no ESO, no reader role. The
  `secretKeyRef` in the manifest (`:176`) is real, but it points at a CP-pushed Secret —
  AM's *shape* with none of the cross-plane discipline.

**#2(b)/#3 Anthropic key to agents-service.**
- *AM:* the only analogous "value crosses as plaintext over an API" case is the **gateway
  upstream key** (Mechanism 2 / Class 3) — and AM flags it as the *deliberate exception*
  forced by the gateway living **outside** OC. AM gates it behind a registration token and
  TLS.
- *Here:* the Anthropic key is served plaintext over `effective-key` to agents-service,
  which lives **inside** the platform and could consume a SecretReference — yet the design
  chooses the exception path anyway, and in compose the endpoint is **unauthenticated**
  (`docker-compose.yml:232-241`). This is strictly weaker than AM's single gateway exception.

**#5 Task JWT bearer into the pod.**
- *AM:* agent-runtime tokens are minted in CP and verified in DP against CP JWKS — the
  token crosses, but it is a short-lived bearer, not a long-lived secret, and AM does not
  put long-lived secrets in pod env at all (Class 1 uses `secretKeyRef`).
- *Here:* the Task JWT is a plaintext Argo param → env var (`ASDLC_BEARER`,
  `app-factory-coding-agent.yaml:166`). It is short-lived (TTL-capped,
  `task_token_manager.go:45`) so this is the least-bad divergence, but a bearer in a
  WorkflowRun spec is visible to anyone with `get workflowrun` in the WP namespace.

**#6/#7/#8 platform crypto.** Largely aligned with AM Classes 4/5 (value stays in CP;
only public keys cross via JWKS). The divergence is operational, not architectural:
single shared OpenBao policy + path-namespacing (`openbao_store.go:15-28`) rather than
AM's distinct **reader vs writer roles** bound to plane-scoped ServiceAccounts
(`08` step 4; AM `06` "two OpenBao policies/roles").

**#9 AES master key.** AM generates and persists a strong `ENCRYPTION_KEY`
(`07` Mechanism 4). Here the default is all-zeros and unset in the local stack — a
concrete secret-at-rest failure for #1/#2.

**Cross-cutting: two OpenBao instances.** AM uses two OpenBao backends (runtime `:8200`
and workflow-plane git `:8201`, `06`/`08` Class 4). This repo's compose points
git-service at a single `OPENBAO_ADDR` (`docker-compose.yml:222`) with root token `root`,
and uses it only for `_platform`. There is no workflow-plane OpenBao and no ESO in the
tenant-secret path at all.

---

## What must change (prioritized, concrete)

**P0 — Stop shipping an all-zeros AES key.** Make `CREDENTIAL_ENCRYPTION_KEY` mandatory
(remove the `"AAAA…="` default at `config_loader.go:45`; fail closed if unset) and
generate a strong value in `setup-asdlc.sh` (it already does `openssl rand -hex 32` for
other secrets, `:607`) wired into `deployments/.env` + compose. Mirror AM's
`randAlphaNum 32 | sha256sum`, stable across restarts (AM `07` Mechanism 4).

**P0 — Authenticate `effective-key`.** Today agents-service calls it unauthenticated
(`docker-compose.yml:232-241`). Even if the key keeps flowing over HTTP short-term, gate
the endpoint with the Service-JWT it is already designed to accept
(`config.go:84-95`), so a plaintext provider key isn't readable by anything on the network.

**P1 — Route the coding-pod Anthropic key through OpenBao + SecretReference + ESO.**
Replace `AnthropicCredentialService.ApplyWPSecret`'s SSA-push
(`anthropic_credential_service.go:347-380`) with: (1) write the value to central OpenBao
at a per-org KV path (the `OpenBaoStore.Put` path scheme `secret/asdlc/{org}/anthropic/key`
already exists, `openbao_store.go:104-112`); (2) create an OpenChoreo `SecretReference` CR
in the WP/`workflows-<org>` namespace whose `remoteRef` is that KV path; (3) let ESO (with
a **reader** role bound to the WP namespace SA) materialize the K8s Secret the pod's
`secretKeyRef` already expects. This is a drop-in for AM Class 1/2 and removes git-service
from the plaintext courier role. The manifest's `secretKeyRef` (`:176-180`) needs no change.

**P1 — Route GitHub build creds through OC `GitSecret` (or OpenBao+ESO).** Replace the
direct `basic-auth` SSA in `build_credentials_service.go:160-203` with an OpenChoreo
`GitSecret` bound to `ClusterWorkflowPlane/default` (AM `06` `git_secrets.go` pattern), so
OC's build workflow consumes it natively and git-service stops writing raw Secrets. The
"`secretRef==""` to skip ExternalSecret synth" hack (`dispatch_service.go:596-642`) goes away.

**P1 — Make OpenBao the value store for org secrets; demote Postgres to references.**
Today `main.go:129` always wires `NewDBStore`. Switch the per-org store to
`NewOpenBaoStore` (already implemented, `openbao_store.go:75`) so #1/#2 values live in
OpenBao, and reduce `org_secrets`/`org_anthropic_credentials` to **metadata + KV-path
references** (mirror AM persisting only `SecretReference` names / `SecretKVPath`, `06`
publisher provisioner). This eliminates the Postgres-AES at-rest surface entirely.

**P2 — Split OpenBao roles.** Replace the single shared policy + path-namespacing
(`openbao_store.go:15-28`) with AM's **reader (DP/WP)** vs **writer (CP)** roles bound to
plane-scoped ServiceAccounts (AM `08` step 4; `openbao-config-job.yaml`). This turns
per-org isolation from "git-service code correctness" into an architectural property — the
exact concern the code comment already raises.

**P2 — Agents-service: prefer SecretReference over plaintext fetch.** Longer term, if
agents-service runs inside OC, give it a mounted K8s Secret (ESO-materialized from the
same per-org OpenBao path) instead of fetching plaintext via `effective-key`
(`anthropic-key-resolver.ts`). That removes the last plaintext-over-HTTP hop and matches
AM's "keep the consumer inside OC → route everything through Mechanism 1" lesson
(`07` "Design lesson").

**P3 — Move the Task JWT bearer out of the WorkflowRun param/env.** Consider delivering it
via a short-lived projected ServiceAccount token or a per-run Secret mount instead of the
plaintext `ASDLC_BEARER` arg (`app-factory-coding-agent.yaml:103-104,166`), so it isn't
visible in WorkflowRun specs.
