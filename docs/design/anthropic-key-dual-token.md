# Dual Anthropic key model — org + platform

**Status:** design, **iteration 3** — pivoted after user correction: per-org secrets live in Postgres `org_secrets` (AES-256-GCM), not OpenBao, since commit `2f26614` (PR #16). The OpenBao policy-split machinery in iteration 2 disappears entirely. Iterations recorded inline (§9).

**Owners:** anjana
**Related:** the deleted `anthropic-key-per-org.md` (single-key, per-org with platform fallback). This doc supersedes and re-frames it as a two-key model that obeys the user's "platform key is *never* in a workflow pod" rule.

---

## 1. Problem & rules

Today there is exactly one Anthropic API key in the cluster:

- Mounted into `app-factory-agents-service` from `deployments-v2/.env` (`${ANTHROPIC_API_KEY}` substituted into the env-overlay).
- Synth'd per-WorkflowRun into the `app-factory-coding-agent` ClusterWorkflow pod via an inline `resources[].template ExternalSecret` pulling `secret/apps/anthropic`.

Both readers share the same key. The user wants two distinct keys with different routing rules:

| Reader | Key it must use | If org key missing |
|---|---|---|
| `app-factory-coding-agent` pod (per-WorkflowRun, ephemeral) | **org key only** | **Block dispatch.** Button is disabled in console, BFF returns 422 `anthropic_key_required` as defence-in-depth |
| `app-factory-agents-service` (long-lived: BA, Architect, TechLead, document-generation) | **org key preferred**, platform fallback | Silently use platform key |

**Invariants (load-bearing):**

1. **Platform key never lands in a workflow pod, ever.** It exists only as an env var in git-service (and only in CP namespaces). Coding-agent pods never see it — there is no Vault path, no SecretReference, no ExternalSecret, no env injection that could deliver it to the WP.
2. **Org key only travels three places:** Postgres `org_secrets(oc_org_id, key="anthropic/key", value=AES-encrypted)`, an in-process decryption in git-service, and a per-org K8s Secret materialised inside `workflows-<ocOrgId>` for the coding-agent to mount. It is never returned to the console.
3. **Agents-service "effective key" lookup is silent** — no console hint about which key answered. The only UI surface that reflects token state is the org settings page and the coding-agent "disabled" state.
4. **Disconnect does not cancel in-flight WorkflowRuns** — once a pod has the Secret mounted, let it finish.

## 2. UX

### 2.1 Org Settings → Anthropic Integration

New left-rail entry in `OrgSettingsLayout`, mirroring GitHub Integration:

```
Settings
├── Integrations
│   ├── GitHub Integration              (existing)
│   └── Anthropic Integration           (new)
```

Route: `/organizations/:orgId/settings/anthropic` → `OrgAnthropicSettings.tsx`.

Three states (same shape as `OrgGitHubSettings`):

- **Not configured** — single password input, "Save & Validate" button. Microcopy: "Required to dispatch the remote coding agent. Spec / design / task generation will use the platform-provided key as a fallback."
- **Connected** — green dot, prefix + last-4 (`sk-ant-ap03-A1B2…XyZw`), "Last validated N min ago". Buttons: Replace key, Disconnect.
- **Validation failed** — red badge, structured error. Buttons: Replace key, Disconnect.

### 2.2 Coding-agent gating

On every project page surface that triggers the coding-agent ("Implement with Remote Agent", "Execute all → Remote Agents", per-task "Run" with mode=remote):

- **Frontend:** button disabled with tooltip "Configure an Anthropic API key in Org Settings to dispatch the remote agent." Tooltip is a link to `/organizations/{org}/settings/anthropic`.
- **BFF:** `POST /tasks/{id}/dispatch` (and the batch variant) pre-flights via the org-anthropic projection; if `status != "active"`, returns:
  ```json
  { "error": {
      "code": "anthropic_key_required",
      "message": "An Anthropic API key must be configured at the org level before the coding agent can run.",
      "settingsUrl": "/organizations/{org}/settings/anthropic"
  } }
  ```
  HTTP 422. Same error-code shape as the existing `app_bind_not_configured` GitHub error.

The agents-service path (requirements / architecture / task generation / wireframe / doc-generation) is **never** gated — it works whether or not the org key is configured.

### 2.3 Disconnect

`Disconnect` deletes:
1. The `org_secrets` row (`oc_org_id`, `key='anthropic/key'`) — the encrypted key bytes.
2. The `org_anthropic_credentials` row (metadata: prefix, last4, status) — or flips its status to `'disconnected'` for audit, then deletes on a later sweep.
3. The per-org K8s Secret in `workflows-<ocOrgId>` (`anthropic-credentials`), best-effort.

After disconnect:
- New coding-agent dispatches are blocked (button disabled, BFF 422).
- In-flight WorkflowRuns keep running — their Secret was already projected into the pod.
- Agents-service silently falls back to the platform key on the next call.

## 3. Mechanism — mirroring the GitHub-credentials pattern

The user said "follow same pattern as github secrets, check how they stored in postgres too". The relevant GitHub-credentials reference points (current, post-commit `2f26614`):

| Surface | GitHub pattern | What this doc reuses |
|---|---|---|
| Secret value at rest | `org_secrets(oc_org_id, key="github/pat", value=AES-256-GCM)` (Postgres, AEAD via `dbStore`) — see `git-service/pkg/credentials/db_store.go` and migration `git-service/database/migrations/org_secrets.go` | Same table — `org_secrets(oc_org_id, key="anthropic/key", value=AES-256-GCM)`. New `key` value, same plumbing, **zero schema changes** to `org_secrets`. |
| Metadata Postgres home | `org_credentials` table in git-service Postgres (identity, status, installation_id, etc.) | **New** table `org_anthropic_credentials` (prefix, last4, status, last_validated_at) — separate from `org_credentials` because of incompatible CHECK constraints on `kind` |
| Service ownership | git-service owns the credential store read/write and the projection | Same — extend `CredentialService` / use existing `OpenBaoStore` interface backed by `dbStore` |
| HTTP surface | `/internal/credentials/orgs/{ocOrgId}` (connect/status/disconnect) | `/internal/credentials/orgs/{ocOrgId}/anthropic` (mirror shape) |
| Authz | service JWT (audience: git-service) | Same — no new auth class |
| Delivery into WP | per-org K8s Secret materialised in `workflows-<ocOrgId>` (see commit `d4d309f`), mounted by name via `parameters.repository.secretRef` | per-org K8s Secret `anthropic-credentials` in `workflows-<ocOrgId>`, mounted via `parameters.anthropic.secretRef` |
| Cleanup | `BuildSecretCleaner.DeleteBuildSecret` on disconnect | New `AnthropicSecretCleaner.DeleteAnthropicSecret` on a parallel interface (see §7) |

**Note on naming.** Despite the type-name `OpenBaoStore`, the live implementation is `dbStore` (Postgres + AES-256-GCM). The interface name was kept for backwards compatibility through the migration; the design treats it as "the per-org KV store" abstraction. There is no OpenBao read or write involved in the org-key flow.

### 3.1 Why a *separate metadata table* rather than extending `org_credentials`

`org_credentials` has CHECK constraints on `kind ∈ {app-installation, user-pat}` and shape constraints `secrets_shape_per_kind` / `app_fields` that are GitHub-specific (webhook_secrets JSONB, installation_id, selected_repos). Forcing Anthropic into a third `kind` and relaxing those constraints would break the existing invariants. A separate metadata table — same shape philosophy, separate semantics — is the cleaner mirror.

The **encrypted key value** still lives in the generic `org_secrets` table (no schema change). The new metadata table only stores non-secret projection fields (prefix, last4, status, last_validated_at, validation_error).

### 3.2 Why per-org K8s Secret, not the prior design's per-run ExternalSecret

The deleted design used the older `externalRefs[]` + per-run ExternalSecret pattern, which was the canonical OC pattern at the time (`dockerfile-builder.yaml` ~188–219). Since commit `d4d309f` (Nov 2026), the dockerfile-builder switched to a per-org K8s Secret materialised directly by git-service into the org's WP namespace and mounted by name. **The "same pattern as GitHub secrets" today is the newer pattern, not the old one.** Adopting it gives us:

- One refresh point per dispatch (git-service SSA), not one per WorkflowRun's ESO sync.
- No `externalRefs[]` indirection — keeps the ClusterWorkflow simple.
- Cleanup on disconnect is a single K8s Secret delete.
- No OpenBao involvement at all — per-org keys live in Postgres `org_secrets` (post-`2f26614`), so the previous design's Vault-reader-role question disappears. Existing build-credential flow uses the same path; we inherit its operational properties for free.

### 3.3 Why an HTTP resolver for agents-service, not a mounted Secret

`app-factory-agents-service` is a long-lived multi-tenant Workload (no per-request pod). It cannot have per-org Secrets mounted directly. Two viable options:

1. Per-request HTTP call to git-service `/internal/credentials/orgs/{ocOrgId}/anthropic/effective-key`, returning the key bytes over service-JWT-authed HTTP, cached in a small TTL LRU.
2. Mount the platform key as the default + look up org keys lazily.

(2) doesn't solve anything — we still need a fetch path for org keys. (1) is the simplest reuse of git-service as "the service that owns per-org credentials". This adds **one new Postgres SELECT + decrypt per first-call-per-org** in agents-service; the LRU keeps steady-state at zero.

The endpoint returns the *effective* key — `{ source: "org" | "platform", key: "sk-ant-…" }`. agents-service doesn't decide which to use; git-service does the lookup:

- If `org_anthropic_credentials.status='active'` for the org → SELECT from `org_secrets` and decrypt with the git-service AES key. Source = `org`.
- Else → return the value of git-service's `ANTHROPIC_PLATFORM_KEY` env var. Source = `platform`.

Source is logged at agents-service for telemetry only — never exposed to the console.

**Conscious divergence from agent-manager.** The canonical OC pattern for "per-tenant secret routing into a long-lived service" is `upsertSecretReference` (`agent-manager-service/clients/secretmanagersvc/client.go:324-365`) — one OC `SecretReference` per (org, secret) pair, ESO-materialised into the consumer's namespace, mounted as env / files. We deliberately reject this here for ASDLC because:

- agents-service is not an OC Component today — it's a `deployments-v2` Workload. Adopting SecretReference-per-org would require either env-var fanout (one env per org) or a projected-volume layout that agents-service has no code path for today.
- ESO refresh stacks: at *N* orgs × 5-min `refreshInterval`, the steady-state Vault read rate scales with org count even when only a fraction are AI-active. The HTTP resolver scales with **active** orgs only.
- git-service is **already** a hard dependency of every dispatch (mint-build, identity, repo provisioning). Adding agents-service to that dependency edge does not introduce a new failure mode class.
- The post-`2f26614` move *off* OpenBao for per-org secrets means there is no Vault-backed `SecretReference` to project anyway. Going to OC's pattern would require migrating per-org keys *back* to a KV-store ESO can read.

If we ever onboard agents-service as a first-class OC Component and per-org keys move back to a Vault-shaped store, revisit the SecretReference path.

## 4. Data model

### 4.1 Storage paths

**Per-org key — Postgres (no OpenBao):**

```
table:  org_secrets
row:    (oc_org_id="<ocOrgId>", key="anthropic/key", value=<base64(nonce||ciphertext+tag)>, updated_at=now())
```

Same shape as the per-org GitHub PAT (`key="github/pat"`). Encrypted with AES-256-GCM via `dbStore.seal`. The encryption key is the existing `CREDENTIAL_ENCRYPTION_KEY` env value — no new key material.

**Platform key — env var (no OpenBao):**

```
git-service env:  ANTHROPIC_PLATFORM_KEY=<bare key>
```

Sourced differently per deployment tier:

- **Local k3d (`deployments-v2/`):** `${ANTHROPIC_API_KEY}` substituted via `envsubst` from `deployments-v2/.env` into `deployments-v2/manifests/env-overlays/app-factory-git-service.yaml`. Same mechanism the agents-service mount uses today; the env-overlay loader is `lab-app-factory-uses-envsubst-not-flux` style (no Flux `substituteFrom`). Identical blast radius and security posture to the current `${ANTHROPIC_API_KEY}` flow into agents-service — we are *relocating* the env, not introducing a new injection.
- **WSO2 Cloud (`controlplane/{aws,onprem}` branches):** **out of scope for this design.** Cloud hosting requires a `SecretReference` in `domains/platform/namespaces/<git-service-ns>/secret-references/anthropic-platform-key.yaml` that ESO materialises into a K8s Secret in git-service's namespace; the Workload picks it up via `envFrom.secretRef`. This is the same shape as the existing `secret-references/anthropic-credentials.yaml` we are deleting from the developer-domain side. Document as a follow-up PR; do not block this design on it.

Read once at startup into `config.Config.AnthropicPlatformKey`; injected into `AnthropicCredentialService` as a constructor argument. Empty string is allowed — agents-service surfaces `503 no_anthropic_key_configured` if both the org row and the env value are absent.

**The legacy `secret/apps/anthropic` OpenBao path is deleted entirely** as part of this work — no consumer is left after the design lands.

### 4.2 Postgres — new table in git-service

```sql
CREATE TABLE org_anthropic_credentials (
  oc_org_id          TEXT PRIMARY KEY,
  key_prefix         TEXT NOT NULL,            -- "sk-ant-ap03-A1B2"
  key_last4          TEXT NOT NULL,            -- "XyZw"
  status             TEXT NOT NULL DEFAULT 'active'
                         CHECK (status IN ('active','invalid','disconnected')),
  connected_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
  last_validated_at  TIMESTAMPTZ,
  validation_error   TEXT
);
```

Migration lives next to the existing ones in `git-service/database/migrations/`. Idempotent (`CREATE TABLE IF NOT EXISTS`).

### 4.3 Per-org K8s Secret in WP namespace (lazy materialisation)

git-service materialises one Secret per active org **on every dispatch**, not on Connect. The DB row is the authoritative source-of-truth; the K8s Secret is SSA-refreshed every dispatch (same model as `MintBuildToken` at `build_credentials_service.go:172-220`).

```yaml
apiVersion: v1
kind: Secret
metadata:
  name: anthropic-credentials
  namespace: workflows-{ocOrgId}
  labels:
    app-factory.openchoreo.dev/managed-by: git-service
    app-factory.openchoreo.dev/kind: anthropic
type: Opaque
data:
  ANTHROPIC_API_KEY: <base64>
```

**Why lazy:** parity with `MintBuildToken`. The build-credential Secret is recomputed from the credential store (Postgres `org_secrets`) on every dispatch — same FieldOwner, same SSA shape — so the Secret in the WP is always exactly what `org_secrets` says it is. Adopting the same model for the Anthropic Secret means rotating the key (i.e. Connect-Replace) is reflected on the *next* dispatch without any separate sync mechanism, and disconnect → reconnect → dispatch ends up with a fresh Secret without further code. The WP-namespace-existence question is incidental — onboarding pre-provisions `workflows-<orgID>` (see `build_credentials_service.go:184-190`) — but the self-healing property is the real reason to push the K8s write to dispatch time.

**On Disconnect:** the K8s Secret is deleted best-effort if the namespace exists, alongside the `org_secrets` row and the `org_anthropic_credentials` row.

### 4.4 `secret-references/anthropic-credentials.yaml` — deleted

`deployments-v2/wso2cloud-deployment/wso2cloud-local/domains/platform/namespaces/wso2cloud/secret-references/anthropic-credentials.yaml` exists today and projects `secret/apps/anthropic` into the `wso2cloud` namespace for agents-service consumption. In this design **agents-service no longer mounts the Anthropic key as env** — it fetches the *effective* key per-call via the git-service resolver (§6.4). The SecretReference has no remaining consumer, so it is deleted.

This is a `domains/platform/` artifact (PE-owned, MEDIUM blast-radius per the wso2cloud-deployment README change matrix); the PR description must call the deletion out explicitly so the platform team understands the env-substitution path is going away.

## 5. ClusterWorkflow change

`app-factory-coding-agent.yaml` (in `wso2cloud-deployment/wso2cloud-local/domains/platform/cluster-shared/cluster-workflows/`) becomes (parameter shape mirrors `dockerfile-builder.yaml`'s `repository.secretRef` naming):

```yaml
parameters:
  openAPIV3Schema:
    properties:
      anthropic:
        type: object
        required: [secretRef]
        properties:
          secretRef: { type: string }   # "anthropic-credentials" — name of the per-org K8s Secret in workflows-<orgID>
```

Pod env block:

```yaml
- name: ANTHROPIC_API_KEY
  valueFrom:
    secretKeyRef:
      name: '{{workflow.parameters.anthropic-secret-ref}}'
      key: ANTHROPIC_API_KEY
```

`resources:` block deletes the existing `anthropic-secret` ExternalSecret entry entirely. The per-org K8s Secret is pre-applied by git-service in the dispatch pre-flight (§6.2). No per-run ESO sync.

`anthropic-secret-ref` argument wires from `${parameters.anthropic.secretRef}` in `arguments.parameters`.

## 6. Flows

### 6.1 Save key (Connect) — no K8s write

```
console → BFF POST /api/v1/organizations/{orgId}/anthropic { apiKey }
  BFF (cred-anthropic proxy) → git-service POST /internal/credentials/orgs/{orgId}/anthropic { apiKey }
    git-service:
      1. validate key shape (regex)
      2. probe POST https://api.anthropic.com/v1/messages (minimal request) → 401 = key_invalid
      3. store.Put(orgId, "anthropic/key", key)   // dbStore — AES-256-GCM into org_secrets
      4. upsert org_anthropic_credentials (prefix, last4, status='active', last_validated_at=now)
      5. broadcast cache invalidate to agents-service
      6. return projection { keyPrefix, keyLast4, status, lastValidatedAt }
  BFF → 200 with projection
```

Connect does **not** touch the WorkflowPlane. The K8s Secret is applied lazily on first dispatch (§6.2), so disconnect → reconnect → first-dispatch always produces a fresh WP Secret.

### 6.2 Dispatch coding-agent (org has key)

```
console: button enabled (projection.status == 'active')
console → BFF POST /api/v1/tasks/{taskId}/dispatch
  BFF (DispatchService):
    1. pre-flight: get anthropic projection from git-service; reject 422 if not 'active'
    2. existing flow: provision branch, issue, PR, mint bearer
    3. git-service POST /internal/credentials/orgs/{orgId}/anthropic/apply-wp-secret
       git-service:
         - store.Get(orgId, "anthropic/key")  // SELECT + AES decrypt from org_secrets
         - SSA-apply Secret workflows-{orgId}/anthropic-credentials with the value
         - returns { secretRefName: "anthropic-credentials" }
       (Same pattern as MintBuildToken: WP Secret is materialised per-dispatch,
        not stored long-term in K8s. The Postgres org_secrets row is the source of truth.)
    4. OC.TriggerCodingAgent(... CodingAgentParams{ AnthropicSecretRef: "anthropic-credentials" })
  OC creates WorkflowRun with parameters.anthropic.secretRef = "anthropic-credentials"
  Argo pod mounts workflows-{orgId}/anthropic-credentials.ANTHROPIC_API_KEY as env
```

Per-dispatch SSA is cheap (one K8s PATCH) and means the Secret is always up-to-date with whatever's in `org_secrets` — same property the build-credential Secret has post-`d4d309f`.

**BFF parallelisation:** `MintBuildToken` (build-credential) and `ApplyAnthropicWPSecret` (anthropic-credential) are independent. `DispatchService` runs them concurrently via `errgroup` so dispatch wall-clock is `max(t_build, t_anthropic) + t_trigger`, not the sum.

**Orphan WP Secrets are inert.** If `ApplyAnthropicWPSecret` succeeds but `TriggerCodingAgent` fails afterwards, the Secret sits in `workflows-<orgID>` unused — nothing mounts it outside an active WorkflowRun, and the next dispatch SSAs the freshest value from `org_secrets`. Disconnect sweeps the Secret best-effort; any residual after disconnect cannot be reached because the next dispatch is blocked at the 422 pre-flight. No leak.

### 6.3 Dispatch coding-agent (no org key) — defence-in-depth

```
console: button disabled, tooltip "Configure Anthropic API key in Org Settings → Anthropic Integration"
(if user bypasses via stale state)
  BFF → 422 {
    error: {
      code: "anthropic_key_required",
      message: "...",
      settingsUrl: "/organizations/{org}/settings/anthropic"
    }
  }
  console: modal with "Configure key" CTA
```

### 6.4 agents-service call (silent org-or-platform)

```
BFF → agents-service POST /v1/agents/architect (X-Oc-Org-Id: {orgId})
  agents-service:
    1. effective-key cache lookup keyed on orgId
    2. on miss → git-service GET /internal/credentials/orgs/{orgId}/anthropic/effective-key
       git-service:
         - if org_anthropic_credentials.status='active' → store.Get(orgId, "anthropic/key") (Postgres SELECT + AES decrypt)
         - else → return config.AnthropicPlatformKey  // env var
         - if both empty → return { source: "none" }
         - return { source: "org" | "platform" | "none", key: "..." }
    3. cache (orgId → { source, key }) with TTL 5 min
    4. createAnthropic({ apiKey: key }).chat(model)  // see §7.2
  agents-service logs: { orgId, source }  // observability only
```

**Header plumbing:** the BFF currently passes no org context to agents-service. New header `X-Oc-Org-Id` populated from the dispatching user's JWT `org` claim — same source as everywhere else in the BFF. agents-service's middleware mandates the header on every `/v1/agents/*` route (415 otherwise).

**Cache invalidation:** Connect / Disconnect emits a `POST /v1/internal/cache/invalidate` to agents-service (service JWT). 5-min TTL bounds staleness if the invalidation is missed (e.g. agents-service restart between Connect and the next call). No event bus required.

**The platform key never reaches a workflow pod via this path** — it lives in git-service's env (CP namespace), is read by git-service, and returned inline only to agents-service (also CP). Coding-agent pods (WP) never receive it through any path because the dispatch flow reads only the per-org row and 422s if the org row is missing.

### 6.5 Disconnect

```
console → BFF DELETE /api/v1/organizations/{orgId}/anthropic
  BFF → git-service DELETE /internal/credentials/orgs/{orgId}/anthropic
    git-service:
      1. status flip to 'disconnected' (org_anthropic_credentials)
      2. store.Delete(orgId, "anthropic/key")  (DELETE row from org_secrets)
      3. delete K8s Secret workflows-{orgId}/anthropic-credentials  (best-effort)
      4. notify agents-service cache invalidate
  BFF → 204
```

In-flight WorkflowRuns: untouched. Each pod already has the Secret mounted as env at start.

## 7. Code change list (file-level)

### Go — git-service
- `git-service/models/org_anthropic_credential.go` — **new**, mirrors `OrgCredential` metadata-only (PRIMARY KEY oc_org_id; columns: key_prefix, key_last4, status, connected_at, last_validated_at, validation_error). **No secret bytes** on this row — the key value lives in `org_secrets`.
- `git-service/database/migrations/anthropic_credentials.go` — **new** migration (idempotent `CREATE TABLE IF NOT EXISTS` + `CHECK (status IN ('active','invalid','disconnected'))`).
- `git-service/database/database.go` — register the new migration in the sequence.
- `git-service/services/anthropic_credential_service.go` — **new**. `Connect / Status / Disconnect / EffectiveKey / ApplyWPSecret` mirroring `CredentialService` shape. Uses the existing `credentials.OpenBaoStore` (i.e. `dbStore`) for Put/Get/Delete on key `"anthropic/key"`. `ApplyWPSecret(ctx, orgId)` is the per-dispatch SSA call (§6.2): SELECT + decrypt → SSA `anthropic-credentials` in `workflows-<orgId>`. Reuses the controller-runtime client and FieldOwner stamp from `BuildCredentialsService`.
- `git-service/services/build_credentials_service.go` — hoist the SSA helper (`applySecret(ns, name, key, value)`) into a small shared internal helper so both build and anthropic call sites use one path; minimal refactor.
- `git-service/services/validator_probes.go` — extend with `ValidateAnthropicKey(ctx, key) → ValidationError`. Probe is `POST https://api.anthropic.com/v1/messages` with a tiny payload; 401 → `anthropic_key_invalid`, 5xx → `anthropic_unreachable`.
- `git-service/api/credentials_routes.go` — add `POST/GET/DELETE /internal/credentials/orgs/{ocOrgId}/anthropic`, `GET /internal/credentials/orgs/{ocOrgId}/anthropic/effective-key`, `POST /internal/credentials/orgs/{ocOrgId}/anthropic/apply-wp-secret`.
- `git-service/pkg/credentials/db_store.go` — **no change**. The existing `Get/Put/Delete(ocOrgID, "anthropic/key", …)` covers the per-org path. Anthropic is just another `key` value alongside `"github/pat"`.
- `git-service/pkg/credentials/openbao_store.go` — **no change**; nothing in the Anthropic flow touches it.
- `git-service/services/credential_service.go` — **mechanical rename**: the existing `WPSecretCleaner` interface becomes `BuildSecretCleaner` (lines 39–41 declaration; lines 52, 558 call sites). The new `AnthropicCredentialService` implements a parallel `AnthropicSecretCleaner` with `DeleteAnthropicSecret`. Each service owns only its own cleanup. (S5 fix.) `WithWPCleaner(cleaner)` becomes `WithBuildSecretCleaner(cleaner)` for symmetry.
- `git-service/config/config.go` + `config_loader.go` — add `AnthropicPlatformKey string` field; load from env `ANTHROPIC_PLATFORM_KEY` (optional, empty = no fallback).
- `git-service/cmd/git-service/main.go` — wire `AnthropicCredentialService` next to `CredentialService`, passing `cfg.AnthropicPlatformKey`. Plumb a small in-process `BroadcastCacheInvalidate(orgId)` hook the service calls; nil-safe.

### Go — BFF (asdlc-service)
- `asdlc-service/clients/gitservice/anthropic_credentials.go` — **new** client wrappers: `CreateOrReplaceAnthropic`, `GetAnthropicProjection`, `DeleteAnthropic`, `ApplyAnthropicWPSecret`. The `effective-key` lookup is **not** consumed by the BFF — it's consumed by agents-service directly (see TS section).
- `asdlc-service/api/org_anthropic_routes.go` — **new** mounted at `/api/v1/organizations/{orgId}/anthropic`. Same auth boundary as `OrgGitHubSettings` (org JWT).
- `asdlc-service/services/dispatch_service.go` — pre-flight the anthropic projection before any `TriggerCodingAgent`. Call `ApplyAnthropicWPSecret` immediately before `TriggerCodingAgent` so the Secret is fresh in `workflows-<orgId>` when the pod starts. Return `ErrAnthropicKeyRequired` → 422 with structured code on missing/invalid projection.
- `asdlc-service/clients/openchoreo/component_client.go` — `CodingAgentParams.AnthropicSecretRef` field (`string`, value: `"anthropic-credentials"`); thread into `codingAgentParameters` under `parameters.anthropic.secretRef`.

### TypeScript — agents-service
- `agents/src/shared/anthropic-key-resolver.ts` — **new**. HTTP client to git-service `effective-key`, in-process 5-min LRU.
- `agents/src/shared/create-agent.ts` — replace `anthropic(config.model)` with `anthropic(config.model, { apiKey: await resolve(orgId) })`.
- `agents/src/server/routes/{architect,document-generation,tech-lead}.ts` — same swap. Pull `orgId` from `req.header('x-oc-org-id')`; 400 if missing.
- `agents/src/server/index.ts` — add `POST /v1/internal/cache/invalidate { orgId }` route (service-JWT, audience: agents-service).

### TypeScript — console
- `console/src/pages/OrgAnthropicSettings.tsx` — **new** (mirrors `OrgGitHubSettings.tsx`).
- `console/src/components/ConnectAnthropicForm.tsx` — **new** (single password field, validate, save).
- `console/src/api/anthropic.ts` — **new** client for the 4 BFF routes.
- `console/src/pages/OrgSettingsLayout.tsx` — add Anthropic Integration row to the rail.
- `console/src/App.tsx` — wire the route.
- `console/src/pages/ProjectTasksPage.tsx` and any "Implement with Remote Agent" callsite — disable button when `useOrgAnthropic().status !== 'active'`, plus tooltip.

### Manifests / kustomize
- `deployments-v2/wso2cloud-deployment/wso2cloud-local/domains/platform/cluster-shared/cluster-workflows/app-factory-coding-agent.yaml` — drop the `resources[].anthropic-secret` ExternalSecret block, drop `anthropic-secret` argument, add `anthropic-secret-ref` argument bound to `${parameters.anthropic.secretRef}`, update env block to point at the per-org Secret name.
- `deployments-v2/wso2cloud-deployment/wso2cloud-local/domains/platform/namespaces/wso2cloud/secret-references/anthropic-credentials.yaml` — **delete**. agents-service now reads the key via git-service resolver, not via a mounted volume. (MEDIUM blast-radius platform artifact; PR description must call out the deletion for the platform team.)
- `deployments-v2/manifests/env-overlays/app-factory-agents-service.yaml` — **remove** the `ANTHROPIC_API_KEY` env entry. Confirm `GIT_SERVICE_URL` is plumbed (already in env today for the existing git-ops calls).
- `deployments-v2/manifests/env-overlays/app-factory-git-service.yaml` — **add** `ANTHROPIC_PLATFORM_KEY: "${ANTHROPIC_API_KEY}"` (substituted from `deployments-v2/.env` at apply time, same source as the existing platform-key today). Optional — empty value means "no platform fallback", which surfaces as 503 `no_anthropic_key_configured` from agents-service.
- `deployments-v2/wso2cloud-deployment/wso2cloud-local/init/layer-1/cluster-secret-store/openbao-seed-secrets.yaml` — **delete** the `secret/apps/anthropic` seed line if/when added; the platform key no longer lives in OpenBao.
- **No OpenBao role/policy changes are needed.** The per-org key never enters OpenBao, the platform key never enters OpenBao, and no pod in `workflows-*` reads any anthropic path. The earlier iteration's policy-split (§7.1) is **dropped** along with the OpenBao dependency.

### 7.1 OpenBao isolation — no work needed

The earlier iteration of this design assumed per-org and platform Anthropic keys lived in OpenBao and required a policy split to prevent the WP from reading the platform key. After the iteration-3 pivot:

- **Per-org keys are in Postgres** (`org_secrets` encrypted with AES-256-GCM). Never enter OpenBao. Not readable from the WP.
- **Platform key is an env var on git-service** (CP namespace). Never enters OpenBao. Not readable from the WP.

Invariant 1 ("platform key never reaches a WP pod") is upheld by the *absence of any Vault/Secret path* between the key and the WP, not by a policy boundary. This is strictly safer than the policy-split approach because there is no Vault role binding to mis-configure.

If a future change re-introduces an OpenBao read path for either key, this section should be revisited and the policy-split approach (preserved in git history at iteration 2) becomes relevant again.

### 7.2 Anthropic SDK call shape

The `@ai-sdk/anthropic` package exports a *default provider* `anthropic` that reads `process.env.ANTHROPIC_API_KEY` and a *factory* `createAnthropic(options)` that takes an explicit `apiKey`. The right shape for per-call key injection is:

```ts
import { createAnthropic } from "@ai-sdk/anthropic";

const { source, key } = await resolveAnthropicKey(orgId);  // git-service HTTP + LRU
const provider = createAnthropic({ apiKey: key });
const model = provider.chat(config.model);  // pass model id to factory output

const result = streamText({ model, /* ...rest unchanged */ });
```

Every call site that today does `anthropic(config.model)` becomes the factory pattern. Affected files:

- `agents/src/shared/create-agent.ts:59`
- `agents/src/server/routes/architect.ts:77`
- `agents/src/server/routes/tech-lead.ts:87,299`
- `agents/src/server/routes/document-generation.ts:68`

After the change, removing `ANTHROPIC_API_KEY` from the agents-service env-overlay is safe — the default `anthropic` import is no longer reached.

## 8. Edge cases & failure modes

| Scenario | Behaviour |
|---|---|
| Connect with invalid key | git-service probe returns 401 → 400 with code `anthropic_key_invalid`. No DB rows written. |
| Connect over an existing 'invalid' row | Treat as Replace: re-validate, flip status back to 'active' on success. |
| Connect race (two browser tabs) | Org-scoped advisory lock `pg_advisory_xact_lock(hashtext('org_anthropic:' || ocOrgID))`. Last write wins. |
| Postgres unreachable on Connect | 503 `db_unavailable` (gorm error). No rows written. The Connect handler wraps the `org_secrets` write + `org_anthropic_credentials` upsert in a single transaction so partial writes are impossible. |
| Postgres unreachable on agents-service resolver call | git-service surfaces 503; agents-service returns 502 to BFF; user sees "AI service temporarily unavailable". No silent fallback to a stale cached key past TTL. |
| Postgres unreachable on Disconnect | All-or-nothing under the same transaction; on failure the row stays `'active'` and the K8s Secret delete is skipped. User can retry. |
| K8s Secret apply fails at dispatch pre-flight | Dispatch returns 502 with `wp_secret_apply_failed`. No `TriggerCodingAgent` call is made. DB rows are untouched (the Connect transaction already committed earlier; dispatch is read-only against `org_secrets`). Next dispatch retries. |
| Disconnect mid-dispatch | Dispatch path took a snapshot of the projection at pre-flight; trigger has already completed by the time disconnect lands. If they race the other way (disconnect first, dispatch second), the dispatch pre-flight sees `status != 'active'` and returns 422 cleanly. |
| Validator drift (key revoked at Anthropic) | Deferred — no background revalidation tick in v1. Surface at next agents-service call (Anthropic 401 → bubble to UI as a soft error) or next dispatch (key absent from K8s Secret would fail the pod, surfaced via the existing coding-agent watcher). Background tick is a follow-up. |
| Platform key absent | git-service's `ANTHROPIC_PLATFORM_KEY` is the empty string; if no org row exists either, return `{ source: "none" }` to agents-service. agents-service emits 503 with `no_anthropic_key_configured`. |
| Cache staleness after Disconnect | 5-min TTL bound. Cache invalidate notification is best-effort. |
| WorkflowRun in-flight when key changed | The pod was started with the *old* env value. Argo doesn't re-mount. Acceptable — explicitly listed as a non-goal in §10. |
| `CREDENTIAL_ENCRYPTION_KEY` rotated | Out of scope. The existing GitHub-PAT path has the same vulnerability — rotating the AES key invalidates every encrypted row. Documented for symmetry but no new gap introduced by this design. |

## 9. Iteration log

### Iteration 0 — initial spec from user (verbatim summary)

- Two kinds of Anthropic token.
- Org-wide: configured in org settings (mirror GitHub PAT). Used **only** by the coding-agent worker; when absent, coding-agent UI is greyed out.
- Platform-wide: used by req / architecture / tasks generation when the user hasn't given an org key.
- Platform key **never** gets passed into a workflow pod.

### Iteration 0.5 — disambiguation Q&A with user

1. *Agents-service routing:* **prefer org key, fall back to platform** (silently).
2. *Coding-agent gating when no org key:* **disabled button + tooltip + 422 BFF defence-in-depth.**
3. *Disconnect blast:* **in-flight WorkflowRuns keep running.**
4. *Ownership of new credential surface:* **extend git-service**, mirror the GitHub-credentials Postgres + service shape exactly.

### Iteration 1 — design draft (this document)

Captured in §§1–8 above.

### Iteration 1 — reviewer feedback (platform-design-expert, condensed)

**Hard violations**

- **H1 — OpenBao policy already lets WP read the platform key.** The current `openchoreo-secret-reader-role` is bound to `dp*,external-secrets,openchoreo-ci-default,workflows-*` with policy `path "secret/data/*" {read}` (verified at `wso2cloud-deployment/wso2cloud-local/init/layer-1/cluster-secret-store/openbao-seed-secrets.yaml:28-32` and `init/layer-0/tools/openbao.yaml:52-65`). Any default-SA pod in `workflows-<orgID>` can mint a Vault token via k8s auth and read `secret/data/apps/anthropic`. The original §7 fix (narrow the role to exclude `openchoreo-workflow-plane`) targets the wrong namespace and treats Vault paths as path-scoped role bindings (they aren't). Need a real policy split.
- **H2 — `@ai-sdk/anthropic` factory shape wrong.** `anthropic(modelId)` returns a model that auto-reads `process.env.ANTHROPIC_API_KEY`. To inject an apiKey you need `createAnthropic({ apiKey }).chat(modelId)`. Verified at `agents/node_modules/@ai-sdk/anthropic/dist/index.d.ts:1084`. The design's `anthropic({ apiKey: key })` doesn't compile; after removing `ANTHROPIC_API_KEY` from env, the call silently 401s.

**Soft concerns**

- **S1 — `workflows-<orgID>` namespace may not exist at Connect time.** `git-service-wp-rbac.yaml` deliberately denies `namespaces create`. Writing the K8s Secret at Connect fails `NotFound` for any org without prior build/dispatch. Three options: lazy materialisation on dispatch (recommended), grant create RBAC (rejected upstream), or hard-couple Anthropic-Connect to GitHub-Connect (rejected — orthogonal flows).
- **S2 — Resolver pattern diverges from agent-manager's `SecretReference per org`.** Canonical OC pattern is `clients/secretmanagersvc/client.go:324-365`: one OC SecretReference per (org, secret) pair, ESO-materialised into the consumer namespace, mounted as env/files. ASDLC's resolver is more code, makes git-service a hard dependency of every agents-service call, and is a conscious divergence — but ESO refresh × N orgs scales worse, and agents-service isn't an OC Component today.
- **S3 — `secret-references/anthropic-credentials.yaml` deletion argument was ping-pong.** Land it cleanly as "agents-service no longer mounts a key; the SecretReference has no consumer".
- **S4 — Separate `org_anthropic_credentials` table is correct.** CHECK constraints on `org_credentials.kind` are GitHub-specific; relaxing them is riskier than a parallel table. Document the future-consolidation cost (already in §10).
- **S5 — `WPSecretCleaner` interface conflation.** Current shape `{ DeleteBuildSecret() }` would gain `DeleteAnthropicSecret()`, coupling GitHub-cred service to anthropic. Split into two interfaces; each service implements only what it owns.

**Recommendations**

- **R1** — Fold the OpenBao policy split (H1) into the design as an architectural enforcement of invariant 1.
- **R2** — Consider dropping the platform-fallback entirely; the user already chose to keep it.
- **R3** — Rename workflow parameter from `anthropic-secret-name` to `anthropic-secret-ref` to mirror `dockerfile-builder.yaml:30-34`.

**OK as-is** — Two-tier model itself, mirror of post-`d4d309f` per-org K8s Secret pattern, defence-in-depth, no in-flight cancellation, 5-min TTL + best-effort cache invalidate, per-org advisory lock, Connect validation chain. Argo doesn't re-mount Secrets — disconnect mid-run is provably safe.

### Iteration 1 — disposition (acceptances / pushbacks)

| Concern | Disposition | Where applied |
|---|---|---|
| **H1** OpenBao policy split | **Accept.** Original §7 fix was wrong. | §1 invariant 1 reworded; new §7.1 with concrete policy + role definitions. |
| **H2** SDK call shape | **Accept.** | New §7.2 with the `createAnthropic({apiKey}).chat(model)` pattern + file/line targets. |
| **S1** Namespace exists at Connect | **Accept (lazy materialisation).** | §4.3 rewritten; §6.1 no longer touches WP; §6.2 adds `apply-wp-secret` step on pre-flight; §7 git-service list adds `ApplyWPSecret`. |
| **S2** Resolver vs SecretReference per org | **Pushback.** Keep the HTTP resolver. | §3.3 expanded with explicit divergence rationale: agents-service isn't an OC Component, ESO refresh scales with N (not active orgs), git-service is already a dispatch-path hard dependency. |
| **S3** `secret-references/anthropic-credentials.yaml` ping-pong | **Accept.** | §4.4 rewritten with a single, clean argument; PR-description callout flagged for the platform team (MEDIUM blast-radius artifact). |
| **S4** Separate table | **Accept (no change).** | Already captured in §3.1 and §10. |
| **S5** Split `WPSecretCleaner` interface | **Accept.** | §7 git-service list adds the interface split (rename existing to `BuildSecretCleaner`; add `AnthropicSecretCleaner`). |
| **R1** OpenBao split as enforcement | **Accept.** | Folded into §7.1 — invariant 1 is now Vault-enforced, not convention. |
| **R2** Drop platform fallback | **Reject (deferred to iteration 0.5 decision).** User explicitly chose to keep platform fallback for non-coding-agent flows. Don't reopen. | §1 table preserved. |
| **R3** Parameter rename | **Accept.** | §5 + §7 manifests bullet now use `anthropic-secret-ref`; `CodingAgentParams.AnthropicSecretRef`. |

**Net design changes between iteration 1 and iteration 2:** §1 invariant 1 hardened (architectural, not convention). §3.3 documents conscious divergence from agent-manager. §4.3 switches to lazy WP Secret materialisation. §4.4 cleaned up. §5 parameter renamed. §6.1 no longer writes to WP; §6.2 adds the per-dispatch SSA step. §7 split into a code-change list + new §7.1 (Vault policy split) + §7.2 (SDK shape fix). §7 Go list adds `ApplyWPSecret`, splits the cleaner interface, calls out the typed `GetPlatformAnthropicKey` helper. No changes to §8 edge cases (already covered by lazy materialisation), §10 non-goals, or §11 references.

### Iteration 2 — reviewer feedback (platform-design-expert, condensed)

**Hard violation:**
- **HV1** — `GetPlatformAnthropicKey` is not parallel to `SeedPlatformValue`. `SeedPlatformValue` writes under `secret/asdlc/_platform/<key>`; the proposed helper reads `secret/apps/anthropic` — a different mount subtree (`apps/` vs `asdlc/_platform/`). Either migrate the platform key to `secret/asdlc/_platform/anthropic/key` (making the helper genuinely parallel), or add a clearly-named separate escape with explicit documentation that `apps/` is a legacy mount.

**Soft concerns:**
- **SC1** — §7.1 WP policy lists `secret/data/asdlc/+/anthropic/*` and `secret/data/asdlc/+/git/*`, but neither is read from the WP today (git-service runs in CP). Narrow further.
- **SC2** — Question on layer-0 vs layer-1: layer-1 is dispositive (last writer); layer-0 needs the same definition only to prevent transient broad access during chart upgrade.
- **SC3** — §4.3 "Why lazy" rationale leans on namespace existence, but `build_credentials_service.go:184-190` shows onboarding pre-provisions the WP namespace. The real reason is self-healing parity with `MintBuildToken`.
- **SC4** — Don't piggyback `ApplyAnthropicWPSecret` on `MintBuildToken` — keep them separate calls but parallelise both at the BFF via `errgroup`.

**Recommendations:** R1: HTTP-resolver pushback holds. R2: Typed helper acceptable *if HV1 is fixed*. R3: One sentence on orphan WP Secrets being inert. R4: No new hard violations.

### Iteration 2 — disposition (acceptances / pushbacks)

| Concern | Disposition | Where applied |
|---|---|---|
| **HV1** Mount-subtree mismatch | **Accept (option a)** — migrate the platform key to `secret/asdlc/_platform/anthropic/key`. | Then immediately superseded by iteration-3 pivot, which removes OpenBao from the flow entirely. |
| **SC1** Narrow WP policy paths | Same fate — superseded by iteration-3 (no policy split at all). |
| **SC2** Layer-0 vs layer-1 clarity | Same fate — superseded. |
| **SC3** §4.3 rationale rewrite | **Accept.** | §4.3 reworded to lean on `MintBuildToken` self-healing parity. |
| **SC4** Parallelise at BFF | **Accept.** | §6.2 mentions `errgroup` parallelisation. |
| **R3** Orphan WP Secret note | **Accept.** | One sentence appended at end of §6.2. |

### Iteration 3 — pivot (user correction)

**Trigger:** user pointed out that per-org secrets do not live in OpenBao — they live in Postgres `org_secrets` (AES-256-GCM, `dbStore` backend), per commit `2f26614` / PR #16 (`feat: implement credential store using Postgres for per-org secrets management`). All my prior iterations treated OpenBao as the storage backend for per-org keys, which was anachronistic.

**Verified by re-reading:**
- `git-service/pkg/credentials/db_store.go` — `NewDBStore(db, key)` returns an `OpenBaoStore` (interface name retained for backwards compatibility) backed by `org_secrets` Postgres table with per-row AES-256-GCM seal.
- `git-service/database/migrations/org_secrets.go` — table schema `(oc_org_id, key, value, updated_at)`.
- `git-service/cmd/git-service/main.go:111-121` — only `NewDBStore` is wired today.
- `git-service/config/config.go:46-49` — comment confirms "OpenBaoAddr / OpenBaoToken are retained until the _platform secret env-mount migration lands. Unused by the credential store."

**Net design changes between iteration 2 and iteration 3:**

- §1 invariants: rewritten — platform key lives in git-service env only; per-org key in `org_secrets`. Both are unreachable from WP by construction (no Vault path exists), not by policy.
- §3 mechanism table: replaced "OpenBao path" row with "Postgres `org_secrets` row". Added a note distinguishing the interface name (`OpenBaoStore`) from the live implementation (`dbStore`).
- §3.1: clarified that the new metadata table stores no secret bytes — the bytes live in `org_secrets`.
- §3.3: kept the HTTP-resolver divergence rationale; added a fourth bullet noting that the post-`2f26614` shift further weakens the "go agent-manager's way" alternative.
- §4.1: replaced the OpenBao section with Postgres `org_secrets` row + env-mounted platform key.
- §6.1 / §6.2 / §6.4 / §6.5: replaced all "OpenBao Get/Put/Delete" calls with `store.Get/Put/Delete(orgId, "anthropic/key", …)` against `dbStore`. Platform-key fallback reads `config.AnthropicPlatformKey` (env var).
- §7 Go list: added `config.AnthropicPlatformKey`; clarified `db_store.go` requires no change; clarified `openbao_store.go` requires no change.
- §7 Manifests list: dropped all OpenBao layer-0/layer-1 changes; added env-var entry on `app-factory-git-service.yaml`.
- §7.1: replaced the policy-split block with a short "no work needed; invariant upheld by absence" note. §7.2 (SDK shape) unchanged.

**What did *not* change:**

- The two-tier model itself (org key → coding-agent; org-then-platform → agents-service).
- Per-org K8s Secret materialised lazily on each dispatch into `workflows-<orgID>` (Build-credential parity).
- ClusterWorkflow parameter shape (`anthropic.secretRef`).
- Defence-in-depth: greyed button + 422 BFF pre-flight.
- Disconnect does not cancel in-flight runs.
- HTTP resolver from agents-service with 5-min LRU + best-effort cache invalidate.
- Per-org metadata table separate from `org_credentials`.
- Split `BuildSecretCleaner` / `AnthropicSecretCleaner` interfaces.
- `createAnthropic({ apiKey }).chat(model)` SDK call shape.

### Iteration 3 — reviewer feedback (platform-design-expert, condensed)

**Hard violations (narrative drift only — no structural issues):**

- **HV1** §2.3 Disconnect step list still names "delete the OpenBao path `secret/asdlc/{ocOrgId}/anthropic/key`" — that path doesn't exist after the pivot. §6.5 already had the right shape; §2.3 must match.
- **HV2** §8 edge-case table is entirely OpenBao-shaped (`openbao_unavailable`, `secret/apps/anthropic` fallback, rollback semantics over OpenBao). Rewrite to Postgres equivalents.

**Soft concerns:**

- **SC1** §4.3 / §6.2 prose still says "from OpenBao" three times. Mechanical s/from OpenBao/from `org_secrets`/g.
- **SC2** §7 entry for `openbao_store.go` should be one short sentence — the `_platform/*` rationale is irrelevant here.
- **SC3** Local-k3d env-overlay for `ANTHROPIC_PLATFORM_KEY` is fine. **Cloud is not** — must come through a SecretReference + ESO. Flag the local-vs-cloud split.
- **SC4** Renaming `WPSecretCleaner → BuildSecretCleaner` touches three call sites in `credential_service.go`. Mention it as a mechanical edit.

**Recommendations:** R1–R4 spell out the cleanups above. R4 specifically: add cloud-deployment to non-goals so the PR scope is unambiguous.

**Answers to specific questions:**

1. SSA pattern correctly mirrors `MintBuildToken` → `applyBuildSecret` exactly (same `client.Apply` + `ForceOwnership` + `FieldOwner` on build_credentials_service.go:212-217). The §7 plan to hoist `applySecret(ns, name, key, value)` is the right de-duplication.
2. Lingering OpenBao mentions: only in the §2.3 / §8 narrative — the §7.1 "no work needed" note, §11 references, and historical iteration sections are correct.
3. Platform-key as env var: acceptable for local-k3d (mechanism parity with today's agents-service injection). **Not acceptable as-written for cloud** — needs SecretReference + ESO.
4. Cleaner interface split: clean; the rename is mechanical.
5. "Same pattern as GitHub secrets" satisfied — same `org_secrets` table, same `dbStore`, same AES key, same projection-table-separate-from-bytes shape, same SSA-into-WP pattern.
6. No new hard violations from the pivot — only narrative drift in §2.3 and §8.

### Iteration 3 — disposition (acceptances / pushbacks)

| Concern | Disposition | Where applied |
|---|---|---|
| **HV1** §2.3 disconnect step still names OpenBao | **Accept.** | §2.3 rewritten — deletes `org_secrets` row + `org_anthropic_credentials` row + K8s Secret. |
| **HV2** §8 edge-case table OpenBao-shaped | **Accept.** | §8 rewritten in terms of Postgres + tx semantics. Added `CREDENTIAL_ENCRYPTION_KEY` rotation row for symmetry with the existing GitHub-PAT gap. |
| **SC1** Prose drift "from OpenBao" | **Accept.** | §4.3 / §6.2 / §3.2 rewritten to refer to `org_secrets`. |
| **SC2** Trim `openbao_store.go` note | **Accept.** | §7 entry collapsed to one line. |
| **SC3** Local-vs-cloud platform-key | **Accept.** | §4.1 platform-key block expanded with explicit local-vs-cloud breakdown. |
| **SC4** Mark `WPSecretCleaner` rename mechanical | **Accept.** | §7 entry annotated with line numbers and rename of `WithWPCleaner → WithBuildSecretCleaner` for symmetry. |
| **R4** Cloud out of scope | **Accept.** | §10 non-goals adds "Cloud deployment of `ANTHROPIC_PLATFORM_KEY` — follow-up PR via SecretReference + ESO". |

**Net design changes between iteration 3 and the final form:** all narrative cleanup; no structural change. Affected sections: §2.3, §3.2, §4.1, §4.3, §6.2, §7 (Go list `openbao_store.go` entry, cleaner-rename annotation), §8 (full edge-case table rewrite), §10 (two new non-goals).

### Convergence

Iteration 3 closes the loop on structural design. The remaining work is the implementation itself, broken down per §7's file list. No further reviewer round needed unless implementation surfaces a new question — most likely candidates are (a) the `errgroup` parallelisation pattern in `DispatchService`, (b) how to authenticate the agents-service → git-service `effective-key` call (probably reuses the existing service-JWT-via-auth-provider pattern from `clients/agents/client.go:128-138`), and (c) whether the cloud-side `secret-references/anthropic-platform-key.yaml` should land in this PR or a sibling.

## 10. Non-goals / explicit deferrals

- Background revalidation tick (no 24h Anthropic-key validator). Add later if needed.
- Per-project keys — one key per org.
- Generic provider-aware metadata table — defer until a third provider key arrives (Gemini / OpenAI). The `org_secrets` table itself is already generic (KV under `(oc_org_id, key)`); only the metadata projection (prefix/last4/status) is per-provider.
- Cancel in-flight WorkflowRuns on disconnect.
- Audit log of key reads.
- Self-service rotation reminder UI.
- **Cloud (`controlplane/{aws,onprem}`) deployment of `ANTHROPIC_PLATFORM_KEY`.** Local-k3d uses env-overlay substitution from `deployments-v2/.env`. Cloud will require a SecretReference + ESO into git-service's CP namespace (mounted via `envFrom.secretRef`). Land as a follow-up PR; this design intentionally stops at the local-dev shape so the developer-facing changes can ship first.
- `CREDENTIAL_ENCRYPTION_KEY` rotation. Same gap exists today for GitHub PATs; not new to this design.

## 11. References

- Deleted prior design: previously at `docs/design/anthropic-key-per-org.md`.
- Credential-store-on-Postgres migration: commit `2f26614` / PR #16 — `feat: implement credential store using Postgres for per-org secrets management`.
- Postgres-backed `OpenBaoStore`: `git-service/pkg/credentials/db_store.go`, migration `git-service/database/migrations/org_secrets.go`.
- GitHub-credentials precedent (metadata table + connect flow): `git-service/models/org_credential.go`, `git-service/services/credential_service.go`, `git-service/database/migrations/phase2_pra.go`.
- Build-credential delivery pattern (per-org K8s Secret SSA into `workflows-<orgID>`): commit `d4d309f` ("Per-org build credential Secret: drop SecretReference hop"); see `git-service/services/build_credentials_service.go:170-248` for `applyBuildSecret` + `DeleteBuildSecret`.
- ClusterWorkflow that hosts the coding-agent today: `deployments-v2/wso2cloud-deployment/wso2cloud-local/domains/platform/cluster-shared/cluster-workflows/app-factory-coding-agent.yaml` (lines 100–101, 157–161, 179–200 are the anthropic-secret call sites to edit).
- agents-service Anthropic call sites: `agents/src/shared/create-agent.ts:59`, `agents/src/server/routes/{architect,document-generation,tech-lead}.ts`.
- `createAnthropic` SDK type: `agents/node_modules/@ai-sdk/anthropic/dist/index.d.ts:1084` (`declare function createAnthropic(options?: AnthropicProviderSettings): AnthropicProvider`).
- agent-manager OpenBao + SecretReference precedent (the path *not* taken; §3.3 divergence): `agent-manager-service/clients/secretmanagersvc/client.go:324-365`.
