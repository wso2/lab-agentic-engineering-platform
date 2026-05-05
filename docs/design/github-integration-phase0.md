# GitHub Integration — Phase 0 Implementation Design

This is the concrete, implementation-level design for the **Phase 0 happy-path chunk** of `github-integration-evolution.md`. The evolution doc is the architectural truth; this doc is the engineering plan: schemas, endpoints, file layouts, dependencies, migration steps. Read the evolution doc first.

The chunk replaces the server-mediated submit pipeline with a GitHub-native flow: each task surfaces as an issue + feature branch + draft PR, the agent uses `git` and `gh` directly inside a per-task workspace, webhooks drive every state transition, and builds trigger from BFF-created `WorkflowRun`s pinned to merge SHAs. MCP goes away. The chunk lands in one batch — splitting it leaves the system with two control planes instead of zero.

---

## 0. Implementation status (as of 2026-04-27)

The Phase 0 happy-path is **shipped end-to-end**, end-to-end-verified against a live GitHub repo + smee.io tunnel + OC cluster: webhook receiver, projector, build watcher, credential resolver seam, branch / PR / webhook services in git-service, smee.io wiring, BFF-driven `WorkflowRun` with SHA injection, per-task bearer, dispatch rewrite, OC component creation at dispatch (`AutoBuild=false`), and the destructive task-model migration are all in place and wired in `main.go`.

The §0 cleanup pass (items 1–5 below) is closed:

1. **Legacy task-status code paths removed.** The four sub-status fields and the `'completed'` terminal are gone from the model and the console type. `services/task_service.go` no longer carries `SubmitImplementation`, `ReportProgress`, `RetryTask`, `runPostImplementationPipeline`, `createOCComponentWithWorkflow`, `updateBuildDeployStatus`, `autoDeploy`, `failTask`, `formatProgressComment`, or `resolveTaskByIssueURL`. The retry endpoint is unmounted (re-dispatch lifecycle is a deferred follow-up — Out item 4); the console retry button is gone. OC component creation lives in `services/remote_worker_service.go::ensureOCComponent` with `AutoBuild=false` and no `TriggerBuild` call.
2. **MCP scaffolding purged.** `asdlc-service/go.mod` no longer depends on `github.com/mark3labs/mcp-go` (`go mod tidy` ran), `remote-worker/plugin/` is deleted, and the console "Implement Locally" dialog now describes the `git`/`gh` workflow instead of plugin install + MCP endpoint.
3. **Repo idempotency contract honoured.** `git-service/services/repo_service.go::CreateRepo` now returns the existing row on repeat calls; the `ErrRepoAlreadyExists` 409 path is gone. The BFF gitservice client accepts both 200 (idempotent) and 201 (first create) as success.
4. **OC `SecretReference` and `Component` idempotency asserted at the call boundary.** `clients/openchoreo/component_client.go::CreateGitSecret` treats 409 Conflict as success (one SecretReference per project); `CreateComponent` treats 409 Conflict as success and re-fetches the existing component to return its row (one Component per `(ocOrgId, project, componentName)`).
5. **OpenBao import-fence enforced.** `git-service/pkg/credentials/import_fence_test.go` walks every Go file under `git-service/` and fails the build if any file outside `pkg/credentials/` imports an OpenBao or HashiCorp Vault SDK package. Phase 2's OpenBao integration must land inside `pkg/credentials/` or the test fails.

### Happy-path verification ledger

The flow below is what the feature-verifier exercised end-to-end against the live stack. Each step lists the outward signal (DB row, GitHub artifact, log line) that confirms it. Reproducing this is the canonical Phase 0 smoke test.

1. **Project + webhook**
   - Create a project via the console.
   - `gh api /repos/<owner>/<repo>/hooks` → exactly one hook, events `["pull_request","push","issue_comment"]` (no `installation_repositories`).
   - DB: `git_repositories.webhook_id IS NOT NULL` for the project.

2. **Spec → design → tasks**
   - Generate spec → save & approve → tag `spec-v1` exists.
   - Generate design → save & approve → tag `design-v1` with lineage `from spec-v1`.
   - Generate tasks → for each task, GitHub issue exists; DB: `component_tasks.issue_number > 0` and `issue_url` set.

3. **Dispatch (Start Implementation)**
   - Feature branch `task/<slug>-<short8>` exists on GitHub.
   - `.asdlc/task.json` exists on the branch (`gh api /repos/<owner>/<repo>/contents/.asdlc/task.json?ref=<branch>` returns base64 JSON with `taskId`, `componentName`, `issueNumber`).
   - Draft PR exists with body `Closes #<issueNumber>`.
   - OC: GitSecret `<projectID>-git-pat` exists in the org namespace; Component `<projectID>-<slug(componentName)>` exists with `AutoBuild=false` and the `dockerfile-builder` workflow pinned to the feature branch.
   - Workspace `~/asdlc-workspace/<orgId>/<projectId>/<taskId>/`: `.asdlc/bearer` (mode 600), `.asdlc/credhelper.sh` (700), `.asdlc/gh` (755); `.git/config` has `user.name`, `user.email`, and `credential.https://github.com.helper` set; sibling `<workspace>.stage` does NOT exist.
   - DB: `status='in_progress'`, `dispatched_at` set, `branch_name` and `pull_request_number` populated.

4. **Webhook injection (signed `pull_request.ready_for_review`)**
   - HMAC-SHA256-signed POST with fresh `X-GitHub-Delivery` returns 200.
   - Task advances `in_progress → ready_for_review`; `last_event_at` updated.
   - Stale tasks in other projects with the same PR number are NOT touched (repo-scoped projector lookup).

5. **Webhook dedup + HMAC**
   - Replay same `X-GitHub-Delivery` → 200, `webhook_deliveries.processed_at` unchanged.
   - Flip one byte of the signature → 401, `webhook: signature rejected` in BFF logs.

6. **Merge + push lifecycle**
   - POST `pull_request.closed merged=true` with a 40-hex `merge_commit_sha` → task `ready_for_review → merged`, `merge_commit_sha` recorded.
   - POST `push` to `refs/heads/<defaultBranch>` with `head_commit.id` matching the merge SHA → task `merged → building`. `LastBuildSHA` and `LastBuildRunName` set on the task.
   - Build watcher takes `building → deployed | failed` from OC `WorkflowRun` status.

---

## 1. Scope

### In

1. **Webhook receiver** at `POST /webhooks/github` on the BFF. HMAC-validated, delivery-ID-deduped, synchronously processed.
2. **Per-repo webhook registration** at `git-service` repo provisioning, using the platform PAT.
3. **Dispatch rewrite**: per task, create issue + feature branch + draft PR + per-task workspace cloned on that branch.
4. **Workspace credential helper + `/credentials/refresh` endpoint** on git-service. Phase 0 returns the static platform PAT with a far-future expiry; the seam exists so Phase 2 short-lived App tokens slot in unchanged.
5. **Agent prompt update**: agent uses `git` and `gh` directly. Posts progress as `gh issue comment` on its task issue. Marks the PR ready (`gh pr ready`) when done.
6. **MCP server fully removed**: `asdlc-service/mcp/` deleted, route unmounted, `remote_worker_service.go` no longer passes an MCP endpoint URL, allowed-tools list in `remote-worker/src/lib/runner.ts` drops both `mcp__asdlc__*` tools.
7. **Webhook handlers — happy path only**: `pull_request.opened` (noop, draft PR is ours), `pull_request.ready_for_review` (advances `in_progress → ready_for_review`), `pull_request.closed merged=true` (records merge SHA, advances `ready_for_review → merged`), `pull_request.closed merged=false` (`* → rejected`), `push` to default branch (creates `WorkflowRun`s for every Component in the project, pinned to the push SHA). All other events: persisted, otherwise no-op.
8. **BFF-driven `WorkflowRun`** with **commit SHA injection** via `params.repository.revision.commit`, mirroring agent-manager (`/Users/wso2/repos/agent-manager/agent-manager-service/clients/openchoreosvc/client/builds.go` lines 78–84). `Component.AutoBuild` flips to `false`; `AutoDeploy` stays `true` so deployment still rolls forward on its own.
9. **Task model migration**: drop `GitStatus / OCStatus / BuildStatus / DeployStatus`. Add `Status` (single enum), `BranchName`, `PullRequestNumber`, `PullRequestURL`, `MergeCommitSHA`, `LastEventAt`. New states: `pending → in_progress → ready_for_review → merged → building → deployed | failed | rejected`.
10. **Credential resolver seam** in git-service: `pkg/credentials/` Go package with one implementation (platform PAT). Every git-service call site routes through `Resolver.Resolve(orgID)` from day one. Plus the **WebhookSecretProvider interface** (§6.4) with the env-backed Phase 0 implementation, and the **OpenBaoStore interface declaration** (§6.5) with no implementation — Phase 2 fills it in. A build-time analyzer (or `go vet` rule) is added in Phase 0 to forbid OpenBao SDK imports outside `pkg/credentials/`, so Phase 2's OpenBao integration cannot accidentally bypass the wrapper.

