---
name: go
description: How to build a Go service on the platform — pinned golang:1.25-alpine builder (the build pod runs with GOTOOLCHAIN=local), pure-Go modernc.org/sqlite driver (CGO times out under the build pod's CPU throttle), suggested layout, port 9090, GET /health liveness, multi-stage Dockerfile → slim runtime, embedded SQLite for per-user data inside the owning service. Apply to every Go component.
metadata:
  asdlc.version: "1"
---

# Go

## What this skill does

The platform's coding-agent + build pipeline have specific constraints
on the Go toolchain (no network-installed newer Go), on CGO (build pods
are CPU-throttled), and on persistence (embedded only — there is no
external Postgres for v1). This skill tells the agent to pin its
Dockerfile base image, use the pure-Go SQLite driver, and follow a
production-shaped project layout.

## Platform facts

- The runner sandbox ships `go` and `node` + `npm` only. No Python, no
  Rust, no custom toolchains.
- The build pod runs with `GOTOOLCHAIN=local` — it will NOT auto-
  download a newer Go toolchain. Pinning an older base image
  (`golang:1.23-alpine` etc.) when `go.mod` requires a newer toolchain
  causes `go mod download` to fail with `go.mod requires go >= X.Y`
  even when the local `go build` verification succeeded.
- The required builder base image is `golang:1.25-alpine`. Using any
  other version is a HARD ERROR at build time. This is enforced by the
  build pod, not by code review — pick the right image up front.
- CGO is not available at build time in any reasonable wall-clock
  budget. The CPU-throttled build pod compiling the SQLite amalgamation
  (`sqlite3-binding.c`, ~3 MB of C) takes 10–20 minutes and frequently
  times out.
- The pure-Go `modernc.org/sqlite` driver compiles in ~30 seconds and
  has the same `database/sql` interface. Use it everywhere.
- Default backend port is 9090.
- Every service exposes `GET /health` returning 200 (the platform's
  readiness probe hits this).
- `/health` is exempt from auth (the gateway lets it through).

## Recommended practice

### Architect

- Default new backend services to Go + `net/http` on port 9090.
- Prefer fewer components: a single Go service owns its API + its
  embedded SQLite database. Do NOT spin off a separate `storage` /
  `database` / `persistence` component.
- Do NOT create scheduled-task / cronjob components in Go (or anywhere
  else). Fold periodic work into the owning service as a background
  goroutine kicked off at startup. Call this out in
  `componentAgentInstructions`.
- For routing, suggest `net/http` (standard library) by default. For
  larger services with grouped routes or middleware chains, `chi`
  (`github.com/go-chi/chi/v5`) is a fine choice. Avoid framework-heavy
  options (Gin, Echo, Fiber) for v1 — they pull large dep trees and
  add little for the platform's typical 5–20-endpoint services.
- Suggest the embedded `modernc.org/sqlite` driver in
  `componentAgentInstructions` when the component owns per-user data
  (e.g. todos, drafts, notes, profile-extension data). Include a
  short note: "Use `modernc.org/sqlite` (pure-Go); driver name is
  `\"sqlite\"`. Store the DB under `/data/<name>.db`."

### Tech-lead — issue body bullets

For every Go service task, include this Scope bullet (HARD requirement):

- "Dockerfile builder base image: Use `FROM golang:1.25-alpine AS builder`
  in the component's `Dockerfile`. The build pod runs with
  `GOTOOLCHAIN=local` and will NOT auto-download a newer Go toolchain
  — picking an older base image (`golang:1.23-alpine` etc.) causes
  `go mod download` to fail with `go.mod requires go >= X.Y` at build
  time even when the local `go build` verification succeeded."

For every Go service task whose component is expected to persist
per-user data, include this Scope bullet:

- "Persistence: use the pure-Go `modernc.org/sqlite` driver (import
  `_ \"modernc.org/sqlite\"`; `sql.Open(\"sqlite\", ...)` — note the
  driver name is `\"sqlite\"`, not `\"sqlite3\"`). Do NOT use
  `mattn/go-sqlite3` — its CGO compilation step times out under the
  build pod's CPU throttle."

For every Go task, include this Acceptance criteria bullet:

- "Local `go build -o /dev/null ./...` exits 0 and the committed
  `go.sum` matches a fresh `go mod tidy` run."

### Coding agent — implementation

Layout (production-shaped, ~5–20 endpoints):

```
<app-path>/
├── go.mod               # module path matches the app folder name
├── go.sum
├── main.go              # entrypoint — for small services, all in one file
├── cmd/                 # optional — multiple binaries; usually omitted
├── internal/
│   ├── handlers/        # http handlers, one file per resource
│   ├── store/           # database access (SQLite)
│   ├── models/          # request/response/domain types
│   └── middleware/      # any cross-cutting middleware (rare — see api-management)
├── Dockerfile
└── workload.yaml
```

`go.mod` — pick `go 1.25` (or older if you genuinely don't need newer
features). Module path = app folder name unless the spec dictates
otherwise.

