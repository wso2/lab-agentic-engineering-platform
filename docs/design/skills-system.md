# Skills System

Status: proposal · Author: anjana · Date: 2026-05-23

## Implementation status (as of 2026-05-24)

What has actually landed on `cleanup-auth` is **all of PR 1 + the runner-side half of PR 3**. The four built-ins flow end-to-end with all four auto-attached (architect inlines → tech-lead inlines → snapshot → runner pulls → SDK preloads), but the **tenant-facing surface (PR 2) and the architect's agency over skills (PR 3 front-half) are not built**. Concretely:

| PR | Scope | Status | Notes |
|---|---|---|---|
| **0a** | Architect golden fixtures (`tests/fixtures/architect/`, `architect-golden.spec.ts`, `normalize.ts`, `architect-shadow.ts`, `ARCHITECT_GOLDEN_TEMPERATURE`) | ❌ not started | No automated proof the extraction preserved architect output. |
| **0b** | SDK plugin-discovery spike (`remote-worker/src/lib/runner.test.ts`) | ❌ not started | Two-plugin name-collision behaviour assumed (later-wins) but unproven by test. |
| **1** | Built-in files + `skills`/`skill_audit_events`/`design_version_skill_snapshots` tables + bootstrap + architect/tech-lead inlining + snapshot UPSERT + slimmed base `asdlc` SKILL.md | ✅ done | Missing only `skill_service_test.go` + `skill_bootstrap_test.go`. `exposesAPI.userContext` deleted from the architect schema (legacy read-strip retained in `artifact_store.go`/`models/design.go` per design). `seedDefaultSkillsApplied` present (correct PR-1 interim). |
| **2** | Custom + imported REST API + console UI | ❌ not started | `skill_mutation_service.go`, `skill_import_service.go`, `skill_routes.go`, the `/api/v1/orgs/:orgId/skills(+/import)` endpoints, and **all** console pages (`Skills.tsx`, `SkillEditor.tsx`, `SkillViewer.tsx`, `SkillImportDialog.tsx`, `api/skills.ts`) are absent. Orgs cannot author, import, or even view skills. |
| **3** | Architect tools + runner pull + materialisation + issue-body bullets | ⏳ partial | **Done:** `GET /api/v1/tasks/:taskId/skills` + `task_skills_service.go`, `skills_pull.ts`, `skills_materializer.ts`, `oneshot.ts` pull-at-init, `runner.ts` preload array. **Missing:** `read_skill`/`attach_skill`/`detach_skill` in `architect/tools.ts`, `doc.ts` attach/detach mutators, issue-body defence-in-depth skill bullets in `issue_body.go`, and the one-shot backfill migration for legacy designs. |

**Hybrid-state coherence note.** The architect *prompt* already carries PR-3 language — it instructs the architect to call `read_skill(name)` / `attach_skill(name)` — but **those tools are not registered in `tools.ts`**. This is currently harmless *only* because `orgSkills` is always empty (no PR 2), so the `if (orgSkills.length > 0)` block that renders that instruction never fires. The moment PR 2 ships custom/imported skills, the architect would be told to call tools that don't exist. Attachment is therefore still automatic (`seedDefaultSkillsApplied` stamps all four built-ins), not LLM-driven — exactly PR 1's documented interim behaviour, but it means the "architect attaches the skills it judges relevant" goal is not yet live. **Implication for sequencing:** shipping PR 2 (REST + console) without PR 3's architect tools gives orgs a way to *author* skills that the architect still can't *attach* — useful for the editing/viewing surface, but the attach loop stays closed until PR 3's front-half lands.

## Problem

Today the platform's domain knowledge — *how to use the API Management gateway*, *how a React SPA reads `window._env_`*, *how a Go service avoids the CGO SQLite trap*, *what Thunder OIDC keys land in a SPA's runtime config* — is hand-stitched into three places:

1. **`agents/src/agents/architect/prompt.ts`** — 350 lines covering api.security classification, CORS rules, runtime-config keys, OIDC-SPA wiring, persistence rules, etc.
2. **`agents/src/agents/tech-lead/prompt.ts`** — surgical conditional sections for OIDC treatment, web-app upstream wiring, Go base image, external dependent APIs.
3. **`remote-worker/plugin/skills/asdlc/SKILL.md`** — 700-line monolith covering workflow + every stack + every auth shape.

Three problems with this:

- **Coupling.** Adding support for, say, a Python backend or a Vue SPA requires editing three files in two languages and rebuilding the runner image.
- **No tenant flexibility.** An org that wants its services to use Asgardeo instead of Thunder, or to mandate Bun over Node, has nowhere to express it. The architect emits Thunder-shaped designs because the prompt says so.
- **Monolithic loading.** Every architect call ships the full keyword catalog for api.security; every coding-agent pod loads 700 lines of SKILL.md even when it only needs the Go + protected-API sections.

## Goals

1. **Decompose** the in-line domain knowledge into named, individually-loadable units called *skills*.
2. **Tenant-configurable** — built-in skills ship with the platform; orgs add their own `custom` skills from the console or `import` AgentSkills directories from the ecosystem. (v1 ships built-ins read-only — editing them is a future PR.)
3. **Cross-agent propagation** — when the architect attaches a skill to a project, the tech-lead writing that project's tasks and the coding agent implementing them get the same skill automatically.
4. **Two-tier loading.** Platform skills (the four built-ins) encode best practices and must be applied — every agent sees their full bodies up-front, never via discovery. Org skills (custom + imported) appear in a manifest with name + description; the agent decides whether to load each body. This eliminates the "agent quietly skipped the platform skill" failure mode while keeping org-authored content cheap.
5. **File-first authoring** — built-in skills live as plain markdown files in the repo, easy to read, diff, and edit. The console editor reads/writes the same on-disk format.

## Non-goals

- Versioning skills like packages with semver + a lockfile. We pin a `version: <int>` field per skill and ship it as-is; bumps are informational only — there's no resolver.
- Letting users author skills that hot-patch existing agent code. Skills are prompt content only.
- Allowing skills to inject new MCP tools or external HTTP calls. Out of scope for v1.

---

## Scope — what skills can and cannot change

A skill is **prompt content**. Skills travel through the agent pipeline as text and shape what the agents emit. They do not deploy anything, do not reconfigure cluster infrastructure, and cannot opt out of platform behaviour. Be honest about this up front — a lot of what looks like a "skill" in casual conversation is actually platform infrastructure that lives in code.

### What a skill CAN change

The table below is **illustrative, not exhaustive** — concrete examples of the kinds of levers skills expose. Anything else that reduces to "different markdown in the prompt → different agent output, on today's cluster, without touching BFF/runner/cluster code" is also a skill. Treat the rows as a representative sample, not a closed list.

| Lever (examples) | Example skill content (which of the 4 built-ins owns it) |
|---|---|
| **Library choice within a stack** | "Use `chi` for routing instead of `net/http`." (`go`) "Prefer `zerolog` over the stdlib logger." (`go`) |
| **Code organisation conventions** | "Group handlers under `internal/api/`; group DB code under `internal/store/`." (`go`) "Vite project layout with `src/pages/`, `src/auth/`, `src/api/`." (`react-webapp`) |
| **Naming conventions** | "snake_case JSON field names." "All env var names UPPER_SNAKE." |
| **Error handling patterns** | "Always wrap errors with `fmt.Errorf` and a context phrase." (`go`) "Use problem+json for API errors." (`api-management`) |
| **Test patterns** | "Every handler has a table-driven test alongside it." (`go`) |
| **Dockerfile shape (within platform limits)** | "Use `golang:1.25-alpine` builder; multi-stage; distroless final." (`go`) "Use `nginx:alpine` static runtime; no envsubst." (`react-webapp`) |
| **Architectural preferences** | "Prefer embedded SQLite for per-user data over requesting a provisioned database." (`go`) |
| **OpenAPI conventions** | "Every response schema includes a top-level `data` envelope." (`api-management`) |
| **`componentAgentInstructions` shape** | What the architect writes into design.md for the coding agent. |
| **Issue body supplementary bullets** | Extra Scope/Acceptance criteria the tech-lead must append for components attached to this skill. |
| **Org-specific compliance language** | "Every monetary value field includes a `currency` sibling field." "Log every PII access to `/audit/v1/events`." (a *custom* skill an org authors) |

These all reduce to "different markdown in the prompt → different output." The platform doesn't care.

### What a skill CANNOT change

The table below is again **illustrative, not exhaustive** — representative platform-owned behaviours that no markdown edit can move. The general rule: anything wired in BFF code, OC controllers, cluster manifests, webhook handlers, or the runner image's build pipeline is off-limits to skills. The rows are a sample showing the *shape* of the boundary, not its full perimeter.