A single `org_credentials` row is materialised in Phase 0 with `kind = "platform-pat"`, the env-derived identity, and `oc_org_id = "platform"`. This is the single-tenant degenerate of Phase 2's per-org table; it gives the operational gate (§7.3) a concrete `kind` field to read at startup, and gives the future per-org schema a row from day one to migrate from. The schema:

```sql
CREATE TABLE org_credentials (
  oc_org_id          TEXT PRIMARY KEY,
  kind               TEXT NOT NULL,           -- 'platform-pat' (Phase 0) | 'app-installation' | 'user-pat'
  github_login       TEXT NOT NULL,
  identity_name      TEXT NOT NULL,
  identity_email     TEXT NOT NULL,
  identity_login     TEXT NOT NULL,
  installation_id    BIGINT,                  -- App mode only
  selected_repos     JSONB,                   -- App mode only (Phase 2)
  pat_secret_ref     TEXT,                    -- PAT mode only (Phase 2)
  webhook_secrets    JSONB,                   -- list of accepted HMAC keys
  status             TEXT NOT NULL DEFAULT 'active',
  connected_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
  last_validated_at  TIMESTAMPTZ
);
```
11. **Idempotency** on the keys in evolution-doc §7.1: repo create on `(ocOrgId, project)`; `SecretReference` create on `(ocOrgId, project)` (one per repo); `Component` create on `(ocOrgId, project, componentName)`; issue/branch/PR creation at dispatch on task ID; webhook receipt on delivery ID. The `SecretReference` and `Component` keys are deliberately different — the `SecretReference` is per-repo and predates any component, while components are per-name within the project.
12. **smee.io tunnel** wired into `start.sh` for local-dev webhook delivery.

### Out (deferred follow-ups, listed in landing order)

1. **Branch protection** at repo creation + backfill script for existing repos. (Evolution-doc §4.8.)
2. **Janitor + cleanup contracts** for `abandoned` and `superseded` states. (Evolution-doc §4.3, §7.3.)
3. **Webhook ingestion durability**: durable queue, async processing, ordering reconciliation, N-of-M secret list, secret rotation. (Evolution-doc §9.1, §9.2, §7.6.)
4. **Re-dispatch / `superseded`** task lifecycle. (Evolution-doc §7.3.)
5. **Production hardening** (§9 of evolution doc, in full). All twelve items are pre-GA work, not Phase-0 work.
6. Phase 2 (per-org App / user-PAT credentials, OpenBao migration, and the local-flow ergonomic plugin folded in per evolution-doc §6.5). The seams below exist so this is additive.

---

## 2. Permission boundaries

The refactor's structural goal is to make four scopes visible in code, not just in design.

| Scope | What lives at this scope | Phase 0 representation | Phase 2 evolution |
|---|---|---|---|
| **Org** | GitHub org binding, credential record, OC namespace, agent identity, webhook-receive secret | Implicit single-platform record (env-derived) | Explicit per-org `Credential` row, kind ∈ {App-install, user-PAT} |
| **Project** | GitHub repo, per-repo webhook registration, OC project, spec/design artifacts, default-branch protection | One row in `git_repositories`; webhook ID stored alongside | Same shape, but the credential it resolves to is the org's |
| **Component** | OC `Component` CR, build state | Created at task dispatch, `Component` idempotent on `(ocOrgId, project, componentName)` | Unchanged; backing credential differs |
| **Project (re §6.3.1)** | OC `SecretReference` CR shared by all Components in the project | Created at repo provision, idempotent on `(ocOrgId, project)` — one per repo, never per-component | Unchanged; backing credential differs |
| **Task** | GitHub issue, feature branch, draft PR, per-task workspace, per-task bearer for credential refresh | One row in `component_tasks`; bearer is a signed JWT scoped to the task | Same; bearer becomes more important when credentials are short-lived |

Two cross-cutting rules these scopes encode:

- **git-service is the sole holder of GitHub credentials.** The BFF orchestrates but never sees a token. The agent reaches credentials via `/credentials/refresh` on git-service, never directly.
- **Credentials are an attribute of the org**, not the project or repo (evolution-doc §3.3). The Phase 0 resolver ignores `orgID` because there's only one credential, but the parameter is mandatory in the interface so Phase 2 doesn't need to thread it through later.

---

## 3. Service responsibilities (post-Phase-0)

```
                    ┌──────────────────────────────────────────┐
                    │ trusted internal network                 │
   GitHub ─webhook──┤▶ asdlc-service (BFF)                     │
                    │     · /webhooks/github                   │
                    │     · task state machine + projector     │
                    │     · workflowrun service (OC client)    │
                    │     · console HTTP API                   │
                    │     · NO github creds, NO MCP            │
                    │                                          │
                    │   git-service                            │
                    │     · sole holder of github credentials  │
                    │     · credentials.Resolver (seam)        │
                    │     · repo / issue / branch / pr / webhook ops
                    │     · /credentials/refresh (bearer-auth) │
                    │     · spec/design artifact tags          │
                    │                                          │
                    │   remote-worker (host)                   │
                    │     · per-task workspace provisioning    │
                    │     · git + gh auth setup, scoped to wkdir
                    │     · spawns Claude CLI                  │
                    │     · NO credential storage, NO MCP      │
                    └──────────────────────────────────────────┘
                                                   │
                                                   ▼ (creds via /credentials/refresh)
                                            agent's workspace
```

What **moves out** of asdlc-service: `services.SubmitImplementation`, `services.runPostImplementationPipeline`, the entire `mcp/` package, the explicit `componentSvc.TriggerBuild()` call after Component creation, the four granular status fields and every read/write site for them.

What **moves in** to asdlc-service: the webhook receiver package, the task state projector, a thin `workflowrun_service` that creates `WorkflowRun` CRs with SHA-pinned parameters, and the per-task bearer issuer.

What **moves in** to git-service: the `credentials` resolver package, branch and pull-request services, a webhook registration service, the `/credentials/refresh` route + bearer validator.

What **moves in** to remote-worker: a `workspace` module that handles the credential-helper script, `gh` config scoping via `GH_CONFIG_DIR`, and per-task path layout.

---

## 4. Data model changes

### 4.1 `component_tasks` (asdlc-service Postgres)

```go
// asdlc-service/models/component_task.go (after migration)
type ComponentTask struct {
    ID                string    `gorm:"type:uuid;primaryKey;default:gen_random_uuid()"`
    OrgID             string    `gorm:"index;not null"`
    ProjectID         string    `gorm:"index;not null"`
    ComponentName     string    `gorm:"not null"`
    ComponentType     string
    AppPath           string
    AgentInstructions string    `gorm:"type:text"`
    Port              int

    // GitHub artifacts (1:1 with this task) — set at dispatch.
    IssueURL          string    `gorm:"index"`
    IssueNumber       int
    BranchName        string    `gorm:"not null"`           // NEW: e.g. task/checkout-svc-a4f2c1d8
    PullRequestNumber int                                   // NEW: 0 until PR is opened (then immutable)
    PullRequestURL    string                                // NEW

    // State derived from webhooks. Single field replaces Git/OC/Build/Deploy.
    Status            TaskStatus `gorm:"not null;index"`    // CHANGED: see TaskStatus below
    MergeCommitSHA    string                                // NEW: set on pull_request.closed merged=true
    LastEventAt       time.Time `gorm:"index"`              // NEW: most-recent webhook event time, for janitor

    // Operational metadata.
    Result            *TaskResult `gorm:"type:jsonb"`
    ErrorMessage      string
    DispatchedAt      *time.Time
    CreatedAt         time.Time
    UpdatedAt         time.Time
}

type TaskStatus string

const (
    TaskStatusPending         TaskStatus = "pending"
    TaskStatusInProgress      TaskStatus = "in_progress"
    TaskStatusReadyForReview  TaskStatus = "ready_for_review"
    TaskStatusMerged          TaskStatus = "merged"
    TaskStatusBuilding        TaskStatus = "building"
    TaskStatusDeployed        TaskStatus = "deployed"
    TaskStatusRejected        TaskStatus = "rejected"
    TaskStatusFailed          TaskStatus = "failed"
)
```

Removed fields: `GitStatus`, `OCStatus`, `BuildStatus`, `DeployStatus`, `ErrorStage`. Status field replaces them. Failures land in `Status = "failed"` with `ErrorMessage` populated; the failing stage is identifiable from `LastEventAt` and the audit trail in `webhook_deliveries`.

Migration: `ALTER TABLE` to add new columns and drop old ones, plus a `TRUNCATE component_tasks` in dev (per user agreement). Production migration is N/A — this lands before any production deployment.

### 4.2 `webhook_deliveries` and `webhook_payloads` (new, asdlc-service Postgres)

The dedup row and the raw payload live in **two tables** so a payload-retention sweep can drop the bulk while preserving the dedup history. Without splitting, `webhook_deliveries` would grow unboundedly (a single `pull_request` payload is 30–50 KB; a `push` with many commits is variable).

