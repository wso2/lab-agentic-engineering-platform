#!/bin/bash
set -e
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
cd "$SCRIPT_DIR"
source "$SCRIPT_DIR/env.sh"
source "$SCRIPT_DIR/utils.sh"

echo "=== Setting up k3d Cluster for OpenChoreo ==="

# Detect Colima runtime — k3d's DNS fix replaces Docker's embedded DNS (127.0.0.11)
# with the gateway IP, which causes DNS timeouts in Colima due to firewall/network
# isolation. Setting K3D_FIX_DNS=0 preserves Docker's built-in DNS.
# See https://github.com/k3d-io/k3d/issues/1449
is_colima=false
if docker info --format '{{.Name}}' 2>/dev/null | grep -qi "colima"; then
    is_colima=true
fi

if k3d cluster list 2>/dev/null | grep -q "${CLUSTER_NAME}"; then
    echo "✅ k3d cluster '${CLUSTER_NAME}' already exists"
    ensure_cluster_accessible
    kubectl cluster-info --context ${CLUSTER_CONTEXT}
else
    check_required_ports || exit 1
    mkdir -p /tmp/k3d-shared

    K3D_CONFIG="${SCRIPT_DIR}/../k3d-local-config.yaml"
    if [ ! -f "$K3D_CONFIG" ]; then
        echo "❌ k3d config not found at $K3D_CONFIG"
        echo "   Restore it with: git checkout HEAD -- deployments/k3d-local-config.yaml"
        exit 1
    fi

    if [ "$is_colima" = true ]; then
        echo "🚀 Creating k3d cluster (Colima detected — K3D_FIX_DNS=0)..."
        K3D_FIX_DNS=0 k3d cluster create --config "$K3D_CONFIG"
    else
        echo "🚀 Creating k3d cluster..."
        k3d cluster create --config "$K3D_CONFIG"
    fi

    echo "✅ k3d cluster created!"
    refresh_kubeconfig
    wait_for_cluster || { echo "❌ Cluster failed to start"; exit 1; }
fi

echo "🔧 Applying CoreDNS custom configuration..."
kubectl apply --context "${CLUSTER_CONTEXT}" \
    -f "https://raw.githubusercontent.com/openchoreo/openchoreo/v${OPENCHOREO_VERSION}/install/k3d/common/coredns-custom.yaml"
echo "✅ CoreDNS configured"

# Fix node-level DNS (8.8.8.8 fallback for external image pulls).
fix_node_dns

# Ensure pods can resolve host.k3d.internal (k3d only sets it as a TLS SAN;
# the CoreDNS NodeHosts entry is on us). Paired with OC's coredns-custom.yaml
# rewrite for *.openchoreo.localhost above.
ensure_host_k3d_internal_in_coredns

# Repair OC's `openchoreo.override` so pods can also reach `*.openchoreo.localhost`
# and `*.openchoreoapis.localhost` — the chart-shipped rewrite only handles
# the first and targets a name the `.:53` plugin chain can't resolve.
ensure_openchoreo_localhost_in_coredns

generate_machine_ids "$CLUSTER_NAME"
echo ""
echo "✅ k3d cluster ready!"
