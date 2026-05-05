#!/bin/bash
# Start all ASDLC services.
# Usage: cd deployments && bash scripts/start.sh
set -e

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT_DIR="$(cd "$SCRIPT_DIR/../.." && pwd)"
DEPLOY_DIR="$SCRIPT_DIR/.."

# shellcheck source=env.sh
source "$SCRIPT_DIR/env.sh"
# shellcheck source=utils.sh
source "$SCRIPT_DIR/utils.sh"

echo "=== Starting ASDLC Platform ==="

# Load specific env vars needed by host services (agent service, remote-worker).
# We extract keys individually rather than blanket-sourcing because .env values
# can contain spaces (e.g. VITE_THUNDER_SCOPES=openid profile email) which
# break set -a / source.
_load_env_key() {
    # Prefer existing shell env var; only fall back to .env if not set
    [ -n "${!1}" ] && return 0
    [ -f "$DEPLOY_DIR/.env" ] || return 0
    local val
    val=$(grep "^$1=" "$DEPLOY_DIR/.env" | head -1 | cut -d= -f2-)
    # Strip surrounding quotes (single or double)
    val="${val#\"}" ; val="${val%\"}"
    val="${val#\'}" ; val="${val%\'}"
    [ -n "$val" ] && export "$1=$val" || true
}
_load_env_key ANTHROPIC_API_KEY
_load_env_key LOG_LEVEL
# Load PUBLIC_THUNDER_URL / PUBLIC_CONSOLE_URL — also exported for docker compose.
_load_env_key PUBLIC_THUNDER_URL
_load_env_key PUBLIC_CONSOLE_URL
# Cap on concurrent Claude Agent SDK queries the remote-worker will accept.
# Above this, /dispatch returns 429 so the BFF can backpressure. Default 8;
# override in .env if your dev machine can sustain more (or fewer).
_load_env_key REMOTE_WORKER_MAX_CONCURRENT_TASKS
# How remote-worker runs: container (default) or host. See bottom of script.
_load_env_key REMOTE_WORKER_MODE
REMOTE_WORKER_MODE="${REMOTE_WORKER_MODE:-container}"

# Refresh k3d DNS — after any Docker/Colima restart the k3d loadbalancer may have
# a new IP. Re-patching ensures host.k3d.internal resolves correctly inside pods,
# which is required by the generate-workload-cr workflow step.
echo ""
echo "🔧 Refreshing k3d DNS..."
if kubectl cluster-info --context "${CLUSTER_CONTEXT}" --request-timeout=5s &>/dev/null; then
    fix_node_dns
    patch_coredns_host_k3d_internal
else
    echo "⚠️  k3d cluster not accessible — skipping DNS refresh (run setup.sh if cluster is missing)"
fi

# 0. OpenBao port bridge — git-service (in docker-compose) needs to reach the
# k3d-hosted OpenBao at host.docker.internal:8200. Fresh clusters created from
# k3d-local-config.yaml have the mapping baked in (8200:30820 → loadbalancer);
# pre-existing clusters don't, so we fall back to a kubectl port-forward.
# See docs/design/github-integration-phase2.md §F-1.
echo ""
echo "🔐 Ensuring OpenBao reachable on :8200..."
if curl -s --max-time 1 http://localhost:8200/v1/sys/health > /dev/null 2>&1; then
    echo "   OpenBao already reachable on :8200"
elif kubectl cluster-info --context "${CLUSTER_CONTEXT}" --request-timeout=5s &>/dev/null; then
    if pgrep -f "port-forward.*openbao.*8200" > /dev/null 2>&1; then
        echo "   OpenBao port-forward already running"
    else
        kubectl port-forward --context "${CLUSTER_CONTEXT}" -n openbao svc/openbao 8200:8200 \
            > /tmp/asdlc-openbao-portfwd.log 2>&1 &
        echo "   port-forward PID: $! (logs: /tmp/asdlc-openbao-portfwd.log)"
        # Wait for it to come up
        for i in 1 2 3 4 5 6 7 8 9 10; do
            if curl -s --max-time 1 http://localhost:8200/v1/sys/health > /dev/null 2>&1; then
                echo "✅ OpenBao reachable on :8200 (via port-forward)"
                break
            fi
            sleep 1
        done
    fi
else
    echo "⚠️  k3d cluster not accessible — git-service will fail OpenBao readiness"
fi

