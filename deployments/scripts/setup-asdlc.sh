#!/bin/bash
set -e
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
cd "$SCRIPT_DIR"
source "$SCRIPT_DIR/env.sh"
source "$SCRIPT_DIR/utils.sh"

echo "=== Setting up ASDLC Platform ==="

# Verify Thunder is running and ASDLC client exists
kubectl get deployment thunder-deployment -n thunder &>/dev/null || {
    echo "❌ Thunder not found. Run setup-openchoreo.sh first."
    exit 1
}
echo "✅ Thunder is running"
echo "   ASDLC OAuth2 clients are bootstrapped via Thunder helm values"
echo "   (59-asdlc-oauth-apps.sh — registers console / api / workload-publisher / 3x bff→service clients)"

# ============================================================================
# Registry mirror for Docker builds
# ============================================================================
# The workflow-plane registry uses a Kubernetes service DNS name that kubelet
# (containerd) can't resolve. We configure a registry mirror that maps the
# service name to its ClusterIP. This requires a k3s restart, which resets
# DNS configuration — so we re-apply DNS fixes afterward.
echo ""
echo "🐳 Configuring container registry for Docker builds..."
configure_registry_mirror

# ============================================================================
# OpenChoreo workflows (dockerfile-builder + app-factory-coding-agent)
# ============================================================================
echo ""
echo "🔨 Installing OpenChoreo workflows..."

# The ClusterWorkflow CR requires the OC controller-manager webhook to be ready.
# Wait for the controller-manager deployment to be available first.
echo "⏳ Waiting for controller-manager webhook to be ready..."
kubectl wait -n openchoreo-control-plane --for=condition=available --timeout=300s \
    deployment/controller-manager

apply_with_retry() {
    local manifest="$1" label="$2"
    for attempt in 1 2 3 4 5; do
        if kubectl apply -f "$manifest" 2>/dev/null; then
            return 0
        fi
        if [ "$attempt" -eq 5 ]; then
            echo "❌ Failed to apply $label after 5 attempts"
            return 1
        fi
        echo "   Webhook not ready yet, retrying $label in 10s (attempt $attempt/5)..."
        sleep 10
    done
}

apply_with_retry "${SCRIPT_DIR}/../manifests/docker-build-workflow.yaml" "docker-build-workflow"
echo "✅ ClusterWorkflow 'dockerfile-builder' installed"

apply_with_retry "${SCRIPT_DIR}/../manifests/app-factory-coding-agent.yaml" "app-factory-coding-agent"
echo "✅ ClusterWorkflow 'app-factory-coding-agent' installed (one-shot coding-agent pod)"

# ============================================================================
# OpenChoreo infrastructure resources
# ============================================================================
echo ""
echo "📦 Setting up OpenChoreo infrastructure resources..."

# Label the default namespace so the OC API recognizes it as a control-plane namespace.
# Without this label, OC API calls against namespace "default" won't work.
kubectl label namespace default openchoreo.dev/control-plane=true --overwrite
echo "✅ Namespace 'default' labeled for OpenChoreo control plane"

# Workflow-plane namespace for the `default` org. git-service writes the
# per-org build credential Secret and per-org Anthropic key Secret here on
# each dispatch (single-tenant local dev — one org → one workflows-* ns).
# Onboarding more orgs would provision a `workflows-<ouHandle>` per org.
kubectl create namespace workflows-default --dry-run=client -o yaml | kubectl apply -f -
kubectl label namespace workflows-default openchoreo.dev/workflow-plane-name=default --overwrite
echo "✅ Namespace 'workflows-default' created (per-org build + anthropic Secrets land here)"

# ClusterComponentType: deployment/service — backend APIs with path-prefix routing
kubectl apply -f - <<'OCEOF'
apiVersion: openchoreo.dev/v1alpha1
kind: ClusterComponentType
metadata:
  name: service
