# ASDLC Platform — Architecture Design

## Overview

ASDLC is a spec-driven software development lifecycle platform built on top of OpenChoreo. It covers the full lifecycle: specification, design, implementation, deployment, and management — with AI agents driving automation at each stage.

## Organizational Model

- **Organization**: Top-level tenant, maps to an OpenChoreo Namespace
- **Project**: A software project under an organization. Maps to an OpenChoreo Project. One project = one Git monorepo
- **Component**: A deployable unit within a project. Maps to an OpenChoreo Component. Represented as a folder in the monorepo

## Lifecycle Flow

```
Specification → Design → Implementation → Build → Deploy → Manage
```

1. **Specification**: User writes or AI-generates a spec (markdown). Stored in ASDLC's database, versioned
2. **Design**: Planner Agent generates an architecture doc from the approved spec — component breakdown, API boundaries, responsibilities, interactions. User reviews and approves
3. **Implementation**: Components auto-created in OpenChoreo and scaffolded in the Git monorepo. Generator and Evaluator agents implement code per component in a feedback loop
4. **Build & Deploy**: OpenChoreo WorkflowRuns build container images. ReleaseBindings deploy across environments (dev → stage → prod). Same patterns as Integration Platform
5. **Manage & Observe**: Logs, metrics, traces via OpenChoreo Observer API

## Core Services

### 1. ASDLC Console (Frontend)

React SPA styled with Oxygen UI (same as Integration Platform).

**Views**:
- Org → Project list
- Project → Spec | Design | Components | Deploy | Manage
- Component → Responsibilities, API boundaries, implementation status, build logs

**Tech**: React, TypeScript, Vite, Oxygen UI (MUI), React Query, React Router

### 2. ASDLC API Service (Go Backend)

Go REST API. The BFF (Backend for Frontend). Manages ASDLC-specific domain (specs, designs) in PostgreSQL and proxies infrastructure operations to OpenChoreo. Delegates AI work to the Agent Service.

**Domains**:
- Spec management (CRUD, versioning, status transitions)
- Design management (store AI-generated architecture docs, component definitions)
- Implementation orchestration (create OC components, trigger agents, track progress)
- OpenChoreo proxy (builds, deployments, environments)

**Tech**: Go, net/http, GORM, PostgreSQL

### 3. ASDLC Agent Service (TypeScript)

Separate service that owns all AI/LLM interactions. Built with Claude Agent SDK (TypeScript). Exposes a REST API consumed by the Go API Service.

**Why separate**:
- Claude Agent SDK is TypeScript — needs a Node.js runtime
- Keeps Go service focused on CRUD/proxy, agent service focused on AI orchestration
- Can scale independently (agents are CPU/memory intensive during runs)
- Clean separation: Go service never calls LLM APIs directly

**Agents**:
- **Planner Agent**: Spec → architecture, component list, API boundaries, interactions
- **Generator Agent**: Per-component code implementation, commits via Git bot
- **Evaluator Agent**: Reviews code against spec/design, runs tests, requests changes

**Tech**: TypeScript, Node.js, Claude Agent SDK, Express (or Hono)

### 4. Git Integration Service

Manages the user's Git repository via an installed bot (GitHub App / GitLab bot).

**Responsibilities**:
- Monorepo scaffolding (component folders, shared config)
- Code commits from Generator Agent
- Branch and PR management
- Webhook reception for status updates

Runs within the Agent Service initially (both need Node.js, both interact with Git).

### 5. OpenChoreo Client Layer

Go client library within the API Service wrapping OpenChoreo's control plane API. Handles:
- Project / Component / Workflow CRUD
- Build triggering (WorkflowRuns)
- Deployment (ReleaseBindings)
- Environment and pipeline management
- Observability queries (Observer API)

Reuses patterns from Agent Manager's `openchoreosvc/client`.

## High-Level Architecture Diagram

