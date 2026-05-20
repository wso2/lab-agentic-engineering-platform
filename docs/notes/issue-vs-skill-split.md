# Issue ↔ Skill content split — investigation notes

> Goal: keep the GitHub issue task-specific (what to build), and let the
> ASDLC plugin's skill carry the platform workflow (how to work inside
> the platform).

**Started:** 2026-05-10
**Branch:** stablize-workers
**Working dir:** /Users/wso2/repos/lab-app-factory

---

## Phase 0 — Pre-flight

- Node Ready ✅
- OC controller leader fresh ✅ (`controller-manager-…-pjlb6`, renewed `2026-05-10T07:06:53Z`)
- OpenBao ClusterSecretStore `Valid` ✅
- All 5 asdlc pods Running in `dp-wso2cloud-app-factory-development-bad5f211` ✅
- Postgres in `wso2cloud` Running ✅
- Console URL discovered: `http://http-app-factory-c-development-wso2cloud-f1c2e3e1.openchoreoapis.localhost:19080`
- One non-critical pod CrashLooping in observability-plane (`metrics-adapter-prometheus`) — not on the blocking list, proceeding.
- Webhook relay was DOWN; restarted with `nohup …/webhook-relay.sh`. Now: smee-client + kubectl port-forward 18080 both up. Smee channel: `https://smee.io/GADXb56RHHozeW`.

## Phase 1 — Where the content lives today

### Issue body — `asdlc-service/services/issue_body.go::buildIssueBody`

Sections, in order:

1. **Rationale blockquote** — task-specific (LLM-authored one-liner)
2. **LLM body** — task-specific (5 ## sections from the tech-lead detail
   prompt: Overview / Scope / Acceptance criteria / References / Task
   dependencies). The tech-lead prompt at
   `agents/src/agents/tech-lead/prompt.ts:184-186` is *already* told NOT
   to restate "constraints, deny-lists, submission flow, project layout"
   — but we still append all of those below.
3. **Component Reference** — task-specific (Name / Type / Language / App
   Path / "see specs/design.json → openAPISpec")
4. **Component Dependencies** — task-specific (lists actual `dependsOn`
   names + injects env var name + a `workload.yaml` snippet wiring the
   `envBindings.address`)
5. **Project Structure Requirements** — **GENERIC** (per-language
   hints from `projectStructureHints`)
6. **Workload.yaml line** — **GENERIC** (already says "refer to the
   OpenChoreo workload configuration guidance in your skill"; redundant
   one-liner that points at the skill anyway)
7. **Local Developer Setup** — task-specific (bash with this task's
   branch / repo URL / app path)
8. **How To Submit** — **GENERIC** (the `gh issue comment` /
   `git push origin HEAD && gh pr ready N` workflow). Only the **PR
   number** in the `gh pr ready N` line is task-specific.
9. **Constraints** — **GENERIC** (Dockerfile, no required env vars, no
   long-running processes, full API contract, no stubs/mocks)
10. **Do Not** — **GENERIC** (don't push other branches, no force-push,
    no `gh pr merge`, no repo settings, no other repos)

### Skill — `remote-worker/plugin/skills/asdlc/SKILL.md`

- "Find the issue" — uses `gh issue list --label asdlc-task` ⚠ (BFF
  actually creates issues with `["asdlc", "implementation", "pending"]`
  labels — `asdlc-task` does not exist; the search would return
  nothing. Bug.)
- "Workflow" — read issue, post opening comment, stay on branch, edit /
  commit / push, post progress comments, finish with `gh pr ready`
- "Constraints" — same 5 bullets as the issue's Constraints section
- "Do not" — same 5 bullets as the issue's Do Not section
- "OpenChoreo Workload Configuration" — full canonical reference
  (already correctly skill-hosted)
- "SPA / Frontend components" — full pattern (already skill-hosted)
- "Common pitfalls" table

### Conclusion of audit

The "generic" content above is duplicated between issue and skill. The
agent reads the issue first — and many of those generic sections are
near-line-by-line copies of what is already in the skill. **Slim the
issue body** to task-specific content; **the skill already has the
workflow** but is missing language project-structure hints. Move those
hints in, fix the label bug, slim the issue.

---

## Phase 2 — Move plan

### Remove from issue body (`buildIssueBody`)

- Project Structure Requirements section (and `projectStructureHints`
  helper) → lives in skill
- "Refer to the OpenChoreo workload configuration guidance" line →
  skill is auto-loaded; the platform agent already loads it; the local
  developer is told via README to install the plugin
- "How To Submit" section → skill
- "Constraints" section → skill (already there)
- "Do Not" section → skill (already there)

### Keep in issue body

