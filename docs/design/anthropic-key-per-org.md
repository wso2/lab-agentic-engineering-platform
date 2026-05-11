# Per-org Anthropic API key

**Status:** design — UX locked, mechanism locked, **ownership open** (see §8). Implementation deferred.

## 1. Problem

The Anthropic API key is platform-wide today: one key in OpenBao at `secret/apps/anthropic`, used by every org. We want each org to be able to provide its own key, with two consumption rules:

| Consumer | Org has key | Org has no key |
|---|---|---|
| `agents-service` (spec / design / task-gen / wireframe) | use org key | **silent** fallback to platform key |
| `app-factory-coding-agent` runner (per-WorkflowRun pod) | use org key | **fail** at dispatch with a clear error |

A platform-level key always exists — it is never removed, only superseded per-org.

## 2. UX (locked with the user)

### 2.1 Settings page

New page at `/organizations/{org}/settings/anthropic`, mirroring the existing `OrgGitHubSettings` page. Same JWT + orgHandle authz boundary as GitHub Settings; no new RBAC.

Three states:

- **Not configured** — single password input + Save & Validate button. Microcopy notes the coding agent is disabled.
- **Connected** — green dot, "Last validated N min ago", key shown as `sk-ant-ap03-A1B2…XyZw` (prefix + last 4). Buttons: Replace key, Disconnect.
- **Validation failed** — red badge, "Validation failed (401 Unauthorized)". Spec/design will continue working via platform fallback; coding agent is blocked. Buttons: Replace key, Disconnect.

### 2.2 Key preview

Display prefix + last 4 chars after save (`sk-ant-ap03-A1B2…XyZw`). Standard for API-key UIs. Helps the user identify which key is connected without exposing it.

### 2.3 Spec / design fallback

**Silent.** No UI hint anywhere if the platform key is being used. Telling the user "your spec was generated with the platform key" creates anxiety without an actionable next step. Only the coding-agent path surfaces the missing-key error.

### 2.4 Coding-agent gating (defence in depth)

- Project page: "Implement with Remote Agent" button is **disabled** with a tooltip ("Configure Anthropic API key in Org Settings to dispatch the remote agent") when the org has no key.
- BFF: even if the user bypasses (stale browser state), `POST /tasks/{id}/dispatch` returns:

```json
{ "error": {
    "code": "anthropic_key_required",
    "message": "An Anthropic API key must be configured at the org level before the coding agent can run.",
    "settingsUrl": "/organizations/{org}/settings/anthropic"
} }
```

This mirrors the existing `app_bind_not_configured` error code shape used by GitHub.

### 2.5 Disconnect

`Disconnect` deletes the OpenBao secret + the SecretReference CR + the DB row. After disconnect:
- Spec/design generation continues (silent platform fallback)
- Coding-agent dispatch is again blocked (button disabled, BFF returns 422)
- In-flight WorkflowRuns keep running with their already-mounted Secret

## 3. Mechanism — decisions reached with the OC + Cloud experts

| # | Decision | Rationale |
|---|---|---|
| Storage path | `secret/asdlc/{ocOrgId}/anthropic/key` (KV v2, field `key`) | Mirrors existing `secret/asdlc/{ocOrgId}/github/pat`. WSO2 Cloud has no prescribed schema; consistency with the GitHub PAT path beats introducing a new shape. |
| Runner secret mechanism | `externalRefs[].kind: SecretReference` (CR-name parameter), **not** raw OpenBao path | Canonical OC pattern (`samples/getting-started/ci-workflows/dockerfile-builder.yaml:188-219`). OC controller enforces `Kind == SecretReference` (`internal/controller/workflowrun/externalref.go:84-87`). Rides `${workflowplane.secretStore}` for plane portability. App Factory already has `EnsureSecretReference()` plumbing for the build flow (`asdlc-service/clients/openchoreo/secretref_client.go:109`). |
| SecretReference lifecycle | **Persistent**, one CR per `(org, anthropic)` | Matches WSO2 Cloud's CP convention (e.g. `agent-manager-service-credentials.yaml`). Created on first key-set, deleted on disconnect. Not per-WorkflowRun. |
| Agents-service resolution | Per-request HTTP call to a resolver, with 30s LRU cache | OC has no opinion on long-lived Workloads resolving per-tenant secrets. WSO2 Cloud principle is "minimise OpenBao readers". |
| Validation | App-level probe to `https://api.anthropic.com/v1/models` | OC has no SecretReference-content admission webhook by design. Mirrors `git-service/services/validator_probes.go` for GitHub PATs. |

