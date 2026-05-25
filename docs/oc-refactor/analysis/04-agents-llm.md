# 04 — AI Agents & LLM-Credential Consumption

> Source verified at `/Users/wso2/repos/labs-agentic-engineer` @ `main` (HEAD `68a0a6f`).
> Reference (verified) Agent-Manager analysis cited as **AM 00/02/07** →
> `/Users/wso2/repos/agent-manager-analysis/{00-overview,02-llm-gateway,07-secret-taxonomy}.md`.
> Citations are `file:line`. This file is the only artifact written by this analysis; no source was modified.

---

## Summary (TL;DR)

The platform has **two distinct AI-agent runtimes**, both Anthropic-only despite the README's "model-agnostic" framing:

1. **`agents/` — the spec-generation service** (`app-factory-agents-service`). A long-lived multi-tenant Express service (`agents/src/server/index.ts`) running the **Vercel AI SDK** (`ai` ^6, `@ai-sdk/anthropic` ^3 — `agents/package.json:13-18`). It hosts the BA / Architect / Tech-Lead / Requirements-Chat / document-generation (wireframes, domain-model) agents. The BFF (`asdlc-service`) calls it over HTTP+SSE (`asdlc-service/clients/agents/client.go`). It does **not** hold an Anthropic key — it resolves the **effective org key per request** from git-service (`agents/src/shared/anthropic-key-resolver.ts`).

2. **`remote-worker/` — the coding agent** (`app-factory-coding-agent-runner`). A **one-shot ephemeral pod** scheduled by an OpenChoreo `ClusterWorkflow` → Argo (`deployments/manifests/app-factory-coding-agent.yaml`), running the **Claude Agent SDK** (`@anthropic-ai/claude-agent-sdk` ^0.2.126 — `remote-worker/package.json:11`). Entry `src/oneshot.ts` reads dispatch from `ASDLC_*` env, clones the repo, materialises per-task skills, and runs `query()` once. It gets `ANTHROPIC_API_KEY` from a **per-org K8s Secret** mounted by the workflow (`app-factory-coding-agent.yaml:176-180`).

**Both call `api.anthropic.com` DIRECTLY.** No `baseURL`/proxy is ever set on `createAnthropic` (grep across `agents/src` + `remote-worker/src` — the only `createAnthropic({ apiKey })` calls pass no base URL), and the Claude Agent SDK uses the bare `ANTHROPIC_API_KEY`. **There is no governed LLM gateway** — the sharpest divergence from AM, whose agents call a WSO2 API-Platform gateway that meters tokens/cost and validates a proxy API-Key (AM 02 Area 3, AM 00 flow B).

There are **two unrelated things both called "skills"**: (a) TS-code `Skill` objects in `agents/src/skills/` (tool+prompt bundles for the AI SDK), and (b) the platform **AgentSkills system** (`asdlc-service/skills/builtin/*/SKILL.md` + DB `skills` table) — markdown domain knowledge propagated architect → tech-lead → coding-agent. (b) is the real "platform-controlled config" analogue to AM.

---

## Agents-service map (`agents/src`)

### Process & transport
- Express app, port `3400` (`server/index.ts:15`), `workload.yaml` endpoint `http` type `HTTP` visibility `project` (`agents/workload.yaml:4-9`) — i.e. **project-internal only**, reached by the BFF in-cluster.
- Auth: optional JWT middleware over `/v1/agents` (`server/index.ts:42-51`); if `JWKS_URL` unset, auth is **DISABLED with a warning** ("only safe in local development", `server/index.ts:33-35`). Every `/v1/agents/*` route also requires `X-Oc-Org-Id` (`middleware/org-id.ts`, `server/index.ts:54`).
- BFF client attaches a **service JWT** (audience `agents-service`) and the `X-Oc-Org-Id` header (`clients/agents/client.go:192-201,220,291`).

