# Artifact Store v2 — clone-less `.asdlc/` persistence

**Status:** Draft v1 — under architectural review.
**Replaces:** the long-lived `git-service` working copy for `.asdlc/` reads, writes, and saves. Adjacent design: `docs/design/git-integration.md`, `docs/design/api-service.md`, `docs/design/github-integration-phase2.md` (credential trust model — preserved here).
**Out of scope:** the coding-agent's per-task ephemeral feature-branch clone (untouched), build dispatch / project board / PR / issue flows (already pure-API).

---

## 1. Motivation

`git-service` maintains a long-lived local clone per project under `${REPO_BASE_PATH}/<orgID>/<projectID>/`. The clone is the source of truth for `.asdlc/` reads, the staging surface for `.asdlc/` writes, and the working tree from which Save commits + pushes.

The clone is **never** pulled when remote `main` advances. That single missing invariant causes a class of bugs, not one. Three observed today:

1. **Non-fast-forward push after merge** — when a coding-agent PR merges on remote `main`, the next Save commits on top of stale main and `git push` is rejected. UI: *"Failed to save design."* This is the immediate trigger for the redesign.
2. **`git add -A` silently re-introduces deleted files.** If the merged PR deleted a `.asdlc/` file, the clone's stale working tree still has the bytes; `git add -A` re-stages them and Save quietly resurrects the file.
3. **`treesEqualAtPath` lies about "unchanged".** Unchanged-detection compares the *stale* local HEAD to the latest local tag. When remote has moved, both sides of the compare are wrong — Save returns "no-op" against a tag that is no longer the canonical version.

The architectural fix is to remove the clone for the `.asdlc/` lifecycle and use the GitHub API directly. `git-service` stays the only thing that holds GitHub credentials; the BFF↔git-service boundary (no token-bearing value crosses it) is preserved exactly as Phase 2 left it.

## 2. Goals & non-goals

**Goals:**
- Eliminate the three bug modes above by removing the stale-clone failure surface entirely.
- Multi-replica-safe BFF (current is single-replica, future is multi-replica). GitHub-side CAS replaces the in-memory mutex as the cross-replica serialization point.
- Preserve every public BFF↔git-service contract (read, put, save, discard, list versions, get-at-tag) modulo two minor shape changes called out below.
- Add cross-tab live visibility: two browser tabs editing the same project see each other's draft state within ≤10s.
- Keep saves cheap (≤10 GitHub API calls), reads conditional (304 = free), and the rate-limit budget legible.

**Non-goals:**
- CRDT-style live collaborative editing on the same file. Conflict surface remains "412 → reload-or-overwrite" at PutFile, "diff-and-resolve" at Save.
- Replacing the coding-agent's pod-side feature-branch clone. That clone is short-lived (one WorkflowRun), single-writer, and not subject to the bug class above.
- Signed tags. Today's CLI flow does not sign; this design does not either.
- Cross-installation rate-limit pooling. Each GitHub App installation / PAT owner has its own 5000/hr budget; we don't share across installs.

## 3. Architecture overview

```
   browser tab(s)
        │
        ▼
┌──────────────────────┐
│ console (React)      │  polls /artifacts/state every 5s while tab is focused
└─────────┬────────────┘     (Page Visibility API; paused on background tabs)
          │ REST (per-user JWT)
          ▼
┌──────────────────────┐    advisory lock around saves
│ asdlc-service (BFF)  │    drafts in Postgres
│   ┌────────────────┐ │    `pg_advisory_xact_lock(hashtext('artifact:'||org||':'||project))`
│   │ artifact_store │ │
│   └────────────────┘ │
└─────────┬────────────┘
          │ REST (Thunder-issued JWT bearer, client_credentials)
          │ NO TOKEN-BEARING GITHUB VALUE CROSSES THIS BOUNDARY
          ▼
┌──────────────────────┐
│ git-service          │   credential resolver + GitHub HTTP client
│   ┌────────────────┐ │   ETag-conditional GETs, rate-limit tracker,
│   │ github_client  │ │   conflict-retry loop with budget
│   └────────────────┘ │
└─────────┬────────────┘
          │ HTTPS
          ▼
       GitHub
       Contents API + Git Data API + Refs/Tags API
```

The clone is gone for `.asdlc/`. Everything that touches `.asdlc/` either reads from Postgres drafts or makes a GitHub API call through git-service.

## 4. Data model

### 4.1 Postgres tables (new, in `wso2cloud.app_factory_*`)

```sql
CREATE TABLE design_drafts (
  org_id       TEXT        NOT NULL,
  project_id   TEXT        NOT NULL,
  content      TEXT        NOT NULL,
  base_tag     TEXT,                                  -- NULL on first-ever save
  version      BIGINT      NOT NULL DEFAULT 1,        -- monotonic per row, drives If-Match
  updated_at   TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  updated_by   TEXT        NOT NULL,                  -- OC user id (subject from inbound JWT)
  PRIMARY KEY (org_id, project_id)
);

CREATE TABLE requirements_drafts (
  org_id       TEXT        NOT NULL,
  project_id   TEXT        NOT NULL,
  file_name    TEXT        NOT NULL,
  content      TEXT        NOT NULL DEFAULT '',
  deleted      BOOLEAN     NOT NULL DEFAULT FALSE,    -- tombstone for cross-tab visibility
  base_tag     TEXT,
  version      BIGINT      NOT NULL DEFAULT 1,
  updated_at   TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  updated_by   TEXT        NOT NULL,
  PRIMARY KEY (org_id, project_id, file_name)
);

CREATE INDEX idx_design_drafts_updated_at      ON design_drafts (updated_at);
CREATE INDEX idx_requirements_drafts_updated_at ON requirements_drafts (updated_at);
```

**Why these fields:**