spec:
  workloadType: deployment
  allowedWorkflows:
    - kind: ClusterWorkflow
      name: dockerfile-builder
  # api-configuration lets a component opt into the WSO2 API Platform
  # gateway path (RestApi + kgateway Backend pointing at the AP router).
  # The trait's CRD is applied below via apply_with_retry.
  allowedTraits:
    - kind: ClusterTrait
      name: api-configuration
  environmentConfigs:
    openAPIV3Schema:
      type: object
      properties:
        replicas:
          type: integer
          default: 1
          minimum: 1
        resources:
          type: object
          default: {}
          properties:
            requests:
              type: object
              default: {}
              properties:
                cpu:
                  type: string
                  default: "50m"
                memory:
                  type: string
                  default: "128Mi"
            limits:
              type: object
              default: {}
              properties:
                cpu:
                  type: string
                  default: "200m"
                memory:
                  type: string
                  default: "256Mi"
  resources:
    - id: deployment
      template:
        apiVersion: apps/v1
        kind: Deployment
        metadata:
          name: "${metadata.componentName}"
          namespace: "${metadata.namespace}"
          labels: "${metadata.labels}"
        spec:
          replicas: "${environmentConfigs.replicas}"
          selector:
            matchLabels: "${metadata.podSelectors}"
          template:
            metadata:
              labels: "${metadata.podSelectors}"
            spec:
              containers:
                - name: main
                  image: "${workload.container.image}"
                  resources:
                    requests:
                      cpu: "${environmentConfigs.resources.requests.cpu}"
                      memory: "${environmentConfigs.resources.requests.memory}"
                    limits:
                      cpu: "${environmentConfigs.resources.limits.cpu}"
                      memory: "${environmentConfigs.resources.limits.memory}"
    - id: service
      template:
        apiVersion: v1
        kind: Service
        metadata:
          name: "${metadata.componentName}"
          namespace: "${metadata.namespace}"
        spec:
          selector: "${metadata.podSelectors}"
          ports: "${workload.toServicePorts()}"

    - id: httproute-external
      forEach: '${workload.endpoints.transformList(name, ep, ("external" in ep.visibility && ep.type in ["HTTP", "REST", "GraphQL", "Websocket"]) ? [name] : []).flatten()}'
      var: endpoint
      template:
        apiVersion: gateway.networking.k8s.io/v1
        kind: HTTPRoute
        metadata:
          name: ${oc_generate_name(metadata.componentName, endpoint)}
          namespace: "${metadata.namespace}"
          labels: '${oc_merge(metadata.labels, {"openchoreo.dev/endpoint-name": endpoint, "openchoreo.dev/endpoint-visibility": "external"})}'
        spec:
          parentRefs:
            - name: "${gateway.ingress.external.name}"
              namespace: "${gateway.ingress.external.namespace}"
          hostnames: |
            ${[gateway.ingress.external.?http, gateway.ingress.external.?https]
              .filter(g, g.hasValue()).map(g, g.value().host).distinct()
              .map(h, metadata.environmentName + "-" + metadata.componentNamespace + "." + h)}
          rules:
            - matches:
                - path:
                    type: PathPrefix
                    value: /${metadata.componentName}-${endpoint}
              filters:
                - type: URLRewrite
                  urlRewrite:
                    path:
                      type: ReplacePrefixMatch
                      replacePrefixMatch: '${workload.endpoints[endpoint].?basePath.orValue("") != "" ? workload.endpoints[endpoint].?basePath.orValue("") : "/"}'
              backendRefs:
                - name: "${metadata.componentName}"
                  port: "${workload.endpoints[endpoint].port}"
OCEOF
echo "✅ ClusterComponentType 'deployment/service' created"

# ClusterTrait: api-configuration — opts a component endpoint into the
# WSO2 API Platform path. Creates a kgateway Backend pointing at the AP
# router, a RestApi CR registering the API, and patches the HTTPRoute's
# backendRef + URL rewrite. Per-environment toggles for CORS / jwtAuth /
# rateLimit / addHeaders flow through the ReleaseBinding's
# `traitEnvironmentConfigs.<instance>` block.
apply_with_retry "${SCRIPT_DIR}/../manifests/api-platform/api-configuration-trait.yaml" "api-configuration-trait"
echo "✅ ClusterTrait 'api-configuration' installed"

# ClusterComponentType: deployment/web-application — frontends with subdomain routing
# Web-apps get their own subdomain via oc_dns_label so SPAs work correctly
# (no subpath issues with asset references).
kubectl apply -f - <<'OCEOF'
apiVersion: openchoreo.dev/v1alpha1
kind: ClusterComponentType
metadata:
  name: web-application