| Constraint (examples) | Why |
|---|---|
| Whether the API Platform gateway is in front of a service | Wired by `services/trait_sync.go` when `exposesAPI.auth` is set. The `api-management` skill body can describe the gateway's behaviour, but rewriting that body cannot disable or replace the gateway. |
| The CORS filter the gateway attaches | An Envoy filter on every `visibility: external` HTTPRoute, declared by the trait. Adding CORS in service code doubles headers and breaks browsers — agents must know this fact (the `api-management` skill documents it), but a skill cannot remove the gateway's filter. |
| The JWT claim → header mapping (`sub → X-User-Id`, etc.) | Wired declaratively in the `api-configuration` ClusterTrait's `claimMappings` (manifest: `deployments/manifests/api-platform/api-configuration-trait.yaml`). `services/trait_sync.go` enables jwtAuth but does not own the mapping. A skill that told the agent to read a different header would produce code that never matches — which is exactly why `api-management` (the skill) declares the header name authoritatively in its **Platform facts** section. (PR 1 deletes the dead `exposesAPI.userContext` field from the architect schema — see the "Delete the dead `exposesAPI.userContext` field" bullet under [PR 1 — Extract built-in skills as AgentSkills directories + project-level attachment](#pr-1--extract-built-in-skills-as-agentskills-directories--project-level-attachment). The skill body is the single source of truth for the header name; the field was redundant and confusing.) |
| Which IDP issues tokens | Controlled by `services/idp_service.go` and the per-org `OrganizationIDPProfile` row (platform / asgardeo / custom). Switching IDPs is a settings-page action against that profile, NOT a skill edit. The `thunder-authentication` skill is specifically Thunder-shaped; an org on Asgardeo would author an `asgardeo-authentication` *custom* skill alongside. |
| The `window._env_` key set the BFF emits | Hardcoded in `services/runtime_config_service.go` (`API_BASE_URL`, `THUNDER_*`, `<UPSTREAM>_URL`). Inventing a key in the `react-webapp` skill makes the agent's code throw at module load because the value is `undefined`. |
| The `workload.yaml` grammar OC consumes | OpenChoreo controllers parse a specific format; deviating means the build fails. A skill can choose what to *put in* a workload.yaml; it can't redefine the file's schema. (Lives in base prompts, not in a skill, because it's universal.) |
| The build/deploy pipeline | `dockerfile-builder` ClusterWorkflow, the watcher, the cascade hook, the on_hold re-evaluation — all in BFF Go code. Skills don't run during build. |
| Webhook handling / task state machine | `services/task_state.go` transitions are platform-owned. |

If an org needs any of the above changed, the answer is "platform code change" — not a skill edit. The four built-in skills are read-only in v1; each carries a **Platform facts** section at the top of its body that documents which sentences are descriptions of cluster behaviour (so the agent's output stays consistent with what the cluster actually does) versus recommended-practice content (what the agent should typically choose). Orgs that need to layer on additional guidance author a sibling **custom** skill and attach it alongside; the agent reconciles both bodies during code generation. See [Built-in catalogue](#the-built-in-catalogue-v1) below for the exact split.

### What about adding genuinely new capabilities?

Things like "support a Postgres-backed component" or "let agents talk to a corporate event bus" sound like skills, but they touch platform infrastructure:

- A Postgres backend exists already (`dbClient.ProvisionDatabase` in `task_service.go`), but the agent doesn't get those credentials piped in via `window._env_` or `workload.yaml` env. Making it usable is a BFF + runtime-config change, not a skill edit. A skill can guide the agent toward *requesting* the existing capability where it's wired (e.g. document the dbClient code path), but cannot wire new ones.
- An event-bus integration would require new dispatch-time work (credential injection, env var emission). Not a skill.

The litmus test: **if the change can be expressed by editing prompt markdown alone and would result in working code on today's cluster, it's a skill. Everything else is a platform PR.**

---

## Skill model

A skill is an **[AgentSkills.io](https://agentskills.io)–compliant directory** — the open standard originally written by Anthropic and adopted by Claude Code, Cursor, GitHub Copilot, Gemini CLI, OpenCode, Goose, OpenHands and ~30 others. Our skills are spec-clean AgentSkills directories that any compatible client can read directly; we ingest skills from any of those tools without translation. We narrow the scope by allowing only `SKILL.md` + optional `references/` files (no `scripts/`, no `assets/`) to keep the security and validation surface tight. See the [AgentSkills compatibility](#agentskills-compatibility) section below for the broader integration picture.

### Directory layout

```
<skill-name>/
├── SKILL.md             # required — frontmatter + the skill body all agents see
└── references/          # optional — supplementary detail files, loaded on demand
    ├── examples.md      # e.g. concrete code samples
    └── deep-dive.md     # e.g. background reading for the rare case
```

**One body, three agents.** Each skill ships a single SKILL.md body. All three agents (architect, tech-lead, coding-agent) see the same body when the skill is attached and loaded. They each decide what's relevant to their responsibility — the architect uses the "what to design for" hints; the tech-lead uses the "what to write into the issue body" hints; the coding-agent uses the implementation patterns. The skill author writes one cohesive document; the agents self-select. No `appliesTo`, no per-role files, no role routing.

This is intentional and aligns with the AgentSkills standard, which assumes one runtime per skill, one body per skill. Multi-agent splits (which were considered in earlier drafts of this design) added complexity without delivering value — the same content needs to repeat across roles for context anyway, and trusting the agents to filter is cheaper than maintaining three synchronized files.

`references/` is for content that grows too long for the main body. The coding-agent (via the Claude Agent SDK's native skill mechanism) discovers references progressively. For the architect and tech-lead in v1, **references are not exposed as a separate tool** — both agents see the SKILL.md body of every attached skill but nothing under `references/`. Rationale: the four built-in bodies fit comfortably (~5–15 KB each) and the v1 catalogue ships zero references. Add a reference-fetch tool only when (a) a built-in's body grows past comfortable inline-load size and ships a `references/` directory, or (b) an imported skill's `references/` content turns out to materially shape the architect's or tech-lead's output. Imported skills with references upload them fine; the content is visible only to the coding agent via SDK discovery.

### SKILL.md body shape

A built-in skill's SKILL.md body is structured (lightly) into three sections:

1. **What this skill does** — a paragraph: which platform capability is in play, when an agent should apply this skill, what it expects from the surrounding design.
2. **Platform facts** — bulleted statements describing cluster behaviour the agent's code MUST be consistent with. Editing these does not change the cluster — it only desyncs the agent's output. The editor surfaces this section with a yellow border and a tooltip explaining the implication.
3. **Recommended practice** — the meat: code patterns, file layout, library choices, conventions. For built-ins this is what the platform recommends (v1: not editable; layer additional rules via a sibling custom skill); for custom skills this is the org's own conventions.

The split is **advisory inside the body**. For built-ins (v1: read-only), the split documents which sentences a future override path would need to treat carefully. For custom skills, an author CAN include a Platform-facts section to document any external-system contracts their skill assumes (e.g. an `internal-analytics` custom skill might describe the audit endpoint URL the org's gateway exposes). (Earlier drafts of this design split skills into "platform-contract" and "practice" *kinds*, with the contract ones being read-only. That added catalogue complexity and didn't reflect reality — every real skill mixes both kinds of content. Collapsed in v1.)

Worked example — `asdlc-service/skills/builtin/api-management/SKILL.md`:

```markdown
---
name: api-management
description: >
  How the platform's API gateway validates JWTs, injects X-User-Id from the
  sub claim, attaches CORS, and how to write services and consumers that
  match. Applies to any service with exposesAPI.auth set, and to any
  consumer that calls a protected sibling.
metadata:
  asdlc.version: "1"
---

# API Management

## What this skill does

The platform fronts every service with `exposesAPI.auth` set through an
API gateway that validates JWTs, injects user-identity headers, and
attaches CORS. This skill tells the agent how to design and write code
that matches the gateway's contract — and how to call protected APIs
from a sibling component.

## Platform facts

The following statements describe cluster behaviour. Editing them in
this skill does not change the cluster; it only desyncs your agent's
output from reality.

- The gateway sits in front of every service whose `exposesAPI.auth` is
  `end-user-required` or `service-required`.
- The gateway validates JWTs against the org's IDP. Your service does
  NOT validate JWTs.
- The gateway injects identity headers (case-insensitive, manifest
  declares lowercase):
  - `sub → X-User-Id` (canonical caller identifier)
  - `username → X-User-Name` (display, optional)
  - `ouHandle → X-User-Ou` (multi-tenant, optional)
- The gateway attaches an Envoy CORS filter to every `visibility: external`
  HTTPRoute. Your service does NOT add CORS middleware.
- The agent does NOT see the gateway's client_id, JWT signing keys, or
  the upstream IDP's discovery URL. Those live in BFF code.
- For consumers of a protected sibling API, the BFF injects the upstream
  URL as `<NAME>_URL` env on the consuming workload's ReleaseBinding.
  The agent reads it from `process.env.<NAME>_URL` (Node) or
  `os.Getenv("<NAME>_URL")` (Go); it does NOT hardcode the URL.

## Recommended practice

(Architect)
- Set `exposesAPI.auth: end-user-required` when the API stores per-user
  data; `service-required` when it's machine-to-machine.
- For a SPA that calls this API, set `callerIdentity.mode: end-user` on
  the SPA component so the platform wires Thunder OIDC into it (see the
  `thunder-authentication` skill).
- Add `dependentApis: [<service-name>]` on consuming components to get
  the `<NAME>_URL` env injection.

(Tech-lead — issue body bullets to append for an attached service)
- Scope: "Reads X-User-Id on every protected handler; keys per-user
  rows on it."
- Acceptance criteria: "GET /v1/<resource> without an X-User-Id header
  returns 401."
- Acceptance criteria: "PUT /v1/<resource>/:id by user-A cannot mutate
  user-B's row."

(Coding agent — implementation)

Read X-User-Id from every protected handler; reject 401 when missing.
Per-user rows MUST be keyed on X-User-Id.

```go
func mustUserID(w http.ResponseWriter, r *http.Request) (string, bool) {
    uid := r.Header.Get("X-User-Id")
    if uid == "" {
        http.Error(w, `{"error":"missing X-User-Id"}`, 401)
        return "", false
    }
    return uid, true
}
```

Gate per-user queries with `AND user_id = ?`. Do NOT validate JWTs in
code. Do NOT add CORS middleware. Errors as `problem+json` with a
top-level `type`, `title`, `status`.
```

The body intentionally uses light per-agent headings (`(Architect)`, `(Tech-lead — issue body…)`, `(Coding agent — implementation)`) so each agent can quickly find its bit. These are conventions inside the body, not load-bearing structure.

### Frontmatter — SKILL.md

Spec-clean AgentSkills frontmatter. Anything passing `skills-ref validate` works. Platform extensions live under `metadata.asdlc.*` (the spec's `metadata` is a free-form `string → string` map).

| Field | Required | Constraint / use |
|---|---|---|
| `name` | yes | AgentSkills rule: 1–64 chars, lowercase kebab, matches directory name, no leading/trailing/consecutive hyphens. |
| `description` | yes | 1–1024 chars. Shown to agents as the manifest line for custom/imported skills (the agent decides whether to `read_skill` based on this alone, so be specific about *what* and *when*); for built-ins, it's still the catalogue label but the body is already inlined into the prompt. |
| `license` | no | AgentSkills standard. Surfaced in the console at import time. |
| `compatibility` | no | AgentSkills standard. Used at attach-time to surface "this skill expects X runtime that the pod doesn't ship" warnings. |
| `allowed-tools` | no | AgentSkills standard (experimental). Accepted for cross-tool compatibility; not honoured by our runner in v1. |
| `metadata.asdlc.version` | yes for built-ins | Hand-bumped integer-as-string. Informational only in v1 (no drift detection, since v1 has no override mechanism); bump when behaviour materially changes for human readers' sake. The follow-on `skill-overrides.md` design re-introduces drift detection on top of this field. |

**Derived, not declared:**

- `kind` — stored on the `skills` table row (`builtin` for rows seeded from bundled files; `custom` for rows POSTed by the org; `imported` for rows from tarball upload).
- There is no `appliesTo` field. All skills are visible to all agents.
- There is no `activation` field or sibling file. Attachment is the architect's call (LLM-driven via `attach_skill`), not an evaluated rule.
- `id` — there is no separate id; the `name` field is the catalogue key.

Reserved names: imported skills cannot use `name` of `asdlc`, or names matching `builtin-*`, `custom-*`, `imported-*`. These prefixes are used at materialisation time (see [Materialisation](#materialisation-at-dispatch)) and reserving them client-side avoids collisions. Custom and imported skill names are additionally capped at 55 chars so the 9-char prefix at materialisation keeps the resulting `name` within AgentSkills' 64-char ceiling. Our four built-ins (max 23 chars: `thunder-authentication`) all fit comfortably.

---

## The built-in catalogue (v1)

Four built-in skills, each corresponding to a real platform capability the four-skill design intentionally maps to. The platform ships them; **orgs cannot edit them in v1** ([built-ins read-only](#built-ins-are-read-only-in-v1)); orgs can author additional custom skills or import AgentSkills directories and attach them alongside.

| ID | What it covers | Maps to platform capability |
|---|---|---|
| `api-management` | Gateway JWT validation, `sub → X-User-Id` header injection, CORS filter, OpenAPI conventions, the `mustUserID` helper, `AND user_id = ?` gating, `<NAME>_URL` env injection for dependent APIs, problem+json error shape. | `services/trait_sync.go`, the `api-configuration` ClusterTrait, `services/external_api_catalog.go`. |
| `thunder-authentication` | Per-project Thunder OAuth client (BFF-owned, agent doesn't see client_id), `callerIdentity.mode` design field, `window._env_.THUNDER_*` key set, OIDC client wiring with `oidc-client-ts`, redirect URI computed by BFF. **Pairs with `react-webapp`** when the project contains a SPA — the SPA wiring patterns live in `react-webapp`'s body; attaching `thunder-authentication` alone on a SPA project leaves the SPA without runtime/layout guidance. | `services/idp_service.go`, `services/runtime_config_service.go:layerThunderKeys`. |
| `react-webapp` | Vite project layout, multi-stage Dockerfile builder → `nginx:alpine` runtime, synchronous `/env-config.js` load before the bundle, the authoritative `window._env_` key set, "throw at module load on missing key" rule, static-only nginx config (no envsubst). | `services/runtime_config_service.go:EmitForComponent` (the `/env-config.js` emission), `/usr/share/nginx/html/` mount contract. |
| `go` | `golang:1.25-alpine` builder pin (the build pod runs with `GOTOOLCHAIN=local`), pure-Go `modernc.org/sqlite` (CGO times out under the build pod's CPU throttle), suggested layout (`cmd/`, `internal/`), `net/http` or `chi`, port 9090, `GET /health` liveness, multi-stage Dockerfile → slim runtime, embedded SQLite for per-user data inside the owning service. | `app-factory-coding-agent` runner workflow, `dockerfile-builder` ClusterWorkflow. |

Each of the four ships read-only in v1 — the console renders them in a viewer, not an editor. The platform-facts section at the top of each body documents which sentences are descriptions of cluster behaviour (the agent's output stays consistent with what the cluster actually does) versus recommended-practice content (typical conventions the org might want to layer on). Orgs that want to layer additional rules author a sibling `custom` skill and attach both alongside; the agent reconciles both bodies during code generation.

**What's NOT in the catalogue.** Universal facts that aren't stack-specific stay in the base prompts, not in skills:

- Branch + PR mechanics, the `Closes #N` PR→task link, the deny-list against `gh pr merge` and repo-config edits, "a human merges." → base prompt of every agent.
- The flat `WorkloadDescriptor` grammar OC's controllers consume, visibility levels, `dependencies.endpoints` not used in v1. → base prompt of the architect.
- The runner sandbox shipping `go` + `node` + `npm` only; lockfile-hash rules; `[build-failed]` draft-PR escape valve. → base prompt of the coding-agent.
- Multi-stage Dockerfile pattern at a generic level (the *specific* base images go inside `go` and `react-webapp`). → covered by per-skill examples; no separate dockerfile skill.
- Issue-body conventions (Scope/Acceptance/Out-of-scope structure). → base prompt of the tech-lead.

Earlier drafts of this design carved these out as separate "platform-contract" skills. They're not skills — they're universal facts that apply to every task regardless of stack or auth shape, so they belong in the agents' base prompts where they always load. Putting them in the catalogue would have meant either auto-attaching them to every project (noise in the UI) or risking an architect that forgets to attach them (broken).

### Slots for org-custom skills

The catalogue advertises these as templates orgs can author; the platform ships none of them.

- `payments-pci-handling` — logging requirements for card data.
- `gdpr-erasure-handling` — patterns for "right to be forgotten" endpoints.
- `internal-analytics` — event taxonomy + ingest endpoint URL.
- `corporate-style-guide` — naming, README structure, license headers.
- `asgardeo-authentication` — a sibling to `thunder-authentication` for orgs whose `OrganizationIDPProfile` is Asgardeo (the platform-facts here would describe Asgardeo's specific OIDC behaviour and discovery URL shape).

Created as `kind: custom`. Lives in the `skills` table with `org_id = <orgId>`.

**Caveat on `asgardeo-authentication` (and any IDP-swap skill).** Writing this custom skill is currently a **half-step**, and the doc is honest about it. The skill body can guide the agent to emit Asgardeo-compatible client code (different discovery URL, different scope set), BUT:

- The BFF still provisions a Thunder OAuth client when `callerIdentity.mode: end-user` is set on a component (`services/idp_service.go:EnsureProjectOAuthClient` is keyed on `mode`, not the attached skill).
- The BFF still writes `THUNDER_*` keys into `window._env_` via `services/runtime_config_service.go:layerThunderKeys` (also unconditional on attached skills).
- The agent will read `THUNDER_*` keys from `window._env_` even though the skill body told it to write Asgardeo client code; the actual issuer it talks to is Thunder.

Full IDP swap requires the platform PR that makes `idp_service.go` and `runtime_config_service.go` honour the org's `OrganizationIDPProfile.flavor` field (`platform | asgardeo | custom`) by emitting profile-appropriate keys (`ASGARDEO_*` instead of `THUNDER_*`) and provisioning at the right IDP. Until that PR lands, attaching `asgardeo-authentication` produces code that *talks Asgardeo client semantics against a Thunder backend* — the OIDC handshake still completes (both speak OIDC), but tenant-scoping and Asgardeo-specific extensions don't apply. Document the partial state clearly in the skill body so the org isn't surprised.

---

## Storage & API

### Single source of truth: the `skills` table, seeded from bundled files

The BFF owns one table that holds every skill — built-in, custom, and imported. Built-in markdown files ship in the BFF binary; at startup, the BFF UPSERTs each into the table. After bootstrap, **the DB is the only thing anyone reads**. A single `SkillService` fronts the table for the architect input, the tech-lead input, the runner pull endpoint, and the console.

```
asdlc-service/skills/builtin/         (files in the repo; embedded into the BFF binary)
  api-management/SKILL.md
  thunder-authentication/SKILL.md
  react-webapp/SKILL.md
  go/SKILL.md
```

Schema:

```sql
CREATE TABLE skills (
    org_id        VARCHAR(64)  NOT NULL DEFAULT '',  -- '' for kind=builtin (global);
                                                      -- real org id for custom/imported
    skill_name    VARCHAR(64)  NOT NULL,
    kind          VARCHAR(32)  NOT NULL,             -- builtin | custom | imported
    description   TEXT         NOT NULL,             -- denormalised from frontmatter
    skill_md      TEXT         NOT NULL,
    "references"  JSONB        NOT NULL,             -- { "references/<file>.md": "...", ... }
    version       INT          NOT NULL,             -- metadata.asdlc.version
    content_sha   VARCHAR(64)  NOT NULL,             -- sha256 over canonical concat
    license       TEXT,                              -- imported only
    compatibility TEXT,                              -- imported only
    created_at    TIMESTAMPTZ  NOT NULL,
    updated_at    TIMESTAMPTZ  NOT NULL,
    updated_by    VARCHAR(64),
    PRIMARY KEY (org_id, skill_name)
);
CREATE INDEX skills_kind_idx ON skills (kind);

CREATE TABLE skill_audit_events (
    id             BIGSERIAL PRIMARY KEY,
    org_id         VARCHAR(64) NOT NULL,
    skill_name     VARCHAR(64) NOT NULL,
    action         VARCHAR(32) NOT NULL,             -- create | update | delete | import | bootstrap-upsert | bootstrap-purge
    actor          VARCHAR(64) NOT NULL,             -- bootstrap uses 'system'
    before_state   JSONB,
    after_state    JSONB,
    occurred_at    TIMESTAMPTZ NOT NULL,
    correlation_id VARCHAR(64)
);
CREATE INDEX skill_audit_events_org_skill_idx ON skill_audit_events (org_id, skill_name, occurred_at);
```

Builtins use `org_id = ''` (empty string, not NULL, so the PRIMARY KEY composite works without NULL-PK gymnastics).

### Bootstrap

On BFF startup, after migrations run, a one-shot `SkillBootstrap.Run()` step reads each `asdlc-service/skills/builtin/<name>/SKILL.md` (via Go `embed.FS`), parses the frontmatter, and runs:

```sql
INSERT INTO skills (org_id, skill_name, kind, description, skill_md, references, version, content_sha, ...)
VALUES ('', $name, 'builtin', ...)
ON CONFLICT (org_id, skill_name) DO UPDATE
   SET description = EXCLUDED.description,
       skill_md    = EXCLUDED.skill_md,
       references  = EXCLUDED.references,
       version     = EXCLUDED.version,
       content_sha = EXCLUDED.content_sha,
       updated_at  = NOW()
   WHERE skills.content_sha <> EXCLUDED.content_sha;   -- only update on actual change
```

Then purge built-ins that have been renamed/removed between releases:

```sql
DELETE FROM skills WHERE kind = 'builtin' AND skill_name NOT IN ($bundled_names);
```

Both writes audit to `skill_audit_events` with `actor = 'system'`, `action ∈ {bootstrap-upsert, bootstrap-purge}`. Multiple BFF replicas seeding concurrently is safe — Postgres's native `ON CONFLICT DO UPDATE` is atomic; if two replicas write at the same moment, the second wins idempotently because the content is identical.

The bundled built-ins never write to the DB at runtime (only at startup). Custom/imported never write at startup (only via REST API). Read-after-write is via the DB on the next request.

### Resolution

```
SkillService.Resolve(orgId, name) -> Skill | nil:
    SELECT * FROM skills
        WHERE skill_name = name
          AND org_id IN ('', orgId)               -- builtin (global) OR this org's
        ORDER BY org_id DESC                      -- non-empty org_id wins over '' (lexicographic)
        LIMIT 1
```

In v1, name-collision validation (below) prevents custom/imported from reusing a builtin name — so only one row ever matches. The `ORDER BY` is the structural answer for when overrides land later (a future `org_id = orgId, kind = 'builtin-override'` row would naturally win).

Listing:

```
SkillService.List(orgId) -> Skill[]:
    SELECT * FROM skills
        WHERE org_id IN ('', orgId)
        ORDER BY kind, skill_name             -- builtin first, then custom, then imported
```

Catalogue prefixed ID (used in snapshot rows, audit log, materialised plugin directory name):

- Built-in: `builtin/<name>`
- Custom: `custom/<name>`
- Imported: `imported/<name>`

Defensive resolver behaviour: if a stored custom/imported row fails frontmatter parsing (e.g. a future validator tightening), the resolver returns `nil` for that name and a structured warning logs. The catalogue listing omits it; the architect cannot attach what isn't there.

**RBAC.** No `org_admin` role exists in the BFF today. PR 2 ships with the existing per-org-member JWT gate (any authenticated org member can create/edit/import; `updated_by` is logged). A follow-on PR (`docs/design/org-rbac.md`, out of scope here) tightens to a real `org_admin` role.

### Validation rules

**Custom skill (POST `/api/v1/orgs/:orgId/skills`)**:

1. `name`: AgentSkills kebab rule, ≤ 55 chars (leaves room for the 9-char materialisation prefix inside the spec's 64-char limit); not reserved (`asdlc`, `builtin-*`, `custom-*`, `imported-*`); does not collide with any existing row in `skills` visible to this org (built-in OR another custom/imported in this org).
2. SKILL.md frontmatter has `name` + `description`; `description` is 1–1024 chars.
3. `references` JSONB keys are all `references/<file>.md` paths; no `scripts/`, `assets/`, other top-level paths.
4. Total size ≤ 400 KB.

**Imported skill (POST `/api/v1/orgs/:orgId/skills/import`)** — multipart tarball upload:

1. Tarball decodes to a single top-level directory. Reject `..` paths, symlinks, hardlinks, files outside the directory, AppleDouble entries.
2. Only `SKILL.md` and `references/<file>.md` allowed.
3. All custom-skill rules above apply (frontmatter, size, reserved names, no-collision-with-builtin).
4. `license` + `compatibility` from frontmatter are stored and surfaced in the import response — the user must explicitly accept any non-OSI license before the import commits.
5. `compatibility` requiring a runtime the runner image doesn't ship (e.g. `python`, `rust`) → warning in response (not a block).
6. `allowed-tools` is preserved in metadata but not honoured by our runner in v1 → `allowed_tools_ignored` in response.

Validation failures return a structured `{ code, message, path }[]` array; nothing persists half-parsed.

### Built-ins are read-only in v1

The four built-ins are written only by the bootstrap step. PUT and DELETE against a built-in name return 403 `SKILL_NOT_EDITABLE`. This is a deliberate scope cut — most useful customisation can be done by authoring a sibling custom skill and attaching both to the project. Built-in editing (drift detection, three-way merge, `[Re-merge]` UI) is a follow-on design (`docs/design/skill-overrides.md`, out of scope here); the single-table model means it lands later as just a precedence rule + an editor, not a schema change.

### REST API (on `asdlc-service`)

```
GET    /api/v1/orgs/:orgId/skills
       → [{ name, kind, version, description, content_sha, editable: bool }, ...]
       editable = (kind != 'builtin')

GET    /api/v1/orgs/:orgId/skills/:name
       → { name, kind, version, description, license, compatibility,
           skill_md, references, content_sha, editable }

POST   /api/v1/orgs/:orgId/skills
       Body: { name, skill_md, references }
       Creates kind=custom. Validation rules above. 409 NAME_COLLISION on
       collision with builtin or existing custom/imported.

PUT    /api/v1/orgs/:orgId/skills/:name
       Body: { skill_md, references }
       Updates a kind=custom row. 403 SKILL_NOT_EDITABLE for built-ins; 404
       when name doesn't map to an existing custom skill.

DELETE /api/v1/orgs/:orgId/skills/:name
       Custom: deletes. Imported: see [Imported-skill deletion lifecycle] —
       409 IMPORTED_SKILL_IN_USE when in-flight tasks reference it. Built-in:
       403 SKILL_NOT_EDITABLE.

POST   /api/v1/orgs/:orgId/skills/import
       Multipart: tarball. Creates kind=imported. Response:
       { name, license, compatibility, warnings: [...] }
```

Everyone — architect-input building, tech-lead-input building, the runner's pull endpoint (see [Coding agent](#coding-agent-claude-agent-sdk-remote-worker)), the console — reads through `SkillService`. The agents-service never touches the DB; it gets the resolved set on the request.

### Project-level attachment

Attachment is per-project, not per-component. (Earlier drafts proposed per-component `skillsApplied`; the simplification: skills shape the project's overall stack and conventions, so a project-level set is sufficient and matches how orgs actually think about "this is our Go + Thunder + React project, attach those three.")

**Where it lives — root `design.md` frontmatter (new block).** Today the root `specs/design/design.md` is written **body-only** by `artifact_store.go:502` — an explicit choice ("keep the markdown editor's view clean (no visible YAML frontmatter as a stray heading)"). This design REVERSES that choice for two reasons:

1. The root file is the natural place for project-wide design state — the same level at which the architect thinks about skills.
2. Versioning is already tied to it (`v<N>-<M>` git tags), so a snapshot of `skillsApplied` at design-version N is recoverable just by reading the tagged file.

PR 1 adds a frontmatter block to the root design.md containing the new `skillsApplied` field (and reserves room for future project-wide fields). To preserve the "clean editor" experience, the console's markdown viewer detects YAML frontmatter (`splitFrontmatter` logic already exists for component design files) and hides it from the rendered preview; the raw editor still shows it.

```yaml
---
skillsApplied:
  - api-management
  - thunder-authentication
  - react-webapp
  - go
  - payments-pci-handling      # a custom skill
---

# Design

(... per-component design body ...)
```

`ArtifactStore.SplitDesign` (`asdlc-service/services/artifact_store.go`) gains a `SkillsApplied []string` field on its return type and a YAML-frontmatter parse step at the top of the root design file. `AssembleDesign` writes the frontmatter block (sorted entries for stable diffs) when the field is non-empty; omits it entirely when empty (keeps backwards-compatible — existing designs with no skills section produce no frontmatter block).

There is no `skillsSuppressed` — without auto-activation rules, suppression has nothing to subtract from. The architect's `detach_skill` removes the entry from `skillsApplied`.

---

## AgentSkills compatibility

The skill format on disk is exactly an [AgentSkills.io](https://agentskills.io) directory. Our skills validate against `skills-ref validate`; our directory layout matches what Claude Code, Cursor, GitHub Copilot, Gemini CLI, OpenCode, Goose, OpenHands, Letta, Roo Code, Tabnine, Laravel Boost and ~30 other clients consume natively. This buys us three things and costs us nothing of structural significance.

**What we gain.**

1. **Ecosystem ingestion.** Any AgentSkills directory authored for another tool can be uploaded into our platform via `POST /api/v1/orgs/:orgId/skills/import` — no translation step. A team's existing Claude Code skill plugin works in our coding-agent pods unchanged (subject to the no-`scripts`/no-`assets` validator).
2. **Local-developer flow stays coherent.** A developer running our `remote-worker/plugin` plus their own Claude Code installation sees the same skill directories the cluster sees. `claude plugin install <our-repo>/remote-worker/plugin` continues to work; no platform-specific tooling needed locally.
3. **Future-proof.** If the AgentSkills standard grows additional capabilities (deterministic activation, multi-agent splits), we adopt them by **promoting** any of our `metadata.asdlc.*` extensions to first-class fields. Until then, we're parked under `metadata.*` which the spec explicitly endorses as the vendor-extension surface.

**What an imported skill looks like.** Concrete shape of the upload from the user's perspective:

```
my-pci-skill.tar.gz                        # what the user uploads
└── my-pci-skill/                          # tarball top-level dir, named after skill
    ├── SKILL.md                           # AgentSkills frontmatter + body
    └── references/                        # optional
        └── examples.md
```

Activation: there's no auto-attach. The architect sees the imported skill in the catalogue listing (name + description); the architect attaches it via `attach_skill` when relevant.

**Validation surface — what we reject from spec-conforming uploads:**

- `scripts/` and `assets/` directories — outside our v1 scope.
- Symlinks, hardlinks, AppleDouble metadata, `..` paths — same as any tarball-handling code.
- `name` reserved prefixes (`asdlc`, `builtin-*`, `custom-*`, `imported-*`) — our materialisation layer uses these.
- `name` colliding with a built-in skill — orgs that want to layer onto a built-in import under a different name (`acme-api-extensions`) and rely on the architect to attach both.
- `compatibility` field requiring a runtime the runner image doesn't ship (surfaced as a warning, not a hard block).
- `allowed-tools` field is accepted (preserved in metadata for cross-tool compatibility) but not honoured by our runner in v1.

**What we don't validate (and why):** the description's clarity, the body's quality, the reference content. The platform is not a content reviewer. The org's audit log (`skill_audit_events`) is the protection surface.

---

## Per-agent integration

### Architect (Vercel AI SDK, `agents/src/agents/architect/`)

Today the prompt is built from `prompt.ts:systemPrompt` (huge) + `buildUserPrompt(input, doc)`. We change this to:

**System prompt**: trimmed to the workflow (three phases, finalize semantics), the output schema rules, the OC workload grammar facts, the branch + PR mechanics — all the universal stuff. Stack-specific knowledge (Go, React, Thunder, gateway) leaves the system prompt entirely; it lives in skills now.

**User prompt** carries two skill sections, one per tier. The platform's four built-ins are **inlined whole** under a "MUST consult" heading — the architect cannot skip them. Org-authored skills (custom + imported) appear under "load if relevant" as a manifest of name + one-line description; bodies load via `read_skill(name)`. The Vercel AI SDK has no native preload mechanism (that lives in the Claude Agent SDK), so we build both tiers in the prompt ourselves.

```
## Platform skills — MUST consult before designing

The following encode ASDLC platform best practices. Apply them to every
component where their concern is relevant. Their full content is below —
you do not need to load them.

### api-management
<full SKILL.md body>

### thunder-authentication
<full SKILL.md body>

### react-webapp
<full SKILL.md body>

### go
<full SKILL.md body>

## Org skills — load if relevant

The following are authored by your organization or imported from the
AgentSkills ecosystem. Call `read_skill(name)` when the description
suggests relevance.

- `payments-pci-handling` — Org-specific PCI-DSS logging requirements.
- `internal-analytics` — Event taxonomy + audit endpoint for in-house dashboards.
- `acme-style-guide` — Imported. Org-wide naming + license header conventions.

Then call `attach_skill(name)` to mark a skill active on this project so the
tech-lead and coding agent inherit it.
```

**Why attach if built-ins are always inlined for the architect?** Attachment scopes propagation. The architect itself sees all four built-ins (it doesn't know which apply yet, and 4 × ~5–15 KB is cheap). Downstream agents only get the attached subset — a Go service task doesn't need `react-webapp`'s body shipped to it.

**New tools** (added to `agents/src/agents/architect/tools.ts` in PR 3):

| Tool | Input | Returns | Side effect |
|---|---|---|---|
| `read_skill` | `{ name }` | `{ name, description, body }` — full SKILL.md body | none (stateless). For org skills; built-in bodies are already inlined in the prompt, so the tool steers away from them in the description. Returns built-in bodies if asked anyway (returns the same content the prompt already has) so the agent never gets a hard error. |
| `attach_skill` | `{ skillName }` | `{ ok: true }` or `{ error: "unknown-skill" }` | adds to design root frontmatter `skillsApplied`; emits SSE `project-updated`; **does NOT invalidate openapi** |
| `detach_skill` | `{ skillName }` | `{ ok: true }` (idempotent — no-op when not attached) | removes from `skillsApplied`; emits SSE |

`DesignFile.skillsApplied: string[] | undefined` is the persisted project-level field. `read_skill` is read-only inspection; `attach_skill` / `detach_skill` are the writers. The earlier draft tool `select_skill_reference` is dropped from v1 — the four built-ins ship no references and imported-skill references are only useful to the coding agent (via SDK discovery), not the architect.

**Three call sites for skill propagation:**

1. `services/design_service.go:StreamArchitect` — calls `SkillService.List(orgId)`, splits the result by `kind`, and ships **both tiers separately** on the architect input: `builtinSkills: SkillRecord[]` (full bodies, for inlining) and `orgSkills: SkillDescription[]` (name + description, for the manifest). After PR 1: `builtinSkills` is the four; `orgSkills` is empty. After PR 2: `orgSkills` is populated. The architect reads org-skill bodies via `read_skill` on demand (added in PR 3).
2. `services/task_stream.go:StreamTechLeadDetail` — reads `skillsApplied` from the design's root frontmatter, resolves each skill's body via `skillCatalog.Resolve`, ships the full bodies as `TechLeadDetailItem.skillsResolved` (new field on `clients/agents/Client.go`). All attached skills go to every task in the project's tech-lead detail batch.
3. `services/task_service.go:ensureIssueForTask` (called from `task_stream.go:persistAndIssue` during the tech-lead T4 phase, NOT from dispatch) — **the canonical handoff snapshot.** Resolves the live attached set and lookup-or-creates rows in `design_version_skill_snapshots` keyed by `(project_id, design_version)` immediately before `gitClient.CreateIssue`. This is the only write site for snapshots; `dispatchOne`, `RetryTask`, and the cascade hook READ from this table and never write to it. Console edits between issue creation and dispatch don't affect in-flight tasks.

**Snapshot at design-version finalize; later edits don't retroactively change open tasks.** The snapshot is keyed on `(project_id, design_version)` rather than per-task because every task that derives from the same design version shares the same `skillsApplied` (the field is per-project, not per-component). This dedupes storage massively: a 5-component design with 4 attached skills × 10 KB each stores 5 × 4 × 10 KB = 200 KB if keyed per-task, vs. 1 × 4 × 10 KB = 40 KB keyed per design version. Tasks reference the snapshot indirectly via their existing `SourceDesignVersion` field on `ComponentTask`.

Inside `task_service.go:ensureIssueForTask`, just before calling `gitClient.CreateIssue`, the BFF performs an upsert: if a `design_version_skill_snapshots` row already exists for `(project_id, task.SourceDesignVersion, *)`, do nothing; otherwise resolve every skill in the design's `skillsApplied` and INSERT one row per skill. Subsequent `RetryTask` and cascade-driven dispatches read these rows; they do not re-snapshot. Tasks whose issues pre-date this PR have no rows — see [Backfill](#backfill-for-pre-skills-tasks) for the fallback rule.

```sql
CREATE TABLE design_version_skill_snapshots (
    project_id        VARCHAR(64)  NOT NULL,
    design_version    VARCHAR(32)  NOT NULL,        -- e.g. "v2-1" — matches ComponentTask.SourceDesignVersion
    skill_id          VARCHAR(128) NOT NULL,        -- prefixed: "builtin/api-management"
    materialized_name VARCHAR(96)  NOT NULL,        -- "builtin-api-management"
    kind              VARCHAR(32)  NOT NULL,        -- builtin | custom | imported
    skill_md          TEXT         NOT NULL,        -- resolved SKILL.md from catalog
    "references"      JSONB        NOT NULL,        -- resolved references
    created_at        TIMESTAMPTZ  NOT NULL,
    PRIMARY KEY (project_id, design_version, skill_id)
);
```

The bodies are snapshotted (not just IDs) because custom and imported skill content can change between design-version finalize and dispatch. The whole point of the snapshot is "what was the contract when the design was finalized?" — that needs the actual text. (Built-in bodies don't change at runtime in v1 since they're read-only — but the snapshot keeps the symmetry simple and futureproofs against the override design landing later.) From the snapshot moment forward, the snapshot is what materialises into the workspace, what the issue body's appended skill-fact bullets are computed against, and what the cascade hook's dispatch path operates on. The user can edit custom skills in the console mid-flight, but those edits land in new design versions and affect future tasks; in-flight tasks tied to an earlier `SourceDesignVersion` use that version's frozen snapshot.

**No GC in v1.** Per-design-version keying bounds growth: roughly `(design versions per project) × (4–6 attached skills) × (5–10 KB each)`. For an org with 1000 projects each at 100 design versions, that's ~5 GB — order-of-magnitude acceptable. A `cleanup_design_version_skill_snapshots()` job that drops rows for design versions where every referencing task is in `merged | rejected | abandoned` for >90 days is filed as a future-PR (`docs/design/skill-snapshot-gc.md`, out of scope here).

**`references` JSONB is empty in v1.** No built-in ships a `references/` directory; the field is `{}` for every row written from the v1 catalogue. Wire types and DB columns are still defined to carry references because (a) imported AgentSkills directories may legitimately ship them — the field is populated for `kind: imported` snapshots — and (b) future built-ins may grow them. Implementers should not infer "empty in v1 means we can simplify the type" — the schema is forward-shaped.

**Paths that read the snapshot, not the live set** (all key on `(project_id, task.SourceDesignVersion)`):

- `services/issue_body.go:buildIssueBody` — the appended platform-facts bullets are computed against the snapshot rows, not the live catalogue. PR 3 threads the snapshot in as a parameter.
- `services/task_stream.go:editIssueBodyWithRetries` — re-renders the issue body on tech-lead-detail re-streams; reads the snapshot so a re-render after a console edit doesn't surprise the in-flight agent with new mandatory bullets that diverge from what the workspace has materialised.
- `dispatch_service.go:RetryTask` — does **not** re-snapshot. The retried run uses the original snapshot for the task's `SourceDesignVersion` so the agent's freshly minted workspace matches the original issue body.
- `dispatch_cascade_hook.go:OnTaskDeployed` → `DispatchTasks` — when dispatching an on-hold task, reads the snapshot for that task's `SourceDesignVersion`. NEVER re-snapshots, even if `skillsApplied` on the live design has changed since the snapshot was taken.

Paths that read the live set:
- The architect agent itself (catalogue loads fresh on each architect call — that's the editing surface).
- The console settings page (always shows live state for the editor).

This split is the entire intent of the snapshot rule: lock contract for in-flight code-gen; keep editing live for design-time exploration.

### Tech-lead (Vercel AI SDK, `agents/src/agents/tech-lead/`)

Already two-phase. Skills attach at both phases. **There is no two-tier split here** — by the time the tech-lead runs, the architect has already attached only the relevant skills, so all attached skills (built-in OR org) get the same treatment: bodies inlined into the user prompt with "MUST consult" framing. No `read_skill` tool, no manifest-only listing.

**Plan phase**: the planner is told which skills are attached at the project level (one-line descriptions of each) so it can frame the task split with that context, but doesn't load bodies — the descriptions are enough for splitting. `TechLeadPlanInput` (`agents/src/agents/tech-lead/schema.ts`) gains a project-level `attachedSkills: Array<{ name: string; description: string }>` field (the planner needs both name and description — names alone aren't enough context for it to frame the split). Add full-body loading here in a future version if planner-time loading becomes valuable.

**Detail phase**: for each task, the BFF auto-loads the full bodies of every skill in `skillsApplied` (both built-in and org). They're concatenated into the user prompt under a "Skills active for this project" section with the same "MUST consult" framing the architect saw. References are not loaded — the four built-in bodies fit comfortably (~5–15 KB each), and none ship a `references/` directory in v1. The decision: add a reference-fetch tool only when a built-in's body grows past comfortable inline-load size and a `references/` file is introduced. Until then, the tech-lead detail phase sees every attached skill's full body in its user prompt; SKILL.md bodies that happen to mention a reference filename are advisory text only (the tech-lead can't load it). Imported skills with references are similarly flattened — only the SKILL.md body is in the tech-lead prompt; references are visible only to the coding agent via SDK-native discovery.

```
## Skills active for this project

The following skills are attached to this project. Treat their content as
mandatory rules for the issue body you produce — look for the
(Tech-lead — issue body bullets...) section in each.

### api-management

(full SKILL.md body)

### thunder-authentication

(full SKILL.md body)

### go

(full SKILL.md body)
```

The big surgical "if OIDC then..." / "if Go then..." conditional blocks currently in `tech-lead/prompt.ts:detailSystemPrompt` are deleted — they live in the skills now and arrive only when their skill is attached.

### Coding agent (Claude Agent SDK, `remote-worker/`)

The coding agent runs in an isolated pod. Unlike the architect and tech-lead (Vercel AI SDK), it uses the **Claude Agent SDK** which has first-class skill primitives. We use them directly:

- **Built-in skills → `skills:` preload.** The SDK's `query({ options: { skills: [...] } })` option injects the full body of each named skill into the agent's context at startup. From the [SDK docs](https://code.claude.com/docs/en/agent-sdk/typescript): *"Skills are preloaded into context at startup when specified in the `skills` option. They're not just 'discoverable'—they're actively injected into the agent's context before the first turn."* This IS the "must be loaded" semantic, native.
- **Org skills (custom + imported) → plugin-discovered.** The SDK's standard plugin mechanism puts each skill's name + description into the agent's skill listing (auto-budgeted at ~1% of context window); the full body loads when the agent invokes the skill. From the [skills docs](https://code.claude.com/docs/en/skills): *"Description always in context, full skill loads when invoked."* This IS the "agent decides" semantic, native.

Both tiers materialise as a single per-task **AgentSkills plugin** under `.asdlc/skills-plugin/`. The pull endpoint response tags each entry with `kind` so `runner.ts` knows which materialised names to push into the `skills:` preload array. There's no homegrown loading machinery — the SDK does both tiers correctly out of the box.

The per-task skills are **pulled** from the BFF at pod init via HTTP (not pushed via Argo parameter) to keep the `WorkflowRun` CR small and avoid the 256 KB etcd object budget entirely.

**The flow:**

```
1. BFF dispatch (dispatchOne)
     creates a WorkflowRun with minimal Argo parameters:
       - asdlcTaskId, asdlcPlatformUrl, asdlcBearerFile path, etc.
     NO skill data inline. The Argo CR stays tiny.

2. Runner pod startup (oneshot.ts)
     Reads existing env: ASDLC_TASK_ID, ASDLC_PLATFORM_URL, ASDLC_BEARER_FILE.
     GET $ASDLC_PLATFORM_URL/api/v1/tasks/$ASDLC_TASK_ID/skills
        Authorization: Bearer $(cat $ASDLC_BEARER_FILE)
     Response 200: { "skills": SkillResolution[] }   # each tagged with kind

3. provisionWorkspace
     If skills is non-empty, write the AgentSkills tree under
     <workspace>/.asdlc/skills-plugin/ (all kinds, materialised as one plugin).
     If skills is empty (pre-PR-1 backfilled task, or design with no
     skills attached), skip the plugin tree entirely.

4. runner.ts (plugins array, conditional second entry; preload list)
     plugins = [{ type: "local", path: PLUGIN_PATH }]              # base asdlc
     preload = []
     if (directoryExists(layout.workspace + "/.asdlc/skills-plugin"))
         plugins.push({ type: "local", path: <skills-plugin path> })
         preload = pulled.filter(s => s.kind === 'builtin')
                          .map(s => `asdlc-task-skills:${s.materializedName}`)

     query({
         options: {
             plugins,
             skills: preload,    # built-in bodies injected at startup
             ...                 # custom/imported sit in the plugin and surface
                                 # via the SDK's auto skill listing
         }
     })

5. SDK loads both plugins; preloads built-in bodies into context;
    surfaces custom/imported as discoverable skills via the standard
    skill mechanism. Agent invokes them as needed.
```

**Why `skills: <built-in names>` and not `skills: 'all'`.** The SDK accepts `'all'` as a shorthand to preload every discovered skill. We deliberately use the array form — the two-tier split is the whole point. Custom and imported skills must remain discoverable-not-forced so the model treats them as org-authored guidance rather than mandatory platform contract. A future reader should not "simplify" the preload to `'all'`.

**BFF endpoint** (added in PR 3):

```
GET /api/v1/tasks/:taskId/skills
   Auth: bearer (the existing per-task JWT in ASDLC_BEARER_FILE)
   Response 200:
     { "skills": [
         { "id": "builtin/api-management",
           "materializedName": "builtin-api-management",
           "kind": "builtin",
           "skillMd": "...",
           "references": {} },
         { "id": "custom/payments-pci-handling",
           "materializedName": "custom-payments-pci-handling",
           "kind": "custom",
           "skillMd": "...",
           "references": {} },
         ...
       ] }
   404: task unknown
   401/403: bearer invalid
```

Server side: read `design_version_skill_snapshots` for the task's `(ProjectID, SourceDesignVersion)`. No live lookup against `skills` — snapshot semantics are unchanged: the runner sees exactly what was frozen at design-version finalize.

**The base `asdlc` skill** stays as a single `SKILL.md` at `remote-worker/plugin/skills/asdlc/SKILL.md`, baked into the runner image. PR 1 slims it (removes stack-specific sections that moved to the four built-ins); what remains is universal workflow + build-verification + deny-list + PR mechanics. The base plugin is **also preloaded** (`skills: ["asdlc:asdlc", ...builtinNames]`) so its body is always in context — the agent needs the workflow instructions immediately, not on-demand. The body adds a brief "Active project skills" note pointing at the per-task plugin location.

**The per-task plugin tree** (written by `provisionWorkspace` from the pull response):

```
.asdlc/skills-plugin/
  .claude-plugin/
    plugin.json                                 # {"name":"asdlc-task-skills","version":"1.0"}
  skills/
    builtin-api-management/                     # materialised name = kind + "-" + skill.name
      SKILL.md                                  # rewritten name: builtin-api-management
    builtin-go/
      SKILL.md
    custom-payments-pci-handling/
      SKILL.md
    imported-acme-style-guide/
      SKILL.md
    …
```

All kinds land in one plugin directory. The materialisation prefix (`builtin-`, `custom-`, `imported-`) is applied to both the directory name AND the `name:` frontmatter field; the original name is preserved under `metadata.asdlc.canonical-name`. This avoids collisions in the SDK's two-plugin merge (per PR 0b's spike result), and gives the runner stable identifiers to feed into the `skills:` preload array.

**Failure modes:**

- BFF unreachable at init → runner errors out. Same failure surface as the runner's existing init-time BFF calls (bearer refresh, `verification-failed` POST, etc.).
- Task ID 404 → runner errors out (task was deleted/abandoned mid-flight).
- Snapshot rows missing (pre-PR-1 backfilled task) → endpoint returns `{ "skills": [] }` → runner loads base plugin only, preload list is `["asdlc:asdlc"]` only — same effective behaviour as today.
- A built-in is in the pull response but absent from the materialised plugin tree (shouldn't happen — same writer materialises and lists) → SDK would skip-with-warning per the docs; we treat it as a runner bug and fail the task.

**Defence in depth.** The SDK preload already guarantees the built-in body is in context; the issue-body bullets give the agent a second cue. For every attached skill in the snapshot, the issue body's Component Reference card carries a one-line `skill attached: <name>` bullet. For platform-facts content specifically (e.g. "gateway injects X-User-Id"), the relevant skill's Platform-facts section is echoed inline as Scope bullets via `services/issue_body.go`. The agent sees the fact twice — once in the preloaded skill body, once in the issue body — so a missed-skill failure mode would require both signals to fail simultaneously.

### Cross-agent propagation summary

```
┌─────────────────────┐  skillsApplied[]    ┌─────────────────────┐
│  Architect          │ ──────────────────► │ specs/design/       │
│ - builtin bodies    │  PROJECT-LEVEL      │ design.md root      │
│   INLINED in prompt │  persisted into     │ frontmatter         │
│ - org skills via    │  root frontmatter   │                     │
│   manifest + read_  │                     │                     │
│   skill tool        │                     │                     │
│ - attach/detach     │                     │                     │
└─────────────────────┘                     └─────────────────────┘
                                                     │
                                                     │ resolved at task generation
                                                     ▼
┌─────────────────────┐  full bodies        ┌─────────────────────┐
│  Tech-Lead          │  per attached       │ TechLeadDetailItem  │
│ - detail phase      │  skill (built-in    │ .skillsResolved[]   │
│   sees the          │  AND org)           │ (concat'd into the  │
│   full SKILL.md     │ ◄────────────────── │ user prompt with    │
│   body of every     │                     │ "MUST consult"      │
│   attached skill    │                     │ framing)            │
└─────────────────────┘                     └─────────────────────┘
                                                     │
                                                     │ at issue creation: SNAPSHOT
                                                     │ (per design_version,
                                                     │  not per task)
                                                     ▼
                                            ┌─────────────────────┐
                                            │ design_version_     │
                                            │ skill_snapshots     │
                                            │ (project_id,        │
                                            │  design_version,    │
                                            │  skill_id) →        │
                                            │  skill_md + refs    │
                                            │  + kind             │
                                            └─────────────────────┘
                                                     │
                                                     │ at pod init: PULL
                                                     │ GET /api/v1/tasks/:taskId/skills
                                                     ▼
┌─────────────────────┐                     ┌─────────────────────┐
│  Coding Agent       │                     │ .asdlc/skills-      │
│ - SDK loads two     │ ◄────────────────── │ plugin/skills/      │
│   plugins (base     │  workspace-local    │  builtin-…/         │
│   asdlc + per-task) │  per-task plugin    │  custom-…/          │
│ - SDK PRELOADS      │  written by         │  imported-…/        │
│   built-in bodies   │  provisionWorkspace │  (AgentSkills       │
│   via skills:[]     │                     │  directories)       │
│ - SDK discovers     │                     │                     │
│   org skills via    │                     │                     │
│   plugin listing    │                     │                     │
└─────────────────────┘                     └─────────────────────┘
```

`skillsApplied` is persisted at the **project** level (one set for the whole project). At issue creation the **resolved bodies** are snapshotted into `design_version_skill_snapshots` so the dispatched agent's workspace contains the same bodies the tech-lead used when authoring the issue. Console edits between issue creation and dispatch affect future tasks; in-flight tasks use the snapshot — see [Snapshot rule](#paths-that-read-the-snapshot-not-the-live-set).

**Two-tier symmetry across agents.** Built-ins are force-loaded for every agent (architect via prompt inlining, tech-lead via prompt inlining, coding agent via SDK `skills:` preload); org skills are discoverable for every agent (architect via `read_skill`, tech-lead via prompt inlining since the architect already chose them, coding agent via SDK plugin listing). The mechanism differs because the runtimes differ — Vercel AI SDK has no skill primitive so we DIY for the design-time agents, Claude Agent SDK has both primitives natively for the runtime agent — but the user-visible semantic is uniform: platform skills must be applied, org skills are recommendations.

**The cascade hook does not interact with skills.** `dispatch_cascade_hook.go:OnTaskDeployed` re-runs `trait_sync.SyncProjectAPITraits` and `runtime_config.EmitForProjectSPAs`, then calls `DispatchTasks` to re-evaluate on-hold tasks. None of those paths read skill content — trait sync sources from `models.DesignComponent.ExposesAPI`, runtime-config sources from a hardcoded key set in `runtime_config_service.go`, and dispatching an on-hold task means it already has a snapshot from its earlier `ensureIssueForTask` write (or, for tasks pre-dating PR 1, falls through the backfill rule below).

---

## Refactoring plan

The cleanup lands in 3 PRs after 2 prerequisite PRs (PR 0a/0b in [Implementation](#implementation)). Each PR's merge gate is its own test suite — no PR needs a later one to be verifiable. After PR 1, user-facing behaviour stays identical to today (golden suite proves it); after PR 2, orgs can author custom + import but skills aren't yet attachable from the architect; after PR 3, the full attach + dispatch + materialise flow works end-to-end.

High-level shape:

### PR 0 — Prerequisites that don't exist today

This PR builds two things the rest of the plan assumes already exist. Neither is skill-system-shaped; they're work the platform owes itself before skills can be merged cleanly.

1. **Architect-output golden fixtures.** Today the only architect E2E spec is `tests/e2e/architect-streaming.spec.ts` — it asserts streaming UI behaviour against a live LLM, not output equality. PR 0a builds:
   - Five canonical designs (hello-api, todo + SPA, OIDC employee app, dependent-API consumer, multi-service) as test fixtures under `tests/fixtures/architect/`.
   - A new `tests/e2e/architect-golden.spec.ts` that runs the architect against each, captures the materialized `DesignFile`, and diffs against a checked-in golden.
   - A `tools/architect-shadow.ts` harness that runs the *old* vs *new* prompt path side-by-side over recent dev-cluster traffic and produces a structural-diff report.
   Without these, "the four built-ins produce equivalent output to today's monolithic prompt on five designs" is fictional.

2. **SDK plugin-discovery spike.** A 50-line test in `remote-worker/src/lib/runner.test.ts` that loads two `{type:"local"}` plugins with a name-colliding skill and asserts which wins (we expect later-plugin-wins; if not, the design needs to namespace per-task skill names). Cite the SDK behaviour in this doc's "Coding agent" section when the spike lands.

### PR 1 — Built-in skill files + `skills` table + bootstrap + tech-lead inlining

- Bundle the four built-in `SKILL.md` files under `asdlc-service/skills/builtin/<name>/`; embed into the BFF binary via Go `embed.FS`.
- Add `skills` + `skill_audit_events` + `design_version_skill_snapshots` migrations.
- Build `SkillBootstrap.Run()` — startup UPSERT of built-ins + purge-removed.
- Build `SkillService.Resolve` + `SkillService.List`.
- Strip stack-specific sections from `agents/src/agents/architect/prompt.ts` and `agents/src/agents/tech-lead/prompt.ts`; thread the four built-ins' descriptions into the architect input and the full bodies into the tech-lead detail-phase input. Use the [orphaned-content mapping table](#where-each-piece-of-the-current-monolithic-prompts-lands-pr-1-checklist) as the line-by-line checklist.
- Slim `remote-worker/plugin/skills/asdlc/SKILL.md` (remove stack-specific sections).
- Interim behaviour: no architect tool yet (lands in PR 3). On finalize, `seedDefaultSkillsApplied` stamps all four built-ins so behaviour stays equivalent to today.

**Honest framing — not a no-behaviour-change PR.** Today's architect `systemPrompt` is a single static string. After PR 1, the four built-ins' bodies are inlined into the user prompt (under "Platform skills — MUST consult") — `seedDefaultSkillsApplied` ensures every new design ships with all four attached, so the user prompt the architect sees is roughly *systemPrompt - stack-specific-sections + four-built-in-bodies*. We expect net-equivalent content but can't prove it in advance — hence the gate below.

**Gating PR 1's merge** (depends on PR 0a landing first):
- Architect golden-output E2E suite (built in PR 0a) passes (post-`normalize.ts`, `temperature: 0`) against the five canonical designs.
- Architect validator's existing rules continue to fire on the same malformed inputs.
- Shadow-mode run (`tools/architect-shadow.ts`) produces no material structural regressions.
- `skills-ref validate` passes against every bundled built-in directory.

**Independently testable:** bootstrap idempotency (unit + integration test) + resolver behaviour (unit) + the four golden fixtures (E2E) — none require PR 2 or PR 3.

### PR 2 — Custom + imported skills + REST API + console

- Wire POST/PUT/DELETE/import REST endpoints on top of the `skills` table (no new tables — the table already exists from PR 1).
- Custom-skill creation + edit + delete; tarball import with license/compatibility preview; audit-log write on every mutation.
- Console: list view (built-ins view-only, custom + imported editable) + custom-skill editor + tarball import dialog + read-only built-in viewer.
- Built-in PUT/DELETE return 403 `SKILL_NOT_EDITABLE`.
- No agent changes.

**Independently testable:** unit tests for each endpoint's validation; E2E tests for create-custom → list → delete and tarball-import → list → delete; built-in PUT returns 403. Skills authored in PR 2 are visible to PR 1's architect input (already wired) but no UI to attach them yet (lands in PR 3).

### PR 3 — Architect tools + runner pull endpoint + per-task plugin materialisation + SDK preload

- Architect tools: `read_skill(name)`, `attach_skill`, `detach_skill`. (No `select_skill_reference` in v1 — see [Skill model > references paragraph](#directory-layout).)
- `skillsApplied[]` becomes a writeable field on the design root frontmatter; persist on every architect finalize. Delete `seedDefaultSkillsApplied` (architect attaches explicitly from PR 3 onward).
- Architect prompt construction (in `agents/src/agents/architect/prompt.ts`) consumes the two-field input the schema now exposes: `builtinSkills` bodies inline under "Platform skills — MUST consult"; `orgSkills` (name + description) render under "Org skills — load if relevant" as a manifest. The single `availableSkills` field from PR 1 is replaced by this pair.
- BFF endpoint: `GET /api/v1/tasks/:taskId/skills` (reads `design_version_skill_snapshots` for the task's `(ProjectID, SourceDesignVersion)`; each row's `kind` flows out in the response).
- Runner: `oneshot.ts` calls the pull endpoint at init; `provisionWorkspace` writes the `.asdlc/skills-plugin/` tree from the response (all kinds materialised into one plugin); `runner.ts` conditionally adds the second plugin AND builds the `skills:` preload array from the `kind === 'builtin'` entries (plus `"asdlc:asdlc"` for the base plugin).
- Issue body grows skill-fact bullets (defence in depth) sourced from snapshot rows; `issue_body.go` + `editIssueBodyWithRetries` read snapshots instead of re-resolving.

**Independently testable:** unit tests for each architect tool; integration test for the pull endpoint (with and without snapshot rows; mixed-kind response); E2E: design with attached built-ins + a custom skill → dispatch → runner pulls → `.asdlc/skills-plugin/skills/<materializedName>/SKILL.md` exists for each → SDK preloads built-in bodies → SDK lists custom skill as discoverable → agent invokes custom skill on-demand.

Each PR is independently mergeable. PR 0a is a hard prerequisite for PR 1; PR 0b is a prerequisite for PR 3.

---

## UI sketch

### Settings → Skills (list view)

The list shows the four built-ins, then any custom + imported skills the org has authored.

```
┌──────────────────────────────────────────────────────────────────────┐
│  Settings ▸ Skills                                                   │
│                                                                       │
│  Skills shape what the platform's agents emit — code patterns,       │
│  conventions, and project layout. They do NOT change platform        │
│  infrastructure: auth, CORS, runtime config, and the build pipeline  │
│  are wired in code. The four built-ins below ship with the platform  │
│  and are read-only in v1; you can author additional Custom skills    │
│  from scratch or Import AgentSkills directories from the ecosystem.  │
│                                                                       │
│  ┌────────────────────────────────────────────────────────────────┐ │
│  │  🔎 Filter…                            [+ New Custom] [Import] │ │
│  └────────────────────────────────────────────────────────────────┘ │
│                                                                       │
│  ── Built-in — 4 (read-only) ───────────────────────────────────── │
│  Shipped with the platform. Click any row to view the body.          │
│                                                                       │
│  ┌────────────────────────────────────────────────────────────────┐ │
│  │ 🛡 api-management                       v1     🔒 read-only    │ │
│  │   Gateway JWT validation, X-User-Id injection, CORS filter,    │ │
│  │   OpenAPI conventions, dependent-API URL injection.            │ │
│  ├────────────────────────────────────────────────────────────────┤ │
│  │ 🔑 thunder-authentication               v1     🔒 read-only    │ │
│  │   Per-project Thunder OAuth client + THUNDER_* keys in         │ │
│  │   window._env_; oidc-client-ts wiring for SPAs.                │ │
│  │   Pairs with: react-webapp (when any SPA uses end-user auth).  │ │
│  ├────────────────────────────────────────────────────────────────┤ │
│  │ ⚛  react-webapp                         v1     🔒 read-only    │ │
│  │   Vite + nginx layout, env-config.js synchronous load,         │ │
│  │   window._env_ key set, multi-stage Dockerfile.                │ │
│  ├────────────────────────────────────────────────────────────────┤ │
│  │ 🐹 go                                   v1     🔒 read-only    │ │
│  │   Pinned golang:1.25-alpine, pure-Go SQLite, suggested layout, │ │
│  │   port 9090, multi-stage Dockerfile.                           │ │
│  └────────────────────────────────────────────────────────────────┘ │
│                                                                       │
│  ── Custom (org-authored) — 2 ─────────────────────────────────────  │
│                                                                       │
│  ┌────────────────────────────────────────────────────────────────┐ │
│  │ 💳 payments-pci-handling                v1                     │ │
│  │   PCI-DSS logging requirements for card-processing services.   │ │
│  ├────────────────────────────────────────────────────────────────┤ │
│  │ 📊 internal-analytics                   v1                     │ │
│  │   Event taxonomy + audit endpoint for in-house dashboards.     │ │
│  └────────────────────────────────────────────────────────────────┘ │
│                                                                       │
│  ── Imported — 1 ──────────────────────────────────────────────────  │
│                                                                       │
│  ┌────────────────────────────────────────────────────────────────┐ │
│  │ 📥 acme-style-guide                     v1                     │ │
│  │   Imported from agentskills.io. Org-wide naming + license      │ │
│  │   header conventions.                                          │ │
│  └────────────────────────────────────────────────────────────────┘ │
└──────────────────────────────────────────────────────────────────────┘

Legend:  🔒 read-only       = ships with the platform; v1 has no edit path
```

Row actions:

- **Built-in row** — `[view]` only. Clicking opens a read-only viewer with the body. A short hint at the top explains why it's not editable and links to the "Built-ins are read-only in v1" section above.
- **Custom row** — `[view] [edit] [delete]`.
- **Imported row** — `[view] [delete] [re-import]`.

`[+ New Custom]` opens the custom-skill editor pre-filled with an empty SKILL.md template (creates `kind: custom`). `[Import]` opens the tarball upload dialog (`SkillImportDialog.tsx`) for AgentSkills directories from the ecosystem (creates `kind: imported`). There's no UI path to edit a built-in in v1.

### Skill editor (custom skills only)

```
┌──────────────────────────────────────────────────────────────────────┐
│  ← Back   New custom skill                                            │
│                                                                       │
│  ⓘ  Custom skills are authored from scratch and stored against your  │
│      org. They appear in the architect's catalogue alongside the     │
│      four built-ins, with the same attach mechanism.                 │
│                                                                       │
│  ┌─ Frontmatter ────────────────────────────────────────────────┐   │
│  │  name        [payments-pci-handling           ]               │   │
│  │  version     [1]                                              │   │
│  │  description                                                  │   │
│  │  ┌────────────────────────────────────────────────────────┐  │   │
│  │  │ PCI-DSS logging requirements for components that       │  │   │
│  │  │ touch card data: every PAN access must log to /audit. │  │   │
│  │  └────────────────────────────────────────────────────────┘  │   │
│  └────────────────────────────────────────────────────────────────┘ │
│                                                                       │
│  Body                                       [Preview]                │
│  ┌────────────────────────────────────────────────────────────────┐ │
│  │ # Payments PCI Handling                                        │ │
│  │                                                                  │ │
│  │ ## What this skill does                                         │ │
│  │ Logs the access pattern for any handler that reads, mutates,    │ │
│  │ or echoes a card primary account number (PAN).                  │ │
│  │                                                                  │ │
│  │ ## Recommended practice                                         │ │
│  │ (Architect)                                                     │ │
│  │ - Mark every handler that touches PAN in design.md              │ │
│  │   componentAgentInstructions.                                   │ │
│  │                                                                  │ │
│  │ (Tech-lead — issue body bullets)                                │ │
│  │ - Acceptance criteria: ...                                      │ │
│  │ ⌜cursor⌟                                                         │ │
│  └────────────────────────────────────────────────────────────────┘ │
│                                                                       │
│  [Cancel]                                  [Save]    [Dry-run ▾]     │
└──────────────────────────────────────────────────────────────────────┘
```

`[Dry-run ▾]` feeds a canned spec + design through the architect/tech-lead and shows the resulting issue body with this skill attached. Cheaper than waiting for a real dispatch.

### Skill viewer (built-ins — read-only)

```
┌──────────────────────────────────────────────────────────────────────┐
│  ← Back   View: api-management            built-in v1                │
│                                                                       │
│  ⓘ  This skill ships with the platform and is read-only in v1.       │
│      To layer org-specific rules on top, create a Custom skill and   │
│      attach it alongside this one on your project. The agent          │
│      reconciles both bodies during code generation.                   │
│                                                                       │
│  Frontmatter                                                          │
│  • name: api-management                                               │
│  • version: 1                                                         │
│  • description: How the platform's API gateway validates JWTs, …     │
│                                                                       │
│  Body                                                                 │
│  ┌────────────────────────────────────────────────────────────────┐ │
│  │ # API Management                                                │ │
│  │ (full SKILL.md body, syntax-highlighted)                         │ │
│  └────────────────────────────────────────────────────────────────┘ │
│                                                                       │
│                                                            [Close]   │
└──────────────────────────────────────────────────────────────────────┘
```

### Design view — project skill chips

Inside the design editor, the project-level header (above all components) carries a chip row showing attached skills:

```
┌──────────────────────────────────────────────────────────────────────┐
│  Design: ACME Employee Portal                                        │
│                                                                       │
│  Project skills (active for every component):           [+ attach]   │
│  [🛡 api-management ×] [🔑 thunder-authentication ×]                 │
│  [⚛  react-webapp ×]  [🐹 go ×]  [💳 payments-pci-handling ×]       │
│                                                                       │
│  ─────────────────────────────────────────────────────────────────   │
│                                                                       │
│  Components:                                                          │
│  [employee-api]  [employee-portal]                                    │
└──────────────────────────────────────────────────────────────────────┘
```

- The `×` removes a skill from the project (`detach_skill`).
- `[+ attach]` opens a picker filtered to skills not currently attached.
- Skills attach at the **project** level, not per-component. A project that has both Go services and React SPAs will attach both `go` and `react-webapp`; the tech-lead and coding agent see both bodies for every task; each task's agent picks the relevant one based on the component's language and type.

---

## Implications + cost

- **Coding-agent pod cold start.** Materialising 4–6 small markdown files in the workspace adds ~5ms over the existing provisioning; negligible.
- **Multi-tenant isolation.** Custom and imported skills land in PostgreSQL and resolve per-request; an org never reads another org's skills. The runner image ships built-ins only — custom/imported bodies arrive on the dispatch payload, scoped to the org of the task.
- **Skill drift risk.** Once a built-in's content moves into a skill file, the architect/tech-lead/coding-agent prompts must NOT separately encode the same rule. Reviewers should treat any new domain-knowledge sentence added to a prompt as a bug request to "make this a skill instead." A lint rule on the prompts file (look for known patterns: "Do NOT add CORS", "modernc.org/sqlite", "window._env_") helps catch regressions during PR review.
- **Testing.** Each built-in skill gets one golden test (`asdlc-service/skills/builtin/<name>.golden.md`) verifying the parsed frontmatter + body structure. End-to-end tests should be adapted to attach a known skill set and verify the architect's emitted design + tech-lead's emitted issue body both reflect it.
- **Backwards compatibility.** Designs on disk that predate the skills system load with empty `skillsApplied`; PR 1's interim behaviour seeds all four built-ins by default. PR 3 introduces explicit attachment and migrates existing designs by attaching the four built-ins on first re-save (the architect prompt's checklist tells it to verify the right set is attached during the architecture phase).

---

## Implementation

This section is the agreed-upon implementation plan after two rounds of architectural review. File paths and function names are concrete; sequence is binding.

### PR 0a — Architect-output golden fixtures (prerequisite)

**Goal:** Establish a reproducible baseline for "did skills-system extraction change what the architect emits?"

- New: `tests/fixtures/architect/` — five canonical specs:
  - `hello-api/spec.md` + `golden-design.json` (single Go service, public).
  - `todo-app/spec.md` + golden (Go service + React SPA, OIDC end-user).
  - `employee-app/spec.md` + golden (consumer of catalog-known external API).
  - `dependent-api-consumer/spec.md` + golden (web-app with multiple sibling deps).
  - `multi-service/spec.md` + golden (3 services, mixed auth).
- New: `tests/e2e/architect-golden.spec.ts` — feeds each spec through `POST /api/v1/orgs/.../designs/stream`, captures the materialised `DesignFile`, normalises it (see below), diffs against the golden. Gated on `ARCHITECT_GOLDEN=1` like the existing streaming spec.
- New: `tests/fixtures/architect/normalize.ts` — the **canonical normaliser** used by both the golden harness and the shadow tool. Steps:
  - Sort `components[]` by `name` (current implementation is order-insensitive anyway, but make it explicit).
  - For each component: sort `dependsOn`, `dependentApis`, OpenAPI `paths` keys, and component frontmatter keys.
  - Strip trailing whitespace, normalise line endings to `\n`, collapse runs of blank lines.
  - Pretty-print OpenAPI JSON with 2-space indent (matches BFF's persisted shape).
- New: `tools/architect-shadow.ts` — CLI that runs the pre-skills and post-skills prompt paths side-by-side over a corpus of dev-cluster designs; applies the same `normalize.ts`; emits a structural-diff report (added/removed components, OpenAPI shape changes, `skillsApplied` deltas).
- **Determinism.** The golden harness runs the architect with `temperature: 0` (via a new `ARCHITECT_GOLDEN_TEMPERATURE=0` env path in `agents-service`) so the LLM's output is reproducible across runs. We don't claim byte-equality at non-zero temperature — that's impossible — but at `temperature: 0` the same model + prompt + input produces identical output, and the normaliser absorbs the remaining surface-level variance.
- No production code-path changes in `agents/` or `asdlc-service/` — this is pure test scaffolding. The `ARCHITECT_GOLDEN_TEMPERATURE` env path is the only `agents-service` change and lands behind a flag.

**Merge gate:** every golden passes (post-normalise, `temperature: 0`) against the current architect (committing the baseline before any refactor).

### PR 0b — SDK plugin-discovery spike

**Goal:** Confirm the Claude Agent SDK accepts multiple `{type:"local"}` plugins and document the collision behaviour.

- New: `remote-worker/src/lib/runner.test.ts` (or extend if exists) — instantiates a `Query` with two plugin paths, each containing one skill with the same `name:` frontmatter. Assert which one wins (expected: later-plugin-wins). Run the test against the actual SDK version pinned in `package.json`.
- Update the "Coding agent" section of this doc with the confirmed behaviour. If the SDK does NOT merge sensibly, switch to namespacing per-task skill names (`<id>-task` suffix) — document the decision either way.

**Merge gate:** test passes; doc section updated.

### PR 1 — Built-in skill files + `skills` table + bootstrap + tech-lead inlining

Files added:

```
asdlc-service/skills/builtin/                      # bundled markdown — embedded into the BFF binary
  api-management/SKILL.md
  thunder-authentication/SKILL.md
  react-webapp/SKILL.md
  go/SKILL.md

asdlc-service/services/skill_bootstrap.go          # startup UPSERT + purge-removed
asdlc-service/services/skill_service.go            # Resolve + List (used by every reader)
asdlc-service/services/skill_service_test.go
asdlc-service/services/skill_bootstrap_test.go     # idempotency + concurrent-replica safety

asdlc-service/db/migrations/000XX_skills.sql                              # single table for builtin + custom + imported
asdlc-service/db/migrations/000XX_skill_audit_events.sql
asdlc-service/db/migrations/000XX_design_version_skill_snapshots.sql      # keyed (project_id, design_version, skill_id)
```

`remote-worker/plugin/skills/asdlc/` keeps its single SKILL.md but is **trimmed** — the stack-specific sections (Go SQLite, React env-config, OIDC SPA wiring, protected-handler Go pattern) move into the four built-ins. What remains: workflow, build verification, deny-list, PR mechanics — the universal parts.

Files modified:

- `asdlc-service/cmd/asdlc-api/main.go` — call `SkillBootstrap.Run()` after migrations, before serving traffic.
- `agents/src/agents/architect/prompt.ts` — strip stack-specific sections; `systemPrompt` keeps workflow + OC grammar + finalize semantics; `buildUserPrompt` grows two skill sections sourced from new input fields. `builtinSkills` (full bodies) inline under "Platform skills — MUST consult"; `orgSkills` (name + description) render as a manifest under "Org skills — load if relevant". In PR 1 `orgSkills` is empty (no API yet) and there's no `read_skill` tool yet; the manifest section renders as "(none)". The architect can SEE the built-in bodies and act on them; the tool surface for org-skill loading + attachment lands in PR 3.
- `agents/src/agents/tech-lead/prompt.ts` — strip surgical conditional sections (OIDC, Go base image, dependent-API); `buildDetailUserPrompt` grows a "## Skills active for this project" section that inlines the full body of every attached skill (no tier split — by the detail phase, the architect has already chosen which skills apply).
- `agents/src/agents/architect/schema.ts` — `ArchitectInput` gains `builtinSkills: z.array(SkillRecord)` (full bodies) and `orgSkills: z.array(SkillDescription)` (name + description). `DesignFile` gains root-level `skillsApplied: z.array(z.string()).optional()`. **Delete `exposesAPI.userContext`** (see note below).
- `agents/src/agents/tech-lead/schema.ts` — `TechLeadDetailItem` gains `skillsResolved: z.array(SkillRecord)` (full bodies); `TechLeadPlanInput` gains `attachedSkills: Array<{ name, description }>`.
- `asdlc-service/clients/agents/client.go` — request/response types mirror the schema additions.
- `asdlc-service/services/design_service.go:StreamArchitect` — call `SkillService.List(orgId)`, split by `kind`, pass full bodies on `builtinSkills` and descriptions on `orgSkills`. In PR 1 the org tier is always empty (no custom/imported until PR 2).
- `asdlc-service/services/task_stream.go:StreamTechLeadDetail` — read the design's root `skillsApplied`; `SkillService.Resolve` each; ship as `skillsResolved` on every TechLeadDetailItem.
- `asdlc-service/services/task_service.go:ensureIssueForTask` (called from `task_stream.go:persistAndIssue`) — UPSERT into `design_version_skill_snapshots` for `(project_id, task.SourceDesignVersion)` immediately before `gitClient.CreateIssue`. Lookup-or-create: no-op if rows already exist for that key. Only snapshot write site.
- `asdlc-service/services/artifact_store.go` — `SplitDesign` parses root-frontmatter `skillsApplied: []string`; `AssembleDesign` writes it sorted. Also strip any legacy `exposesAPI.userContext` field at read time (logged as `userContext-legacy-value-discarded`).
- **Dead-field deletion: `exposesAPI.userContext`.** The field was round-tripped to disk but never consumed (`services/trait_sync.go` reads `claimMappings` from the ClusterTrait manifest, not the design). PR 1 removes it from the architect schema; `api-management` skill body declares `X-User-Id` as the canonical header. The removal is unblocked by the skill — without somewhere else to document the header name, deleting the field would have left the architect blind.

The runner still loads only the base plugin in PR 1 — the per-task pull endpoint and materialisation land in PR 3.

**Where each piece of the current monolithic prompts lands (PR 1 checklist).** This is the implementer's traceability map. Every numbered section below corresponds to a block of prose in today's prompt files; the right column names the destination so nothing gets lost.

| # | Current location | Destination |
|---|---|---|
| 1 | `architect/prompt.ts:18-50` (overview, three phases, finalize semantics) | **Base prompt** — stays in `systemPrompt`. Universal workflow. |
| 2 | `architect/prompt.ts:53-87` (`dependentApis` external-API rules) | **`api-management` skill body** — under "Recommended practice (Architect)". The skill's scope explicitly covers both protected services AND consumers of sibling/external APIs (the `<NAME>_URL` env injection works for both — sibling via the BFF's runtime config, external via dispatch-time env on the ReleaseBinding). |
| 3 | `architect/prompt.ts:89-119` (`exposesAPI.auth` keyword classifier — "if you see 'login required' → end-user-required") | **Base prompt** — stays. This is the architect's schema-shaping heuristic for translating spec text into a schema enum value. It's NOT a skill (the rule applies to every architect call regardless of which skills are attached). |
| 4 | `architect/prompt.ts:120-138` (`Caller identity` HARD REQUIREMENT — every web-app calling a protected service MUST set `callerIdentity.mode: end-user`) | **Base prompt** — stays. Schema-enforcement rule the validator depends on; not a recommendation a skill can override. |
| 5 | `architect/prompt.ts:140-187` (Thunder-specific design rules) | **`thunder-authentication` skill body** — under "Recommended practice (Architect)". |
| 6 | `tech-lead/prompt.ts:294-343` (detail-phase OIDC block) | **`thunder-authentication` skill body** — under "Recommended practice (Tech-lead — issue body bullets)". |
| 7 | `tech-lead/prompt.ts:372-383` (Go base image hard requirement) | **`go` skill body** — under "Platform facts" (it's a build-pod-driven constraint, not just a recommendation). |
| 8 | `tech-lead/prompt.ts:384-409` (external dependent-API surfacing in issue body) | **`api-management` skill body** — under "Recommended practice (Tech-lead — issue body bullets)". |
| 9 | `remote-worker/plugin/skills/asdlc/SKILL.md:53-100` + `:380-401` (workflow, branch+PR, `Closes #N`) | **Base asdlc plugin** — stays, slimmed only by removing stack-specific bits. |
| 10 | `asdlc/SKILL.md:102-216` (`<NAME>_URL` SPA upstream wiring) | **`react-webapp` skill body** — under "Recommended practice (Coding agent)". The Go-side consumer pattern of the same env var lives in `api-management`'s "Recommended practice (Coding agent)" section so Go services get it from the right place. |
| 11 | `asdlc/SKILL.md:218-309` (build verification: `go mod tidy`, `npm install`, dep cache) | **Split:** stack-specific commands (`go mod tidy` etc.) live in `go`; SPA-specific commands (`npm install`, `npm run build`) live in `react-webapp`. The escape valve ("if verification keeps failing, draft PR with `[build-failed]`") stays in the base asdlc plugin since it's universal across stacks. |
| 12 | `asdlc/SKILL.md:230-264` (Go runtime: `modernc.org/sqlite`, `golang:1.25-alpine`) | **`go` skill body** — under "Platform facts" (CGO-times-out reason) and "Recommended practice (Coding agent)". |
| 13 | `asdlc/SKILL.md:404-457` (workload.yaml grammar, visibility, no `dependencies.endpoints`) | **Base asdlc plugin** — stays. Universal OC contract. |
| 14 | `asdlc/SKILL.md:461-562` (Vite + nginx + Dockerfile for SPAs) | **`react-webapp` skill body** — under "Recommended practice (Coding agent)". |
| 15 | `asdlc/SKILL.md:566-633` (Thunder OIDC keys + `oidc-client-ts` wiring) | **`thunder-authentication` skill body** — under "Recommended practice (Coding agent)". |
| 16 | `asdlc/SKILL.md:636-701` (protected handler Go pattern: `mustUserID`, `AND user_id = ?`, problem+json) | **`api-management` skill body** — under "Recommended practice (Coding agent)" (already shown in the worked example earlier in this doc). |
| 17 | `asdlc/SKILL.md:705-715` (Common pitfalls table) | **Split per row:** the workload-grammar pitfalls (no `dependencies.endpoints`, visibility levels) stay in the **base asdlc plugin**; the SPA-runtime pitfalls (`env-config.js` order, no envsubst) move to **`react-webapp` skill body**; the Go pitfalls (CGO sqlite) move to **`go` skill body**. |

Items NOT in this table are either platform code (out of scope for skills entirely — see "What a skill CANNOT change") or stay in the base prompts unchanged.

**Default attachment for pre-PR-3 designs.** PR 1's `seedDefaultSkillsApplied(design)` helper in `services/design_service.go` runs at every `StreamArchitect` finalize and stamps `skillsApplied: [api-management, thunder-authentication, react-webapp, go]` if the field is missing or empty. PR 3 deletes this helper entirely.

**Merge gate:** PR 0a goldens pass; PR 0b SDK behaviour documented; `skills-ref validate` passes against every built-in directory; integration test confirms bootstrap idempotency under concurrent BFF replicas; E2E test confirms a snapshot is persisted on issue creation and contains all four built-ins by default.

### PR 2 — Custom + imported skills + REST API + console

PR 2 wires the org-editable surface (custom + imported) on top of the `skills` table that PR 1 already created. No new tables. Built-ins remain read-only — built-in PUT/DELETE return 403 `SKILL_NOT_EDITABLE`. See [Built-ins are read-only in v1](#built-ins-are-read-only-in-v1).

Files added:

```
asdlc-service/services/skill_mutation_service.go         # POST/PUT/DELETE for custom; DELETE for imported; built-in 403s
asdlc-service/services/skill_import_service.go           # tarball decode + AgentSkills validation
asdlc-service/api/skill_routes.go                        # REST routes registered in main.go
console/src/pages/settings/Skills.tsx                    # list view with built-in (view-only) / custom / imported groupings
console/src/pages/settings/SkillEditor.tsx               # custom-skill editor (single SKILL.md + optional references)
console/src/pages/settings/SkillViewer.tsx               # read-only viewer (built-ins; view-mode for custom/imported)
console/src/pages/settings/SkillImportDialog.tsx         # tarball upload + compatibility/license preview
console/src/api/skills.ts
```

Files modified:

- `asdlc-service/cmd/asdlc-api/main.go` — register skill routes.
- `asdlc-service/services/skill_service.go` (from PR 1) — `Resolve` and `List` already read the `skills` table generically; no change needed beyond ensuring `kind` is exposed in the response payload (it was already there).

**Merge gate:** POST/PUT/DELETE validation rejects every category in the Validation rules section with structured errors; built-in PUT/DELETE returns 403 `SKILL_NOT_EDITABLE`; tarball import end-to-end test (upload AgentSkills directory from `agentskills.io/examples/`, validate, list shows it as `kind: imported`); console smoke test (list, view, create custom, edit, delete).

### PR 3 — Architect tools + runner pull endpoint + per-task plugin materialisation

Files added:

```
asdlc-service/api/task_skills_route.go             # GET /api/v1/tasks/:taskId/skills
asdlc-service/services/task_skills_service.go      # reads design_version_skill_snapshots → SkillResolution[]
remote-worker/src/lib/skills_pull.ts               # HTTP GET against BFF; returns SkillResolution[]
remote-worker/src/lib/skills_materializer.ts       # writes the .asdlc/skills-plugin/ tree
```

Files modified:

- `agents/src/agents/architect/tools.ts` — adds `read_skill`, `attach_skill`, `detach_skill`. `read_skill` returns the resolved SKILL.md body for any catalogue name (built-in or org). The tool description steers the LLM toward calling it for org skills only — built-in bodies are already inlined.
- `agents/src/agents/architect/doc.ts` — `DesignDoc` grows `attachSkillToProject(name)` / `detachSkillFromProject(name)`; mutates root frontmatter's `skillsApplied`; openapi NOT invalidated.
- `agents/src/agents/architect/prompt.ts` — `buildUserPrompt` now constructs the two-tier skill section: inlines `builtinSkills` bodies under "Platform skills — MUST consult", renders `orgSkills` as a name + description manifest under "Org skills — load if relevant". System prompt's architecture-phase checklist gains: "Attach the skills this project needs. Common pairings: `go` for any Go service; `react-webapp` for any SPA; `api-management` for any service with `exposesAPI.auth` set; `thunder-authentication` whenever a SPA uses end-user auth (always attach `react-webapp` alongside — the SPA wiring patterns live there)."
- `agents/src/agents/architect/schema.ts` — split `availableSkills` into `builtinSkills: z.array(SkillRecord)` (full bodies) and `orgSkills: z.array(SkillDescription)` (name + description). PR 1's single `availableSkills` field is deleted here.
- `asdlc-service/services/design_service.go:StreamArchitect` — splits `SkillService.List(orgId)` result by `kind` and populates the two fields above.
- `asdlc-service/services/design_service.go` — **delete `seedDefaultSkillsApplied` entirely.** New designs ship with empty `skillsApplied`; the architect's `attach_skill` is the only writer. If the architect later detaches every skill, the field becomes empty and STAYS empty — no re-stamping path. A one-shot migration on PR 3 deploy attaches the four built-ins to existing designs with no `skillsApplied` (preserves today's behaviour for legacy projects).
- `remote-worker/src/oneshot.ts` — at init, after bearer is loaded, calls `skills_pull` against `$ASDLC_PLATFORM_URL/api/v1/tasks/$ASDLC_TASK_ID/skills`; passes the response to `provisionWorkspace`. Existing init-retry logic covers transient BFF blips.
- `remote-worker/src/lib/workspace.ts:provisionWorkspace` — calls `skills_materializer` with the pulled `SkillResolution[]`. If empty, skips creating `.asdlc/skills-plugin/`; otherwise writes the full AgentSkills plugin tree (all kinds in one plugin).
- `remote-worker/src/lib/runner.ts` — `plugins` array conditionally adds a second entry pointing at `<workspace>/.asdlc/skills-plugin/` only when the directory exists. **Also builds the `skills:` SDK preload array** from the pulled response: `["asdlc:asdlc", ...pulled.filter(s => s.kind === 'builtin').map(s => "asdlc-task-skills:" + s.materializedName)]`. Custom and imported entries are NOT in the preload — they sit in the plugin and surface via the SDK's standard skill listing. Empty pull (pre-PR-1 backfilled tasks, or design with empty `skillsApplied`) leaves the preload as `["asdlc:asdlc"]` and loads only the base plugin — same effective behaviour as today.
- `asdlc-service/services/issue_body.go:buildIssueBody` — appends skill-fact bullets derived from snapshot rows (one per attached skill plus inline echoing of Platform-facts content for defence in depth).
- `asdlc-service/services/task_stream.go:editIssueBodyWithRetries` — reads snapshot, not live design.

**Argo CR stays small.** No skill data in `WorkflowRun.spec.arguments.parameters`. No 256 KB etcd budget concern. No ConfigMap creation, no owner-reference dance, no GC race — the earlier-design `ASDLC_SKILLS_CONFIGMAP` escape valve is unnecessary under the pull model. The ClusterWorkflow manifest changes shrink to just the existing parameter set (no new fields).

**Merge gate:** end-to-end test: create a project → architect attaches three built-ins + one custom skill → tech-lead emits skill content in the issue body → dispatched runner pulls from the BFF → `.asdlc/skills-plugin/skills/<materializedName>/SKILL.md` exists for each → SDK preloads the three built-in bodies (assert via SDK transcript inspection or initial-context probe) → SDK lists the custom skill as discoverable but does NOT inject its body until the agent invokes it → `skills-ref validate` passes on the materialised tree. Plus negative-path tests: simulate BFF 404 → runner errors out cleanly with diagnostic; simulate empty `{ skills: [] }` → runner loads base plugin only, preload reduces to `["asdlc:asdlc"]` without warnings; simulate a `kind=builtin` entry whose materialised file is missing → runner fails the task with a clear error (rather than letting the SDK's skip-with-warning hide the bug).

### Out of scope for this design

Filed as separate design docs (sequenced after, blocking-on as noted):

- `docs/design/skill-overrides.md` — built-in skill overrides (org rewrites `api-management` etc.). Includes drift detection, three-way merge UI, per-file vs whole-skill granularity, base-sha tracking, `[Re-merge]` flow. v1 ships built-ins read-only; this design re-evaluates demand after PR 2 lands and orgs start using custom skills.
- `docs/design/org-rbac.md` — `org_admin` role; non-blocking — PR 2 ships with the looser gate.
- `docs/design/skill-snapshot-gc.md` — periodic cleanup of `design_version_skill_snapshots` rows where all referencing tasks are terminal for >90 days; non-blocking.

---

## Appendix — finalised mechanics

These are precise specifications surfaced during architectural review. Each is referenced from earlier sections; they live here to keep the body of the doc readable.

### Backfill for pre-skills tasks

PR 1 lands the snapshot machinery. Design versions whose tasks were created before PR 1 deploys have no `design_version_skill_snapshots` rows. The PR includes a one-shot migration **scoped to design versions that still have un-dispatched tasks** (any `ComponentTask` in `pending` or `on_hold`):

```sql
-- Pseudocode embedded in the PR 1 migration:
-- For every distinct (project_id, source_design_version) tuple where at least one
-- ComponentTask is in (pending, on_hold) and no matching snapshot row exists:
--   1. Resolve the design.md at that version via ArtifactStore (reads from the
--      git tag v<N>-<M>).
--   2. Read its root-frontmatter skillsApplied (defaulting to all four built-ins
--      if absent).
--   3. Resolve each skill at the org's CURRENT catalog (PR 1 = built-ins only
--      since custom/imported tables don't exist until PR 2; built-ins never
--      change at resolve time anyway since v1 has no override mechanism).
--   4. INSERT snapshot rows.
-- Logs a warning for any design version that fails to resolve (e.g. the git tag
-- is missing — its tasks will dispatch with an empty skill set, same as today).
```

Tasks already in `in_progress`, `ready_for_review`, or `building` at PR 1 deploy time are deliberately **NOT** backfilled. Their agent pods were already provisioned (or completed) without skill materialisation, and their issue bodies were already written without skill-fact bullets. Backfilling these would let PR 3's `editIssueBodyWithRetries` re-render and inject bullets that weren't there at agent-start, surprising the in-flight agent mid-task. PR 3's read paths (`buildIssueBody`, `editIssueBodyWithRetries`, materialisation) treat a missing snapshot row as "this task pre-dates skills; behave as today" — no bullets injected, no per-task plugin materialised.

The result: in-flight pre-PR-1 work runs to merge/reject under its original contract; new work (pending at PR 1 deploy) joins the skills system at PR 1's first dispatch.

### Imported-skill deletion lifecycle

`DELETE /api/v1/orgs/:orgId/skills/:name` on an imported skill:

- **No tasks reference it (no row in `design_version_skill_snapshots` AND no `skillsApplied` entry on any design.md):** deletes the row outright. Audit-logged.
- **In-flight tasks reference it via a snapshot row for some `(project_id, design_version)`** (tasks not yet merged/rejected/abandoned): refuses with 409 `IMPORTED_SKILL_IN_USE`, response body listing affected `(project_name, design_version, task_count_in_flight)` tuples. The console offers a "force-delete with detach" flow: requires explicit confirmation, then strips the skill from every project's design.md going forward (new design versions exclude it) and proceeds with the delete. In-flight tasks keep their snapshot-frozen body — the snapshot table is the source of truth for what the agent sees; future re-renders of the issue body still work because they read the snapshot, not the live catalogue.
- **Past design versions reference it** (snapshot rows for design versions whose tasks are all merged/rejected/abandoned): the snapshot rows are kept indefinitely (they're an immutable record of what the platform shipped to that design version's agents), but the `skills` table row deletes cleanly. The snapshot rows don't need a foreign-key reference to survive.

**Re-uploading a name from a previous era.** If an org deletes `imported/payments-pci-handling` and later imports a new tarball under the same name, no auto-re-attachment to historical designs happens — `skillsApplied[]` on each design.md only re-attaches a manual entry if the name is still there. The force-delete flow stripped them; the new import is a fresh start.

### PR 1 listing-and-passive-display vs PR 3 tools — clarification

PR 1 ships the data flow and the snapshot machinery. The architect's user prompt grows the two-tier skill section: the four built-in bodies inline under "Platform skills — MUST consult" (since `seedDefaultSkillsApplied` attaches all four by default); the "Org skills" manifest renders as "(none)" because no custom/imported rows exist yet. The tech-lead's user prompt similarly grows full bodies for every attached skill. The architect can READ the built-in content and produce designs informed by it, but cannot yet take action on attachment (no `read_skill` to load org-skill bodies, no `attach_skill` to mutate `skillsApplied`).

PR 3 adds the tools (`read_skill`, `attach_skill`, `detach_skill`). At that point the architect can load org-skill bodies on demand, manually attach custom/imported skills, and detach built-ins that don't fit. New designs ship with empty `skillsApplied`; the architect attaches what it sees as relevant.

This split exists because PR 1 is a load-bearing refactor with golden-fixture gating; PR 3 introduces new tool surfaces that need their own testing. Stacking them doubles the merge risk.

---

## Summary

The skills system turns the platform's three monolithic prompt sources into a tenant-attachable catalogue of **four** focused [AgentSkills.io](https://agentskills.io)-compliant directories, plus a tenant-editable surface for org-authored extensions — but only for the parts that *can* be edited.

**Format.** Each skill is an AgentSkills directory: `SKILL.md` (frontmatter + body) + optional `references/<file>.md` files (supplementary detail, loaded on demand). No `scripts/`, no `assets/`. Spec-clean — `skills-ref validate` passes; any AgentSkills-compatible client (Claude Code, Cursor, Copilot, ~30 others) reads our skills directly, and we ingest theirs without translation.

**One body per skill, three agents.** No `appliesTo`, no per-role files. Each skill's body is structured into "What this skill does" / "Platform facts" / "Recommended practice" sections; all three agents see the body and pick the parts relevant to their responsibility.

**Four built-ins.**
- **`api-management`** — gateway JWT validation, `X-User-Id` header, CORS, OpenAPI conventions, dependent-API URL injection, `mustUserID` helper.
- **`thunder-authentication`** — per-project Thunder OAuth client, `window._env_.THUNDER_*` keys, `oidc-client-ts` wiring.
- **`react-webapp`** — Vite + nginx layout, `/env-config.js` synchronous load, `window._env_` key set.
- **`go`** — pinned `golang:1.25-alpine`, pure-Go SQLite, suggested layout, port 9090, multi-stage Dockerfile.

All four ship read-only in v1; orgs can author additional `custom` skills from scratch or `import` AgentSkills directories from the ecosystem. Built-in editing is a follow-on PR (`docs/design/skill-overrides.md`).

**Storage.** A single `skills` table holds built-in, custom, and imported rows (distinguished by `kind`; built-ins have `org_id = ''`). On BFF startup, an idempotent bootstrap step UPSERTs the bundled files into the table. From then on, every reader — architect input, tech-lead input, runner pull endpoint, console — goes through one `SkillService`.

**Propagation.** Skills attach at the **project** level — `skillsApplied: [...]` in `specs/design/design.md` root frontmatter. The architect picks via `attach_skill`. At issue creation the **resolved bodies** are snapshotted into `design_version_skill_snapshots` (keyed `(project_id, design_version)`, each row tagged with `kind`) so the dispatched agent's workspace and the issue body stay consistent until the task lands. At pod init the runner pulls its skills via `GET /api/v1/tasks/:taskId/skills`, writes them under `.asdlc/skills-plugin/`, and starts the Claude Agent SDK with built-in names in the `skills:` preload array so their bodies inject at startup; custom and imported skills sit in the same plugin and surface via the SDK's standard skill listing (description in context, body on invoke).

**Two-tier loading.** Platform skills (the four built-ins) are force-loaded into every agent's context — architect via prompt inlining, tech-lead via prompt inlining, coding agent via SDK `skills:` preload. Org skills (custom + imported) are discoverable — architect via `read_skill` tool, tech-lead via prompt inlining (the architect has already chosen them), coding agent via SDK plugin listing. The mechanism differs because the runtimes differ (Vercel AI SDK for design-time agents has no skill primitive; Claude Agent SDK has both natives), but the user-visible semantic is uniform across agents: platform skills are mandatory contract, org skills are recommendations.

**Implementation.** 2 prerequisite PRs (architect-output golden fixtures + SDK plugin-discovery spike) + 3 implementation PRs (built-in extraction with default-four-attached + tech-lead inlining; custom + imported REST + console; architect tools + runner pull + SDK `skills:` preload). Built-ins are read-only in v1 — the override design ships separately when demand is measured. Each PR's merge gate is its own test suite; behaviour stays equivalent to today after PR 1, gains org-editable skills after PR 2, and gains full attach + materialise + dispatch end-to-end after PR 3.
