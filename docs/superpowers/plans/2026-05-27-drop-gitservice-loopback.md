# Drop the gitservice loopback HTTP client (WS0.1.h follow-up)

**Goal:** Delete `asdlc-service/clients/gitservice/` and rewire every consumer to call in-process services directly. Stop reading `GIT_SERVICE_BASE_URL`; collapse `AGENT_GIT_SERVICE_URL` into `AGENT_PLATFORM_URL`. After this, asdlc-api makes zero HTTP calls to itself.

**Why:** `WS0.1` of the OC refactor folded git-service into asdlc-service but kept the HTTP client as a loopback (deferred as a "perf/maint follow-up" — `docs/superpowers/plans/2026-05-25-app-factory-workflows-in-wso2cloud-progress.md` line 33). In WSO2 cloud's dev environment that loopback resolves to the standalone git-service Service which has been scaled to 0 replicas (wso2cloud-deployment PR #2222), producing 503s on every credential / issue / artifact path. Removing the loopback eliminates this whole class of failure.

**Architecture:** No new behaviour. Every method on the `gitservice.Client` interface already has an in-process implementation under `asdlc-service/services/` — verified by grep. One method (`MergeInstallationRepos`) needs to be ported from the deleted git-service binary onto `CredentialService`.

---

## Method mapping (HTTP client → in-process service)

| `gitservice.Client` method | In-process replacement | Notes |
|---|---|---|
| **Credentials** | | |
| `CreateOrReplaceCredential` | `CredentialService.Connect` | request shape — see signature notes |
| `GetCredentialProjection` | `CredentialService.Status` | |
| `GetCredentialIdentity` | `CredentialService.IdentityFor` | rename |
| `DisconnectCredential` | `CredentialService.Disconnect` | |
| `UninstallAppInstallation` | `CredentialService.UninstallAppInstallation` | |
| `GetWebhookSecrets` | `CredentialService.GetWebhookSecrets` | verify exists; if not, expose `webhookSecrets` |
| **Installations** | | |
| `OrgIDByInstallationID` | `CredentialService.OrgIDByInstallationID` | |
| `OrgIDByRepoFullName` | `CredentialService.OrgIDByRepoFullName` | |
| `SetInstallationStatus(_, _, "suspended")` | `CredentialService.SuspendInstallation` | semantic split |
| `SetInstallationStatus(_, _, "active")` | `CredentialService.UnsuspendInstallation` | semantic split |
| `GetInstallationRepositories` | `CredentialService.ListInstallationRepos` | rename |
| `MergeInstallationRepos` | `CredentialService.MergeSelectedRepos` | rename |
| `ResolveUserInstallations` | `CredentialService.ResolveUserInstallations` | |
| **Anthropic credentials** | | |
| `CreateOrReplaceAnthropic` | `AnthropicCredentialService.Connect` | |
| `GetAnthropicProjection` | `AnthropicCredentialService.Status` | |
| `DisconnectAnthropic` | `AnthropicCredentialService.Disconnect` | |
| `ApplyAnthropicWPSecret` | `AnthropicCredentialService.ApplyWPSecret` | |
| **Repo lifecycle** | | |
| `InitProjectComponents` | `RepoService.CreateRepo` | rename + sig delta — see notes |
| `GetRepo` | `RepoService.GetRepo` | sig delta — drops `orgID` |
| `DeleteRepo` | `RepoService.DeleteRepo` | sig delta — drops `orgID` |
| **Git ops** | | |
| `Commit` | `GitOpsService.Commit` | |
| `Push` | `GitOpsService.Push` | sig delta — branch arg |
| `Pull` | `GitOpsService.Pull` | |
| `CreateTag` | `GitOpsService.CreateTag` | |
| `ListTags` | `GitOpsService.ListTags` | |
| `GetFileAtTag` | `GitOpsService.GetFileAtTag` | |
| **Issues** | | |
| `CreateIssue` | `IssueService.CreateIssue` | sig delta — drops `orgID` |
| `ListIssues` | `IssueService.ListIssues` | sig delta |
| `CloseIssue` | `IssueService.CloseIssue` | sig delta |
| `CommentIssue` | `IssueService.CommentIssue` | sig delta |
| `EditIssueBody` | `IssueService.EditIssueBody` | sig delta |
| **Branches / PRs** | | |
| `CreateBranch` | `BranchService.CreateBranch` | sig delta |
| `SeedBranchCommit` | `BranchService.SeedBranchCommit` | sig delta |
| `CreateDraftPR` | `PullRequestService.CreateDraftPR` | sig delta |
| **Webhooks** | | |
| `RegisterWebhook` | `webhookService.Register` | rename |
| `DeregisterWebhook` | `webhookService.Deregister` | rename |
| **Board** | | |
| `GetBoard` | `repoBoardService.GetBoard` | |
| `MoveIssueToStatus` | `repoBoardService.MoveIssueToStatus` | |
| **Build secrets** | | |
| `StageBuildSecret` | `BuildCredentialsService.StageBuildSecret` | |
| **Requirements** (12 methods) | `artifactService.*` | rename — drop the `Requirements` suffix where it duplicates a category; sig deltas everywhere (drops `orgID`) |
| **Design** (9 methods) | `artifactService.*` | same as Requirements |

