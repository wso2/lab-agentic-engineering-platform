# Branch-Based Lifecycle Test Suite Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Ship the end-to-end test suite for the branch-based lifecycle (§10 of the design spec): 7 API integration tests (vitest against `docker compose`) and 4 Playwright E2E tests, backed by fresh test infrastructure that lets tests spin up isolated projects with real git repos on every run.

**Architecture:** Tests run against the real `docker compose` stack — no service mocks. Git-service gains a `TEST_MODE` path that accepts `file://` URLs (pointing at bare repositories created by each test), so tests can exercise real clone / commit / push / merge without hitting GitHub. Tests use a JWT helper that mints an unsigned bearer token (the middleware parses claims but skips signature verification). A `fixture` helper creates a bare repo, a project wired to it, and returns handles to inspect the remote. API tests then drive the BFF's REST surface exactly the way the console does; E2E tests drive the console itself in a headed Playwright browser.

**Tech Stack:** Go 1.25 (git-service + asdlc-service), vitest 3 (API tests), Playwright 1.52 (E2E tests), `go-git`/`git` CLI for bare-repo setup, `golang-jwt/jwt/v5` (already used) for unsigned test tokens.

**Explicitly deferred (not in this plan):**
- **Build-check** pipeline (per-component `go build`/`npm run build`/... during review). Noted as a future review-pipeline addition.
- **AI review** agent (reads diff + artifacts, emits findings). Future plan.
- **Lint / static checks** on review. Future plan.
- **OpenChoreo reconciliation assertions in integration tests.** OC requires a running k3d cluster and is heavy to verify at this layer — Plan 3 already ships unit tests for `ReconcilerService`. The approve integration test asserts the merge succeeded on `main` and the change branch is gone; OC state is out of scope.
- **Gitea / real git server fixtures.** We use local bare repos served over `file://` — adequate for the spec's scenarios and much simpler to set up.

---

## Scope Map (spec §10 → tasks)

| Spec scenario | Task |
|---|---|
| API §10.1 — Create change → branch exists; change.json correct | Task 5 |
| API §10.1 — PUT spec/design/plan on change → file committed+pushed | Task 6 |
| API §10.1 — Discard → branch gone; tasks marked discarded | Task 7 |
| API §10.1 — Approve → main contains the change; branch gone | Task 8 |
| API §10.1 — Parallel changes → two branches coexist | Task 9 |
| API §10.1 — Conflict at approve → merge blocked; conflicts list correct | Task 10 |
| API §10.1 — Scaffolding → new project ends on a change branch | Task 11 |
| E2E §10.2 — Edit-on-main → URL transitions; banner; save=commit | Task 13 |
| E2E §10.2 — Submit for review → Approve & merge → main updates; branch gone | Task 14 |
| E2E §10.2 — Parallel two-tab → second submit surfaces conflict | Task 15 |
| E2E §10.2 — Scaffolding → initial prompt lands on a change page | Task 16 |

---

## File Structure

**New / modified (`git-service`):**
- `services/repo_service.go` — `repoService` gains `testMode bool`; `ValidateRepo` and `validateRepoURL` permit `file://` under TEST_MODE. Constructor signature changes.
- `services/repo_service_test.go` — unit tests for the new branches (existing file; extend).
- `cmd/git-service/main.go` — pass `cfg.TestMode` into `NewRepoService`.

**New (`tests/helpers/`):**
- `tests/helpers/api-client.ts` — modify: point at port 9090, inject JWT bearer.
- `tests/helpers/jwt.ts` — mint an unsigned HS256 JWT with `client_id` + `sub` claims for the BFF to parse.
- `tests/helpers/fixture.ts` — create a bare repo on disk, register a project with the BFF, return a `Fixture` with `orgId`, `projectName`, `bareRepoPath`, `cleanup()`, and helpers to inspect the remote (`listRemoteBranches`, `readFileAtRef`).
- `tests/helpers/wait.ts` — `waitFor(pred, timeoutMs, intervalMs)` polling helper; used when repo cloning is async.
- `tests/helpers/db-reset.ts` — modify: port 9090 + JWT bearer.

**New (`tests/api/`):**
- `tests/api/changes-create.test.ts` — Task 5.
- `tests/api/changes-put-artifacts.test.ts` — Task 6.
- `tests/api/changes-discard.test.ts` — Task 7.
- `tests/api/changes-approve.test.ts` — Task 8.
- `tests/api/changes-parallel.test.ts` — Task 9.
- `tests/api/changes-conflict.test.ts` — Task 10.
- `tests/api/projects-scaffold.test.ts` — Task 11.

**New (`tests/e2e/`):**
- `tests/e2e/helpers/login.ts` — Thunder login helper.
- `tests/e2e/helpers/seed.ts` — spins up a fresh project fixture via HTTP before the test.
- `tests/e2e/edit-on-main.spec.ts` — Task 13 (replaces the skipped `changes.spec.ts`).
- `tests/e2e/submit-and-approve.spec.ts` — Task 14.
- `tests/e2e/parallel-conflict.spec.ts` — Task 15.
- `tests/e2e/scaffolding.spec.ts` — Task 16.

**Modified:**
- `tests/package.json` — add `"test:api"`, `"test:e2e"`, `"test:all"` scripts (cleanup/organize); add `jose` for JWT minting.
- `tests/playwright.config.ts` — add `storageState` hook for reusing login across specs; respect `E2E_BASE_URL` env var.
- `tests/vitest.config.ts` — add `sequence: { concurrent: false }` so shared DB isn't clobbered (one suite at a time).
- `deployments/docker-compose.yml` — mount `${HOME}/.asdlc/test-fixtures:/data/test-fixtures` into `asdlc-api` and `git-service` so bare repos are readable by both sides.
- `tests/helpers/README.md` — short notes on the test setup; keeping in sync with spec §10.
- `tests/e2e/changes.spec.ts` — **delete** (superseded by Task 13).

---

## Task Order Rationale

Phase A (Tasks 1–4) lays down the test scaffolding that every later task depends on. Phase B (Tasks 5–11) writes the 7 API integration tests one scenario per task. Phase C (Tasks 12–16) writes the 4 E2E tests plus one helper task. Phase D (Task 17) wires up npm scripts and a short README so running the suite is a one-liner.

Each API-test task can be implemented independently once Phase A is done; E2E tasks are sequential because Task 12 (login helper) is a dependency.

---

## Phase A — Test Infrastructure

### Task 1: Git-service TEST_MODE allows `file://` URLs

Test bare repos live on the host filesystem. Git-service currently only accepts `https://github.com/...` URLs. Add a TEST_MODE branch that skips GitHub validation when the URL scheme is `file://`.

**Files:**
- Modify: `git-service/services/repo_service.go:39-60, 239-251`
- Modify: `git-service/cmd/git-service/main.go:55`
- Test: `git-service/services/repo_service_test.go` (new file)

- [ ] **Step 1: Write the failing test**

Create `git-service/services/repo_service_test.go`:
```go
package services

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

type fakeCrypto struct{}

func (fakeCrypto) Encrypt(s string) (string, error) { return s, nil }
func (fakeCrypto) Decrypt(s string) (string, error) { return s, nil }

func TestValidateRepoAcceptsFileURLInTestMode(t *testing.T) {
	tmp := t.TempDir()
	bare := filepath.Join(tmp, "bare.git")
	require.NoError(t, exec.Command("git", "init", "--bare", bare).Run())

	svc := NewRepoService(nil, fakeCrypto{}, tmp, true) // testMode = true

	err := svc.ValidateRepo(context.Background(), "file://"+bare, "")
	require.NoError(t, err)
}

func TestValidateRepoRejectsFileURLInProdMode(t *testing.T) {
	svc := NewRepoService(nil, fakeCrypto{}, t.TempDir(), false) // testMode = false

	err := svc.ValidateRepo(context.Background(), "file:///some/bare.git", "")
	require.Error(t, err)
}

func TestValidateRepoRejectsNonGitHubHTTPSInProdMode(t *testing.T) {
	svc := NewRepoService(nil, fakeCrypto{}, t.TempDir(), false)

	err := svc.ValidateRepo(context.Background(), "https://gitlab.com/foo/bar", os.Getenv("UNUSED"))
	require.Error(t, err)
}
```