- Rationale blockquote
- LLM body (5 ## sections)
- Component Reference card (with `appPath`, `openAPISpec` pointer)
- Component Dependencies block (env var injection)
- Local Developer Setup (compact: `git clone … && cd … && git checkout
  task-branch && claude`)
- One short final line carrying the **task-specific** anchors:
  `Branch: <branch>` · `Mark ready when done: gh pr ready <N>`. This
  preserves the only PR-number-bearing instruction so the agent has it
  inline.

### Add to skill

- Project structure hints by language (Go / TypeScript / React / Python
  / fallback) — small section the agent reads when scaffolding.
- Fix the label discovery snippet: change `--label asdlc-task` →
  `--label asdlc` (or, better, just rely on the issue URL the platform
  passes via the prompt — the skill already says you can; clarify).
- Add a note: "The platform passes you the issue URL in your prompt.
  Just `gh issue view <URL>`. Use the label query only when running
  locally without a prompt."

### Files touched

- `asdlc-service/services/issue_body.go` (delete the 4 generic sections;
  drop `projectStructureHints`)
- `asdlc-service/services/issue_body_test.go` (if exists — update
  expectations)
- `remote-worker/plugin/skills/asdlc/SKILL.md` (add project structure
  hints, fix label query, restructure)

### Rebuild

- `app-factory-api` (BFF) — picks up new issue body
- `app-factory-coding-agent-runner` — image bakes the plugin/skill, so
  skill changes need a rebuild + import

---

## Phase 3 — Verification plan

Run two end-to-end passes:

1. **Baseline** (before changes): create project with hello-world API
   prompt → generate requirements → design → tasks. Capture the issue
   bodies verbatim.
2. **After changes**: same flow → capture the new (slim) issue bodies.
   Verify the skill now carries the moved sections.
3. **Remote dispatch**: pick one task, click "Run on Cluster", watch
   the WorkflowRun complete, verify PR is opened ready_for_review.

---

## Phase 4 — Baseline run

Created project `hello-api-v1` (repo `asdlc-repos/hello-api-v1122`).
Prompt: "Build a Hello World REST API. Single GET /hello endpoint that
returns a JSON greeting. Implement in Go with a Dockerfile."

- One task generated: "Implement hello-api service with /hello
  endpoint" → issue #1 (5162 chars / 91 lines).
- Issue captured at `/tmp/baseline-issue-body.md`.
- Labels actually applied: `asdlc`, `implementation`, `pending` (skill
  searches for `asdlc-task` — confirmed bug).

Bug spotted in baseline body — `cd asdlc-repos-hello-api-v1122//hello-api`
(double slash). Cause: design returned `appPath = "/hello-api"` with
leading slash; issue body code blindly does `repoSlug + "/" + appPath`.
Will fix as part of the slim refactor.

## Phase 5 — Move plan (final)

The baseline confirms the audit. Sections to remove from
`buildIssueBody`:
- Project Structure Requirements
- "Refer to OpenChoreo workload guidance in your skill" sentence
- How To Submit (keep a 1-line "Mark ready when done" carrying PR#)
- Constraints
- Do Not

Keep in issue:
- Rationale blockquote
- LLM body (5 ## sections)
- Component Reference
- Component Dependencies snippet (when applicable)
- Local Developer Setup (compact, fixed `appPath` slash handling)
- One trailing line: branch + `gh pr ready <N>` for the PR-specific
  command.

Skill additions:
- Project structure hints by language
- Fix label query: `--label asdlc` (not `asdlc-task`)
- Note that the platform passes the issue URL via prompt, so most of
  the time the agent doesn't need to discover the issue at all.

## Phase 5 — Rebuild output

Rebuilt 4 components (BFF, agents-service, runner image, console, then
git-service after artifact 404 was traced to a stale running binary
predating the multi-file requirements rename). 2GB runner image needed
a docker builder/image prune to fit through k3d's import (initial OOM
exit 137 in `k3d-openchoreo-tools` container; `docker builder prune`
freed ~7GB and the second import succeeded).

While debugging this I confirmed: the baseline (`hello-api-v1`) had
appeared to work because both BFF and console were at the same stale
revision; rebuilding only the BFF revealed the route mismatch
(`/spec/generate` vs `/requirements/files/.../generate`). The same
applied to git-service (running binary lacked the artifact routes
entirely → all `/artifacts/*` paths returned 404 from the http mux).

This is independent of the issue/skill refactor — it surfaced because
the dev-cycle naturally re-derived an image hash for changed BFF code
and the user's local cluster had drifted across components. Worth
calling out: if you rebuild only one component after a long pause,
expect mismatches.

## Phase 6 — Re-verification

Re-ran the same `Build a Hello World REST API ...` prompt on a fresh
project (`hello-api-v2`) after all rebuilds.

- Issue body length dropped from **5162 → 2825 chars (-45 %)**.
- Removed sections (now in skill): Project Structure Requirements,
  workload.yaml pointer line, How To Submit, Constraints, Do Not.
- Kept (task-specific): rationale, 5-section LLM body, Component
  Reference, Local Developer Setup (compact), Submission anchor with
  the branch + `gh pr ready <N>` line.
- The earlier slug bug (`asdlc-repos-hello-api-v2751//hello-api`,
  double slash) is fixed — body now shows
  `cd asdlc-repos-hello-api-v2751/hello-api`.
- Slim body saved at `slim-issue-body.md` next to this file.

## Phase 7 — Remote agent + PR

Dispatched the slim-issue task via "Execute all → Implement via Remote
Agents". Coding-agent ran end-to-end on the cluster:

- WorkflowRun `coding-agent-af9aada7-1778400471905` scheduled in
  `workflows-default` namespace
- Pod ran for ~90 s and completed cleanly
- Agent loaded the **`asdlc:asdlc` skill** as the very first tool call
  — confirmed in pod logs (seq=4, kind=tool_use, tool=Skill,
  skill="asdlc:asdlc"). This was the goal: the skill is auto-loaded by
  the platform agent now that it's bundled in the runner image
- Agent posted opening + completion comments on issue #1
- PR #2 (`task/hello-api-af9aada7`) opened, **isDraft: false**
  (ready_for_review), 110 additions / 0 deletions, 5 files:
  - `specs/task.json` (platform-seeded)
  - `hello-api/main.go`
  - `hello-api/go.mod`
  - `hello-api/Dockerfile`
  - `hello-api/workload.yaml` (correct flat WorkloadDescriptor format
    from skill: port 9090, external visibility, no `kind:`/`spec:`
    wrapper — proves the skill content was used)

**End-to-end outcome.** Slim issue + skill-loaded workflow works. The
agent had everything it needed: task spec from issue, platform conventions
from skill, no duplication.

---

## Summary

- **Code change**: 3 files
  - `asdlc-service/services/issue_body.go` (rewritten — slim,
    task-specific only; fixes the `//hello-api` double-slash bug)
  - `remote-worker/plugin/skills/asdlc/SKILL.md` (added project
    structure hints, fixed label query from `asdlc-task` →
    `asdlc + implementation`, restructured workflow text)
  - `remote-worker/plugin/.claude-plugin/plugin.json` (version bump
    0.2.0 → 0.3.0)
  - `agents/src/agents/tech-lead/prompt.ts` (updated the "what the
    platform appends" comment to match the new layout)
- **Build**: 4 components rebuilt (BFF, agents-service, runner image,
  console). Git-service was also surfaced as stale during debugging
  and rebuilt — pre-existing drift, not caused by this change.
- **Verification**: issue body 5162 → 2825 chars (-45 %); remote-agent
  dispatch produces a ready_for_review PR with the expected
  `workload.yaml` shape from the skill.

## Bugs found and fixed in passing

1. `appPath` with leading slash → `cd repo//hello-api` double-slash
   in Local Developer Setup. Fixed via `normalizeAppPath` (issue body
   shows `hello-api`, not `/hello-api`).
2. Skill's issue-discovery snippet referenced label `asdlc-task` but
   BFF actually applies `asdlc` + `implementation`. Fixed.

## Bugs found but NOT fixed (out of scope, recorded for follow-up)

1. **`git-service` repo status stuck at `cloning` after webhook
   register** — `hello-api-v3` clone succeeded (logs show "repository
   cloned successfully") but the persisted row stayed at
   `status: "cloning"` because subsequent webhook-registration / link
   updates appear to overwrite the row before the clone goroutine sets
   `Status: "ready"`. Reproduces deterministically on a second
   project created back-to-back. The fix is in `repo_service.go`
   ordering — the clone goroutine should be the *only* path that flips
   to `"ready"`, or the webhook/link updates should preserve the
   field.
2. **`metrics-adapter-prometheus` in `openchoreo-observability-plane`
   crashloops** continuously; not in the cluster-health critical path
   so the runbook lets it ride. Worth flagging upstream.
3. **Dev-cycle drift** — when one component is rebuilt after a long
   pause, the others stay at their old image; if any had cross-service
   contract changes (the requirements/spec rename in this case),
   you'll get 404s until each component is rebuilt. Not a code bug;
   worth a `dev-cycle.sh --all` shortcut and a brief note in the
   troubleshooting section of `deployments-v2/README.md`.