spec:
  workloadType: deployment
  allowedWorkflows:
    - kind: ClusterWorkflow
      name: dockerfile-builder
  environmentConfigs:
    openAPIV3Schema:
      type: object
      properties:
        replicas:
          type: integer
          default: 1
          minimum: 1
        resources:
          type: object
          default: {}
          properties:
            requests:
              type: object
              default: {}
              properties:
                cpu:
                  type: string
                  default: "50m"
                memory:
                  type: string
                  default: "128Mi"
            limits:
              type: object
              default: {}
              properties:
                cpu:
                  type: string
                  default: "200m"
                memory:
                  type: string
                  default: "256Mi"
  resources:
    - id: deployment
      template:
        apiVersion: apps/v1
        kind: Deployment
        metadata:
          name: "${metadata.componentName}"
          namespace: "${metadata.namespace}"
          labels: "${metadata.labels}"
        spec:
          replicas: "${environmentConfigs.replicas}"
          selector:
            matchLabels: "${metadata.podSelectors}"
          template:
            metadata:
              labels: "${metadata.podSelectors}"
            spec:
              containers:
                - name: main
                  image: "${workload.container.image}"
                  resources:
                    requests:
                      cpu: "${environmentConfigs.resources.requests.cpu}"
                      memory: "${environmentConfigs.resources.requests.memory}"
                    limits:
                      cpu: "${environmentConfigs.resources.limits.cpu}"
                      memory: "${environmentConfigs.resources.limits.memory}"
    - id: service
      template:
        apiVersion: v1
        kind: Service
        metadata:
          name: "${metadata.componentName}"
          namespace: "${metadata.namespace}"
        spec:
          selector: "${metadata.podSelectors}"
          ports: "${workload.toServicePorts()}"

    - id: httproute-external
      forEach: '${workload.endpoints.transformList(name, ep, ("external" in ep.visibility && ep.type in ["HTTP", "REST", "GraphQL", "Websocket"]) ? [name] : []).flatten()}'
      var: endpoint
      template:
        apiVersion: gateway.networking.k8s.io/v1
        kind: HTTPRoute
        metadata:
          name: ${oc_generate_name(metadata.componentName, endpoint)}
          namespace: "${metadata.namespace}"
          labels: '${oc_merge(metadata.labels, {"openchoreo.dev/endpoint-name": endpoint, "openchoreo.dev/endpoint-visibility": "external"})}'
        spec:
          parentRefs:
            - name: "${gateway.ingress.external.name}"
              namespace: "${gateway.ingress.external.namespace}"
          hostnames: |
            ${[gateway.ingress.external.?http, gateway.ingress.external.?https]
              .filter(g, g.hasValue()).map(g, g.value().host).distinct()
              .map(h, oc_dns_label(endpoint, metadata.componentName, metadata.environmentName, metadata.componentNamespace) + "." + h)}
          rules:
            - matches:
                - path:
                    type: PathPrefix
                    value: /
              backendRefs:
                - name: "${metadata.componentName}"
                  port: "${workload.endpoints[endpoint].port}"
OCEOF
echo "✅ ClusterComponentType 'deployment/web-application' created"

# Environment: development — backed by the default ClusterDataPlane
kubectl apply -f - <<'OCEOF'
apiVersion: openchoreo.dev/v1alpha1
kind: Environment
metadata:
  name: development
  namespace: default
spec:
  dataPlaneRef:
    kind: ClusterDataPlane
    name: default
OCEOF
echo "✅ Environment 'development' created"

# DeploymentPipeline: default — single environment pipeline
kubectl apply -f - <<'OCEOF'
apiVersion: openchoreo.dev/v1alpha1
kind: DeploymentPipeline
metadata:
  name: default
  namespace: default
spec:
  promotionPaths:
    - sourceEnvironmentRef:
        name: development
      targetEnvironmentRefs: []
OCEOF
echo "✅ DeploymentPipeline 'default' created"

# RBAC: bind both the BFF service account AND human admin users to the
# OC admin role. The first is what the BFF presents on its outbound
# client_credentials calls; the second binds Thunder's default
# `Administrators` group (which `admin/admin` is a member of) so an
# operator logging into the console immediately has admin rights.
kubectl apply -f - <<'OCEOF'
apiVersion: openchoreo.dev/v1alpha1
kind: ClusterAuthzRoleBinding
metadata:
  name: asdlc-api-client-binding
spec:
  effect: allow
  entitlement:
    claim: sub
    value: asdlc-api-client
  roleMappings:
  - roleRef:
      kind: ClusterAuthzRole
      name: admin
---
apiVersion: openchoreo.dev/v1alpha1
kind: ClusterAuthzRoleBinding
metadata:
  name: administrators-group-binding
spec:
  effect: allow
  entitlement:
    claim: groups
    value: Administrators
  roleMappings:
  - roleRef:
      kind: ClusterAuthzRole
      name: admin
OCEOF
echo "✅ ASDLC API service account + Administrators group authorized"

