# GitHub Integration — Architecture & Evolution

This document describes how ASDLC integrates with GitHub, the trust model behind that integration, and the three-phase evolution that takes us from today's single shared platform PAT to per-org credentials with the agent driving git directly.

It is forward-looking and intentionally architectural. Implementation specifics — schemas, endpoints, file layouts, ordered engineering steps, acceptance test enumerations — live with the code at implementation time. The purpose of this doc is alignment: any team building toward this should be able to read it once and know which boundaries to honour and which abstractions to converge on.

This file is a sibling of `git-integration.md` (current state). The two converge once Phase 2 lands.

---

## 1. Context

Today, a single platform PAT shared across the entire deployment is used for: creating repos under a hard-coded org, cloning, committing artifacts (spec/design tag pushes), opening issues per task, and a server-mediated submit pipeline that pushes generated code directly to the default branch and triggers a build.

This produces three structural problems:

- **No human review gate.** Generated code lands on the default branch instantly.
- **Two parallel control planes.** MCP carries agent state transitions that GitHub already represents (PR commits, issue comments, merges). Two channels, two ways to drift.
- **Credential reach is the whole platform.** One leak compromises every project of every organisation.

The evolution below addresses all three.

---

## 2. Trust model and posture

The new posture: **the agent holds a real GitHub credential and uses git and `gh` directly.** Server-mediated push is removed. Defence is bounded blast radius, not credential abstinence.

| Surface | Credential reach |
|---|---|
| Today (platform PAT) | Whole platform, all orgs, indefinitely |
| Phase 0 (interim) | Whole platform — accepted as transitional |
| Phase 2, remote flow | One organisation, plus (App mode) per-call short-lived tokens |
| Phase 2, local flow | Whatever the developer's own `gh auth` already has — the platform holds no credential |

Server-mediation was the only honest defence against accidental exfiltration via transcript or prompt injection; the alternatives (in-workspace askpass shims) didn't actually defend. We accept that risk in exchange for a much smaller surface area: no credential-bearing MCP tools, no per-task bearer schemes for git operations, no path-equality bind-mount worktree-locking gymnastics.

### 2.1 Trust boundaries

```
                          ╔═══════════════ external ═══════════════╗
                          ║                                        ║
┌──────────┐   webhook    ║   ┌──────────┐    credential resolve   ║   ┌────────────┐
│  GitHub  │ ──────────── ║ ► │   BFF    │ ─────────────────────── ║ ► │ git-service│
└────┬─────┘              ║   └─────┬────┘                         ║   └─────┬──────┘
     │                    ║         │ orchestration / state        ║         │
     │ git, gh            ║         ▼                              ║         │
     │                    ║   ┌──────────┐  workspace + credential ║         │
     └────────────────────║─► │  Agent   │ ◄──────────────────────────────── ┘
                          ║   └──────────┘                         ║
                          ║                                        ║
                          ╚════════════════════════════════════════╝
                                  trusted internal network
```

The double-line frame is the trust boundary. Inside it: BFF, git-service, agent (running on platform infrastructure or platform-issued workspace). Outside: GitHub, the developer's machine in the local-flow case.

- **Agent ↔ GitHub.** The agent holds a credential bound to one task's reach. Crossing the boundary outward; reach-bounded by §2 posture.
- **GitHub ↔ BFF.** Inbound webhook deliveries, HMAC-validated against an N-of-M secret list. Crossing inward; this is the only inbound channel.
- **Agent ↔ git-service.** Internal-network channel for the credential-refresh endpoint, used to fetch a fresh token mid-run when the kind requires it. Authenticated by a short-lived per-task bearer issued at dispatch.
- **BFF ↔ git-service.** Internal-network. **git-service is the sole holder of GitHub credentials in the platform.** The BFF orchestrates but never sees a token; this is the rule that makes credential storage auditable in one place.
- **Cross-org isolation.** Every operation that touches GitHub is parametrised by `ocOrgId`. The resolver refuses unknown orgs; the webhook receiver rejects events whose payload-derived org binding doesn't match a stored connection (§4.3). A leak of one org's credential cannot be used against another org's repos because no call site reaches GitHub without first naming the org it's acting for.

---

## 3. Core abstractions

These are the load-bearing concepts the rest of the doc assumes.

### 3.1 ComponentTask, issue, branch, PR

A `ComponentTask` is the platform's unit of agent work. It maps 1:1:1:1 to a GitHub issue (durable agent context), a feature branch, and a draft PR. The issue body is what the agent reads at the start; the PR is where humans review at the end.

**Task lifecycle state is derived from GitHub events, not asserted by the agent.** There is no "I'm done" call into the platform; there is `gh pr ready` followed by a human merge, both of which arrive as webhooks. This single property is what justifies removing MCP — the parallel control plane disappears because GitHub already represents every transition the agent could announce.

### 3.2 The credential resolver

git-service exposes a single internal abstraction that, given an organisation, returns a `Credential` usable against GitHub. The credential is polymorphic: callers consume it through a uniform surface, and each kind implements that surface in its own way.

The surface has four observables:

- **Token use.** Callers obtain a token and use it for a GitHub API or git operation.
- **Identity.** Callers reading committer attribution ask the credential for its identity. App-mode credentials answer with the App's bot identity; PAT-mode credentials answer with the PAT owner; the platform-PAT answers with a fixed bot.
- **Repo owner.** Callers provisioning a new repo ask the credential for the GitHub account login under which to create it. App-mode answers with the install's account login; PAT-mode answers with the GitHub org the user chose at connect time (validated against the PAT's reach); the platform-PAT answers with the configured `GITHUB_REPO_OWNER`. This decouples the OC org slug from the GitHub org slug — they need not match.
- **Webhook strategy.** Callers wiring up event delivery for a new repo ask the credential for its strategy. Some kinds answer "register a per-repo hook"; others answer "rely on platform-level delivery, do nothing." The caller dispatches the strategy without inspecting which kind it is.