# 0.5. GitHub App private key placeholder — docker-compose mounts
# deployments/github-app-private-key.pem into git-service. If the operator
# hasn't dropped a real key yet, we touch a placeholder so the volume mount
# succeeds; the seed will skip silently and App-mode connect surfaces the
# specific error at the connect endpoint. The .gitignore excludes *.pem
# so the operator's real key is never committed.
if [ ! -f "$DEPLOY_DIR/github-app-private-key.pem" ]; then
    echo ""
    echo "🔑 Creating placeholder for GitHub App private key (deployments/github-app-private-key.pem)..."
    echo "   Drop the real key here to enable App-mode connect."
    touch "$DEPLOY_DIR/github-app-private-key.pem"
fi

# 0.7. Sync public URLs (.env → cluster). Touches Thunder ConfigMap, OIDC issuer,
#      HTTPRoute, redirect_uris. Idempotent — fast no-op if nothing changed.
echo ""
if kubectl cluster-info --context "${CLUSTER_CONTEXT}" --request-timeout=5s &>/dev/null; then
    load_public_urls "$DEPLOY_DIR/.env"
    apply_public_urls_to_cluster
else
    echo "⚠️  k3d cluster not accessible — skipping public-URL sync"
fi

# 1. Docker Compose. In host mode we scale remote-worker to 0 so the
#    container service is excluded; the host process below replaces it.
cd "$DEPLOY_DIR"
if [ "$REMOTE_WORKER_MODE" = "host" ]; then
    # Override compose defaults so the BFF reaches the host worker via
    # host-gateway, and so the host-side agent reaches git-service via the
    # published localhost port.
    export REMOTE_WORKER_BASE_URL="http://host.docker.internal:3200"
    export GIT_SERVICE_HOST_URL="http://localhost:3300"
    echo ""
    echo "🐳 Starting Docker services (remote-worker scaled to 0 — host mode)..."
    docker compose up --build -d --scale remote-worker=0
    echo "✅ Docker services started"

    # 2. Remote-worker on host (uses Claude Code OAuth session from keychain).
    echo ""
    echo "📦 Starting remote-worker on host..."
    if lsof -i :3200 > /dev/null 2>&1; then
        echo "   Remote-worker already running on :3200"
    else
        cd "$ROOT_DIR/remote-worker"
        if [ ! -d node_modules ]; then
            echo "   Installing dependencies (first run only, ~30s)..."
            npm install > /tmp/asdlc-remote-worker-install.log 2>&1
        fi
        npx tsx src/index.ts --service=remote-worker > /tmp/asdlc-remote-worker.log 2>&1 &
        echo "   PID: $! (logs: /tmp/asdlc-remote-worker.log)"
        rw_ready=0
        for i in 1 2 3 4 5 6 7 8 9 10 11 12 13 14 15; do
            if curl -s http://localhost:3200/health > /dev/null 2>&1; then
                echo "✅ Remote-worker started on :3200"
                rw_ready=1
                break
            fi
            sleep 1
        done
        if [ "$rw_ready" != "1" ]; then
            echo "❌ Remote-worker failed to start — check /tmp/asdlc-remote-worker.log"
        fi
    fi
else
    # Container mode — explicitly override the values .env carries (which
    # are tuned for host mode) so the in-container agent reaches git-service
    # over the asdlc bridge network.
    export REMOTE_WORKER_BASE_URL="http://remote-worker:3200"
    export GIT_SERVICE_HOST_URL="http://git-service:3300"
    echo ""
    echo "🐳 Starting Docker services (remote-worker in container)..."
    docker compose up --build -d
    echo "✅ Docker services started"

    echo ""
    echo "📦 Waiting for remote-worker container to be ready..."
    rw_ready=0
    for i in 1 2 3 4 5 6 7 8 9 10 11 12 13 14 15; do
        if curl -s http://localhost:3200/health > /dev/null 2>&1; then
            echo "✅ Remote-worker container ready on :3200"
            rw_ready=1
            break
        fi
        sleep 1
    done
    if [ "$rw_ready" != "1" ]; then
        echo "❌ Remote-worker container did not become ready — check 'docker logs asdlc-remote-worker'"
    fi
fi

echo ""
echo "============================================"
echo "  ✅ All services running!"
echo "============================================"
echo ""
echo "  Console:          http://localhost:8090"
echo "  API:              http://localhost:9090"
echo "  Remote Worker:    http://localhost:3200"
echo "  Git Service:      http://localhost:3300"
echo "  Agents Service:   http://localhost:3400"
echo "  MCP Endpoint:     http://localhost:9090/mcp/"
echo ""
echo "  Login: admin@openchoreo.dev / Admin@123"
echo ""
echo "  To stop: cd deployments && bash scripts/stop.sh"
