# Architecture Validation — `asdlc-service` Refactor Plan

> Adversarial validation of `00-executive-summary.md`, `10-target-architecture.md`,
> `20-refactor-roadmap.md` against the verified Agent Manager (AM) reference
> (`/Users/wso2/repos/agent-manager-analysis/`), AM source
> (`/Users/wso2/repos/agent-manager/agent-manager-service/`, checkout `b60e912a`), and the
> labs source (`/Users/wso2/repos/labs-agentic-engineer/`). Citations are `file:line`.
> Scope per charter: **code-level boundaries are the priority.** This doc does not modify the plan.

---

## 1. Verdict

**Sound-with-required-changes.**

The core architectural decisions are correct: the merge is right (the leak is *structural* — the
secret store landed in the one service with no OC client, `10` §0 / `02-secrets.md:31-51`), the
`secretmanagersvc` interface is the right seam, and reference-only secret crossing is the correct
invariant. **But two load-bearing claims in the plan are factually wrong against the cited
sources and must be corrected before they drive sequencing**: (1) the "port AM's `secretmanagersvc`
1:1 with **both** providers" claim — AM's checkout contains **only** the `openbao` provider and has
**no `SecretManagerAPI` config struct** (`provider.go`, `types.go:61-87`, confirmed by `09`'s own
"Unverified/hedge"), so Phase 3 is **not** "config-only"; (2) the plan never carries over the one
piece of code-level boundary enforcement labs *already has* — the OpenBao import fence
(`git-service/pkg/credentials/import_fence_test.go`) — and provides no replacement, which is the
single biggest risk that the merge re-blurs the boundary it exists to fix. Fix the boundary
enforcement (§4), correct the two-provider/"config-only" framing (§3 BLOCKER-1), and the plan is
shippable.

---

## 2. What's right (do not regress)

