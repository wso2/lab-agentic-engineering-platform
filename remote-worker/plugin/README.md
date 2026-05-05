# ASDLC Claude Code plugin

Defines the workflow contract an agent follows when working a component task
dispatched by the ASDLC platform. The skill body mirrors `asdlc-service/services/issue_body.go` —
read the issue first, work on the task branch, post progress as `gh issue comment`,
finish with `gh pr ready`, never merge.

## What's inside

- `.claude-plugin/plugin.json` — plugin manifest.
- `.claude-plugin/marketplace.json` — marketplace metadata (for `claude plugin install`).
- `skills/asdlc/SKILL.md` — the workflow skill the agent loads.

No MCP server. The agent uses `git` and `gh` directly inside the per-task
workspace; the platform observes the work via GitHub webhooks.

## Loaded by

- **Remote flow** — `remote-worker/src/lib/runner.ts` passes
  `plugins: [{ type: "local", path: <repo>/remote-worker/plugin }]` to the
  Claude Agent SDK at dispatch.
- **Local flow** — a developer can install it into their own Claude Code:
  ```bash
  claude plugin install /path/to/asdlc/remote-worker/plugin
  ```
  In local mode the developer authenticates with their own `gh auth login`;
  the skill is identical.

## Authentication

The skill never sees credentials. In remote mode the workspace is provisioned
with a git credential helper and a `gh` wrapper that fetch fresh tokens from
git-service on every call. In local mode `gh auth` and the developer's git
config supply credentials. Either way, the agent just runs `git` and `gh`.

See `docs/design/github-integration-phase0.md` §7 for the workspace credential
setup; `docs/design/github-integration-evolution.md` §6 for the per-org
credential model that takes over in Phase 2.