## 4. Data model

### 4.1 OpenBao

```
secret/asdlc/{ocOrgId}/anthropic/key      # KV v2; field "key"
secret/apps/anthropic                      # platform fallback; unchanged
```

### 4.2 Postgres — new table

```sql
CREATE TABLE org_anthropic_credentials (
  oc_org_id          text PRIMARY KEY,
  secret_ref_name    text NOT NULL,         -- "app-factory-anthropic-{ocOrgId}"
  key_prefix         text NOT NULL,         -- "sk-ant-ap03-A1B2"
  key_last4          text NOT NULL,         -- "XyZw"
  status             text NOT NULL,         -- 'active' | 'invalid'
  connected_at       timestamptz NOT NULL,
  last_validated_at  timestamptz,
  validation_error   text
);
```

A new table (not a discriminator on the GitHub-shaped `org_credentials`) — schemas don't overlap, and YAGNI on a generic `org_secrets` until a second non-GitHub provider arrives. Which Postgres this lives in depends on §8 (the open ownership question).

### 4.3 OpenChoreo control-plane CR (persistent, one per org)

```yaml
apiVersion: openchoreo.dev/v1alpha1
kind: SecretReference
metadata:
  name: app-factory-anthropic-{ocOrgId}
  namespace: <org CP namespace>
spec:
  data:
    - secretKey: ANTHROPIC_API_KEY
      remoteRef:
        key: secret/data/asdlc/{ocOrgId}/anthropic/key
        property: key
```

Created via the existing `EnsureSecretReference()` helper.

## 5. Flows

### 5.1 Save key (Connect)

```
console → BFF POST /api/v1/organizations/{org}/anthropic { apiKey }
  BFF → resolver POST /credentials/orgs/{org}/anthropic { apiKey }
    resolver: probe POST https://api.anthropic.com/v1/models  (401 → 400 invalid_anthropic_key)
    resolver: write secret/asdlc/{org}/anthropic/key
    resolver: upsert org_anthropic_credentials (prefix, last4, last_validated_at, status='active')
  BFF: EnsureSecretReference("app-factory-anthropic-{org}", "secret/data/asdlc/{org}/anthropic/key")
  BFF → 200 { status, keyPrefix, keyLast4, lastValidatedAt }
```

`resolver` here is whichever service ends up owning the OpenBao surface — see §8.

### 5.2 Dispatch coding agent (org has key)

```
console: button enabled  (projection.anthropic.connected = true)
console → BFF POST /api/v1/tasks/{id}/dispatch
  BFF: read projection; status must be 'active'
  BFF → OC POST /api/v1/.../workflowruns
        spec.parameters.anthropic = { secretRef: "app-factory-anthropic-{org}" }
  OC controller: resolve externalRefs → render per-run ExternalSecret in WorkflowPlane ns
  ESO: sync OpenBao → k8s Secret → mount into runner pod env
```

### 5.3 Dispatch coding agent (no key) — defence in depth

```
console: button disabled, tooltip "Configure Anthropic API key in Org Settings…"
(if user bypasses via stale state)
  BFF → 422 { error: { code: "anthropic_key_required", … } }
  console: render modal with "Configure key" CTA
```

### 5.4 Spec/design (agents-service, silent fallback)

```
BFF → agents-service POST /spec/generate (X-Org-Id: {ocOrgId})
  agents-service: cache lookup (ocOrgId)
  on miss → resolver GET /credentials/orgs/{ocOrgId}/anthropic/effective-key
    resolver:
      if org row exists AND status='active' → return secret/asdlc/{org}/anthropic/key
      else                                   → return secret/apps/anthropic   (platform fallback)
  agents-service: cache 30s; hand to AI SDK anthropic({ apiKey })
```

### 5.5 Disconnect

```
console → BFF DELETE /api/v1/organizations/{org}/anthropic
  BFF → resolver DELETE /credentials/orgs/{org}/anthropic
    resolver: delete OpenBao path + DB row
  BFF: delete SecretReference CR app-factory-anthropic-{org}
  BFF → 204
```

