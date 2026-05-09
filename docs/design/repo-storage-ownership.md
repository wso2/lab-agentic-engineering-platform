# Repo Storage Ownership — git-service as Sole Owner of `/data/repos`

> **Note (post-v4.1):** the wireframes feature has since been removed from the platform end-to-end. References to `.asdlc/wireframes/*`, `WriteWireframe`, the `wireframes` artifact endpoints, and the spec-save extra-staging logic in this document are historical only.

> **Note (multi-document requirements refactor):** the spec artifact has since been renamed and restructured. There is **no longer** a `.asdlc/spec.md`; requirements are stored as a directory of markdown files at `.asdlc/requirements/{requirements.md, functional-requirements.md, non-functional-requirements.md, user-stories.md}`. The tag scheme has changed from per-artifact prefixes (`spec-v*`, `design-v*`) to:
> - Requirements: `v<N>` — one save tags the whole `.asdlc/requirements/` directory snapshot.
> - Design: `v<N>-<M>` — N is the source requirements version, M is the design revision under that N. Saving design without an existing `v<N>` returns 409.
>
> Lineage is now encoded in the design tag name itself (no `source-spec:` annotation lines); the `Lineage` request/response field has been removed. Any references in this document to `.asdlc/spec.md`, `spec-v*`, `design-v*`, `parseLineage`, `buildLineageMessage`, or `Lineage` are historical context for the v4.1 design — see `git-service/services/artifact_service.go` and `artifact_versioning.go` for current behaviour.