### Endpoints (all registered in `server/index.ts:73-78`) and the agents behind them
| HTTP route | Registrar | Agent / purpose |
|---|---|---|
| `POST /v1/agents/document-generation/:skillId` | `routes/document-generation.ts` | Requirements bootstrap, functional-reqs, **wireframes**, **domain-model** (DocumentGenerationSkill registry) |
| `POST /v1/agents/architect` | `routes/architect.ts` | Architecture / design.md generation (SSE custom data-* events) |
| `POST /v1/agents/tech-lead/plan` + `/detail` | `routes/tech-lead.ts` | Task planning + per-task issue-body detail |
| `POST /v1/agents/requirements-chat` | `routes/requirements-chat.ts` | Interactive requirements editing (EARS DSL) |
| `POST /v1/internal/dsl/render` | `routes/internal-dsl.ts` | Cluster-internal Excalidraw render helper (no LLM) |
| `POST /v1/internal/cache/invalidate` | inline in `server/index.ts:62-71` | git-service drops the per-org key cache on Connect/Disconnect |

The "business-analyst" lives as a document-generation skill (`agents/src/agents/business-analyst/`), not a top-level route. `agents/src/agents/developer/` exists in the tree but the **runtime coding agent is `remote-worker/`**, not this — `developer` is exported from `index.ts:18-21` but no server route registers it.

### The two code paths to the LLM
1. **`shared/create-agent.ts`** — the generic factory. `run(input, orgId)` (line 46) builds system prompt + tools, resolves the key (`resolveAnthropicKey(orgId)`, line 62), `createAnthropic({ apiKey: key })` (line 63), `streamText({ model: anthropic(config.model), ... })` (line 67). Used by schema-validated, non-streaming-to-client agents.
2. **Each route handler** re-implements the same pattern inline for SSE streaming to the BFF (e.g. `routes/architect.ts:37-45,94-95`; `document-generation.ts:58-66,86`; `tech-lead.ts:86,104,238,330`; `requirements-chat.ts:43,89`). Every one of them: resolve key → `createAnthropic({ apiKey })` → `anthropic(config.model)`.

### How it obtains the LLM API key — **per-request resolver, never mounted**
`shared/anthropic-key-resolver.ts`:
- `resolveAnthropicKey(ocOrgId)` (line 76) GETs git-service `…/internal/credentials/orgs/{orgId}/anthropic/effective-key` (line 96), which returns `{ source: "org"|"platform"|"none", key }` (line 15-20).
- git-service decides org-key-vs-platform-fallback; the **platform key lives in git-service's env `ANTHROPIC_PLATFORM_KEY`** (`docs/design/anthropic-key-dual-token.md:121-124,350`), per-org keys in **Postgres `org_secrets` AES-256-GCM** (not OpenBao — design doc §3, lines 3, 95, 110).
- Cached in an in-process **5-minute LRU keyed by orgId** (lines 36-43, 141-144); `source:"none"` is **not cached** (lines 130-138) so a freshly configured key is picked up immediately; explicit invalidation via `POST /v1/internal/cache/invalidate` (resolver line 49, server line 62).
- Key is used **inline** (`createAnthropic({ apiKey: key })`) and **never logged** (only `source` is logged, line 146-148).

This is **AM Secret-Mechanism 1-adjacent but simpler** (AM 07): instead of OpenBao→SecretReference→ESO→pod env, the long-lived multi-tenant service can't mount per-org secrets, so it fetches the *effective* key over service-JWT HTTP and caches it (design doc §6.4, lines 299-320; matches AM 02's open-question resolution that the agent gets its credential from the control plane and then calls out directly).

### Model-agnostic mechanism — **aspirational, not implemented**
- `shared/config.ts:2`: `model: process.env.AGENT_MODEL || "claude-sonnet-4-5"`. A **single global env var**, one model for every agent and every org. No per-agent and no per-org model selection exists.
- The provider is **hardcoded Anthropic**: every call site imports `@ai-sdk/anthropic` and `createAnthropic`; `tech-lead.ts` even types the handle as `AnthropicProvider`. The Vercel AI SDK *would* allow swapping `@ai-sdk/openai` etc. behind the `model:` arg, but no abstraction layer, registry, or config selects provider/model per agent or per org. The README's "pick the LLM behind each agent" is **not realised in code** — the only knob is `AGENT_MODEL` (whole-service) for the spec agents and `AGENT_MODEL`-independent `claude-*` defaults baked into the Claude Agent SDK for the coding agent.

