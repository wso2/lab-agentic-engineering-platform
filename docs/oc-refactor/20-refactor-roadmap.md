# Refactor Roadmap — Phase 0–1 (M1, self-hosted)

> **Scope: M1 only — Phase 0 (structural) + Phase 1 (secret-plane + go-live).** Everything beyond M1
> (LLM gateway, WSO2 Cloud packaging, `secret-manager-api`, lifecycle) is in **`future-phases.md`**.
> Gaps G1–G3 are addressed here; G4–G10 are future. Touch-points from `analysis/01`–`06`; AM refs
> (`/Users/wso2/repos/agent-manager-analysis/`) inline.

**Clean refactor — no backward compatibility.** Principles:

1. **Structural changes first.** Consolidate topology + code structure before changing behavior.
2. **Each phase is a testable milestone.** Batch several changes, then verify at once — every phase
   ends with a **Test gate** runnable against the local k3d + docker-compose stack (`deployments/`).
3. **No half-states for compat.** When a phase replaces something, the old path is deleted with it.

**M1 = Phase 0 + Phase 1 = the self-hosted go-live.** Phase 1 is widened so "0 and 1 done" is
shippable (it includes the deploy-status fix WS1.5 and hardening WS1.6). **Test gate 1 is the go-live
gate.** Deferred fast-follow (LLM gateway, WSO2 Cloud, multi-env, App mode) → `future-phases.md`.

---

## Cross-cutting: error logging for secret/network failures (required)

The riskiest part of the dev rollout is that a **secret isn't injected** or a **dependency isn't
reachable** — and today those fail silently or with opaque errors. **Every new secret/network path in
Phase 0–1 must fail loud and specific**, so a dev deploy is diagnosable without kubectl-spelunking:

- **Secret resolution:** wrap `secretmanagersvc` errors with context (org, KV path/`SecretLocation`,
  provider) — distinguish *backend unreachable* (OpenBao down / wrong URL / auth rejected) from
  *secret not found* (never written) from *not yet materialized* (ESO hasn't synced the
  `SecretReference`/`GitSecret` into a K8s Secret).