> **Status: v4.1 (final)** — extends v3.1 with the local-developer flow fix; fourth review pass landed "ship after fixes" with all items folded in here. v4.1 deletes the dead `hostRepoBase` plumbing and extends the issue body with a "Local Developer Setup" section sourced from `GitRepository.RepoURL` + `task.BranchName` + `task.AppPath`. The section is gated on `BranchName != ""` (it's only rendered at dispatch-time re-render, not at first issue creation), the existing "How To Submit" preamble is rephrased to remove the contradiction the reviewer caught, the auth block is `gh auth status || gh auth login` with the write-access scope spelled out, and the skill (`SKILL.md:8`) gets a one-line edit pointing local-flow developers at the issue body's setup section. Both code and skill changes ride PR 2.

## Problem

Today the BFF (`asdlc-service`) and `git-service` **both** bind-mount `~/.asdlc/repos` → `/data/repos` and read/write the same on-disk git working trees. This works in docker-compose (single host) but breaks in a real Kubernetes deployment: two Pods cannot reliably share a host directory, and even with a shared `ReadWriteMany` PVC the two services would race on the same working tree (BFF doing `os.WriteFile` while git-service runs `git add`/`git commit`).

The audit confirmed:

- BFF's `ArtifactStore` (`asdlc-service/services/artifact_store.go`) is the **only** remaining BFF code that touches the working tree. It does `os.ReadFile` / `os.WriteFile` / `os.ReadDir` on `.asdlc/spec.md`, `.asdlc/design.json`, and `.asdlc/wireframes/*`.
- Every other git operation (commit, push, pull, tag, branches, PRs, issues) already goes through git-service HTTP.
- BFF and git-service independently compute the same path layout `{REPO_BASE_PATH}/{orgId}/{projectId}` — two sources of truth.
- `remote-worker` does **not** read `/data/repos`; it clones each task into its own per-task workspace. Out of scope.
- **Pre-existing bug** (caught in review): `spec_service.go:231-237` passes both `Files: [".asdlc/spec.md"]` and `Directory: ".asdlc/wireframes"` to `Commit`, but `git_ops_service.go:209-218` only stages `Directory` when `Files` is empty. Wireframes are silently not committed as part of spec save today; the working tree is dirty when the user lands on the design step. v2 fixes this by collapsing the call into a single atomic save.

## Goal

`git-service` is the sole owner of repo working trees on disk. The BFF holds no filesystem path to a repo and has no `REPO_BASE_PATH` env var. Save flows (write + commit + push + tag) are atomic from the BFF's point of view: one HTTP call, one mutex hold inside git-service.

Non-goals:
- Changing how `remote-worker` clones (it already owns its own workspace).
- Multi-replica `git-service` (still single-replica; horizontal scaling is a future design).
- Re-doing the local-developer flow that templates a host path into GitHub issue bodies (called out under Open Questions).

## Design

### Ownership rule

> Only `git-service` reads or writes inside a project's working tree. The BFF identifies a working tree by `(orgId, projectId)` only and never constructs a filesystem path.

The path layout `{base}/{orgId}/{projectId}` is private to git-service. The duplicated path-derivation in `artifact_store.go:29-31` is deleted.

### Why domain endpoints, not generic file CRUD

The v1 proposal exposed `GET/PUT/DELETE /files?path=...`. Three concrete problems:

1. **No atomic save.** `SaveAndProceed` is `Commit → Push → ListTags → hasChanged → CreateTag` — five separate critical sections. Two concurrent saves can interleave to produce skipped or duplicated versions, and a `WriteSpec` that succeeds but whose `Commit` fails leaves a dirty tree inconsistent with the tag history.
2. **Domain knowledge stays in two places.** `.asdlc/spec.md`, the `spec-v` tag prefix, the lineage marker `source-spec: ...` in tag messages — all of this leaks across the boundary if the BFF still constructs paths and tag names. git-service is *already* artifact-aware (`ListTags` filters by prefix; `GetFileAtTag` knows the tag-and-path shape) — the line was already crossed.
3. **Pre-existing bug stays hidden.** A generic file API doesn't fix the spec-save-doesn't-commit-wireframes bug. A domain `save` endpoint that owns staging end-to-end does.

### API surface on git-service

All routes nest under `/api/v1/repos/{orgId}/{projectId}/...`. **`orgId` is in the path** so middleware can validate `repo.OrgID == pathOrgID` against the JWT — closes the cross-org-read hole the review surfaced. Existing repo routes that take only `{projectId}` are migrated to `{orgId}/{projectId}` in the same change so the precedent is uniform. (Backwards-compat shims are out of scope: BFF is the only Service-JWT principal.)

| Method | Path | Body / Params | Response | Mutex held |
|---|---|---|---|---|
| `GET` | `…/artifacts/spec` | — | `200 {content, sha}` or `404` | per-project, briefly |
| `PUT` | `…/artifacts/spec` | `{content, ifMatch?: <sha>}` (text) | `200 {sha}` or `412` | per-project, briefly |
| `POST` | `…/artifacts/spec/save` | `{content, message, lineage?: {sourceSpec, sourceDesign}}` | `200 {version, tag, commitHash, status, lineage}` | per-project, **across the whole save** |
| `POST` | `…/artifacts/spec/discard` | — | `200 {content, status}` or `404` | per-project |
| `GET` | `…/artifacts/spec/versions` | — | `200 [{tag, version, commitHash, message, lineage}]` | per-project, briefly |
| `GET` | `…/artifacts/spec/versions/{tag}` | — | `200 {content, lineage}` | per-project, briefly |

Same shape for `design`. Wireframes are handled separately (next section). All `Content-Type: text/plain; charset=utf-8` for spec/design/wireframe bodies; no base64. (Resolves v1 inconsistency with `GetFileAtTag`.)

Concrete semantics pinned down in v3:

- **`content` is required on `save`.** v2 had it optional; that left a window where a tail-end `PUT /artifacts/spec` from a still-finishing stream could land between the save's stage and commit, committing bytes the save's caller didn't author. Required `content` makes the writer-of-the-bytes equal the caller-of-save.
- **`sha` is `git hash-object` of the working-tree blob.** Stable across replica restarts (no mtime dependence). The `PUT` accepts an optional `ifMatch: <sha>`; mismatch returns `412 Precondition Failed`. Streaming clients pass the `sha` returned by the previous `PUT` to get optimistic concurrency control on the working-tree write path. (Partly mitigates the `streamGenerateSpec` race that's otherwise deferred.)
- **`lineage` is structured in *all* responses.** `GET /versions`, `GET /versions/{tag}`, and `POST /save` return `lineage: {sourceSpec, sourceDesign}` as fields. The on-tag-message format (`source-spec: spec-vN\nsource-design: design-vM`) is private to git-service; the BFF never parses tag bodies. This deletes `parseLineage` and `buildTagMessage` from `asdlc-service/services/versioning.go`.
- **`discard` on a never-tagged artifact returns `404`** (`no saved version to revert to`), matching today's behavior in `spec_service.go:298-301`. On success it returns the reverted content plus the status the BFF would have computed.
- **`design`-specific note:** `DesignFile.SourceSpec` (the field embedded in `.asdlc/design.json` by `StreamGenerateDesign` for UI display of "draft generated from spec-vN") is **not** authoritative for tag metadata. The request-body `lineage` on `save` is the only input git-service uses when writing a tag. The save handler does not read or modify the in-file `sourceSpec`. If the two disagree, the request body wins for what gets tagged; the in-file value continues to drive only the "draft was generated from" UI label.
- **`PUT /artifacts/{spec,design}` is bytes-in, bytes-out.** The handler does not parse JSON, validate schemas, or rewrite embedded fields like `sourceSpec`. It writes the supplied bytes via `tmpfile + rename`. Schema validation lives in the BFF / agents-service producers; git-service stays domain-aware only at the *artifact-type* level (which file goes where, which tag prefix to use), not at the *content-shape* level.

The atomic `save` handler — under one mutex hold — does:

0. **Self-heal previous tag-push failures.** `git push origin --tags` (no-op if all local tags are already on remote). This is the must-fix from review pass 2: a previous save can have committed and pushed but failed to push the tag, leaving `spec-v{n}` local-only. Without this step, the next save's `hasChanged` check sees the local tag and silently no-ops, so the missing remote tag never recovers.
1. Write `content` to `tmpfile + rename` into `.asdlc/spec.md`. (Resolves disk-full / partial-write hazard.)
2. `git add .asdlc/spec.md` and, for the spec save, `git add .asdlc/wireframes/` (this is the wireframes-commit fix).
3. `git commit` with the supplied message + author from credential identity. If `nothing to commit` and the previous tag's commit equals HEAD, return `{status: "unchanged", version, tag}` (no work to do).
4. `git push origin <default>`.
5. `git fetch --tags`, list `spec-v*`, compute next version.
6. If working-tree content equals last-tag content → return `{version: <existing>, status: "unchanged"}`. Else create `spec-v{n}` annotated tag with body `source-spec: …\nsource-design: …` (lineage from request) and `git push origin spec-v{n}`. If the tag-push fails specifically, the local tag is deleted before returning the error so step 0 of the next save doesn't silently absorb the failure into a no-op.
7. Return `{version, tag, commitHash, status: "approved", lineage}`.

Tag prefixes (`spec-v`, `design-v`) are constants in one file per artifact type — no string-literals scattered across handlers.

`hasChangedSinceLastTag`, `nextVersion`, and lineage formatting move from `asdlc-service/services/versioning.go` into git-service. The BFF's `versioning.go` reduces to a thin wrapper that calls the save endpoint and returns structured `lineage` to the UI. **No tag-message parsing on the BFF side.**

Concrete failure-class branching in step 6 (called out by review):

- `git tag -a` succeeds, `git push origin <tag>` fails → delete the local tag and return the push error. Step 0 of the next save creates and pushes a tag against current HEAD with whichever version `nextVersion` computes at that point.
- `git tag -a` fails with `tag already exists` (some other actor — manual ops, or a future multi-replica sibling — created `spec-v{n}` between step 0 and step 6) → **do not delete**. Treat as a concurrent-write conflict and return `409 Conflict`. The tag isn't ours to remove.
- `git tag -a` fails for other reasons → return the error; the tag was never created so nothing to clean up.

Sharing the per-project mutex matters: **all artifact endpoints for a given project take the same per-project mutex.** `save` holds it across the whole flow (steps 0–7); `PUT` releases promptly. So a `PUT` cannot land between a `save`'s tmpfile-rename and `git add` — it blocks until the save completes. This is why `save` does not need its own `ifMatch`.

Step 0 cost note: doing `git push origin --tags` on every save is wasteful in the common case where there's nothing to self-heal. Cheap optimisation: cache `last_failed_tag_push_at` per project in-memory (single replica, fine to lose on restart) and skip step 0 when clean. Or use `git ls-remote --tags` plus `git tag --list` to compute the local-only set and only push that. Pick one; don't pessimise the hot path with a full `--tags` push.

### Wireframes — explicit batching

Wireframes stream in async; today's flow is "many `WriteWireframe` calls; later one `Commit directory:.asdlc/wireframes`". The review flagged a partial-batch hazard if a future flow streams *N* wireframes in parallel and the user clicks Save mid-batch. Fix:

| Method | Path | Behavior |
|---|---|---|
| `PUT` | `…/artifacts/wireframes/{name}` | Atomic single-file write (`tmpfile + rename`) into `.asdlc/wireframes/{name}`. |
| `GET` | `…/artifacts/wireframes` | List names + sizes. |
| `GET` | `…/artifacts/wireframes/{name}` | Read one. |

Wireframes are committed by the **spec** save (step 2 above stages `.asdlc/wireframes/`). They have no independent tag stream. This matches today's intent and fixes the silent commit-drop bug.

If we ever stream a real batch (multiple files), introduce `?staged=true` writes into `.asdlc/wireframes/.staging/` plus an explicit `POST /artifacts/wireframes/promote` that renames the staging dir under the per-project mutex. Out of scope for this design; called out so the API doesn't paint us into a corner.

### Path validation

The new endpoints accept `{name}` only for wireframes, not arbitrary paths. The handler enforces:

- `name` matches `^[a-zA-Z0-9._-]+$` (no path separators, no `..`).
- File size cap (5 MiB) to bound memory.
- Allow-list is implicit in routing — there's no path string the caller can supply that escapes `.asdlc/`.

Spec/design endpoints take no path at all.

### Concurrency & race windows (review item #3)

The mutex change matters. v1 acquired the mutex per HTTP call — five critical sections for a save. v2 acquires it once across the whole save, so:

- Two concurrent `SaveAndProceed` calls on the same project are now strictly serialised. Second caller sees the first's tag and either no-ops (`status: "unchanged"`) or creates `spec-v{n+1}`.
- A long `streamGenerateSpec` still races against a save (the BFF holds nothing during the stream). This race already exists; v3 narrows it via the `ifMatch` precondition on `PUT /artifacts/spec` (the streaming client passes the previous write's `sha`; concurrent writes detect the conflict with `412` instead of silently overwriting). Full prevention would need a session-scoped lock — explicitly deferred.
- **Mutex-hold latency.** `save` holds the per-project mutex across two network ops (commit-push and tag-push). On a slow GitHub day this can be 5–15s. Two users hitting Save on the same project at once will see the second request block. Not a regression — today's `Commit` already pushes inside the mutex — but the bundling does compound the worst case. Acceptable for the current scale; revisit if the metric shows it.

### Clone management (review item #6 + #9)

`ensureCloneReady` (`git_ops_service.go:106-171`) currently `os.RemoveAll`s the working tree on re-clone, which would silently delete unsaved spec/design content if `.git` is corrupt after a Pod restart. Two changes in this PR:

1. **No destructive re-clone.** Clone to `…/.tmpclone-<ts>`, validate, then atomic `os.Rename` over the existing path. If the existing path holds files but no `.git`, fail loudly and surface to the user; do not nuke their unsaved content.
2. **Pre-warm at startup.** On boot, `git-service` first cleans any orphaned `.tmpclone-*` directories left over from a previous crash (so they don't accumulate), then reads `git_repositories` from the DB and kicks off clones with a worker pool of **10**. Per-project mutex is acquired by each worker so a request landing on an un-pre-warmed project still serialises correctly. Failures are logged with the org/project and surfaced via a `clones_pending` and `clones_failed` gauge on `/metrics`; cold-start incidents become operator-visible instead of presenting as "first request hangs". On a request for a project whose pre-warm clone failed, `ensureCloneReady` retries the clone with the request's context using the same clone-and-rename idiom (writes to a fresh `.tmpclone-<ts>`, never inherits the failed dir). GitHub secondary rate-limits at 10 concurrent clones is not a concern (we clone via per-org PAT/App tokens) but if we ever scale beyond hundreds of projects per replica, the bound becomes a tunable.

### Auth (review item #2)

The Service-JWT middleware is extended to:

1. Resolve `{orgId}` from the path.
2. Resolve the repo by `(orgId, projectId)` — return `404` if the pair doesn't match an existing row (do not leak existence by returning a different code for "wrong org").

> **Scope note (added during PR 0 audit).** The original design also called for a step 3: *"Reject if the JWT's `OcOrgID` claim does not match path `orgId`."* Audit revealed today's Service JWT is a single per-instance token minted via `client_credentials` against Thunder (`asdlc-service/clients/auth/provider.go:115-150`) with **no `OcOrgID` claim**. Implementing JWT-side org enforcement requires the BFF to mint **per-org** Service JWTs (token cache keyed by org, independent refresh, possibly Thunder-side org-context support). That's a meaningful auth-model change deserving its own design pass — token lifecycle, refresh semantics, retry behavior — and was deliberately deferred so PR 0 can ship the containerization unblock without conflating two concerns.
>
> **PR 0 implements steps 1+2 only.** Threat-model reduction is still real: BFF bugs that use the wrong `projectId` now fail loudly (`404`) instead of silently cross-accessing; audit logs carry org scope; mismatched paths can't reach unrelated repos. The remaining gap — a stolen/leaked BFF service token can still craft any valid `(orgId, projectId)` path — is the same posture the existing `/internal/credentials/orgs/{ocOrgId}/...` routes already have, and the cluster's network perimeter contains that risk for now.
>
> **Follow-up**: a separate design + PR for per-org Service JWTs. Tracked as a known limitation; not gating the storage-ownership rollout.

This change closes the *path-side* cross-org-read hole on the new artifact endpoints **and** retroactively on the existing `/commit`, `/push`, `/tags/*`, `/branches/*`, `/issues/*` routes. The migration is mechanical; the BFF caller already knows `orgId` at most call sites and is threaded through the six that don't (`spec_service.ReadSpec` / `SaveAndProceed`, `design_service.ReadDesign`, `project_service.GetRepoStatus` / `GetProjectStatus`, `task_service.GenerateTasks` issue calls — controllers updated to pass `orgID` in).

### Encoding & 404 semantics (review items #11, #12)

- All artifact bodies are UTF-8 text (`Content-Type: text/plain`). We control the producers; HTML wireframes are UTF-8.
- `GetFileAtTag` is updated to the same shape (it currently returns `{content: string}`; no change to the wire shape, just consistency in the new endpoints).
- `404` is returned for "artifact does not exist". `200 {content: ""}` is returned for "artifact exists but is empty". The BFF's `ReadSpec` callers branch on `errors.Is(err, ErrArtifactNotFound)`, not on empty string. The current `("", nil)` quirk in `artifact_store.go:46-48` is removed.

## BFF changes

### `ArtifactStore` collapses; `versioning.go` thins

The `ArtifactStore` interface used by `spec_service.go`, `design_service.go`, `task_service.go` is preserved verbatim — call sites don't change. The implementation switches from `os.ReadFile`/`os.WriteFile` to HTTP calls against `/artifacts/spec` etc.

`SaveAndProceed` collapses from ~70 lines (Commit → Push → ListTags → hasChanged → CreateTag) into one call:

```go
res, err := s.gitClient.SaveSpec(ctx, orgID, projectID, &SaveSpecRequest{
    Content: content,
    Message: "Add ASDLC specification",
})
```

`hasChangedSinceLastTag`, `nextVersion`, `parseLineage`, `buildTagMessage`, and `tagsToVersions` move out of `asdlc-service/services/versioning.go` — into git-service or deleted entirely. The BFF's `versioning.go` consumes structured `lineage` from `GET /versions` responses; **it parses no tag bodies and constructs no tag messages**. The `gitservice.TagInfo.Message` field is dropped from the BFF-side client struct in favour of `Lineage` (a `{sourceSpec, sourceDesign}` struct). `ReadRawFile` (`design_service.go:106`) is replaced by `GET /artifacts/design/versions/{tag}` at the same call site.

PR 2 grep checklist (so nothing slips through):

- `parseLineage` — delete all callers and the function itself.
- `buildTagMessage` — delete.
- `tagsToVersions` — collapses to a pure rename of API response fields, or deletes if the API already returns the right shape.
- `gitservice.TagInfo.Message` — replaced by `Lineage`.
- `if content == ""` for "no spec yet" — switch to `errors.Is(err, ErrArtifactNotFound)`.
- The `("", nil)` quirk in `artifact_store.go:46-48` — gone.

### Config / env removed

- `REPO_BASE_PATH` env from BFF (Dockerfile, docker-compose, helm).
- `${HOME}/.asdlc/repos:/data/repos` bind mount on `asdlc-api`.
- `mkdir -p /data/repos` in `asdlc-service/Dockerfile:12`.
- `RepoBasePath` field from `config.Config`.

`REMOTE_WORKER_REPO_BASE_PATH` is **also removed** — see "Local-developer flow under k8s" below. The audit found the env var is read into `config.RemoteWorkerRepoBasePath` and stored on `taskService.hostRepoBase`, but `buildIssueBody` discards it. It's vestigial; the issue-body fix obviates the need for any host-side path.

## docker-compose / k8s changes

```yaml
asdlc-api:
  # REMOVE the volumes block and REPO_BASE_PATH.

git-service:
  # KEEP — git-service is now the only owner.
  volumes:
    - ${HOME}/.asdlc/repos:/data/repos
  environment:
    REPO_BASE_PATH: "/data/repos"
```

In k8s:
- BFF Pod: no repo volume.
- git-service Pod: `PersistentVolumeClaim` (`ReadWriteOnce`; single-replica matches today's mutex model) mounted at `/data/repos`.
- The PVC is sized for projects served by this replica. Multi-replica is a separate design (DB-backed locks + sticky routing on `projectId`).

## Failure modes

| Scenario | Behavior |
|---|---|
| `git-service` down during `ReadSpec` | BFF returns 503 to the console (same as any git-service call today). |
| Clone stale / missing | `ensureCloneReady` re-clones via clone-and-rename — preserves any unsaved working-tree content if `.git` is corrupt. |
| `.git` corrupt + working tree non-empty | `ensureCloneReady` refuses the destructive re-clone and returns `503` with a body referencing the operator runbook. Caller knows it isn't transient and shouldn't retry-loop. |
| `save` self-heal: previous run pushed commit but tag-push failed | Step 0 (`git push origin --tags`) puts the missing tag on remote on the next save. If the tag-push specifically failed in the same save call, step 6 deletes the local tag before returning the error so the next save reissues it cleanly. |
| `save` invoked but content equals last-tagged content | Returns `200 {status: "unchanged", version: <existing>, tag, lineage}`. No commit, no push, no new tag, mutex released. |
| `save` race: another actor created `spec-v{n}` between step 0 and step 6 (`git tag -a` fails `already exists`) | `409 Conflict`. Local tag is **not** deleted (it isn't ours). BFF surfaces a "someone else just saved" message; user re-reads and re-saves. |
| `PUT` with stale `ifMatch` | `412 Precondition Failed`. Caller (streaming client) re-reads, decides whether to overwrite or surface a conflict. |
| Disk full mid-write | `tmpfile + rename` fails → error to caller; original file untouched. No partially-written JSON in the working tree. |
| `save` half-completes (commit succeeds, push fails) | Working tree has the commit, no tag created (push happens before tag in the save handler — see step ordering above). Next `save` retries idempotently: `hasChanged` returns false against the unpushed commit, no-op. |
| Pod restart after `PUT /artifacts/spec` (write but not yet saved) | PVC preserves working tree → next `GetSpec` returns the unsaved content as before. |
| Path traversal (`../`) on wireframe `name` | Rejected at the validator (`400`); never hits disk. |
| Cross-org JWT (`OcOrgID` ≠ path `orgId`) | Middleware rejects with `403`. |
| Two concurrent `save` on same project | Strict serialisation via per-project mutex; second caller sees first's tag. |
| Two concurrent `streamGenerateSpec` finishing → racing `PUT /artifacts/spec` | Last-writer-wins on the working tree. Pre-existing race; not widened. Documented. |

## Out of scope

- **Multi-replica git-service.** Per-project mutex still serves us; sticky routing or DB-backed locks is a separate design.
- **Streaming uploads.** Specs/designs are KB; wireframes tens of KB.
- **Read caching in BFF.** Add only with a profile.
- **Wireframe batch promotion (`?staged=true`).** API shape is reserved; not implemented until we have multi-file streaming.
- **Restricting `PUT /artifacts/spec` to a streaming-session principal.** Today any Service-JWT principal can clobber `.asdlc/spec.md` directly without going through `save`. The BFF is the only such principal, so the blast radius is bounded; tightening this would require a second JWT audience for streaming and is deferred. Flagged so a future endpoint addition doesn't widen it accidentally.
- **Binary artifacts.** All current artifacts are UTF-8 text. If we ever add binary artifacts (images, archives), they need a separate endpoint with `application/octet-stream`, not retrofitted `Accept`-negotiation onto the text endpoints.
- **Removing `REMOTE_WORKER_REPO_BASE_PATH`.** Tracked under the broader localism inventory; called out below.

## Migration / rollout

1. **PR 0 — `orgId` on path + cross-org auth (its own release).** Migrate every existing repo route from `/api/v1/repos/{projectId}/...` to `/api/v1/repos/{orgId}/{projectId}/...`. Middleware enforces `repo.OrgID == pathOrgID == JWT.OcOrgID` and returns `404` (not `403`) when the pair doesn't match a row, to avoid leaking existence. BFF call sites updated (every git-client invocation already has `orgId` in scope). This is split out from the artifact work so it lands and rolls back independently — it's an auth tightening, not a feature.
   - **Rollout ordering across Pods.** BFF and git-service are separate Pods with independent `helm upgrade` rollouts; a naive ordering would break a live BFF mid-upgrade. PR 0 ships git-service with **both old and new routes** wired to the same handlers. Deployment order in this release: BFF first (now calling new routes), git-service second. PR 1 (the next release) drops the old git-service routes once the BFF rollout has settled. No flag-gate on the BFF — it always uses new routes after PR 0.
   - **Test**: JWT(`OcOrgID=A`) + path(`orgId=B`) + repo-exists-in-B → `403`; repo-not-existing → `404` with no discriminator.
2. **PR 1 — git-service artifact endpoints.**
   - Add `/artifacts/{spec,design,wireframes}` endpoints (read, write, save, discard, versions).
   - Move `hasChangedSinceLastTag` + `nextVersion` + lineage formatting from BFF; return structured `lineage` in API responses.
   - Save handler self-heals previous tag-push failures (step 0 above) and deletes failed local tags (step 6 above), with the failure-class branching pinned down: delete-only-on-push-fail; `409` on `tag already exists`.
   - Replace `RemoveAll` re-clone with clone-and-rename; refuse destructive re-clone if working tree non-empty; clean orphan `.tmpclone-*` at startup.
   - Startup pre-warm with worker pool = 10; `clones_pending`/`clones_failed` gauges.
   - Drop the old (pre-PR-0) routes that PR 0 left as compat shims.
   - **Test**: regression test that asserts `.asdlc/wireframes/*.html` lands in the spec-save commit (the silent-drop bug must not recur).
3. **PR 2 — BFF.** Switch `ArtifactStore` to HTTP; collapse `SaveAndProceed` flows; thin `versioning.go` (delete `parseLineage`, `buildTagMessage`, in-process tag-message parsing); remove `REPO_BASE_PATH`. Replace `if content == ""` checks with `errors.Is(err, ErrArtifactNotFound)` (the `("", nil)` quirk in `artifact_store.go:46-48` goes away).

   **Also in PR 2 — local-developer flow fix:**
   - Delete `hostRepoBase` plumbing (`task_service.go:42, 54, 65, 394`; `issue_body.go:33`'s second parameter; `RemoteWorkerRepoBasePath` config field). Wider `grep` before merge: confirm `REMOTE_WORKER_REPO_BASE_PATH` is removed from BFF Dockerfile, `deployments/docker-compose.yml:69`, `deployments/scripts/setup.sh`, `.env.example`, and any helm values — `docker-compose.yml` has a `${REMOTE_WORKER_REPO_BASE_PATH:-${HOME}/.asdlc/repos}` interpolation that must go away alongside the Go field.
   - Extend `buildIssueBody` to accept `repoURL` + `repoSlug` and emit the "Local Developer Setup" section (gated on `task.BranchName != ""`).
   - Rephrase the existing "How To Submit" preamble in the same file to remove the contradiction with the new section.
   - Hoist `gitClient.GetRepo` out of the per-task loop in `GenerateTasks`; fetch once, pass `repoURL` + `repoSlug` into the loop.
   - Edit `remote-worker/plugin/skills/asdlc/SKILL.md:8` to add the one-line "if you're running locally, follow the Local Developer Setup" pointer.
4. **PR 3 — deployment.** Remove `/data/repos` mount on `asdlc-api`; PVC on `git-service` Pod; helm values updated.

PR 1 and PR 2 land in the same release. PR 0 ships ahead, in its own release. No flag-gating on PRs 1–2: the previous code only worked because of the shared mount, which is exactly what we're removing.

## Local-developer flow under k8s

The original v1 wording flagged this as an "open prerequisite" — that the issue body templates a host path which stops existing under k8s. Audit revealed the framing was wrong on the first half and right on the second half:

- **`hostRepoBase` is dead code.** `buildIssueBody` (`asdlc-service/services/issue_body.go:33`) explicitly discards its second parameter (`_ string`). No host path is in any issue body today. `taskService.hostRepoBase` (`task_service.go:42`), `config.RemoteWorkerRepoBasePath`, and the `REMOTE_WORKER_REPO_BASE_PATH` env are vestigial — they were planned but never actually consumed.
- **The lurking UX problem is real, just located elsewhere.** The local-flow plugin's skill (`remote-worker/plugin/skills/asdlc/SKILL.md:8`) asserts: *"The current working directory is a per-task workspace: a fresh clone of the project's GitHub repo with the task's feature branch already checked out, and `git` + `gh` already authenticated."* Today this assertion happens to hold for local-flow developers because docker-compose's bind mount puts the platform's clone at `~/.asdlc/repos/{orgId}/{projectId}` on their laptop. Under k8s the platform's clone lives on a PVC in the cluster, not on the laptop. The skill's precondition fails silently — a developer running `claude` in an arbitrary directory will get nonsense behaviour from the skill instead of a clear "you need to clone first" signal.

### Fix

Two parts, both land in PR 2 alongside the BFF refactor:

#### 1. Delete the dead code

- Remove `hostRepoBase` from `taskService` (`task_service.go:42, 54, 65, 394`).
- Remove the unused second parameter from `buildIssueBody` and update the one call site.
- Remove `RemoteWorkerRepoBasePath` from `config.Config` and the loader.
- Remove `REMOTE_WORKER_REPO_BASE_PATH` from BFF Dockerfile, docker-compose, and helm values.

This is pure cleanup — none of it has runtime effects today.

#### 2. Make the issue body self-sufficient for local-flow developers

Two structural changes in `buildIssueBody`, in this order:

**a. Rephrase "How To Submit" to be context-neutral.** Today's preamble (`issue_body.go:112`) reads *"Your working directory is a fresh clone of the project repo with `git` and `gh` configured for this repo."* That's an unconditional assertion — fine for remote-flow agents but it directly contradicts the new local-setup section. Reword to:

> *"Your working directory should be a fresh clone of this repo with branch `<branchName>` checked out and `git` + `gh` configured. The remote-worker flow prepares this for you; if you're running locally, see Local Developer Setup below."*

This preserves the agent-reads-this-first ordering, removes the contradiction, and introduces the fork explicitly.

**b. Append a "Local Developer Setup" section, gated on `task.BranchName != ""`.** The block is rendered only when the branch is known — i.e. on the dispatch-time re-render, not on the initial `EnsureIssueForTask` at design-approval time. (Same conditional as the existing `How To Submit` branch lines at `issue_body.go:113-117`.)

```markdown
## Local Developer Setup

These commands are for the **human developer**, before invoking `claude`. The
agent itself must not modify auth state — the skill forbids `gh auth login`,
credential-helper edits, and token writes.

```bash
git clone <repoUrl> <repo-slug>
cd <repo-slug>/<appPath>          # AppPath from Component Constraints above
git checkout <branchName>
gh auth status || gh auth login   # must be authenticated as a user with write access to <repoUrl>
claude                            # with the asdlc plugin installed
```

Required `gh` scopes: `repo` and `workflow` (same as the platform PAT, per
`CLAUDE.md`). The remote-worker flow handles all of this automatically; this
section is only for the local-laptop path.
```

Concrete shape decisions (driven by review feedback):

- **`gh auth status || gh auth login`**, not bare `gh auth status`. The bare form only reports state; the `|| login` form recovers from "not logged in" without leaving the developer at a dead end.
- **Write-access requirement is stated explicitly.** The platform's PR/branch belongs to the org's GitHub account; a developer authenticated as a different user cannot `gh issue comment` or `gh pr ready`. This was load-bearing-but-implicit; now it's spelled out.
- **`<repo-slug>` is rendered as a literal value** (sourced from the URL's last path segment, with `.git` stripped if present), not a placeholder. The developer copy-pastes a complete command, doesn't have to compute the slug.
- **`cd <repo-slug>/<appPath>`** lands the developer in the component subdirectory before `claude` runs, so Claude Code's cwd is correctly scoped from the start. `task.AppPath` is already in the issue body under Component Constraints.
- **"These commands are for the human developer"** preamble disambiguates the agent-reads-this-too problem the reviewer caught: the skill (`SKILL.md:10`) explicitly forbids the agent from running `gh auth login` or modifying credential state, so the new block must not look like it's instructing the agent.

#### 3. Update the skill (`remote-worker/plugin/skills/asdlc/SKILL.md:8`)

The skill's flat assertion *"The current working directory is a per-task workspace…"* silently misleads any local-flow developer who reads `SKILL.md` and skips the issue body. Add one sentence:

> *"If you're running this task locally rather than via the platform's remote worker, follow the **Local Developer Setup** section in the issue body before reading further — the assertions below assume that setup is complete."*

One-line skill edit, lands with PR 2.

### Wiring the URL through

`buildIssueBody` is called from two places: task generation (issue created at "Generate tasks" time) and dispatch (issue updated when work begins). The dispatch site already has `RepoURL` from `gitClient.GetRepo` (`remote_worker_service.go:88`). The task-generation site (`GenerateTasks` → `EnsureIssueForTask` in a per-component loop, `task_service.go:394`) needs the URL too. **Hoist the `GetRepo` call out of the per-task loop**: fetch once per `GenerateTasks` invocation and pass `repoURL` (plus `repoSlug`, computed once) into `EnsureIssueForTask`. Avoids 12 redundant HTTP calls for a 12-component project.

Note: `RepoURL` is captured into the issue body at issue-creation time. If a project's GitHub repo is later renamed or transferred, GitHub's redirect handling makes the embedded `git clone` URL still resolve, so we don't need a body-rewrite mechanism for the rename case.

### Why not surface this in the console UI instead?

A "Run locally" panel on the task detail page would be cleaner UX, but the issue body is the canonical task spec — it's what `gh issue view` returns, what the skill instructs the agent to read, and what survives if the console is unreachable. Putting setup in the body keeps the task self-describing. A console affordance is fine as a follow-up but is not a correctness gate.

### Why not gate on cwd-detection inside the skill?

The skill could plausibly check on entry whether `git rev-parse --show-toplevel` succeeds and bail with a helpful error otherwise. But the skill runs *inside* a Claude Code session — by the time the skill loads, the user has already invoked `claude` in the wrong directory. Surfacing the setup in the issue body lets the developer fix their environment *before* spending tokens. Skill-side detection can land later as a defence-in-depth measure; it doesn't replace the body change.

## Open questions

1. **Quota on writes.** Should we cap writes-per-minute per project to defend against a runaway agent? Probably yes; tracked as a follow-up.
2. **Versioning in git-service vs. BFF.** v2 moves `hasChanged` + `nextVersion` into git-service so save can be atomic under one mutex. That makes git-service ASDLC-aware (it already filters tags by prefix `spec-v*`/`design-v*`, so this is a small step). If we ever build a second consumer of the same repos with different versioning rules, we'd lift them back out behind a config. Comfortable accepting this trade-off given a single consumer today.