```go
// asdlc-service/models/webhook_delivery.go
type WebhookDelivery struct {
    DeliveryID    string     `gorm:"primaryKey"`        // X-GitHub-Delivery (UUID); dedup key
    OcOrgID       string     `gorm:"index;not null"`    // resolved at receive time; Phase 0: "platform"
    Event         string     `gorm:"index;not null"`    // X-GitHub-Event
    Action        string     `gorm:"index"`             // payload.action when present
    ReceivedAt    time.Time  `gorm:"index;not null"`
    ProcessedAt   *time.Time
    ProcessError  string
    // No payload here — see WebhookPayload.
}

// asdlc-service/models/webhook_payload.go
type WebhookPayload struct {
    DeliveryID string         `gorm:"primaryKey"`        // FK → webhook_deliveries.delivery_id
    Payload    datatypes.JSON `gorm:"type:jsonb;not null"`
    CreatedAt  time.Time      `gorm:"index;not null"`
}
```

Phase 0 retains all rows. A retention policy lands as part of §9 hardening: payloads older than N days are deleted from `webhook_payloads`; the dedup row stays forever (or with a much longer retention). `OcOrgID` is recorded even though Phase 0 has only one org, so audit queries stay scoped per-org from the first row. Phase 2 fills it in from the routing-key resolution.

`DeliveryID` PK gives free dedup: `INSERT … ON CONFLICT DO NOTHING` and check `RowsAffected`. Phase 0 retains all rows (small volume); a retention policy is a §9 hardening item.

### 4.3 `git_repositories` (git-service Postgres)

Add one column:

```go
// git-service/models/repository.go
type GitRepository struct {
    // ... existing fields ...
    WebhookID  *int64  // GitHub-assigned hook ID (nullable for repos created pre-Phase-0)
}
```

The webhook *secret* is **not** per-repo. Phase 0 uses a single platform-wide secret in BFF env (`GITHUB_WEBHOOK_SECRET`), generated once at platform setup and used for every per-repo registration. This is the single-tenant degenerate case of Phase 2's per-org secrets (evolution-doc §7.6): in Phase 2 each org's credential record holds its own webhook-secret list, and the receiver picks the right list after resolving the event's `ocOrgId` (evolution-doc §4.3 step 2). The Phase 0 receiver short-circuits step 2 to "the platform org" and validates against the single secret. The interface seam is shaped so step 2 becomes a real lookup in Phase 2 without changing call sites elsewhere in the receiver.

### 4.4 GitHub-side artifacts per task

| Artifact | Created at | Owner | Cleanup |
|---|---|---|---|
| Issue | Task generation (existing) | git-service | Closed by webhook handler on PR merged or rejected; janitor closes abandoned |
| Feature branch `task/<slug>-<short-id>` | Task generation (new in Phase 0) | git-service | Deleted on PR merged or rejected (GitHub default), janitor sweeps orphans |
| Draft PR | Task generation (new in Phase 0) | git-service | Closed alongside issue |
| Per-task workspace at `~/asdlc-workspace/<orgId>/<projectId>/<taskId>/` | Dispatch (new layout) | remote-worker | Deleted on terminal status |

Cleanup logic for non-happy-path states is deferred to the janitor follow-up.

---

## 5. HTTP surface changes

### 5.1 BFF (`asdlc-service`)

| Method | Path | Auth | Purpose |
|---|---|---|---|
| `POST` | `/webhooks/github` | HMAC, no JWT | **NEW.** Receive GitHub events. Same mount-pattern as old `/mcp/` (outside JWT middleware). |
| ~~`/mcp/*`~~ | — | — | **REMOVED.** |

Existing console-facing routes are unchanged. Internally, `tasks/dispatch` is rewritten to drive the new flow but its contract with the console is preserved.

### 5.2 git-service

| Method | Path | Auth | Purpose |
|---|---|---|---|
| `POST` | `/api/v1/repos/{projectId}/branches` | internal | **NEW.** Create branch from a base SHA / ref. |
| `POST` | `/api/v1/repos/{projectId}/pulls` | internal | **NEW.** Create draft PR (head, base, title, body). |
| `POST` | `/api/v1/repos/{projectId}/webhooks` | internal | **NEW.** Register webhook for the repo (called once at provision). |
| `DELETE` | `/api/v1/repos/{projectId}/webhooks` | internal | **NEW.** Deregister (used by repo cleanup). |
| `POST` | `/api/v1/credentials/refresh` | per-task bearer | **NEW.** Workspace credential helper endpoint. Returns `{ token, expiresAt, identity }`. |
| `POST` | `/api/v1/repos/{projectId}/issues/{n}/comments` | internal | **EXISTING — kept.** BFF still uses for one-off comments outside the agent flow if needed; agent uses `gh` directly. |

Existing endpoints stay: repo create/get/delete, credentials (legacy `GetCredentials` for OC `GitSecret` provisioning), commit/push/pull (still used by spec/design tag operations on the shared clone), tags, issues create/list/close.

The legacy `POST /commit` and `POST /push` calls from the implementation flow disappear when `runPostImplementationPipeline` is deleted. They survive as endpoints because spec/design tag flow still uses them.

### 5.3 remote-worker

| Method | Path | Purpose |
|---|---|---|
| `POST` | `/dispatch` | **CHANGED.** Body now includes `branchName`, `repoUrl`, `bearer` (per-task), `identity` (`name`, `email`). No `mcpEndpointUrl`. |

---

## 6. Credential resolver — the seam

The resolver lives in git-service and is the **only** place in the codebase that knows what kind of credential is in play.

### 6.1 Interface

```go
// git-service/pkg/credentials/credential.go
package credentials

import (
    "context"
    "time"
)

// Credential is a polymorphic surface over the ways the platform can authenticate to GitHub.
// Phase 0 has one implementation (platform PAT). Phase 2 adds App-installation and per-org user-PAT.
// Callers MUST NOT type-switch on the implementation.
type Credential interface {
    // Token returns a usable GitHub token and the time at which it stops being valid.
    // Long-lived kinds may return time.Time{} (zero) to indicate "never expires" — callers
    // treat zero as "no refresh needed".
    Token(ctx context.Context) (token string, expiresAt time.Time, err error)

    // Identity returns the committer attribution this credential maps to.
    Identity() Identity

    // RepoOwner returns the GitHub org/user login under which new repos are provisioned.
    // App mode: the install's account login. PAT mode: the GitHub org chosen at connect time.
    // Phase 0: the configured GITHUB_REPO_OWNER.
    RepoOwner() string

    // WebhookStrategy says how the platform should arrange event delivery for repos using this credential.
    WebhookStrategy() WebhookStrategy
}

type Identity struct {
    Name  string  // git author name
    Email string  // git author email
    Login string  // GitHub login (for audit)
}

type WebhookStrategy int
const (
    WebhookPerRepo  WebhookStrategy = iota  // register a webhook on each repo at provision
    WebhookPlatform                         // event delivery is platform-wide; do nothing per-repo
)

// Resolver resolves the credential for a given organisation.
// Phase 0 ignores ocOrgID (there is one credential, shared across all orgs);
// Phase 2 looks it up against the per-org connection record. The parameter is
// MANDATORY in Phase 0 even though it's unused — call sites that pass it from
// day one don't need to be revisited when Phase 2 lands.
type Resolver interface {
    Resolve(ctx context.Context, ocOrgID string) (Credential, error)
}
```

### 6.2 Phase 0 implementation

```go
// git-service/pkg/credentials/platform_pat.go
type platformPAT struct {
    token     string
    identity  Identity
    repoOwner string  // from GITHUB_REPO_OWNER
}

func (p *platformPAT) Token(ctx context.Context) (string, time.Time, error) {
    return p.token, time.Time{}, nil  // long-lived
}
func (p *platformPAT) Identity() Identity              { return p.identity }
func (p *platformPAT) RepoOwner() string               { return p.repoOwner }
func (p *platformPAT) WebhookStrategy() WebhookStrategy { return WebhookPerRepo }

// PlatformPATResolver returns the same credential for every org.
type PlatformPATResolver struct {
    cred *platformPAT
}

func NewPlatformPATResolver(token, repoOwner, name, email, login string) *PlatformPATResolver {
    return &PlatformPATResolver{cred: &platformPAT{
        token:     token,
        repoOwner: repoOwner,
        identity:  Identity{Name: name, Email: email, Login: login},
    }}
}
func (r *PlatformPATResolver) Resolve(_ context.Context, _ string) (Credential, error) {
    return r.cred, nil
}
```

### 6.3 Call-site discipline

Every call site in git-service that today reads `cfg.GitHub.PlatformPAT` directly is rewritten to take a `credentials.Resolver`, call `Resolve(ocOrgID)`, then `Token(ctx)`. The `ocOrgID` is sourced from the `GitRepository.OrgID` for repo-scoped operations, or from the request context for org-scoped operations.

Three rules enforce the seam:

