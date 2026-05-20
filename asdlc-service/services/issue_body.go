package services

import (
	"fmt"
	"strings"

	"github.com/wso2/asdlc/asdlc-service/models"
)

// issueTitle returns the GitHub issue title for a ComponentTask.
func issueTitle(task *models.ComponentTask) string {
	return task.Title
}

// buildIssueBody produces the markdown body for the GitHub issue that anchors
// a single ComponentTask. The body is intentionally task-specific — workflow,
// constraints, deny-list, and project-structure conventions live in the
// `asdlc` skill (`remote-worker/plugin/skills/asdlc/SKILL.md`), which the
// platform's coding-agent loads at dispatch and a local-flow developer
// installs into Claude Code via `claude plugin install`.
//
// Layout (top → bottom):
//
//  1. Rationale blockquote (LLM-authored, one short sentence).
//  2. LLM-authored body — 5 ## sections (Overview / Scope / Acceptance
//     criteria / References / Task dependencies). Streamed by the
//     tech-lead detail phase.
//  3. Component Reference card (name, type, language, app path,
//     OpenAPI pointer).
//
// Under the URL-as-constant model, the dispatcher posts each upstream's
// external URL via `## Dependency endpoint resolved` comments on this
// issue, and the agent bakes them into the SPA bundle at `npm run build`
// time. CORS is handled at the gateway via the ClusterComponentType's
// Envoy filter — backends MUST NOT ship CORS middleware. The
// empty-URL silent-fallback bug (a `?? ""` default that turned every
// fetch into a relative URL and produced `405 Method Not Allowed` from
// the SPA's own nginx) is guarded against by:
//   1. SKILL.md mandating `throw` on missing env var in `src/api.ts`
//      (no silent same-origin fallback);
//   2. tech-lead's per-task issue body Setup subsection listing each
//      upstream's verbatim `.env=URL` line; and
//   3. architect validator rule that every web-app's `dependsOn` is
//      matched in `componentAgentInstructions` with the upstream's
//      expected env var name.
// All of this lives in the `asdlc` skill and the architect/tech-lead
// prompts; the issue body remains task-specific.
//
// envBindings caveat: OC's `dependencies.endpoints.visibility` enum is
// `{project, namespace}` only. An external URL CANNOT be injected via
// envBindings — the only externally-reachable URL is the gateway's
// HTTPRoute hostname, and that comes from the dispatcher's comment.
// Do not be tempted to "migrate to runtime env injection" for SPAs.
//
// The agent owns branch + PR creation. The PR body MUST contain
// `Closes #<this-issue>` so the platform's pull_request webhook can link
// the PR back to this task — we restate that as a single trailing line
// here for the human-reading-on-GitHub audience and as a fail-safe in
// case the skill is somehow not loaded.
//
// The two unused parameters (repoURL, repoSlug) are kept to preserve the
// existing call sites; they were used by the now-removed Local Developer
// Setup section. Drop them once the call sites stop passing them.
func buildIssueBody(task *models.ComponentTask, comp *models.DesignComponent, _repoURL, _repoSlug string) string {
	var sb strings.Builder

	if task.Rationale != "" {
		sb.WriteString("> ")
		sb.WriteString(task.Rationale)
		sb.WriteString("\n\n")
	}

	if task.Body != "" {
		sb.WriteString(task.Body)
		if !strings.HasSuffix(task.Body, "\n") {
			sb.WriteString("\n")
		}
		sb.WriteString("\n")
	}

	sb.WriteString("---\n\n")

	// Component reference card — points the agent at the multi-file design
	// tree under `specs/design/` rather than inlining contracts/specs.
	sb.WriteString("## Component Reference\n")
	sb.WriteString(fmt.Sprintf("- **Name:** %s\n", task.ComponentName))
	if comp != nil {
		sb.WriteString(fmt.Sprintf("- **Type:** %s\n", comp.ComponentType))
		sb.WriteString(fmt.Sprintf("- **Language/Stack:** %s\n", comp.Language))
		if comp.AppPath != "" {
			sb.WriteString(fmt.Sprintf("- **App Path (within repo):** `%s`\n", normalizeAppPath(comp.AppPath)))
		}
		sb.WriteString(fmt.Sprintf("- **Design:** `specs/design/components/%s/design.md`\n", task.ComponentName))
		if comp.OpenAPISpec != "" {
			sb.WriteString(fmt.Sprintf("- **Contract:** `specs/design/components/%s/openapi.yaml`\n", task.ComponentName))
		}
		sb.WriteString("- **System overview:** `specs/design/design.md`\n")
	}
	sb.WriteString("\n")

	sb.WriteString("---\n")
	sb.WriteString(fmt.Sprintf("When you open the PR, include `Closes #%d` in its body so the platform links the PR back to this task. The full workflow, constraints, and deny-list are in the `asdlc` skill loaded in your Claude Code session.\n", task.IssueNumber))

	return sb.String()
}

// normalizeAppPath trims a single leading slash so the rendered Component
// Reference shows e.g. `hello-api` instead of `/hello-api`.
func normalizeAppPath(appPath string) string {
	return strings.TrimPrefix(appPath, "/")
}
