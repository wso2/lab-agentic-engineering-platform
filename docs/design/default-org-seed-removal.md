# Platform Org-Neutrality Refactor

This document specifies a refactor that removes all "default" / "admin"
org special-casing from the platform code path, replaces the shared
`asdlc-user-projects` collapsing namespace with per-org namespaces
derived directly from the OC `ouHandle`, and reduces the local-dev
GitHub-credential convenience to a single tightly-scoped admin-only
script.

After this change, every long-lived service binary (BFF, git-service,
console build) is **org-agnostic**: it knows about `ouHandle`, nothing
else. There is no env var that names an org. There is no env var that
maps an org to a namespace. There is no platform manifest that names
"admin" or "default". The only place either of those words can appear
is in a `LOCAL_DEV_*` env var consumed exclusively by a host-side
script.

This doc has been written and iterated against the
`oc-design-expert` and `wso2cloud-expert` reviews; both signed off on
the smaller PAT-removal refactor (round 1) and have been re-engaged on
this expanded scope (round 2).

---

## 0. Update — agent-manager alignment (supersedes parts of §3)

After landing the original refactor, follow-up research into
agent-manager and canonical wso2cloud-deployment showed that this
stack's established convention is:

- **Tenant onboarding is the platform's job, not the BFF's.** In
  hosted, `platform-api-service` creates the per-tenant OC namespace
  via Thunder's `notify_org_created` webhook. The BFF reads the
  resulting state.
- **The BFF is verify-only.** agent-manager never calls
  `POST /api/v1/namespaces`; it reads OC namespaces directly via the
  OC system token. `orgName == OC namespace == K8s namespace` 1:1.
- **No reserved-handle deny list anywhere.** Thunder enforces OU
  uniqueness; OC's `CreateNamespace` refuses to adopt unlabelled K8s
  namespaces. The defenses live outside the BFF.

To match that posture, the App Factory binary was further trimmed:

- `OrganizationService.EnsureForOuHandle` is now **verify-only** —
  calls `nsCli.GetNamespace(ouHandle)` and caches the local row's
  UUID. Returns `ErrOrganizationNotProvisioned` on 404, which the
  middleware logs and passes through.
- `OrganizationService.Create` and `POST /api/v1/organizations` are
  **removed**. The BFF binary cannot create namespaces in any
  environment — same as agent-manager.
- The reserved-handle deny list is **deleted**. No `default`,
  `kube-*`, `dp-*`, `cp-*`, `workflows-*` blocking; the BFF doesn't
  create, so there is nothing to deny.
- No `DEPLOYMENT_TIER`-gated branches in the BFF org code path.

Local-dev tenant onboarding now mirrors `platform-api-service`'s job
imperatively at install time:

- New `deployments-v2/scripts/lib/seed-admin-org.sh`, invoked from
  `setup.sh`, labels the K8s `default` namespace with
  `openchoreo.dev/control-plane: "true"` and applies the per-org
  bootstrap manifests (Project, DeploymentPipeline, Environments,
  ComponentTypes, Traits, plane refs) inside it. These manifests
  already ship in the App Factory submodule under
  `wso2cloud-local/orgdefaultresources/` — same content
  `platform-api-service` applies in hosted (canonical's
  `org-default-resources/dev/.../v1.0/cp/`).
- The Thunder seed places the admin user in OU `Default`
  (handle=`default`); after the user_attributes update on
  `APP_FACTORY_CONSOLE` the JWT carries `ouHandle="default"` and the
  binary verifies against the K8s `default` ns.
- `LOCAL_DEV_ADMIN_OUHANDLE` defaults to `default`.
  `seed-admin-github.sh` no longer JIT-creates the org — it only
  POSTs the PAT to the Connect API.

Hosted promotion checklist (in addition to §6 below):

- Drop `local-dev-seeder` from `JWT_AUDIENCE` (local-only Thunder
  client).
- Confirm `platform-api-service` runs on the target cluster and is
  configured to materialise OC namespaces per Thunder OU.
- Do NOT ship `seed-admin-org.sh` or `seed-admin-github.sh` into
  hosted setup. They are local-only.
- The BFF service identity does NOT need `namespace:create` — the
  binary never calls it.

---

## 1. Why this matters

The codebase contains four overlapping leaks of the same anti-pattern:
"the platform special-cases org names." Three were already known. The
fourth (the `asdlc-user-projects` collapsing namespace) is the one
that makes multi-tenancy fake today.

### 1.1 `git-service` binary's runtime fallback PAT (load-bearing leak)

`IssueService`, `BoardService`, and `ProjectController` use
`cfg.GitHubPlatformPAT` for every GitHub Projects (V2 / GraphQL) call,
bypassing the per-org `credentials.Resolver`. `ProjectController.InitProject`
additionally hard-codes `cfg.GitHubRepoOwner` as the GitHub org under
which boards are created (`controllers/project_controller.go:61`).

> **This is a hard tenant-isolation break.** Every tenant's GitHub
> project board is created under, and auth'd with, the platform's
> GitHub identity — regardless of which OC org the request belongs
> to. The startup seeder's existence is the only reason this hasn't
> been a visible bug.

### 1.2 `git-service` startup seeder

`git-service/internal/seed/default_org_pat.go`, invoked from
`cmd/git-service/main.go:231-241`, reads `GITHUB_PLATFORM_PAT`,
`GITHUB_REPO_OWNER`, and `GITHUB_PLATFORM_PAT_SEED_ORGS` (default
`"default"`) on every boot and writes credentials for the named orgs.
The binary special-cases an org name and pulls its credential from env.

### 1.3 BFF's `PLATFORM_API_NAMESPACE_OVERRIDE` collapsing namespace

The BFF reads
`PLATFORM_API_NAMESPACE_OVERRIDE="admin=asdlc-user-projects,default=asdlc-user-projects"`
and, in `clients/openchoreo/client_base.go:resolveNamespace`, maps
both org handles to the literal namespace `asdlc-user-projects` for
every OC API call. The platform manifests pre-create that namespace
at `wso2cloud-deployment/wso2cloud-local/domains/developers/namespaces/wso2cloud/projects/app-factory/namespace.yaml`.
`asdlc-service/services/progress_service.go:84` carries
`"asdlc-user-projects"` as a hardcoded fallback when
`WORKFLOW_PLANE_NAMESPACE` is unset.

