#!/bin/bash
# scripts/setup-observability.sh — installs the OpenChoreo Observability
# Plane (minimum profile: Observer + cluster-agent + OpenSearch + Fluent Bit).
# Required for the in-UI Live Progress panel to stream coding-agent + build
# logs. Without this, the BFF's /progress/agent endpoint returns 503 and the
# UI falls back to "Live progress unavailable — falling back to status polling".
#
# Idempotent: re-running is safe — helm install is gated by helm_install_if_not_exists,
# kubectl apply is server-side, and ExternalSecrets / CRs are upserts.
#
# Wiring summary:
#   - Helm: openchoreo-observability-plane @ v1.0.1-hotfix.1
#       Observer + cluster-agent + RCA. Creates HTTPRoute on its OWN
#       gateway (openchoreo-observability-plane/gateway-default, port 11080).
#   - Helm: observability-logs-opensearch @ v0.3.11
#       OpenSearch (storage) + Fluent Bit (DaemonSet, log shipper).
#   - Cross-namespace HTTPRoute on the MAIN kgateway
#       (openchoreo-control-plane/gateway-default) for observer.openchoreo.localhost
#       so the BFF in docker-compose can reach the Observer via the same
#       k3d-openchoreo-serverlb:8080 it uses for everything else (the
#       obs-plane's own port-11080 gateway isn't exposed by k3d serverlb).
#   - ExternalSecret: opensearch-admin-credentials, observer-secret
#       Pull username/password/OAuth-client-secret from OpenBao
#       (seeded by single-cluster/values-openbao.yaml postStart hook).
#   - CR: ClusterObservabilityPlane/default registers this plane with the CP.
#   - Job: opensearch-bootstrap-templates fixes the upstream chart's
#       index-template race where Fluent Bit's first write lands before the
#       container-logs template, leaving kubernetes.pod_name as text instead
#       of keyword (Observer's wildcard query then matches zero docs).
set -e
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
cd "$SCRIPT_DIR"
source "$SCRIPT_DIR/env.sh"
source "$SCRIPT_DIR/utils.sh"

OBS_PLANE_VERSION="1.0.1-hotfix.1"
OBS_LOGS_VERSION="0.3.11"
NS="openchoreo-observability-plane"

echo "=== Installing OpenChoreo Observability Plane ==="

kubectl cluster-info --context $CLUSTER_CONTEXT &>/dev/null || {
    echo "❌ Cluster '$CLUSTER_CONTEXT' not running. Run: ./setup-k3d.sh && ./setup-prerequisites.sh && ./setup-openchoreo.sh"
    exit 1
}

# ── 1. Namespace + ExternalSecrets (pulled from OpenBao) ─────────────────
echo ""
echo "1️⃣  Namespace + ExternalSecrets"
kubectl --context "$CLUSTER_CONTEXT" create namespace "$NS" --dry-run=client -o yaml | kubectl apply -f - >/dev/null
# The observability-plane chart mounts the cluster-gateway-ca ConfigMap into
# its cluster-agent pod but does not create it (same as the DP/WF charts).
# Without this the cluster-agent pod sits in ContainerCreating forever with
# `MountVolume.SetUp failed for volume "server-ca" : configmap "cluster-gateway-ca" not found`.
# Mirrors create_plane_cert_resources calls for openchoreo-{data,workflow}-plane
# in setup-openchoreo.sh.
create_plane_cert_resources "$NS"
kubectl --context "$CLUSTER_CONTEXT" apply -f - <<EOF
apiVersion: external-secrets.io/v1
kind: ExternalSecret
metadata:
  name: opensearch-admin-credentials
  namespace: $NS
spec:
  refreshInterval: 1h
  secretStoreRef:
    kind: ClusterSecretStore
    name: default
  target:
    name: opensearch-admin-credentials
  data:
    - secretKey: username
      remoteRef: { key: opensearch-username, property: value }
    - secretKey: password
      remoteRef: { key: opensearch-password, property: value }
