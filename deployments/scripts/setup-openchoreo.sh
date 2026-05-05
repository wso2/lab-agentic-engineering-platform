#!/bin/bash
set -euo pipefail
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
cd "$SCRIPT_DIR"
source "$SCRIPT_DIR/env.sh"
source "$SCRIPT_DIR/utils.sh"

echo "=== Installing OpenChoreo ==="

kubectl cluster-info --context $CLUSTER_CONTEXT &>/dev/null || {
    echo "❌ Cluster '$CLUSTER_CONTEXT' not running. Run: ./setup-k3d.sh && ./setup-prerequisites.sh"
    exit 1
}
kubectl config use-context $CLUSTER_CONTEXT

# Load PUBLIC_THUNDER_URL / PUBLIC_CONSOLE_URL so values-thunder.yaml and
# values-cp.yaml can be rendered with the user's chosen public URLs.
load_public_urls "$SCRIPT_DIR/../.env"
RENDERED_THUNDER_VALUES="$(render_values_file "$SCRIPT_DIR/../single-cluster/values-thunder.yaml")"
RENDERED_CP_VALUES="$(render_values_file "$SCRIPT_DIR/../single-cluster/values-cp.yaml")"
trap 'rm -f "$RENDERED_THUNDER_VALUES" "$RENDERED_CP_VALUES"' EXIT

# ============================================================================
# Control Plane
# ============================================================================
echo "1️⃣  Control Plane"

# Create backstage-secrets before installing the control plane.
# The Backstage pod references this secret for its backend-secret, OAuth client-secret,
# and Jenkins API key. Without it, the pod stays in CreateContainerConfigError.
if ! kubectl get secret backstage-secrets -n openchoreo-control-plane &>/dev/null; then
    echo "🔑 Creating backstage-secrets..."
    kubectl create namespace openchoreo-control-plane --dry-run=client -o yaml | kubectl apply -f -
    kubectl create secret generic backstage-secrets \
        -n openchoreo-control-plane \
        --from-literal=backend-secret="$(head -c 32 /dev/urandom | base64)" \
        --from-literal=client-secret="backstage-portal-secret" \
        --from-literal=jenkins-api-key="placeholder-not-in-use"
fi

if helm status openchoreo-control-plane -n openchoreo-control-plane --kube-context ${CLUSTER_CONTEXT} &>/dev/null; then
    echo "⏭️  Already installed"
else
    echo "📦 Installing OpenChoreo Control Plane (may take up to 10 minutes)..."
    helm upgrade --install openchoreo-control-plane \
        oci://ghcr.io/openchoreo/helm-charts/openchoreo-control-plane \
        --version ${OPENCHOREO_VERSION} \
        --namespace openchoreo-control-plane --create-namespace \
        --values "$RENDERED_CP_VALUES"
fi
echo "⏳ Waiting for Control Plane (core components)..."
kubectl wait -n openchoreo-control-plane --for=condition=available --timeout=300s \
    deployment/controller-manager \
    deployment/openchoreo-api \
    deployment/cluster-gateway \
    deployment/gateway-default
echo "✅ Control Plane ready"
echo ""

# ============================================================================
# Data Plane
# ============================================================================
echo "2️⃣  Data Plane"
if helm status openchoreo-data-plane -n openchoreo-data-plane --kube-context ${CLUSTER_CONTEXT} &>/dev/null; then
    echo "⏭️  Already installed"
else
    echo "📦 Installing OpenChoreo Data Plane..."
    create_plane_cert_resources openchoreo-data-plane
    helm upgrade --install openchoreo-data-plane \
        oci://ghcr.io/openchoreo/helm-charts/openchoreo-data-plane \
        --version ${OPENCHOREO_VERSION} \
        --namespace openchoreo-data-plane --create-namespace \
        --values "${SCRIPT_DIR}/../single-cluster/values-dp.yaml"
fi
echo "⏳ Waiting for Data Plane..."
kubectl wait -n openchoreo-data-plane --for=condition=available --timeout=300s deployment --all
echo "✅ Data Plane ready"

