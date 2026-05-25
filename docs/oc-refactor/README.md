# OpenChoreo Refactor Docs

Analysis and plan for refactoring WSO2 Labs Agentic Engineer to properly follow
OpenChoreo (OC) plane/secret/construct discipline — using the verified **WSO2 Agent Manager (AM)**
as the reference for "the right way."

> **Current scope: Phase 0–1 (M1, self-hosted go-live).** The main docs (`00`/`10`/`20`) cover M1
> only. Everything beyond M1 — LLM gateway, WSO2 Cloud packaging, `secret-manager-api`, lifecycle —
> is in **[`future-phases.md`](./future-phases.md)**.

> AM reference analysis: `/Users/wso2/repos/agent-manager-analysis/` (files 00–09).
>
> **Source to port from:** OSS open-core `/Users/wso2/repos/agent-manager/agent-manager-service/`
> (`clients/secretmanagersvc/` + `openbao` provider); enterprise superset
> `/Users/wso2/repos/agent-platform/agent-manager-service/` (`secrets/` = the `secret-manager-api`
> provider). See `10-target-architecture.md` "Reference sources."

## Read in this order

1. **[`00-executive-summary.md`](./00-executive-summary.md)** — what's already right, the gap list,
   M1 plane map, sequencing. **Start here.**
2. **[`10-target-architecture.md`](./10-target-architecture.md)** — the M1 target design (service
   topology/merge, code-level boundaries §0.3, secret architecture on the `openbao` provider,
   local dev, acceptance invariants).
3. **[`20-refactor-roadmap.md`](./20-refactor-roadmap.md)** — the M1 plan: Phase 0 (structural
   consolidation) + Phase 1 (secret-plane cutover + go-live), each with a **Test gate**.
4. **[`30-architecture-validation.md`](./30-architecture-validation.md)** — adversarial architect
   review (verdict *Sound-with-required-changes*) + the **enforced code-level boundary spec**;
   corrections folded into `00`/`10`/`20`.
5. **[`future-phases.md`](./future-phases.md)** — post-M1: LLM gateway, WSO2 Cloud packaging +
   `secret-manager-api` overlay, lifecycle. *Not part of the current effort.*

## Supporting subsystem analysis (`analysis/`)

Each is code-grounded with `file:line` citations and a "Gap vs AM/OC" + "What must change"
section.

| File | Subsystem |
|---|---|
| `analysis/01-bff-orchestrator.md` | `asdlc-service` BFF + OC integration + state machine |
| `analysis/02-secrets.md` | All secret levels (org/pod/platform) + taxonomy |
| `analysis/03-workflows-build-deploy.md` | Coding-agent runner, WorkflowRuns, build, deploy |
| `analysis/04-agents-llm.md` | `agents/` + `remote-worker/` + skills + LLM creds |
| `analysis/05-deployment-topology.md` | `deployments/`, compose, k3d, manifests, planes |
| `analysis/06-git-identity-datamodel.md` | git-service, GitHub App, webhooks, auth, data model |

## Verification (adversarial re-check of the above)

| File | Scope | Outcome |
|---|---|---|
| `analysis/VERIFY-secrets.md` | secret + OC-client claims | mostly confirmed; OpenBao fully unwired; OC client HAS SecretReference/GitSecret |
| `analysis/VERIFY-orchestration.md` | workflow/build/deploy/LLM | 9/10 confirmed; annotation holds Workload CR (with image), not bare image |
| `analysis/VERIFY-topology.md` | deployment topology | corrections folded into `00`/`10`/`20` (heredoc mix, `.cicd/` partial path, image pinning) |

The synthesis docs (`00`/`10`/`20`) have been corrected to reflect all verification findings.