---

## Remote-worker map (`remote-worker/src` + `plugin/`)

### Where it runs & how it's invoked
- `deployments/manifests/app-factory-coding-agent.yaml` — an OpenChoreo `ClusterWorkflow` (`kind: ClusterWorkflow`, line 2) bound to `ClusterWorkflowPlane/default` (lines 10-12). The BFF creates a WorkflowRun → Argo renders an **ephemeral pod** per task. **This is the AM-sanctioned Workflow-Plane pattern** (AM 00 lesson: use WorkflowRun + Workflow Plane for batch/automation).
- Dispatch arrives **as `ASDLC_*` env vars** (no HTTP body, no token-in-body): `oneshot.ts:34-67` reads `ASDLC_TASK_ID/ORG_ID/PROJECT_ID/COMPONENT_NAME/REPO_URL/BEARER/GIT_SERVICE_URL/PROMPT/IDENTITY_*/PLATFORM_URL/CORRELATION_ID`. Manifest wires each from workflow parameters (`yaml:146-171`).
- Exit codes 0/1/2 map to Argo step status (`oneshot.ts:1-14`); no `retryStrategy` because the agent has side effects (commits, `gh pr ready`) — `yaml:124-126`.

### Claude Agent SDK configuration (`src/lib/runner.ts`)
- `query({ prompt: req.prompt, options: { … } })` (line 91). Options: `cwd = workspace`, `plugins`, `skills: skillPreload`, `allowedTools = ["Read","Write","Edit","Bash","Glob","Grep"]` (line 15,99), `permissionMode:"bypassPermissions"` + `allowDangerouslySkipPermissions:true` (lines 100-101), `persistSession:false`, **`settingSources:[]`** (line 103 — no host settings leak into the container), and a curated `env` (lines 59-68).
- **MCP is retired** ("Phase 0 … MCP is retired", line 14; plugin README line 14). The agent uses raw `git`/`gh` via `Bash`.
- The SDK auto-discovers its bundled native binary (`npm ci --include=optional` in `Dockerfile`); no `pathToClaudeCodeExecutable` (runner line 88-90).

### How it gets its LLM key — **mounted per-org K8s Secret**
- `ANTHROPIC_API_KEY` flows in **from `process.env`** (container env) into the SDK (runner.ts:52, "ANTHROPIC_API_KEY flows through from process.env"); the SDK reads the standard var.
- The manifest mounts it from a **per-org K8s Secret** in `workflows-<orgID>`: `valueFrom.secretKeyRef.name = {{anthropic-secret-ref}}, key = ANTHROPIC_API_KEY` (`yaml:176-180`). git-service materialises that Secret in the dispatch pre-flight (`ApplyAnthropicWPSecret`, design doc §5; manifest comment lines 64-68, 109-113). **Per-WorkflowRun ESO is gone** (manifest lines 197-201) — the key sits in a per-org Secret, mirroring the build-credential Secret.
- **Platform-key safety rule:** the *platform* fallback key **never reaches a workflow pod** — dispatch reads only the per-org row and 422s if absent (design doc lines 320). So the coding agent always runs on the **org's own** Anthropic key.

### How it gets the GitHub token — **never via env; fetched from git-service**
`src/lib/workspace.ts` (`provisionWorkspace`):
- `resolvePATForClone` POSTs git-service `…/api/v1/credentials/refresh` with the per-task **bearer** (read from a chmod-600 file, lines 80-124) and gets a short-lived **GitHub PAT**, embedded once into the clone URL (`x-access-token:<pat>@`, line 176) and **not persisted** in `.git/config`.
- Runtime git/gh use a **credential helper** (`credhelper.sh`) + a **`gh` wrapper** on `PATH` (`workspace.ts:31,203-207`; `lib/credhelper.ts`) that fetch fresh tokens on each call. **No token crosses via process env** (runner.ts:50-52 design note) so SDK transcripts can't leak it. The `asdlc` SKILL.md forbids the agent from touching auth (`plugin/skills/asdlc/SKILL.md:14-18`).