---
apiVersion: external-secrets.io/v1
kind: ExternalSecret
metadata:
  name: observer-secret
  namespace: $NS
spec:
  refreshInterval: 1h
  secretStoreRef:
    kind: ClusterSecretStore
    name: default
  target:
    name: observer-secret
  data:
    - secretKey: OPENSEARCH_USERNAME
      remoteRef: { key: opensearch-username, property: value }
    - secretKey: OPENSEARCH_PASSWORD
      remoteRef: { key: opensearch-password, property: value }
    - secretKey: UID_RESOLVER_OAUTH_CLIENT_SECRET
      remoteRef: { key: observer-oauth-client-secret, property: value }
EOF
echo "✅ ExternalSecrets applied"

# ── 2. Observability plane chart (Observer + cluster-agent + RCA) ────────
echo ""
echo "2️⃣  openchoreo-observability-plane chart (v${OBS_PLANE_VERSION})"
cat > /tmp/obs-plane-values.yaml <<EOF
observer:
  openSearchSecretName: opensearch-admin-credentials
  secretName: observer-secret
  http:
    hostnames:
      - observer.openchoreo.localhost
  # The obs-plane runs co-located with the control-plane here, so talk
  # to it via in-cluster DNS instead of the chart default
  # (api.openchoreo.localhost, which only resolves on the host).
  controlPlaneApiUrl: "http://openchoreo-api.openchoreo-control-plane.svc.cluster.local:8080"
security:
  enabled: true
  oidc:
    jwksUrl: "http://thunder-service.thunder.svc.cluster.local:8090/oauth2/jwks"
    tokenUrl: "http://thunder-service.thunder.svc.cluster.local:8090/oauth2/token"
    authServerBaseUrl: "http://thunder.openchoreo.localhost:8080"
rca:
  http:
    hostnames:
      - rca-agent.openchoreo.localhost
# Disable the chart's standalone Gateway on :11080. k3d-openchoreo-serverlb
# doesn't expose 11080, and step 4 below adds a cross-NS HTTPRoute on the
# main kgateway (:8080) which is what the BFF reaches via
# Host: observer.openchoreo.localhost. The bundled Gateway is dead weight.
gateway:
  enabled: false
EOF
# Use `upgrade --install` (not the shared helm_install_if_not_exists helper)
# so re-runs pick up value changes from /tmp/obs-plane-values.yaml. The
# helper skips already-installed releases, which would silently bypass any
# future tuning here.
helm upgrade --install observability-plane \
    "oci://ghcr.io/openchoreo/helm-charts/openchoreo-observability-plane" \
    --namespace "$NS" --create-namespace --kube-context "${CLUSTER_CONTEXT}" \
    --version "$OBS_PLANE_VERSION" \
    --values /tmp/obs-plane-values.yaml \
    --timeout 10m
echo "⏳ Waiting for Observer + controller-manager..."
kubectl --context "$CLUSTER_CONTEXT" -n "$NS" wait --for=condition=Available deployment/observer --timeout=300s
kubectl --context "$CLUSTER_CONTEXT" -n "$NS" wait --for=condition=Available deployment/controller-manager --timeout=300s
echo "✅ observability-plane ready"

# ── 3. Logs + OpenSearch + Fluent Bit chart ──────────────────────────────
echo ""
echo "3️⃣  observability-logs-opensearch chart (v${OBS_LOGS_VERSION})"
cat > /tmp/obs-logs-values.yaml <<EOF
openSearchSetup:
  openSearchSecretName: opensearch-admin-credentials
# Local-dev sizing — default heap is -Xmx512M, which resolves to ~980 MiB
# resident with JVM overhead. 256M heap is enough for one developer's log
# volume and brings resident usage to ~500-600 MiB. Subchart key is
# openSearch (camelCase) — confirmed via
# 'helm get values observability-logs-opensearch --all'.
# (Backticks avoided here — this heredoc is unquoted to allow ${VAR}
# substitution elsewhere, so backticks would trigger command substitution.)
openSearch:
  opensearchJavaOpts: "-Xmx256M -Xms256M"
  resources:
    requests:
      cpu: 200m
      memory: 512Mi
    limits:
      memory: 768Mi