# Register Data Plane
if ! kubectl get clusterdataplane default -n default &>/dev/null; then
    echo "🔗 Registering Data Plane..."
    local_ca=$(kubectl get secret cluster-agent-tls -n openchoreo-data-plane -o jsonpath='{.data.ca\.crt}' | base64 -d)
    register_data_plane "$local_ca" "default" "default"
fi
echo ""

# ============================================================================
# Thunder (Auth IDP)
# ============================================================================
echo "3️⃣  Thunder (Auth IDP)"
if helm status thunder -n thunder --kube-context ${CLUSTER_CONTEXT} &>/dev/null; then
    echo "⏭️  Already installed"
else
    echo "📦 Installing Thunder (Asgardeo IDP)..."
    helm upgrade --install thunder \
        oci://ghcr.io/asgardeo/helm-charts/thunder \
        --version ${THUNDER_VERSION} \
        --namespace thunder --create-namespace \
        --values "$RENDERED_THUNDER_VALUES" \
        --timeout 10m || {
        echo "❌ Thunder installation failed."
        exit 1
    }
fi
echo "⏳ Waiting for Thunder..."
kubectl wait -n thunder --for=condition=available --timeout=300s deployment --all
echo "✅ Thunder ready"

# The Helm chart uses security.oidc.tokenUrl for both the OpenChoreo API metadata
# (browser-facing) and the Backstage token exchange (server-side). The browser URL
# (thunder.openchoreo.localhost:8080) is not resolvable from inside the cluster, so
# we override Backstage's env var to use the cluster-internal Thunder service URL.
echo "🔧 Patching Backstage token URL for in-cluster resolution..."
kubectl set env deployment/backstage \
    -n openchoreo-control-plane --context "${CLUSTER_CONTEXT}" \
    OPENCHOREO_AUTH_TOKEN_URL="http://thunder-service.thunder.svc.cluster.local:8090/oauth2/token"
kubectl rollout status deployment/backstage -n openchoreo-control-plane \
    --context "${CLUSTER_CONTEXT}" --timeout=120s
echo "✅ Backstage token URL patched"
echo ""

# ============================================================================
# Workflow Plane (optional, for builds)
# ============================================================================
echo "4️⃣  Workflow Plane"
if helm status openchoreo-workflow-plane -n openchoreo-workflow-plane --kube-context ${CLUSTER_CONTEXT} &>/dev/null; then
    echo "⏭️  Already installed"
else
    echo "📦 Installing Workflow Plane..."
    create_plane_cert_resources openchoreo-workflow-plane

    helm upgrade --install registry docker-registry \
        --repo https://twuni.github.io/docker-registry.helm \
        --namespace openchoreo-workflow-plane --create-namespace \
        --values "https://raw.githubusercontent.com/openchoreo/openchoreo/v${OPENCHOREO_VERSION}/install/k3d/single-cluster/values-registry.yaml"

    helm upgrade --install openchoreo-workflow-plane \
        oci://ghcr.io/openchoreo/helm-charts/openchoreo-workflow-plane \
        --version ${OPENCHOREO_VERSION} \
        --namespace openchoreo-workflow-plane --create-namespace
fi
echo "⏳ Waiting for Workflow Plane..."
kubectl wait -n openchoreo-workflow-plane --for=condition=available --timeout=300s deployment --all
echo "✅ Workflow Plane ready"

if ! kubectl get clusterworkflowplane default -n default &>/dev/null; then
    echo "🔗 Registering Workflow Plane..."
    wp_ca=$(kubectl get secret cluster-agent-tls -n openchoreo-workflow-plane -o jsonpath='{.data.ca\.crt}' | base64 -d)
    register_workflow_plane "$wp_ca" "default" "default"
fi
echo ""

echo "✅ OpenChoreo installation complete!"
echo ""
echo "📊 Pod Status:"
for ns in openchoreo-control-plane openchoreo-data-plane openchoreo-workflow-plane thunder; do
    echo "--- $ns ---"
    kubectl get pods -n $ns --no-headers 2>/dev/null || echo "  (no pods)"
    echo ""
done