> **This is what makes multi-tenancy fake.** Two distinct OC orgs
> (`admin`, `default`) collapse into one OC Project / one workflow
> plane. Their builds, coding-agent runs, and observability all
> commingle in a shared namespace. As `oc-native-migration.md:81`
> already calls out: "Cannot ship to external customers."
>
> The runtime *already* supports per-org namespaces — `client_base.go`
> has a fallback path that returns `orgHandle` directly when the
> override map is empty, and `OrganizationService.Create`
> (`asdlc-service/services/organization_service.go:104-148`) already
> calls OC `POST /api/v1/namespaces` to provision a per-org OC
> namespace. The override env var is the only thing collapsing them.

### 1.4 Console "default" fallback

`console/src/layouts/AsdlcLayout.tsx:63` and
`console/src/pages/OrgGitHubAppPicker.tsx:27`:

```ts
const claimsOrgId = claims?.ouHandle || claims?.ouName || claims?.ouId || 'default';
```

When the JWT has no org claims, the console synthesizes the literal
string `'default'` as the org. This couples the frontend to a
particular tenant name and hides authentication bugs (a missing
`ouHandle` should fail loudly, not silently inherit a tenant
identity).

### 1.5 Goals

1. The platform binary is **org-agnostic.** No service code, no env
   var consumed by a service, and no GitOps manifest names an org. The
   binary computes everything per-request from `ouHandle`.
2. Every OC operation maps `ouHandle → namespace = ouHandle` directly.
   Per-org namespaces are JIT-created by the BFF on first authenticated
   request via the existing `NamespaceClient.CreateNamespace` path.
3. The shared `asdlc-user-projects` namespace and its manifest are
   deleted. `WORKFLOW_PLANE_NAMESPACE` becomes a per-request derivation
   from the org's namespace, not a platform-wide singleton.
4. `git-service` GitHub operations route through
   `credentials.Resolver(ocOrgID)` for both REST and GraphQL V2.
5. The only org-aware code that survives is a host-side bash script
   that POSTs the admin user's GitHub PAT via the public Connect API
   — local-dev only, scoped to `admin`, no equivalent in any
   manifest.

### 1.6 Non-goals

- Changing OC's namespace model or introducing a new prefix
  (e.g. `aft-{slug}` or `oc-{slug}`). The natural mapping
  `ouHandle == namespace` is the simplest and what `client_base.go`
  already falls back to.
- Implementing the BootstrapManifest sequence (RoleBindings, NetworkPolicies,
  per-tenant ServiceAccounts) that `oc-native-migration.md` Phase 2
  describes. That work, if needed at all, happens after this doc lands;
  this refactor only enables it.
- Touching `agents-service`, `remote-worker`, or the BFF's existing
  GitHub-webhook code path.
- Production GitOps onboarding for hosted environments. Hosted tenants
  use the console UX flow per GUIDELINES.md §9.

---

## 2. Target architecture

```
JWT arrives at BFF with ouHandle=foo
    │
    ├── BFF: jit_ensure_org(foo)
    │     └── if local Organization row missing OR OC namespace missing:
    │            insert row, call OC POST /api/v1/namespaces { name: foo }
    │            (existing path: OrganizationService.Create flow,
    │             generalised to be triggered from auth middleware)
    │
    ├── BFF: every OC API call uses namespace = ouHandle directly
    │     (clients/openchoreo/client_base.go:resolveNamespace falls
    │      through to orgHandle when nsMap is nil)
    │
    ├── BFF: every workflow-plane derivation uses
    │     workflowPlaneNs = "workflows-" + ouHandle
    │     (passed per-call to ProgressService / Observer; no global
    │      WORKFLOW_PLANE_NAMESPACE env var)
    │
    └── git-service: every GitHub call (REST + V2)
          └── cred = resolver.Resolve(ctx, ouHandle)
                cred.Token(ctx)        ← bearer for any GH API
                cred.RepoOwner()       ← github org login
                cred.Identity()        ← committer
                cred.WebhookStrategy() ← per-repo or platform
              No fallback PAT. No env-driven owner.

Local-dev convenience (NOT in any manifest):
    setup.sh → seed-admin-github.sh
       └── if LOCAL_DEV_ADMIN_GITHUB_PAT + ..._OWNER set:
              mint Thunder S2S token (client = local-dev-seeder)
              POST /api/v1/orgs/admin/credentials/connect
                   { kind: "user-pat", pat, githubLogin }
       └── exactly one org (admin), unconditional Connect (idempotent),
           localhost-only guard, soft-fail.
```

There is exactly one place where the string `admin` appears in
the host-side `.env` (a `LOCAL_DEV_ADMIN_GITHUB_*` var) and exactly
one place where a script reads it (`seed-admin-github.sh`). Nothing
else in the repository — service code, manifests, console, agents —
mentions either `admin` or `default`.

---

## 3. Detailed design

### 3.1 `git-service` — credential resolver as the only seam

Identical to round 1; folded here for completeness.

**Delete:**

- `git-service/internal/seed/default_org_pat.go` (entire file).
- The seeder invocation at `cmd/git-service/main.go:231-241`.
- Fields `GitHubPlatformPAT`, `GitHubRepoOwner`, `GitHubPlatformPATSeedOrgs`
  from `config/config.go:21-33`.
- The corresponding three lines from `config/config_loader.go:35-37`.

**Refactor `IssueService` (`services/issue_service.go`):**

- Drop the `pat string` field; remove it from `NewIssueService`.
- In `ensureBoard()` and `addIssueToProject()`, reuse the credential
  already resolved by `resolveRepoAndCredential` (line 168) rather
  than resolving twice.
- Fetch `cred.Token(ctx)` **once per request**, not per-call. App-installation
  tokens are 1h-TTL and `ensureBoard` makes three sequential V2 calls.

**Refactor `BoardService` (`services/board_service.go`):**

- Drop the `pat string` field. Inject `resolver credentials.Resolver`.
- `GetBoard` and `MoveIssueToStatus` already start by fetching `gitRepo`;
  resolve the credential by `gitRepo.OrgID` and use `cred.Token(ctx)`.

**Refactor `ProjectController.InitProject`
(`controllers/project_controller.go`):**

- Drop `pat` and `repoOwner` constructor args.
- After `repoService.CreateRepo` returns, resolve the credential by
  `req.OrgID`. Use `cred.RepoOwner()` for `GetOrgID`. Use `cred.Token(ctx)`
  for the V2 calls.

