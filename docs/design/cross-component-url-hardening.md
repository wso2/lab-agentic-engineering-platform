# Web → API URL wiring — V1 hardening plan

**Status**: proposal, awaiting platform-design-expert review of diffs
**Date**: 2026-05-16
**Replaces**: an earlier same-origin-proxy proposal (rejected — OC `dependencies.endpoints` cannot inject the externally-reachable URL, only the in-cluster `*.svc.cluster.local` URL; a static `dist/config.json` injected at Docker build is equivalent to today's `.env` bake, not strictly simpler)

## Context — the bug

In the `dasd11634` expense app:

1. `expense-web/.env` shipped as `VITE_EXPENSE_API_URL=` (empty value)
2. SPA's `BASE_URL = (import.meta.env.VITE_EXPENSE_API_URL as string) ?? ''` fell back to `""`
3. Every `fetch(\`${BASE_URL}${path}\`)` became a relative URL
4. `POST /auth/login` hit the SPA's own nginx → **405**
5. Built JS bundle confirmed the bug: zero references to any `*.openchoreoapis.localhost` host

**The platform's gateway CORS filter and the topology itself are fine.** Empirical proof: a real Chrome session driven by Playwright against a correctly-built V1 image completed login + dashboard with **zero** console errors, **zero** CORS warnings, and clean 200 responses to cross-origin `POST /auth/login` and `GET /claims`. The bug is **agent reliability**, not architecture.

This plan keeps the existing topology (per-component HTTPRoutes, gateway CORS filter, build-time URL bake) and hardens it against the bug class with structured fields, loud failures, and design-time validation.

## The design

The V1 contract is "the agent reads each upstream's URL from the `## Dependency endpoint resolved` comment, fills `.env` before `npm run build`, and the bundle calls the URL cross-origin." The hardening adds **four layers of defense** against silent failure:

### Layer 1 — Structured architect output
Architect emits `apiBaseUrl` as a typed field on each `service` component, and each `web-app` lists its upstream env-var bindings explicitly. No more free-text. The tech-lead and the issue body consume the structured field directly.

### Layer 2 — Tech-lead writes the URL line verbatim
For every web-app task, the issue body contains a **Setup section** that quotes the exact `.env` line the agent must write. The agent has zero excuse to invent the var name or leave the value blank.

### Layer 3 — Runtime hard fail
The SKILL teaches `if (!BASE_URL) throw new Error(...)` in the SPA's API module — replacing `... ?? ''`. A missing or empty value blows up on boot, visible in the page (or in the agent's verify-before-PR smoke test), instead of silently degrading to a relative URL.

### Layer 4 — Design-time validator + build-artifact check
- Architect validator rejects any web-app whose `componentAgentInstructions` doesn't list a binding for every `dependsOn`.
- The build pipeline already checks that the produced bundle exists; we add a check that the bundle contains the expected hostname literal (catches `.env` empty at build time, not at user-discovery time).