- **The merge is correctly diagnosed and correctly scoped.** The blur is structural, not
  behavioral: git-service has the K8s client + secret store but **no OC client**
  (`VERIFY-secrets.md` claim 8: "git-service has no OC API client; it talks to k8s directly via
  controller-runtime"), so plaintext SSA-push was the *only* way to get values to pods. Merging into
  the OC-client-owning process dissolves it. Keep this.
- **`secretmanagersvc` is the right abstraction and the seam is real.** The
  `SecretReferenceManager` / `ManagesSecretReferences()` branch genuinely exists and works as the
  plan describes (`wiring/wire.go:138-156`, `wire_gen.go:330-347`; `client.go:390-403` gates
  SecretReference authoring on `ocClient != nil`). The "who authors the SecretReference" decision
  *does* follow from one env-selected provider. That design is sound.
- **OC client surface is verified present.** `SecretReference` full CRUD and `GitSecret`
  Create/List/Delete already exist in the generated client (`VERIFY-secrets.md` claim 8; confirmed:
  `grep` of `gen/client.gen.go` returns all five SecretReference ops + Create/List/Delete GitSecret).
  No regeneration needed. The `GitSecret` no-Get/Update caveat is real and correctly flagged.
- **Git Data API is partially already built.** Multi-file atomic commit via blob→tree→commit→UpdateRef
  with CAS retry already exists (`git-service/services/save_via_api.go:105-189`, "re-fetch
  ref/commit/tree so base_tree is fresh"). The plan's WS0.4 is *less* work than stated — the engine
  exists; only the working-tree dependency must be removed.
- **The reference-only invariant and the acceptance criteria** (`10` §7) are the correct end-state
  contract and are individually grep-checkable.
- **Structural-first + M1=Phase0+1** is the correct sequencing instinct (see §5).

---

## 3. Findings (severity-ranked)

### BLOCKER-1 — "Port `secretmanagersvc` 1:1 with both providers" is false; Phase 3 is NOT config-only

> **RESOLUTION (post-review):** the conclusion (one provider in OSS; Phase 3 is *additive*, not
> config-only) stands — but the missing provider is **not net-new of unknown protocol**. It exists in
> the **enterprise `agent-platform` repo** at `agent-manager-service/secrets/provider.go` (cloned at
> `/Users/wso2/repos/agent-platform`): a ~470-LOC `secretmanagersvc.Provider`/`SecretsClient` impl,
> `WriteOnly`, `ManagesSecretReferences()=true`, **JWT-from-context auth** (explains the cloud
> ReleaseBinding's missing `OPENBAO_TOKEN`), REST `/secrets` with label-based ID lookup. AM ships
> **open-core** (OSS = interfaces + openbao) **+ enterprise** (agent-platform adds secret-manager-api).
> So Phase 3 is a **port**, not a rewrite. Docs updated accordingly.

**Issue.** The plan repeatedly asserts a 1:1 port yields both providers and therefore Phase 3 (WSO2
Cloud) is "a config flip, not a rewrite" (`00` §4 lines 87-91; `10` §3 lines 114-137, §5.1 lines
263-272; `20` WS0.2 lines 56-66, lines 253-254). **The AM checkout does not contain a
`secret-manager-api` provider.** Verified:
- `clients/secretmanagersvc/providers/` contains **only** `openbao/` (`find` output; matches `09`
  lines 126-134).
- `StoreConfig` (`types.go:61-68`) has **only** an `OpenBao *OpenBaoConfig` field — there is **no**
  `SecretManagerAPI` config struct and **no** `BaseURL` plumbed into any provider. `09` lines 132-134
  confirm: "the wiring builds the `StoreConfig` with only `OpenBao{URL, Token}` and never passes
  `BaseURL` to any provider."
- The openbao provider **mandates a token** and **does not** implement `SecretReferenceManager`
  (`providers/openbao/provider.go:91` "openbao auth token is required"; `09` lines 128-131).
- `09` lines 154-159 (the plan's own cited source) explicitly states the deployed WSO2 Cloud binary
  is a commit **absent from local history** and the wire protocol "isn't determinable from this repo."

So the `secret-manager-api` provider is **net-new code of unknown protocol** (Vault-KV-compatible vs
custom REST is unknown), requiring a new `StoreConfig` variant, a new provider implementing
`SecretReferenceManager`, and a new `main.go` blank-import. This is not a "1:1 port."

**Why it matters.** The entire justification for building both providers in Phase 0 ("turns the WSO2
Cloud cutover into a config flip instead of a rewrite", `00` line 91) rests on this false premise. If
the team builds a *speculative* `secret-manager-api` provider in Phase 0 against a guessed protocol,
it will almost certainly not match the real WSO2 Cloud service, and Phase 3 becomes a rewrite anyway —
having paid Phase-0 cost for nothing and widened the M1 surface for a non-M1 feature.

**Fix.**
1. Reword the plan: Phase 0 ports the `secretmanagersvc` **interfaces + `openbao` provider** 1:1
   (that part *is* a true port) and builds **only** the `openbao` provider. The `secret-manager-api`
   provider is **deferred to Phase 3** and built then, against the real deployed service's contract
   (obtainable from `wso2cloud-deployment` `dataplane/.../secret-manager-api/` per `09` Surface B).
2. Keep the *seam* (`SecretReferenceManager`, the `StoreConfig.Provider` switch, the
   `SECRET_MANAGER_*` config keys) in Phase 0 — that is the cheap, real "pluggable" property and
   genuinely makes Phase 3 additive (register a new provider) rather than structural. Phase 3 stays
   "additive provider + config", which is honest; "zero code change" is not.
3. Drop "byte-for-byte AM's deployed shape" certainty (`10` line 266, `20` line 183) — `09` says the
   deployed protocol is unverified.

---

### BLOCKER-2 — The merge deletes the only existing boundary enforcement and replaces it with prose

**Issue.** labs **already enforces** "only `pkg/credentials/` may import the Vault SDK" with a
compile-time AST walk (`git-service/pkg/credentials/import_fence_test.go`: `TestOpenBaoImportFence`,
bans `github.com/openbao/` and `github.com/hashicorp/vault` outside `pkg/credentials/`). This is
*exactly* the code-level boundary the charter demands — and the plan **deletes the package that
hosts it** (`10` §0.2 line 60: "Deleted in the merge: git-service `pkg/credentials`") without
porting the fence forward. The target layout (`10` §0.2) is described only as a directory list with
no allowed-imports rules and no enforcement mechanism. Given the merge fuses ~64k LOC (asdlc) + ~17k
LOC (git-service) into one module, "boundaries by convention" will rot.

**Why it matters.** The merge's *entire stated purpose* is to fix a blurred boundary. With no
enforced import rules, nothing stops a `services/` file from importing `hashicorp/vault/api`
directly, or `clients/github` from importing the OC client, recreating the leak in a new shape. The
plan's invariant #6 ("Grep: no second Go service with an OC client or a credential store", `10` line
299) checks *service count*, not *intra-process import direction* — it would pass even with a fully
blurred internal graph.

**Fix.** Adopt the boundary spec in §4 and **enforce it in CI**, porting the fence forward and
extending it:
- Keep `TestOpenBaoImportFence` semantics: only `clients/secretmanagersvc/providers/openbao/` may
  import `hashicorp/vault`/`openbao`.
- Add a fence: only `clients/secretmanagersvc/**` and the wiring may import the secret-provider SDKs;
  **no `services/`, `controllers/`, or `clients/github/` file may import any secret backend SDK or
  the OpenBao config.**
- Add a fence: only `clients/openchoreo/**` and `clients/secretmanagersvc/**` may import the OC
  generated client; **`clients/github/**` must NOT import `clients/openchoreo` or
  `clients/secretmanagersvc`.**
- Prefer the cheap, already-proven mechanism (the AST-walk test) over heavier tooling; optionally add
  `go-arch-lint`/`depguard`. Consider `internal/` placement (§4) so the compiler helps.

---

### MAJOR-3 — `clients/github` has no AM analog; the boundary cannot be "mirrored," it must be designed

**Issue.** The plan repeatedly says `asdlc-service` mirrors `amp-api` and that `clients/github`
"replaces git-service github_client + app_token_minter" (`10` §0.2 lines 50-58). But `amp-api` has
**no `github` package** — its `clients/` are `gitprovider, openchoreosvc, secretmanagersvc,
thundersvc, observabilitysvc, requests` (verified `ls`). AM does **not** author commits via the Git
Data API at all; labs does (`save_via_api.go`). So the most leak-prone new package — the one that
holds GitHub App private keys, mints installation tokens, *and* does multi-file commits — is a
**labs-original** with no reference design to copy. The plan treats it as a mechanical move.

**Why it matters.** This is precisely where a secret value can re-enter the wrong layer: today the
GitHub App private key lives in OpenBao and is loaded into the git-service process
(`02-secrets.md:149-160`, `app_token_minter.go:408`). After the merge, if `clients/github` reads the
App key *itself* from the secret backend, it gains a dependency on the secret store and becomes a
second secret-holding seam — re-blurring the boundary. App-token minting also needs a backend secret
(the App private key), which is the one legitimate exception that must be handled deliberately.

**Fix.** Localize per §4: `clients/github` is given an already-resolved credential (PAT, or a minted
installation token, or the App private key bytes) by its **caller in `services/`**, which obtained it
via `secretmanagersvc`. `clients/github` itself imports **neither** `secretmanagersvc` **nor**
`clients/openchoreo`. The App-private-key→installation-token minting is a `services/`-level
orchestration that *calls* `secretmanagersvc` to fetch the App key and *calls* `clients/github` to
mint — the two never import each other. Make this explicit in `10` §0.2.

---

### MAJOR-4 — `SecretLocation` taxonomy (org/project/env/agent/config/entity) is AM-shaped, not validated against labs' org/project/task model

**Issue.** `10` §3.1 line 143 says `SecretLocation` "carries org/project/env/entity segments." AM's
actual struct is richer — `OrgName, ProjectName, AgentName, EnvironmentName, EntityName, ConfigName,
SecretKey` (`client.go:38-47`) — and its KV-path / `SecretRefName` logic encodes **AM's** domain
(agents, configs, provider/proxy handles; `client.go:60-156`). labs' domain is org → project →
**task** (GitHub-issue-anchored), not org → agent → config. The plan never maps labs' entities onto
these segments. `SecretRefName()` (`client.go:145-156`) derives K8s names from `ConfigName`/`EntityName`
that labs doesn't have, and the `workflows-<orgID>` namespacing labs uses (`wp_naming.go`,
`02-secrets.md` table) must map to `SecretLocation.OrgName` as the namespace
(`client.go:331` `Namespace: location.OrgName`) — which works only if labs' OC org ID is the namespace,
which should be confirmed.

**Why it matters.** Porting `SecretLocation` "1:1" imports AM's entity model. If labs' anthropic key
is org-scoped (it is today: `org_secrets` key `anthropic/key`, `02-secrets.md:119`), the right
location is `{OrgName, EntityName:"anthropic"}` → 2-segment KV path — fine. But coding-agent secrets
are *task*-scoped, and there is no `TaskName` segment; overloading `AgentName` or `ConfigName` for
"task" is a silent semantic mismatch that will produce confusing KV paths and SecretReference names.

**Fix.** Before WS0.2, write a one-page mapping: labs {org, project, task, entity} →
`SecretLocation` fields, and decide whether to (a) reuse AM's struct with a documented field
convention (e.g. task→`AgentName`), or (b) fork `SecretLocation` to labs' domain (rename
`AgentName`→`TaskName`). Option (b) is cleaner and is *not* more work, since the port is into a fresh
package anyway. Either way, validate `Namespace == OC org ID` holds for labs.

---

### MAJOR-5 — The runner pod's identity/token (`ASDLC_BEARER`) is delivered as a plaintext WorkflowRun param and is not fixed by Phase 1's secret cutover

**Issue.** WS1.4 says "stop passing the Task JWT as the `ASDLC_BEARER` plaintext WorkflowRun param …
materialize via ESO/`secretKeyRef`" (`20` lines 112-113). But the Task JWT is **minted per-dispatch**
(`02-secrets.md:71`, `dispatch_service.go:244`) — it is not a stored secret, so there is no KV path to
reference and ESO/`SecretReference` (which materializes *stored* values) does not apply to it cleanly.
The runner reads it from env today (`remote-worker/src/oneshot.ts:40` `requireEnv("ASDLC_BEARER")`).
Routing a freshly-minted short-lived token through OpenBao + SecretReference + ESO on every dispatch
adds a write+refresh cycle and latency for a token whose whole point is to be ephemeral.

**Why it matters.** This is an ordering/feasibility hazard hidden inside the M1 gate. ESO refresh
intervals (the SecretReference `RefreshInterval`, `client.go:336`) are minutes-scale; a per-dispatch
token written then immediately read via ESO may race the refresh. The cleaner fix (a projected
ServiceAccount token, as `02-secrets.md:294-297` P3 suggests) is a *different* mechanism than the one
WS1.4 prescribes.

**Fix.** Split the WS1.4 token item out of the "via ESO/SecretReference" framing. For the per-dispatch
Task JWT, either (a) keep it as a WorkflowRun param but mount via a per-run K8s Secret created
*alongside* the WorkflowRun (not ESO), or (b) move to a projected SA token with the BFF as audience.
Both are M1-reasonable; neither is "SecretReference + ESO." Don't let the gate language imply the
stored-secret path covers the minted-token path.

---

### MINOR-6 — God-package risk is real (~81k LOC merge) and the plan offers only a flat directory list

`asdlc-service` (~64k LOC) + `git-service` (~17k LOC) merge into one module. Both have a `models/`
package (`models/tasks.go` etc. vs `models/repository.go`, `org_credential.go`) — these will collide
or need namespacing. The plan's layout (`10` §0.2) is a flat list with no sub-domain structure for
`services/` (which will absorb the largest blast radius). **Fix:** see §4 — split `services/` into
domain sub-packages and adopt `internal/` so the public surface is intentional.

### MINOR-7 — Webhook ingress auth and multi-tenant namespace isolation are under-specified

WS1.6 puts "real ingress" in front of the webhook receiver (`20` line 124) but says nothing about
**HMAC verification** of GitHub webhooks (today the secret lives in `org_credentials.webhook_secrets`
plaintext JSONB / OpenBao `_platform`, `02-secrets.md` row 10). Real ingress without verified-source
HMAC is exposure. Separately, `workflows-<orgID>` namespacing is asserted but cross-org ESO-reader-role
scoping is not (the `02-secrets.md:281-285` P2 "split reader/writer roles" is deferred to Phase 3
WS3.2, yet Phase 1 materializes tenant secrets into per-org namespaces — the isolation property is
claimed before its enforcement lands). **Fix:** state HMAC verification as a WS1.6 line item; pull the
*reader-role-per-namespace* binding into WS1.3 (it already defines the roles) rather than Phase 3.

### MINOR-8 — DB migration strategy for dropping `org_secrets` values is absent

WS1.4 "remove `org_secrets` value storage (keep non-secret projections)" (`20` line 109) implies a
schema migration (drop/blank the `value` column, `02-secrets.md:99`) and a **data migration** of
existing AES-encrypted values into OpenBao before the column is dropped — clean-refactor or not, any
non-empty dev/staging DB needs this or secrets vanish. The plan has no migration step. **Fix:** add a
one-shot "read all `org_secrets`, `PushSecret` into OpenBao, create SecretReferences, then drop
column" job to WS1.4 (or explicitly declare "no data preservation; re-enter all secrets," which is
acceptable for a clean refactor but must be *stated*).

---

## 4. Recommended code-level boundary spec for `asdlc-service` (CORE)

The merge must produce **one binary, many enforced seams**. Use `internal/` so packages are
non-importable outside the module, and split `services/` by domain. Proposed layout:

```
asdlc-service/
├── cmd/asdlc-service/        # main: blank-imports providers, builds wiring, starts server
├── config/                          # env → typed Config (incl. SECRET_MANAGER_*, OPENBAO_*)
├── wiring/                          # the ONLY place providers + clients are assembled (DI seam)
├── internal/
│   ├── clients/
│   │   ├── openchoreo/              # OC generated client + typed SecretReference/GitSecret wrappers
│   │   ├── secretmanagersvc/        # secret abstraction (interfaces, client, registry, types)
│   │   │   └── providers/
│   │   │       ├── openbao/         # ONLY place hashicorp/vault is imported (Phase 0)
│   │   │       └── secretmanagerapi/# Phase 3; implements SecretReferenceManager
│   │   ├── github/                  # REST + Git Data API + App-JWT signing/token mint
│   │   └── thunder/                 # IdP client (identity)
│   ├── services/
│   │   ├── secrets/                 # orchestrates secretmanagersvc + OC SecretReference/GitSecret
│   │   ├── git/                     # spec commits, PRs, issues (uses clients/github)
│   │   ├── orchestration/           # task state machine, dispatch, WorkflowRun authoring
│   │   └── webhook/                 # ingestion + HMAC verify + projection
│   ├── controllers/                 # HTTP handlers (thin; call services)
│   ├── repositories/                # DB access (metadata/projections only — NO secret values)
│   └── models/                      # domain types (org/project/task/component)
└── middleware/                      # jwt, etc.
```

### Allowed-imports / dependency-direction matrix

Direction is strictly **downward**: `controllers → services → clients → (config/models)`. No upward,
no sideways between sibling clients.

| Package | MAY import | MUST NOT import |
|---|---|---|
| `cmd/` | `wiring`, provider pkgs (blank), `config` | `services`, `controllers`, `clients/*` directly |
| `wiring/` | all `clients/*`, `config`, `services` constructors | `controllers` |
| `clients/openchoreo` | `gen/`, `config`, `models` | `secretmanagersvc`, `github`, `services`, any Vault SDK |
| `clients/secretmanagersvc` | `clients/openchoreo` (for SecretReference upsert), `config` | `github`, `services`, `controllers` |
| `clients/secretmanagersvc/providers/openbao` | `hashicorp/vault/api` (**ONLY here**), parent iface | OC client, `github`, `services` |
| `clients/secretmanagersvc/providers/secretmanagerapi` | HTTP libs, parent iface | OC client (it sets `ocClient=nil` via the seam), Vault SDK |
| `clients/github` | HTTP, crypto (App-JWT signing), `config`, `models` | **`secretmanagersvc`, `clients/openchoreo`** (no secret backend, no OC client) |
| `clients/thunder` | HTTP, `config` | secret backends, OC client, `github` |
| `services/secrets` | `clients/secretmanagersvc`, `clients/openchoreo` | `clients/github`, any Vault SDK directly |
| `services/git` | `clients/github`, `repositories`, `models` | `clients/secretmanagersvc`, `clients/openchoreo`, Vault SDK |
| `services/orchestration` | `clients/openchoreo`, `services/secrets` (to author refs), `repositories`, `models` | Vault SDK, `clients/secretmanagersvc` provider pkgs |
| `services/webhook` | `repositories`, `models`, `clients/github` (verify) | secret backends |
| `controllers/` | `services/*`, `models`, `middleware` | any `clients/*` directly, any secret backend |
| `repositories/` | `models`, DB driver | any `clients/*`, any secret backend |
| `models/` | (stdlib only) | everything else |

### The hard line ("values vs references")

- **Values enter exactly one package: `clients/secretmanagersvc`** (and, transitively, its
  `openbao`/`secretmanagerapi` provider). A raw secret `[]byte`/`map[string]string` is produced by
  callers and handed to `PushSecret`/`CreateSecret`; it is **never** returned across a plane boundary.
- **`services/secrets` is the only service that touches `secretmanagersvc`.** It writes the value and
  (via `ocClient` inside `secretManagementClient`, `client.go:390-403`) authors the
  `SecretReference`/`GitSecret`. SecretReference authoring is therefore localized to
  `clients/secretmanagersvc` + `clients/openchoreo`, **not** in `github` or general `services`.
- **`clients/github` never sees the secret store.** It receives an already-resolved token from
  `services/git`, which got it from `services/secrets`. This is the §MAJOR-3 fix and the explicit
  answer to "does a secret value flow into `github`?" — only a *resolved credential for immediate use*
  does, never the *store* and never a value destined to cross a plane.
- **`repositories/` stores metadata only** — KV-path references, last4, status — never values
  (kills the `org_secrets.value` surface, §MINOR-8).

### The `SecretReferenceManager` seam wiring (validated, keep)

`wiring/` is the only place the branch lives (mirror `wire_gen.go:330-347`): if the configured
provider implements `SecretReferenceManager` and returns true (`secretmanagerapi`), pass `OCClient:
nil` so the platform service authors the SecretReference; otherwise (`openbao`) pass the OC client so
`services/secrets` authors it. This boundary is clean *as long as* §MAJOR-3 holds (the provider, not
`github`, owns the secret backend) and §BLOCKER-2's fences are enforced.

### Enforcement (required, not optional)

1. **Port + extend the existing AST import-fence test** (`import_fence_test.go`) into the merged
   module, covering every MUST-NOT row above (at minimum: Vault SDK confined to `openbao` provider;
   OC client out of `github`; secret backend out of `services/git`/`github`/`controllers`/`repositories`).
2. **`internal/`** placement so cross-cutting imports are compiler-checked at the module edge.
3. Optionally `depguard`/`go-arch-lint` in CI for the full matrix. Make this a **Test gate 0**
   acceptance item — the merge is not "done" until the fences are green.

---

## 5. Phasing corrections

Keep **structural-first** and **M1 = Phase 0 + Phase 1**. Targeted corrections:

- **Phase 0 (WS0.2):** build the `openbao` provider + the *seam/config* only; **defer the
  `secretmanagerapi` provider to Phase 3** (§BLOCKER-1). Add **WS0.5 — port & extend the import
  fence** (§BLOCKER-2, §4) as a Test-gate-0 item: "merge done = fences green."
- **Phase 0 Test gate:** add "the import-fence tests pass for all MUST-NOT edges" to the existing
  gate (`20` lines 78-84). Without it, the gate verifies behavior but not the boundary the phase
  exists to create.
- **Phase 1 (WS1.3):** pull the **reader-role-per-`workflows-<org>`-namespace binding** into Phase 1
  (it already defines the roles); the per-namespace ESO reader scoping is what makes cross-org
  isolation an architectural property, and Phase 1 already materializes per-org secrets (§MINOR-7).
  Phase 3 keeps only the cross-*cluster* split.
- **Phase 1 (WS1.4):** (a) add the **`org_secrets` data/schema migration** step or an explicit
  "secrets re-entered, no preservation" statement (§MINOR-8); (b) **separate the `ASDLC_BEARER`
  token item** from the SecretReference/ESO mechanism — minted tokens use a per-run Secret or
  projected SA token, not ESO (§MAJOR-5).
- **Phase 1 (WS1.6):** add **webhook HMAC verification** as an explicit line item alongside "real
  ingress" (§MINOR-7).
- **Phase 3 framing:** change "config-only flip / no code change / byte-for-byte" to "**additive
  provider + config**: register the `secretmanagerapi` provider built against the real deployed
  service contract; the seam makes it additive, not structural." This is the honest, still-strong
  version of the pluggability claim.
- **Ordering hazards confirmed clean:** deleting `effective-key` is correctly held to Phase 2 (after
  the gateway lands, `20` WS2.3) — the interim in-process endpoint in WS1.4 is the right bridge. Good.

No reason found to abandon structural-first or to move the M1 boundary.

---

## 6. Open questions for the user

1. **`secret-manager-api` protocol:** can you obtain the real contract from the deployed
   `wso2cloud-deployment` `dataplane/.../secret-manager-api/` (per `09` Surface B) *now*, so Phase 3
   builds against truth rather than a guess? If yes, the §BLOCKER-1 risk shrinks.
2. **`SecretLocation` mapping (§MAJOR-4):** is labs' anthropic key org-scoped or task-scoped, and do
   you want to reuse AM's `SecretLocation` with a documented "task→AgentName" convention, or fork it
   to a labs-native `{org, project, task, entity}` shape? (I recommend the fork.)
3. **Runner identity (§MAJOR-5):** for the per-dispatch Task JWT, do you prefer a per-run K8s Secret
   mount or a projected ServiceAccount token with the BFF as audience? This determines a WS1.4 detail.
4. **`org_secrets` data (§MINOR-8):** does any environment hold real tenant secrets that must survive
   the cutover (→ need a migration job), or is "re-enter all secrets" acceptable (clean refactor)?
5. **`internal/` adoption:** are you willing to move the clients/services under `internal/` (compiler
   enforced edge) in the merge, or must some be importable by other repos (which would weaken §4)?
6. **God-package (§MINOR-6):** OK to split `services/` into `secrets/git/orchestration/webhook`
   sub-packages during the merge, and to resolve the two colliding `models/` packages then?