- **No call site type-switches** on the `Credential` interface.
- **No call site reads identity, repo-owner, or token from any other source** — not env, not the GitRepository row, not the BFF.
- **Every external GitHub operation passes `ocOrgID`** as an explicit parameter. The resolver refuses an empty `ocOrgID`; this is the multi-tenant invariant that Phase 2 inherits.

#### 6.3.1 git-service call-site checklist

The actual count is **larger than the placeholder**. There are 13 distinct PAT-handling sites in git-service plus 6 GitHub-API Bearer-header constructions plus 6 `GIT_ASKPASS` injections — 25 sites total to audit. Plus 39 gitservice-client call sites in asdlc-service that don't read the PAT but must thread `ocOrgID` (or rely on `projectID → GitRepository.OrgID` derivation) so git-service can resolve.

**git-service — convert every site below to `Resolver.Resolve(ocOrgID).Token(ctx)`:**

```
config + bootstrap (do NOT route through resolver — config layer):
  config/config_loader.go:31        env GITHUB_PLATFORM_PAT
  config/config_loader.go:32        env GITHUB_REPO_OWNER
  cmd/git-service/main.go:52,53,57,58   inject into service constructors

GitHub API Bearer headers (all in services/github_client.go):
  :95   CreateOrgRepo            POST /orgs/{org}/repos
  :141  CreateIssue              POST /repos/{owner}/{repo}/issues
  :183  EnsureLabel              POST /repos/{owner}/{repo}/labels
  :215  CloseIssue               PATCH /repos/{owner}/{repo}/issues/{n}
  :246  CommentIssue             POST /repos/{owner}/{repo}/issues/{n}/comments
  :275  ListIssues               GET /repos/{owner}/{repo}/issues

IssueService PAT injection (services/issue_service.go):
  :58, :64, :81, :92, :97, :110

RepoService PAT injection (services/repo_service.go):
  :103  CreateRepo → CreateOrgRepo
  :141  embeds PAT in RepoCredentials response struct (NOT GitHub-bound — see flag below)
  :188  CloneRepo → createAskPassScript
  :197  GIT_ASKPASS for clone

GitOpsService GIT_ASKPASS (services/git_ops_service.go):
  :119, :265, :319, :424, :486    EnsureCloned, Commit, Push, Pull, Status
```

**Two non-GitHub uses to flag and handle separately:**

- `services/repo_service.go:141` and `services/task_service.go:514` (asdlc-service) — the `GetCredentials` flow that returns the platform PAT to asdlc-service for OC `GitSecret` provisioning. This is a bridge that goes away in Phase 2 (replaced by OpenBao + `SecretReference` per evolution-doc §6.3.1). For Phase 0: keep the call but route the PAT read through the resolver so the audit checklist is uniform; in Phase 2 the call is removed entirely.

**asdlc-service — does not handle tokens but must thread `ocOrgID` through every gitservice client call.** The 39 sites are in:

- `services/spec_service.go` — 12 call sites (ListTags, GetFileAtTag, Commit, Push, CreateTag, etc.) — spec/design tag operations on the shared clone
- `services/design_service.go` — 14 call sites — same shape as spec
- `services/task_service.go` — 11 call sites (issue, comment, close, commit, push, get-credentials, list-tags, create-tag, create-issue)
- `services/project_service.go` — 6 call sites (create/get/delete repo, list-tags)

Most of these take `projectID` and git-service derives `ocOrgID` from `GitRepository.OrgID`. The exception is org-scoped operations (e.g. `CreateRepo`, which is the first call for a new project) — those take `ocOrgID` explicitly.

A simple `staticcheck`-style lint (or just code review discipline) prevents new direct PAT reads outside the bootstrap path.

### 6.4 Webhook secret provider — a second seam for Phase 2

The webhook secret is **not** a GitHub credential, but it is a security secret, and Phase 2 stores it on the per-org credential record (evolution-doc §7.6). The Phase 0 placement (BFF env) is not the single-tenant degenerate of that — they live in different services. To make the Phase-0-to-Phase-2 swap a real fill-in, introduce the provider interface in Phase 0 with one trivial implementation:

```go
// asdlc-service/services/webhook/secrets.go
type SecretProvider interface {
    // Secrets returns the current accepted HMAC keys for the given org, ordered current-first.
    // Phase 0 ignores ocOrgID; Phase 2 looks it up via git-service.
    Secrets(ctx context.Context, ocOrgID string) ([][]byte, error)
}

// Phase 0:
type EnvSecretProvider struct{ secret []byte }
func (p *EnvSecretProvider) Secrets(_ context.Context, _ string) ([][]byte, error) {
    return [][]byte{p.secret}, nil
}
```

Phase 2 swaps to a `GitServiceSecretProvider` that calls `git-service GET /internal/credentials/orgs/{ocOrgID}/webhook-secrets` and caches the result for ~30 s. The receiver code path is identical: parse routing key → resolve `ocOrgID` → `provider.Secrets(ocOrgID)` → HMAC-validate against any of the returned secrets → continue.

**Cache miss-then-refetch on HMAC mismatch.** A 30-s cache combined with a static-list HMAC check creates a rotation hole: when an org rotates secrets, the receiver's cache holds the old list and the sender (GitHub) starts signing with the new one — every event in the cache window fails HMAC, gets 5xx'd, and GitHub redelivers hot. Fix: on HMAC mismatch against the cached list, the provider re-fetches *before* returning a 401 result. If the refetched list also fails, then 401. Sequence: `Secrets(ctx, orgID, opts={"force": false}) → cached || fetch+cache; if validate fails, Secrets(ctx, orgID, opts={"force": true}) → fetch fresh; if still fails, 401`. This makes rotation propagate within one event, not within one cache window.

### 6.5 OpenBao access wrapper — Phase 2 architectural enforcement

Phase 0 does not need OpenBao. Phase 2 introduces it as the storage for encrypted PATs and as the backing for `SecretReference` CRs (evolution-doc §6.3.1, §9.10). The doc's deliberate decision to use a single OpenBao policy with path-namespaced isolation (`secret/asdlc/{ocOrgID}/...`) places the per-org isolation property entirely on git-service code correctness — exactly what the multi-tenant invariant says it shouldn't.

The architectural enforcement is a **wrapper that's the only OpenBao access point**, with `ocOrgID` mandatory in every method:

```go
// git-service/pkg/credentials/openbao_store.go (Phase 2 introduction; named in Phase 0 for forward-shape)
type OpenBaoStore interface {
    Get(ctx context.Context, ocOrgID, key string) ([]byte, error)
    Put(ctx context.Context, ocOrgID, key string, value []byte) error
    Delete(ctx context.Context, ocOrgID, key string) error
}

// Implementation builds the path internally:
func (s *openBaoStore) path(ocOrgID, key string) string {
    if ocOrgID == "" { panic("ocOrgID required") }
    return fmt.Sprintf("secret/asdlc/%s/%s", ocOrgID, key)
}
```

Rules: **no call site touches the OpenBao client directly.** No raw paths. No string concatenation that builds a path outside `openBaoStore.path()`. A test (or a `go vet` analyzer) fails the build if any code outside the `credentials` package imports the OpenBao SDK. This makes "every read/write is parametrised by `ocOrgID`" a compile-time property, not a discipline. Phase 0 declares the contract and the Phase 2 PR introduces the implementation.

---

## 7. Workspace credential setup

The agent runs `git` and `gh` inside its per-task workspace. Neither tool gets the token via process env (env leaks to transcripts). Both use **filesystem-scoped credential storage** that the remote-worker writes at workspace setup, refreshed by the credential helper for `git` and by an explicit `gh auth` call for `gh`.

### 7.1 At dispatch (in remote-worker)