**Architectural seam invariant:** new code must not call
`OpenBaoStore.Get(orgID, "github/pat")` directly; it must
`resolver.Resolve(ctx, ocOrgID)`. This is what keeps the
`pkg/credentials/credential.go:12-18` rule enforceable.

**Suggested helper:** `RepoRepository.OrgIDFor(ctx, projectID)` so all
three call sites share one resolution path. Ergonomics; design works
without it.

**Wiring in `cmd/git-service/main.go`:**

- `NewIssueService(...)`: drop the trailing `cfg.GitHubPlatformPAT` arg.
- `NewBoardService(...)`: pass `resolver` instead of `cfg.GitHubPlatformPAT`.
- `NewProjectController(...)`: pass `resolver`; drop `cfg.GitHubPlatformPAT`
  and `cfg.GitHubRepoOwner`.

**Precondition:** audit GitHub App's `organization_projects: read+write`
permission. Once the runtime fallback is removed, App-mode tenants will
silently `403` if the App lacks Projects permissions.

### 3.2 BFF — `ouHandle` is the namespace

**Delete:**

- `PLATFORM_API_NAMESPACE_OVERRIDE` from `asdlc-service/config/config.go`
  and `config_loader.go` (the entire `PlatformAPI.NamespaceOverride`
  field and parsing).
- The `parseNamespaceOverride` function and the `nsMap` field on
  `clientBase` (`clients/openchoreo/client_base.go:28-65`).
- `WORKFLOW_PLANE_NAMESPACE` from `config.go:111-117` and
  `config_loader.go:59`. There is no platform-wide workflow plane
  namespace once orgs are not collapsed.
- The hardcoded `"asdlc-user-projects"` fallback at
  `services/progress_service.go:84`.

**Simplify `client_base.go:resolveNamespace`:**

```go
// Before:
//   resolveNamespace(orgHandle string) string
//     if c.nsMap == nil { return orgHandle }
//     if ns, ok := c.nsMap[orgHandle]; ok { return ns }
//     return orgHandle
//
// After:
//   resolveNamespace is deleted. Call sites pass ouHandle directly
//   to namespace-shaped OC APIs.
```

Every call site that today does `c.resolveNamespace(orgHandle)` becomes
just `orgHandle`. There is no override, no fallback, no map.

**JIT org provisioning:** add a `jit_ensure_org` step in the BFF auth
middleware (or in a new `OrganizationResolver` invoked on every request
that needs an org-scoped OC call):