**Pre-existing notes:**
- In-process `GetCredentialProjection`/`Status` returns `*services.Projection`, not `*gitservice.CredentialProjection`. Same data; controllers need a struct switch.
- `gitservice.IsNotFound(err)` / `gitservice.IsConflict(err)` / `gitservice.CredentialError` go away. Replace with `errors.Is(err, services.ErrXxx)` where the in-process service defines a sentinel error.
- Many in-process services derive `orgID` internally via a `RepoRepository` lookup on `projectID`. Consumers can drop the `orgID` argument they were passing.

---

## New code

**None.** Every method on `gitservice.Client` has an in-process equivalent already (verified by grep). The rewrite is pure rewiring.

---

## main.go wiring delta

Stop calling `gitservice.NewClient`. Ensure these services are constructed and available for injection (most already are — see the existing `services.NewXService(...)` calls scattered through `cmd/asdlc-api/main.go`):

- `CredentialService`
- `AnthropicCredentialService`
- `IssueService`
- `RepoService`
- `BranchService`
- `PullRequestService`
- `GitOpsService`
- `RepoBoardService`
- `BuildCredentialsService`
- `webhookService`
- `artifactService`

Pass them into the controllers and services that previously took a `gitservice.Client`. Drop the gitservice import.

---

## Per-consumer rewire (execution checklist)

Order: smallest blast radius first; verify `go build ./...` after each.

- [ ] **`controllers/org_anthropic_controller.go`** — replace `gitClient gitservice.Client` field with `anthropicSvc *services.AnthropicCredentialService`. 3 call sites (L56 Connect, L75 Status, L96 Disconnect). Update `NewOrgAnthropicController` signature + main.go.
- [ ] **`controllers/org_github_controller.go`** — replace with `credentialSvc *services.CredentialService`. 4 call sites (L170 ResolveUserInstallations, L242/L283 Connect, L302 Status). Update error-handling: `gitservice.IsNotFound(err)` → equivalent sentinel; drop `writeProxiedCredentialError` in favour of a small local translator.
- [ ] **`controllers/webhook_controller.go`** — already uses the `webhook.OcOrgIDLookup` interface; just wire its impl to `CredentialService.OrgIDByInstallationID` / `OrgIDByRepoFullName`.
- [ ] **`services/commit_identity.go`** — function parameter `gitClient gitservice.Client` → `credentialSvc *services.CredentialService`. 1 call site (L27 `GetCredentialIdentity` → `IdentityFor`).
- [ ] **`services/board_service.go`** — `gitClient` → `repoBoardSvc`. 3 call sites.
- [ ] **`services/component_service.go`** — `gitClient` → `repoSvc *services.RepoService`. 4 call sites (mostly `GetRepo`).
- [ ] **`services/task_service.go`** — `gitClient` → `issueSvc *services.IssueService`. 4 call sites (CreateIssue, ListIssues).
- [ ] **`services/workflowrun_service.go`** — `gitClient` → `repoSvc` + `buildCredentialsSvc`. 6 call sites.
- [ ] **`services/org_disconnect_service.go`** — `gitClient` → `credentialSvc` + `issueSvc`. 6 call sites.
- [ ] **`services/project_service.go`** — `gitClient` → `repoSvc` + `artifactSvc` + `webhookSvc`. 9 call sites.
- [ ] **`services/requirements_chat_service.go`** — `gitClient` → `artifactSvc`. 9 call sites (5 snapshot methods).
- [ ] **`services/task_stream.go`** — `gitClient` → `issueSvc` + `repoSvc` + `artifactSvc`. 8 call sites.
- [ ] **`services/requirements_service.go`** — `gitClient` → `artifactSvc`. 8 call sites.
- [ ] **`services/design_service.go`** — `gitClient` → `artifactSvc`. 11 call sites.
- [ ] **`services/artifact_store.go`** — `gitClient` → `artifactSvc`. ~10 internal call sites. **Keep the public surface intact** — `ArtifactStore` owns the external-API catalog (`ExternalAPICatalog`) and the `DesignFile` YAML shape; it's not a pure forwarding wrapper. 7 downstream services (design_service, requirements_chat_service, requirements_service, trait_sync, runtime_config_service, project_service, component_service) consume the 13 public methods — those stay untouched.
- [ ] **`services/dispatch_service.go`** — `gitClient` → `repoSvc` + `credentialSvc` + `issueSvc` + `repoBoardSvc` + `anthropicSvc`. 11 call sites.
- [ ] **`services/webhook/installation_handlers.go`** — `gitClient` → `credentialSvc` + `issueSvc`. 9 call sites (incl. the new `MergeInstallationRepos`).
- [ ] **`services/webhook/secrets.go`** — already interface-based (`SecretFetcher`); wire to `credentialSvc.GetWebhookSecrets`.
- [ ] **`services/webhook/routing_key.go`** — already interface-based (`OcOrgIDLookup`); wire to `credentialSvc`.
- [ ] **`clients/openchoreo/component_client.go`** — single reference is the `GitServiceURL` string field stamped onto OC resources for the coding-agent runner. After the AGENT_GIT_SERVICE_URL collapse (below), this becomes the same URL as `AgentPlatformURL` — keep the field name for now, source from `cfg.AgentPlatformURL`.

