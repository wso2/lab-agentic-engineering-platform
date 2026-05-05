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
echo "   ASDLC OAuth2 client is bootstrapped via Thunder helm values (60-asdlc-console-app.sh)"

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
# Docker build workflow (Argo ClusterWorkflowTemplates + OC ClusterWorkflow)
# ============================================================================
echo ""
echo "🔨 Installing Docker build workflow..."

# The ClusterWorkflow CR requires the OC controller-manager webhook to be ready.
# Wait for the controller-manager deployment to be available first.
echo "⏳ Waiting for controller-manager webhook to be ready..."
kubectl wait -n openchoreo-control-plane --for=condition=available --timeout=300s \
    deployment/controller-manager

# Retry loop — webhook endpoint registration can lag behind pod readiness
for attempt in 1 2 3 4 5; do
    if kubectl apply -f "${SCRIPT_DIR}/../manifests/docker-build-workflow.yaml" 2>/dev/null; then
        break
    fi
    if [ "$attempt" -eq 5 ]; then
        echo "❌ Failed to apply docker-build-workflow after 5 attempts"
        exit 1
    fi
    echo "   Webhook not ready yet, retrying in 10s (attempt $attempt/5)..."
    sleep 10
done
echo "✅ Docker build workflow installed (dockerfile-builder)"

# ============================================================================
# OpenChoreo infrastructure resources
# ============================================================================
echo ""
echo "📦 Setting up OpenChoreo infrastructure resources..."

# Label the default namespace so the OC API recognizes it as a control-plane namespace.
# Without this label, OC API calls against namespace "default" won't work.
kubectl label namespace default openchoreo.dev/control-plane=true --overwrite
echo "✅ Namespace 'default' labeled for OpenChoreo control plane"

# Trial-tenant namespace — invited users (Platform IDP OU
# `demo-app-factory`) drop their Project CRs here. The git-service auto-seeds
# the platform PAT into this org so trial users get GitHub access without
# the Settings → GitHub Integration flow.
kubectl create namespace demo-app-factory --dry-run=client -o yaml | kubectl apply -f -
kubectl label namespace demo-app-factory openchoreo.dev/control-plane=true --overwrite
echo "✅ Namespace 'demo-app-factory' created and labeled for OpenChoreo control plane"

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

# RBAC: grant the asdlc-api-client service account admin access to OC API.
# This allows the BFF to create/deploy resources via client_credentials tokens
# (e.g., deploy triggered from MCP after submit_implementation).
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
OCEOF
echo "✅ ASDLC API service account authorized (admin)"

# ============================================================================
# Generate .env file
# ============================================================================
echo ""
echo "📝 Generating .env file..."

ENV_FILE="${SCRIPT_DIR}/../.env"

# Generate Phase 0 secrets and a smee.io channel for local webhook delivery.
# The webhook secret lives only in this file; smee.io is the public callback
# URL we register on each repo's GitHub webhook.
WEBHOOK_SECRET="$(openssl rand -hex 32 2>/dev/null || python3 -c 'import secrets; print(secrets.token_hex(32))')"
OAUTH_STATE_KEY="$(openssl rand -hex 32 2>/dev/null || python3 -c 'import secrets; print(secrets.token_hex(32))')"
echo "🔐 Generated GITHUB_WEBHOOK_SECRET (32 bytes) and OAUTH_STATE_SIGNING_KEY (32 bytes; GitHub App connect CSRF state only)"

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

# Provision a fresh smee.io channel. We read the Location header from the
# 307 redirect rather than following it (-L would chase the redirect target
# and leave us with an empty redirect_url at the end). Avoid reusing across
# runs so a left-over channel from another developer's machine never
# proxies events to this one.
SMEE_URL="$(curl -fsS -D - -o /dev/null https://smee.io/new 2>/dev/null | awk '/^location:/ {print $2}' | tr -d '\r')"
if [ -z "$SMEE_URL" ]; then
    echo "⚠️  Could not auto-create smee.io channel — set GITHUB_WEBHOOK_PROXY_URL manually"
    SMEE_URL=""
else
    echo "🌐 Provisioned smee.io channel: $SMEE_URL"
fi

cat > "$ENV_FILE" <<EOF
# Auto-generated by setup-asdlc.sh — $(date -u +"%Y-%m-%dT%H:%M:%SZ")

