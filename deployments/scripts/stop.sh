#!/bin/bash
# Stop all ASDLC services.
# Usage: cd deployments && bash scripts/stop.sh
set -e

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
DEPLOY_DIR="$SCRIPT_DIR/.."

echo "=== Stopping ASDLC Platform ==="

echo "🛑 Stopping OpenBao port-forward..."
pkill -f "port-forward.*openbao.*8200" 2>/dev/null && echo "   Stopped" || echo "   Not running"

echo "🐳 Stopping Docker services..."
cd "$DEPLOY_DIR"
docker compose down

echo ""
echo "✅ All services stopped"
echo "   (k3d cluster is still running — use 'k3d cluster delete openchoreo' to remove)"
