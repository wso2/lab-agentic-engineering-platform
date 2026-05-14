# Build credential injection — minimal-complexity design

## Status

Proposed. Replaces the post-`2f26614` approach on `worker-image` that edits the
shared `dockerfile-builder` ClusterWorkflow.

## Problem

App Factory needs to feed per-org GitHub PATs into the shared
`dockerfile-builder` ClusterWorkflow on WSO2 Cloud, subject to four hard
constraints:

1. **Cannot edit `dockerfile-builder.yaml`.** It is a `cluster-shared`
   workflow consumed by every tenant on WSO2 Cloud (agent-manager, ipaas,
   devant, …). Forking it is a CRITICAL blast-radius change.
2. **No OpenBao in wso2dev.** PR #16 (commit `2f26614`) already moved per-org
   credentials out of OpenBao because of this. The PAT now lives in
   git-service's postgres `org_secrets` table (AES-256-GCM encrypted).
3. **No new platform API.** We do not want to depend on `secret-manager-api`
   or stand up a new service.
4. **Minimal complexity.** Reuse what we already have.

The current `worker-image` approach violates (1): the submodule commit
`052cabe4` edits `dockerfile-builder.yaml` to mount a per-org K8s Secret by
name and drop the `SecretReference` + per-run `ExternalSecret` synth. That has
to be reverted.

## The two levers upstream gives us

Read `dataplane/common/domains/platform/cluster-shared/cluster-workflows/dockerfile-builder.yaml`
in the upstream `wso2cloud-deployment` repo. Two lines do all the work:

```yaml
# line 143-144 — argo workflow parameter
- name: git-secret
  value: ${metadata.workflowRunName}-git-secret
```

The Argo `checkout-source` step mounts a **K8s Secret named
`<workflowRunName>-git-secret`** in the WorkflowRun's namespace. This name is
templated from `${metadata.workflowRunName}` directly — **not** from
`parameters.repository.secretRef`.

```yaml
# line 205-206 — ExternalSecret synth gating
resources:
  - id: git-secret
    includeWhen: ${has(parameters.repository.secretRef) && parameters.repository.secretRef != ""}
```

The `ExternalSecret` (which would otherwise materialise that Secret from a
`SecretReference` → Vault → ESO chain) is **only created when
`parameters.repository.secretRef` is non-empty**. If we pass an empty
`secretRef`, no `ExternalSecret`, no Vault dependency, no
`SecretReference` lookup.

So the workflow has a built-in escape hatch: **provide your own
`<workflowRunName>-git-secret` Secret in the WorkflowRun's namespace, pass
`secretRef=""`, and the workflow just mounts what you put there.**

## Design

### Flow

The BFF already controls the WorkflowRun name
(`component_client.go:621` —
`runName := fmt.Sprintf("%s-%d", scopedComp, time.Now().UnixMilli())`).
That's the lynchpin: we know `<runName>` before we POST the WorkflowRun, so we
can pre-stage `<runName>-git-secret` in the WP namespace and then POST.

```
                      ┌──────────────────────────────┐
                      │  BFF — workflowrun_service   │
                      │  dispatchBuild(orgID, ...)   │
                      └──────────────┬───────────────┘
                                     │
            1. Generate runName = "<comp>-<timestamp>"
                                     │
                                     ▼
                      ┌──────────────────────────────┐
                      │  git-service                 │
                      │  POST /internal/build-creds/ │
                      │     stage                    │
                      │  {orgID, runName}            │
                      └──────────────┬───────────────┘
                                     │
            2. Decrypt PAT from org_secrets (AES-256-GCM)
            3. SSA K8s Secret:
                 name:      <runName>-git-secret
                 namespace: workflows-<orgID>
                 type:      kubernetes.io/basic-auth
                 data:
                   username: x-access-token
                   password: <pat>
                                     │
                                     ▼ (returns 204)
                      ┌──────────────────────────────┐
                      │  BFF — OC client             │
                      │  POST /workflowruns          │
                      │    metadata.name: <runName>  │
                      │    spec.workflow.parameters: │
                      │      repository.secretRef: ""│
                      └──────────────┬───────────────┘
                                     │
                                     ▼
                      ┌──────────────────────────────┐
                      │  OC controller               │
                      │  — externalRefs skipped (no  │
                      │    SecretReference lookup    │
                      │    when secretRef=="")       │
                      │  — git-secret resource       │
                      │    skipped (includeWhen=false│
                      │    when secretRef=="")       │
                      └──────────────┬───────────────┘
                                     │
                                     ▼
                      ┌──────────────────────────────┐
                      │  Argo in workflows-<orgID>   │
                      │  checkout-source mounts      │
                      │  <runName>-git-secret        │
                      │  — finds our pre-staged      │
                      │    secret. Build proceeds.   │
                      └──────────────────────────────┘
```