### The `plugin/` overlay and `ASDLC_PROD_RUNNER`
- `remote-worker/plugin/` is a **Claude Code plugin** (`.claude-plugin/plugin.json` name `asdlc` v0.4.0; `marketplace.json`) carrying the single workflow skill `skills/asdlc/SKILL.md` (the 270-line task contract: read issue, branch, `Closes #N`, verify-before-PR, deny-list, workload.yaml grammar).
- runner.ts always loads it: `plugins:[{type:"local", path: PLUGIN_PATH}]` where `PLUGIN_PATH = ../../plugin` (lines 11,77-79), and **preloads its body** via `skills:["asdlc:asdlc"]` (line 80).
- **`ASDLC_PROD_RUNNER`** (git commit `b3b4a91`): the dev workflow bind-mounts the *host* `remote-worker/plugin` onto `/app/plugin` so SKILL.md edits land without rebuilding/pushing the `xlight05/app-factory-coding-agent-runner:latest` image. `app-factory-coding-agent.dev-patch.yaml` adds a `hostPath` volume `/asdlc-dev/plugin` → `/app/plugin` (lines 22-31). The overlay is **default-on** in `setup-asdlc.sh`/`setup-k3d.sh`; **`ASDLC_PROD_RUNNER=1` opts out** to the baked-in image path (`setup-asdlc.sh:65-75`, `setup-k3d.sh:38-46`). Pure dev ergonomics — no runtime behaviour difference.

---

## Skills system

Two separate things share the name; keep them apart:

### (a) AI-SDK code "skills" (`agents/src/skills/`)
TS `Skill` objects = `{ name, description, instructions, tools }` (`skills/types.ts:10-17`). `create-agent.ts` appends each skill's `instructions` to the system prompt under `## Skills` and merges its `tools` (lines 10-32). Only one exists in v1: `codebaseExploration` (`skills/codebase-exploration/index.ts`, bundling `readFile`/`listDirectory`/`searchFiles`). This is an **in-process prompt+tool bundle**, not tenant-configurable. (There's also a separate `skills/document-generation/` registry of `DocumentGenerationSkill`s selected by `:skillId` on the doc-gen route — these are the BA/wireframe/domain-model generators.)

### (b) Platform AgentSkills system — the real "platform-controlled config"
Designed in `docs/design/skills-system.md`; implemented by the "Implement skills system across BFF, agents, and runner" commit (`7881543`).

- **What they are:** AgentSkills.io-compliant markdown directories (`SKILL.md` frontmatter + optional `references/`) carrying platform/stack domain knowledge — **prompt content only**, cannot change cluster behaviour (design doc "Scope" §, lines 36-83). Kinds: `builtin` (platform-shipped, read-only in v1), `custom` (org-authored), `imported` (tarball).
- **Where stored:** four built-ins ship as files `asdlc-service/skills/builtin/{api-management,thunder-authentication,react-webapp,go}/SKILL.md`, **embedded into the BFF binary** (`skills/embed.go` `//go:embed builtin/*/SKILL.md`). On BFF startup `SkillBootstrap.Run()` UPSERTs each into the Postgres **`skills` table** (`asdlc-service/services/skill_bootstrap.go:38-113`), keyed `(org_id, skill_name)` with `org_id=''` for built-ins; purges removed built-ins (lines 99-109). After bootstrap **the DB is the single source of truth** (design doc lines 278-280).
- **How attached & propagated:** the architect attaches a subset to the project (`skillsApplied` in root `design.md` frontmatter). Propagation is **cross-agent** (design doc "Per-agent integration"):
  - **Architect** (Vercel SDK): built-in bodies **inlined whole** under "Platform skills — MUST consult"; org skills as a name+description manifest with a `read_skill` tool. Shipped via `ArchitectRequest.BuiltinSkills []SkillRecord` (full body) + `OrgSkills []SkillDescription` (`clients/agents/client.go:88-111`).
  - **Tech-lead**: plan phase gets descriptions (`AttachedSkills`); detail phase gets **full bodies** per task (`TechLeadDetailItem.SkillsResolved []SkillRecord`, client.go:131-176).
  - **Coding agent**: pulls the **per-task snapshot** at runtime — `oneshot.ts:104-131` calls `pullTaskSkills` (`lib/skills_pull.ts`: GET `{ASDLC_PLATFORM_URL}/api/v1/tasks/{taskId}/skills`, RS256 bearer) → `materializeSkills` (`lib/skills_materializer.ts`) writes them into a **second local plugin** `.asdlc/skills-plugin/` (`{name:"asdlc-task-skills"}`). runner.ts adds that plugin and pushes each `builtin` skill's name into the SDK `skills:` preload as `asdlc-task-skills:<name>` (runner.ts:80-86) so built-in bodies inject at startup; custom/imported surface via SDK discovery (description in context, body on invoke — runner.ts:27-42).