- `content`: the working bytes the user has edited but not yet saved. For markdown, raw text. For design, canonicalised JSON (same canonicalisation as today's `normalizeDesignJSON`).
- `base_tag`: the tag the draft was *opened against*. Anchors optimistic-concurrency (§9). `NULL` only for first-ever save when no tag exists yet.
- `version`: per-row monotonic. Drives `If-Match` on PutFile to detect cross-tab races. Resets to `1` when the row is freshly created (after save or discard).
- `deleted`: tombstone for cross-tab visibility. Tab A deletes a requirements file → row flips to `deleted=true`. Tab B's next poll sees the tombstone and hides the file from its sidebar. Restore = flip back to `deleted=false`. Both transitions bump `version`.
- `updated_by`, `updated_at`: presence indicator. The poll endpoint surfaces "Anjana edited 12s ago" to other tabs.

### 4.2 Existing tables affected

- `git_repositories.status` — semantics redefined (§14): drop the `cloning` state.

## 5. BFF↔git-service contract

The BFF-facing surface stays the same shape. Two minor changes:

- **`Discard*` returns `{status: "discarded", base_tag: "<latest>"}`** — no content body. Callers refetch via the normal GET path.
- **New endpoint `POST /artifacts/state`** — polls draft state for cross-tab visibility. Body: `{kind: "requirements"|"design"}`. Response: `{files: [{name, version, deleted, updated_at, updated_by}], base_tag, latest_tag, versions_behind}`. Used by the console at ~5s cadence per open editor (paused on background tabs). For `kind=design`, `files` is a single-element array `[{name: "design.json", version, deleted: false, ...}]`.
  - **Authorisation:** caller's JWT subject must be a member of `org` and have read access to `project`; otherwise 403.
  - **Coalescing:** the BFF coalesces concurrent polls from the same `(org, project, user)` within 1s into a single Postgres read.
  - **Rate limit:** 15/min/user (slightly above the 12/min ceiling implied by 5s cadence so a single missed tick doesn't 429).
  - **`versions_behind` definition:** strictly newer tags of the same kind as the draft's `base_tag`. Requirements drafts: count `v<N>` tags where N > base_tag's N. Design drafts based on `vA-B`: count `vN-M` tags where `(N, M) > (A, B)` in lexicographic order; cross-N transitions count one each.
- **New endpoint `POST /artifacts/<kind>/retry-tag`** — used when a save returns `{status: "saved_without_tag"}` (§9.3). Body: `{commit_sha}`. Re-attempts tag creation against `commit_sha` with a fresh `ListMatchingRefs`. Idempotent; refuses if `commit_sha` is not an ancestor of current main.

The git-service↔GitHub HTTP shape grows; methods live in `git-service/services/github_client.go`:

```
GetContents(owner, repo, path, ref) -> {content, blobSHA, etag, status}
PutContents(owner, repo, path, branch, msg, content, expectedSHA?, author, committer) -> {commitSHA}
DeleteContents(owner, repo, path, branch, msg, expectedSHA, author, committer) -> {commitSHA}

GetRef(owner, repo, ref) -> {sha}
GetCommit(owner, repo, sha) -> {treeSHA}
GetTree(owner, repo, treeSHA, recursive bool) -> [{path, mode, type, sha, size}]
GetBlob(owner, repo, blobSHA) -> {content, encoding}

CreateBlob(owner, repo, content) -> {sha}
CreateTree(owner, repo, baseTree, entries) -> {sha}
CreateCommit(owner, repo, msg, treeSHA, parents, author, committer) -> {sha}
UpdateRef(owner, repo, ref, sha, force bool) -> {sha}            // PATCH /git/refs/heads/<branch>, force=false

CreateTagObject(owner, repo, tag, msg, objectSHA, type, tagger) -> {sha}
CreateTagRef(owner, repo, tag, tagSHA) -> {sha}
ListMatchingRefs(owner, repo, prefix) -> [{ref, sha}]
```

All methods accept the resolved credential as an internal parameter resolved per call from the credential resolver; the BFF never sees a token. All methods read `X-RateLimit-Remaining` / `X-RateLimit-Reset` headers into the per-installation `RateLimitTracker` (§12).

## 6. Read flow

### 6.1 Single file

`GET /artifacts/<kind>/files/<name>?ref=<draft|main|v<N>>` on git-service:

- `ref=draft` (default for editor): return draft row content if it exists; otherwise fall through to `ref=main`. If `deleted=true`, return 404 with `X-Asdlc-Draft-Deleted: 1` header so the console renders the right empty state.
- `ref=main`: `GetContents(path, "main")`. Cache key `(owner, repo, path, "main")`. ETag-conditional. Soft TTL 30s revalidate-via-304; hard TTL 5min unconditional refetch.
- `ref=v<N>`: `GetContents(path, tag)`. Cache forever (immutable).

Response includes `{content, blobSHA, base_tag, latest_tag, versions_behind, etag}` so the editor has everything it needs for §10's staleness banner without a second call.

### 6.2 Directory listing

`GET /artifacts/requirements?ref=...`. Implementation: one `GetTree(repo, treeSHAOfRef, recursive=true)` filtered to `.asdlc/requirements/*.md`. Tree call is one round-trip per ref. Cached on the same TTL as single-file reads keyed by `(owner, repo, treeSHA)` (immutable: tree SHA implies content).

For `ref=draft`, overlay draft rows on top of the `main` listing: drafts hide tombstoned files, surface drafts-not-yet-on-main, and tag every file with `{drafted: bool, deleted: bool, version}`.

### 6.3 Bundle read for the agents-service

`POST /artifacts/bundle` on git-service. Body: `{ref, paths: [...]}`. Implementation:

- `ref=draft`: read draft rows for the listed paths from Postgres; for any path with no draft row, fall through to a `ref=main` read of just that path. Return combined map.
- `ref=main` / `ref=v<N>`: `GetTree(recursive=true)` once; resolve listed paths to blob SHAs; parallel `GetBlob` for each. Conditional-cached.

Bundle reads are classified **essential** for rate-limit purposes (§12) — they fire mid-skill-execution and a 503 aborts the run.

### 6.4 Cache

Two-layer in-memory LRU inside git-service, keyed `(owner, repo, path-or-treeSHA, ref)`. Default size **8000 entries** (configurable via `GITSERVICE_ARTIFACT_CACHE_SIZE`). LRU eviction is the only eviction mechanism — even tag-pinned entries fall off the back of the LRU when capacity is reached.

- **Soft TTL (30s):** revalidate via `If-None-Match: <etag>`. A 304 response is free and counts as a hit. A 200 response replaces the entry.
- **Hard TTL (5min):** unconditional refetch; ETag may be returned but is not used. Bounded write-back: a single concurrent revalidation per key (singleflight) so a herd of tabs polling on the same project doesn't fan out.
- **Tag-pinned entries:** infinite TTL within the LRU; immutable on GitHub for our purposes (we never delete or move them).

**Ref → commit → tree resolution chain.** A second LRU (smaller, ~2000 entries) caches the mapping `(owner, repo, ref) → {commitSHA, treeSHA}` so that `GetTree(treeSHAof(v2), recursive=true)` doesn't pay an extra `GetRef + GetCommit` per save. Tag refs cache forever; branch refs follow soft/hard TTL. This is the mapping the save flow walks for the `base_tag → base_tree → blob SHAs` chain in §8.3.

**Invalidation triggers (in priority order):**

1. **Own writes** — when `PutContents` / `UpdateRef` returns the new commit SHA, the cache for affected paths is *upserted* with the new content and ETag before the BFF sees a success response. Upsert (not delete-and-repopulate) prevents thundering-herd misses.
2. **Inbound `push` webhook on main** — invalidate `(owner, repo, *, "main")` entries. Skipped if the webhook's `after` SHA equals the last own-write commit SHA (de-duplicates self-induced webhooks).
3. **Periodic sweep** — every 60s the BFF's "open editor" set drives a conditional revalidation of cached entries for each open project. Closes the smee.io-flakiness gap. Bounded cost: only revalidates entries already in the LRU. **Stagger:** revalidations are spread evenly across the 60s window (~`editor_count / 60` calls per second) so a content-storm doesn't produce a synchronised burst of 200 responses against the rate budget. **Cold-entry deferral:** under soft cap (`remaining < 500`), entries with no recent editor reads (last 30s) defer their revalidation to the next cycle; hot entries still refresh.

## 7. Edit flow (PutFile / Delete)

### 7.1 PutFile

`PUT /artifacts/<kind>/files/<name>` on git-service:

```
1. Resolve owner/repo from project record.
2. Resolve base_tag: caller may pass it, else fall back to "latest" computed via ListMatchingRefs.
3. Begin Postgres transaction.
4. UPSERT into <kind>_drafts:
     INSERT INTO <kind>_drafts (org_id, project_id, file_name?, content, base_tag, version, updated_by)
       VALUES (..., 1, $user)
     ON CONFLICT (org_id, project_id [, file_name])
       DO UPDATE SET content = EXCLUDED.content,
                     deleted = FALSE,
                     base_tag = EXCLUDED.base_tag,
                     version = <kind>_drafts.version + 1,
                     updated_at = NOW(),
                     updated_by = EXCLUDED.updated_by
       WHERE <kind>_drafts.version = $if_match;
5. Check RowsAffected. If 0 -> 412 Precondition Failed with current row state in body.
6. Commit. Return {version, etag = version, base_tag, latest_tag, versions_behind}.
```

The `If-Match: <version>` header is the cross-tab race detector. Two tabs editing the same file race only at `PutFile`; the loser's next PUT returns 412 and the console offers reload-or-overwrite.

First PutFile after a save or discard sends `If-Match: 0` ("expect no row") so the UPSERT can either insert a fresh row at `version=1` or detect that someone else has already raced ahead.

**Postgres semantics check (for the reader implementing this):** with `If-Match: 0`, the `WHERE drafts.version = 0` clause **only fires on the UPDATE branch** of `ON CONFLICT`. So:
- Row doesn't exist → INSERT branch fires unconditionally → row created at `version=1`, returns `RowsAffected=1`. ✓
- Row exists at `version >= 1` → UPDATE branch evaluates `WHERE version=0` → false → `RowsAffected=0` → 412. ✓
- Row doesn't exist AND `If-Match: N > 0` (e.g. editor's local state survived a Save that deleted the row) → INSERT branch fires unconditionally → row created at `version=1`, returns `RowsAffected=1`. The editor receives `{version: 1}` and trusts the server per the editor contract below. Mild counterintuitive (the editor thought it was at N), but consistent.

**Wire contract:** the BFF **always sends `If-Match` as a literal integer**, including `0` when expecting no row. A missing or empty header is rejected with 400 — never inferred as "no precondition." This avoids the failure mode where a malformed client lets any concurrent writer win on a freshly recreated row.

**Editor contract (load-bearing):** the editor always trusts `version` from the most recent server response and never increments locally. The Save response (§8) returns `{new_base_tag, draft_version: 0}` so the editor knows the row is gone and resets its local `If-Match` clock. The next PutFile after Save sends `If-Match: 0`.

### 7.2 Delete (tombstone)

`DELETE /artifacts/requirements/files/<name>` on git-service:

```
1. UPSERT requirements_drafts row with `deleted=TRUE, content=''`,
   bumping version, respecting If-Match.
2. Return {version}.
```

The actual GitHub delete happens at Save. Until then, the tombstone is visible to other tabs via `/artifacts/state`.

Restore (= un-delete) is a normal PutFile with non-empty content; the UPSERT sets `deleted=FALSE`.

`requirements.md` is the main document and **cannot be tombstoned**. The git-service rejects a delete on it with `400 invalid argument` (parity with today's `artifact_service.go:368`).

### 7.3 No advisory lock on edits

PutFile is high-frequency. We rely on `version` + `If-Match` for cross-tab safety and don't take the per-project advisory lock — that would serialise editing across tabs unnecessarily.

## 8. Save flow

### 8.1 Decision: which GitHub API path

```
chooseSavePath(draft):
  if multi-file:                         return git_data_api
  if single-file && content_bytes < 900_000:  return contents_api
  else:                                  return git_data_api
```

900 KB ≈ 1 MB / 1.33 (base64 inflation cap on Contents API). Empirically design.json is ~10-50 KB so the common path is Contents API.

### 8.2 Contents API path (single small file)

Two named SHAs in the flow:

- **`precondition_sha`** — blob SHA of the file at `draft.base_tag`. This is the value sent as `sha` to `PutContents` to assert the OCC invariant *"the file's bytes on main right now are what they were at base_tag"*. When no per-file conflict exists, `precondition_sha == current_main_blob_sha` and either works as the PutContents `sha` parameter.
- **`current_main_blob_sha`** — blob SHA of the same file at current main. Used for unchanged-detection (draft bytes already on main) and for the per-file conflict check (`current_main_blob_sha != precondition_sha` means the file moved since base_tag).

```
BEGIN TXN
  pg_advisory_xact_lock(hashtext('artifact:'||org||':'||project))

  // Read draft inside the lock so unchanged-detection sees consistent state.
  draft = SELECT * FROM design_drafts WHERE org=? AND project=?
  IF NOT EXISTS(draft):
    RETURN 200 {status: "unchanged", tag: latest_tag, draft_version: 0}

  // Force a conditional revalidation of the tag list inside the txn
  // so nextDesignTag() doesn't collide on a stale latest_tag (§10.2).
  tags = ListMatchingRefs(prefix="tags/v", conditional=true)   // 0–1 calls (304 = free)

  // Compute both anchors.
  IF draft.base_tag IS NULL:
    // First-ever save — no precondition (file doesn't exist yet).
    precondition_sha = nil
    current_main_blob_sha = nil  // GetContents(path, "main") returns 404
  ELSE:
    precondition_sha       = blobSHA at draft.base_tag for path      // cached forever
    current_main_blob_sha  = blobSHA at main for path                // conditional GET (304 = free)

  draft_blob_sha = computeBlobSHA(draft.content)   // local hash, no GitHub call

  // Unchanged-detection: draft bytes already match what's on main.
  // Anchor on CURRENT MAIN, not base_tag, so we catch "main moved to same content"
  // via a different path and don't return a stale tag.
  IF draft_blob_sha == current_main_blob_sha:
    DELETE FROM design_drafts WHERE ...
    cache.invalidate_state()
    RETURN 200 {status: "unchanged", tag: latest_tag, draft_version: 0}

  // Per-file conflict: file moved on main since base_tag.
  IF precondition_sha != current_main_blob_sha:
    RETURN 409 {kind: "file_conflict", file: path,
                current_main_blob_sha, latest_tag,
                preserved_draft: true}

  // Safe to save.
  res = PutContents(path, "main",
                    content        = draft.content,
                    sha            = precondition_sha,  // omit if precondition_sha is nil (first save)
                    author         = oc_user,
                    committer      = cred_identity,
                    message        = "Update design")
  // On 409 from PutContents -> retry loop (§9.3).
  // On success: res.commitSHA is the new HEAD of main.

  // Annotated tag (two-step). N comes from the latest requirements tag; M is incremented.
  // On 422 from CreateTagRef (tag already exists — only possible if our tag-list cache
  // was wrong despite the conditional revalidate above), retry the tag step with a fresh
  // ListMatchingRefs and the next available M.
  tag = nextDesignTag(tags, requirementsN = parseN(draft.base_tag) or latestRequirementsN(tags))
  tagObject = CreateTagObject(tag.name, message=tag.message,
                              objectSHA=res.commitSHA, type="commit", tagger=oc_user)
  CreateTagRef(tag.name, tagObject.sha)    // retry-on-422

  // Cache: upsert the file's main-ref entry with new content + new blob SHA + new ETag.
  // PutContents response body includes content.sha (the new blob SHA); use it directly.
  // Invalidate the dir-level tree cache (Contents API doesn't return tree SHA — repopulate on next read).
  cache.upsert((owner,repo,path,"main"), {content: draft.content, blobSHA: res.content.sha, etag: res.etag})
  cache.invalidate_tree((owner,repo,"main"))

  DELETE FROM design_drafts WHERE ...
COMMIT

RETURN 200 {tag, status: "approved", new_base_tag: tag, draft_version: 0,
            latest_tag: tag, versions_behind: 0}
```

The save response's `draft_version: 0` tells the editor to reset its `If-Match` clock; the next PutFile sends `If-Match: 0` (§7.1).

### 8.3 Git Data API path (multi-file requirements)

```
BEGIN TXN + advisory_xact_lock

  drafts = SELECT * FROM requirements_drafts WHERE ...
  IF NOT EXISTS(drafts):
    RETURN 200 {status: "unchanged", tag: latest_tag, draft_version: 0}

  // Force-revalidate the tag list inside the txn (§10.2) so nextRequirementsTag
  // doesn't collide on stale latest_tag.
  tags = ListMatchingRefs(prefix="tags/v", conditional=true)   // 0–1 calls

  // Resolve refs → trees. ref-resolution cache (§6.4) makes the common path cheap.
  current_main_sha = GetRef("heads/main").sha                       // 0–1 calls (cached)
  current_tree_sha = GetCommit(current_main_sha).treeSHA            // 0–1 calls (cached)
  main_tree        = GetTree(current_tree_sha, recursive=true)      // 0–1 calls (cached)

  IF every draft.base_tag IS NULL:    // first-ever save
    base_tree = {}                                                  // 0 calls
  ELSE:
    base_tag = drafts[0].base_tag      // all drafts share base_tag (asserted at PutFile time)
    base_commit_sha = GetRef("tags/"+base_tag).sha                  // 0–1 calls (cached forever)
    base_tree_sha   = GetCommit(base_commit_sha).treeSHA            // 0–1 calls (cached forever)
    base_tree       = GetTree(base_tree_sha, recursive=true)        // 0–1 calls (cached forever)

  // 1. Filter no-op tombstones: a deletion of a file that isn't on main is a no-op.
  //    GitHub returns 422 if we send {sha: null} for a path that doesn't exist in base_tree.
  drafts = filter(drafts) by:
    NOT (draft.deleted AND main_tree.get(draft.file_name) IS NULL)

  // 2. Per-file conflict check: for each remaining draft, the file's blob on main
  //    must equal its blob at base_tag. If different, the file moved since the user
  //    opened the editor → real conflict.
  conflicts = []
  FOR draft IN drafts:
    base_blob_sha = base_tree.get(draft.file_name).sha  OR null
    main_blob_sha = main_tree.get(draft.file_name).sha  OR null
    IF main_blob_sha != base_blob_sha:
      conflicts.append(draft.file_name)
  IF conflicts:
    RETURN 409 {kind: "file_conflict", files: conflicts,
                latest_tag, preserved_draft: true}

  // 3. Unchanged-detection (must handle the mixed cases explicitly):
  isUnchanged = true
  FOR draft IN drafts:
    main_blob = main_tree.get(draft.file_name)   // may be missing
    IF draft.deleted:
      // After step-1 filter, draft.deleted with missing main_blob is impossible.
      // A real tombstone (file IS on main, we want to delete it) is a CHANGE.
      isUnchanged = false; break
    ELSE:
      IF main_blob IS NULL:
        isUnchanged = false; break    // new file in draft, absent on main = CHANGE
      IF computeBlobSHA(draft.content) != main_blob.sha:
        isUnchanged = false; break    // edited file = CHANGE
  IF isUnchanged:
    DELETE drafts
    RETURN 200 {status: "unchanged", tag: latest_tag, draft_version: 0}

  // 4. Build the commit.
  // 4a. Blobs: only create blobs for files where draft.content's blob SHA
  //     differs from what's already on main (skips redundant CreateBlob calls).
  new_blobs = {}
  parallel FOR draft IN drafts WHERE NOT draft.deleted:
    draft_blob_sha = computeBlobSHA(draft.content)
    main_blob_sha  = main_tree.get(draft.file_name)?.sha
    IF draft_blob_sha == main_blob_sha:
      new_blobs[draft.file_name] = main_blob_sha   // reuse, no CreateBlob
    ELSE:
      new_blobs[draft.file_name] = CreateBlob(content=draft.content).sha

  // 4b. Tree entries. Tombstones expressed as sha:null. (Step-1 filter already
  //     removed no-op tombstones, so every sha:null here is a real deletion.)
  entries = []
  FOR draft IN drafts:
    path = ".asdlc/requirements/" + draft.file_name
    IF draft.deleted:
      entries.append({path: path, mode: "100644", type: "blob", sha: null})
    ELSE:
      entries.append({path: path, mode: "100644", type: "blob", sha: new_blobs[draft.file_name]})

  new_tree = CreateTree(base_tree=current_tree_sha, entries=entries)
  new_commit = CreateCommit(msg="Update requirements",
                            treeSHA=new_tree.sha,
                            parents=[current_main_sha],
                            author=oc_user, committer=cred_identity)

  // 5. Atomic ref update — GitHub-side CAS.
  UpdateRef("heads/main", new_commit.sha, force=false)
  // On 422 -> retry loop (§9.3): refresh main, re-run conflict check, retry once.

  // 6. Annotated tag. retry-on-422 in case ListMatchingRefs cache was wrong.
  tag = nextRequirementsTag(tags)
  tagObject = CreateTagObject(tag.name, "Requirements v<N>", new_commit.sha, "commit", oc_user)
  CreateTagRef(tag.name, tagObject.sha)    // retry-on-422

  // 7. Cache updates. Upsert each affected file's main-ref content; invalidate dir tree.
  FOR draft IN drafts WHERE NOT draft.deleted:
    cache.upsert((owner,repo,path,"main"), {content: draft.content, blobSHA: new_blobs[draft.file_name]})
  FOR draft IN drafts WHERE draft.deleted:
    cache.evict((owner,repo,path,"main"))
  cache.invalidate_tree((owner,repo,"main"))

  DELETE drafts
COMMIT

RETURN 200 {tag, status: "approved", new_base_tag: tag, draft_version: 0,
            latest_tag: tag, versions_behind: 0}
```

**Per-call cost (5 files, fully cold cache):** 1 GetRef + 1 GetCommit(main) + 1 GetTree(main) + 1 GetRef(base_tag) + 1 GetCommit(base) + 1 GetTree(base) + 5 × CreateBlob + 1 CreateTree + 1 CreateCommit + 1 UpdateRef + 1 ListMatchingRefs + 1 CreateTagObject + 1 CreateTagRef = **20 calls**. Steady state after first save (everything but the writes is conditional and serves 304s): ~10 billed calls per save.

**Unchanged-detection rationale** (the explicit pseudo-code above): tombstones for files absent on main are filtered out in step 1, so by step 3 every tombstone is a real deletion (= CHANGE). New files in draft (no `main_blob`) are CHANGE. Edited files whose draft blob differs from main are CHANGE. Only the all-files-match-main case is `unchanged` → returns the latest tag without bumping.

### 8.4 First-ever save

`base_tag IS NULL` in the draft row. Implications:

- Contents API path: omit the `sha` parameter from `PutContents`. GitHub creates the file.
- Git Data API path: there is still a current main (the initial commit produced by repo bootstrap). `base_tree = null` is invalid; use `current_tree_sha` as base. No per-file conflict check is needed (nothing exists yet); skip the base_tree fetch entirely.
- Unchanged-detection: trivially "changed".
- Tag: start at `v1` for requirements, `v1-1` for design (parented on the just-created `v1`).

## 9. Concurrency model

### 9.1 Per-process serialisation

The save flow runs inside a Postgres transaction holding `pg_advisory_xact_lock(hashtext('artifact:'||orgID||':'||projectID))`. This is the same pattern used today by `services/task_state.go` and `services/webhook/projector.go`. The lock:

- Prevents two saves on the same project from interleaving their unchanged-detection vs. blob-write phases inside one BFF replica.
- Is released automatically on transaction end (commit or rollback) — no leaked locks on panic.
- Crosses replicas because it's database-side: any BFF replica taking the lock blocks any other replica's same-project save.

PutFile and Delete do **not** take this lock — they rely on `If-Match: <version>` instead.

### 9.2 Optimistic concurrency anchors

There are two distinct named SHAs, used by two different decisions:

- **`precondition_sha`** = `blobSHA(file at draft.base_tag)`. Sent to `PutContents` as its `sha` parameter. A mismatch — surfaced by GitHub as a 409 — means another commit moved the file since the user opened the editor.
- **Unchanged-detection compare**: `blobSHA(draft.content)` vs `blobSHA(file at current main)`. If equal, return the **latest tag**, not base_tag's tag — this handles "remote arrived at the same content via a different path."

These were conflated in earlier drafts; they live at different layers and answer different questions.

### 9.3 Retry policy

When the GitHub-side CAS fails (`PutContents` 409, `UpdateRef` 422, or `CreateTagRef` 422 because another save claimed the next tag name), main or the tag list has moved between our read and our write. We retry:

```
attempts = [50ms ±50% jitter, 200ms ±50%, 800ms ±50%]
budget   = leaky-bucket 6 retries per (org, project) per 60s

on conflict (PutContents 409 or UpdateRef 422):
  if budget exhausted -> 409 to BFF immediately
  else:
    refresh precondition_sha and current_main_blob_sha
    re-run per-file conflict check; if files now conflict -> 409 with file list
    else retry with new SHAs

on tag collision (CreateTagRef 422):
  always retry (no leaky-bucket budget; tag collisions can only come from external pushers
  because the advisory lock serialises in-project saves).
  Fetch ListMatchingRefs fresh; recompute nextTag(). Give up after 3 attempts.

on CAS final failure:
  -> 409 to BFF with {current_main_sha, latest_tag, preserved_draft: true}

on tag-collision exhaustion (3 tag attempts failed BUT the commit DID land on main):
  -> 200 to BFF with {status: "saved_without_tag", commit_sha,
                      warning: "tag claim contested; manual retry available"}
     Console shows a one-time toast: "Saved, but couldn't claim version label. Retry?"
     The retry button posts to /artifacts/<kind>/retry-tag (§5) which re-attempts
     tag creation against `commit_sha` with a fresh ListMatchingRefs.
     **Draft-row lifecycle on this path:** the draft row is DELETED before returning
     200 (the bytes are on main; a lingering draft would reappear in the editor as
     "unsaved changes" against current main). Retry-tag therefore operates on
     `commit_sha` alone and recomputes the next available tag fresh.
```

Each ref-CAS retry costs +1 Contents/Tree API call per anchor refresh. With the budget cap, a thrashing project consumes at most ~6×N≈60 calls/min — comfortably within the rate budget but enough to fail-fast rather than spin.

The BFF surfaces a 409 to the console as the **ConflictBanner**: *"This project moved while you were editing. Reload to see the latest, or overwrite anyway. Your draft is preserved."*

### 9.4 Cross-replica race summary

| Scenario | Mechanism | Outcome |
|---|---|---|
| Two saves, same project, two BFF replicas | Postgres advisory lock (cross-replica) | Serialised |
| Two saves, same project, one BFF replica, two requests | Same advisory lock | Serialised |
| Save vs. external push (CI bot, coding-agent merge) | GitHub CAS on `UpdateRef` | Retry loop, then 409 |
| Save with stale `expected_sha` because base_tag was N versions ago | GitHub CAS rejects | Per-file conflict check on retry |
| Two `PutFile`s on the same draft row | Postgres `If-Match` (version) | 412 to loser; UI offers reload-or-overwrite |
| Two tabs viewing same project (one edits, one doesn't) | `/artifacts/state` poll | Observer sees tombstones / version bumps within 5s |

## 10. Cross-tab + multi-user UX

### 10.1 Live state polling

The console polls `POST /artifacts/state` every 5s per open editor. Body: `{kind}`. Response:

```json
{
  "files": [
    { "name": "requirements.md", "version": 4, "deleted": false,
      "updated_at": "2026-05-11T16:44:35Z", "updated_by": "user:abc123" }
  ],
  "base_tag": "v2-1",
  "latest_tag": "v2-3",
  "versions_behind": 2
}
```

The poll endpoint reads only from Postgres (no GitHub call) so it's free against the rate budget. The `latest_tag` value piggybacks on the cached `ListMatchingRefs` (refreshed at most every 30s by the periodic sweep + own-writes).

Console behaviour:
- Polling is **paused while the tab is backgrounded** (Page Visibility API). Resumes on `visibilitychange` → `visible` with an immediate poll, then 5s cadence.
- Track every open editor's `version` per file. If a poll returns a higher `version` than the local editor, show *"Anjana is editing — click to reload"* banner.
- If a poll returns `deleted=true` for a file currently open in the editor, surface *"This file was deleted in another tab"* and offer Restore.
- **LineageLabel / StalenessChip composition** (two sibling components, not one):
  - `<LineageLabel parent={design.sourceSpec || base_tag} />` — static fact, decoded from the design tag name. Shows *"Based on requirements v2"*.
  - `<StalenessChip versionsBehind={state.versions_behind} latestTag={state.latest_tag} />` — dynamic comparison against latest tag. Shows *"current v2-3"* when behind, nothing when up to date.
- Both components live in `console/src/components/artifacts/` and consume different data — `LineageLabel` from the artifact GET (`design.sourceSpec`), `StalenessChip` from `/artifacts/state` (`versions_behind`, `latest_tag`).

### 10.2 Staleness handling — per-file conflict, not version-count gate

**Decision (chosen this round):** never block save just because `versions_behind > 0`. Instead, the save flow does a per-file conflict check (§8.2 step "Per-file conflict") and only blocks when a file the user is editing has actually moved on main since `base_tag`.

This avoids two failure modes:
- A version-count gate would force users editing for hours to abandon their draft when an unrelated coding-agent merge bumps `versions_behind`, even though their file is untouched.
- A "save anyway" gate without per-file conflict checking would silently overwrite work on the same file that someone else committed.

The per-file check + Git Data API's `base_tree=<current main>` together preserve unrelated work automatically: the new commit's tree contains everything that's on current main *except* the files the user edited, which carry the user's content.

**Worked example.** User edits `requirements.md` from `v2`. Another user saves a brand-new file `user-stories.md` (= `v3`). User clicks Save. §8.3 flow:

1. Drafts: 1 row (`requirements.md`). No row for `user-stories.md`.
2. `main_tree` includes `requirements.md` (unchanged blob since v2) + the new `user-stories.md`.
3. Per-file conflict iteration only loops drafts — i.e. only `requirements.md`. `main_blob[requirements.md] == base_blob[requirements.md]` → no conflict.
4. `CreateTree(base_tree = current_tree_sha, entries = [{requirements.md, sha = new blob}])` → result tree carries `user-stories.md` forward AND lands the edited `requirements.md`.
5. New tag is `v4`. The in-txn `ListMatchingRefs` revalidate caught that `v3` already exists. It **narrows but does not close** the tag-claim race — another external pusher could claim `v4` between our `ListMatchingRefs` and our `CreateTagRef`. §9.3's CreateTagRef retry loop handles that residual case.

`user-stories.md` is preserved automatically. This is the property a version-count gate would have broken by forcing a refresh-and-rebase round.

### 10.3 No live collaborative editing on one file

Out of scope. If two tabs edit the same file:
- Tab A bumps version 1 → 2 on its PutFile.
- Tab B's editor still thinks version is 1; its next PutFile fails 412.
- Tab B's UI offers reload (lose tab B's edits, accept A's) or overwrite (re-fetch fresh version, replay tab B's edits on top, re-submit).

This is good enough for the SDLC editor surface; serious live editing would need a CRDT layer that's not in scope.

## 11. Author attribution

GitHub Contents API and Git Data API both accept explicit `author` and `committer` objects. We use them:

- `committer = credential identity`. App mode: bot name + bot email. PAT mode: PAT owner's name + email. Drives GitHub-side auth and is what GitHub shows in the "Committed via..." flair.
- `author = OC user`. The console-side user who clicked Save. Mapped via:
  - **V1 fallback** (no thunder→github mapping available): `name = <oc display name>`, `email = <oc-user-id>@users.app-factory.dev`. Readable, attributable, never collides with a real GitHub email.
  - **V2**: when a thunder→github identity mapping is in place, use the real GitHub identity. Doesn't gate V1.
- `tagger` on the annotated tag object = same as `author`. Consistent provenance.

`asdlc-service/services/commit_identity.go` already resolves the credential identity — keep it as-is for `committer`. Add a sibling resolver in a new file:

```go
// asdlc-service/services/author_identity.go
func authorIdentity(ctx context.Context, ocUserID, ocDisplayName string) (name, email string) {
    if ocUserID == "" {
        return "ASDLC User", "user@asdlc.dev"   // never happens in real flow; defensive
    }
    return ocDisplayName, fmt.Sprintf("%s@users.app-factory.dev", ocUserID)
}
```

The OC user ID + display name come from the inbound JWT (already extracted by the BFF auth middleware) and flow into the save request:

```go
type SaveArtifactRequest struct {
    Message           string
    CommitterOrgID    string   // resolves committer identity
    AuthorUserID      string   // resolves author identity
    AuthorDisplayName string
}
```

Extending `commitIdentity` to return two identities would require threading the OC user ID through every save-flow caller. The sibling shape is cleaner.

## 12. Rate limit budget

Each GitHub App installation token gets **5000 requests/hour**. PAT users also get 5000/hr per user.

### 12.1 Steady-state estimate

- Per save (Contents path, ~3 GitHub calls + 2 tag calls + cache prewarm) ≈ **5 calls**.
- Per save (Git Data path, 5 files) ≈ **10–14 calls**.
- Per editor read (cached, conditional) ≈ **0.5 calls** average (most reads hit local cache or revalidate as 304).
- Per agents-service skill run (5-file bundle) ≈ **6 calls** (tree + 5 blobs, cached after first).
- `/artifacts/state` poll: **0 calls** (Postgres-only).

Worst case: 10 active editors, save every minute, 2 skill runs/min ≈ 50 + 12 = **62 calls/min** = **3720/hr**. Headroom: ~1300/hr for retries, conflict refreshes, periodic sweeps.

### 12.2 `RateLimitTracker`

Per-installation gauge in git-service:

```go
type RateLimitTracker struct {
  remaining map[installationID]int   // last seen X-RateLimit-Remaining
  resetAt   map[installationID]time.Time
  mu        sync.RWMutex
}

func (t *RateLimitTracker) Observe(installationID string, h http.Header) { ... }
func (t *RateLimitTracker) Headroom(installationID string) (remaining int, resetIn time.Duration)
```

Updated from every response's headers. Exposes a Prometheus gauge.

### 12.3 Backpressure

```
remaining >= 500    -> normal operation
remaining < 500     -> 503 with Retry-After on NON-ESSENTIAL writes-only.
                       All reads (including periodic sweep) continue if conditional.
remaining < 50      -> 503 with Retry-After on writes too.
                       Conditional reads still permitted (304s are free).
```

**Classification:**
- **Essential reads** (always allowed, even at hard cap): `/artifacts/bundle`, single-file editor reads, `ListMatchingRefs` for tag resolution.
- **Conditional reads** (always allowed because their expected outcome is 304): periodic-sweep revalidations. A 304 is free against the budget per GitHub policy; a 200 response means content actually changed, which is information we need (otherwise we serve stale). So the sweep is *not* a candidate for backpressure — it's load-bearing for cache correctness.
- **Writes** (PutContents, CreateBlob/Tree/Commit, UpdateRef, CreateTagObject/Ref): soft-capped at <500 remaining, hard-capped at <50.

Cache 304s do **not** count against the limit per GitHub policy — confirmed in the cache layer to rely on this.

### 12.4 Multi-replica BFF

For current single-replica deployment, each git-service replica's local view of `RateLimitTracker` is authoritative. For multi-replica (future), the gauge needs to be shared — Redis or a Postgres row would work, with a small write-on-every-response overhead. Deferred but flagged as a known limitation.

## 13. Observability

New metrics (Prometheus, scraped by the OC ObservabilityPlane):

| Metric | Type | Labels | What it measures |
|---|---|---|---|
| `git_service_github_api_calls_total` | counter | `op`, `outcome` (success / 304 / 4xx / 5xx / rate_limited) | Per-call accounting |
| `git_service_github_rate_limit_remaining` | gauge | `installation_id` | Last observed remaining quota |
| `git_service_artifact_save_duration_seconds` | histogram | `kind`, `outcome` | End-to-end save latency |
| `git_service_artifact_save_conflicts_total` | counter | `kind`, `retries` (0/1/2/3/exhausted) | CAS retry distribution |
| `git_service_artifact_cache_total` | counter | `kind`, `outcome` (hit / miss / stale / revalidated_304) | Cache efficacy |
| `git_service_draft_age_seconds` | histogram | `kind` | How long drafts live before save/discard |

Logs (DEBUG, one line per save):

```json
{
  "op": "save_design",
  "project": "hello-world-1778516159",
  "base_tag": "v1-2",
  "new_tag": "v1-3",
  "conflict_retries": 0,
  "github_calls": 5,
  "total_ms": 487
}
```

Alerts:
- `rate_limit_remaining < 200` for any installation → page on-call (we will silently start failing in minutes).
- `artifact_save_conflicts_total{retries="exhausted"}` rate > 1/min for a single project → likely thrashing PR bot or wedged editor.

## 14. `git_repositories.status` redefinition

Before: `pending → cloning → ready → failed`. The `cloning` state is gone.

After:

| State | Meaning |
|---|---|
| `pending` | Credentials not yet set, or repo creation in flight. |
| `ready` | GitHub repo exists, credentials resolve, `GET /contents/.asdlc/` returns 200 or 404 (both fine). |
| `error` | Credentials invalid, or repo not accessible. `error_message` carries detail. |

Migration: an in-place backfill flips every `ready` row to `ready` (no-op) and every `cloning` row that has a clone on disk to `ready`. Failed/orphaned `cloning` rows surface as `error`.

## 15. Cutover plan

Six steps, each its own deploy. Steps 1–3 are reversible; step 6 is the point of no return.

**Step 1: GitHub-API reads behind a feature flag.**
- Add `gitservice.NewArtifactsReadPath=false` env var; when true, read endpoints hit the new path.
- Disk reads still available. Side-by-side ~3 days. Compare cache hit rates and latencies in observability.
- Rollback: flip flag to false.

**Step 2: Backfill prep.**
- Add a maintenance command `git-service migrate-drafts` that walks every `git_repositories.status='ready'` row and:
  1. Resolves the live `ClonePath` (re-reading the record under the per-project lock, in case `REPO_BASE_PATH` changed).
  2. Runs `git status --porcelain` on the clone. Untracked + modified files under `.asdlc/` → snapshot into draft tables with `base_tag = <latest local tag>, updated_by = "migration"`.
  3. Runs `git log <latest-tag>..HEAD -- .asdlc/` to find commits that landed on local main but were never tagged (the `pushAllTags` failure mode). If any → **create the missing tag** during backfill (Tag API), not as a draft snapshot. This closes the lost-tag case.
  4. Idempotent on re-run.
- Run dry-run first; report per-project status. Fix outliers manually.

**Step 3: Drafts + Saves atomic flip.**
- Single deploy that enables `gitservice.NewArtifactsWritePath=true`. This **simultaneously**:
  - Routes `PutFile` / `Delete` to Postgres draft tables.
  - Routes `Save*` to GitHub API path.
- Drafts and saves cannot ship independently — the save path reads drafts, not the working tree.
- Run `git-service migrate-drafts` immediately before the deploy.
- 30s graceful drain on BFF + git-service: SIGTERM → finish in-flight saves, reject new ones, exit.
- **Rollback runbook (production)** — drafts MUST be replayed back to the working tree before old code can find them:
  1. Revert the deploy (Helm rollback / Flux suspend).
  2. Wait for old code to come up healthy (the working-tree clone code still exists at this point — Step 6 cleanup hasn't run).
  3. Run `git-service migrate-drafts-back`: walks `*_drafts` tables, writes each draft's content into the per-project clone's working tree (re-cloning if missing), then truncates the draft tables. **Idempotent** — re-running on an empty draft table is a clean no-op.
  4. Verify with a manual save on a canary project before announcing recovery.
- **Known edges of `migrate-drafts-back`:**
  - **Destructive to the working tree.** Replay overwrites working-tree bytes with the (newer) draft content. The verify-on-canary step must include a project with known drafts to confirm the overwrite did the right thing.
  - **Re-clone failure → manual recovery required.** If credentials rotated since Step 2, or the GitHub repo is missing, the re-clone fails. The runbook flags those projects with `status='error'` and surfaces them for manual recovery (per-project: fix credentials, re-clone, then re-run `migrate-drafts-back` for that project only).
  - **`base_tag` lineage loss is accepted.** Working-tree mode has no concept of `base_tag`; users' draft OCC anchors are lost on replay. Until the user re-saves under the working-tree code, conflict detection is "whatever HEAD is." Documented limitation, not a bug.
- **Rollback (dev / staging):** acceptable to skip the replay if losing drafts is tolerable. Surface a banner in the console explaining the loss; do not silently proceed.

**Step 4: Cross-tab polling + staleness UX.**
- Deploy the `/artifacts/state` endpoint and console-side poller. Feature-flag the staleness chip + reload banner.
- Roll out gradually per org if needed.

**Step 5: Stabilisation (1 week).**
- Watch save conflict rate, rate-limit headroom, save p50/p99.
- Tune leaky-bucket parameters if needed.

**Step 5.5: Drain the `cloning` rows.**
- `git_repositories.status='cloning'` rows skipped by Step 2's backfill (which only walked `ready`). Now resolve them:
  1. For each `cloning` row, attempt one final clone (the code path still exists pre-Step-6).
  2. On success → flip to `ready`, run `migrate-drafts` on it, idempotent.
  3. On failure (repo gone, credentials bad) → flip to `error` with explanatory message.
- After this step, no `cloning` rows remain — Step 6's enum drop is safe.

**Step 6: Cleanup deploy.**
- Delete: `ensureCloneReady`, `cloneIntoPath`, `.tmpclone-*`, `PreWarmClones`, `pushAllTags`, `bestEffortFetchTags`, `treesEqualAtPath`, per-project file mutex inside git-service, `migrate-drafts`, `migrate-drafts-back`.
- Delete: `REPO_BASE_PATH` env var, the disk-volume mount from `deployments-v2/manifests/env-overlays/git-service.yaml`, `CleanupOrphanTmpClones`.
- Drop `cloning` from the `git_repositories.status` enum check constraint.
- This is the point of no return; the clone code is gone, and rollback to working-tree mode is no longer possible.

## 16. Out of scope / future

- **CRDT-based live editing on the same file.** Would replace the 412 + reload UX. Material new layer.
- **Server-Sent Events for cross-tab state.** V1 uses polling at 5s. SSE upgrades latency to ~real-time at the cost of an additional connection budget.
- **Per-org rate-limit pooling.** Today's design treats every installation independently. If an org has multiple installations (multi-repo across accounts) we could pool, but it's not on the roadmap.
- **Signed tags.** Today's CLI flow doesn't sign; V2 doesn't either. If a future compliance requirement lands, sign tags client-side and include the signature in the tag object's message field — non-trivial.
- **Cross-installation budgeting.** Same as above; not in scope.
- **Backup / point-in-time recovery of drafts.** Drafts are by definition unsaved; if Postgres loses a draft (unlikely with the BFF's Postgres deployment) the user re-edits. Acceptable.

### 16.1 Draft GC policy

Draft rows expire **7 days after `updated_at`** with a soft-warn banner ("Your draft will be discarded in 2 days — save or discard") visible in the console from day 5 onward. A nightly Postgres job deletes expired draft rows. Banner copy + cutoffs are tunable; specced here so the first PR doesn't pick numbers arbitrarily.

### 16.2 Canonicalisation before draft write

`ArtifactStore.WriteDesign` today runs `normalizeDesignJSON` on the bytes before persisting them. **The new flow must run the same canonicalisation BEFORE writing to `design_drafts`.** Otherwise two byte-different-but-semantically-equivalent JSON payloads (LLM whitespace drift, key reordering) will:
- Show as different in cross-tab `version` deltas (false "Anjana is editing").
- Compute different blob SHAs in unchanged-detection (false "changed").

Canonicalisation is **content-shape-aware** (JSON for `design.json`; passthrough for markdown). Per the existing service boundary (Phase 2 §1.4 — git-service is artifact-type-aware but not content-shape-aware), canonicalisation correctly lives in BFF, not git-service. The BFF runs `normalizeDesignJSON` on every design PutFile and is a no-op on requirements PutFile.

**Markdown line endings:** requirements files are stored byte-exact (`content TEXT`), including line endings. Two editors writing `\r\n` vs `\n` for the same content will produce different blob SHAs and trigger spurious `version` deltas in cross-tab polls. Mitigation: **the BFF normalises line endings to `\n` on every requirements PutFile** before the draft UPSERT (one-line transform; matches what GitHub's web editor does). Applies to both `.md` and `.excalidraw` (the two extensions accepted under `.asdlc/requirements/` per `allowedRequirementExts`); excalidraw scenes are JSON so the normalisation is semantically a no-op but byte-stabilising for the dedup path. Documented here so we don't litigate it at PR time.

## 17. Decision log

User-confirmed product decisions (this design):

1. **Cross-tab live visibility — in scope.** Tombstones, version polling, presence indicator all in V1. SSE upgrade deferred.
2. **Streaming crash recovery — dropped.** Stream errors require user re-run; no `partial=true` machinery. State machine is simpler.
3. **Staleness handling — per-file conflict, not version-count gate.** Soft-warn via `LineageLabel`'s staleness chip; hard-block save only when a draft file's blob on main differs from its blob at `base_tag`.

Architectural decisions:

- Clone removal scoped to `.asdlc/` only. The coding-agent pod-side feature-branch clone is untouched.
- Drafts in Postgres, not on a remote `drafts` branch or in-memory in BFF.
- Author = OC user (via `<id>@users.app-factory.dev` fallback for V1), committer = credential identity. Same identity used as tag tagger.
- Postgres advisory lock around Save; `If-Match: <version>` around PutFile/Delete.
- Per-file conflict check at Save anchors on `blobSHA(file at base_tag)`; unchanged-detection anchors on `blobSHA(file at current main)`. Two distinct anchors.
- Retry budget: 3 attempts with exponential backoff + per-project leaky bucket (6 retries / project / 60s).
- Cache invalidation order on own writes: GitHub success → upsert cache → commit Postgres → return success.
- Bundle reads classified essential (don't 503 mid-skill).
- Self-induced `push` webhooks deduped against last own-write SHA.

## 18. Resolved implementation notes

- **Author / committer split** — `commit_identity.go` stays as the committer resolver. A new sibling `services/author_identity.go` provides the author resolver. `SaveArtifactRequest` carries `CommitterOrgID`, `AuthorUserID`, `AuthorDisplayName`. Spec'd in §11.
- **Read-vs-state endpoint shape** — `GET /requirements` / `GET /design` continue to return content + lineage; the dynamic `{versions_behind, latest_tag, files[].version, …}` lives behind `POST /artifacts/state`. Sibling endpoint, not a shape change to the existing GETs. Spec'd in §5 and §10.1.
- **Periodic-sweep open-editor set** — BFF maintains an in-memory TTL set keyed on `(orgID, projectID)`. Every `/artifacts/state` poll inserts/refreshes the entry with TTL = 2 × poll cadence (10s). The sweep iterates entries that are still live. No Postgres state needed; sweep is replica-local (each replica sweeps the editors connected to it).
- **Schema migrations** — new tables added in `asdlc-service/database/migrations/`, next sequence number from `git log` of that directory. Schema is `public` (default); database is the existing `app-factory-postgresql` in the `wso2cloud` namespace.
- **Indexes on `*_drafts`** — primary keys are sufficient for editor reads + Save reads. Add `idx_*_updated_at` (already in §4.1) for the GC sweep (§16.1) and `/artifacts/state` presence queries.
