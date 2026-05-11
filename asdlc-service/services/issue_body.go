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
//  4. Component Dependencies block (env-binding wiring, only when the
//     component declares `dependsOn`).
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

	// Component reference card — points the agent at .asdlc/design.json for
	// the canonical OpenAPI / appPath / buildpack rather than inlining them.
	sb.WriteString("## Component Reference\n")
	sb.WriteString(fmt.Sprintf("- **Name:** %s\n", task.ComponentName))
	if comp != nil {
		sb.WriteString(fmt.Sprintf("- **Type:** %s\n", comp.ComponentType))
		sb.WriteString(fmt.Sprintf("- **Language/Stack:** %s\n", comp.Language))
		if comp.AppPath != "" {
			sb.WriteString(fmt.Sprintf("- **App Path (within repo):** `%s`\n", normalizeAppPath(comp.AppPath)))
		}
		if comp.OpenAPISpec != "" {
			sb.WriteString("- **Contract:** see `.asdlc/design.json` → `components[name=\"")
			sb.WriteString(task.ComponentName)
			sb.WriteString("\"].openAPISpec`\n")
		}
		if len(comp.DependsOn) > 0 {
			sb.WriteString("\n## Component Dependencies\n")
			sb.WriteString("This component depends on the following components. Declare each as a dependency in `workload.yaml` (the `asdlc` skill has the full format) and read the URL from the injected env var — never hardcode service URLs.\n\n")
			for _, dep := range comp.DependsOn {
				envVar := strings.ToUpper(strings.ReplaceAll(dep, "-", "_")) + "_URL"
				sb.WriteString(fmt.Sprintf("- **%s** → use env var `%s`\n", dep, envVar))
			}
			sb.WriteString("\n```yaml\n")
			sb.WriteString("dependencies:\n")
			sb.WriteString("  endpoints:\n")
			for _, dep := range comp.DependsOn {
				envVar := strings.ToUpper(strings.ReplaceAll(dep, "-", "_")) + "_URL"
				sb.WriteString(fmt.Sprintf("    - component: %s\n", dep))
				sb.WriteString("      name: http\n")
				sb.WriteString("      visibility: project\n")
				sb.WriteString("      envBindings:\n")
				sb.WriteString(fmt.Sprintf("        address: %s\n", envVar))
			}
			sb.WriteString("```\n")
		}
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