# Enable Fluent Bit immediately so log collection is active from first install
# (avoids a second helm-upgrade pass).
fluent-bit:
  enabled: true
EOF
helm upgrade --install observability-logs-opensearch \
    "oci://ghcr.io/openchoreo/helm-charts/observability-logs-opensearch" \
    --namespace "$NS" --create-namespace --kube-context "${CLUSTER_CONTEXT}" \
    --version "$OBS_LOGS_VERSION" \
    --values /tmp/obs-logs-values.yaml \
    --timeout 15m
echo "⏳ Waiting for OpenSearch StatefulSet (large image — first install ~5-10 min)..."
kubectl --context "$CLUSTER_CONTEXT" -n "$NS" rollout status statefulset/opensearch-master --timeout=900s
echo "✅ logs-opensearch ready"

# ── 4. Cross-namespace HTTPRoute on the MAIN kgateway ────────────────────
# The chart's own HTTPRoute attaches to a separate Gateway on port 11080.
# k3d's serverlb only exposes the main kgateway on port 8080. Add a second
# HTTPRoute targeting the main kgateway so docker-compose-hosted BFF can
# reach the Observer via http://k3d-openchoreo-serverlb:8080 + Host header.
echo ""
echo "4️⃣  Cross-namespace HTTPRoute on main kgateway"
kubectl --context "$CLUSTER_CONTEXT" apply -f - <<EOF
apiVersion: gateway.networking.k8s.io/v1
kind: HTTPRoute
metadata:
  name: observer-mainkgw
  namespace: $NS
spec:
  parentRefs:
    - name: gateway-default
      namespace: openchoreo-control-plane
      sectionName: http
  hostnames:
    - observer.openchoreo.localhost
  rules:
    - matches:
        - path: { type: PathPrefix, value: / }
      backendRefs:
        - name: observer
          port: 8080
      timeouts:
        request: "0s"
        backendRequest: "0s"
EOF
echo "✅ HTTPRoute observer-mainkgw applied"

# ── 4b. App Factory authz role + binding for Observer ───────────────────
# The OC control-plane chart ships a `observer-resource-reader` role
# bound to `openchoreo-observer-resource-reader-client` (the Observer's
# UID-resolver subject), but that role only has component/project/
# namespace/environment :view — NOT logs:view or workflowrun:view, both
# of which the Observer requires for /api/v1/logs/query. Without these
# the Observer returns 403 "no matching policies found" even though
# JWT auth succeeds. Mirrors v2 wso2cloud-deployment/.../init/layer-2/controlplane.yaml.
echo ""
echo "4b. App Factory ClusterAuthzRole + binding for Observer"
kubectl --context "$CLUSTER_CONTEXT" apply -f - <<'EOF'
apiVersion: openchoreo.dev/v1alpha1
kind: ClusterAuthzRole
metadata:
  name: app-factory-observer-reader
spec:
  actions:
    - "logs:view"
    - "workflowrun:view"
    - "component:view"
    - "project:view"
    - "namespace:view"
    - "environment:view"
---
apiVersion: openchoreo.dev/v1alpha1
kind: ClusterAuthzRoleBinding
metadata:
  name: app-factory-observer-reader-binding
spec:
  effect: allow
  entitlement:
    claim: sub
    value: openchoreo-observer-resource-reader-client
  roleMappings:
    - roleRef:
        kind: ClusterAuthzRole
        name: app-factory-observer-reader
EOF
echo "✅ App Factory observer-reader role + binding applied"

# ── 5. ClusterObservabilityPlane CR (registers plane with the CP) ────────
echo ""
echo "5️⃣  ClusterObservabilityPlane CR"
kubectl --context "$CLUSTER_CONTEXT" apply -f - <<EOF
apiVersion: openchoreo.dev/v1alpha1
kind: ClusterObservabilityPlane
metadata:
  name: default