- [ ] **Step 2: Run test to verify compile error**

Run: `cd git-service && go test ./services/ -run TestValidateRepo -v`
Expected: FAIL with `too many arguments in call to NewRepoService` (constructor doesn't yet take `testMode`).

- [ ] **Step 3: Extend the `repoService` struct and constructor**

In `git-service/services/repo_service.go`, change the struct + constructor + `ValidateRepo`:
```go
type repoService struct {
	repo         repositories.RepoRepository
	crypto       CryptoService
	repoBasePath string
	testMode     bool
}

func NewRepoService(repo repositories.RepoRepository, crypto CryptoService, repoBasePath string, testMode bool) RepoService {
	return &repoService{repo: repo, crypto: crypto, repoBasePath: repoBasePath, testMode: testMode}
}

func (s *repoService) ValidateRepo(ctx context.Context, repoURL, pat string) error {
	if s.testMode && strings.HasPrefix(repoURL, "file://") {
		return nil
	}
	if err := validateRepoURL(repoURL); err != nil {
		return err
	}

	owner, repoName, err := parseGitHubRepo(repoURL)
	if err != nil {
		return err
	}

	return checkGitHubAccess(ctx, owner, repoName, pat)
}
```

Also loosen `CloneRepo` URL validation the same way — replace the `validateRepoURL(repoURL)` call at the top of `CloneRepo` (line 64) with:
```go
	if !(s.testMode && strings.HasPrefix(repoURL, "file://")) {
		if err := validateRepoURL(repoURL); err != nil {
			return nil, err
		}
	}
```

- [ ] **Step 4: Update the main.go wiring**

In `git-service/cmd/git-service/main.go` line 55, change:
```go
	repoService := services.NewRepoService(repoRepo, cryptoSvc, cfg.RepoBasePath, cfg.TestMode)
```

- [ ] **Step 5: Run test to verify it passes**

Run: `cd git-service && go test ./services/ -run TestValidateRepo -v`
Expected: 3 tests PASS.

- [ ] **Step 6: Verify rest of git-service still compiles**

Run: `cd git-service && go build ./... && go test ./... -count=1`
Expected: all existing tests still pass.

- [ ] **Step 7: Commit**

```bash
git add git-service/services/repo_service.go git-service/services/repo_service_test.go git-service/cmd/git-service/main.go
git commit -m "feat(git-service): accept file:// URLs when TEST_MODE is enabled"
```

---

### Task 2: Test JWT helper + corrected API base

The existing `tests/helpers/api-client.ts` points at `localhost:8080` (wrong — BFF listens on 9090) and doesn't send a bearer token (required by JWT middleware). Both fail every API test today. Fix the port, add a JWT minter, and thread the bearer through every request.

The BFF's `ExtractJWTClaims` (`asdlc-service/middleware/jwt/jwt.go`) parses with `ParseUnverified`, meaning any well-formed JWT with a `client_id` claim is accepted.

**Files:**
- Modify: `tests/helpers/api-client.ts`
- Modify: `tests/helpers/db-reset.ts`
- Create: `tests/helpers/jwt.ts`
- Modify: `tests/package.json`

- [ ] **Step 1: Add `jose` as a dev dependency**

Run:
```bash
cd tests && npm install --save-dev jose@5
```

Expected: `package.json` gets `"jose": "^5.x"` in devDependencies.

- [ ] **Step 2: Create `tests/helpers/jwt.ts`**

```ts
import { SignJWT } from 'jose';

/**
 * Mint a JWT that the BFF's `ExtractJWTClaims` will accept. The BFF parses
 * claims unverified, so any HS256 token with a `client_id` claim works.
 * The secret used here is arbitrary.
 */
const TEST_SECRET = new TextEncoder().encode('asdlc-test-secret-not-verified');

export async function testToken(clientId = 'asdlc-test-client', subject = 'test-user'): Promise<string> {
  return new SignJWT({ client_id: clientId })
    .setProtectedHeader({ alg: 'HS256', typ: 'JWT' })
    .setSubject(subject)
    .setIssuedAt()
    .setExpirationTime('1h')
    .sign(TEST_SECRET);
}
```

- [ ] **Step 3: Rewrite `tests/helpers/api-client.ts`**

```ts
/**
 * Test API client — direct HTTP to the Go backend with a test JWT bearer.
 */
import { testToken } from './jwt';

const API_BASE = process.env.API_BASE_URL || 'http://localhost:9090';

async function authHeaders(extra?: Record<string, string>): Promise<Record<string, string>> {
  const token = await testToken();
  return {
    'Content-Type': 'application/json',
    Authorization: `Bearer ${token}`,
    ...(extra ?? {}),
  };
}

async function parseBody<T>(res: Response): Promise<T> {
  if (res.status === 204) return undefined as T;
  const text = await res.text();
  if (!text) return undefined as T;
  try {
    return JSON.parse(text) as T;
  } catch {
    return text as unknown as T;
  }
}

export async function apiGet<T>(path: string): Promise<{ status: number; data: T }> {
  const res = await fetch(`${API_BASE}${path}`, { headers: await authHeaders() });
  return { status: res.status, data: await parseBody<T>(res) };
}

export async function apiPost<T>(path: string, body?: unknown): Promise<{ status: number; data: T }> {
  const res = await fetch(`${API_BASE}${path}`, {
    method: 'POST',
    headers: await authHeaders(),
    body: body === undefined ? undefined : JSON.stringify(body),
  });
  return { status: res.status, data: await parseBody<T>(res) };
}

export async function apiPatch<T>(path: string, body: unknown): Promise<{ status: number; data: T }> {
  const res = await fetch(`${API_BASE}${path}`, {
    method: 'PATCH',
    headers: await authHeaders(),
    body: JSON.stringify(body),
  });
  return { status: res.status, data: await parseBody<T>(res) };
}

export async function apiPut<T>(path: string, body: unknown): Promise<{ status: number; data: T }> {
  const res = await fetch(`${API_BASE}${path}`, {
    method: 'PUT',
    headers: await authHeaders(),
    body: JSON.stringify(body),
  });
  return { status: res.status, data: await parseBody<T>(res) };
}

export async function apiDelete(path: string): Promise<{ status: number }> {
  const res = await fetch(`${API_BASE}${path}`, { method: 'DELETE', headers: await authHeaders() });
  return { status: res.status };
}
```

- [ ] **Step 4: Rewrite `tests/helpers/db-reset.ts`**

```ts
/**
 * Reset test state between test suites. Calls _test/reset on both the BFF
 * and the git-service, truncating DB tables and wiping cloned workspaces.
 */
import { testToken } from './jwt';

const API_BASE = process.env.API_BASE_URL || 'http://localhost:9090';
const GIT_BASE = process.env.GIT_SERVICE_URL || 'http://localhost:3300';

export async function resetTestState(): Promise<void> {
  const token = await testToken();
  await fetch(`${API_BASE}/api/v1/_test/reset`, {
    method: 'POST',
    headers: { Authorization: `Bearer ${token}` },
  });
  // git-service has no auth middleware on its routes
  await fetch(`${GIT_BASE}/api/v1/_test/reset`, { method: 'POST' });
}
```

- [ ] **Step 5: Verify existing tests still shape-check**

Run: `cd tests && npx tsc --noEmit`
Expected: no type errors.

- [ ] **Step 6: Run existing API tests against a live stack**

Start the stack if not already running:
```bash
cd deployments && docker compose up -d
```

Then:
```bash
cd tests && npx vitest run api/health.test.ts
```

Expected: 1 test passes. (If not — stack isn't up, not a code bug.)

- [ ] **Step 7: Commit**

```bash
git add tests/helpers/api-client.ts tests/helpers/db-reset.ts tests/helpers/jwt.ts tests/package.json tests/package-lock.json
git commit -m "test: point helpers at port 9090 and inject JWT bearer"
```

---

### Task 3: Bare-repo fixture helper

Every API test needs a clean git remote. This helper creates a bare repo on the host, seeds it with an initial commit on `main`, and produces a `file://` URL both the host (tests) and the containers (git-service/BFF) can read.

Docker compose already mounts `${HOME}/.asdlc/repos:/data/repos`. We add a separate `${HOME}/.asdlc/test-fixtures:/data/test-fixtures` mount for test bare repos so they don't mix with real project clones.

**Files:**
- Modify: `deployments/docker-compose.yml` — mount test-fixtures into `asdlc-api` and `git-service`.
- Create: `tests/helpers/fixture.ts`.

- [ ] **Step 1: Add the fixture mount to docker-compose**

In `deployments/docker-compose.yml`, update the `asdlc-api` service volumes block (currently `- ${HOME}/.asdlc/repos:/data/repos`) to:
```yaml
    volumes:
      - ${HOME}/.asdlc/repos:/data/repos
      - ${HOME}/.asdlc/test-fixtures:/data/test-fixtures
```

And the same for `git-service`:
```yaml
    volumes:
      - ${HOME}/.asdlc/repos:/data/repos
      - ${HOME}/.asdlc/test-fixtures:/data/test-fixtures
```

- [ ] **Step 2: Create `tests/helpers/fixture.ts`**

```ts
/**
 * Fixture helper: creates a bare git repo on disk with an initial commit on main,
 * and exposes a file:// URL that git-service (running in Docker) can read via the
 * /data/test-fixtures mount, AND that the host-side test code can also read directly.
 *
 * Host path: ${HOME}/.asdlc/test-fixtures/<name>.git
 * Container path: /data/test-fixtures/<name>.git
 */
import { execFileSync } from 'node:child_process';
import { mkdirSync, mkdtempSync, rmSync, writeFileSync } from 'node:fs';
import { tmpdir, homedir } from 'node:os';
import { join } from 'node:path';

const FIXTURES_HOST = join(homedir(), '.asdlc', 'test-fixtures');
const FIXTURES_CONTAINER = '/data/test-fixtures';

export interface BareRepo {
  /** Host path to the bare .git directory. */
  hostPath: string;
  /** file:// URL that the git-service container uses. */
  containerURL: string;
  /** file:// URL the host-side tests use (for ls-remote, file reads). */
  hostURL: string;
  /** Clean up this bare repo. */
  cleanup: () => void;
}

function sh(cwd: string, ...args: string[]): string {
  return execFileSync('git', args, { cwd, stdio: ['ignore', 'pipe', 'pipe'] }).toString();
}

/**
 * Create a fresh bare repo with one initial commit on main, so branches can be
 * created off of a real commit. Returns both host and container URLs.
 */
export function createBareRepo(name: string): BareRepo {
  mkdirSync(FIXTURES_HOST, { recursive: true });
  const hostPath = join(FIXTURES_HOST, `${name}.git`);
  rmSync(hostPath, { recursive: true, force: true });

  execFileSync('git', ['init', '--bare', '--initial-branch=main', hostPath], { stdio: 'ignore' });

  // Seed with an initial commit via a temporary working clone.
  const seed = mkdtempSync(join(tmpdir(), 'asdlc-seed-'));
  try {
    sh(seed, 'init', '--initial-branch=main', '.');
    sh(seed, 'config', 'user.email', 'test@asdlc.local');
    sh(seed, 'config', 'user.name', 'ASDLC Test');
    writeFileSync(join(seed, 'README.md'), `# ${name}\n`);
    sh(seed, 'add', 'README.md');
    sh(seed, 'commit', '-m', 'initial commit');
    sh(seed, 'remote', 'add', 'origin', `file://${hostPath}`);
    sh(seed, 'push', 'origin', 'main');
  } finally {
    rmSync(seed, { recursive: true, force: true });
  }

  const containerURL = `file://${FIXTURES_CONTAINER}/${name}.git`;
  const hostURL = `file://${hostPath}`;

  return {
    hostPath,
    containerURL,
    hostURL,
    cleanup: () => rmSync(hostPath, { recursive: true, force: true }),
  };
}