In-flight WorkflowRuns keep running — their Secret is already materialised. Future dispatches are blocked at BFF (and the OC controller would reject anyway since the SecretReference no longer resolves).

## 6. Coding-agent ClusterWorkflow change

`deployments-v2/wso2cloud-deployment/wso2cloud-local/domains/platform/cluster-shared/cluster-workflows/app-factory-coding-agent.yaml` currently hard-codes:

```yaml
# lines ~155–199 today
- name: ANTHROPIC_API_KEY
  valueFrom: { secretKeyRef: { name: '{{workflow.parameters.anthropic-secret}}', key: ANTHROPIC_API_KEY } }
# + ExternalSecret block pointing at secret/apps/anthropic
```

Replace with the canonical `externalRefs` shape from `dockerfile-builder.yaml:188-219`:

```yaml
parameters:
  openAPIV3Schema:
    properties:
      anthropic:
        type: object
        required: [secretRef]
        properties:
          secretRef: { type: string }   # SecretReference CR name
externalRefs:
  - id: anthropic-secret-ref
    apiVersion: openchoreo.dev/v1alpha1
    kind: SecretReference
    name: ${parameters.anthropic.secretRef}
resources:
  - id: anthropic-secret
    template:
      apiVersion: external-secrets.io/v1
      kind: ExternalSecret
      metadata:
        name: '{{workflow.parameters.run-name}}-anthropic'
      spec:
        secretStoreRef: { kind: ClusterSecretStore, name: ${workflowplane.secretStore} }
        data: ${externalRefs['anthropic-secret-ref'].spec.data.map(s, {…})}
```

## 7. Files to change

**Go:**
- `<resolver>/models/org_anthropic_credential.go` — new
- `<resolver>/database/migrations/` — new migration
- `<resolver>/repositories/anthropic_credential_repository.go` — new
- `<resolver>/pkg/credentials/openbao_store.go` — extend with anthropic methods
- `<resolver>/services/anthropic_credential_service.go` — new
- `<resolver>/services/validator_probes.go` — extend with `ValidateAnthropicKey`
- `<resolver>/api/credentials_routes.go` — add anthropic + effective-key routes
- `asdlc-service/clients/<resolver>/anthropic_credential_client.go` — new
- `asdlc-service/api/org_anthropic_routes.go` — new
- `asdlc-service/controllers/org_anthropic_controller.go` — new
- `asdlc-service/services/workflowrun_service.go` — pre-flight check + parameter wiring

**TypeScript:**
- `console/src/pages/OrgAnthropicSettings.tsx` — new
- `console/src/components/ConnectAnthropicForm.tsx` — new
- `console/src/api/anthropic.ts` — new
- `console/src/pages/<ProjectPage>.tsx` — disable button + tooltip + banner
- `agents/src/shared/anthropic-key-resolver.ts` — new (HTTP client + cache)
- `agents/src/shared/create-agent.ts` — wire resolver into provider construction

