#!/bin/bash
# Tear down the full v1 dev environment.
# Usage: cd deployments && bash scripts/teardown.sh
#
# Equivalent of agent-manager/deployments/scripts/teardown.sh — stops the
# compose stack (with volumes) then deletes the k3d cluster. Does NOT touch
# Colima / Docker themselves.
set -e

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "$SCRIPT_DIR/env.sh"
source "$SCRIPT_DIR/utils.sh"

DEPLOY_DIR="$SCRIPT_DIR/.."
COMPOSE_FILE="$DEPLOY_DIR/docker-compose.yml"

echo "=== Tearing Down App Factory v1 Environment ==="

# ── Stop OpenBao port-forward (started by start.sh as a fallback) ────────
echo ""
echo "1️⃣  Stop OpenBao port-forward"
if pgrep -f "port-forward.*openbao.*8200" > /dev/null 2>&1; then
    pkill -f "port-forward.*openbao.*8200" 2>/dev/null || true
    echo "✅ port-forward stopped"
else
    echo "⏭️  no openbao port-forward running"
fi

# ── Compose stack ────────────────────────────────────────────────────────
echo ""
echo "2️⃣  Stop Docker Compose services (with volumes)"
if [ -f "$COMPOSE_FILE" ]; then
    docker compose -f "$COMPOSE_FILE" down -v --remove-orphans
    echo "✅ Compose stack down"
else
    echo "⚠️  docker-compose.yml not found at $COMPOSE_FILE — skipping"
fi

# ── k3d cluster ──────────────────────────────────────────────────────────
echo ""
echo "3️⃣  Delete k3d cluster"
if command -v k3d &>/dev/null; then
    if k3d cluster list 2>/dev/null | grep -q "${CLUSTER_NAME}"; then
        k3d cluster delete "${CLUSTER_NAME}"
        echo "✅ Cluster deleted"
    else
        echo "⏭️  Cluster '${CLUSTER_NAME}' not found"
    fi
else
    echo "⚠️  k3d not installed — skipping"
fi

# ── Generated artifacts the operator typically wants gone ───────────────
echo ""
echo "4️⃣  Clean generated artifacts"
rm -f "$DEPLOY_DIR/.kube/config" 2>/dev/null && echo "   removed deployments/.kube/config" || true
rm -rf "$DEPLOY_DIR/.kube" 2>/dev/null || true

# git-service stores cloned workspaces at $HOME/.asdlc/repos (host bind
# mount, see docker-compose.yml `volumes:` on git-service). docker compose
# down -v wipes named volumes but NOT bind mounts, so without this the
# next `setup.sh` runs into "destination path already exists" when
# git-service tries to clone a freshly-created repo into a stale workspace.
REPOS_DIR="${HOME}/.asdlc/repos"
if [ -d "$REPOS_DIR" ]; then
    rm -rf "$REPOS_DIR" 2>/dev/null && echo "   removed git-service workspaces at $REPOS_DIR" || true
fi

echo "   keeping deployments/.env, deployments/keys/, deployments/github-app-private-key.pem"

echo ""
echo "✅ Teardown complete!"
echo "   Re-create with:  cd deployments && bash scripts/setup.sh && bash scripts/start.sh"