```go
module example.com/<component-name>

go 1.25
```

Routing — `net/http` for small services, `chi` if it earns its keep:

```go
mux := http.NewServeMux()
mux.HandleFunc("GET /health", healthHandler)
mux.HandleFunc("GET /todos", listTodos)
mux.HandleFunc("POST /todos", createTodo)
mux.HandleFunc("PATCH /todos/{id}", updateTodo)
// ... etc.

log.Printf("listening on :9090")
log.Fatal(http.ListenAndServe(":9090", mux))
```

SQLite — pure-Go driver, use literal `"sqlite"` (not `"sqlite3"`):

```go
import (
    "database/sql"
    _ "modernc.org/sqlite"
)

db, err := sql.Open("sqlite", "/data/todos.db")
if err != nil {
    log.Fatal(err)
}

// Initialise schema. Use IF NOT EXISTS so re-deploys are idempotent.
if _, err := db.Exec(`
    CREATE TABLE IF NOT EXISTS todos (
        id        INTEGER PRIMARY KEY AUTOINCREMENT,
        user_id   TEXT NOT NULL,
        title     TEXT NOT NULL,
        done      INTEGER NOT NULL DEFAULT 0,
        created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
    );
    CREATE INDEX IF NOT EXISTS idx_todos_user_id ON todos (user_id);
`); err != nil {
    log.Fatal(err)
}
```

Performance is comparable to `mattn` for typical CRUD workloads; the
only loss is FTS3/FTS5 which the platform's todo-shaped services don't
need.

`/health` handler (no auth, no DB ping required — keep it cheap):

```go
func healthHandler(w http.ResponseWriter, r *http.Request) {
    w.WriteHeader(http.StatusOK)
    w.Write([]byte("OK"))
}
```

Errors — `application/problem+json` (see `api-management` for the full
convention):

```go
func problemJSON(w http.ResponseWriter, status int, title, detail string) {
    w.Header().Set("Content-Type", "application/problem+json")
    w.WriteHeader(status)
    json.NewEncoder(w).Encode(map[string]any{
        "type":   "about:blank",
        "title":  title,
        "status": status,
        "detail": detail,
    })
}
```

`Dockerfile` — multi-stage, pinned builder, slim runtime:

```dockerfile
FROM golang:1.25-alpine AS builder
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -ldflags='-s -w' -o /out/app ./

FROM alpine:3.20
RUN apk add --no-cache ca-certificates
COPY --from=builder /out/app /app
RUN mkdir -p /data
EXPOSE 9090
ENTRYPOINT ["/app"]
```

`workload.yaml` for a Go service:

```yaml
apiVersion: openchoreo.dev/v1alpha1
metadata:
  name: <service-component-name>

endpoints:
  - name: http
    type: HTTP
    port: 9090
    basePath: /
    visibility:
      - external
```

### Build verification

Run BEFORE opening the PR — this catches lockfile hash mismatches,
missing imports, syntax errors, type errors:

```bash
cd <app-path>
go mod tidy 2>&1 | tail -20   # regenerate go.sum from real checksums
go build -o /dev/null ./...   # compile everything; fails on any error
```

After `go mod tidy` succeeds, COMMIT the updated `go.sum` along with
your source. Without it, the build pipeline will fail on the next
`go mod download` step because lockfile entries are missing.

### Don't

- ❌ Use `mattn/go-sqlite3` (CGO times out).
- ❌ Use the driver name `"sqlite3"` — for `modernc.org/sqlite` it's
  `"sqlite"`.
- ❌ Pin `golang:1.23-alpine` or any other Go base image — the build
  pod's `GOTOOLCHAIN=local` rejects toolchain auto-upgrades.
- ❌ Spin off a separate `db` / `storage` component — every service
  owns its own SQLite.
- ❌ Use port 8080, 3000, or any other port — the platform expects 9090.
- ❌ Skip `go mod tidy` before committing — hand-written `go.sum`
  hashes cause `checksum mismatch ... SECURITY ERROR` at build time.
- ❌ Use CGO. Set `CGO_ENABLED=0` explicitly or use the pure-Go driver
  to make sure you're not accidentally linking C code.

### Common pitfalls

| Symptom | Cause | Fix |
|---|---|---|
| Build fails with `go.mod requires go >= 1.25` | Dockerfile pinned older Go | Use `FROM golang:1.25-alpine AS builder`. |
| Build times out at the `mattn/go-sqlite3` step | CGO compilation under throttle | Switch to `modernc.org/sqlite`. |
| `sql.Open` returns `unknown driver "sqlite3"` | Used `mattn` driver name with `modernc` import | Use `sql.Open("sqlite", ...)`. |
| `checksum mismatch ... SECURITY ERROR` at build | `go.sum` is stale or hand-edited | `go mod tidy` locally; commit the result. |
| Pod won't start; logs show "panic: listen tcp :8080" | Used wrong port | Use port 9090. |
