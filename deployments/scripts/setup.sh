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
echo "  2. Prerequisites (cert-manager, Kgateway, ESO, OpenBao)"
echo "  3. OpenChoreo (Control Plane, Data Plane, Workflow Plane, Thunder)"
echo "  4. Observability Plane (Observer + OpenSearch + Fluent Bit — for"
echo "     in-UI Live Progress streaming during coding-agent + build runs)"
echo "  5. ASDLC-specific config (ClusterWorkflows, ComponentTypes,"
echo "     Environment, AuthzRoleBindings, .env file)"
echo ""

bash "$SCRIPT_DIR/setup-k3d.sh"
echo ""

bash "$SCRIPT_DIR/setup-prerequisites.sh"
echo ""

bash "$SCRIPT_DIR/setup-openchoreo.sh"
echo ""

bash "$SCRIPT_DIR/setup-observability.sh"
echo ""

bash "$SCRIPT_DIR/setup-asdlc.sh"
echo ""

echo "============================================"
echo "  ✅ Setup Complete!"
echo "============================================"
echo ""
echo "  Start ASDLC:  cd deployments && bash scripts/start.sh"
echo "  Stop ASDLC:   cd deployments && bash scripts/stop.sh"
echo "  Console:      http://localhost:8090"
echo "  Login:        admin / admin"
echo ""
echo "  Coding-agent: dispatched as a one-shot pod via the"
echo "                'app-factory-coding-agent' ClusterWorkflow"
echo "                in the workflow plane (no long-lived runner)."
echo ""