### Why this satisfies every constraint

- **No workflow edit.** `dockerfile-builder.yaml` reverts to byte-identical
  upstream. The `secretRef==""` path is the workflow's own opt-out clause —
  intended behaviour, not a hack.
- **No OpenBao, no Vault, no `secret-manager-api`.** ESO never runs for this
  flow. The K8s Secret is created directly by git-service's existing
  `clients/k8s/` controller-runtime client.
- **No new platform API.** One new internal endpoint on git-service. Same
  package, same auth, same RBAC scope it already needs to write the
  Anthropic per-org Secret (`anthropic_credential_service.go`).
- **Minimal new code.** ~80 LoC in git-service + ~10 LoC in the BFF. Removes
  more than it adds (the submodule revert and the dead
  `models.BuildSecretName` per-org naming).

### Tradeoffs vs the current `worker-image` approach

| Aspect | `worker-image` (per-org Secret) | This design (per-WorkflowRun Secret) |
|---|---|---|
| Shared workflow YAML | **Edited** (blocker) | **Untouched** ✅ |
| K8s Secrets in WP ns | 1 per org, persistent | 1 per build, GC'd after 1d |
| Write volume on Connect | One SSA on token mint | One SSA per build dispatch |
| Cleanup | Manual (overwrites) | Auto via ownerRef → Argo Workflow |
| Code on hot path | Identical (SSA a basic-auth Secret) | Identical |

The per-build Secret is N copies of the same PAT at any given time, but each
copy is ~few-hundred bytes and lifecycled to a single build's lifetime.
That's the right grain for credentials anyway — short-lived blast radius.

## Implementation plan

### 1. Revert the workflow edit

```
cd deployments-v2/wso2cloud-deployment
git revert 052cabe4
```

Verify byte-identical to upstream `origin/main`'s
`dataplane/common/domains/platform/cluster-shared/cluster-workflows/dockerfile-builder.yaml`.

The submodule pointer in the parent repo (commit `d4d309f` advanced it to
`052cabe4`) needs to follow — bump the asdlc-side pointer back as part of
the implementation commit.

### 2. git-service — new endpoint

`POST /internal/build-credentials/stage`

```json
{
  "ocOrgId": "default",
  "workflowRunName": "default-greeting-api-1731538100123"
}
```

Behaviour:

1. Validate `ocOrgId` shape (existing `validateOrgID`).
2. Look up `org_credentials` row by `oc_org_id`. 404 if not connected,
   409 if `status != active`.
3. PAT-mode only for now (matches the scope of this design): read
   `org_secrets(oc_org_id, key="github/pat")` → decrypt with the existing
   `dbStore`.
4. SSA a `corev1.Secret` via the existing `clients/k8s/` client:

   ```yaml
   apiVersion: v1
   kind: Secret
   type: kubernetes.io/basic-auth
   metadata:
     name: <workflowRunName>-git-secret
     namespace: workflows-<ocOrgId>
     labels:
       app-factory.openchoreo.dev/managed-by: app-factory-git-service
       app-factory.openchoreo.dev/build-secret: "true"
       app-factory.openchoreo.dev/oc-org-id: <ocOrgId>
   stringData:
     username: x-access-token
     password: <pat>
   ```

   Field-manager `app-factory-git-service-build-credentials`. SSA = idempotent
   on retries.

5. Return `204 No Content`. Errors:
   - `404` org has no credential.
   - `409` `status != active`.
   - `500` decrypt / SSA failure.

Auth: same shared-secret middleware as the existing
`/internal/credentials/...` routes.

### 3. git-service — refactor `MintBuildToken`

Today (`build_credentials_service.go:109-167`) `MintBuildToken` does two
things: persist to postgres and SSA a per-org K8s Secret into
`workflows-<orgID>`. **Drop the SSA half.** Keep the postgres persistence (the
runtime token-cache invariant). Rename to make the new contract explicit, or
keep the name and update the doc — the SSA step is what we're killing.