# ============================================================================
# Generate .env file
# ============================================================================
echo ""
echo "📝 Generating .env file..."

ENV_FILE="${SCRIPT_DIR}/../.env"

# Generate Phase 0 secrets and a smee.io channel for local webhook delivery.
# The webhook secret lives only in this file; smee.io is the public callback
# URL we register on each repo's GitHub webhook.
gen_hex32() {
    openssl rand -hex 32 2>/dev/null || python3 -c 'import secrets; print(secrets.token_hex(32))'
}

# Preserve existing secret values across re-runs so already-registered webhooks
# don't suddenly fail HMAC validation (and the BFF's task signing key keeps the
# same JWKS so in-flight Task JWTs still verify).
existing_val() {
    [ -f "$ENV_FILE" ] || return 0
    grep -E "^$1=" "$ENV_FILE" | head -1 | cut -d= -f2-
}

WEBHOOK_SECRET="$(existing_val GITHUB_WEBHOOK_SECRET)"
[ -z "$WEBHOOK_SECRET" ] && WEBHOOK_SECRET="$(gen_hex32)"
OAUTH_STATE_KEY="$(existing_val OAUTH_STATE_SIGNING_KEY)"
[ -z "$OAUTH_STATE_KEY" ] && OAUTH_STATE_KEY="$(gen_hex32)"
echo "🔐 Using GITHUB_WEBHOOK_SECRET (preserved) and OAUTH_STATE_SIGNING_KEY (preserved)"

# Generate the BFF Task JWT signing keypair if it doesn't already exist.
# Idempotent — on re-runs the existing key is left untouched so existing
# task workspaces continue to verify against the same JWKS. The matching
# public key is published by the BFF at /auth/external/jwks.json.
KEYS_DIR="${SCRIPT_DIR}/../keys"
TASK_KEY_PATH="${KEYS_DIR}/task-signing.pem"
mkdir -p "$KEYS_DIR"
if [[ ! -f "$TASK_KEY_PATH" ]]; then
    # `openssl genpkey` emits PKCS#8 by default; task_token_manager.go falls
    # back to PKCS#1 if needed, so this is forward-compatible.
    openssl genpkey -algorithm RSA -out "$TASK_KEY_PATH" -pkeyopt rsa_keygen_bits:2048 2>/dev/null
    chmod 600 "$TASK_KEY_PATH"
    echo "🔐 Generated BFF Task JWT signing key (RSA 2048, PKCS#8) at $TASK_KEY_PATH"
else
    echo "🔐 BFF Task JWT signing key already present at $TASK_KEY_PATH (re-using)"
fi

# Provision a fresh smee.io channel only if we don't already have one — reusing
# the existing URL keeps any GitHub webhook registrations valid across re-runs.
SMEE_URL="$(existing_val GITHUB_WEBHOOK_PROXY_URL)"
if [ -z "$SMEE_URL" ]; then
    SMEE_URL="$(curl -fsS -D - -o /dev/null https://smee.io/new 2>/dev/null | awk '/^location:/ {print $2}' | tr -d '\r')"
    if [ -z "$SMEE_URL" ]; then
        echo "⚠️  Could not auto-create smee.io channel — set GITHUB_WEBHOOK_PROXY_URL manually"
        SMEE_URL=""
    else
        echo "🌐 Provisioned smee.io channel: $SMEE_URL"
    fi
else
    echo "🌐 Reusing existing smee.io channel: $SMEE_URL"
fi

# Preserve any operator-supplied values across re-runs.
ANTHROPIC_KEY="$(existing_val ANTHROPIC_API_KEY)"
GITHUB_APP_ID_VAL="$(existing_val GITHUB_APP_ID)"
GITHUB_CLIENT_ID_VAL="$(existing_val GITHUB_CLIENT_ID)"
GITHUB_CLIENT_SECRET_VAL="$(existing_val GITHUB_CLIENT_SECRET)"
GITHUB_APP_SLUG_VAL="$(existing_val GITHUB_APP_SLUG)"
[ -z "$GITHUB_APP_SLUG_VAL" ] && GITHUB_APP_SLUG_VAL="asdlc-platform"

# Local-dev seed convenience — consumed exclusively by scripts/seed-dev.sh.
# Preserved across re-runs so the operator doesn't have to re-append them
# every time setup-asdlc.sh regenerates the .env from scratch.
LOCAL_DEV_ADMIN_GITHUB_PAT_VAL="$(existing_val LOCAL_DEV_ADMIN_GITHUB_PAT)"
LOCAL_DEV_ADMIN_GITHUB_OWNER_VAL="$(existing_val LOCAL_DEV_ADMIN_GITHUB_OWNER)"

