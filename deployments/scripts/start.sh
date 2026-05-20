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

# Pre-flight — mirrors agent-manager/deployments/scripts/setup-platform.sh.
# These tools are required for the docker compose build + run flow; failing
# fast here beats cryptic errors from `docker compose up` minutes later.
if ! docker info &>/dev/null; then
    echo "❌ Docker is not running. Start Docker / Colima first."
    exit 1
fi
if ! docker compose version &>/dev/null; then
    echo "❌ Docker Compose plugin not installed. Install via Docker Desktop or 'brew install docker-compose'."
    exit 1
fi
if ! docker buildx version &>/dev/null; then
    echo "❌ Docker Buildx plugin not installed. Install via Docker Desktop or 'brew install docker-buildx'."
    exit 1
fi
if [ ! -f "$DEPLOY_DIR/docker-compose.yml" ]; then
    echo "❌ docker-compose.yml missing at $DEPLOY_DIR/docker-compose.yml"
    exit 1
fi
echo "✅ Pre-flight ok (docker + compose + buildx + docker-compose.yml)"

# Load specific env keys needed before `docker compose up` runs envsubst.
# We extract keys individually rather than blanket-sourcing because .env
# values can contain spaces (e.g. VITE_THUNDER_SCOPES="openid profile email")
# which break `set -a; source`.
_load_env_key() {
    [ -n "${!1}" ] && return 0
    [ -f "$DEPLOY_DIR/.env" ] || return 0
    local val
    val=$(grep "^$1=" "$DEPLOY_DIR/.env" | head -1 | cut -d= -f2-)
    val="${val#\"}" ; val="${val%\"}"
    val="${val#\'}" ; val="${val%\'}"
    [ -n "$val" ] && export "$1=$val" || true
}
_load_env_key ANTHROPIC_API_KEY
_load_env_key LOG_LEVEL
_load_env_key PUBLIC_THUNDER_URL
_load_env_key PUBLIC_CONSOLE_URL

# 0. Refresh k3d node DNS (8.8.8.8 fallback for image pulls). k3d's
#    native host.k3d.internal mapping + OC's CoreDNS rewrite handle the
#    rest — see fix_node_dns comment in utils.sh.
echo ""
echo "🔧 Refreshing k3d node DNS..."
if kubectl cluster-info --context "${CLUSTER_CONTEXT}" --request-timeout=5s &>/dev/null; then
    fix_node_dns
else
    echo "⚠️  k3d cluster not accessible — skipping DNS refresh (run setup.sh if cluster is missing)"
fi

# 1. OpenBao port bridge — git-service (in docker-compose) needs to reach
#    the k3d-hosted OpenBao at host.docker.internal:8200. Fresh clusters
#    created from k3d-local-config.yaml have the mapping baked in (8200:30820
#    → loadbalancer → openbao-0); pre-existing clusters don't, so we fall
#    back to a kubectl port-forward.
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

# 2. GitHub App private key placeholder — docker-compose mounts the file
#    into git-service. If the operator hasn't dropped a real key, we touch
#    a placeholder so the volume mount succeeds; the seed skips silently
#    and App-mode connect surfaces the error at the connect endpoint.
if [ ! -f "$DEPLOY_DIR/github-app-private-key.pem" ]; then
    echo ""
    echo "🔑 Creating placeholder for GitHub App private key..."
    echo "   Drop a real key at $DEPLOY_DIR/github-app-private-key.pem to enable App-mode connect."
    touch "$DEPLOY_DIR/github-app-private-key.pem"
fi