/** List branches on the bare repo by running `git ls-remote --heads` on the host. */
export function listRemoteBranches(repo: BareRepo): string[] {
  const out = execFileSync('git', ['ls-remote', '--heads', repo.hostURL], { stdio: ['ignore', 'pipe', 'pipe'] }).toString();
  return out
    .split('\n')
    .map((l) => l.trim())
    .filter(Boolean)
    .map((l) => l.replace(/^[0-9a-f]+\s+refs\/heads\//, ''));
}

/** Read a file from a branch or tag on the bare repo. Returns undefined if not present. */
export function readFileAtRef(repo: BareRepo, ref: string, path: string): string | undefined {
  try {
    const out = execFileSync(
      'git',
      ['-C', repo.hostPath, 'cat-file', 'blob', `${ref}:${path}`],
      { stdio: ['ignore', 'pipe', 'pipe'] },
    );
    return out.toString();
  } catch {
    return undefined;
  }
}
```

- [ ] **Step 3: Add a smoke test for the fixture**

Create `tests/helpers/fixture.smoke.test.ts`:
```ts
import { describe, it, expect, afterEach } from 'vitest';
import { createBareRepo, listRemoteBranches, readFileAtRef, BareRepo } from './fixture';

describe('bare-repo fixture', () => {
  let repo: BareRepo | null = null;
  afterEach(() => repo?.cleanup());

  it('creates a bare repo with main and README.md', () => {
    repo = createBareRepo('fixture-smoke');
    const branches = listRemoteBranches(repo);
    expect(branches).toContain('main');
    const readme = readFileAtRef(repo, 'main', 'README.md');
    expect(readme).toContain('fixture-smoke');
  });
});
```

- [ ] **Step 4: Run the smoke test**

Run: `cd tests && npx vitest run helpers/fixture.smoke.test.ts`
Expected: PASS.

- [ ] **Step 5: Update `tests/vitest.config.ts` to include helper smoke tests**

```ts
import { defineConfig } from 'vitest/config';

export default defineConfig({
  test: {
    include: ['api/**/*.test.ts', 'helpers/**/*.test.ts'],
    testTimeout: 60000,
    sequence: { concurrent: false },
  },
});
```

- [ ] **Step 6: Commit**

```bash
git add deployments/docker-compose.yml tests/helpers/fixture.ts tests/helpers/fixture.smoke.test.ts tests/vitest.config.ts
git commit -m "test: add bare-repo fixture helper and mount test-fixtures into containers"
```

---

### Task 4: Project + change factory helper

Every API scenario needs a project wired to a bare repo, and most also need a change already opened on that project. Centralize the creation into one factory so each test reads in a single line.

**Files:**
- Create: `tests/helpers/factory.ts`.

- [ ] **Step 1: Create `tests/helpers/factory.ts`**

```ts
import { apiGet, apiPost } from './api-client';
import { createBareRepo, BareRepo } from './fixture';
import { waitFor } from './wait';

export interface ProjectFixture {
  orgId: string;
  projectName: string;
  repo: BareRepo;
  /** Delete the bare repo from disk. */
  cleanup: () => void;
}

/**
 * Create a bare repo + a project in the BFF that references it. Waits for the
 * clone to finish (GetRepoStatus → "ready").
 */
export async function createProjectFixture(seed: string): Promise<ProjectFixture> {
  const orgId = 'default';
  const projectName = `${seed}-${Date.now().toString(36)}`;
  const repo = createBareRepo(projectName);

  const { status, data } = await apiPost<{ name: string }>(
    `/api/v1/organizations/${orgId}/projects`,
    {
      name: projectName,
      displayName: projectName,
      description: 'created by integration test',
      gitRepoUrl: repo.containerURL,
      gitPat: '',
    },
  );
  if (status !== 201) {
    repo.cleanup();
    throw new Error(`CreateProject failed: ${status} ${JSON.stringify(data)}`);
  }

  // Wait for clone to complete.
  await waitFor(async () => {
    const res = await apiGet<{ status: string }>(`/api/v1/organizations/${orgId}/projects/${projectName}/repo`);
    return res.status === 200 && res.data.status === 'ready';
  }, 20000, 250);

  return {
    orgId,
    projectName,
    repo,
    cleanup: () => repo.cleanup(),
  };
}

export interface ChangeFixture extends ProjectFixture {
  changeId: string;
  changeBranch: string;
}

/** Create a project + open a change on it. */
export async function createChangeFixture(seed: string, title = 'Test change'): Promise<ChangeFixture> {
  const project = await createProjectFixture(seed);
  const { status, data } = await apiPost<{ id: string; branch: string }>(
    `/api/v1/organizations/${project.orgId}/projects/${project.projectName}/changes`,
    { title },
  );
  if (status !== 201) {
    project.cleanup();
    throw new Error(`CreateChange failed: ${status} ${JSON.stringify(data)}`);
  }
  return { ...project, changeId: data.id, changeBranch: data.branch };
}
```

- [ ] **Step 2: Create `tests/helpers/wait.ts`**

```ts
export async function waitFor(
  pred: () => Promise<boolean>,
  timeoutMs: number,
  intervalMs = 200,
): Promise<void> {
  const start = Date.now();
  while (Date.now() - start < timeoutMs) {
    if (await pred()) return;
    await new Promise((r) => setTimeout(r, intervalMs));
  }
  throw new Error(`waitFor timed out after ${timeoutMs}ms`);
}
```

- [ ] **Step 3: Add a smoke test for the factory**

Create `tests/helpers/factory.smoke.test.ts`:
```ts
import { describe, it, expect, afterEach } from 'vitest';
import { createProjectFixture, ProjectFixture } from './factory';
import { listRemoteBranches } from './fixture';

describe('project fixture factory', () => {
  let fx: ProjectFixture | null = null;
  afterEach(() => fx?.cleanup());

  it('creates a project cloned from the bare repo', async () => {
    fx = await createProjectFixture('factory-smoke');
    expect(listRemoteBranches(fx.repo)).toContain('main');
  });
});
```

- [ ] **Step 4: Run the smoke test against a live stack**

Run (stack up): `cd tests && npx vitest run helpers/factory.smoke.test.ts`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add tests/helpers/factory.ts tests/helpers/wait.ts tests/helpers/factory.smoke.test.ts
git commit -m "test: add project and change factory helpers"
```

---

## Phase B — API Integration Tests

Each API test creates its own fixture, drives a scenario, and cleans up. Tests run serially (see Task 3's vitest config change).

### Task 5: API — Create change → branch exists + change.json correct

**Files:**
- Create: `tests/api/changes-create.test.ts`.

- [ ] **Step 1: Write the test**

```ts
import { describe, it, expect, afterEach } from 'vitest';
import { apiPost } from '../helpers/api-client';
import { createProjectFixture, ProjectFixture } from '../helpers/factory';
import { listRemoteBranches, readFileAtRef } from '../helpers/fixture';

describe('POST /changes', () => {
  let fx: ProjectFixture | null = null;
  afterEach(() => fx?.cleanup());

  it('creates a change/* branch on the remote and commits .asdlc/change.json', async () => {
    fx = await createProjectFixture('create-change');

    const { status, data } = await apiPost<{ id: string; branch: string; stage: string; title: string }>(
      `/api/v1/organizations/${fx.orgId}/projects/${fx.projectName}/changes`,
      { title: 'First change' },
    );

    expect(status).toBe(201);
    expect(data.branch).toMatch(/^change\/\d{8}-[a-z0-9]{8}$/);
    expect(data.stage).toBe('drafting_spec');
    expect(data.title).toBe('First change');

    const branches = listRemoteBranches(fx.repo);
    expect(branches).toContain(data.branch);

    const raw = readFileAtRef(fx.repo, data.branch, '.asdlc/change.json');
    expect(raw).toBeDefined();
    const parsed = JSON.parse(raw!) as Record<string, unknown>;
    expect(parsed.id).toBe(data.id);
    expect(parsed.branch).toBe(data.branch);
    expect(parsed.stage).toBe('drafting_spec');
    expect(parsed.title).toBe('First change');
    expect(typeof parsed.baseCommit).toBe('string');
    expect((parsed.baseCommit as string).length).toBeGreaterThan(0);
  });
});
```

- [ ] **Step 2: Run the test against a live stack**

Run: `cd deployments && docker compose up -d && cd ../tests && npx vitest run api/changes-create.test.ts`
Expected: PASS.

- [ ] **Step 3: Commit**

```bash
git add tests/api/changes-create.test.ts
git commit -m "test(api): cover change creation → branch + change.json"
```

---

### Task 6: API — PUT spec/design/plan on change → file committed+pushed

**Files:**
- Create: `tests/api/changes-put-artifacts.test.ts`.

- [ ] **Step 1: Write the test**

```ts
import { describe, it, expect, afterEach } from 'vitest';
import { apiPut } from '../helpers/api-client';
import { createChangeFixture, ChangeFixture } from '../helpers/factory';
import { readFileAtRef } from '../helpers/fixture';

describe('PUT change artifacts', () => {
  let fx: ChangeFixture | null = null;
  afterEach(() => fx?.cleanup());

  it('PUT spec writes .asdlc/spec.md on the change branch', async () => {
    fx = await createChangeFixture('put-spec');
    const content = '# Spec\n\nBuild the widget.\n';

    const { status } = await apiPut(
      `/api/v1/organizations/${fx.orgId}/projects/${fx.projectName}/changes/${fx.changeId}/spec`,
      { content },
    );

    expect(status).toBe(200);
    const blob = readFileAtRef(fx.repo, fx.changeBranch, '.asdlc/spec.md');
    expect(blob).toBe(content);
  });

  it('PUT design writes .asdlc/design.json on the change branch', async () => {
    fx = await createChangeFixture('put-design');
    const design = {
      components: [{ name: 'api', type: 'service' }],
      overview: 'A tiny service.',
    };

    const { status } = await apiPut(
      `/api/v1/organizations/${fx.orgId}/projects/${fx.projectName}/changes/${fx.changeId}/design`,
      { content: design },
    );

    expect(status).toBe(200);
    const blob = readFileAtRef(fx.repo, fx.changeBranch, '.asdlc/design.json');
    expect(blob).toBeDefined();
    expect(JSON.parse(blob!)).toMatchObject(design);
  });

  it('PUT plan writes .asdlc/plan.json on the change branch', async () => {
    fx = await createChangeFixture('put-plan');
    const plan = {
      strategy: 'ship small',
      phases: [{ name: 'P1', tasks: [] }],
    };

    const { status } = await apiPut(
      `/api/v1/organizations/${fx.orgId}/projects/${fx.projectName}/changes/${fx.changeId}/plan`,
      { content: plan },
    );

    expect(status).toBe(200);
    const blob = readFileAtRef(fx.repo, fx.changeBranch, '.asdlc/plan.json');
    expect(blob).toBeDefined();
    expect(JSON.parse(blob!)).toMatchObject(plan);
  });
});
```

- [ ] **Step 2: Run the test**

Run: `cd tests && npx vitest run api/changes-put-artifacts.test.ts`
Expected: 3 tests PASS.

- [ ] **Step 3: Commit**

```bash
git add tests/api/changes-put-artifacts.test.ts
git commit -m "test(api): cover PUT spec/design/plan on change"
```

---

### Task 7: API — Discard → branch gone + tasks marked discarded

**Files:**
- Create: `tests/api/changes-discard.test.ts`.

- [ ] **Step 1: Write the test**

```ts
import { describe, it, expect, afterEach } from 'vitest';
import { apiGet, apiPost, apiPut } from '../helpers/api-client';
import { createChangeFixture, ChangeFixture } from '../helpers/factory';
import { listRemoteBranches } from '../helpers/fixture';

describe('POST /changes/{id}/discard', () => {
  let fx: ChangeFixture | null = null;
  afterEach(() => fx?.cleanup());

  it('deletes the remote branch and 404s subsequent reads', async () => {
    fx = await createChangeFixture('discard-plain');

    expect(listRemoteBranches(fx.repo)).toContain(fx.changeBranch);

    const { status } = await apiPost(
      `/api/v1/organizations/${fx.orgId}/projects/${fx.projectName}/changes/${fx.changeId}/discard`,
      {},
    );
    expect(status).toBe(204);

    expect(listRemoteBranches(fx.repo)).not.toContain(fx.changeBranch);

    const follow = await apiGet(`/api/v1/organizations/${fx.orgId}/projects/${fx.projectName}/changes/${fx.changeId}`);
    expect(follow.status).toBe(404);
  });

  it('flips related ComponentTasks to discarded', async () => {
    fx = await createChangeFixture('discard-with-tasks');

    // Seed a plan so CreateTasks has something to read.
    await apiPut(
      `/api/v1/organizations/${fx.orgId}/projects/${fx.projectName}/changes/${fx.changeId}/plan`,
      {
        content: {
          strategy: 'seed',
          phases: [{ name: 'P1', tasks: [{ component: { name: 'svc', type: 'service' } }] }],
        },
      },
    );

    const create = await apiPost<{ items: Array<{ id: string; status: string }> }>(
      `/api/v1/organizations/${fx.orgId}/projects/${fx.projectName}/changes/${fx.changeId}/tasks/create`,
      {},
    );
    expect(create.status).toBe(200);
    expect(create.data.items.length).toBeGreaterThan(0);

    const discard = await apiPost(
      `/api/v1/organizations/${fx.orgId}/projects/${fx.projectName}/changes/${fx.changeId}/discard`,
      {},
    );
    expect(discard.status).toBe(204);

    const tasks = await apiGet<{ items: Array<{ id: string; status: string; changeBranch: string }> }>(
      `/api/v1/organizations/${fx.orgId}/projects/${fx.projectName}/tasks`,
    );
    expect(tasks.status).toBe(200);
    const related = tasks.data.items.filter((t) => t.changeBranch === fx!.changeBranch);
    expect(related.length).toBeGreaterThan(0);
    expect(related.every((t) => t.status === 'discarded')).toBe(true);
  });
});
```

- [ ] **Step 2: Run the test**

Run: `cd tests && npx vitest run api/changes-discard.test.ts`
Expected: 2 tests PASS.

- [ ] **Step 3: Commit**

```bash
git add tests/api/changes-discard.test.ts
git commit -m "test(api): cover discard → branch deletion + task transitions"
```

---

### Task 8: API — Approve → main contains change + branch gone

**Files:**
- Create: `tests/api/changes-approve.test.ts`.

- [ ] **Step 1: Write the test**

```ts
import { describe, it, expect, afterEach } from 'vitest';
import { apiPost, apiPut } from '../helpers/api-client';
import { createChangeFixture, ChangeFixture } from '../helpers/factory';
import { listRemoteBranches, readFileAtRef } from '../helpers/fixture';

describe('POST /changes/{id}/approve', () => {
  let fx: ChangeFixture | null = null;
  afterEach(() => fx?.cleanup());

  it('merges the change into main and deletes the branch', async () => {
    fx = await createChangeFixture('approve-happy');

    // Write a spec so the branch has meaningful content.
    await apiPut(
      `/api/v1/organizations/${fx.orgId}/projects/${fx.projectName}/changes/${fx.changeId}/spec`,
      { content: '# Spec v1\n' },
    );

    // Submit for review so stage → review_ready.
    const submit = await apiPost(
      `/api/v1/organizations/${fx.orgId}/projects/${fx.projectName}/changes/${fx.changeId}/submit-for-review`,
      {},
    );
    expect(submit.status).toBe(200);

    // Run the conflict-pre-check (synchronous).
    const review = await apiPost(
      `/api/v1/organizations/${fx.orgId}/projects/${fx.projectName}/changes/${fx.changeId}/review/run`,
      {},
    );
    expect(review.status).toBe(200);

    // Approve.
    const approve = await apiPost(
      `/api/v1/organizations/${fx.orgId}/projects/${fx.projectName}/changes/${fx.changeId}/approve`,
      {},
    );
    expect(approve.status).toBe(200);

    // main now contains the spec; change branch is gone.
    const mainSpec = readFileAtRef(fx.repo, 'main', '.asdlc/spec.md');
    expect(mainSpec).toContain('Spec v1');
    expect(listRemoteBranches(fx.repo)).not.toContain(fx.changeBranch);
  });
});
```

- [ ] **Step 2: Run the test**

Run: `cd tests && npx vitest run api/changes-approve.test.ts`
Expected: PASS.

- [ ] **Step 3: Commit**

```bash
git add tests/api/changes-approve.test.ts
git commit -m "test(api): cover approve → merge into main, branch deletion"
```

---

### Task 9: API — Parallel changes coexist

**Files:**
- Create: `tests/api/changes-parallel.test.ts`.

- [ ] **Step 1: Write the test**

```ts
import { describe, it, expect, afterEach } from 'vitest';
import { apiPost, apiPut } from '../helpers/api-client';
import { createProjectFixture, ProjectFixture } from '../helpers/factory';
import { listRemoteBranches, readFileAtRef } from '../helpers/fixture';

describe('parallel changes', () => {
  let fx: ProjectFixture | null = null;
  afterEach(() => fx?.cleanup());

  it('two changes branches coexist on the remote and edit their own spec.md', async () => {
    fx = await createProjectFixture('parallel');

    const a = await apiPost<{ id: string; branch: string }>(
      `/api/v1/organizations/${fx.orgId}/projects/${fx.projectName}/changes`,
      { title: 'change A' },
    );
    const b = await apiPost<{ id: string; branch: string }>(
      `/api/v1/organizations/${fx.orgId}/projects/${fx.projectName}/changes`,
      { title: 'change B' },
    );
    expect(a.status).toBe(201);
    expect(b.status).toBe(201);
    expect(a.data.branch).not.toBe(b.data.branch);

    // Both branches exist on the remote.
    const branches = listRemoteBranches(fx.repo);
    expect(branches).toContain(a.data.branch);
    expect(branches).toContain(b.data.branch);

    // Write a different spec on each.
    await apiPut(
      `/api/v1/organizations/${fx.orgId}/projects/${fx.projectName}/changes/${a.data.id}/spec`,
      { content: 'A content\n' },
    );
    await apiPut(
      `/api/v1/organizations/${fx.orgId}/projects/${fx.projectName}/changes/${b.data.id}/spec`,
      { content: 'B content\n' },
    );

    // Each branch has its own spec.md; main is untouched.
    expect(readFileAtRef(fx.repo, a.data.branch, '.asdlc/spec.md')).toBe('A content\n');
    expect(readFileAtRef(fx.repo, b.data.branch, '.asdlc/spec.md')).toBe('B content\n');
    expect(readFileAtRef(fx.repo, 'main', '.asdlc/spec.md')).toBeUndefined();
  });
});
```

- [ ] **Step 2: Run the test**

Run: `cd tests && npx vitest run api/changes-parallel.test.ts`
Expected: PASS.

- [ ] **Step 3: Commit**

```bash
git add tests/api/changes-parallel.test.ts
git commit -m "test(api): cover parallel changes coexisting"
```

---

### Task 10: API — Conflict at approve

**Files:**
- Create: `tests/api/changes-conflict.test.ts`.

- [ ] **Step 1: Write the test**

The scenario: open change A, merge it (→ main has spec "A"). Open change B off the old base, edit the same spec, submit, approve → 409 with conflict list.

```ts
import { describe, it, expect, afterEach } from 'vitest';
import { apiPost, apiPut } from '../helpers/api-client';
import { createProjectFixture, ProjectFixture } from '../helpers/factory';

describe('merge conflict at approve', () => {
  let fx: ProjectFixture | null = null;
  afterEach(() => fx?.cleanup());

  it('returns 409 with conflict file list when the branch has diverged', async () => {
    fx = await createProjectFixture('conflict');

    // Create two parallel changes off main (both see an empty .asdlc/spec.md state).
    const a = await apiPost<{ id: string; branch: string }>(
      `/api/v1/organizations/${fx.orgId}/projects/${fx.projectName}/changes`,
      { title: 'A' },
    );
    const b = await apiPost<{ id: string; branch: string }>(
      `/api/v1/organizations/${fx.orgId}/projects/${fx.projectName}/changes`,
      { title: 'B' },
    );
    expect(a.status).toBe(201);
    expect(b.status).toBe(201);

    // Both write to .asdlc/spec.md.
    await apiPut(`/api/v1/organizations/${fx.orgId}/projects/${fx.projectName}/changes/${a.data.id}/spec`, {
      content: 'from A\n',
    });
    await apiPut(`/api/v1/organizations/${fx.orgId}/projects/${fx.projectName}/changes/${b.data.id}/spec`, {
      content: 'from B\n',
    });

    // Merge A first.
    await apiPost(`/api/v1/organizations/${fx.orgId}/projects/${fx.projectName}/changes/${a.data.id}/submit-for-review`, {});
    await apiPost(`/api/v1/organizations/${fx.orgId}/projects/${fx.projectName}/changes/${a.data.id}/review/run`, {});
    const approveA = await apiPost(
      `/api/v1/organizations/${fx.orgId}/projects/${fx.projectName}/changes/${a.data.id}/approve`,
      {},
    );
    expect(approveA.status).toBe(200);

    // Now approve B — must conflict on .asdlc/spec.md.
    await apiPost(`/api/v1/organizations/${fx.orgId}/projects/${fx.projectName}/changes/${b.data.id}/submit-for-review`, {});
    await apiPost(`/api/v1/organizations/${fx.orgId}/projects/${fx.projectName}/changes/${b.data.id}/review/run`, {});
    const approveB = await apiPost<{ conflicts?: string[]; message?: string }>(
      `/api/v1/organizations/${fx.orgId}/projects/${fx.projectName}/changes/${b.data.id}/approve`,
      {},
    );

    expect(approveB.status).toBe(409);
    expect(Array.isArray(approveB.data.conflicts)).toBe(true);
    expect(approveB.data.conflicts).toContain('.asdlc/spec.md');
  });
});
```

- [ ] **Step 2: Run the test**

Run: `cd tests && npx vitest run api/changes-conflict.test.ts`
Expected: PASS.

- [ ] **Step 3: Commit**

```bash
git add tests/api/changes-conflict.test.ts
git commit -m "test(api): cover merge conflict at approve (409 + conflict list)"
```

---

### Task 11: API — Scaffolding leaves the user on a change branch

**Files:**
- Create: `tests/api/projects-scaffold.test.ts`.

- [ ] **Step 1: Write the test**

```ts
import { describe, it, expect, afterEach } from 'vitest';
import { apiPost } from '../helpers/api-client';
import { createProjectFixture, ProjectFixture } from '../helpers/factory';
import { listRemoteBranches } from '../helpers/fixture';

describe('POST /bootstrap-change', () => {
  let fx: ProjectFixture | null = null;
  afterEach(() => fx?.cleanup());

  it('returns a drafting_spec change; remote has the change branch', async () => {
    fx = await createProjectFixture('scaffold');

    const { status, data } = await apiPost<{ id: string; branch: string; stage: string }>(
      `/api/v1/organizations/${fx.orgId}/projects/${fx.projectName}/bootstrap-change`,
      { title: `Initial build: ${fx.projectName}` },
    );
    expect(status).toBe(201);
    expect(data.stage).toBe('drafting_spec');
    expect(data.branch).toMatch(/^change\/\d{8}-[a-z0-9]{8}$/);

    expect(listRemoteBranches(fx.repo)).toContain(data.branch);
  });

  it('is idempotent: second call returns the same drafting_spec change', async () => {
    fx = await createProjectFixture('scaffold-idem');

    const first = await apiPost<{ id: string }>(
      `/api/v1/organizations/${fx.orgId}/projects/${fx.projectName}/bootstrap-change`,
      { title: 'Init' },
    );
    const second = await apiPost<{ id: string }>(
      `/api/v1/organizations/${fx.orgId}/projects/${fx.projectName}/bootstrap-change`,
      { title: 'Init' },
    );

    expect(first.status).toBe(201);
    expect(second.status).toBe(201);
    expect(second.data.id).toBe(first.data.id);
  });
});
```

- [ ] **Step 2: Run the test**

Run: `cd tests && npx vitest run api/projects-scaffold.test.ts`
Expected: 2 tests PASS.

- [ ] **Step 3: Commit**

```bash
git add tests/api/projects-scaffold.test.ts
git commit -m "test(api): cover scaffolding lands on a drafting_spec change"
```

---

## Phase C — E2E Tests

### Task 12: E2E helpers — login + seed

Playwright must authenticate through Thunder and start each test on a known project. The login helper logs in once per spec with `admin@openchoreo.dev` / `Admin@123` and persists `storageState` so subsequent page navigations skip the redirect. The seed helper creates a project via the BFF's REST API (using the JWT helper from Task 2) before the spec opens the browser.

**Files:**
- Create: `tests/e2e/helpers/login.ts`.
- Create: `tests/e2e/helpers/seed.ts`.

- [ ] **Step 1: Create `tests/e2e/helpers/login.ts`**

```ts
import { Page } from '@playwright/test';

export const TEST_USER = {
  username: process.env.E2E_USERNAME ?? 'admin@openchoreo.dev',
  password: process.env.E2E_PASSWORD ?? 'Admin@123',
};

/**
 * Navigate to "/" and complete the Thunder OAuth redirect. Idempotent: if
 * already signed in, returns immediately.
 */
export async function loginAsAdmin(page: Page): Promise<void> {
  await page.goto('/');
  // Thunder redirects to its hosted login page if there's no session cookie.
  if (page.url().includes('thunder')) {
    await page.getByLabel(/username|email/i).fill(TEST_USER.username);
    await page.getByLabel(/password/i).fill(TEST_USER.password);
    await page.getByRole('button', { name: /sign in|log in/i }).click();
    await page.waitForURL(/localhost:8090/);
  }
  // Confirm we reached the console.
  await page.waitForSelector('h4');
}
```

- [ ] **Step 2: Create `tests/e2e/helpers/seed.ts`**

```ts
import { createProjectFixture, createChangeFixture, ProjectFixture, ChangeFixture } from '../../helpers/factory';
import { apiPost, apiPut } from '../../helpers/api-client';

export async function seedProjectWithMainSpec(name: string): Promise<ProjectFixture> {
  // Creating a change, writing a spec, and approving gives us a main that has a spec.
  const ch: ChangeFixture = await createChangeFixture(name, 'seed-spec');
  const base = `/api/v1/organizations/${ch.orgId}/projects/${ch.projectName}/changes/${ch.changeId}`;
  await apiPut(`${base}/spec`, { content: '# Seed spec\n' });
  await apiPost(`${base}/submit-for-review`, {});
  await apiPost(`${base}/review/run`, {});
  await apiPost(`${base}/approve`, {});
  return { orgId: ch.orgId, projectName: ch.projectName, repo: ch.repo, cleanup: ch.cleanup };
}

export { createProjectFixture as seedEmptyProject };
```

- [ ] **Step 3: Update `tests/playwright.config.ts` to widen timeouts for stack startup**

```ts
import { defineConfig } from '@playwright/test';

export default defineConfig({
  testDir: './e2e',
  timeout: 90_000,
  retries: 0,
  use: {
    baseURL: process.env.E2E_BASE_URL ?? 'http://localhost:8090',
    headless: process.env.E2E_HEADED !== 'true',
    trace: 'retain-on-failure',
    screenshot: 'only-on-failure',
  },
});
```

- [ ] **Step 4: Verify TypeScript compiles**

Run: `cd tests && npx tsc --noEmit`
Expected: no errors.

- [ ] **Step 5: Commit**

```bash
git add tests/e2e/helpers/login.ts tests/e2e/helpers/seed.ts tests/playwright.config.ts
git commit -m "test(e2e): add Thunder login helper and seed-project helper"
```

---

### Task 13: E2E — Edit-on-main routes to change

**Files:**
- Delete: `tests/e2e/changes.spec.ts`.
- Create: `tests/e2e/edit-on-main.spec.ts`.

- [ ] **Step 1: Delete the skipped spec**

Run: `rm tests/e2e/changes.spec.ts`.

- [ ] **Step 2: Write the new test**

Create `tests/e2e/edit-on-main.spec.ts`:
```ts
import { test, expect } from '@playwright/test';
import { loginAsAdmin } from './helpers/login';
import { seedProjectWithMainSpec } from './helpers/seed';
import { ProjectFixture } from '../helpers/factory';

test.describe('Edit on main opens a change', () => {
  let fx: ProjectFixture | null = null;

  test.afterEach(async () => {
    fx?.cleanup();
    fx = null;
  });

  test('clicking Edit on requirements routes to /changes/{id}/spec and shows the banner', async ({ page }) => {
    fx = await seedProjectWithMainSpec('edit-main');
    await loginAsAdmin(page);

    await page.goto(`/organizations/${fx.orgId}/projects/${fx.projectName}/requirements`);
    await expect(page.locator('h4')).toContainText(/Requirements/i);

    await page.getByRole('button', { name: /^Edit$/i }).click();

    await page.waitForURL(/\/changes\/[a-f0-9]{8}\/spec$/);
    await expect(page.getByText(/Editing in change/i)).toBeVisible();
    await expect(page.getByRole('link', { name: /View overview/i })).toBeVisible();

    // Save a new spec; verify it committed on the branch (via a poll over the API).
    await page.getByRole('textbox').fill('# Edited spec\n');
    await page.getByRole('button', { name: /Save/i }).click();
    await expect(page.getByText(/saved/i)).toBeVisible();
  });
});
```

- [ ] **Step 3: Run the test**

Run: `cd tests && npx playwright test e2e/edit-on-main.spec.ts`
Expected: PASS.

- [ ] **Step 4: Commit**

```bash
git rm tests/e2e/changes.spec.ts
git add tests/e2e/edit-on-main.spec.ts
git commit -m "test(e2e): edit-on-main opens a change and shows banner"
```

---

### Task 14: E2E — Submit, approve, main updates, branch gone

**Files:**
- Create: `tests/e2e/submit-and-approve.spec.ts`.

- [ ] **Step 1: Write the test**

```ts
import { test, expect } from '@playwright/test';
import { loginAsAdmin } from './helpers/login';
import { seedProjectWithMainSpec } from './helpers/seed';
import { listRemoteBranches, readFileAtRef } from '../helpers/fixture';
import { ProjectFixture } from '../helpers/factory';

test.describe('Submit for review → Approve & merge', () => {
  let fx: ProjectFixture | null = null;

  test.afterEach(async () => {
    fx?.cleanup();
    fx = null;
  });

  test('edit → submit → approve → main updates and branch disappears', async ({ page }) => {
    fx = await seedProjectWithMainSpec('approve-flow');
    await loginAsAdmin(page);

    // Start at requirements, click Edit to open a change.
    await page.goto(`/organizations/${fx.orgId}/projects/${fx.projectName}/requirements`);
    await page.getByRole('button', { name: /^Edit$/i }).click();
    await page.waitForURL(/\/changes\/[a-f0-9]{8}\/spec$/);
    const changeSpecURL = page.url();
    const branchMatch = /\/changes\/([a-f0-9]{8})\//.exec(changeSpecURL);
    expect(branchMatch).not.toBeNull();

    // Edit + save.
    await page.getByRole('textbox').fill('# Spec v2\n');
    await page.getByRole('button', { name: /Save/i }).click();
    await expect(page.getByText(/saved/i)).toBeVisible();

    // Go to the overview page.
    await page.getByRole('link', { name: /View overview/i }).click();
    await page.waitForURL(/\/changes\/[a-f0-9]{8}$/);

    // Submit for review, wait for review_ready.
    await page.getByRole('button', { name: /Submit for review/i }).click();
    await expect(page.getByText(/review/i)).toBeVisible();

    // Approve & merge.
    await page.getByRole('button', { name: /Approve & merge/i }).click();

    // Open Changes list; the approved change is gone.
    await page.goto(`/organizations/${fx.orgId}/projects/${fx.projectName}/changes`);
    const branchesOnRemote = listRemoteBranches(fx.repo);
    const changeBranchGone = !branchesOnRemote.some((b) => b.startsWith('change/'));
    expect(changeBranchGone).toBe(true);

    // main has Spec v2.
    const mainSpec = readFileAtRef(fx.repo, 'main', '.asdlc/spec.md');
    expect(mainSpec).toContain('Spec v2');
  });
});
```

- [ ] **Step 2: Run the test**

Run: `cd tests && npx playwright test e2e/submit-and-approve.spec.ts`
Expected: PASS.

- [ ] **Step 3: Commit**

```bash
git add tests/e2e/submit-and-approve.spec.ts
git commit -m "test(e2e): submit → approve → main updates and branch disappears"
```

---

### Task 15: E2E — Parallel two-tab conflict

**Files:**
- Create: `tests/e2e/parallel-conflict.spec.ts`.

- [ ] **Step 1: Write the test**

Two browser contexts open two changes, both edit spec, the second approve hits the conflict panel.

```ts
import { test, expect } from '@playwright/test';
import { loginAsAdmin } from './helpers/login';
import { seedProjectWithMainSpec } from './helpers/seed';
import { ProjectFixture } from '../helpers/factory';

test.describe('Parallel conflict', () => {
  let fx: ProjectFixture | null = null;

  test.afterEach(async () => {
    fx?.cleanup();
    fx = null;
  });

  test('second approve surfaces the ConflictPanel', async ({ browser }) => {
    fx = await seedProjectWithMainSpec('parallel-conflict');
    const ctx1 = await browser.newContext();
    const ctx2 = await browser.newContext();
    const page1 = await ctx1.newPage();
    const page2 = await ctx2.newPage();

    await Promise.all([loginAsAdmin(page1), loginAsAdmin(page2)]);

    // Both open an edit; both get their own change branch.
    for (const p of [page1, page2]) {
      await p.goto(`/organizations/${fx.orgId}/projects/${fx.projectName}/requirements`);
      await p.getByRole('button', { name: /^Edit$/i }).click();
      await p.waitForURL(/\/changes\/[a-f0-9]{8}\/spec$/);
    }

    // Both write a different spec.
    await page1.getByRole('textbox').fill('# From tab 1\n');
    await page1.getByRole('button', { name: /Save/i }).click();
    await expect(page1.getByText(/saved/i)).toBeVisible();

    await page2.getByRole('textbox').fill('# From tab 2\n');
    await page2.getByRole('button', { name: /Save/i }).click();
    await expect(page2.getByText(/saved/i)).toBeVisible();

    // Tab 1 approves first.
    await page1.getByRole('link', { name: /View overview/i }).click();
    await page1.getByRole('button', { name: /Submit for review/i }).click();
    await page1.getByRole('button', { name: /Approve & merge/i }).click();

    // Tab 2 tries to approve.
    await page2.getByRole('link', { name: /View overview/i }).click();
    await page2.getByRole('button', { name: /Submit for review/i }).click();
    await page2.getByRole('button', { name: /Approve & merge/i }).click();

    // Conflict panel should appear.
    await expect(page2.getByText(/merge conflict/i)).toBeVisible();
    await expect(page2.getByText(/\.asdlc\/spec\.md/)).toBeVisible();

    await ctx1.close();
    await ctx2.close();
  });
});
```

- [ ] **Step 2: Run the test**

Run: `cd tests && npx playwright test e2e/parallel-conflict.spec.ts`
Expected: PASS.

- [ ] **Step 3: Commit**

```bash
git add tests/e2e/parallel-conflict.spec.ts
git commit -m "test(e2e): parallel approve surfaces ConflictPanel in the second tab"
```

---

### Task 16: E2E — Scaffolding lands on a change page

**Files:**
- Create: `tests/e2e/scaffolding.spec.ts`.

- [ ] **Step 1: Write the test**

```ts
import { test, expect } from '@playwright/test';
import { loginAsAdmin } from './helpers/login';
import { seedEmptyProject } from './helpers/seed';
import { ProjectFixture } from '../helpers/factory';

test.describe('Scaffolding', () => {
  let fx: ProjectFixture | null = null;

  test.afterEach(async () => {
    fx?.cleanup();
    fx = null;
  });

  test('a new project routes the user to /changes/{id}/spec after the initial prompt', async ({ page }) => {
    fx = await seedEmptyProject('scaffold');
    await loginAsAdmin(page);

    // Navigate to the project; it has no spec on main, so it lands on the prompt page.
    await page.goto(`/organizations/${fx.orgId}/projects/${fx.projectName}`);
    await page.waitForURL(/\/prompt|\/changes\//);

    // If on prompt, fill + submit.
    if (page.url().includes('/prompt')) {
      await page.getByRole('textbox').fill('build me a todo list');
      await page.getByRole('button', { name: /Generate|Continue/i }).click();
    }

    await page.waitForURL(/\/changes\/[a-f0-9]{8}\/spec$/, { timeout: 60_000 });
    await expect(page.getByText(/Editing in change/i)).toBeVisible();
  });
});
```

- [ ] **Step 2: Run the test**

Run: `cd tests && npx playwright test e2e/scaffolding.spec.ts`
Expected: PASS. (This spec may be slow because it triggers AI spec generation.)

- [ ] **Step 3: Commit**

```bash
git add tests/e2e/scaffolding.spec.ts
git commit -m "test(e2e): scaffolding lands on a change spec page"
```

---

## Phase D — CI Wiring

### Task 17: npm scripts, vitest config, README

Make the suite runnable as `npm run test:all` with a single prerequisite (`docker compose up -d`).

**Files:**
- Modify: `tests/package.json`.
- Create: `tests/README.md`.

- [ ] **Step 1: Rewrite `tests/package.json`**

```json
{
  "name": "asdlc-tests",
  "private": true,
  "type": "module",
  "scripts": {
    "test:api": "vitest run",
    "test:e2e": "playwright test",
    "test:e2e:headed": "playwright test --headed",
    "test:all": "npm run test:api && npm run test:e2e"
  },
  "devDependencies": {
    "@playwright/test": "^1.52.0",
    "@types/node": "^25.6.0",
    "jose": "^5.9.6",
    "typescript": "^5.9.3",
    "vitest": "^3.2.1"
  }
}
```

Note: `test:api` uses vitest's default file-discovery plus the config from Task 3 to include `api/**/*.test.ts` and `helpers/**/*.test.ts`.

- [ ] **Step 2: Create `tests/README.md`**

```markdown
# ASDLC Tests

End-to-end verification for the branch-based lifecycle.

## Prerequisites

- Docker + `docker compose` (macOS: Colima or Docker Desktop).
- The full stack must be running:
  ```bash
  cd deployments && docker compose up -d
  ```
- For E2E tests, `asdlc-agent-service` and `remote-worker` must ALSO be
  running on the host (they aren't in docker-compose because they need the
  `claude` CLI OAuth session from the macOS keychain):
  ```bash
  cd asdlc-agent-service && npx tsx src/index.ts &
  cd remote-worker     && npx tsx src/index.ts &
  ```

## Running

```bash
cd tests
npm install
npm run test:api          # vitest — 7 API integration scenarios + helper smokes
npm run test:e2e          # Playwright — 4 E2E scenarios
npm run test:all          # both
```

## Environment

- `API_BASE_URL` — default `http://localhost:9090`.
- `GIT_SERVICE_URL` — default `http://localhost:3300`.
- `E2E_BASE_URL` — default `http://localhost:8090`.
- `E2E_USERNAME` / `E2E_PASSWORD` — Thunder credentials; default to the
  admin account from `deployments/scripts/setup.sh`.

## How it works

- **Bare-repo fixtures:** `tests/helpers/fixture.ts` creates a throwaway git
  bare repo on disk under `~/.asdlc/test-fixtures/<name>.git` and mounts it
  into both `git-service` and `asdlc-api` as `/data/test-fixtures/...`. Tests
  use `file://` URLs; `git-service`'s `TEST_MODE` accepts them.
- **JWT:** Tests mint an unsigned HS256 token with a `client_id` claim. The
  BFF parses it unverified (gateway verifies in prod).
- **Isolation:** Each test creates its own fixture and cleans up in
  `afterEach`. `tests/helpers/db-reset.ts` is still available for suite-level
  cleanup if you need it.
```

- [ ] **Step 3: Verify everything ties together**

Run a full dry check:
```bash
cd tests && npx tsc --noEmit
```
Expected: no errors.

- [ ] **Step 4: Commit**

```bash
git add tests/package.json tests/README.md
git commit -m "test: wire up npm scripts and document the suite"
```

---

## Self-Review

**Spec §10.1 coverage** (API integration — 7 scenarios):

| Scenario | Task |
|---|---|
| Create change → branch exists; change.json correct | Task 5 ✅ |
| PUT spec/design/plan on change → file committed+pushed | Task 6 ✅ |
| Discard → branch gone; tasks marked discarded | Task 7 ✅ |
| Approve → main contains change; branch gone | Task 8 ✅ |
| Parallel changes → two branches coexist | Task 9 ✅ |
| Conflict at approve → merge blocked; conflicts list correct | Task 10 ✅ |
| Scaffolding → new project ends on a change branch | Task 11 ✅ |

**Spec §10.2 coverage** (E2E — 4 scenarios):

| Scenario | Task |
|---|---|
| Edit-on-main → URL transitions; banner; save=commit | Task 13 ✅ |
| Submit for review → Approve & merge → main updates; branch disappears | Task 14 ✅ |
| Parallel two-tab → second submit surfaces conflict | Task 15 ✅ |
| Scaffolding → initial prompt lands on change page | Task 16 ✅ |

**Spec §10.3 coverage** (requirements docs) — already done in Plan 4.

**Spec §8.2 note** — build-check / AI review / lint review pipeline additions are explicitly deferred per the plan header and spec §8.2 ("whatever each component defines; requires component-metadata support").

**Placeholder scan:** every task has concrete code and an exact run command. No TBDs, no "add error handling", no "similar to Task N".

**Type consistency:** `ChangeFixture` / `ProjectFixture` / `BareRepo` are defined once (`tests/helpers/factory.ts` + `fixture.ts`) and used verbatim everywhere else. `testToken()`, `apiGet/apiPost/apiPut/apiPatch/apiDelete` all share one implementation. API routes match `asdlc-service/api/*_routes.go` exactly (verified by reading `change_routes.go`, `spec_routes.go`, etc.).

---

## Execution Handoff

Plan complete and saved to `docs/superpowers/plans/2026-04-20-plan-5-branch-based-lifecycle-tests.md`. Two execution options:

1. **Subagent-Driven (recommended)** — I dispatch a fresh subagent per task, review between tasks, fast iteration.
2. **Inline Execution** — Execute tasks in this session using executing-plans, batch execution with checkpoints.

Which approach?