- **Pod-side:** when a workflow/runner pod can't find its `secretKeyRef` target Secret or env is
  empty, fail fast with a message naming the missing Secret + the SecretReference it should come from
  (don't start and silently 401 to GitHub/Anthropic).
- **Dependency reachability:** clear, retried-with-backoff, logged errors for OC API, OpenBao,
  Thunder, and GitHub — each naming the target URL and the failing operation.
- **Surface upward:** secret/dependency failures during dispatch/deploy must land on the **task /
  deployment status** (not just logs), so a stuck task shows *why* (e.g. "secret `…` not materialized").

This is explicitly checked in **Test gate 1** (induce a missing/uninjected secret and an unreachable
backend; confirm both produce a specific, actionable error in logs *and* task status).

---

## Phase 0 — Structural consolidation (DO FIRST)

All structural moves, batched. Behavior-preserving where possible; the point is to land the target
topology and code structure (`10` §0) so every later phase is built on it.

### WS0.1 — Fold `git-service` into `asdlc-service` (keep the existing name)
- **No platform rename** — the product name isn't finalized. The merged service stays `asdlc-service`;
  keep existing `asdlc-*` identifiers, the `console`/`agents`/`remote-worker` names, and current Go
  package paths. The *only* removal is `git-service` as a separate deployable.
- One Go module/deployable (= AM's `amp-api`). Target package layout (`10` §0.2): `clients/openchoreo`,
  `clients/secretmanagersvc`, `clients/github`, `services`, `controllers`, `models`, `repositories`.
- Move git-service's GitHub code into `clients/github/` and its App-token minter alongside. One
  Postgres, one config, one HTTP server. The `effective-key` endpoint becomes in-process-served by
  the merged service (still used by the TS `agents` until the gateway lands post-M1).
- **AM ref:** AM is one Go control-plane service + TS console + ephemeral pods.

### WS0.2 — Port `secretmanagersvc` interfaces + the `openbao` provider + the seam (NOT both providers)
- Copy `provider.go` (`Provider`, `SecretsClient`, `SecretReferenceManager`), `registry.go`,
  `client.go` (`SecretManagementClient`), `types.go` from the **OSS** `wso2/agent-manager`
  (`/Users/wso2/repos/agent-manager/agent-manager-service/clients/secretmanagersvc/`). Build **only**
  the `openbao` provider (raw Vault + token, local). **The `secretmanagerapi` provider is deferred to
  Phase 3** — it lives in the **enterprise** `agent-platform` repo (`.../secrets/`) and is ported
  there, not built net-new (validation BLOCKER-1, resolved).
- Keep the *seam*: config `SECRET_MANAGER_PROVIDER` (default `openbao`) / `SECRET_MANAGER_API_URL` /
  `OPENBAO_*`; `ProvideSecretManagementClient` with the `provider.(SecretReferenceManager)` →
  `ocClient=nil` branch (AM `wire_gen.go:330-334`). This is the real "pluggable" property and makes
  Phase 3 *additive* (register one provider), not structural.
- **Fork `SecretLocation` to a labs-native `{org, project, task, entity}` shape** (don't overload AM's
  `AgentName`); confirm `Namespace == OC org ID` (validation MAJOR-4).
- Wire the `openbao` provider at the local OpenBao the dev stack already runs (`analysis/05`).
  Additive — not yet the sole secret path (cutover is Phase 1).

### WS0.3 — Add `SecretReference` / `GitSecret` typed wrappers to the OC client
- The *generated* client already exposes both (VERIFIED, `analysis/VERIFY-secrets.md`); add the thin
  typed wrappers + mocks mirroring AM `secret_references.go` / `git_secrets.go`. (Used in Phase 1.)

### WS0.4 — Migrate control-plane git to the GitHub Git Data API; delete shell-git
- Repo create / issues / PRs already use REST. Move **spec commits** to the Git Data API
  (blobs/trees/commits/refs) — the multi-file atomic commit engine **already exists**
  (`git-service/services/save_via_api.go:105-189`); only the working-tree dependency is removed.
  **Delete** shell-git clone/commit/push + the workspace volume + the `git` binary from the
  long-lived image. Filesystem git now lives only in the ephemeral runner pod. (`10` §0.)

### WS0.5 — Port + extend the import-fence test (the boundary enforcement)
- Carry forward `git-service/pkg/credentials/import_fence_test.go` (AST walk) into the merged module
  and **extend it to every MUST-NOT edge in `10` §0.3**: Vault SDK only in the openbao provider; OC
  client out of `clients/github`; secret-backend SDK out of `services/*`/`controllers`/`repositories`.
  Adopt `internal/` placement. (validation BLOCKER-2 — the merge's whole purpose fails without this.)

**✅ Test gate 0 (structural — nothing regressed + abstraction + boundaries enforced):**
- `asdlc-service` builds and boots as a single service; no separate `git-service`.
- **Full local E2E unchanged:** prompt → repo created → requirements/architecture/tasks committed
  (now via Git Data API) → GitHub issues → coding-agent pod opens a PR → merge → build → deploy.
- `secretmanagersvc` integration test writes+reads a secret to the local OpenBao via the `openbao`
  provider.
- The runtime image contains **no `git` binary** and mounts no workspace volume.
- **The import-fence tests pass for every MUST-NOT edge** (`10` §0.3). The merge is not done until
  the fences are green.

---

## Phase 1 — Secret-plane cutover + go-live readiness (= the M1 ship)

Route every tenant/workload secret through the Phase-0 `SecretManagementClient` and **delete the
legacy plaintext paths** (AM `08`), then add the two pulled-in go-live items (WS1.5 deploy status,
WS1.6 hardening). **Completing this phase = the self-hosted go-live.**

### WS1.1 — Anthropic key → coding-agent pod via `SecretReference` + ESO
- BFF calls `secretClient.Create(SecretLocation{org,env,entity:anthropic}, value)`; on `openbao` it
  also creates the OC `SecretReference` in the WP-bound namespace; ESO materializes the Secret; the
  runner consumes it via the existing `secretKeyRef` (`app-factory-coding-agent.yaml:176-180`).
  **Delete** `anthropic_credential_service.go` SSA-apply. (AM `08` Class 1.)

### WS1.2 — GitHub build credential → `GitSecret`
- Create an OC **`GitSecret`** bound to `ClusterWorkflowPlane/default`; **delete**
  `build_credentials_service.go:160-203` SSA-apply. (AM `06` `git_secrets.go:30`.)

### WS1.3 — ESO `ClusterSecretStore` + reader/writer roles (incl. per-namespace scoping)
- Reader role bound to `workflows-<org>` (and DP) `default` SAs; writer role for the platform ESO SA.
  **Pull the per-namespace reader-role binding in here** (not Phase 3): Phase 1 materializes per-org
  secrets, so cross-org isolation must be enforced *now*, not asserted (validation MINOR-7). (AM `08`.)

### WS1.4 — Delete the Postgres-AES store; migrate data; fix the bearer-token path
- Remove git-service `dbStore` / `org_secrets` *value* storage (keep non-secret projections); all
  values now resolve via `SecretManagementClient`. Kills G1 + G2.
- **Data migration — DECIDED: none.** Clean refactor: **no value preservation; operators re-enter org
  secrets** after cutover. Schema change = drop the `org_secrets.value` column. No migration job.
- **Runner auth — adopt AMP's eval-job pattern exactly (validation MAJOR-5; aligned).** Drop the
  bespoke `ASDLC_BEARER` minted-JWT param entirely. Mirror what AMP does for its ephemeral eval jobs,
  verified in `wso2-amp-evaluation-extension/.../cluster-workflow-monitor-evaluation.yaml`:
  1. Provision a **per-org OAuth client** (publisher-style) in Thunder and store its **client-secret**
     via the secret abstraction (SecretReference/ESO — Class 1; stable, not minted, so no ESO refresh
     race).
  2. The coding-agent `WorkflowRun` template emits a **per-run ExternalSecret** named
     `${workflowRunName}-credentials` materializing that client-secret into a per-run K8s Secret.
  3. The runner reads `client-secret` via `secretKeyRef` and does **OAuth2 `client_credentials`**
     against Thunder to get a short-lived token, then calls the BFF.
  4. The BFF **authorizes callbacks by run/task ID** (the org-scoped token + the run's task binding),
     exactly as AMP scopes eval score-publishing by `monitorId/runId`.
  - This unifies on the one secret mechanism (ESO/SecretReference + a per-run ExternalSecret), is
    byte-for-byte AMP's approach, and removes labs' divergent "control-plane mints + injects a token"
    path. (labs' existing `TaskTokenManager` minting becomes unnecessary for the runner; if a
    task-scoped JWT is still wanted, layer AMP's `agent_token_manager` style token *exchange* on top —
    runner presents the publisher token, BFF issues a scoped one.)
- *Interim:* the in-process `effective-key` endpoint now reads the org key from OpenBao and is
  JWT-authed; it stays only until Phase 2 moves agents to the gateway.

### WS1.5 — Gate `deployed` on ReleaseBinding Ready (G3) — *pulled in for go-live*
- `build_watcher.go:213` + `task_state.go:110`: set `deployed` only when
  `ReleaseBinding.Status.Conditions[Ready]` is true, not on build-WorkflowRun success. Without this
  the platform reports success while an app crash-loops. (AM `01`, `deployments.go:313`.)

### WS1.6 — Self-hosted production hardening — *pulled in for go-live*
- Seal OpenBao (no dev mode, **no `root` token** — the local stack runs unsealed with token `root`),
  give `asdlc-service` a **scoped ServiceAccount** (drop the host kubeconfig + cluster-admin
  creds), and put **real ingress** in front of the GitHub webhook receiver (drop the smee.io relay).
- **Webhook HMAC verification (validation MINOR-7):** real ingress is not enough — verify the GitHub
  webhook HMAC signature on every payload (secret sourced via `secretmanagersvc`, not plaintext
  JSONB). Real ingress without verified-source HMAC is open exposure.
- *Scope note:* this is the single-cluster self-host slice of WS3.4; cross-cluster plane separation
  stays in Phase 3 (WSO2 Cloud).

**✅ Test gate 1 — this is the GO-LIVE gate (see "Release milestones"):**
- Grep/CI check: **zero** SSA `Secret` applies of tenant values; **no** DB-stored secret values;
  OC CRDs carry only KV-path references.
- The coding-agent pod boots with `ANTHROPIC_API_KEY` from an **ESO-materialized** Secret derived
  from a `SecretReference` (not an SSA-pushed Secret); the build pulls creds from a `GitSecret`.
- A deliberately crash-looping deploy does **not** report `deployed`; a healthy one does.
- OpenBao is sealed (no `root` token), the api runs under a scoped SA, webhooks arrive via real
  ingress (no smee).
- **Failure modes log loud + specific (see "Cross-cutting: error logging"):** inject a *missing*
  secret and an *unreachable* OpenBao/OC-API; confirm each produces an actionable, contextual error
  in logs **and** on the task/deployment status — not a silent hang or opaque 500.
- Full local E2E passes end-to-end on the `openbao` provider against a self-hosted cluster.

---

## Beyond M1

Phases 2 (LLM governance), 3 (WSO2 Cloud packaging + `secret-manager-api`), and 4 (lifecycle) are in
**`future-phases.md`**.

> **One M1 note that future-proofs against Phase 3:** at M1 the control plane reads the per-org GitHub
> PAT from the (ReadWrite) local OpenBao for its GitHub calls — fine. On WSO2 Cloud the backend is
> WriteOnly and the CP can't read tenant secrets back, so control-plane GitHub auth must move to
> GitHub App tokens (App key = a platform secret). That's a Phase-3 concern (see `future-phases.md`);
> it does **not** affect M1.

---

## Dependency graph (M1 build order)

```
Phase 0 (STRUCTURAL, first — one testable batch):
  WS0.1 merge ─► WS0.2 secretmanagersvc(iface + openbao + seam) ─► WS0.3 OC ref wrappers
            └─► WS0.4 Git Data API (delete shell-git)    WS0.5 import-fence (boundary enforcement)
  └─ Test gate 0: app works after consolidation + abstraction tests pass + IMPORT FENCES GREEN

Phase 1 = M1 GO-LIVE (needs 0):
  WS1.1 + WS1.2 + WS1.3 (incl per-ns reader role) ─► WS1.4 delete legacy + migrate + fix bearer
  WS1.5 deploy-on-Ready ; WS1.6 harden + webhook HMAC
  └─ Test gate 1 = GO-LIVE gate: zero plaintext + honest deploy status + sealed OpenBao
  ════════════════════ ship self-hosted here ════════════════════
```

## Risk notes & verified feasibility (M1)
- **Phase 0 is the big batch but the lowest-risk** — merge + restructure + Git Data API are
  behavior-preserving, and the abstraction is additive. Test gate 0 (full flow still works **+ import
  fences green**) catches both regressions and boundary violations before any secret behavior changes.
- **Phase 1 is the riskiest behaviorally** — it changes how every workflow pod gets its creds.
  Validate the coding-agent flow end-to-end on the local `openbao` provider before the phase is done.
- **Boundaries are enforced, not conventional (validation BLOCKER-2):** WS0.5 ports + extends labs'
  existing AST import-fence test (`git-service/pkg/credentials/import_fence_test.go`) to every
  MUST-NOT edge in `10` §0.3; CI-gated at Phase 0. Without it the merge can re-blur the boundary.
- **OC client surface — VERIFIED (`analysis/VERIFY-secrets.md`):** the generated client already
  exposes `SecretReference` (CRUD) + `GitSecret` (Create/List/Delete) — no regeneration; only the
  WS0.3 wrappers are missing. `GitSecret` has no Get/Update → use List for read-back.
- **Ownership — VERIFIED:** the merge (WS0.1) puts `secretmanagersvc` + the OC client in one process
  (= `amp-api`), eliminating the cross-service plaintext hop that caused G1.