# OpenChoreo Platform API
# Backend reaches OC via the k3d Docker network
PLATFORM_API_SERVICE_BASE_URL=http://k3d-${CLUSTER_NAME}-serverlb:8080
PLATFORM_API_SERVICE_HOST=api.openchoreo.localhost

# Public URLs — single source of truth for Thunder + console addresses.
# - Local-only mode: keep these as the localhost defaults below.
# - Collaborative mode: change them to your ngrok / public hostnames.
# After editing, re-run scripts/start.sh to propagate to the cluster.
PUBLIC_THUNDER_URL=http://thunder.openchoreo.localhost:8080
PUBLIC_CONSOLE_URL=http://localhost:8090

# Thunder OAuth client + scopes (consumed by the console Vite build)
VITE_THUNDER_CLIENT_ID=asdlc-console-client
VITE_THUNDER_SCOPES=openid profile email

# Thunder proxy (console nginx proxies to Thunder via k3d network)
THUNDER_PROXY_URL=http://k3d-${CLUSTER_NAME}-serverlb:8080
THUNDER_HOST=thunder.openchoreo.localhost

# Agents service (BusinessAnalyst / Architect / TechLead / Developer)
# Uses the Anthropic API directly (not the Claude CLI OAuth session).
# Fill this in before starting the agents service.
ANTHROPIC_API_KEY=
AGENT_MODEL=claude-sonnet-4-5

# GitHub — repo provisioning
# Each org connects via the console (Settings → GitHub Integration) using
# either GitHub App (preferred) or a Personal Access Token. There is no
# platform-wide PAT — git-service holds per-org credentials and routes
# every operation through them.
GITHUB_REPO_VISIBILITY=private

# GitHub-integration secrets. WEBHOOK_SECRET is the HMAC key the receiver
# validates events with; OAUTH_STATE_SIGNING_KEY signs the connect-state
# JWT carried in the GitHub App OAuth state query param (CSRF
# protection on the connect callback). Both generated once at setup;
# rotate on demand by editing this file and re-running setup.
GITHUB_WEBHOOK_SECRET=${WEBHOOK_SECRET}
OAUTH_STATE_SIGNING_KEY=${OAUTH_STATE_KEY}

# Local-dev webhook delivery: GitHub posts events to this smee.io channel,
# which forwards to the BFF's /webhooks/github via the smee-client
# container. Auto-provisioned by setup-asdlc.sh; replace with an ingress
# URL in production.
GITHUB_WEBHOOK_PROXY_URL=${SMEE_URL}

# Committer attribution for platform-driven commits + tags. Phase 0 uses
# the configured PAT owner's identity by default; override here for a
# bot identity if you have one.
GIT_COMMITTER_NAME=ASDLC Bot
GIT_COMMITTER_EMAIL=bot@asdlc.dev

# Phase 0 platform-PAT mode is dev-only. The BFF refuses to start with
# kind=platform-pat unless DEPLOYMENT_TIER=dev.
DEPLOYMENT_TIER=dev

# How remote-worker runs:
#   container — as a Docker service in compose, authed via ANTHROPIC_API_KEY (default)
#   host      — on the host using your Claude Code OAuth session (requires the claude CLI logged in)
REMOTE_WORKER_MODE=container

# GIT_SERVICE_HOST_URL is intentionally NOT set in this template.
#   container mode: compose default `http://git-service:3300` (in-network DNS) applies.
#   host mode:      scripts/start.sh exports `http://localhost:3300` before `docker compose up`.
# Setting it here would leak the host-mode value into container mode and break
# the in-container agent's credhelper.

# Cap on concurrent Claude Agent SDK queries the remote-worker accepts.
# Above this, /dispatch returns 429 so the BFF can backpressure. Default 8.
REMOTE_WORKER_MAX_CONCURRENT_TASKS=8
EOF

echo "✅ .env file generated at $(realpath "$ENV_FILE")"

echo ""
echo "✅ ASDLC setup complete!"
echo ""
echo "   Default login credentials:"
echo "     Username: admin@openchoreo.dev"
echo "     Password: Admin@123"
echo ""
echo "   To start ASDLC:"
echo "     cd deployments && bash scripts/start.sh"