CORS handling is unchanged: the ClusterComponentType-attached Envoy CORS filter on every `visibility: external` HTTPRoute answers preflights correctly. Backends do not ship their own CORS middleware (per the platform-design review's permanent rule).

## File-by-file changes

### File 1 — `remote-worker/plugin/skills/asdlc/SKILL.md`

Focused rewrite. Smaller than the previous proxy proposal.

#### 1A. "Dependency endpoints" section (~lines 104–162)

**Keep the section but tighten it.** New content:

> **Upstream URLs are baked at Docker build time. Resolution flow:**
>
> 1. The platform posts one `## Dependency endpoint resolved` comment on your task's issue per upstream once that upstream reaches `deployed`. The comment carries the upstream's external HTTPRoute URL (e.g. `http://development-default.openchoreoapis.localhost:19080/user-api-http`).
> 2. You write that URL into `<appPath>/.env` keyed by `VITE_<UPSTREAM_UPPER_SNAKE>_URL` BEFORE running `npm run build`.
> 3. The bundle gets the URL frozen in at build time. Vite cannot read pod env at runtime.
>
> **Empty value / fallback handling: do NOT use `?? ''` or any silent default.** A missing or empty env var is a hard build error, surfaced via the runtime hard-fail pattern below. The default-to-empty-string bug shipped to production once and produced a `405 Method Not Allowed` from the SPA's own nginx because every `fetch` became a relative path.

#### 1B. "SPA / Frontend components" section (~lines 476–565)

Keep the topology (static-only nginx, no proxy). Tighten the example.

**Replace the `Reading the URL` subsection (~line 545)** with:

```ts
// src/api.ts — hard fail on missing URL, no silent fallback
const BASE_URL = import.meta.env.VITE_EXPENSE_API_URL as string | undefined;
if (!BASE_URL) {
  throw new Error(
    "VITE_EXPENSE_API_URL not set. Check that .env was filled with the upstream URL from the issue's `## Dependency endpoint resolved` comment BEFORE `npm run build`."
  );
}
```

Add immediately after: *"A `throw` at module top-level surfaces in the browser as a script-load failure — the SPA visibly fails to mount, the agent's verify-before-PR smoke test sees a broken page, and the regression cannot ship invisibly."*

**Remove** the older example with `?? ''` (line ~548 of the current file).

Keep the `workload.yaml`, `Dockerfile`, `.env`, `nginx.conf` snippets — they're already correct for V1.

#### 1C. "Common pitfalls" (~lines 559–565)

- **Keep** "Browser fetches `undefined/todos`" — but rewrite to: *"`.env` missing, wrong key, or built before `.env` was written. With the hard-fail pattern this surfaces as a script error on load, not as a 405."*
- **Modify** "CORS error in browser when calling upstream": *"The ClusterComponentType attaches an Envoy CORS filter to every `visibility: external` HTTPRoute. If you see a CORS error, the gateway filter is misconfigured or your upstream isn't `visibility: external` — do NOT add CORS middleware in your service code."*
- **Add new pitfall**: *"Bundle has the relative URL `/auth/login` instead of the upstream host → check `.env` was non-empty at `npm run build` time."*

#### 1D. "Constraints" (~lines 368–396)

- **Add**: *"Backend service components MUST NOT include CORS middleware. The gateway's Envoy CORS filter on every external HTTPRoute handles preflights. Doubled CORS headers break browsers."*

### File 2 — `agents/src/agents/architect/prompt.ts`

In "Rules for components" (~lines 30–56), add:

- *"All deployable components default to `visibility: [external]`. The platform's gateway CORS filter attaches automatically to every external HTTPRoute via the ClusterComponentType. Service components MUST NOT include CORS middleware in code."*
- *"For each backend in a web-app's `dependsOn`, the web-app's `componentAgentInstructions` must contain a line of the form: `Upstream <name>: env var VITE_<NAME_UPPER_SNAKE>_URL — set in .env from the issue's `## Dependency endpoint resolved` comment.` This is the contract the coding agent and the tech-lead's issue body both consume."*

Remove (if present): any prior text suggesting CORS middleware on backends.

### File 3 — `agents/src/agents/architect/schema.ts` (RECOMMENDED, optional)

Add structured fields so the contract is typed:

```ts
// On SlimComponent — web-app components
upstreamEnvBindings: z.array(z.object({
  upstream: z.string().describe(
    "Sibling component name — must be in dependsOn"
  ),
  envVar: z.string().describe(
    "Vite env var name, e.g. VITE_USER_API_URL. The coding agent writes this key into .env at build time with the upstream's external URL."
  ),
})).optional().describe(
  "For 'web-app' components only: per-upstream Vite env var bindings. " +
  "The tech-lead echoes each entry verbatim into the issue body's Setup section."
),
```

If you don't want a schema change, this stays inside the free-text `componentAgentInstructions` field. Schema is strictly safer because the validator (File 9) can enforce it.

### File 4 — `agents/src/skills/document-generation/component-design.ts`

If File 3 ships, frontmatter template adds:

```
upstreamEnvBindings:                  # web-app only — required when dependsOn non-empty
  - upstream: user-api
    envVar: VITE_USER_API_URL
```

Add: *"For `web-app` components, `upstreamEnvBindings` is required when `dependsOn` is non-empty (one entry per upstream)."*

### File 5 — `agents/src/agents/tech-lead/prompt.ts`

**Phase 2 detail prompt** — add a new mandatory section to the issue body:

> **For `web-app` tasks**: the issue body's Scope section MUST include a Setup subsection with one bullet per `upstreamEnvBindings` entry (or one bullet per `dependsOn` upstream, if no structured field): *"Set `<envVar>=<URL>` in `<appPath>/.env` BEFORE `npm run build`. The URL comes from the `## Dependency endpoint resolved` comment for upstream `<name>` on this issue."*
>
> For `service` tasks: the issue body MUST mention *"Do NOT include CORS middleware. The platform's gateway attaches a CORS filter to every `visibility: external` HTTPRoute."*