- **Snapshot semantics:** skills are frozen per `(project_id, design_version)` at issue-creation time (`design_version_skill_snapshots`) so in-flight tasks use the contract as of design finalize, not the live edited set (design doc lines 566-607).

**Relation to AM platform-controlled config:** AM's platform config is *infrastructure* — `LlmProxy`/`LlmProvider`/policy parameters pushed to the gateway, plus the agent ComponentTypes/Traits (AM 00 §"diverge", AM 02). Here, the platform-controlled surface is **prompt knowledge** (skills) + secrets, with **no runtime policy enforcement**. Skills cannot meter, gate, or route LLM calls — the design is explicit that anything enforceable lives in BFF/OC code, not skills (design doc lines 60-72).

---

## LLM credential consumption + model-agnostic mechanism (consolidated)

| | `agents/` spec service | `remote-worker/` coding agent |
|---|---|---|
| SDK | Vercel AI SDK (`ai`, `@ai-sdk/anthropic`) | Claude Agent SDK (`@anthropic-ai/claude-agent-sdk`) |
| Key source | git-service `effective-key` resolver per request, 5-min LRU | per-org K8s Secret `ANTHROPIC_API_KEY` mounted by workflow |
| Org vs platform | org key preferred, **platform fallback** allowed | **org key only** (platform key 422s, never enters WP pod) |
| Key at rest | Postgres `org_secrets` AES-256-GCM (git-service) / env for platform | per-org Secret in `workflows-<orgID>` (git-service materialises) |
| Endpoint hit | `api.anthropic.com` **direct** (no baseURL) | `api.anthropic.com` **direct** (SDK default) |
| Model selection | `AGENT_MODEL` env, default `claude-sonnet-4-5` (whole service) | SDK default `claude-*` |
| Provider | hardcoded Anthropic | hardcoded Anthropic (SDK) |

**Model-agnostic verdict:** not implemented. One global model var for the spec service; provider is Anthropic everywhere. No per-org / per-agent model or provider configuration plumbing exists.

---

## Plane placement (current vs should-be)

| Component | Today | Should-be (per AM/OC lessons) |
|---|---|---|
| `agents/` service | OC **data-plane workload** (`workload.yaml`, visibility `project`); reached by BFF; resolves key from git-service (CP-adjacent) | Fine as an OC Component; LLM egress should traverse a **governed gateway** rather than going direct |
| `remote-worker/` coding agent | OC **Workflow Plane** ephemeral pod via `ClusterWorkflow`→Argo — the correct, AM-sanctioned pattern (AM 00) | Keep; route its LLM egress through the same gateway, meter per-org token/cost |
| Anthropic platform key | git-service env (CP namespace), returned inline to agents-service only | If a gateway is adopted, becomes the gateway's **upstream provider credential**, never handed to agents |
| Per-org Anthropic key | Postgres `org_secrets` AES-256-GCM + per-org WP Secret | Behind a gateway, would be the upstream provider auth; agents would hold only a **proxy API-Key** (AM 02 Area 3) |
| AgentSkills | files→BFF embed→Postgres `skills`; snapshot per design version | Sound; this is the right control-plane-owned config surface |