spec:
  planeID: default
  clusterAgent:
    clientCA:
      secretKeyRef:
        key: ca.crt
        name: cluster-agent-tls
        namespace: $NS
  observerURL: http://observer.openchoreo.localhost:11080
EOF
echo "✅ ClusterObservabilityPlane registered"

# ── 6. OpenSearch index-template bootstrap Job ───────────────────────────
# Fixes the upstream chart's race where Fluent Bit's first stdout-write can
# land before the container-logs index template applies, leaving
# kubernetes.pod_name as `text` instead of `keyword` — Observer's wildcard
# query then matches zero docs. Self-healing: applies the priority-500
# composable template and deletes any indices with the wrong mapping.
echo ""
echo "6️⃣  OpenSearch index-template bootstrap Job"
kubectl --context "$CLUSTER_CONTEXT" -n "$NS" delete job opensearch-bootstrap-templates --ignore-not-found >/dev/null
kubectl --context "$CLUSTER_CONTEXT" apply -f - <<'EOF'
apiVersion: batch/v1
kind: Job
metadata:
  name: opensearch-bootstrap-templates
  namespace: openchoreo-observability-plane
spec:
  backoffLimit: 5
  ttlSecondsAfterFinished: 600
  template:
    spec:
      restartPolicy: OnFailure
      containers:
        - name: bootstrap
          image: curlimages/curl:8.10.1
          env:
            - { name: OS_HOST, value: opensearch }
            - { name: OS_PORT, value: "9200" }
            - name: OS_USER
              valueFrom: { secretKeyRef: { name: opensearch-admin-credentials, key: username } }
            - name: OS_PASS
              valueFrom: { secretKeyRef: { name: opensearch-admin-credentials, key: password } }
          command: ["/bin/sh", "-c"]
          args:
            - |
              set -eu
              OS="https://${OS_HOST}:${OS_PORT}"
              CURL="curl -sk -u ${OS_USER}:${OS_PASS} -H Content-Type:application/json"
              echo "Waiting for OpenSearch ready..."
              for i in $(seq 1 60); do
                if $CURL "${OS}/_cluster/health?wait_for_status=yellow&timeout=5s" >/dev/null 2>&1; then break; fi
                sleep 5
              done
              echo "Applying composable index template container-logs..."
              $CURL -X PUT "${OS}/_index_template/container-logs" -d @- <<'JSON'
              {"index_patterns":["container-logs-*"],"priority":500,"template":{"settings":{"index":{"number_of_shards":1,"number_of_replicas":1}},"mappings":{"dynamic":"true","properties":{"@timestamp":{"type":"date"},"stream":{"type":"keyword"},"log":{"type":"text"},"kubernetes":{"properties":{"pod_name":{"type":"keyword"},"namespace_name":{"type":"keyword"},"container_name":{"type":"keyword"},"pod_id":{"type":"keyword"},"host":{"type":"keyword"},"labels":{"type":"object","dynamic":true},"annotations":{"properties":{"workflows_argoproj_io/node-name":{"type":"keyword"}}}}}}}}}
              JSON
              echo
              echo "Scanning indices for wrong pod_name mapping..."
              for idx in $($CURL "${OS}/_cat/indices/container-logs-*?h=index" 2>/dev/null); do
                t=$($CURL "${OS}/${idx}/_mapping/field/kubernetes.pod_name" 2>/dev/null \
                  | grep -o '"type":"[a-z]*"' | head -1 | cut -d'"' -f4)
                if [ "$t" = "text" ]; then echo "  - ${idx}: '$t', recreating"; $CURL -X DELETE "${OS}/${idx}" >/dev/null
                else echo "  - ${idx}: '$t', ok"; fi
              done
              echo "Bootstrap complete."
EOF
echo "⏳ Waiting for bootstrap Job to finish..."
kubectl --context "$CLUSTER_CONTEXT" -n "$NS" wait --for=condition=complete job/opensearch-bootstrap-templates --timeout=300s
echo "✅ OpenSearch index-template bootstrap complete"

echo ""
echo "✅ Observability Plane installation complete!"