---

## URL config collapse

- [ ] Delete `cfg.GitService` (parsed from `GIT_SERVICE_BASE_URL`) entirely.
- [ ] Delete `cfg.AgentGitServiceURL` (parsed from `AGENT_GIT_SERVICE_URL`).
- [ ] Replace its usage in `dispatch_service.NewDispatchService` and `openchoreo/component_client.go` with `cfg.AgentPlatformURL` (parsed from `AGENT_PLATFORM_URL`). The runner-pod side reads it via `WorkflowRun.parameters.gitService.url` — leave the parameter name alone for now (renaming touches the runner image; defer to a later cleanup).
- [ ] Drop the fallback at `main.go:577–579` (`agentGitServiceURL == "" → cfg.GitService.BaseURL`).
- [ ] Update `asdlc-service/.env.example` — remove `GIT_SERVICE_BASE_URL` and `AGENT_GIT_SERVICE_URL`.
- [ ] Update `deployments/docker-compose.yml` — remove the same two lines + the `GIT_SERVICE_URL` line in the agents service (if present).

---

## Cleanup

- [ ] Delete `asdlc-service/clients/gitservice/` (4 files, ~1,824 LOC).
- [ ] Remove every `"github.com/wso2/asdlc/asdlc-service/clients/gitservice"` import.
- [ ] Run `goimports -w` across `asdlc-service/`.

---

## Test plan

- [ ] `cd asdlc-service && go build ./...` clean.
- [ ] `cd asdlc-service && go test ./...` clean. Pay attention to:
  - `services/build_credentials_service_test.go`
  - `services/credentials_refresh_service_test.go`
  - `services/api_security_test.go`
  - `services/artifact_store_api_test.go`
  - `services/progress_service_test.go`
  - `services/codingagent/job_template_test.go`
- [ ] Local stack: `cd deployments && bash scripts/setup.sh && bash scripts/start.sh`; sanity-check `GET /api/v1/organizations/admin/github` returns 200 (not_connected); create a project; save a tiny spec; save a tiny design; generate tasks; dispatch one — agent pod must reach the merged binary, no 503s.
- [ ] No outbound HTTP-client construction left: `grep -rn "gitservice.NewClient\|gitservice\\.Client" asdlc-service/` returns zero results.

---

## Out of scope (explicitly deferred)

- **Renaming the runner-pod parameter** `WorkflowRun.parameters.gitService.url`. Touches the remote-worker image. Folded into a later cleanup.
- **Test coverage backfill** for the few in-process methods that lacked direct test coverage when they lived in git-service (e.g. `MergeInstallationRepos`). The new tests fit better as a separate PR after this one merges.