```
                         ┌──────────────────────┐
                         │   User's Browser      │
                         └──────────┬───────────┘
                                    │
                         ┌──────────▼───────────┐
                         │   ASDLC Console       │
                         │   (React + Oxygen UI) │
                         └──────────┬───────────┘
                                    │ REST
                         ┌──────────▼───────────┐
                         │   ASDLC API Service   │
                         │   (Go — BFF)          │
                         └──┬─────┬────┬────┬───┘
                            │     │    │    │
               ┌────────────┘     │    │    └────────────────┐
               │                  │    │                     │
    ┌──────────▼──────┐  ┌───────▼────▼──────┐   ┌─────────▼─────────┐
    │   PostgreSQL     │  │ ASDLC Agent Svc   │   │  OpenChoreo API   │
    │                  │  │ (TypeScript)       │   │  (Control Plane)  │
    │ - specs          │  │                    │   │                   │
    │ - designs        │  │  Claude Agent SDK  │   │ - Projects        │
    │ - agent_runs     │  │  Planner Agent     │   │ - Components      │
    │ - impl_status    │  │  Generator Agent   │   │ - Builds          │
    └──────────────────┘  │  Evaluator Agent   │   │ - Deployments     │
                          │                    │   │ - Environments    │
                          └────────┬───────────┘   │ - Observability   │
                                   │               └───────────────────┘
                          ┌────────▼───────────┐
                          │ User's Git Repo     │
                          │ (monorepo)          │
                          │ /component-a/       │
                          │ /component-b/       │
                          └────────────────────┘
```

## Service Communication

```
Console ──REST──→ API Service ──REST──→ Agent Service
                      │
                      ├──REST──→ OpenChoreo API
                      └──SQL───→ PostgreSQL
```

- **Console → API Service**: All frontend requests go to the Go BFF
- **API Service → Agent Service**: Design generation, implementation triggers (REST)
- **API Service → OpenChoreo**: Project/component/build/deploy operations (REST)
- **API Service → PostgreSQL**: Specs, designs, agent run state (SQL)
- **Agent Service → Git**: Code commits via bot (GitHub API)

## Data Ownership

| Data | Owner | Why |
|------|-------|-----|
| Specs, designs, agent runs | PostgreSQL (ASDLC) | Rich documents, versioning, agent history — not suited for K8s CRDs |
| Projects, components, builds, deployments | OpenChoreo CRDs | Infrastructure primitives — OC is the source of truth |
| Source code | Git repo | Standard practice, agents commit via bot |

## Local Development & Testing

### Docker Compose

All services run via `docker-compose.yml` for local dev:
- **postgres**: PostgreSQL 16
- **asdlc-api**: Go API service (builds from source, hot reload optional)
- **asdlc-agent**: TypeScript agent service (builds from source)
- **asdlc-console**: React dev server (Vite)

### Integration Tests

Tests use the same docker-compose stack. Written in TypeScript (Playwright for E2E, direct HTTP for API tests).

- **E2E tests**: Playwright drives the browser against the real Console → API → Agent → DB stack
- **API integration tests**: HTTP calls against the real API service with real DB
- Tests run against `docker compose up` — no mocks for infrastructure
- Each test suite gets a clean DB state (transaction rollback or truncate between tests)

## Key Design Decisions

| Decision | Rationale |
|----------|-----------|
| Own DB for specs/designs | OC CRDs aren't designed for versioned document storage |
| Separate Agent Service (TypeScript) | Claude Agent SDK is TypeScript; clean separation of AI from CRUD |
| Monorepo per project | Single repo context simplifies agent code generation |
| Planner/Generator/Evaluator agents | Separation prevents self-evaluation bias; context resets prevent anxiety (per agent_blog.md) |
| Bot-installed repo | Agents need write access without user credentials |
| Reuse Integration Platform deploy UX | Same OC primitives, no need to reinvent |
| Docker Compose for everything | Single `docker compose up` for dev and tests — no mocked infra |
