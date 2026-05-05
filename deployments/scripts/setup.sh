#!/bin/bash
set -e
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
cd "$SCRIPT_DIR"

echo "============================================"
echo "  ASDLC Platform — Full Setup"
echo "============================================"
echo ""
echo "This script sets up everything needed to run ASDLC:"
echo "  1. k3d cluster"
echo "  2. Prerequisites (cert-manager, Kgateway, etc.)"
echo "  3. OpenChoreo (Control Plane, Data Plane, Workflow Plane, Thunder)"
echo "  4. ASDLC-specific config (Thunder OAuth2 client, .env file)"
echo ""

bash "$SCRIPT_DIR/setup-k3d.sh"
echo ""

bash "$SCRIPT_DIR/setup-prerequisites.sh"
echo ""

bash "$SCRIPT_DIR/setup-openchoreo.sh"
echo ""

bash "$SCRIPT_DIR/setup-asdlc.sh"
echo ""

echo "============================================"
echo "  ✅ Setup Complete!"
echo "============================================"
echo ""
echo "  Start ASDLC:  cd deployments && bash scripts/start.sh"
echo "  Stop ASDLC:   cd deployments && bash scripts/stop.sh"
echo "  Console:       http://localhost:8090"
echo "  Login:         admin@openchoreo.dev / Admin@123"
echo ""
