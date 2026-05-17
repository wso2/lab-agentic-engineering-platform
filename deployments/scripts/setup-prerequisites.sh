#!/bin/bash
set -e
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
cd "$SCRIPT_DIR"
source "$SCRIPT_DIR/env.sh"
source "$SCRIPT_DIR/utils.sh"

GATEWAY_API_VERSION="v1.4.1"
CERT_MANAGER_VERSION="v1.19.4"
EXTERNAL_SECRETS_VERSION="2.0.1"
KGATEWAY_VERSION="v2.2.1"
OPENBAO_VERSION="0.25.6"

echo "=== Installing Prerequisites for OpenChoreo ==="

kubectl cluster-info --context $CLUSTER_CONTEXT &>/dev/null || {
    echo "❌ Cluster '$CLUSTER_CONTEXT' not running. Run: ./setup-k3d.sh"; exit 1
}

echo ""
echo "1️⃣  Gateway API CRDs"
kubectl --context "${CLUSTER_CONTEXT}" apply --server-side --force-conflicts \
    -f "https://github.com/kubernetes-sigs/gateway-api/releases/download/${GATEWAY_API_VERSION}/experimental-install.yaml" &>/dev/null
echo "✅ Gateway API CRDs applied"

echo ""
echo "2️⃣  cert-manager"
helm_install_if_not_exists "cert-manager" "cert-manager" \
    "oci://quay.io/jetstack/charts/cert-manager" \
    --version ${CERT_MANAGER_VERSION} --set crds.enabled=true
kubectl wait --for=condition=available deployment/cert-manager -n cert-manager --context ${CLUSTER_CONTEXT} --timeout=120s
echo "✅ cert-manager ready"

echo ""
echo "3️⃣  External Secrets Operator"
helm_install_if_not_exists "external-secrets" "external-secrets" \
    "oci://ghcr.io/external-secrets/charts/external-secrets" \
    --version ${EXTERNAL_SECRETS_VERSION} --set installCRDs=true
kubectl wait --for=condition=Available deployment --all -n external-secrets --context ${CLUSTER_CONTEXT} --timeout=180s
echo "✅ External Secrets ready"

echo ""
echo "4️⃣  Kgateway"
helm_install_if_not_exists "kgateway-crds" "openchoreo-control-plane" \
    "oci://cr.kgateway.dev/kgateway-dev/charts/kgateway-crds" --version ${KGATEWAY_VERSION}
helm_install_if_not_exists "kgateway" "openchoreo-control-plane" \
    "oci://cr.kgateway.dev/kgateway-dev/charts/kgateway" --version ${KGATEWAY_VERSION} \
    --set controller.extraEnv.KGW_ENABLE_GATEWAY_API_EXPERIMENTAL_FEATURES=true
echo "✅ Kgateway installed"

echo ""
echo "5️⃣  OpenBao"
# Auth + policy + role seeding is now baked into the postStart hook in
# single-cluster/values-openbao.yaml (mirrors agent-manager's
# single-cluster/values-openbao.yaml pattern — declarative, no
# kubectl-exec follow-up). NodePort 30820 is exposed on host port 8200
# by k3d-local-config.yaml, so docker-compose services reach OpenBao via
# host.docker.internal:8200; in-cluster consumers use openbao.openbao.svc:8200.
helm_install_if_not_exists "openbao" "openbao" \
    "oci://ghcr.io/openbao/charts/openbao" --version ${OPENBAO_VERSION} \
    --values "${SCRIPT_DIR}/../single-cluster/values-openbao.yaml" \
    --set "server.service.type=NodePort" \
    --set "server.service.nodePort=30820"
kubectl wait --for=condition=Ready pod -l app.kubernetes.io/name=openbao -n openbao --context ${CLUSTER_CONTEXT} --timeout=120s
# The pod is Ready before postStart finishes — wait for the auth/kubernetes
# mount to appear before any downstream caller (External Secrets) tries to
# resolve a role.
echo "⏳ Waiting for OpenBao postStart hook to finish..."
for i in $(seq 1 30); do
    if kubectl exec -n openbao --context ${CLUSTER_CONTEXT} openbao-0 -- \
        sh -c 'BAO_ADDR=http://127.0.0.1:8200 BAO_TOKEN=root bao auth list 2>/dev/null | grep -q kubernetes'; then
        echo "✅ OpenBao ready (kubernetes auth + policies seeded by postStart)"
        break
    fi
    sleep 2
done

echo ""
echo "🔧 Configuring External Secrets ClusterSecretStore..."
kubectl --context ${CLUSTER_CONTEXT} apply -f - <<EOF
apiVersion: v1
kind: ServiceAccount
metadata:
  name: external-secrets-openbao
  namespace: openbao
---
apiVersion: external-secrets.io/v1
kind: ClusterSecretStore
metadata:
  name: default
spec:
  provider:
    vault:
      server: "http://openbao.openbao.svc:8200"
      path: "secret"
      version: "v2"
      auth:
        kubernetes:
          mountPath: "kubernetes"
          role: "openchoreo-secret-writer-role"
          serviceAccountRef:
            name: "external-secrets-openbao"
            namespace: "openbao"
EOF
echo "✅ ClusterSecretStore configured"

echo ""
echo "✅ All prerequisites installed!"