1. **Resolve the canonical claim.** A single function in the BFF
   middleware returns `ouHandle` from JWT — no other code reads the
   claim directly. The console and BFF must agree on precedence
   (today's console at `AsdlcLayout.tsx:63` is `ouHandle → ouName →
   ouId`); the BFF mirrors that order verbatim. Divergence between
   console-side and BFF-side claim resolution is itself a tenant-leak
   surface. One resolver function, one precedence list, both sides
   import it (or document the contract and integration-test it).
2. **Validate against the reserved-handle deny list.** `ouHandle`
   MUST NOT match any of:
   - Kubernetes system namespaces: `default`, `kube-system`,
     `kube-public`, `kube-node-lease`, `local-path-storage`.
   - Any prefix `kube-`, `openchoreo-`, `cert-manager`, `flux-system`,
     `external-secrets`, `dp-`, `cp-`, `wso2cloud`, `workflows-`.

   The `workflows-` prefix is the most important: OC's WorkflowRun
   pipeline at `internal/pipeline/workflow/pipeline.go:211` literally
   renders `workflows-<sourceNs>` as the target namespace. If
   Thunder issues `ouHandle = workflows-foo`, the BFF JIT-creates
   namespace `workflows-foo`, then a WorkflowRun in *some other*
   org `foo` *also* targets `workflows-foo` — cross-tenant
   contamination. This is a hard constraint, not a soft concern.
3. Look up local `Organization` row by name.
4. If missing, insert row + call `OrganizationService.Create`-equivalent
   path that POSTs to OC `/api/v1/namespaces` with `name = ouHandle`.
5. Idempotent: 409 from OC means the namespace already exists; backfill
   the local row from `nsCli.ListNamespaces` (the existing
   `OrganizationService.List` backfill path already does this).
6. **Best-effort, never 5xx the user's request.** If OC's API server
   is briefly unavailable during a request and the JIT call returns
   a transient error, the middleware logs and proceeds — the next
   request retries. Do not let a transient OC blip propagate as a
   user-facing 500. Returning the OC error verbatim only happens
   when the failure is unambiguously the request's fault (e.g. 400
   from `CreateNamespace` validation — meaning the deny-list check
   above missed something, which should be a development-time bug).

The existing `OrganizationService.Create`
(`services/organization_service.go:104-148`) does steps 1-3 already;
we factor out the name-resolution path so it is reachable from
auth middleware, not just the
`POST /api/v1/organizations` controller. The DB-row-first-then-OC-call
ordering is preserved verbatim because it is what makes concurrent
JIT-creates safe.

**Per-request workflow plane derivation:** `ProgressService` (and
anything else that today reads `cfg.Observability.WorkflowPlaneNamespace`)
takes the org namespace as a per-call argument:

```go
// Before:
//   svc := NewProgressService(..., cfg.Observability.WorkflowPlaneNamespace)
//   svc.GetProgress(ctx, taskID) // looks up workflowPlaneNs = field
//
// After:
//   svc := NewProgressService(...)  // no workflow-plane arg
//   svc.GetProgress(ctx, taskID, ouHandle)
//      └── workflowPlaneNs := "workflows-" + ouHandle
//          (Observer prepends "workflows-" today; this just makes the
//           caller responsible for picking the right OC project ns)
```

The `workflows-` prefix is derived inside the OC platform itself: the
WorkflowRun pipeline's CEL context binding at
`internal/pipeline/workflow/pipeline.go:211` renders
`fmt.Sprintf("workflows-%s", input.Context.NamespaceName)`, and the
WorkflowRun controller's `ensurePrerequisites` at
`internal/controller/workflowrun/run_engine.go:26-49` creates the
target namespace + a `ServiceAccount` + `Role` + `RoleBinding` (with
`workflowtaskresults` permissions only) on every reconcile, using
`IsAlreadyExists` as the idempotency gate. There is no
`ClusterWorkflowPlane` controller knob, no per-tenant config, no
pre-declaration anywhere; the workflow plane namespace materialises
on first WorkflowRun for any source namespace. The Observer side
mirrors this stateless model: queries at
`internal/observer/opensearch/queries.go:189-191` derive
`workflows-<NamespaceName>` per request, with no subscription state.
The BFF passes the OC project namespace name (`ouHandle`) to the
Observer per call; the prefix is applied downstream.

### 3.3 Console — drop the `'default'` fallback

`console/src/layouts/AsdlcLayout.tsx:63` and
`console/src/pages/OrgGitHubAppPicker.tsx:27`:

```ts
// Before:
const claimsOrgId = claims?.ouHandle || claims?.ouName || claims?.ouId || 'default';

// After:
const claimsOrgId = claims?.ouHandle ?? claims?.ouName ?? claims?.ouId;
if (!claimsOrgId) {
    // Render an unambiguous error UI:
    //   "Your account has not been assigned to an organization.
    //    Contact your administrator."
    // Do not redirect to login (the user IS authenticated; their
    // JWT just lacks org context). Do not show a generic 500.
    // The single legitimate path that produces this state is a
    // pre-onboarded user — show the message and a stub link to
    // org-creation if available.
}
```

The `??` (vs `||`) change is deliberate: empty string `""` should also
fail rather than fall through. JWT issuers must populate at least one
of these claims; today's behaviour silently masks three different
failure modes:

1. **Pre-onboarded user** — Thunder issued a JWT with `sub` but no
   `ouHandle` because the user hasn't been assigned to any org. The
   error message above is for this case.
2. **Misconfigured Thunder OAuth app** — admin forgot to enable the
   `ouHandle` claim mapping. Fail-loud surfaces the misconfiguration.
3. **M2M token in browser context** — `client_credentials` tokens
   have no `ouHandle` because they have no human user. The console
   should never see one; if it does, that is a copy-paste accident
   or a misconfigured client and we want it to fail loud.

The console-side claim resolver MUST be the same precedence the BFF
uses (§3.2 step 1) — exported from a shared module or documented
identically and integration-tested.

### 3.4 GitOps — delete the collapsing namespace and override

**Repo (`lab-app-factory`):**

- `deployments-v2/manifests/env-overlays/app-factory-api.yaml`:
  delete `PLATFORM_API_NAMESPACE_OVERRIDE` (line 15) and
  `WORKFLOW_PLANE_NAMESPACE` (lines 78-79).
- `deployments-v2/manifests/env-overlays/app-factory-git-service.yaml`:
  delete `GITHUB_PLATFORM_PAT_SEED_ORGS` / `GITHUB_REPO_OWNER` /
  `GITHUB_PLATFORM_PAT` (lines 34-41).

**Submodule (`wso2cloud-deployment`, branch `local-app-factory`):**

- `wso2cloud-local/domains/platform/namespaces/wso2cloud/release-bindings/app-factory/app-factory-api/app-factory-api-development.yaml:60`
  — delete the `PLATFORM_API_NAMESPACE_OVERRIDE` env entry.
- `wso2cloud-local/domains/platform/namespaces/wso2cloud/release-bindings/app-factory/app-factory-git-service/app-factory-git-service-development.yaml:49-67`
  — delete the three `GITHUB_*` env keys.
- `wso2cloud-local/domains/platform/namespaces/wso2cloud/secret-references/github-platform-pat.yaml`
  — delete the file.
- `wso2cloud-local/domains/platform/namespaces/wso2cloud/secret-references/kustomization.yaml`
  — drop the `- github-platform-pat.yaml` entry. (GUIDELINES.md:559.)
- `wso2cloud-local/domains/developers/namespaces/wso2cloud/projects/app-factory/namespace.yaml`
  — **delete the file.** This is the literal `asdlc-user-projects`
  Namespace declaration. With the override gone, no code or manifest
  references it, so the namespace must not be platform-pre-created
  either.
- `wso2cloud-local/domains/developers/namespaces/wso2cloud/projects/app-factory/kustomization.yaml`
  — drop the `- namespace.yaml` entry. If the directory becomes empty
  after this, delete the directory and the parent kustomize entry too.
  (GUIDELINES.md §5:265-296 — "list resources explicitly.")

**Layer-0 / layer-1 blockers (also in the submodule):**

- `wso2cloud-local/init/layer-0/namespaces/workflows-default.yaml`
  — **delete the file.** This is a pre-created `workflows-default`
  Namespace from the era when all tenants collapsed to `default`.
  After the refactor it is dead weight: OC's WorkflowRun controller
  auto-creates `workflows-<srcNs>` on first run via
  `ensurePrerequisites` (cited in §3.2). Verified.
- `wso2cloud-local/init/layer-0/namespaces/kustomization.yaml`
  — drop the `- workflows-default.yaml` entry (line 11).
- `wso2cloud-local/init/layer-1/cluster-secret-store/openbao-seed-secrets.yaml:30`
  — **generalise the OpenBao Kubernetes-auth allowlist.** Today:

  ```
  bound_service_account_namespaces="dp*,external-secrets,openchoreo-ci-default,workflows-default"
  ```

  Replace `workflows-default` with the glob `workflows-*`:

  ```
  bound_service_account_namespaces="dp*,external-secrets,openchoreo-ci-default,workflows-*"
  ```

  Without this change, coding-agent runner pods scheduled into
  `workflows-<ouHandle>` cannot authenticate against OpenBao to pull
  `secret/apps/anthropic` (the runner's `ANTHROPIC_API_KEY` source).
  The glob pattern matches the existing `dp*` (data-plane) glob style
  in the same line — internally consistent. Local-only; on hosted
  environments the allowlist is governed separately.

**Submodule PR description:**

```text
This PR removes all `default-org / asdlc-user-projects / workflows-default`
platform special-casing from the `local-app-factory` overlay:

  - release-bindings/.../app-factory-api-development.yaml
    key: PLATFORM_API_NAMESPACE_OVERRIDE
  - release-bindings/.../app-factory-git-service-development.yaml
    keys: GITHUB_PLATFORM_PAT, GITHUB_REPO_OWNER, GITHUB_PLATFORM_PAT_SEED_ORGS
  - secret-references/github-platform-pat.yaml (deleted)
  - secret-references/kustomization.yaml (entry removed)
  - domains/developers/.../app-factory/namespace.yaml
    (asdlc-user-projects Namespace, deleted)
  - domains/developers/.../app-factory/kustomization.yaml
    (entry removed)
  - init/layer-0/namespaces/workflows-default.yaml
    (workflows-default Namespace, deleted)
  - init/layer-0/namespaces/kustomization.yaml (entry removed)
  - init/layer-1/cluster-secret-store/openbao-seed-secrets.yaml
    (allowlist `workflows-default` → `workflows-*` glob)

This surface MUST NOT return on `dev`, `stage`, or `prod` on
promotion. The platform binary is org-agnostic post-refactor:
each OC org maps 1:1 to a namespace named `<ouHandle>` and is
JIT-created by the BFF on first authenticated request via the
existing OrganizationService path. In hosted environments
tenants are onboarded per GUIDELINES.md §9.

This rule applies to `local-app-factory` only. The unrelated
`local` branch of OpenChoreo's own non-app-factory stack also
carries `workflows-default`; that is intentional and out of
scope.

Promotion guard (run on every promotion to dev/stage/prod):

  git grep -nE \
      'GITHUB_PLATFORM_PAT|GITHUB_REPO_OWNER|github-platform-pat|asdlc-user-projects|PLATFORM_API_NAMESPACE_OVERRIDE|workflows-default|workflows-asdlc|workflows-app-factory' \
      -- ':!terraform/**'

should return nothing on the promoted branch. The `:!terraform/**`
exclusion matters because the unrelated Vault Terraform module on
`dev`/`stage`/`prod` does mention `GITHUB_PLATFORM_PAT`.

The durable rationale is in
lab-app-factory:docs/design/default-org-seed-removal.md
(reference by lab-app-factory commit SHA, not branch).
```

### 3.5 Local-dev seed — admin-only, host-side

A new file: `deployments-v2/scripts/lib/seed-admin-github.sh`.

```text
LOCAL DEV ONLY — for hosted environments, the tenant connects via the
console UI (Settings → GitHub Integration). This script is the
equivalent action for a freshly bootstrapped k3d cluster, scoped to
the single Thunder-seeded admin user.
```

Behaviour:

1. **Inputs** (read from `.env` via `env.sh`):
   - `LOCAL_DEV_ADMIN_GITHUB_PAT`
   - `LOCAL_DEV_ADMIN_GITHUB_OWNER`
   - `LOCAL_DEV_ADMIN_OUHANDLE` — defaults to `admin`. This is the
     Thunder-issued `ouHandle` for the seeded admin user. Pinned as
     a knob (rather than hardcoded) so a future Thunder values
     change that issues a different admin handle (e.g.
     `platform-admin`) only requires bumping `.env`, not editing
     the script. **The only place an org name appears in code or
     env is this single variable.**

   No-op when `LOCAL_DEV_ADMIN_GITHUB_PAT` or `LOCAL_DEV_ADMIN_GITHUB_OWNER`
   is empty.
   **No `LOCAL_DEV_SEED_ORGS` knob.** The script seeds exactly one
   org, the one named by `LOCAL_DEV_ADMIN_OUHANDLE`. If a contributor
   wants more orgs locally, they connect via the console — same path
   as the user.

2. **Localhost guard** — refuses to run unless `PUBLIC_THUNDER_URL` host
   is `localhost`, `127.0.0.1`, or `*.openchoreo.localhost`. Prevents
   copy-paste use against shared clusters.

3. **Mint a Thunder S2S token** via `${PUBLIC_THUNDER_URL}/oauth2/token`
   using `grant_type=client_credentials` against a dedicated client
   `local-dev-seeder` (registered once during `setup.sh`'s Thunder
   bootstrap, alongside the existing S2S clients at
   `deployments-v2/scripts/lib/asdlc.sh:293-356`).

   **Entitlement scope** for the `local-dev-seeder` principal — exactly
   two endpoints, scoped to `${LOCAL_DEV_ADMIN_OUHANDLE}` only:
   - `POST /api/v1/organizations` (BFF) — to JIT-create the admin
     org's OC namespace before Connect (step 4a below).
   - `POST /api/v1/orgs/{ouHandle}/credentials/connect` (git-service)
     — the actual PAT seed.

   Every other endpoint on both services returns 403 for this
   principal. No `Disconnect`, no `IdentityFor`, no read endpoints,
   no other org's `{ouHandle}`. Implemented at the auth-middleware
   layer in each service.

4. **JIT-create the admin org's namespace, then POST Connect.**

   4a. `POST {BFF_URL}/api/v1/organizations` with body
       `{name: "${LOCAL_DEV_ADMIN_OUHANDLE}"}`. Idempotent (409
       on already-exists is treated as success). This ensures the
       OC namespace is present even if no admin login has occurred
       yet — keeps `setup.sh` self-contained.

   4b. `POST {GIT_SERVICE_URL}/api/v1/orgs/${LOCAL_DEV_ADMIN_OUHANDLE}/credentials/connect`:

       ```
       Authorization: Bearer <s2s-token>
       Content-Type: application/json

       {
         "kind": "user-pat",
         "pat": "<LOCAL_DEV_ADMIN_GITHUB_PAT>",
         "githubLogin": "<LOCAL_DEV_ADMIN_GITHUB_OWNER>"
       }
       ```

   `CredentialService.Connect()` is the single writer of both Postgres
   `org_credentials` and OpenBao `secret/asdlc/${LOCAL_DEV_ADMIN_OUHANDLE}/github/pat`.

5. **Idempotency:** unconditional Connect on every `setup.sh`. `Connect`
   is already idempotent within the same `kind`. `409` handling:

   - `disconnecting` in flight → brief retry, treat as success.
   - Cross-mode conflict (existing `app-installation` row from a prior
     console-connect) → `log_warn` and skip; do not silently
     overwrite a real intent mismatch. The script's stdout shows:

     ```
     admin org already connected via app-installation — skipping
     local-dev PAT seed. Disconnect from the console first if you
     want PAT-mode locally.
     ```

6. **Hygiene:**
   - `set +x` around the curl.
   - Trap-clear local variable holding the access token on exit.
   - Log `{ocOrgId: "admin", kind: "user-pat", githubLogin}`. Never
     log the PAT or token.
   - Soft-fail: warn-and-continue on non-2xx; do not break `setup.sh`.
     User can connect via UI.
   - Differentiate exit codes for debuggability when run standalone.

7. **Invocation:** `setup.sh` calls `seed_admin_github` after
   `bootstrap_workloads` reports git-service and BFF ready. Step 4a
   (org JIT-create) ensures the namespace exists without requiring a
   prior admin login — the script is self-contained.

### 3.6 Cleanup of `asdlc.sh`

`deployments-v2/scripts/lib/asdlc.sh`:

- Delete the `bao kv put secret/apps/github-platform-pat` line (line 26).
- Delete the entire per-org pre-seed block (lines 38-67).
- Delete the `export GITHUB_REPO_OWNER` / `export GITHUB_PLATFORM_PAT`
  lines (94-97).
- In `register_service_oauth_clients` (lines 293-356), add a
  `local-dev-seeder` entry alongside the existing clients with a TODO:

  ```sh
  # TODO: imperative Thunder OAuth client registration is acceptable
  # only on local-app-factory. On dev/stage/prod the equivalent must
  # be declared in the Thunder HelmRelease values block. See
  # docs/design/default-org-seed-removal.md §3.5.
  ```

`deployments-v2/scripts/lib/env.sh:132-233`:

- Rename prompts and exports:
  `GITHUB_PLATFORM_PAT` → `LOCAL_DEV_ADMIN_GITHUB_PAT`
  `GITHUB_REPO_OWNER` → `LOCAL_DEV_ADMIN_GITHUB_OWNER`
- Drop the "auto-seed" log lines (226-233); replace with one line.

`deployments-v2/.env.example` lines 19-30:

- Replace the block. Comment is unambiguous: "Local-dev shortcut for
  pre-connecting the *admin* user's GitHub PAT only. Has no effect
  outside `setup.sh`. The platform binary does not read these. For
  hosted environments and any non-admin org, the tenant uses the
  console UI: Settings → GitHub Integration → Connect."

`deployments-v2/README.md` lines 72, 98-99: rename and re-describe.

`deployments/docker-compose.yml` lines 202-204 (deprecated tier):
delete.

### 3.7 Documentation cleanup

- `CLAUDE.md` lines 75, 116, 139 and `AGENTS.md` mirror: drop "auto-seed",
  drop `GITHUB_PLATFORM_PAT` references; replace with a one-line mention
  of the admin-only local-dev seed. Drop the "default org auto-seeded"
  language entirely.
- `docs/design/github-integration-evolution.md` and `phase2.md`: drop
  references to a "platform-PAT credential kind" / "Phase 0 platform
  PAT". After this refactor that credential kind no longer exists.
- `docs/design/github-integration-phase0.md`: delete. The pattern it
  describes is removed.
- `docs/design/oc-native-migration.md`: cross-reference this doc.
  Section 1.4 ("multi-tenancy is currently fake") and the
  `asdlc-user-projects → aft-{tenantSlug}` migration row become
  *historical* — the per-org natural-namespace pattern (without an
  `aft-` prefix) supersedes them. Keep the doc but mark those rows
  as superseded.

---

## 4. Migration ordering

Order is load-bearing. Inverting any of these creates a window where
the binary fails to start (env var missing for a field it still
reads) or silently regresses (binary still uses fallback but no
fallback is supplied).

The refactor naturally splits into **three sub-PRs** that land
sequentially. Each is independently reviewable; each leaves the
system in a deployable state.

- **Sub-PR A** — §3.1 (`git-service` credential resolver).
  Self-contained. Removes the runtime fallback PAT and the startup
  seeder. Manifests still set the env vars; binary ignores.
- **Sub-PR B** — §3.2 + §3.3 + §3.4 (BFF JIT org-provisioning,
  console fail-loud, all submodule deletes including layer-0
  `workflows-default.yaml` and the layer-1 OpenBao allowlist).
  This is the namespace refactor. Highest review surface; must
  land as a pair with the submodule PR + pin bump.
- **Sub-PR C** — §3.5 + §3.6 + §3.7 (local-dev seed script,
  `asdlc.sh` cleanup, env/README/docs renames).

Numbered ordering:

1. **Sub-PR A — service code (git-service).** Land §3.1 in
   `lab-app-factory`. After this PR merges, `git-service` no longer
   reads `GITHUB_PLATFORM_PAT*`. Manifests still pass them; binary
   ignores. Cluster behaviour unchanged from the user's perspective.
2. **Sub-PR B — service code (BFF + console) AND submodule PR + pin
   bump.** §3.2 + §3.3 land in `lab-app-factory`; §3.4 lands in
   the submodule on `local-app-factory`. lab-app-factory's submodule
   pointer bumps to the new SHA in the same PR.

   **Critical:** the BFF's JIT org-provisioning (§3.2 step 3) must
   ship in the same code-PR as the deletion of
   `PLATFORM_API_NAMESPACE_OVERRIDE` / `WORKFLOW_PLANE_NAMESPACE`.
   Otherwise there is a window where the binary collapses orgs
   (override removed, JIT not running, fallback returns `ouHandle`
   directly → namespace doesn't exist → 404 from OC).
3. **Sub-PR C — local-dev script + cleanup.** Land §3.5, §3.6, §3.7
   in `lab-app-factory`. Renames env vars, adds `seed-admin-github.sh`,
   updates docs.
4. **Verify** with a clean `teardown.sh --all && setup.sh`. Expected
   end state:

   - Admin user logs in. JWT `ouHandle = "admin"`. BFF JIT-creates OC
     namespace `admin`. Local `Organization` row inserted.
   - `seed-admin-github.sh` runs (if `LOCAL_DEV_ADMIN_GITHUB_*` set):
     POSTs Connect for org `admin`. Postgres `org_credentials` row
     present, OpenBao `secret/asdlc/admin/github/pat` populated.
   - `kubectl get ns | grep -E 'admin|asdlc-user-projects'` → only
     `admin`. The collapsing namespace is gone.
   - `kubectl get ns workflows-admin` → exists (auto-created by OC
     ClusterWorkflowPlane reconciler when the first WorkflowRun is
     dispatched into namespace `admin`).
   - Creating a project, dispatching a coding-agent run, building, and
     deploying all succeed end-to-end.
   - A second user logs in with `ouHandle=foo`: BFF JIT-creates ns
     `foo`; their work is fully isolated from `admin`.

---

## 5. Risks & mitigations

| Risk | Likelihood | Mitigation |
|---|---|---|
| BFF JIT org-creation fails because OC's RBAC for the BFF's service identity disallows `POST /namespaces` | Medium | The current `OrganizationService.Create` already calls this endpoint and works for the user-driven "Create Organization" flow — same RBAC, same endpoint, just triggered from auth middleware. No new permission needed. |
| App-mode tenants lose board ops because the GitHub App lacks `organization_projects: write` | High if not checked | §3.1 Precondition. Add a one-shot canary in `tests/api`. |
| **Existing local clusters break on upgrade** — data scoped to `asdlc-user-projects` becomes inaccessible because no code/manifest references that namespace any more | High for any developer with running state | **In-place upgrade is not supported.** Hard requirement: `teardown.sh --all && setup.sh`. Document prominently in the PR description and in the README. Hosted environments do not carry this surface, so promotion is non-disruptive. |
| BFF JIT-creates a namespace whose `ouHandle` collides with a Kubernetes / OC reserved namespace (e.g. `default`, `kube-system`, `workflows-foo`, `openchoreo-ci-default`) | Medium if Thunder allows arbitrary handles | §3.2 step 2: BFF deny-list rejects reserved prefixes/names. The `workflows-` prefix in particular causes cross-tenant contamination because OC's WorkflowRun pipeline derives `workflows-<srcNs>`. DNS-1123 is necessary but not sufficient. Treat the deny-list as a security-grade check, not a polish item. |
| BFF reads `claims.OuHandle` and a future Thunder change starts issuing a different claim shape (e.g. `org_id`, `tenantHandle`) | Medium | Pin the claim resolution behind a single function shared by BFF and console (or documented identically). Add an integration test that asserts the JWT shape Thunder issues today. Any future Thunder change becomes a one-place edit, not a multi-site drift. |
| Console treats a missing `ouHandle` as fatal and locks legitimate users out | Low (Thunder PKCE issues `ouHandle` for every assigned user today) | Verify by inspecting JWTs from both PKCE and `client_credentials` flows. The error UX surfaces the legitimate failure mode (pre-onboarded user) with a specific message, not a 500. Add integration test "JWT without ouHandle → console shows org-assignment error." |
| Workflow plane namespace `workflows-<ouHandle>` does not exist when the first WorkflowRun is dispatched | Low | OC's WorkflowRun controller auto-creates `workflows-<srcNs>` on first run via `ensurePrerequisites` (`internal/controller/workflowrun/run_engine.go:26-49`); the prefix is rendered by the WorkflowRun pipeline's CEL binding (`internal/pipeline/workflow/pipeline.go:211`). Verified in OC source. The `init/layer-0/workflows-default.yaml` manifest existed only because the auto-create path was not relied on; deleting it is safe. |
| Hosted environments use a different workflow-plane prefix or a separate workflow-plane cluster | Deferred — promotion blocker, not this PR | Before promoting any of this work to `dev`/`stage`/`prod`, verify the prefix is literally `workflows-<ouHandle>`. If hosted clusters use a different scheme, the BFF's per-call derivation must come from a per-env config the binary reads, not be hardcoded. State this in the promotion-time checklist. |
| Someone re-introduces `asdlc-user-projects`, `PLATFORM_API_NAMESPACE_OVERRIDE`, or `workflows-default` on a future submodule branch | Low | Submodule PR description carries the rule and the `git grep` guard (§3.4). This doc is the durable reference. |

---

## 6. Out of scope

- Replacing the local-dev seed with Flux/ESO/SecretReference. GUIDELINES.md
  §6 governs cluster-bound deployment artefacts; the seed script is a
  CLI client of the BFF, not an artefact.
- Renaming the OpenBao path scheme. Today: `secret/asdlc/{ocOrgId}/github/pat`
  for per-org PATs and `secret/apps/github-platform-pat` for the
  platform PAT. After §3.6 the platform path is no longer written;
  the per-org scheme is unchanged and remains the only one.

### 6.1 Deliberate departure from GUIDELINES.md §9

GUIDELINES.md §9 (551-563) describes WSO2 Cloud's onboarding model as
a **PR-driven flow**: a tenant is provisioned by a PR that lands a
manifest somewhere in `domains/developers/.../<ouHandle>/`, and Flux
reconciles the namespace into existence. App Factory's tenant model
is different by construction: tenants come from Thunder JWTs and
materialise on first authenticated request. There is no PR review
between "user logs in" and "user can create projects."

This refactor commits App Factory to **JIT runtime onboarding via the
OC `POST /api/v1/namespaces` API**, not Flux-PR onboarding. The OC
API itself is sanctioned (the existing user-driven
`POST /api/v1/organizations` already uses it; RBAC for the BFF's
service identity already permits the call). The departure from §9 is
deliberate and worth flagging:

- Onboarding becomes invisible to GitOps audit: a new tenant produces
  no PR, no commit, no Flux event. This is acceptable because OC's
  per-tenant resources (Project, Component, Workload) are the audit
  surface, not the namespace itself.
- Tenant identity is anchored in Thunder, not Git. If Thunder is
  compromised or misconfigured, tenant onboarding is compromised.
  This is true today already (Thunder is the IDP); the refactor does
  not weaken it.
- Hosted promotion will need to revisit this: §9-style onboarding may
  be required if a customer's compliance posture demands GitOps
  audit of every tenant materialisation. Plan for that as a hosted
  concern, not a `local-app-factory` one.

The two substitutions §9 implicitly relied on are made explicit
elsewhere in this design: the §3.2 step 2 deny list replaces §9's
implicit *human review of tenant names* with a coded check; OC API
audit logs replace Git history as the audit surface for tenant
materialisation.

### 6.2 Per-tenant security isolation is *blocked work*, not optional

This refactor establishes per-org **Kubernetes namespace isolation**
as the primary enforced boundary between tenants. After it lands:

- Org A and Org B's OC `Project` / `Component` / `Workload` /
  `WorkflowRun` CRs live in separate namespaces (`A` vs `B`).
- Their build pipelines, coding-agent runs, and observability are
  separated by namespace (`workflows-A` vs `workflows-B`), with
  per-namespace `ServiceAccount` + `Role` + `RoleBinding` auto-created
  by OC's WorkflowRun controller (workflow-task scope only).

**What is in place automatically (via OC):**

- **Per-Component `NetworkPolicy` on dataplane application
  namespaces.** OC's `releasebinding/controller.go:486-495` calls
  `internal/networkpolicy/networkpolicy.go:26-62`'s
  `MakeComponentPolicies` for every Component endpoint, materialising
  per-Component `networking.k8s.io/v1 NetworkPolicy` with
  `policyTypes: ["Ingress"]` and rules derived from endpoint
  visibility. Once a tenant's app goes through the Component →
  ReleaseBinding pipeline, ingress isolation ships by construction.
  This is the OC concept docs' "each cell operates as an independent
  unit with its own namespace, network policies, and security
  boundaries" (`concepts/runtime-model.md`) actually working.

**What is NOT in place — blocked work, not optional polish:**

- **No `NetworkPolicy` on workflow-plane namespaces
  (`workflows-<ouHandle>`).** OC's WorkflowRun
  `ensurePrerequisites` (`run_engine.go:26-49`) plants
  Namespace + ServiceAccount + Role + RoleBinding but no network
  policy. Runner pods in `workflows-A` can today reach pods in
  `workflows-B`. **This is the primary cross-tenant exposure
  surface in App Factory** because runner pods carry per-WorkflowRun
  ExternalSecrets pulling tenant `ANTHROPIC_API_KEY` values from
  OpenBao.
- **No per-tenant `RoleBinding` for human users.** Today's RBAC is
  cluster-wide for whoever holds the kubeconfig.
- **No per-tenant `ResourceQuota` / `LimitRange`.**
- **No per-tenant `PodSecurityAdmission` configuration.**
- **OpenBao Kubernetes-auth role on hosted clusters must use a
  per-tenant policy scope, not a `workflows-*` namespace wildcard.**
  The current `policies=openchoreo-secret-reader-policy` is a
  read-only policy scoped to specific KV paths and acceptable on
  `local-app-factory` (single trusted operator); on hosted, the
  *policy itself* must be split per-tenant so a runner pod in
  `workflows-A` cannot read keys destined for `workflows-B`.

App Factory's per-tenant security work is the right home for those
CRs; when it lands, it folds into `OrganizationService.Create` and
applies uniformly to every JIT-provisioned org. **It is blocked work
that must complete before non-trusted tenants share a hosted cluster,
not optional polish.** On `local-app-factory` (single trusted
operator runs the whole stack) the gap is acceptable; on hosted that
is a production gate. State this in the hosted-promotion checklist.

---

## 7. References

**Project files:**

- `git-service/pkg/credentials/credential.go:12-18` — the rule we are
  enforcing.
- `git-service/services/credential_service.go:189-220` — the Connect
  flow we reuse unchanged.
- `git-service/cmd/git-service/main.go:206,231-241,257,297` — wiring
  to change.
- `git-service/services/issue_service.go`,
  `git-service/services/board_service.go`,
  `git-service/controllers/project_controller.go` — runtime call sites.
- `asdlc-service/clients/openchoreo/client_base.go:28-65` — the
  `resolveNamespace` to delete; fallback path that already returns
  `orgHandle` directly.
- `asdlc-service/services/organization_service.go:104-148` — the JIT
  provisioning path the auth middleware reuses.
- `asdlc-service/services/progress_service.go:74-90` — workflow plane
  hardcoded fallback to delete.
- `console/src/layouts/AsdlcLayout.tsx:63`,
  `console/src/pages/OrgGitHubAppPicker.tsx:27` — `'default'`
  fallback to delete.
- `deployments-v2/scripts/lib/asdlc.sh:293-356` — Thunder S2S client
  registration we extend with `local-dev-seeder`.

**OpenChoreo precedent:**

- OC `internal/openchoreo-api/services/namespace/service.go:36-78` —
  the namespace service the BFF calls; idempotent via
  `IsAlreadyExists` (line 65); stamps `LabelKeyControlPlaneNamespace=true`
  (line 57) so OC will not later try to manage non-OC namespaces.
- OC `internal/controller/workflowrun/run_engine.go:26-49` —
  `ensurePrerequisites` auto-creates the workflow-plane namespace +
  ServiceAccount + Role + RoleBinding on first WorkflowRun for any
  source namespace. This is what makes deleting `workflows-default.yaml`
  safe.
- OC `internal/pipeline/workflow/pipeline.go:211` — the
  `workflows-<srcNs>` derivation. Hard reserved prefix; explains the
  `workflows-` entry on the §3.2 deny list.
- OC `internal/observer/opensearch/queries.go:189-191` — Observer
  queries derive `workflows-<NamespaceName>` from a per-request
  parameter, not from any stored subscription state. Supports the
  per-call `ProgressService.GetProgress(ctx, taskID, ouHandle)`
  shape in §3.2.
- OC `api/v1alpha1/secretreference_types.go:64-79,114` and
  `concepts/platform-abstractions.md` — `SecretReference` is
  workload-bound; using it for a platform PAT inverts the model.
- OC `internal/openchoreo-api/config/security.go:27,176-204` and
  `api/v1alpha1/authzrolebinding_types.go:35` — subject identification
  via JWT claims, entitlement scoping per-subject. Each Thunder
  client = a distinct subject.

**WSO2 Cloud GUIDELINES.md compliance:**

- §3 placement under controlplane/dataplane — unchanged; manifests
  remain in the same paths.
- §5:265-296 Kustomize patterns ("List resources explicitly — avoid
  wildcards") — addressed in §3.4 by removing the
  `- namespace.yaml` entry from
  `domains/developers/.../app-factory/kustomization.yaml` and the
  `- workflows-default.yaml` entry from
  `init/layer-0/namespaces/kustomization.yaml`.
- §6 Flux variable substitution from `cluster-secrets` — **no longer
  applies to this surface**. After the refactor there is no `${var}`
  substitution for the per-org PAT and no SecretReference; `git-service`
  consumes the PAT at runtime via its `credentials.Resolver`, the
  same shape as `bff-task-signing-key`.
- §9:551-563 onboarding checklist — App Factory's tenant model
  departs from §9's PR-driven onboarding (see §6.1 above for the
  rationale and trade-offs); the kustomization-update line at §9:559
  is explicitly addressed by §3.4's removal of the
  `- github-platform-pat.yaml` entry from
  `secret-references/kustomization.yaml`.

**Reviews:**

- `oc-design-expert` round 1 — approved direction; flagged
  `ProjectController.InitProject` tenant-isolation break as
  non-negotiable, App-mode V2 permission audit as precondition.
- `wso2cloud-expert` round 1 — approved local-only script approach;
  supplied localhost guard, dedicated S2S client, hygiene rules,
  migration ordering, branch hygiene confirmation.
- Round 2 (this expanded scope) — pending. To be folded into the doc
  as inline updates with rationale.
