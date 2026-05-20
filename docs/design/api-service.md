# ASDLC API Service — Component Design

## Overview

Go REST API backend. The BFF (Backend for Frontend). Manages ASDLC-specific domain state in PostgreSQL, proxies infrastructure operations to OpenChoreo, and delegates AI work to the Agent Service.

## Tech Stack

- Go 1.25+
- net/http (routing, Go 1.22+ method patterns)
- GORM + PostgreSQL (data layer)
- slog (structured JSON logging)

## Project Structure

```
asdlc-service/
├── cmd/asdlc-api/        → Entry point
├── api/                   → Route registration + middleware stack
├── controllers/           → HTTP handlers
├── services/              → Business logic
├── repositories/          → Data access (GORM)
├── clients/
│   ├── openchoreo/        → OC API client (projects, components)
│   ├── agent/             → Agent Service REST client
│   └── requests/          → Fluent HTTP request builder
├── models/                → Domain models
├── middleware/             → Auth, logging, debug logging, correlation ID
├── migrations/            → gormigrate DB migrations
├── config/                → Environment-based config
└── utils/                 → Response helpers
```

## API Surface

### Projects (proxy to OpenChoreo)
```
GET    /api/v1/projects
POST   /api/v1/projects
GET    /api/v1/projects/{projectName}
DELETE /api/v1/projects/{projectName}
```

### Specs (own DB)
```
POST   /api/v1/projects/{projectName}/spec            → Create spec (from prompt)
GET    /api/v1/projects/{projectName}/spec             → Get current spec
PATCH  /api/v1/projects/{projectName}/spec/approve     → Approve spec
```

### Designs (own DB + delegates to Agent Service)
```
POST   /api/v1/projects/{projectName}/design/generate  → Call Agent Service planner
GET    /api/v1/projects/{projectName}/design            → Get design doc + components
PATCH  /api/v1/projects/{projectName}/design/approve    → Approve → create OC components
```

### Components (OpenChoreo + design info from DB)
```
GET    /api/v1/projects/{projectName}/components        → List components with design info
GET    /api/v1/projects/{projectName}/components/{name}  → Component + responsibilities + API boundaries
```

### Health
```
GET    /health
```

## Database Models

```sql
-- specs
id          UUID PRIMARY KEY
project_id  VARCHAR NOT NULL
org_id      VARCHAR NOT NULL
title       VARCHAR
content     TEXT
status      VARCHAR DEFAULT 'draft'   -- draft | approved
created_at  TIMESTAMP
updated_at  TIMESTAMP
deleted_at  TIMESTAMP

-- designs
id          UUID PRIMARY KEY
spec_id     UUID REFERENCES specs(id)
project_id  VARCHAR NOT NULL
org_id      VARCHAR NOT NULL
content     TEXT                       -- Full design doc markdown
components  JSONB                      -- Array of component definitions
status      VARCHAR DEFAULT 'draft'   -- draft | approved
created_at  TIMESTAMP
updated_at  TIMESTAMP

-- agent_runs
id            UUID PRIMARY KEY
project_id    VARCHAR NOT NULL
org_id        VARCHAR NOT NULL
agent_type    VARCHAR NOT NULL          -- planner | generator | evaluator
status        VARCHAR DEFAULT 'running' -- running | completed | failed
input         TEXT
output        TEXT
tokens_used   INTEGER
started_at    TIMESTAMP
completed_at  TIMESTAMP
```

## Client Connections

```
API Service ──REST──→ Agent Service (design generation, implementation)
API Service ──REST──→ OpenChoreo API (projects, components, builds, deploys)
API Service ──SQL───→ PostgreSQL (specs, designs, agent runs)
```

The Go service has a `clients/agent/` package that calls the Agent Service's REST API:
- `POST /api/v1/design/generate` → returns design + component definitions
- `POST /api/v1/implement/start` → returns run ID
- `GET /api/v1/implement/status/{runId}` → returns progress

## Debug Logging

All API requests and responses are logged at DEBUG level. This includes:
- Request method, path, headers, body
- Response status, body, timing
- Outbound calls to OpenChoreo and Agent Service (request/response)

Toggle via `DEBUG_LOG=true` environment variable or `LOG_LEVEL=debug`.