The new `StageBuildSecret(ocOrgId, workflowRunName)` (mounted on the new
endpoint above) is the only K8s write for the build path.

`DeleteBuildSecret` on org disconnect deletes any lingering build Secrets in
the WP namespace by label
(`app-factory.openchoreo.dev/managed-by=app-factory-git-service`,
`app-factory.openchoreo.dev/oc-org-id=<ocOrgId>`). Idempotent.

`models.BuildSecretName` (`wp_naming.go:30-42`) becomes dead — remove.
`models.WorkflowPlaneNamespace` stays (still the source of truth for the
WP-namespace name).

### 4. BFF — wire the stage call

`asdlc-service/services/workflowrun_service.go:dispatchBuild`:

- Generate `runName` **before** the call to git-service. Currently the
  BFF lets the OC client generate it inside `triggerBuildInner`
  (`component_client.go:621`). Lift that name-generation into the BFF so we
  can pass it through to both git-service and OC. Two clean shapes:
  - (preferred) Add `runName string` parameter to
    `TriggerBuildAtCommit` and have the BFF generate it. Inner code uses the
    passed name verbatim.
  - (alt) Add a side channel — `RunNameFor(orgID, projectID, componentName)`
    helper exported from the OC client — and call it in
    `workflowrun_service.go` before the mint+stage.

- Replace the existing `MintBuildToken` call site (line 292) with
  `StageBuildSecret(ctx, orgID, runName)`. The BFF no longer needs to know
  the SecretRef name because **`parameters.repository.secretRef` will be
  empty**.

- In `component_client.go:triggerBuildInner` / `buildWorkflowFromComponent`:
  force `params["repository"]["secretRef"] = ""` (or strip the key) before
  the POST. The Component CR's stored workflow params may still have a
  populated `secretRef` from earlier code paths — explicitly null it for the
  build trigger. This is a one-line edit in `cloneParameterMap`'s caller, not
  a schema change.

### 5. Cleanup

First-iteration choice: rely on org-disconnect cascade only.
`DeleteBuildSecretsForOrg` (called from `CredentialService.Disconnect`'s
WP-cleanup hook) deletes every Secret in `workflows-<ocOrgId>` labelled
`app-factory.openchoreo.dev/secret-type=build-credentials`. Per-build
Secrets accumulate while the org stays connected, but each one is small
(few hundred bytes) and scoped to a single build.

If accumulation becomes a real problem, three options in order of
preference:

**(a) ownerRef to the Argo Workflow.** After the BFF's
`CreateWorkflowRun` returns, the workflowplane agent syncs an
`argoproj.io Workflow` into `workflows-<ocOrgId>` with name `<runName>`
(same as the WorkflowRun's `metadata.name` — see
`dockerfile-builder.yaml:110`). git-service fires a brief async goroutine
(`5s` timeout, exponential backoff): look up the Argo Workflow by name,
patch the staged Secret with its `ownerReference`. When the Argo Workflow
is GC'd after `ttlAfterCompletion: 1d`, the Secret cascades.

**(b) Sweep loop in git-service.** Daily ticker walks every
`workflows-*` namespace, lists Secrets with label
`app-factory.openchoreo.dev/secret-type=build-credentials` and
`creationTimestamp` older than 25h, deletes them.

**(c) Both.** ownerRef is the primary cleanup; sweep is the safety net.

### 6. RBAC

The git-service already has SSA write capability on `Secrets` in
`workflows-*` namespaces (the existing
`deployments-v2/manifests/git-service-wp-rbac.yaml` namespaced Role + RoleBinding
covers this). Confirm the Role's resource list includes:

- `secrets`: `get`, `create`, `patch`, `apply`, `delete`, `list`
- `workflows.argoproj.io`: `get` (for the ownerRef lookup in cleanup-a)

If `workflows.argoproj.io: get` isn't already there, add it. No new
namespaces needed.

For production WSO2 Cloud (cloud DP cluster), the equivalent RBAC is
declared on the deployment-repo side under the project's
`controlplane/.../namespaces/wso2cloud/projects/app-factory/.../` tree.
Same shape; add a Role + RoleBinding per WP namespace pattern. (See OC
docs for `workflows-<orgID>` namespace lifecycle — these namespaces are
created on first Project creation under an org, by OC itself.)

### 7. Disconnect