Token expiry is communicated alongside the token so the workspace credential helper knows when to re-fetch. Long-lived kinds report no expiry; short-lived kinds report a deadline. The helper's behaviour is the same code path either way.

The deliberate property: **callers never branch on credential kind.** A new kind can be added by implementing the three observables, with no ripple beyond git-service.

Today there is one kind (the platform PAT). Phase 2 retires it and introduces two new kinds (per-org user PAT, per-org App installation). The abstraction exists from the start so the kind set can change without rippling through call sites.

### 3.3 Per-org credential binding

Credentials are an attribute of the **organisation**, not of the project or repo. The actual credential is resolved fresh per call from the org's record. The org's chosen credential kind is fixed at connect time and applies uniformly to every repo the org owns (see §6 — switching is not supported in v1).

The mapping is strictly 1:1. One OC org binds to one GitHub org (or user account); a single GitHub org cannot back two OC orgs (uniqueness on `githubLogin` across active connections). This is the property that makes "org" a meaningful unit of isolation rather than a label. Within an org, projects map 1:1 to GitHub repos.

The org's credential record carries:

- `ocOrgId` — primary key, references the OC org.
- `githubLogin` — the GitHub org or user login under which repos are provisioned. Decoupled from `ocOrgId`; the OC slug and the GitHub slug can differ. Unique across active connections.
- `kind` — `app-installation` | `user-pat` (Phase 2). The Phase 0 platform-PAT binding is implicit and shared across all orgs (effectively single-tenant).
- `identity` — `{name, email, login}`, the committer attribution. Recomputed on every connect or replace; identity changes are audit-logged (§7.4).
- `installationId` (App mode) — links the install on the GitHub side.
- `selectedRepos` (App mode) — the list of GitHub repository full-names the install reaches. Synced via `installation_repositories.added/removed` webhooks; on initial install, populated from `GET /installation/repositories`. Used to refuse task dispatch against repos the install can't reach.
- `patSecretRef` (PAT mode) — opaque pointer to the OpenBao path holding the encrypted PAT (`secret/asdlc/{ocOrgId}/github/pat`). The cleartext token is never returned over BFF↔git-service.
- `webhookSecrets` — list of accepted HMAC keys for inbound events; per-org in PAT mode, App-wide in App mode (§7.6).
- `status` — `active` | `suspended` | `disconnected`.
- `connectedAt` / `lastValidatedAt`.

The record lives in git-service's storage (it is the credential holder per §2.1). The BFF reads non-sensitive projections (kind, identity, githubLogin, status) for display.

### 3.4 GitHub as the agent ↔ platform bus

After Phase 0 there is no MCP, no server-side commit pipeline, and no platform-side state-transition API the agent calls. The agent reads the issue, edits code, commits, pushes, posts progress as `gh issue comment`s on the task's issue, and marks the PR ready. The platform observes everything via webhooks. This is the single most important architectural simplification of the plan: one channel, one source of truth, one ordering.

---

## 4. Phase 0 — Issue and PR-driven platform

### 4.1 What changes

The submit pipeline is replaced by an issue + draft PR scaffold. At task generation: the platform opens an issue with the full agent context, opens a feature branch named `task/<slug(component-name)>-<short-task-id>` (e.g. `task/checkout-svc-a4f2c1d8`), opens a draft PR linking to the issue, provisions a per-task workspace cloned on that branch, and dispatches the agent. The agent does the rest using git and `gh` against the workspace's credential.

Build-and-deploy moves to merge time: a push to the default branch causes the BFF to trigger an OC build. The OC `Component` itself is created upfront at dispatch (idempotent on `(project, componentName)`), so the merge → push → build path is entirely declarative. See §4.4 for why the BFF triggers the build instead of OC's own autobuild webhook.

### 4.2 Why per-task workspaces, not shared

With the agent pushing directly, there is no reason to share a clone between the agent and git-service. Each task gets its own clone, isolated `.git/config`, and credential helper. The shared clone owned by git-service remains, but only for spec/design artifact tag operations on the default branch, serialised by an in-process project lock. This eliminates cross-process worktree coordination entirely.

### 4.3 Webhooks as the only control channel

A single `/webhooks/github` endpoint receives all events for all orgs. The single-endpoint shape is dictated by App mode (one App, one configured callback URL); PAT mode follows it for uniformity rather than registering a distinct URL per org.

The receiver runs in this order, and the order matters:

1. **Parse routing key** from the payload — `installation.id` for App-mode events, `repository.full_name` for PAT-mode (which the receiver maps to `(ocOrgId, projectId)` via the repo record).
2. **Resolve `ocOrgId`** from the routing key against connection records. Events that resolve to no org are rejected as misrouted.
3. **HMAC-validate** against that org's webhook-secret set (N-of-M, so secrets can rotate). Routing fields are not sensitive — tampering with them just causes the signature check to fail at this step.
4. **De-duplicate** on delivery ID.
5. **Dispatch** by event class.

This payload-routing-then-verify pattern is what gives §2.1's cross-org isolation invariant its teeth: a forged event for org B cannot be smuggled in by signing with org A's secret, because step 3 only consults the secrets registered to the org named in step 2.