cat > "$ENV_FILE" <<EOF
# Auto-generated by setup-asdlc.sh — $(date -u +"%Y-%m-%dT%H:%M:%SZ")
#
# Re-running setup-asdlc.sh preserves secrets, the smee.io channel, and any
# values you've hand-edited (ANTHROPIC_API_KEY, GitHub App credentials).

# ── OpenChoreo Platform API ────────────────────────────────────────────────
# asdlc-api (in compose) reaches OC via the k3d Docker network. The Host
# header is what kgateway routes on.
PLATFORM_API_SERVICE_BASE_URL=http://k3d-${CLUSTER_NAME}-serverlb:8080
PLATFORM_API_SERVICE_HOST=api.openchoreo.localhost

# ── Public URLs ─────────────────────────────────────────────────────────────
# Single source of truth for the browser-facing Thunder + console hostnames.
# Change these (and re-run start.sh) to expose over ngrok / a public URL.
PUBLIC_THUNDER_URL=http://thunder.openchoreo.localhost:8080
PUBLIC_CONSOLE_URL=http://localhost:8090

# ── Thunder OAuth client (consumed by the console at runtime) ──────────────
VITE_THUNDER_CLIENT_ID=asdlc-console-client
VITE_THUNDER_SCOPES=openid profile email

# ── Agents service ─────────────────────────────────────────────────────────
# Anthropic key used as the platform fallback (returned by git-service to
# agents-service when an org has not connected its own key). Required for
# any AI flow to work in dev.
ANTHROPIC_API_KEY=${ANTHROPIC_KEY}
AGENT_MODEL=claude-sonnet-4-5

# ── GitHub App (optional — only for App-mode Connect) ──────────────────────
# Each org connects via Settings → GitHub Integration using either GitHub
# App or a Personal Access Token. App credentials are only needed for the
# App connect flow; PAT mode works without them.
GITHUB_APP_ID=${GITHUB_APP_ID_VAL}
GITHUB_CLIENT_ID=${GITHUB_CLIENT_ID_VAL}
GITHUB_CLIENT_SECRET=${GITHUB_CLIENT_SECRET_VAL}
GITHUB_APP_SLUG=${GITHUB_APP_SLUG_VAL}
GITHUB_APP_PRIVATE_KEY_PATH=/etc/github-app/private-key.pem
GITHUB_REPO_VISIBILITY=private

# ── GitHub webhook secrets ─────────────────────────────────────────────────
# WEBHOOK_SECRET is the HMAC key the receiver validates events with;
# OAUTH_STATE_SIGNING_KEY signs the GitHub App connect-state JWT
# (CSRF protection on the connect callback). Generated once at setup;
# rotate by clearing the values here and re-running setup-asdlc.sh.
GITHUB_WEBHOOK_SECRET=${WEBHOOK_SECRET}
OAUTH_STATE_SIGNING_KEY=${OAUTH_STATE_KEY}

# Local-dev webhook delivery — GitHub posts events to this smee.io channel,
# which the smee-client compose service forwards to /webhooks/github.
GITHUB_WEBHOOK_PROXY_URL=${SMEE_URL}

# ── Committer identity (for platform-driven commits + tags) ────────────────
GIT_COMMITTER_NAME=ASDLC Bot
GIT_COMMITTER_EMAIL=bot@asdlc.dev

# Dev gate — the BFF refuses some destructive seed paths unless tier=dev.
DEPLOYMENT_TIER=dev

# ── Local-dev seed (scripts/seed-dev.sh) ────────────────────────────────────
# Optional. When set, scripts/seed-dev.sh pre-connects the default org's
# GitHub credentials so you don't have to clickthrough Settings → GitHub
# after every fresh setup. Not read by any platform code path.
LOCAL_DEV_ADMIN_GITHUB_PAT=${LOCAL_DEV_ADMIN_GITHUB_PAT_VAL}
LOCAL_DEV_ADMIN_GITHUB_OWNER=${LOCAL_DEV_ADMIN_GITHUB_OWNER_VAL}
EOF

echo "✅ .env file generated at $(realpath "$ENV_FILE")"

echo ""
echo "✅ ASDLC setup complete!"
echo ""
echo "   Default login credentials:"
echo "     Username: admin"
echo "     Password: admin"
echo ""
echo "   To start ASDLC:"
echo "     cd deployments && bash scripts/start.sh"
