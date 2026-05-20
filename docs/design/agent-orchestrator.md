# ASDLC Agent Service — Component Design

## Overview

Separate TypeScript service that owns all AI/LLM interactions. Built with Claude Agent SDK. Exposes a REST API consumed by the Go API Service.

## Why a Separate Service

- Claude Agent SDK is TypeScript — requires Node.js runtime
- Go API Service stays focused on CRUD, proxying, and DB operations
- Agent runs are long-running and resource-intensive — separate scaling
- Clean boundary: Go service never calls LLM APIs directly

## Tech Stack

- TypeScript, Node.js
- Claude Agent SDK (`@anthropic-ai/agent-sdk` or `@anthropic-ai/sdk`)
- Express or Hono (HTTP framework)
- Git operations (octokit for GitHub API)

## Architecture

```
ASDLC Agent Service
├── Planner Agent    — Spec → Design (architecture, components, APIs)
├── Generator Agent  — Design → Code (per component, commits to repo)
└── Evaluator Agent  — Code → Review (tests, spec compliance, change requests)
```

### Planner Agent
- **Input**: Approved specification text
- **Output**: Structured design document with:
  - Architecture overview (markdown)
  - Component definitions (JSON): name, responsibilities, API boundaries, interactions, tech stack
- **Runs once** per spec approval
- Returns structured JSON that the Go service stores in PostgreSQL

### Generator Agent
- **Input**: Component definition (responsibilities, API boundaries) from design
- **Output**: Implemented code committed to the monorepo
- **Runs per component**, works in sprints
- Commits code via Git bot (octokit)

### Evaluator Agent
- **Input**: Generated code + original spec/design context
- **Output**: Pass/fail + specific change requests
- **Separated from Generator** to avoid self-evaluation bias
- Reviews: test coverage, spec compliance, API contract adherence, code quality

### Feedback Loop
```
Generator → commits code → Evaluator reviews
    ↑                           │
    └── change requests ────────┘
```

Loop continues until Evaluator approves. Max iterations configurable to prevent infinite loops.

## API Surface

Consumed by the Go API Service only (internal service-to-service).

```
POST /api/v1/design/generate
  Request:  { spec: string, projectContext: { name, repoUrl } }
  Response: { design: string, components: ComponentDefinition[] }

POST /api/v1/implement/start
  Request:  { design: Design, components: ComponentDefinition[], repoUrl: string, gitToken: string }
  Response: { runId: string }

GET  /api/v1/implement/status/{runId}
  Response: { status, components: [{ name, status, lastCommit }] }

GET  /health
```

## Project Structure

```
agents/
├── src/
│   ├── index.ts               → Library entry point (agent exports)
│   ├── server/                → Express server (port 3400)
│   │   ├── index.ts           → Server bootstrap, route registration
│   │   └── routes/            → Per-agent HTTP routes
│   │       ├── business-analyst.ts  → SSE streaming spec generation
│   │       ├── architect.ts         → SSE streaming design
│   │       └── task-generator.ts    → JSON task generation
│   ├── agents/                → One directory per agent (schema, prompt, index)
│   ├── shared/                → createAgent factory, config, types
│   ├── tools/                 → Shared tools (readFile, listDirectory)
│   └── skills/                → Composable skill bundles
├── package.json
├── tsconfig.json
└── Dockerfile
```

## Key Design Principles (from agent_blog.md)

1. **Context resets**: Fresh context for each agent invocation to prevent context anxiety
2. **Separate generator from evaluator**: Prevents self-praise bias
3. **Sprint-based generation**: Break implementation into manageable chunks
4. **Polling-based orchestration**: Go service polls status endpoint for long-running implementations
5. **Harness simplification**: Re-evaluate agent complexity as models improve

## Configuration

```
PORT=3100
ANTHROPIC_API_KEY=sk-ant-...
GITHUB_APP_ID=...
GITHUB_PRIVATE_KEY_PATH=...
LOG_LEVEL=info
```