```
1. Compute paths:
   workspace  = ~/asdlc-workspace/<orgId>/<projectId>/<taskId>/
   ghConfig   = <workspace>/.gh-config
   bearerFile = <workspace>/.asdlc/bearer            (chmod 600)
   helperBin  = <workspace>/.asdlc/credhelper.sh     (chmod 700)
   ghWrapper  = <workspace>/.asdlc/gh                (chmod 755, on PATH)
2. rm -rf <workspace>                                  # idempotent — retry-safe (see §12 dispatch ordering)
3. Fetch initial credential:
   POST git-service /api/v1/credentials/refresh
     headers: Authorization: Bearer <per-task-bearer>
     body: { ocOrgId, taskId }
     response: { token, expiresAt, identity: {name, email, login} }
4. Write bearerFile with the per-task bearer (NOT in env — bearer is itself a credential).
5. Write credhelper.sh:
     #!/usr/bin/env bash
     bearer=$(cat "$ASDLC_BEARER_FILE")
     resp=$(curl -sS -H "Authorization: Bearer $bearer" \
              "$ASDLC_GIT_SERVICE_URL/api/v1/credentials/refresh")
     token=$(echo "$resp" | jq -r .token)
     echo "username=x-access-token"
     echo "password=$token"
6. Write ghWrapper (a 30-line preflight that refreshes hosts.yml then execs real gh):
     #!/usr/bin/env bash
     bearer=$(cat "$ASDLC_BEARER_FILE")
     resp=$(curl -sS -H "Authorization: Bearer $bearer" \
              "$ASDLC_GIT_SERVICE_URL/api/v1/credentials/refresh")
     token=$(echo "$resp" | jq -r .token)
     login=$(echo "$resp" | jq -r .identity.login)
     mkdir -p "$GH_CONFIG_DIR"
     cat > "$GH_CONFIG_DIR/hosts.yml" <<EOF
     github.com:
       oauth_token: $token
       user: $login
       git_protocol: https
     EOF
     exec /usr/local/bin/gh "$@"
7. git clone --branch <branchName> <repoUrl> <workspace>
   (clone uses GIT_ASKPASS=credhelper.sh, or credential.helper configured below)
8. Inside <workspace>/.git/config:
     [user]
       name  = <identity.name>
       email = <identity.email>
     [credential "https://github.com"]
       helper = <workspace>/.asdlc/credhelper.sh
9. Spawn Claude CLI with:
     cwd  = <workspace>
     PATH = <workspace>/.asdlc:$PATH               # so `gh` resolves to ghWrapper
     env  =
       GH_CONFIG_DIR        = <ghConfig>
       ASDLC_BEARER_FILE    = <bearerFile>
       ASDLC_GIT_SERVICE_URL= http://git-service:3300
       (NEITHER token NOR bearer in env)
```

**Why this shape:**

- **No credentials in env.** Both the GitHub token and the per-task bearer live on disk inside the workspace, chmod-restricted. Env carries only file paths and the service URL — pointers, not secrets. This holds the rule "tokens never in env" uniformly.
- **`git` refresh-on-use** via `credhelper.sh`. Phase 0's static token makes this trivial; Phase 2's short-lived App tokens use the same code path with no change.
- **`gh` refresh-on-use** via `ghWrapper`. Every `gh` invocation rewrites `hosts.yml` with a fresh token before exec'ing the real binary. Phase 0 doesn't strictly need this (the token is long-lived) but shipping it now means Phase 2's hour-TTL App tokens work transparently — a long-running task that calls `gh` after 60 minutes gets a fresh token, not a 401. The alternative (a daemon) was deferred; the wrapper subsumes it.
- **Identity drift handling.** The credhelper response carries `identity` — when an org's PAT is replaced and identity changes (evolution-doc §6.3 Replace), the next call to `ghWrapper` rewrites `hosts.yml` with the new login, and the workspace's `.git/config` user.name/email is updated by a small `update-git-identity` helper invoked from `credhelper.sh` when the response identity differs from the current `.git/config`. Past commits in the task keep their original attribution; future commits use the new identity. This is best-effort: a task mid-`git commit` won't have its in-flight commit re-authored.
- **Per-task bearer is a credential, treated as one.** Storage rule: workspace disk, chmod 600, never logged. Lifecycle: issued at dispatch with TTL ≤ task budget (24 h hard maximum, evolution-doc §15 q1); revoked by the bearer service when the task reaches a terminal state; revoked on platform restart for any task already in a terminal state (the dispatch-resume sweep clears stale bearers).
- **Remote-worker host isolation.** The bearer file's chmod 600 only protects against same-host non-root users. Phase 0 runs all per-task workspaces under **the same OS user** on a single remote-worker process (one host per platform deployment). Cross-task escalation between agents on the same host is **not prevented by the file mode** — a prompt-injected agent in task A can read task B's `bearerFile` if it knows the path. Phase 0 accepts this in dev environments; production deployments using remote agents need either per-task UIDs (Linux user-namespace isolation, run each Claude CLI as a different user) or container-per-task isolation. State this in deployment docs alongside the §7.3 "dev-environment only" gate.

### 7.2 Allowed Claude CLI tools

`remote-worker/src/lib/runner.ts` allowed-tools list becomes:

```ts
[
  "Read", "Write", "Edit",
  "Bash",          // git, gh, build/test/lint commands the agent picks
  "Glob", "Grep",
]
```

`mcp__asdlc__report_progress` and `mcp__asdlc__submit_implementation` removed. No MCP tools registered.

### 7.3 Agent prompt changes

The issue body (built by `asdlc-service/services/issue_body.go`) is updated:

- Drop the "call `mcp__asdlc__submit_implementation` when done" instruction.
- Add:
  - "Your working directory has `git` and `gh` configured for this repo. Branch `<branchName>` is already checked out."
  - "Post progress as comments on this issue: `gh issue comment <number> --body "..."`."
  - "When implementation is complete, push your branch and run `gh pr ready <prNumber>`. Do not merge; review and merge are human gates."
- **Constraints (do not do these):**
  - Do not push to any branch other than `<branchName>`. Do not force-push.
  - Do not run `gh pr merge`, `gh pr close`, `gh repo create`, `gh repo delete`, `gh repo fork`, or `gh repo edit`.
  - Do not delete remote branches (`git push --delete`, `git push :branch`).
  - Do not modify branch protection, secrets, settings, collaborators, or webhooks.
  - Do not interact with repos other than this one.

**Honest framing about blast radius.** These are **prompt-level** instructions, not security boundaries. The platform PAT used in Phase 0 has broad reach across the configured GitHub org (per-org PAT scopes lock this down in Phase 2). A prompt-injected agent or a buggy agent that ignores the constraints can do anything the PAT permits. The Phase 0 posture (evolution-doc §2) accepts this trade in exchange for removing server-mediation; branch protection (deferred §1 follow-up) closes the most damaging holes (force-push to default, direct push to default). Until that follow-up lands, Phase 0 is **dev-environment only** — do not point a Phase 0 deployment at a GitHub org that hosts non-platform code.

**Operational gate** (not just prose). The BFF reads a `DEPLOYMENT_TIER` env var (`dev` | `staging` | `production`) at startup. If `tier != dev` and the per-org credential record's `kind == "platform-pat"` (Phase 0 single-tenant), the BFF refuses to start with `FATAL: platform-pat credential kind is dev-only; production deployments must use Phase 2 per-org credentials`. The console renders a persistent banner "Phase 0 — branch protection not enforced — dev only" when tier is `dev`. This is a fail-fast gate, not policy: the only way around it is editing config, which makes the choice explicit.

---

## 8. Webhook receiver design

### 8.1 Endpoint

```
POST /webhooks/github
  Headers:
    X-Hub-Signature-256: sha256=<hex>
    X-GitHub-Event:      <event-name>
    X-GitHub-Delivery:   <uuid>
  Body: GitHub event JSON
```

Mounted in `asdlc-service/api/app.go` outside JWT middleware (same pattern the now-removed `/mcp/` mount used).

### 8.2 Pipeline

```
read raw body
   │
   ▼
parse routing key                                # installation.id (App, Phase 2)
   │                                             # | repository.full_name (PAT) | "platform" (Phase 0)
   ▼
resolve ocOrgId                                  # Phase 0: returns "platform" without lookup
   │
   ▼
HMAC-validate against secrets for that org      # 401 on mismatch (Phase 0: list of one)
   │
   ▼
INSERT INTO webhook_deliveries (delivery_id, oc_org_id, event, action, received_at, ...)
ON CONFLICT (delivery_id) DO NOTHING
   │
   ├─ row existed AND processed_at IS NOT NULL ─▶ ack 200 (replay of finished work)
   │
   ▼  (row freshly inserted, OR row existed with processed_at IS NULL)
route by (event, action) → handler                # synchronous in Phase 0
   │                                              # handler resolves task/project,
   │                                              # acquires its lock (see below),
   │                                              # then runs projector
   ├─ success ─▶ UPDATE deliveries SET processed_at = now(), process_error = NULL
   │             ack 200
   │
   └─ failure ─▶ UPDATE deliveries SET process_error = err
                 ack 5xx                          # GitHub redelivers; on redelivery the row exists
                                                  # but processed_at is null → re-process
```

Two corrections from earlier drafts:

- **Acks track success.** Acking 200 on a processing error forfeits GitHub's redelivery — exactly the safety net the dedup table was supposed to enable. Phase 0 acks 5xx on processing failure. GitHub redelivers up to ~9 hours; on each redelivery the dedup row already exists but `processed_at IS NULL`, so the handler runs again. This makes the system self-healing across deploys at the cost of a broken handler retrying hot for the redelivery window. The trade is right: silent loss is worse than retry noise.
- **Locks live in the per-event handlers, not at receive time.** The lock key isn't known at the receiver — `pull_request` events resolve to a task ID via `(repo, pr.number)`, `push` events resolve to a project ID, `installation` events have no task or project at all. Each handler computes its own lock scope:
  - `pull_request.*` handlers: `pg_advisory_xact_lock(hashtext('task:' || task_id))` after looking up the task.
  - `push` handler: `pg_advisory_xact_lock(hashtext('project:' || project_id))` so two concurrent pushes to default for the same project serialise.
  - `installation.*` handlers: `pg_advisory_xact_lock(hashtext('org:' || oc_org_id))`.
  - `issue_comment`, no-state events: no lock needed.
  Locks are held for the lifetime of the handler's transaction and released on commit. Sufficient for Phase 0's single BFF replica; multi-replica hardening (evolution-doc §9.3) inherits this scheme unchanged because Postgres advisory locks are cluster-wide.