When an org disconnects, `CredentialService.Disconnect`
(`credential_service.go:528`) already wipes the postgres credential rows and
calls a `WithWPCleaner` hook for per-org WP-namespace cleanup. Wire the new
`DeleteBuildSecret`-by-label call into that hook. Any in-flight builds at
the moment of disconnect will fail at checkout-source — acceptable.

## What's deleted

The following on `worker-image` should be reverted or removed:

- Submodule commit `052cabe4` (the `dockerfile-builder.yaml` edit) — reverted.
- `git-service/models/BuildSecretName` (`wp_naming.go:30-42`) — removed.
- `git-service/services/build_credentials_service.go::applyBuildSecret` SSA
  path — removed.
- The dead `pat_secret_ref` column on `org_credentials` — leave alone for
  now (separate cleanup; not load-bearing).
- The submodule pointer in `deployments-v2/wso2cloud-deployment` is bumped
  back to whatever is upstream `local-app-factory` HEAD without the
  `052cabe4` commit.

## What's kept

- `org_secrets` (postgres-backed, encrypted PAT) stays the runtime
  source of truth. Unchanged.
- The Connect / Disconnect / Status flow is unchanged.
- `userPATCred.Token()` (the in-process credential read path used by git
  operations) is unchanged.
- Anthropic per-org Secret SSA flow is unchanged (separate path).

## Risks & open questions

1. **OC `externalRefs` lookup when `secretRef==""`** — the workflow's
   `externalRefs[git-secret-reference].name = ${parameters.repository.secretRef}`
   will resolve to `""`. The OC controller will try to look up a
   SecretReference named `""` in the WorkflowRun's namespace and fail. The
   `git-secret` resource's `includeWhen` skips creating the ExternalSecret,
   but does the externalRefs lookup itself short-circuit when the name is
   empty? **Needs verification against the OC source**
   (`internal/controller/workflowrun/externalref.go:92-99`). If it errors,
   the workaround is to either:
   - Patch OC to treat `name == ""` as a no-op, or
   - Pre-create a dummy empty SecretReference named e.g. `noop` per org and
     pass `secretRef: noop` — the `includeWhen` still skips the
     ExternalSecret, but the lookup succeeds. The dummy SecretReference's
     `data` can be `[]` (just exists). One-time setup per org.

   Confirm before implementation. If OC requires a non-empty
   `secretRef`, option 2 is still tiny — git-service SSAs a dummy
   SecretReference `app-factory-noop` in the CP tenant namespace on org
   connect.

2. **Race: BFF stages Secret → BFF posts WorkflowRun → workflowplane
   agent syncs Argo Workflow → Argo pod scheduler mounts Secret.** The
   Secret must exist in the WP namespace before the pod mounts it. Since
   the BFF stages **before** the POST, and the sync+schedule path is at
   least a few hundred ms, this should be fine in practice. Worth a smoke
   test on local k3d to confirm.

3. **App-mode handoff.** Out of scope for this design (PAT only). When App
   mode lands, the only change is: `StageBuildSecret` mints a fresh
   installation token via `AppTokenMinter.MintForInstallation` instead of
   reading from `org_secrets`. The K8s SSA path is unchanged. Token-TTL
   handling (1h on installation tokens) for builds that take >1h is a
   follow-up — not specific to this credential-delivery design.

4. **The dead `pat_secret_ref` column** — separate cleanup, not load-bearing.
   Drop in a follow-up migration when convenient.

## Files touched

- `deployments-v2/wso2cloud-deployment/.../dockerfile-builder.yaml` — revert
- `deployments-v2/wso2cloud-deployment` submodule pointer — bumped back
- `git-service/services/build_credentials_service.go` — drop `applyBuildSecret`,
  add `StageBuildSecret`
- `git-service/api/credentials_routes.go` (or a new
  `build_credentials_routes.go`) — `POST /internal/build-credentials/stage`
- `git-service/models/wp_naming.go` — drop `BuildSecretName`
- `git-service/services/credential_service.go::Disconnect` — wire
  label-based cleanup of build Secrets
- `asdlc-service/services/workflowrun_service.go::dispatchBuild` — generate
  `runName` upfront, call `StageBuildSecret`, drop `MintBuildToken` call
- `asdlc-service/clients/gitservice/client.go` — new `StageBuildSecret`
  method
- `asdlc-service/clients/openchoreo/component_client.go::triggerBuildInner` —
  accept `runName` parameter, force `secretRef=""` on parameters
- (if OC requires non-empty `secretRef`) one-time dummy
  `SecretReference` SSA on org connect — git-service