### File 6 — `asdlc-service/services/issue_body.go`

The Go comment at lines ~28–37 currently says:

> F3b — the legacy "Component Dependencies" block (consumer-side `dependencies.endpoints` env-binding wiring) has been removed. Under the deploy-gating + URL-as-constant model, the dispatcher injects each upstream URL directly into the agent prompt; the agent bakes it in as a build-time constant.

This is still accurate for V1-hardened, but **rewrite to call out the safety properties**:

> Under the URL-as-constant model, the dispatcher posts each upstream's external URL via `## Dependency endpoint resolved` comments and the agent bakes them into the SPA bundle at `npm run build` time. CORS is handled at the gateway via the ClusterComponentType's Envoy filter — backends MUST NOT ship CORS middleware. Empty-URL bugs are guarded against by the SKILL's mandated `throw` on missing env var, the tech-lead's verbatim Setup subsection in the issue body, and the architect validator rule that every `web-app` `dependsOn` is matched by an `upstreamEnvBindings` entry.

**Optional structural change**: if File 3 ships with the structured field, add to the Component Reference card a small block listing the upstream env vars verbatim:

```go
if comp != nil && comp.ComponentType == "web-app" && len(comp.UpstreamEnvBindings) > 0 {
    sb.WriteString("## Upstream env vars\n")
    sb.WriteString("Fill each of these in `<appPath>/.env` from the `## Dependency endpoint resolved` comments below BEFORE `npm run build`:\n\n")
    for _, b := range comp.UpstreamEnvBindings {
        sb.WriteString(fmt.Sprintf("- `%s=<URL of `%s`>`\n", b.EnvVar, b.Upstream))
    }
    sb.WriteString("\n")
}
```

This requires `models.DesignComponent` to carry `UpstreamEnvBindings []UpstreamEnvBinding`.

### File 7 — `asdlc-service/services/dispatch_service.go`

`AnnounceDependencyDeployed` (lines ~448–558) stays — it is **load-bearing for V1**. The agent reads this comment's URL into `.env`. No change required.

Optional belt-and-suspenders: when posting the comment, include a verbatim `.env` line in the body:

```
## Dependency endpoint resolved

- **user-api**: http://development-default.openchoreoapis.localhost:19080/user-api-http

