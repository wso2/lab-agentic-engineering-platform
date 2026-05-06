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
// a single ComponentTask. Two parts:
//
//  1. The LLM-authored body (`task.Body` from the tech-lead detail phase) —
//     5 markdown sections (## Overview, ## Scope, ## Acceptance criteria,
//     ## References, ## Task dependencies). References point at
//     `.asdlc/design.json` and `.asdlc/spec.md`; OpenAPI YAML is NEVER inlined.
//  2. The platform-appended suffix: a "Local Developer Setup" section, "How
//     To Submit", and "Constraints/Do Not" — same content as before, just
//     pulled out so the LLM can never produce these.
//
// The DesignComponent is read fresh from .asdlc/design.json on every call;
// task rows no longer snapshot component shape.
func buildIssueBody(task *models.ComponentTask, comp *models.DesignComponent, repoURL, repoSlug string) string {
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
			sb.WriteString(fmt.Sprintf("- **App Path (within repo):** `%s`\n", comp.AppPath))
		}
		if comp.OpenAPISpec != "" {
			sb.WriteString("- **Contract:** see `.asdlc/design.json` → `components[name=\"")
			sb.WriteString(task.ComponentName)
			sb.WriteString("\"].openAPISpec`\n")
		}
		if len(comp.DependsOn) > 0 {
			sb.WriteString("\n## Component Dependencies\n")
			sb.WriteString("This component depends on the following components. You MUST:\n")
			sb.WriteString("1. Declare each as a dependency in `workload.yaml` — OpenChoreo injects the resolved URL as an environment variable at runtime.\n")
			sb.WriteString("2. Use the injected environment variable in your application code — **never hardcode service URLs**.\n\n")
			for _, dep := range comp.DependsOn {
				envVar := strings.ToUpper(strings.ReplaceAll(dep, "-", "_")) + "_URL"
				sb.WriteString(fmt.Sprintf("- **%s** → use env var `%s`\n", dep, envVar))
			}
			sb.WriteString("\nAdd this to your `workload.yaml`:\n\n")
			sb.WriteString("```yaml\n")
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

	sb.WriteString("## Project Structure Requirements\n")
	sb.WriteString("Create a production-ready project structure under your component's app path:\n")
	if comp != nil {
		sb.WriteString(projectStructureHints(comp.Language))
	} else {
		sb.WriteString(projectStructureHints(""))
	}
	sb.WriteString("\n")
	sb.WriteString("Create a `workload.yaml` at the root of your component directory to declare endpoints and dependencies. Refer to the OpenChoreo workload configuration guidance in your skill for the correct format, endpoint types, dependency wiring, and the SPA nginx proxy pattern for frontend components.\n\n")
	sb.WriteString("The platform will handle git commit, push, build, and deploy automatically.\n\n")

	// Local Developer Setup — only emitted when both the branch name and
	// repo URL are known. computeBranchName is deterministic on task.ID, so
	// the caller passes either the persisted task.BranchName or the
	// computed-ahead-of-time prediction; either way the developer copies the
	// final value.
	branchForBody := task.BranchName
	if repoURL != "" && repoSlug != "" && branchForBody != "" {
		sb.WriteString("## Local Developer Setup\n")
		sb.WriteString("These commands are for the **human developer**, before invoking `claude`. The agent itself must not modify auth state — the skill forbids `gh auth login`, credential-helper edits, and token writes.\n\n")
		sb.WriteString("```bash\n")
		sb.WriteString(fmt.Sprintf("git clone %s %s\n", repoURL, repoSlug))
		appPath := ""
		if comp != nil {
			appPath = comp.AppPath
		}
		if appPath != "" {
			sb.WriteString(fmt.Sprintf("cd %s/%s          # AppPath from Component Reference above\n", repoSlug, appPath))
		} else {
			sb.WriteString(fmt.Sprintf("cd %s\n", repoSlug))
		}
		sb.WriteString(fmt.Sprintf("git checkout %s\n", branchForBody))
		sb.WriteString("gh auth status || gh auth login   # must be authenticated as a user with write access to the repo above\n")
		sb.WriteString("claude                            # with the asdlc plugin installed\n")
		sb.WriteString("```\n\n")
		sb.WriteString("Required `gh` scopes: `repo` and `workflow` (same as the platform PAT, per `CLAUDE.md`). The cluster coding-agent runs everything in an ephemeral pod for you; this section is only for the local-laptop path.\n\n")
	}

	sb.WriteString("## How To Submit\n")
	sb.WriteString("Your working directory should be a fresh clone of this repo with `git` and `gh` configured. The cluster coding-agent prepares this for you; if you're running locally, see Local Developer Setup above. ")
	if task.BranchName != "" {
		sb.WriteString(fmt.Sprintf("Branch `%s` is the working branch — do all work on that branch.\n\n", task.BranchName))
	} else {
		sb.WriteString("Your feature branch is the working branch — do all work on that branch.\n\n")
	}
	sb.WriteString("- Post progress updates as comments on this issue:\n")
	sb.WriteString(fmt.Sprintf("    `gh issue comment %d --body \"...\"`\n", task.IssueNumber))
	sb.WriteString("- When implementation is complete, push your branch and mark the PR ready for review:\n")
	if task.PullRequestNumber > 0 {
		sb.WriteString(fmt.Sprintf("    `git push origin HEAD && gh pr ready %d`\n\n", task.PullRequestNumber))
	} else {
		sb.WriteString("    `git push origin HEAD && gh pr ready <pr-number>`\n\n")
	}
	sb.WriteString("- **Do not merge the PR.** Review and merge are human gates.\n\n")

	sb.WriteString("## Constraints\n")
	sb.WriteString("- Implement the full API contract — every endpoint must be functional.\n")
	sb.WriteString("- The component must have a `Dockerfile` for containerized builds.\n")
	sb.WriteString("- The app MUST start without any required environment variables — use sensible hardcoded defaults for all config (JWT secrets, DB paths, API URLs, etc.). Environment variables may override defaults but must never be required.\n")
	sb.WriteString("- Do NOT stub or mock — write real, working implementations.\n")
	sb.WriteString("- Do NOT run, start, or execute the application server locally. Only write source files. The platform builds and deploys your code automatically — local execution is unnecessary and causes port conflicts.\n")
	sb.WriteString("- If you must run a quick compile check (e.g. `go build`, `tsc --noEmit`), do so without starting a server. Never use `go run`, `npm start`, `node server.js`, or any command that starts a long-running process.\n\n")

	sb.WriteString("## Do Not\n")
	if task.BranchName != "" {
		sb.WriteString(fmt.Sprintf("- Push to any branch other than `%s`. Do not force-push (`git push --force`).\n", task.BranchName))
	} else {
		sb.WriteString("- Push to any branch other than your feature branch. Do not force-push.\n")
	}
	sb.WriteString("- Run `gh pr merge`, `gh pr close`, `gh repo create`, `gh repo delete`, `gh repo fork`, or `gh repo edit`.\n")
	sb.WriteString("- Delete remote branches (`git push --delete`, `git push origin :branch`).\n")
	sb.WriteString("- Modify branch protection, secrets, repository settings, collaborators, or webhooks.\n")
	sb.WriteString("- Interact with repos other than this one.\n")

	return sb.String()
}

func projectStructureHints(language string) string {
	lower := strings.ToLower(language)
	contains := func(subs ...string) bool {
		for _, sub := range subs {
			if strings.Contains(lower, strings.ToLower(sub)) {
				return true
			}
		}
		return false
	}

	switch {
	case contains("go", "golang"):
		return `- go.mod with proper module path
- cmd/ or main.go entry point
- Dockerfile (multi-stage build)
- Internal packages as needed (handlers/, services/, models/)
`
	case contains("react"):
		return `- package.json with dependencies and scripts
- tsconfig.json
- src/ with App component and entry point
- vite.config.ts
- Dockerfile (build + nginx for serving)
`
	case contains("typescript", "node"):
		return `- package.json with dependencies and scripts
- tsconfig.json
- src/ directory with entry point
- Dockerfile (multi-stage build with node:alpine)
`
	case contains("python"):
		return `- requirements.txt or pyproject.toml
- src/ or app/ directory with entry point
- Dockerfile
`
	default:
		return `- Appropriate package/dependency manifest for the language
- Clear entry point
- Dockerfile for containerized builds
`
	}
}
