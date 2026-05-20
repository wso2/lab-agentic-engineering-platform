> Creates the Go REST API service that returns a JSON greeting at the /hello endpoint and runs in a Docker container.

## Overview

This task implements the **hello-api** service, a Go REST API component that provides a JSON greeting endpoint. The service is part of the hello-api-v2 project, a minimal demonstration API that returns a "Hello, World!" message to callers. This is the foundational service component for the project.

## Scope

- Implement a Go HTTP service that listens on port 9090 and exposes the `/hello` endpoint returning `{"message": "Hello, World!"}` with a 200 status code.
- Add a `/health` endpoint that returns `{"status": "healthy"}` with a 200 status code for liveness checks.
- Include proper JSON `Content-Type` headers and request logging.
- Provide a Dockerfile that builds a minimal, production-ready container image for this service.
- Use Go modules for dependency management.
- Do not modify other components in this repo.

## Acceptance criteria

- GET `/hello` returns HTTP 200 with `{"message": "Hello, World!"}` and `Content-Type: application/json`.
- GET `/health` returns HTTP 200 with `{"status": "healthy"}` and `Content-Type: application/json`.
- The service listens on port 9090 inside the container.
- The Dockerfile successfully builds a runnable container image.
- The service logs incoming HTTP requests to stdout.
- The container starts without errors and responds to requests.

## References

- `specs/requirements/requirements.md` — describes the single `/hello` endpoint requirement and the JSON greeting format.
- `specs/design.json` — the `components[name="hello-api"]` entry contains the `componentAgentInstructions` field with implementation guidance.

## Task dependencies

None.

---

## Component Reference
- **Name:** hello-api
- **Type:** service
- **Language/Stack:** Go
- **App Path (within repo):** `hello-api`
- **Contract:** see `specs/design.json` → `components[name="hello-api"].openAPISpec`

## Local Developer Setup
Optional — only if you want to run this task on your laptop instead of letting the platform's coding-agent run it. Install the `asdlc` skill into Claude Code (`claude plugin install <repo>/remote-worker/plugin`); it carries the workflow, constraints, and deny-list. The agent itself never touches auth.

```bash
git clone https://github.com/asdlc-repos/hello-api-v2751.git asdlc-repos-hello-api-v2751
cd asdlc-repos-hello-api-v2751/hello-api
git checkout task/hello-api-af9aada7
gh auth status || gh auth login
claude
```

## Submission
Working branch: `task/hello-api-af9aada7` (already checked out for the cluster agent).
When done: `git push origin HEAD && gh pr ready <pr-number>`. Do not merge.

Full workflow, constraints, project-structure conventions, and deny-list: see the `asdlc` skill loaded in your Claude Code session.