The two routing steps (parse routing key, resolve `ocOrgId`) are constants in Phase 0 — the routing data backing them (an `installations` table for App-mode, the `git_repositories` reverse lookup for PAT-mode) is genuinely a Phase 2 introduction, not a Phase 0 fill-in. The pipeline *shape* is preserved; the *data* is not yet there. The honest framing: Phase 2 fills in the resolution function; the rest of the pipeline including dedup, locking, ack semantics is unchanged. The `webhook_deliveries` row stores the resolved `ocOrgId` so audit reads stay scoped per-org from day one.

### 8.3 Event routing (Phase 0)

All `pull_request` event handlers look up the task by `(repo, pr.number) == ComponentTask.PullRequestNumber` — never by branch name. Branch is decorative; the PR number is the durable key. A human who happens to push a branch matching `task/...` and opens a PR is therefore correctly ignored.

| Event | Action | Handler effect |
|---|---|---|
| `pull_request` | `opened` | If `pr.number` matches a `ComponentTask.PullRequestNumber`, no-op (we created it). Otherwise persist for audit and ignore. |
| `pull_request` | `ready_for_review` | Look up task by `pr.number`. Advance `in_progress → ready_for_review`. |
| `pull_request` | `closed` (merged=true) | Look up task. Resolve merge SHA (see below). Advance `* → merged`. |
| `pull_request` | `closed` (merged=false) | Look up task. Advance `* → rejected`. Workspace cleanup queued. |
| `pull_request` | `reopened` | Phase 0: ignore. The task is in `rejected` (terminal) — re-dispatch is the supported path, not reopen. The grace-period reopen flow is a follow-up alongside `superseded` semantics. |
| `push` | — | See §8.4 for the matching and trigger rules. |
| `issue_comment` | `created` | Persist for audit. No state effect. (Console may surface in UI later.) |
| All others | — | Persisted; no-op. |

**Merge-SHA resolution.** `pr.merge_commit_sha` is sometimes null for ~1–2 seconds after `merged=true` returns — GitHub computes the merge commit asynchronously. The handler:

1. If `pr.merge_commit_sha` is non-null: use it.
2. If null: hit `GET /repos/{owner}/{repo}/pulls/{n}` once; use the field from the response.
3. If still null: persist task with `MergeCommitSHA = ""` and rely on the subsequent `push` event to backfill (see §8.4 backfill rule).

The projector lives in `asdlc-service/services/webhook/projector.go`. It is a single function `Apply(ctx, tx, event)` that reads task state under the per-task advisory lock from §8.2, computes the new state, writes atomically. Transition validation lives next to the state enum in `asdlc-service/services/task_state.go`.

### 8.4 Build trigger flow (push → WorkflowRun)

This is the only spot that diverges from agent-manager's exact code path because we trigger from a webhook, not from a console action. The shape converges. The handler runs three steps on every `push` event whose `ref == refs/heads/{defaultBranch}`:

**Step 1 — Reconcile task state.** The push payload includes `commits[]` and `head_commit.id`. For each task with `Status = merged` that has either:
- `MergeCommitSHA` matching any `commits[].id`, OR
- `MergeCommitSHA == ""` (null-resolution backfill case from §8.3 step 3), in which case match by `pull_request.head.ref == BranchName` and the push containing the PR's last-known head commit,

advance `merged → building`. Matching against the full `commits[]` list — not just `head_commit.id` — handles squash, rebase, and merge-commit strategies uniformly: in every strategy, the merge SHA is somewhere in the push's commit list as long as no unrelated push interleaved.

**Step 2 — Determine which components to build.** Naive "rebuild everything" wastes builds and breaks horizontally as projects grow. The handler filters:

```
changedPaths = unique(flatten(push.commits[].added, modified, removed))
if len(changedPaths) >= 3000 OR push.commits truncated:
    try: changedPaths = GET /repos/{owner}/{repo}/compare/{push.before}...{push.after} → files[].filename
    if compare response truncates (commits > 100 OR files > 300):
        # Pessimistic fallback: GitHub will not tell us all changed paths.
        componentsToBuild = all components in project
        return  # skip the path-filter, build everything
componentsToBuild = [
    c for c in components_in_project
    if any path in changedPaths starts with c.AppPath
]
```

The 3000-file ceiling is GitHub's documented push-payload limit; `compare` API is the first fallback (paginated, capped at 300 files / 100 commits per page). For pushes that exceed `compare`'s limits — a release-merge or a large refactor — the handler **builds every component in the project**. Wasted builds are the right failure mode here: under-building (skipping a component that actually changed) silently ships stale code; over-building wastes build time but is correct. Components whose `AppPath` matches no changed path are skipped — this is the "skip irrelevant paths (e.g. docs-only diffs)" affordance evolution-doc §4.4 names; the pessimistic-fallback case forfeits that affordance to preserve correctness.

**Step 3 — Create WorkflowRuns** for `componentsToBuild` only, skipping any whose `LastBuildSHA == sha`:

```go
// asdlc-service/services/workflowrun_service.go
func (s *workflowRunService) TriggerForPush(ctx context.Context, projectID, sha string, components []Component) error {
    for _, c := range components {
        if c.LastBuildSHA == sha {
            continue  // already built this exact SHA — re-push, force-push, or pessimistic-fallback duplicate
        }
        runName := fmt.Sprintf("%s-%d", c.Name, time.Now().UnixMilli())
        if err := s.ocClient.CreateWorkflowRun(ctx, c.OcOrgID, &openchoreo.WorkflowRun{
            Name:          runName,
            ComponentName: c.Name,
            Parameters: map[string]any{
                "repository": map[string]any{
                    "url":       c.RepoURL,
                    "secretRef": c.SecretRefName,
                    "appPath":   c.AppPath,
                    "revision": map[string]any{
                        "branch": c.DefaultBranch,
                        "commit": sha,                    // ← SHA injection (agent-manager parity)
                    },
                },
                "docker": c.DockerParams,
            },
        }); err != nil {
            // log; continue with other components — partial failure is recoverable via re-trigger
            continue
        }
        s.componentRepo.UpdateLastBuildSHA(ctx, c.ID, sha)  // persist for the next trigger's idempotency
    }
    return nil
}
```

The `LastBuildSHA` check is what makes the pessimistic-fallback (step 2) and admin re-pushes idempotent: if every component is over-built once, the same SHA arriving again skips them all. `Component.LastBuildSHA` is a new column on the OC component-record, populated here and read by the build-watcher sweep (§9) for log attribution.

**Unmatched-push policy.** Pushes to default that don't correspond to a merged task (force-push by an admin, manual hotfix, branch protection bypass) still trigger builds for components whose `AppPath` matches changed files — the build is the source of truth for "what's running in production," not the task lifecycle. The build's `WorkflowRun` has no `ComponentTask` to attribute to; that's intentional. Branch protection (deferred follow-up per §1) prevents most of these in production environments.

`Component.AutoBuild` flips to `false` in `task_service.createOCComponentWithWorkflow`. The current explicit `componentSvc.TriggerBuild()` call (`task_service.go:572`) is removed — the first build now runs only on first push to default, which happens when the first task's PR merges. Components created without any prior merge sit dormant until then; this is correct behavior (no code → no build).

---

## 9. Task state projector

```go
// asdlc-service/services/task_state.go
type stateTransition struct {
    From  TaskStatus
    To    TaskStatus
    Event string  // "pr.ready_for_review", "pr.merged", "pr.rejected", "push.matched", "build.succeeded", "build.failed"
}

var allowedTransitions = []stateTransition{
    {TaskStatusPending,        TaskStatusInProgress,     "dispatch.success"},
    {TaskStatusInProgress,     TaskStatusReadyForReview, "pr.ready_for_review"},
    {TaskStatusReadyForReview, TaskStatusMerged,         "pr.merged"},
    {TaskStatusReadyForReview, TaskStatusRejected,       "pr.rejected"},
    {TaskStatusInProgress,     TaskStatusRejected,       "pr.rejected"},
    {TaskStatusMerged,         TaskStatusBuilding,       "push.matched"},
    {TaskStatusBuilding,       TaskStatusDeployed,       "build.succeeded"},
    {TaskStatusBuilding,       TaskStatusFailed,         "build.failed"},
}
```

**Webhook arrival order is not guaranteed** (evolution-doc §4.3, §9.2). The push for a merge can arrive before `pull_request.closed merged=true`. The push and PR handlers hold different advisory-lock scopes (push: project; PR: task per §8.2 N2 fix), so a push handler that wrote to task rows would race against a concurrent PR handler. The clean solution is **project-scoped state, not cross-handler task-row writes:**

