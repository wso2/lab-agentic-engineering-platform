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
API_PLATFORM_OPERATOR_VERSION="0.6.0"
# AP gateway-runtime chart version. 1.0.1 ships runtime + controller +
# policy-engine images at tag 1.0.0, which adds `jwt-auth v1` support
# (per-RestApi issuers + audience filtering — see Phase 6 of
# docs/design/api-platform-integration.md).
API_PLATFORM_CHART_VERSION="1.0.1"

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
---
# Sibling of `default` for compose-resident consumers (currently just
# `secret-manager-api`). Same auth, same Vault path, only the server
# URL differs — `host.docker.internal:8200` is reachable from compose
# (k3d-local-config.yaml exposes openbao NodePort 30820 on host 8200);
# `openbao.openbao.svc:8200` is in-cluster DNS only. ESO's vault
# provider still authenticates via k8s SA tokens — OpenBao is
# in-cluster and can TokenReview against the apiserver.
apiVersion: external-secrets.io/v1
kind: ClusterSecretStore
metadata:
  name: default-from-compose
spec:
  provider:
    vault:
      server: "http://host.docker.internal:8200"
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
echo "✅ ClusterSecretStore configured (default + default-from-compose)"

echo ""
echo "6️⃣  WSO2 API Platform operator"
# The AP operator deploys the gateway runtime (controller + router + policy-engine)
# in `openchoreo-data-plane`. The namespace is created by setup-openchoreo.sh
# later, but the operator install runs --create-namespace and `kubectl apply`
# manifests below short-circuit on the same namespace, so order is safe.
#
# The operator manages the gateway-runtime chart out-of-band — see
# manifests/api-platform/operator-values.yaml for the chart pin (gateway v1.0.0).
# CRDs (APIGateway, RestApi) ship with the operator chart.
helm_install_if_not_exists "api-platform-operator" "openchoreo-data-plane" \
    "oci://ghcr.io/wso2/api-platform/helm-charts/gateway-operator" \
    --version ${API_PLATFORM_OPERATOR_VERSION} \
    --set gatewayApi.installStandardCRDs=false \
    --set "gateway.helm.chartVersion=${API_PLATFORM_CHART_VERSION}" \
    --values "${SCRIPT_DIR}/../manifests/api-platform/operator-values.yaml"
kubectl wait --for=condition=available deployment \
    -l app.kubernetes.io/instance=api-platform-operator \
    -n openchoreo-data-plane --context ${CLUSTER_CONTEXT} --timeout=180s || true
echo "✅ API Platform operator installed"

# AES-GCM encryption key required by gateway-controller v1.0.0+ for
# at-rest secret encryption. Generated once and kept in a Secret —
# subsequent setup runs check for the Secret and skip regeneration to
# preserve the key (rotating the key drops all encrypted state).
# Production should provision via ExternalSecret backed by OpenBao/KMS.
if ! kubectl --context ${CLUSTER_CONTEXT} get secret -n openchoreo-data-plane api-platform-controller-aesgcm-key &>/dev/null; then
    AESGCM_KEY_B64=$(openssl rand 32 | base64 | tr -d '\n')
    kubectl --context ${CLUSTER_CONTEXT} create secret generic api-platform-controller-aesgcm-key \
        -n openchoreo-data-plane \
        --from-literal="default-aesgcm256-v1.bin=${AESGCM_KEY_B64}" \
        --dry-run=client -o yaml | \
    sed "s|default-aesgcm256-v1.bin: .*|default-aesgcm256-v1.bin: ${AESGCM_KEY_B64}|" | \
    kubectl --context ${CLUSTER_CONTEXT} apply -f -
    echo "✅ AES-GCM controller encryption key provisioned"
else
    echo "✅ AES-GCM controller encryption key already present (preserved)"
fi

# ConfigMap consumed by the APIGateway CR via spec.configRef.
# Contains the Thunder JWKS keymanagers under jwtauth_v0 (legacy) +
# jwtauth_v1 (Phase 6 — supports issuers/audience filtering).
kubectl --context ${CLUSTER_CONTEXT} apply -f "${SCRIPT_DIR}/../manifests/api-platform/gateway-config.yaml"
echo "✅ gateway-config ConfigMap applied (Thunder keymanager configured)"

# RBAC: lets OC's cluster-agent-dataplane SA reconcile RestApi CRs created
# by the api-configuration trait. The SA is created by setup-openchoreo.sh
# later — k8s allows binding to a non-existent subject.
kubectl --context ${CLUSTER_CONTEXT} apply -f "${SCRIPT_DIR}/../manifests/api-platform/rbac.yaml"
echo "✅ API Platform RBAC applied"

# APIGateway CR — triggers the operator to deploy the gateway-runtime
# (controller, router, policy-engine) reading config from the ConfigMap above.
kubectl --context ${CLUSTER_CONTEXT} apply -f "${SCRIPT_DIR}/../manifests/api-platform/api-gateway.yaml"
echo "✅ APIGateway CR applied — operator will deploy the gateway runtime"

echo ""
echo "✅ All prerequisites installed!"