---

## Gap vs AM / OC

1. **No governed LLM gateway — calls go DIRECT to Anthropic.** AM routes every agent→LLM call through a WSO2 API-Platform gateway (envoy router + policy-engine) that (a) validates a per-agent **proxy `API-Key`**, (b) holds the real provider key as **upstream auth** so agents never see it, and (c) **meters tokens & enforces cost/rate-limit policies in the data plane** (AM 02 Areas 2-3, 5; AM 00 flow B). Here, **both runtimes hold the real provider key and call `api.anthropic.com` directly** — no metering, no per-org rate/cost limiting, no central policy, no token redaction at the wire. This is the single biggest divergence and the main thing to close for a multi-tenant platform.

2. **Provider key reaches the workload (worse than AM's worst case, but narrower blast radius).** AM's least-protected credential is the provider key shipped *plaintext to the gateway pod* (AM 07 Mechanism 2). Here the org's Anthropic key is mounted **into the coding-agent pod** and **fetched into the spec-service process memory** — i.e. the credential reaches the *agent* itself, which AM specifically avoids by interposing the gateway. Mitigations present: per-org isolation, platform key never entering WP pods, key never logged, scrubber primed on it (`oneshot.ts:80`).

3. **No usage/cost accounting.** AM meters via gateway policy-engine using per-provider token-extraction identifiers (AM 02 Area 5). This platform captures only AI-SDK `usage.{inputTokens,outputTokens}` locally for logging (`create-agent.ts:81-97`, route `onFinish` logs) — no aggregation, no per-org billing, no enforcement.

4. **"Model-agnostic" is unrealised.** AM's whole point is a provider/proxy catalog with model-name normalization and per-deployment routing (AM 02). Here it's one hardcoded provider + one global model env.

5. **Secrets path is a reasonable OC-aligned simplification, not a regression.** Using a per-org WP K8s Secret (coding agent) + a control-plane resolver (spec service) avoids AM's out-of-band gateway secret duplication. Per-org keys live in Postgres AES-GCM rather than OpenBao/SecretReference (`anthropic-key-dual-token.md` §3) — acceptable, but means the platform process can decrypt org keys (same property AM flags as the consequence of not keeping the consumer inside the SecretReference machinery, AM 07 lesson).

6. **AgentSkills ≠ governed runtime policy.** AM's platform config can *enforce* (gateway policies, traits). Skills are prompt-only and explicitly cannot gate behaviour (design doc lines 60-72). Governance of *what models/tools/limits* an org's agents may use has no home today.

---

## What must change

1. **Interpose a governed LLM gateway** between both runtimes and Anthropic (the AM model). Concretely: set `createAnthropic({ apiKey, baseURL: <gateway> })` in the spec service and `ANTHROPIC_BASE_URL`/SDK option in the coding agent; hand agents a **per-org proxy key**, keep the real Anthropic key only as the gateway's upstream auth. This recovers metering, cost/rate-limit policy, central audit, and removes the provider key from agent pods/process memory. (AM 02 Areas 2-3.)
2. **Make model/provider selection real.** Replace the single `AGENT_MODEL` env with per-org (and ideally per-agent) model+provider config resolved alongside the key — only then is the README's "pick the LLM behind each agent" true. The Vercel SDK already supports multiple providers; add the abstraction + config.
3. **Add usage/cost flow-back.** Either meter at the gateway (preferred, AM-style) or aggregate the `usage` already collected per call into a per-org ledger and enforce quotas.
4. **Tighten the spec service's internal endpoints.** `/v1/internal/cache/invalidate` is ungated when `JWKS_URL` is unset and is explicitly deferred for a service-JWT gate (`server/index.ts:56-71`); auth-disabled mode (`server/index.ts:32-35`) must not survive into any shared environment.
5. **Decide the org-key-at-rest boundary.** If the gateway is adopted, per-org keys become upstream auth held only by the gateway; if not, document that the platform process can decrypt org Anthropic keys (Postgres AES-GCM) and gate accordingly (AM 07 design lesson).