```go
// asdlc-service/models/project_default_push.go
type ProjectDefaultPush struct {
    ProjectID string    `gorm:"primaryKey"`           // composite PK
    SHA       string    `gorm:"primaryKey"`           // composite PK
    PushedAt  time.Time `gorm:"index;not null"`
    BuiltAt   *time.Time                              // when WorkflowRuns were created (idempotency for §8.4 step 3)
}
```

- The **push handler** (project lock) does two things: (1) creates `WorkflowRun`s with the SHA-pinned spec from §8.4, idempotent on `(componentName, sha)`; (2) inserts `(project_id, sha)` into `project_default_pushes`. It does **not** touch any task row. No task lock needed.
- The **PR handler** (task lock) does the merge transition: on `pr.merged`, it advances `* → merged`, records `MergeCommitSHA`, then queries `project_default_pushes` for a row with `SHA == MergeCommitSHA`. If found, the matching push has already been processed; the projector advances `merged → building` immediately within the same task-lock transaction.

This keeps the transition table strictly linear, the lock ordering one-scope-per-handler, and out-of-order arrivals deterministic. `project_default_pushes` is also useful for audit and for the janitor's "no push observed for a merged task" detection (deferred follow-up).

Terminal states: `deployed`, `rejected`, `failed`. Late events on terminal states are ignored (evolution-doc §7.2). The projector validates transitions and refuses unknown ones; this catches event reordering bugs early.

`build.succeeded` / `build.failed` transitions are driven by polling the `WorkflowRun` status from OC. The polling is **a single periodic sweep**, not per-build goroutines. A goroutine-per-build would die on BFF restart and leave tasks stuck in `building` forever; the sweep is restart-safe by construction.

```go
// asdlc-service/services/build_watcher.go (runs on a 10-second tick)
SELECT id, oc_org_id, project_id, component_name, last_build_run_name
  FROM component_tasks
 WHERE status = 'building'
   FOR UPDATE SKIP LOCKED                                  // multi-replica safe
 LIMIT 50;

for each task:
    workflowRun ← s.ocClient.GetWorkflowRun(task.OcOrgID, task.LastBuildRunName)
    if workflowRun.Status == "Succeeded": projector.Apply(task, "build.succeeded")
    if workflowRun.Status == "Failed":    projector.Apply(task, "build.failed")
    // running / pending: leave for next tick
```

`LastBuildRunName` is persisted on the task when the `push` handler creates the `WorkflowRun` (§8.4 step 3). On BFF restart, the next sweep tick picks up every `building` task without any in-memory state. `FOR UPDATE SKIP LOCKED` lets multiple BFF replicas share the work without coordination — the §9.3 hardening item naturally satisfied here. Sweep cadence trades latency vs. OC API load; 10 s is the agent-manager precedent and a fine starting point.

---

## 10. Code organisation (post-Phase-0)

```
asdlc-service/
├── api/
│   ├── app.go                          # CHANGED: drop /mcp/ mount, add /webhooks/github mount
│   ├── webhook_routes.go               # NEW
│   └── ... (existing routes)
├── controllers/
│   └── webhook_controller.go           # NEW
├── services/
│   ├── webhook/                        # NEW package
│   │   ├── verifier.go                 # HMAC validation, secret list (Phase 0: list of 1)
│   │   ├── deliveries.go               # webhook_deliveries persistence + dedup
│   │   ├── router.go                   # event-type dispatch
│   │   └── projector.go                # event → ComponentTask state transition
│   ├── workflowrun_service.go          # NEW: BFF-driven WorkflowRun creation, SHA-pinned
│   ├── task_state.go                   # NEW: state enum + transition table
│   ├── task_dispatch_service.go        # CHANGED (renamed from inline dispatch logic): orchestrates issue + branch + draft PR + workspace + remote-worker call
│   ├── task_service.go                 # SHRUNK: SubmitImplementation, runPostImplementationPipeline, createOCComponentWithWorkflow's TriggerBuild call all removed
│   ├── issue_body.go                   # CHANGED: agent prompt updated, no MCP refs
│   ├── remote_worker_service.go        # CHANGED: dispatch payload includes branch + bearer + identity, no mcpEndpointUrl
│   ├── bearer_service.go               # NEW: per-task JWT issuance + verification
│   └── ... (existing)
├── models/
│   ├── component_task.go               # CHANGED: new fields, status enum, removed Git/OC/Build/Deploy
│   └── webhook_delivery.go             # NEW
└── (asdlc-service/mcp/ DELETED)
```

```
git-service/
├── api/
│   ├── repo_routes.go                  # CHANGED: + branches, pulls, webhooks, credentials/refresh
│   ├── credentials_routes.go           # NEW (or inline above)
│   └── middleware/
│       └── task_bearer.go              # NEW: validates per-task JWT for /credentials/refresh
├── pkg/
│   └── credentials/                    # NEW package
│       ├── credential.go               # interfaces + types
│       ├── platform_pat.go             # Phase 0 implementation
│       └── resolver.go                 # PlatformPATResolver
├── services/
│   ├── github_client.go                # CHANGED: takes credentials.Resolver, calls Resolve(orgID).Token()
│   ├── repo_service.go                 # CHANGED: also calls webhook_service.Register on provision
│   ├── issue_service.go                # CHANGED: removes platformPAT param, takes resolver
│   ├── branch_service.go               # NEW
│   ├── pull_request_service.go         # NEW
│   ├── webhook_service.go              # NEW: register/unregister webhook on GitHub
│   └── ... (existing)
├── models/
│   └── repository.go                   # CHANGED: + WebhookID
```

```
remote-worker/
├── src/
│   ├── routes/
│   │   └── dispatch.ts                 # CHANGED: payload includes branch, bearer, identity
│   ├── lib/
│   │   ├── runner.ts                   # CHANGED: allowed-tools list (no MCP), env (no mcpEndpointUrl)
│   │   ├── workspace.ts                # NEW: clone + git/gh credential setup
│   │   └── credhelper.ts               # NEW: writes credhelper.sh into workspace
│   └── plugin/                         # unchanged
```

```
console/
└── src/
    ├── pages/
    │   ├── ProjectTasksPage.tsx        # CHANGED: status enum, render new lifecycle
    │   └── ComponentBuildPage.tsx      # CHANGED: derive build status from task.Status
    └── services/api/
        └── types.ts                    # CHANGED: TaskStatus enum, drop GitStatus/OCStatus/etc.
```

---

## 11. Maintainability principles for this refactor

These are the rules that make Phase 2 additive instead of a rewrite. Hold the line on them in code review.

1. **Org-scope, project-scope, component-scope, task-scope are explicit in code.** The `Resolver.Resolve(ocOrgID)` signature, the `ComponentTask.OrgID + ProjectID` keys, the `Component` per-(project, name) idempotency, and the per-task workspace path layout each name their scope. No implicit "current org from env" reads outside `PlatformPATResolver` and config bootstrapping.

2. **Every external operation passes `ocOrgId`.** This is the multi-tenant invariant. Repo provisioning, issue creation, branch creation, PR creation, webhook registration, credential refresh, OC `Component` / `WorkflowRun` / `SecretReference` creation — all take `ocOrgId` as an explicit parameter, not as an ambient context value. The resolver and the OC client both refuse empty `ocOrgId`. Phase 0 has one org so the parameter is constant in practice; Phase 2 makes it variable without touching call sites.

3. **git-service is the credential boundary** with **one named legacy exception** that disappears in Phase 2. Audit rule: no `Authorization: Bearer` headers built outside `git-service/services/github_client.go`. No GitHub API calls outside git-service. The BFF and remote-worker are credential-blind by construction. **Exception:** the `GetCredentials` flow (`asdlc-service/services/task_service.go:514`) currently retrieves the platform PAT cleartext from git-service to provision an OC `GitSecret` — the BFF doesn't *call GitHub* with the PAT but it does *hold* the PAT briefly. This is the only Phase 0 site outside git-service that touches a GitHub credential, and it is retired wholesale in Phase 2 when OC `GitSecret` is replaced by OpenBao + `SecretReference` (evolution-doc §6.3.1). Tag the call site `// PHASE-2-REMOVE: legacy bridge, GitSecret → SecretReference migration` so the audit checklist clears in one named place.

4. **No call site branches on credential kind.** The `credentials.Credential` surface is the only place implementations differ. Type assertions on the interface are a code-review red flag.

5. **State transitions are declarative.** The transition table in `task_state.go` is the source of truth. The projector validates against it; anywhere else that wants to write a state transition uses the same `Apply` function. Direct `task.Status = ...` writes outside the projector are a code-review red flag.

6. **GitHub is the bus.** No dual-write paths where the agent updates platform state via a non-GitHub channel (no MCP, no platform REST). If a feature wants to surface agent intent, it goes through `gh issue comment` or PR state, observed via webhook.

7. **One handler, one event.** Webhook handlers are pure functions of `(currentState, event) → newState + sideEffects`. Side effects are explicit (create WorkflowRun, enqueue cleanup). No hidden state mutation.

