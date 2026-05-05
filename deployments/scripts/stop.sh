#!/bin/bash
# Stop all ASDLC services.
# Usage: cd deployments && bash scripts/stop.sh
set -e

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
DEPLOY_DIR="$SCRIPT_DIR/.."

echo "=== Stopping ASDLC Platform ==="

# Read REMOTE_WORKER_MODE from .env (default: container) so we know whether
# to pkill a host process. In container mode, docker compose down handles it.
REMOTE_WORKER_MODE="container"
if [ -f "$DEPLOY_DIR/.env" ]; then
    val=$(grep "^REMOTE_WORKER_MODE=" "$DEPLOY_DIR/.env" | head -1 | cut -d= -f2-)
    val="${val#\"}" ; val="${val%\"}" ; val="${val#\'}" ; val="${val%\'}"
    [ -n "$val" ] && REMOTE_WORKER_MODE="$val"
fi

# Stop host services
echo "🛑 Stopping agent service..."
pkill -f "service=agent-service" 2>/dev/null && echo "   Stopped" || echo "   Not running"

if [ "$REMOTE_WORKER_MODE" = "host" ]; then
    echo "🛑 Stopping remote-worker (host)..."
    pkill -f "service=remote-worker" 2>/dev/null && echo "   Stopped" || echo "   Not running"
fi

echo "🛑 Stopping OpenBao port-forward..."
pkill -f "port-forward.*openbao.*8200" 2>/dev/null && echo "   Stopped" || echo "   Not running"

# Stop Docker services
echo "🐳 Stopping Docker services..."
cd "$DEPLOY_DIR"
docker compose down

echo ""
echo "✅ All services stopped"
echo "   (k3d cluster is still running — use 'k3d cluster delete openchoreo' to remove)"