# 3. Seed an internal kubeconfig for git-service. The host kubeconfig points
#    at https://127.0.0.1:6550 which is unreachable from inside a container.
#    The k3d server container is on the `k3d-openchoreo` docker network as
#    `k3d-openchoreo-server-0`, so rewrite the server URL accordingly.
#    git-service mounts this file at /app/.kube/config (read-only). This
#    mirrors agent-manager's docker-compose pattern (KUBECONFIG env var).
echo ""
echo "🔑 Seeding internal kubeconfig for git-service..."
INTERNAL_KUBE_DIR="$DEPLOY_DIR/.kube"
INTERNAL_KUBECONFIG="$INTERNAL_KUBE_DIR/config"
mkdir -p "$INTERNAL_KUBE_DIR"
if kubectl cluster-info --context "${CLUSTER_CONTEXT}" --request-timeout=5s &>/dev/null; then
    # Use k3d's "internal" kubeconfig which sets the server to the in-network
    # hostname. If unavailable, fall back to a manual rewrite of the host
    # kubeconfig.
    if k3d kubeconfig get "${CLUSTER_NAME}" --internal > "$INTERNAL_KUBECONFIG" 2>/dev/null; then
        echo "✅ Wrote internal kubeconfig (k3d --internal) → $INTERNAL_KUBECONFIG"
    else
        kubectl config view --raw --minify --context "${CLUSTER_CONTEXT}" > "$INTERNAL_KUBECONFIG"
        # k3d server's in-network port is always 6443 (not the host-mapped 6550)
        # and the container hostname follows the k3d-<cluster>-server-0 pattern.
        sed -i.bak -E "s|server: https?://[^[:space:]]+|server: https://k3d-${CLUSTER_NAME}-server-0:6443|" "$INTERNAL_KUBECONFIG"
        rm -f "$INTERNAL_KUBECONFIG.bak"
        echo "✅ Wrote rewritten kubeconfig → $INTERNAL_KUBECONFIG"
    fi
    chmod 600 "$INTERNAL_KUBECONFIG"
else
    echo "⚠️  Cluster unreachable — leaving existing $INTERNAL_KUBECONFIG (may be stale)"
    [ -f "$INTERNAL_KUBECONFIG" ] || touch "$INTERNAL_KUBECONFIG"
fi

# 4. BFF Task JWT signing key — bind-mounted into asdlc-api as
#    /app/keys/task-signing.pem (docker-compose volume). The BFF reads
#    the PEM from BFF_TASK_SIGNING_KEY_PATH; mounting beats env-passing
#    a multi-line value through compose's `${VAR}` substitution.
TASK_KEY_PATH="$DEPLOY_DIR/keys/task-signing.pem"
if [ ! -f "$TASK_KEY_PATH" ]; then
    echo "⚠️  BFF Task JWT signing key missing at $TASK_KEY_PATH — coding-agent dispatch will fail."
    echo "   Run setup-asdlc.sh to generate it."
fi

# 5. Sync public URLs (.env → cluster). Touches Thunder ConfigMap, OIDC
#    issuer, HTTPRoute, redirect_uris. Idempotent — fast no-op if nothing
#    changed.
echo ""
if kubectl cluster-info --context "${CLUSTER_CONTEXT}" --request-timeout=5s &>/dev/null; then
    load_public_urls "$DEPLOY_DIR/.env"
    apply_public_urls_to_cluster
else
    echo "⚠️  k3d cluster not accessible — skipping public-URL sync"
fi

# 6. Bring up the compose stack. The coding-agent runner is no longer a
#    long-lived service — it's dispatched as a one-shot pod via the
#    `app-factory-coding-agent` ClusterWorkflow in the cluster (installed
#    by setup-asdlc.sh). No host-mode toggle here.
cd "$DEPLOY_DIR"
echo ""
echo "🐳 Starting Docker services..."
docker compose up --build -d
echo "✅ Docker services started"

echo ""
echo "============================================"
echo "  ✅ All services running!"
echo "============================================"
echo ""
echo "  Console:          http://localhost:8090"
echo "  API:              http://localhost:9090"
echo "  Git Service:      http://localhost:3300"
echo "  Agents Service:   http://localhost:3400"
echo ""
echo "  Coding-agent:     dispatched as a one-shot pod via the"
echo "                    'app-factory-coding-agent' ClusterWorkflow"
echo "                    (Workflow Plane namespace 'workflows-default')."
echo ""
echo "  Login: admin / admin (default Thunder admin, in the 'Administrators' group)"
echo ""
echo "  To stop: cd deployments && bash scripts/stop.sh"
