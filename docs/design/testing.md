# Testing Strategy — Component Design

## Overview

All tests run against the real docker-compose stack. No mocked infrastructure. The same `docker-compose.yml` used for local development is the test environment.

## Test Types

### 1. E2E Tests (Playwright)

Full browser-based tests that drive the Console UI against the complete stack.

- **Tool**: Playwright (TypeScript)
- **Target**: Console (browser) → API Service → Agents Service → PostgreSQL
- **Purpose**: Verify user-facing scenarios match `requirements/` specs
- **Location**: `tests/e2e/`

### 2. API Integration Tests

Direct HTTP tests against the Go API Service with real DB and real Agents Service.

- **Tool**: Playwright test runner (or vitest) with native fetch
- **Target**: API Service → PostgreSQL, API Service → Agents Service
- **Purpose**: Verify API contracts, error handling, data persistence
- **Location**: `tests/api/`

## Test Infrastructure

### Docker Compose (shared)

Tests use the same `docker-compose.yml`:

```yaml
services:
  postgres:
    image: postgres:16-alpine
    environment:
      POSTGRES_USER: asdlc
      POSTGRES_PASSWORD: asdlc
      POSTGRES_DB: asdlc
    ports:
      - "5432:5432"

  asdlc-api:
    build: ./asdlc-service
    environment:
      DB_HOST: postgres
      AGENTS_SERVICE_BASE_URL: http://agents-service:3400
      # ... other env vars
    ports:
      - "9090:9090"
    depends_on:
      postgres:
        condition: service_healthy

  agents-service:
    build: ./agents
    environment:
      ANTHROPIC_API_KEY: ${ANTHROPIC_API_KEY}
    ports:
      - "3400:3400"

  asdlc-console:
    build:
      context: ./console
      dockerfile: Dockerfile.dev
    ports:
      - "8090:8090"
    depends_on:
      - asdlc-api
```

### Test Lifecycle

1. `docker compose up -d` (start all services)
2. Wait for health checks to pass
3. Run test suites
4. Each test suite cleans DB state between tests (truncate tables or per-test transactions)
5. `docker compose down` (teardown)

### DB Cleanup Between Tests

The API service exposes a test-only endpoint (enabled via `TEST_MODE=true`):

```
POST /api/v1/_test/reset → truncates all tables, re-runs seed data
```

This is only available when `TEST_MODE=true` is set, which docker-compose sets for the test profile.

## Test Structure

```
tests/
├── e2e/                          → Playwright E2E tests
│   ├── playwright.config.ts
│   ├── project-creation.spec.ts  → Scenario 1: create project
│   ├── specification.spec.ts     → Scenario 2: write/approve spec
│   ├── design.spec.ts            → Scenario 3: generate/approve design
│   └── components.spec.ts        → Scenario 4: view components
├── api/                          → API integration tests
│   ├── projects.test.ts
│   ├── specs.test.ts
│   ├── designs.test.ts
│   └── components.test.ts
├── helpers/
│   ├── setup.ts                  → docker compose up, wait for health
│   ├── teardown.ts               → docker compose down
│   ├── api-client.ts             → typed fetch wrapper for test API calls
│   └── db-reset.ts               → call _test/reset between tests
├── package.json
└── tsconfig.json
```

## Mapping Tests to Requirements

Every scenario in `requirements/*.md` must have a corresponding test:

| Requirement | E2E Test | API Test |
|-------------|----------|----------|
| 1-project-management.md | project-creation.spec.ts | projects.test.ts |
| 2-specification.md | specification.spec.ts | specs.test.ts |
| 3-design.md | design.spec.ts | designs.test.ts |
| 4-implementation.md | components.spec.ts | components.test.ts |
| 5-build-deploy.md | (later milestone) | (later milestone) |
| 6-manage-observe.md | (later milestone) | (later milestone) |

## Development Workflow Integration

1. Implement feature
2. Enable `DEBUG_LOG=true` — verify request/response flow in service logs
3. Manually verify with Playwright (`/playwright` skill) in the browser
4. Write test that replicates the exact manual scenario
5. Test must pass in CI against docker-compose stack
