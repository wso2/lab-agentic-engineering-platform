> Delivers the complete REST API service that returns a JSON greeting when GET /hello is called, packaged as a Docker container.

## Overview

This task implements the **hello-api** service, a **Go-based REST API service** for the **hello-api-v1** project. The service provides a single greeting endpoint that returns a JSON message when called, fulfilling the project's core requirement to deliver a simple containerized API.

## Scope

- Implement the full OpenAPI contract for this service (see `specs/design.json` under `components[name="hello-api"].openAPISpec`).
- Service must respond to GET /hello with a JSON greeting message.
- Include a health check endpoint at GET /health for container orchestration.
- Package the service as a Docker container that can be deployed standalone.
- Do not modify other components in this repo.

## Acceptance criteria

- GET /hello returns 200 with `{"message": "Hello, World!"}` and `Content-Type: application/json`.
- GET /health returns 200 with `{"status": "ok"}` and `Content-Type: application/json`.
- Service listens on port 9090 as specified in the component design.
- Docker container builds successfully and runs the service.
- Service handles shutdown signals gracefully.
- Basic request logging is present for observability.

## References

- Component design entry: `specs/design.json` under `components[name="hello-api"]`
- OpenAPI specification: `specs/design.json → components[name="hello-api"].openAPISpec`
- Component agent instructions contain additional implementation guidance for this service.

## Task dependencies

None.

---

## Component Reference
- **Name:** hello-api
- **Type:** service
- **Language/Stack:** Go
- **App Path (within repo):** `/hello-api`
- **Contract:** see `specs/design.json` → `components[name="hello-api"].openAPISpec`

## Project Structure Requirements
Create a production-ready project structure under your component's app path:
- go.mod with proper module path
- cmd/ or main.go entry point
- Dockerfile (multi-stage build)
- Internal packages as needed (handlers/, services/, models/)

Create a `workload.yaml` at the root of your component directory to declare endpoints and dependencies. Refer to the OpenChoreo workload configuration guidance in your skill for the correct format, endpoint types, dependency wiring, and the SPA nginx proxy pattern for frontend components.

The platform will handle git commit, push, build, and deploy automatically.

## Local Developer Setup
These commands are for the **human developer**, before invoking `claude`. The agent itself must not modify auth state — the skill forbids `gh auth login`, credential-helper edits, and token writes.

```bash
git clone https://github.com/asdlc-repos/hello-api-v1122.git asdlc-repos-hello-api-v1122
cd asdlc-repos-hello-api-v1122//hello-api          # AppPath from Component Reference above
git checkout task/hello-api-1f9d3bdf
gh auth status || gh auth login   # must be authenticated as a user with write access to the repo above
claude                            # with the asdlc plugin installed
```

Required `gh` scopes: `repo` and `workflow` (same as the platform PAT, per `CLAUDE.md`). The cluster coding-agent runs everything in an ephemeral pod for you; this section is only for the local-laptop path.

## How To Submit
Your working directory should be a fresh clone of this repo with `git` and `gh` configured. The cluster coding-agent prepares this for you; if you're running locally, see Local Developer Setup above. Branch `task/hello-api-1f9d3bdf` is the working branch — do all work on that branch.

- Post progress updates as comments on this issue:
    `gh issue comment 1 --body "..."`
- When implementation is complete, push your branch and mark the PR ready for review:
    `git push origin HEAD && gh pr ready <pr-number>`

- **Do not merge the PR.** Review and merge are human gates.

## Constraints
- Implement the full API contract — every endpoint must be functional.
- The component must have a `Dockerfile` for containerized builds.
- The app MUST start without any required environment variables — use sensible hardcoded defaults for all config (JWT secrets, DB paths, API URLs, etc.). Environment variables may override defaults but must never be required.
- Do NOT stub or mock — write real, working implementations.
- Do NOT run, start, or execute the application server locally. Only write source files. The platform builds and deploys your code automatically — local execution is unnecessary and causes port conflicts.
- If you must run a quick compile check (e.g. `go build`, `tsc --noEmit`), do so without starting a server. Never use `go run`, `npm start`, `node server.js`, or any command that starts a long-running process.

## Do Not
- Push to any branch other than `task/hello-api-1f9d3bdf`. Do not force-push (`git push --force`).
- Run `gh pr merge`, `gh pr close`, `gh repo create`, `gh repo delete`, `gh repo fork`, or `gh repo edit`.
- Delete remote branches (`git push --delete`, `git push origin :branch`).
- Modify branch protection, secrets, repository settings, collaborators, or webhooks.
- Interact with repos other than this one.