- **Issue and PR events** drive task state transitions.
- **Push to the default branch** is the canonical build trigger. Picking one signal eliminates the merge-vs-push ordering ambiguity. Pushes to feature branches are recorded for audit and otherwise ignored.

State advance and build trigger are independent operations on independent events: `pull_request.closed merged=true` advances the task's `ready_for_review → merged` and records the merge SHA; `push` to default creates the `WorkflowRun` for that SHA. They do not share ordering — either may arrive first, and the projector reconciles.

Phase 0 processes events synchronously: persist the delivery row, then run the handler, then ack `2xx`. The N-of-M secret list, durable queue, and async processing land as part of the §9.1 hardening pass; the receiver's interface is shaped so that swap is local.

A periodic janitor handles tasks that no event ever closes out: stale drafts get cleaned up, dropped merges (merge observed but build never observed) get reconciled.

### 4.4 Build trigger model: BFF-driven `WorkflowRun`, not OC autobuild

OC offers two ways to start a build: set `autoBuild: true` on the `Component` and let OC's own `/api/v1alpha1/autobuild` webhook react to GitHub pushes, or create a `WorkflowRun` CR directly with the commit SHA injected into the workflow's parameters. We use the second.

OC `Component`s are still the unit of build/deploy — `autoBuild` is set to `false` (or omitted), and `autoDeploy` is left `true` so a successful build still rolls forward on its own. The only thing the BFF takes over is the *trigger*.

Reasons:

- **GitHub App delivers webhooks to one URL.** The App sends every install's events to a single App-configured callback (the BFF). OC's autobuild expects its own per-repo webhook with a separate HMAC secret in `git-webhook-secrets`. Routing both paths means either dual webhooks per repo (impossible in App mode) or the BFF re-emitting raw bytes to OC with a second secret synced between systems — fragile and duplicative.
- **The BFF already needs the event.** Push events drive task state (§3.4) regardless of whether they trigger a build. Having OC observe the same event independently means two consumers reacting to one fact, with no shared ordering — a race against the BFF's own state writes.
- **Triggering belongs to the orchestrator.** The BFF can dedupe redeliveries, skip irrelevant paths (e.g. docs-only diffs), gate on task state, and update `ComponentTask` status atomically with the trigger. None of this is expressible inside OC's webhook handler.
- **Established pattern on OC.** Both sibling WSO2 platforms built on OC (agent-manager, integration-platform) trigger builds by creating `WorkflowRun`s directly and do not use `autoBuild`. They also inject the **commit SHA** at trigger time via `params.repository.revision.commit` so the build is pinned to the exact merged commit rather than re-resolving HEAD at run time. ASDLC adopts the same shape. Diverging would be the unusual choice, not the default.
- **One webhook secret.** The App's webhook secret stays only in the BFF. No `git-webhook-secrets` provisioning during cluster setup, no second rotation lifecycle.

### 4.5 Phase 0 scope: what's deferred, what's scaffolded

**Deferred to Phase 2:** App mode, per-org PAT mode, org-scoped credential storage, the connect/disconnect UX, and the lifecycle around revocation detection.

**Scaffolded in Phase 0 (for Phase 2 to extend):**

- The credential resolver (§3.2), with one kind: the platform PAT. Every git-service call site routes through it from day one. Phase 2 swaps the kind set (out: platform PAT; in: org PAT and App installation) without touching call sites.
- The per-repo webhook registration mechanism. Phase 0 needs it because no platform-level delivery exists yet; Phase 2's PAT mode reuses it unchanged. App mode opts out via the credential's webhook strategy (§3.2).
- The credential-refresh endpoint and workspace credential helper. In Phase 0 this returns a static value; the same code path serves App mode's mid-run refresh in Phase 2.

The forward references in this section are deliberate: Phase 0 builds the seams Phase 2 plugs into. If those seams aren't in place from the start, Phase 2 becomes a rewrite instead of an extension.

### 4.6 Task lifecycle states

```
   pending → in_progress → ready_for_review → merged
                       ↘ rejected
                       ↘ abandoned   (no decision in activity window)
                       ↘ superseded  (re-dispatched)
```

The granular per-stage status fields the current submit pipeline maintains (`GitStatus`, `OCStatus`, `BuildStatus`, `DeployStatus`) collapse into this one progression, derived from webhooks. Re-dispatching a terminal task creates a new task that supersedes the old one; the old terminal state is retained for audit. See §7.3 for the formal taxonomy.

### 4.7 Identity

Agent commits in the remote flow are authored by the credential's identity. Today (platform PAT) this is a fixed bot identity. Under Phase 2 it is the App's bot identity (App mode) or the PAT owner's identity (PAT mode). In all cases, identity is read off the credential, not configured separately.

### 4.8 Branch protection

At repo creation: default branch requires review and disallows force-push and direct push; feature branches under `task/*` disallow force-push but allow deletion (so cleanup works). Test environments bypass the review gate via an admin token.

Branch protection is **deferred to a follow-up after the Phase 0 happy path**. The rules above are the target end state. Landing them in the first chunk would force every E2E test to carry an admin-bypass path, and the rules can be retroactively applied to existing repos via a one-shot backfill script when they ship. Phase 0 ships without protection; the follow-up adds it at repo provision and backfills.

### 4.9 Local development webhook delivery

GitHub must reach the BFF's `/webhooks/github` endpoint. Production deployments expose it via ingress; local-dev deployments use **smee.io** as a webhook proxy. `start.sh` runs a smee client alongside the platform services, forwarding `https://smee.io/<channel>` to `http://localhost:9090/webhooks/github`. The smee channel URL is configured per-developer in `deployments/.env` (`GITHUB_WEBHOOK_PROXY_URL`), and `git-service` registers each repo's webhook against that URL. This keeps the dev flow shape-identical to production: the BFF receives identical event payloads via the same handler, and the only thing that changes is the public-reachability mechanism.