8. **Idempotency keys are stable and obvious.** Every external side-effect operation names its key in code. Re-dispatch and webhook redelivery are first-class scenarios, not edge cases.

9. **Phase 2 plumbing exists from day one.** The `Resolver` interface, the `/credentials/refresh` endpoint, the workspace credential helper, the per-task bearer, the routing-key + ocOrg-resolution steps in the webhook pipeline, and the `WebhookDelivery.OcOrgID` column are all in the Phase 0 codebase even though they're trivial here. Phase 2 fills them in; it does not introduce them.

---

## 12. Migration plan

### 12.1 Dispatch ordering (idempotency contract)

A dispatch creates several artifacts — one DB row, one GitHub issue, one GitHub branch, one GitHub PR, one workspace — none of which is in a transaction with the others. A crash mid-sequence must leave the system in a state from which re-dispatch is safe. The contract:

```
Step  Action                                                   Persisted column on resume-check
────  ───────────────────────────────────────────────────────  ─────────────────────────────────
 1    INSERT component_tasks (status='pending', no GH fields)  task row exists by ID
 2    git-service: create issue                                IssueNumber IS NOT NULL
 3    git-service: create branch task/<slug>-<short-id>        BranchName IS NOT NULL
 4    git-service: create draft PR                             PullRequestNumber IS NOT NULL
 5    remote-worker: rm -rf workspace, then clone+configure    (no resume-check column;
      then UPDATE component_tasks SET DispatchedAt = now()      DispatchedAt set ONLY after
                                                                clone+configure succeeds)
 6    UPDATE component_tasks SET status='in_progress'          status = 'in_progress'
```

The `DispatchedAt` write happens **after** `git clone` and credential configuration succeed, not before. This is the key idempotency property for step 5: a crash mid-clone leaves `DispatchedAt = NULL`, so the resume sweep re-enters step 5, which begins with `rm -rf` — the partial workspace from the previous attempt is wiped before re-cloning. If we set `DispatchedAt` before clone, a crash mid-clone would short-circuit the resume past a half-provisioned workspace and the next agent run would fail on missing files.

Re-dispatch re-runs each step in order; each step short-circuits if its persisted column is already set. The git-service operations (steps 2–4) are themselves idempotent on `(repo, kind, key)`:

- Issue create: skip if an open issue with `task-<id>` label already exists; return its number.
- Branch create: skip if branch with the deterministic name already exists; return its tip SHA.
- Draft PR create: skip if a PR exists for `head=<branchName>`; return its number.

This makes step 1's "task row first" rule load-bearing: without the row, there's no key to deduplicate against on retry. **Never call any external GitHub API before the task row is persisted.**

### 12.2 Landing order

The chunk lands as one PR sequence (not necessarily one commit). Suggested order so each commit compiles and runs:

1. **Schema migrations** (in `asdlc-service/database/migrations/`): add `component_tasks` columns, drop old ones; create `webhook_deliveries` table; add `git_repositories.webhook_id`. Wipe `component_tasks` rows in the dev DB.
2. **Resolver scaffold** in git-service: new `pkg/credentials` package, swap `issue_service`, `github_client`, etc. to take `Resolver`. Tests pass; behavior unchanged.
3. **New git-service endpoints**: branches, pulls, webhooks, credentials/refresh. Per-task bearer middleware. No callers yet.
4. **Webhook receiver scaffold** in BFF: route mounted, HMAC verify, dedup, persist. Empty router (logs only). Manually trigger with a fake delivery to verify the pipeline.
5. **Dispatch rewrite**: BFF `task_dispatch_service` now creates branch + draft PR + per-task workspace via remote-worker. remote-worker `workspace.ts` + `credhelper.ts` written. Old submit-pipeline code still in place but unreachable from the new dispatch.
6. **Webhook handlers + projector**: implement the routing table from §8.3. Wire the workflowrun_service.
7. **Component creation flip**: `AutoBuild: true → false`, drop the explicit `TriggerBuild()` call.
8. **MCP removal**: delete `asdlc-service/mcp/`, drop the route mount, drop `mcpEndpointUrl` plumbing, drop the two MCP tools from remote-worker's allowed-tools.
9. **Status field migration in code**: replace every read/write of the four status fields with `Status` reads/writes. The 43 references in `task_service.go`, the 10 in `console/src/services/api/types.ts`, and the rest collapse.
10. **smee.io wiring** in `deployments/scripts/start.sh` + `.env.example`.
11. **CLAUDE.md update**: PAT scope additions, MCP endpoint removed from service table, smee note.

Steps 1–4 are independently testable (unit tests + manual webhook injection). Steps 5+ start exercising the new flow end-to-end.

---

## 13. Test strategy

### 13.1 Unit

- `credentials.PlatformPATResolver`: returns same credential per call, identity matches config.
- `webhook.Verifier`: HMAC validation accepts/rejects with single-secret list; dedup row insert is idempotent.
- `task_state.Apply`: every transition in the table is allowed; every other transition is refused; terminal states absorb late events.
- `workflowrun_service.TriggerForPush`: builds the correct OC `WorkflowRun` body, including SHA injection.
- `bearer_service`: issued tokens validate; tampering fails; expired tokens fail.

### 13.2 Integration (against docker-compose stack)

- Provision a project. Verify GitHub repo exists, webhook is registered on it (mock GitHub or use a smee channel pointed at a test handler).
- Generate tasks. Verify each task has issue + branch + draft PR on GitHub.
- Dispatch tasks. Verify per-task workspaces exist on the host with `.gh-config/hosts.yml` populated, `.git/config` user/email set, `.asdlc/credhelper.sh` present.
- Inject a fake `pull_request.ready_for_review` webhook. Verify task transitions to `ready_for_review`.
- Inject `pull_request.closed merged=true` + `push to default`. Verify task transitions to `merged → building`, and `WorkflowRun` is created with the expected SHA.
- Replay any of the above webhooks. Verify dedup absorbs.

### 13.3 E2E

The single happy-path scenario (evolution-doc §13 in the prior conversation): create project → generate tasks → dispatch → agent commits → PR ready → webhook → human merge → webhook → WorkflowRun → deployed. A real Claude Code agent in a fresh workspace should complete a trivial component task end-to-end.

Existing E2E that depend on the old submit pipeline are expected to red — they get rewritten as part of this chunk.

---

## 14. Convergence with agent-manager and divergence

**Where ASDLC mirrors agent-manager:**

- `WorkflowRun` creation pattern (commit-SHA-injected at trigger time via `params.repository.revision.commit`).
- OC client surface for Component / WorkflowRun / SecretReference.
- Three-level resource labelling (org via namespace, project via label, component via label).
- `autoDeploy: true` on Components; Components created before first build trigger.
- (Phase 2) OpenBao + `SecretReference` CR for git credential indirection. ASDLC is currently writing PAT directly to OC `GitSecret` (`task_service.go` lines 537–567); Phase 2 migrates to the agent-manager pattern.

**Where ASDLC diverges:**

- ASDLC has a webhook receiver and a task/issue/PR model. agent-manager does neither.
- ASDLC has an agent workspace with `git`/`gh` credentials. agent-manager has no agent workspace at all (builds run inside OC's workflow engine).
- ASDLC has the credential resolver abstraction. agent-manager reads the PAT directly from config; this is fine for them because their credential model is single-tenant and never grows. ASDLC has the abstraction because Phase 2 introduces multiple kinds **and** multiple orgs simultaneously — the same seam carries both. agent-manager isn't a precedent for the multi-org pattern; the resolver is ASDLC-original.

**What we should pick up from agent-manager during implementation:**

- The OC OAuth2 token minter pattern at `agent-manager-service/clients/openchoreosvc/auth/` if ASDLC's OC client doesn't already match it.
- The polling cadence + termination conditions for `WorkflowRun` status (`agent-manager-service/clients/openchoreosvc/client/builds.go`).
- Naming convention: `git-{org}-{repo}` for `SecretReference`s (Phase 2).

---

## 15. Open questions / decisions still outstanding

1. **Per-task bearer TTL.** The bearer must outlive the longest task. Suggestion: max 24 h, with a renewal endpoint if a task legitimately runs longer. Decision needed before implementation.
2. **Polling vs. OC push for `WorkflowRun` status.** Polling is simpler and matches agent-manager. If OC publishes status via Kubernetes watch streams, switching to a watch is a perf optimisation but a correctness wash. Recommend: poll in Phase 0, revisit if scale demands.
3. **`gh` config refresh daemon.** Phase 2 needs it; Phase 0 does not. Recommend: define the contract now (daemon writes `<workspace>/.gh-config/hosts.yml` with current token; runs every 50 min) but ship Phase 0 without the daemon.
4. **Agent identity in Phase 0.** The platform PAT's GitHub login is whatever the user configured (`anjanasupun05@gmail.com`'s account). For dev that's fine. For "looks like a bot" we'd create a service account. Decision: stick with the developer's account in Phase 0; revisit when Phase 2 lands or when a multi-developer dev environment becomes painful.