Set this in your web-app's `.env` before `npm run build`:
```
VITE_USER_API_URL=http://development-default.openchoreoapis.localhost:19080/user-api-http
```
```

This makes the contract impossible to miss. Requires the BFF to know each downstream web-app's expected env-var name — which it does if File 3 ships and the BFF has the design loaded.

### File 8 — `agents/src/agents/architect/validator.ts`

**New rule**: every `web-app` with a non-empty `dependsOn` must have a matching `upstreamEnvBindings[]` entry per upstream, with `upstreamEnvBindings[].upstream ⊆ dependsOn` and `envVar` matching `/^VITE_[A-Z0-9_]+_URL$/`. This catches design-time omissions before they propagate to a build.

If the schema change in File 3 isn't shipped, the validator can scan `componentAgentInstructions` text for the expected pattern — weaker but still useful.

### File 9 — `agents/src/agents/architect/doc.test.ts`

Already dirty in the working tree. Update fixtures:
- A `web-app` with `upstreamEnvBindings: [{ upstream: 'user-api', envVar: 'VITE_USER_API_URL' }]`
- A `service` with `visibility: [external]` and a note "no CORS middleware in source"

### File 10 — `agents/src/agents/architect/tools.ts`

If File 3 ships, `add_component` and `set_*` tools must accept `upstreamEnvBindings`. Small change.

### File 11 — build pipeline (optional)

In whatever step builds the web-app bundle, after `npm run build`, grep the built JS for at least one expected upstream hostname. If absent, fail the build with a clear message. This is the very last safety net and catches "`.env` was technically present but empty / wrong" at build time, not at deploy time.

The implementation path: a small post-build script in the `dockerfile-builder` ClusterWorkflow's web-app branch, or a check in the Vite plugin pipeline. Skip if the rest of the layers suffice.

## What you do NOT change

- **`wso2cloud-deployment` submodule** — gateway CORS filter is already attached by the ClusterComponentType.
- **`asdlc-service/clients/openchoreo/`** — Workload CR shape unchanged.
- **`agents/src/agents/business-analyst/`** — spec generation unaffected.
- **Backend CORS middleware** — already wasn't required; this plan codifies that backends MUST NOT add it.
- **Topology** — still one HTTPRoute per component, still cross-origin from SPA to API, still gateway CORS filter answering preflights.

## Priority tiering

| Priority | Files | What it gets you |
|---|---|---|
| **P0 — mandatory, ship together** | File 1 (SKILL.md), File 2 (architect/prompt.ts), File 6 comment rewrite | Hard fail replaces silent fallback. Bug class no longer ships silently. |
| **P1 — strongly recommended** | Files 3, 4, 5, 8 (schema + doc-gen skill + tech-lead prompt + validator) | Typed contract; tech-lead's issue body has the verbatim Setup section; design-time validator catches design-level omissions. |
| **P2 — polish** | Files 6 (structural add), 7 (dep comment with .env line), 10 (tools.ts) | Belt-and-suspenders inside the issue body and the dep comment. Optional. |
| **P3 — last safety net** | File 11 (build-time bundle grep) | Catches the edge case where everything else passed but the bundle is empty. Optional. |

P0 alone removes the silent-fallback footgun. P0+P1 makes the contract typed end-to-end. P2+P3 are belt-and-suspenders.

## Validation plan after edits land

1. **Unit / component tests**: existing tests in `agents/src/agents/architect/doc.test.ts` pass with updated fixtures. `npx tsc --noEmit` in `agents/` exits clean.
2. **End-to-end via the platform**: create a fresh small project through the console (1 service + 1 web), run design → implement → build → deploy. Confirm:
   - Service has `visibility: [external]` and NO CORS middleware in code.
   - Web's `.env` is filled with the correct URL.
   - SPA's `api.ts` uses `throw` on missing env var, not `?? ''`.
   - Browser-side login works with zero CORS errors (replicating the V1 Playwright result from this validation).
3. **Negative test**: simulate the V0 bug by deleting `.env`'s value before build. Confirm the SPA throws on load and the agent's verify-before-PR step catches it.
4. **Regression check**: re-deploy `dasd11634` with a trivial commit; confirm it still builds; then ALSO open a follow-up PR that fixes its `.env` per this plan and confirm the SPA now reaches the API.
5. **`platform-design-expert` review** of the actual file diffs (per CLAUDE.md).

## Empirical evidence backing this plan

- **V1 topology works end-to-end in a real browser**: Playwright run on 2026-05-16 against `validation-expense` project. Login + dashboard transition succeeded. Network log shows cross-origin `POST` to `http://development-default…:19080/validation-expense-api-http/auth/login` → 200, and `GET .../claims` → 200. **Zero** console errors, **zero** CORS warnings. Screenshot: `/tmp/expense-validation/v1-success.png`.
- **Why a runtime-config "/config.json" alternative was rejected**: OC's `dependencies.endpoints.visibility` enum is `{project, namespace}` only — `external` is rejected with `Unsupported value`. envBindings inject in-cluster `*.svc.cluster.local` URLs, which the browser cannot resolve. Confirmed empirically by `kubectl patch` returning the validation error. Any "ship the URL via `/config.json`" pattern is therefore equivalent to the existing build-time bake (the URL still has to come from somewhere at build time).
- **Why no proxy was chosen for V1**: a same-origin nginx proxy works (V4 was validated end-to-end), but it requires a much larger SKILL.md rewrite (envsubst templates, new `dependencies.endpoints` block, new agent contract). V1-hardened achieves the same user-facing reliability with smaller blast radius, leaving the option to migrate to a proxy later open without breaking the current contract.
- **Reference implementations in-tree**: `app-factory-console` follows the V4-proxy pattern with envBindings; `dasd11634` follows the V1-bake pattern with the bug class this plan eliminates. Either pattern is OC-idiomatic; this plan picks the smaller-blast-radius option for the v1 platform.

## Open questions

- **Should we make the dep comment include the verbatim `.env` line (File 7 optional)?** It requires the BFF to know each downstream web-app's expected env-var name. If File 3 ships, this is easy; otherwise the BFF has to guess from convention (`VITE_<UPSTREAM_UPPER_SNAKE>_URL`). Recommend: yes, with `VITE_<UPSTREAM_UPPER_SNAKE>_URL` as the canonical convention fallback when the structured field is absent.
- **Build-time bundle grep (File 11) — worth doing?** Catches a narrow class of bugs that the other layers should already catch. Adds a build step. Recommend: defer to P3; revisit if P0+P1 leave a residual gap in practice.