---

## 5. Local agent flow (auth folded into Phase 2)

Local mode already works as of Phase 0: the dispatch dropdown's `Implement Locally` option provisions the same issue + feature branch + draft PR, and the developer clones the repo, runs Claude Code, pushes, and runs `gh pr ready`. The platform observes this identically to a remote task — same webhook stream, same projector — because GitHub is the agent ↔ platform bus (§3.4). There is no second control plane.

The one piece left to settle for local mode is **auth**, and that's Phase 2 work by definition: Phase 2 is what retires the shared platform PAT, so until it lands the local flow's credential story is also implicitly the platform PAT's story. Phase 2 closes that out — see §6 — by establishing per-org credentials for the remote flow and stating the local flow's posture (the developer's own `gh auth`, no platform credential involvement) inline. Any local-flow ergonomic add-ons (e.g. a Claude Code skill mirroring `services/issue_body.go`) ride along but are not the point.

---

## 6. Phase 2 — Per-org credentials

Phase 2 settles auth for **both** the remote and local flows. The remote flow is what most of this section is about: the platform-issued credential the agent uses gets unbundled from a single shared platform PAT into per-org credentials in one of two peer modes. The local flow's posture is named explicitly here too because it's the same question — "what credential reaches GitHub on this task's behalf?" — answered differently: the developer's own `gh auth`, with no platform credential involved (more on this below and in §6.3).

For the **remote flow**, two peer modes are introduced. Either replaces the platform PAT for an organisation:

- **App mode.** A GitHub App installed on the organisation. Tokens minted on demand from a signing key, short-lived, with platform-level webhook delivery and a stable bot identity.
- **User-PAT mode.** A PAT supplied by the user, scoped to one organisation, encrypted at rest. Long-lived, with per-repo webhook delivery and the PAT owner's identity.

Both are first-class. Neither is a "fallback." The choice is per-org and **recorded once at connect time**. An org cannot switch modes after the fact in v1 — the mode is fixed for the lifetime of the org's connection. Disconnecting and reconnecting in the other mode is not supported either; the platform refuses the second connect if any repos exist under the first mode. This avoids the entire class of "old repos under one mode, new repos under another, App-not-installed-on-PAT-era-repos" cross-mode reach problems by construction.

For the **local flow**, there is no platform credential. The developer authenticates their own `gh` and `git` against GitHub via `gh auth login`; the platform never sees, stores, refreshes, or rotates that credential. Identity on commits, comments, and PR actions is the developer's, not a bot's; branch protection (§4.8) applies to them as it does to any human contributor — same review gate, same merge constraints. The platform records "this came from a local agent" by inferring from commit identity, used only for UI labelling. Everything else in this section that talks about per-org credentials, the resolver (§3.2), webhook secrets, OpenBao, App private-key custody, and rate-limit budgeting refers to the **remote** flow only — the local flow has no credential lifecycle on the platform side because there is no platform credential to manage.

### 6.1 Why both

App mode fits orgs that can install third-party GitHub Apps and want short-lived tokens, granular per-repo selection, and no rotation work. PAT mode fits orgs that can't or won't — restricted enterprises, deployments that lack a publicly reachable callback URL, contributors validating the platform locally, and any environment where App approval is a blocker. Neither covers the population the other serves; both are required for the platform to land everywhere it should.

The cost of supporting both is low because the credential resolver (§3.2) already abstracts the difference. Adding either mode is a matter of implementing the three resolver observables, a connect/validate endpoint, storage for whatever the mode persists, and the mode's revocation-detection lifecycle.

### 6.2 What differs by mode (and what does not)

| Concern | App mode | User-PAT mode |
|---|---|---|
| Token TTL | ~1 h, minted on demand | Long-lived, stored encrypted |
| Identity | App's bot identity | PAT owner's identity (resolved at connect time) |
| Webhook delivery | Platform-level (App-wide) | Per-repo (registered at repo creation) |
| Revocation signal | Webhook (`installation.deleted`) | Lazy on 401, plus periodic validator |
| Setup friction | App install flow + callback URL | Paste a PAT, validate scopes |

Everything else is shared: the resolver interface, the credential refresh endpoint, the workspace credential helper, OC build credential provisioning (§6.3.1), branch protection, identity attribution mechanism, repo creation routing.

### 6.3 Credential lifecycle

Each transition has a defined API surface on the BFF (user-facing) and a defined call into git-service (the credential holder). All BFF endpoints scope to `ocOrgId` in the path; git-service refuses to act without one.

**Connect — App mode.**

1. `POST /api/v1/orgs/{ocOrgId}/github/app/start` → `{ installUrl }`. The BFF builds GitHub's install URL with a signed, single-use, time-bounded `state` param carrying `ocOrgId` and a CSRF nonce. The user is redirected.
2. After install, GitHub sends the user back to the App's configured callback `GET /api/v1/github/app/callback?installation_id&state`. The BFF verifies `state`, extracts `ocOrgId`, and calls git-service `POST /internal/credentials/orgs/{ocOrgId}` with `{kind: app-installation, installationId}`. git-service mints a token to read the install's `account.login` and bot identity, persists, and returns the projection for display.
3. The platform-wide `installation.created` webhook is the second channel of confirmation: if the browser callback is missed (window closed mid-flow), the webhook arriving with the same `installation_id` completes the connect by matching against the pending `state` record. Without this fallback, an interrupted flow leaves an orphan install on the GitHub side.

**Connect — User-PAT mode.**

`POST /api/v1/orgs/{ocOrgId}/github/pat` with body `{pat, githubLogin}`. The BFF forwards to git-service, which: validates against `GET /user` and a reach check on `githubLogin` (org membership or repo-create probe), generates a per-org webhook secret, encrypts the PAT and stores it at `secret/asdlc/{ocOrgId}/github/pat` in OpenBao, persists the credential record, and returns the identity projection. The PAT is never written to logs and never returned over the wire after this call.

**Status / read.** `GET /api/v1/orgs/{ocOrgId}/github` → `{kind, identity, githubLogin, status, connectedAt, lastValidatedAt}` plus, in App mode, the selected-repos list. Never returns the token.

**Use.** Every git-service operation routes through the resolver, parametrised by `ocOrgId`. The resolver looks up the org's record, mints (App) or fetches (PAT) the token, and returns the polymorphic `Credential`. The remote agent's workspace is configured with a credential helper that re-fetches via the platform when needed. **remote-worker** (the workspace provisioner that sits between the BFF and the Agent SDK) never holds a GitHub token in either mode: the BFF dispatches it a per-task bearer + the resolved identity (`name`, `email`, `login`), it writes the credhelper + `gh` wrapper into the workspace, and every actual GitHub token is minted on demand by git-service when the agent shells out. App mode changes what the resolver returns (a freshly-minted ~1h installation token instead of a static PAT) but does not change the dispatch payload shape, the bearer flow, or what remote-worker holds. The local flow does not pass through this path at all — the developer's `gh auth` is consulted directly by `git` and `gh` on their own machine, so connect/use/refresh/replace/disconnect have no local-flow analogue. Any task the platform dispatches as `Implement Locally` skips credential provisioning entirely; the workspace credential helper is never written, no per-task bearer is issued for that purpose.

**Refresh.** App mode mints fresh tokens on demand within a cached window; PAT mode returns the stored value. The resolver hides the difference from callers.

**Replace** (PAT mode only). `PUT /api/v1/orgs/{ocOrgId}/github/pat` swaps the PAT in place. The credential kind does not change. **Identity is recomputed**; if the new PAT belongs to a different GitHub user, the change is audit-logged (§7.4) and surfaced in the org's settings UI ("Identity changed from X to Y on YYYY-MM-DD"). Past commits keep their original attribution; future commits use the new identity. App mode has no analogue — installs are atomic and identity is fixed by the App definition.

In-flight tasks at the moment of replace: their workspaces hold a `.git/config` and `hosts.yml` written with the *old* identity. The credhelper.sh response carries the current identity on every refresh; the workspace's `update-git-identity` hook (Phase 0 §7.1) detects identity drift and rewrites `.git/config` user fields. A commit currently in progress will keep the old identity for that one commit — best-effort, not transactional. If the user requires clean attribution, they re-dispatch the affected tasks (`superseded` semantics; deferred follow-up). The settings UI surfaces the count of in-flight tasks at replace time so the user can choose.

**Reach reconciliation** (App mode). `installation_repositories.removed` shrinks `selectedRepos`; `installation_repositories.added` extends it. Tasks targeting a repo that was just removed cascade to `abandoned` (the credential is healthy but the repo is dark — no recovery without the user re-checking the repo on GitHub). The platform surfaces this in the org settings UI alongside the credential identity. `installation.suspend` / `unsuspend` flip `status` to `suspended` / `active`; in-flight tasks freeze (no new agent dispatches, in-progress agents will hit auth errors on next token mint and fail).

**Disconnect.** `DELETE /api/v1/orgs/{ocOrgId}/github`. App mode also learns from `installation.deleted` webhooks; PAT mode learns from a 401 or from a periodic validator. Either way:

- **Order matters.** Before the credential is invalidated, the disconnect handler posts a best-effort "abandoned: org disconnected" comment on every in-flight task's issue using the still-valid token. Then the record's `status` flips to `disconnected`; subsequent resolver calls return an error and tasks dispatch refuses.
- All projects under the org move to a `git_disconnected` state with a UI banner.
- **In-flight tasks (any non-terminal state) cascade to `abandoned`** as a fifth recognized cascade source (alongside the activity-window cases in §7.3). Their workspaces are queued for cleanup; the platform-side `status=abandoned` is authoritative regardless of whether the GitHub-side comment posted (best-effort — the issue may still appear active on GitHub if the comment write failed mid-cascade).

Recovery in App mode is reinstalling the App; in PAT mode it is replacing the PAT. The credential kind does not change. Tasks abandoned during the disconnect must be re-dispatched as new tasks if the user wants them resumed — there is no automatic resurrection on reconnect.

### 6.3.1 OC build credentials and App-token rotation

OC's build workflow does not call back to the platform for credentials. It pulls a git token from a K8s `Secret`, resolved through a `SecretReference` CR backed by an external store (OpenBao). This is fine for PAT mode — the PAT is long-lived, written once at repo creation, never rotated by the build path. App mode is the hard case: installation tokens expire after ~1 hour, so a token written into OpenBao goes stale before the next build runs.

The model:

- **One `SecretReference` per repo**, named deterministically (`git-{ocOrgId}-{repo}`), pointing at a single OpenBao KV path (`secret/asdlc/{ocOrgId}/git/{repo}`). The `Component`'s `repository.secretRef` is set to this name at dispatch and never changes. Per-org isolation is enforced by the path namespace: git-service's OpenBao policy grants read-write only on `secret/asdlc/*`, and every read or write inside that prefix is parametrised by `ocOrgId`. Per-tenant ACLs (one OpenBao policy per org) would add a dynamic-provisioning step at every connect, out of proportion for the threat model — path-namespaced single policy is the deliberate choice. The OC `Component`, `WorkflowRun`, and `SecretReference` CRs themselves live in the OC namespace `{ocOrgId}` (one OC namespace per OC org); cross-namespace reads are not permitted.

The honest framing of the path-namespace decision: it places the multi-tenant isolation property on **git-service code correctness**, not on OpenBao ACL configuration. To turn that from code discipline into an architectural property, the *only* OpenBao access surface in git-service is a wrapper that takes `ocOrgId` as a mandatory parameter and constructs paths internally — no caller can write a raw path. A build-time check (test or `go vet` analyzer) fails if any code outside the credentials package imports the OpenBao SDK. The wrapper, not OpenBao itself, is the architectural enforcement boundary. The interface is named in Phase 0 (see github-integration-phase0.md §6.5) and implemented in Phase 2.
- **PAT mode** writes the user's PAT into that path once and is done.
- **App mode** writes a freshly-minted installation token into that path immediately before each `WorkflowRun` is created. The build pod resolves the secret on pod start (within seconds of trigger), well inside the 1 h TTL. The token is overwritten on the next build, not deleted — concurrent builds against the same repo share the same path safely because tokens are interchangeable.
- **Long-running or queued builds** (queue depth, retries, OC controller backoff) can outlast the token. The `WorkflowRun` controller surfaces the failure as an auth error; the BFF observes the failed run, mints a fresh token, overwrites the secret, and re-creates the `WorkflowRun`. A bounded retry budget prevents loops if the underlying problem is not token expiry.
- **Token minting itself** stays in git-service, not in the build pod. The build pod is a closed system that only knows how to read its mounted secret; pushing the credential to it is the platform's job. This preserves the §2.1 invariant that git-service is the sole token holder.

What this rules out, and why:

- *Storing only an App private key in OpenBao and minting in the build pod.* Would push GitHub App private-key reach into every build worker. Violates §2.1.
- *Long-lived deploy keys per repo as a workaround.* OC's dockerfile-builder uses HTTPS, not SSH. Adding a parallel SSH path doubles the credential surface for a single edge case.
- *Pre-minting tokens with refresh-ahead caching only in git-service.* git-service can refresh-ahead for live API calls, but the build pod reads the secret once at start; refresh-ahead in git-service does nothing for an already-mounted token.

### 6.4 Why no mode switching in v1

Allowing an org to switch modes mid-life would force the platform to either (a) silently rewrite every repo's webhook wiring against the user's GitHub — a large invisible bulk operation with partial-failure modes that leave the platform inconsistent with reality, or (b) leave repos in a mixed population where the new credential might lack reach to old repos (e.g. App not installed on a PAT-era repo). Both are observable user-facing problems with no clean recovery.

The simpler rule — choose at connect time, no switching — eliminates both classes of problem at the cost of one explicit user limitation, surfaced in the connect UX.

---

## 7. Cross-cutting invariants

These hold across all phases and modes. Implementations must preserve them.

### 7.1 Idempotency

Every external side-effect operation is idempotent on a stable key:
- Repo creation: idempotent on `(ocOrgId, projectId)`.
- Issue / branch / PR creation at dispatch: idempotent on task ID.
- Webhook receipt: idempotent on delivery ID.
- OC `SecretReference` creation: idempotent on `(ocOrgId, projectId)` — one per repo, shared by all components in the project. Created at repo provisioning, not at task dispatch.
- OC `Component` creation: idempotent on `(ocOrgId, projectId, componentName)`. Created at task dispatch.

The two OC keys are deliberately different. The `SecretReference` is repo-scoped (one per repo) and predates components; conflating its key with the per-component key would force a `SecretReference` creation on every dispatch and require dedup on the wider tuple.

Re-running any phase of dispatch must be safe. Partial-failure recovery is handled by re-running, not by special-case rollback paths.

### 7.2 Source of truth for task state

Task state is derived from GitHub events. The platform's stored state is a projection, never an assertion. Where the platform writes state synchronously (e.g. recording an issue number when it opens the issue), the corresponding webhook is treated as a backstop that confirms the same fact.

A consequence: a task in a terminal state ignores late events. A `pull_request.synchronize` arriving after `pull_request.closed merged=true` does not clobber the merge SHA.

### 7.3 Failure-mode taxonomy

There are four terminal task states, distinguished by who decided and what happens next:

- **`merged`** — human approved; build pipeline runs.
- **`rejected`** — human declined; cleanup runs.
- **`abandoned`** — no decision reached within the activity window (covers both "agent never made progress" and "draft sat untouched"); cleanup runs.
- **`superseded`** — replaced by a re-dispatched successor; old resources cleaned up, new task takes over.

The activity window is a platform-wide default (separate thresholds for stale drafts and stale ready-for-review PRs), with per-org override deferred to a later need.

Each state has a defined cleanup contract for the resources dispatch created (branch, PR, issue, workspace, OC `Component`, OC `SecretReference`). Per-state contract details are specified at implementation time in the janitor and webhook-handler code; this doc fixes only the architectural rule. The cleanup rule for OC resources is reference-counted: a component is retained iff a non-terminal task references it or a current release uses its most recent build.

### 7.4 Identity and auditability

The identity recorded on a commit, comment, or PR action is the credential's identity. The platform's stored credential record carries the identity, so any past action can be traced to (org, credential kind, identity at the time). User-PAT mode in particular puts a human's identity on automated commits — this is intentional (the user took responsibility by handing over the PAT) and must be visible in the org's UI so the implication is not silent.

### 7.5 What survives a restart

- **Persisted:** webhook delivery IDs (for de-dup), credential records, repo records (including which mode they were created under), task records and their derived state, OC resource references.
- **Not persisted, recoverable:** per-task workspace clones (re-cloned on re-dispatch from the recorded branch); App installation token cache (re-minted on demand); single-flight locks.
- **Not persisted, not recoverable:** in-flight per-task bearers (re-issued on re-dispatch).

The "recoverable" tier is what makes "re-dispatching is safe" actually true post-restart. After a restart, the system reconverges by replaying webhooks (which GitHub will redeliver on demand) and by the janitor's reconciliation pass.

### 7.6 Secret rotation and isolation

Webhook-receiving secrets are accepted as a list **per credential record**, not a single value. The list serves two purposes:

- **Rotation:** add new, wait for all senders to switch, drop old.
- **Isolation:** PAT-mode secrets are per-org (one list per credential record); a leak in one org's secret cannot be used to forge events for another, because the receiver only consults the resolved org's secrets after step 2 of §4.3. App mode contributes one App-wide secret (no per-org isolation gain — App-wide is the natural scope of an App), but the same list mechanism handles its rotation.

The Phase 0 platform-PAT deployment uses a single platform-wide secret (effectively single-tenant), reusing the same list-of-one shape that Phase 2 expands.

---

## 8. What's deliberately out of scope

- **Migration of pre-Phase-2 projects.** Phase 2 applies to organisations and projects created after it lands. Projects created under the legacy platform-PAT setup are not migrated, retrofitted, or re-homed under the new per-org model. The platform PAT and its hard-coded org are simply retired alongside the projects that used them.
- **Switching credential mode after an org has connected.** Mode is fixed at connect time (§6, §6.4).
- **Multi-installation per org** (an org with two ASDLC tenants sharing one GitHub org). One active credential per org.
- **First-class platform audit log** of "credential X performed action Y at time T." The credential identity is on every commit/comment/PR, so GitHub's own audit log carries this; the platform does not duplicate it.
- **Local-flow plugin distribution and discovery.** Ad-hoc until the plugin stabilises (the plugin itself is folded into Phase 2 per §5 / §6.5).

---

## 9. Production-readiness gaps to close before GA

The design above is sound at the abstraction level. The following are concrete operational concerns that the current text either glosses over or leaves implicit. Each must have a decided answer before this is something we can run in production for paying organisations, not just dogfood.

**Priority:** P0 = ship-blocker for GA (security or data-loss). P1 = ship-blocker for paying users at any scale (correctness or UX). P2 = tidy-up that real operations will eventually demand.
**Effort:** XS ≤ 1 day · S ≈ 2–4 days · M ≈ 1–2 weeks · L ≈ multi-week, cross-system.

### 9.1 Webhook ingestion durability

**Priority:** P0 · **Effort:** M

GitHub retries failed deliveries up to ~8 times across ~9 hours, then drops. The current text says webhooks are de-duped on delivery ID, but does not say *when* the delivery ID is persisted relative to processing. The production rule must be: persist delivery ID + raw payload, ack `2xx` to GitHub, then process from a durable queue. Synchronous "validate → process → ack" couples GitHub's view of liveness to internal handler latency and loses events on any restart mid-handler. This belongs in §4.3.

### 9.2 Webhook ordering

**Priority:** P1 · **Effort:** S–M

GitHub does not guarantee delivery order. `pull_request.closed` can arrive before `pull_request.opened`; `push` can arrive before the `pull_request` event that contextualises it. The handler must be idempotent **and** order-independent (e.g. by reconciling against the GitHub API on ambiguous transitions, not by trusting event sequence). §7.2's "late events are ignored on terminal state" covers part of this; the broader rule needs to be stated.

### 9.3 BFF horizontal scaling and the janitor

**Priority:** P1 (P0 the moment we run >1 BFF replica) · **Effort:** S

§4.3 mentions a periodic janitor; §7.5 lists "single-flight locks" as not persisted. With more than one BFF replica, the janitor becomes a multi-writer race against itself, and per-project single-flight locks become per-process, not per-platform. Production needs leader election for the janitor (lease-backed) and a real distributed lock (Postgres advisory locks are sufficient and already in the stack).

### 9.4 GitHub App private-key custody

**Priority:** P0 · **Effort:** S (assuming §9.10 is done; otherwise blocked)

The App's RSA private key is the master credential for every installation. It must live in a real secret manager (OpenBao / cloud KMS), not in environment variables or a K8s Secret loaded from `.env`. Rotation: GitHub supports up to two active App private keys, so rotation is "add new, switch signer, drop old" — same shape as §7.6's webhook rotation rule. Document the storage choice and the rotation runbook.

### 9.5 Rate-limit budgeting

**Priority:** P1 · **Effort:** M

A GitHub App installation gets 5,000 REST requests/hour and a separate GraphQL budget. Concurrent agents on a busy org (each calling `gh` for issue reads, PR updates, comments) plus webhook-driven reconciliation can saturate this. Production needs: per-installation token bucket in git-service, exponential backoff on 403/secondary-rate-limit, and a metric so saturation is visible before users hit it.

### 9.6 Per-task bearer issuance

**Priority:** P0 · **Effort:** S–M

§2.1 and §7.5 mention a "short-lived per-task bearer" used for the agent's credential-refresh calls back to git-service, but the issuance/validation/revocation mechanism is not specified. Production needs a concrete scheme: signed JWT with task ID and TTL bounded by max task duration, validated by git-service against the dispatch record, revoked when the task reaches a terminal state. Without this, the credential-refresh endpoint is implicitly trusting the internal network as its only authn.

### 9.7 Build-credential garbage collection

**Priority:** P2 · **Effort:** S

§6.3.1 writes per-repo entries into OpenBao keyed by `git-{org}-{repo}`. Repo deletion or org disconnect must remove the corresponding KV entries and the `SecretReference` CR. The reference-counted cleanup rule in §7.3 covers Components but not the secret material. Add the explicit GC contract.

### 9.8 Public reachability assumptions in App mode

**Priority:** P1 · **Effort:** XS

App mode requires GitHub to reach the BFF's webhook URL. Self-hosted deployments inside corporate networks may not have ingress for this. PAT mode is the documented escape (§6.1), but the design should also state that App mode is unavailable in air-gapped or callback-less environments, so the connect UX can refuse it cleanly rather than failing partway through install.

### 9.9 Secret redaction in logs and transcripts

**Priority:** P0 · **Effort:** S

Agents write to transcripts, BFF logs HTTP requests, git-service logs token refreshes. Any of these can leak a token. Production needs a scrubbing layer at the log boundary (regex for `gh[a-z]_*` and JWT shapes) and a transcript-side filter on agent output. Cheap to add, expensive to retrofit after the first leak.

### 9.10 OpenBao operational dependency

**Priority:** P0 for Phase 2 (blocks §9.4 and §6.3.1) · **Effort:** L

The design now hard-depends on an external secret store for both PAT storage and App-token rotation (§6.3.1). OpenBao is not currently part of the ASDLC deployment topology — agent-manager runs it, ASDLC does not. Adding it is its own production exercise: HA, unseal flow, backup, access policies for git-service, audit log. This belongs in the deployment plan that accompanies Phase 2, not as a footnote.

**Downtime runbook.** When OpenBao is unreachable, git-service degrades according to credential kind:

- **App mode.** git-service caches minted installation tokens in process memory until their natural expiry (~1 hour). During an OpenBao outage, in-flight tokens keep working; new mints fail. Tasks that hit auth on a fresh-mint path fail and are retried by the next webhook redelivery. No additional security trade — the cache TTL is the GitHub-imposed token TTL, not an extension.
- **PAT mode.** git-service caches the *decrypted* PAT in process memory after first read with a **30-minute TTL** (refreshed on use). During an OpenBao outage shorter than 30 minutes, in-flight tasks keep working. Longer outages cause read failures on the next refresh. The security trade: the PAT lives in process memory longer than a single request — a process memory dump or a git-service compromise during the cache window exposes the PAT. 30 minutes is the deliberate compromise between "outage tolerance" and "cache lifetime"; reducing it tightens the trade at the cost of more frequent OpenBao calls. **The cache is process-local**: a git-service restart drops the cache, so a rolling deploy of git-service during an OpenBao outage drops PAT-mode tasks immediately on the new pod's first read attempt. On-call should hold deploys of git-service during a known OpenBao outage; this is documented in the deploy runbook, not just the source.
- **Build pods (both modes).** A pod that starts during an OpenBao outage cannot resolve its `SecretReference` and fails on `git clone`. The `WorkflowRun` controller surfaces this as a build failure; the BFF re-creates the run on a bounded retry budget (per §6.3.1). The retry handles transient OpenBao restarts; sustained outage means builds genuinely cannot run, which is honest behavior.

The runbook is "OpenBao unsealed → cache absorbs ≤30 min outages transparently → tasks resume on auto-retry once OpenBao is back". On-call doesn't need to touch ASDLC during a routine OpenBao restart.

### 9.12 Webhook-secret refetch DoS amplification

**Priority:** P2 · **Effort:** XS

The receiver re-fetches an org's webhook secrets on HMAC mismatch (Phase 0 design §6.4) to close the secret-rotation cache hole. A flood of forged events (any payload with a valid routing key but an invalid signature) therefore amplifies to a flood of git-service `webhook-secrets` lookups. Token-bucket rate-limit the `force=true` refetch path per `(routingKey, sourceIP)` so a single forged stream cannot saturate git-service. The legitimate rotation path is unaffected — rotation only triggers one mismatch per sender.

### 9.13 OpenBao-reachability gate on git-service rollout

**Priority:** P2 · **Effort:** XS

PAT-mode tokens cache 30 min in git-service process memory. A rolling deploy of git-service drops the cache; coinciding with an OpenBao outage the new pod can't read PATs and tasks fail. Make this an architectural property rather than a runbook discipline: git-service's startup health check probes OpenBao reachability and refuses ready-state until OpenBao is reachable, so the deployment platform's rolling-deploy mechanism naturally holds during outages. Same shape as a database-reachability check on app startup — well-understood pattern.

### 9.11 `installation_repositories` event handling

**Priority:** P1 · **Effort:** S

App mode must react not only to `installation.deleted` but to `installation_repositories.removed` (user un-checks a repo from the App's selected list). The credential reach silently shrinks; any task targeting a removed repo will start failing on next token mint. The lifecycle in §6.3 only names `installation.*`; expand to cover repository-level changes.

---

## 10. Open questions

1. **Phase 2 sequencing.** User-PAT mode is significantly easier to validate end-to-end (no public callback, no App approval timeline). Ship it before App mode so contributors and self-hosted deployments aren't blocked. Both arms hang off the same resolver, so this is an ordering decision, not a design split. *(Tentatively decided: PAT first.)*