**Manifests / kustomize:**
- `deployments-v2/wso2cloud-deployment/wso2cloud-local/domains/platform/cluster-shared/cluster-workflows/app-factory-coding-agent.yaml` — replace hard-coded ExternalSecret with `externalRefs[]` + `resources[]` shape (see §6)
- `deployments-v2/manifests/env-overlays/app-factory-agents-service.yaml` — **remove** `ANTHROPIC_API_KEY`
- `deployments-v2/manifests/env-overlays/<resolver>.yaml` — **add** `ANTHROPIC_PLATFORM_KEY` mounted via ExternalSecret pointing at `secret/apps/anthropic`
- `deployments-v2/wso2cloud-deployment/wso2cloud-local/init/layer-0/tools/openbao.yaml` — verify `openchoreo-secret-reader-role` `bound_service_account_namespaces` covers `openchoreo-workflow-plane` (cloud expert flagged: today it's `dp*` only)

`<resolver>` is the service that ends up owning the OpenBao surface — see §8.

## 8. OPEN QUESTION — ownership

Both reviewers (`oc-design-expert`, `wso2cloud-expert`) recommended **extending git-service** because it's already the only OpenBao reader/writer and concentrating Vault access in one service is a real security property. The user's concern is the cohesion / naming problem: git-service is named for git ops, and adding non-Git credential storage makes it less honest as the codebase grows.

The four candidates considered:

### A. Extend git-service (experts' recommendation)
- **Cost:** smallest. ~1 day.
- **Wins:** single OpenBao reader/writer; reuses existing service auth, deployment, DB, observability.
- **Loses:** name lies harder. Each new provider key (OpenAI, Gemini, etc.) lands in git-service too. Cohesion gets worse over time.

### B. Rename git-service → `credentials-service`
- **Cost:** medium. ~2-3 days of mechanical renaming. No new services.
- **Wins:** name matches reality.
- **Loses:** the binary still does git ops. Swaps one cohesion problem (name lies) for another (`credentials-service` does git ops). To do this *cleanly* you'd also split git ops out, which doubles the cost.

### C. New dedicated `secret-resolver` service
- git-service keeps GitHub creds (tightly coupled with git ops via token-refresh, repo-scoped tokens, App installation tokens). New tiny service owns AI-provider creds (Anthropic now, OpenAI later).
- **Cost:** medium-high. ~4-5 days. New Workload manifest, env-overlay, image, DB migration, healthcheck, deployment story.
- **Wins:** crisp separation by domain. Future-proof for any provider key. Aligns with WSO2 Cloud's `secret-manager-api` pattern in agent-manager.
- **Loses:** another long-lived service to operate. Two OpenBao readers (git-service + secret-resolver) instead of one — but each on a *narrow* path scope (`secret/asdlc/+/github/*` vs `secret/asdlc/+/anthropic/*`), so security blast radius does not actually grow. Opens a second auth boundary, more YAML.

### D. BFF + agents-service own it directly (rejected)
- BFF gets OpenBao **write** scope; agents-service gets **read** scope; git-service stays GitHub-only.
- **Loses:** three OpenBao clients instead of one. BFF is the most-exposed service and would gain the most powerful credential. Architecturally worst from the security perspective.

### Recommendation framing for revisit

- If optimising for **shipping fast**: A.
- If optimising for **right long-term shape** + a second provider key is expected within a quarter or two: C — and treat that as the moment to draw the line: GitHub creds stay glued to git ops in git-service; everything else (AI providers, third-party API keys) lands in `secret-resolver`.
- B is not recommended (cost-to-value bad). D is not recommended (security regression).

User to decide on revisit.

## 9. Non-goals / explicit deferrals

- No background revalidation tick (no equivalent of GitHub PAT validator). Add later via `validator_probes.go` ticker pattern if needed.
- No "key owner" identity display — Anthropic doesn't expose a `whoami`. Prefix + last-4 only.
- No per-project keys — one key per org.
- Disconnect does not cancel in-flight runs.
- Generic `org_secrets` table deferred until a second non-GitHub provider lands.

## 10. References

- `samples/getting-started/ci-workflows/dockerfile-builder.yaml:188-219` — canonical `externalRefs` + ExternalSecret pattern in OC.
- `internal/controller/workflowrun/externalref.go:84-87` — OC controller enforcing `Kind == SecretReference`.
- `api/v1alpha1/workflow_types.go:117`, `api/v1alpha1/workflowrun_types.go:54` — Workflow / WorkflowRun parameter shape.
- `asdlc-service/clients/openchoreo/secretref_client.go:109` — existing `EnsureSecretReference()`.
- `git-service/api/credentials_routes.go`, `git-service/models/org_credential.go`, `git-service/pkg/credentials/openbao_store.go` — existing per-org GitHub credential pattern (the precedent we mirror for path/auth/projection shape).
- `deployments-v2/wso2cloud-deployment/wso2cloud-local/domains/platform/cluster-shared/cluster-workflows/app-factory-coding-agent.yaml:155-199` — current hard-coded ExternalSecret block to replace.
- `deployments-v2/wso2cloud-deployment/wso2cloud-local/init/layer-0/tools/openbao.yaml:62-65` — reader role bound namespaces, may need `openchoreo-workflow-plane` extension.
- `controlplane/common/.../secret-references/agent-manager-service-credentials.yaml` (in `wso2cloud-deployment` main branch) — WSO2 Cloud convention for persistent SecretReference CRs.
