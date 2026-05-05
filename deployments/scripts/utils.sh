# Shared utilities — sourced by setup scripts.

is_port_in_use() {
    lsof -i :"$1" -sTCP:LISTEN &>/dev/null
}

check_required_ports() {
    local ports=(
        "6550:Kubernetes API"
        "8080:Control Plane HTTP"
        "8443:Control Plane HTTPS"
        "19080:Data Plane HTTP"
        "19443:Data Plane HTTPS"
        "10081:Argo Workflows UI"
        "10082:Container Registry"
        "11080:Observability HTTP"
        "11085:OpenSearch HTTPS"
    )
    local blocked=()
    echo "🔍 Checking port availability..."
    for p in "${ports[@]}"; do
        local port="${p%%:*}" desc="${p#*:}"
        if is_port_in_use "$port"; then blocked+=("$port ($desc)"); fi
    done
    if [ ${#blocked[@]} -gt 0 ]; then
        echo "❌ Ports in use: ${blocked[*]}"
        echo "   Free them or stop the conflicting process."
        return 1
    fi
    echo "✅ All ports available"
}

helm_install_if_not_exists() {
    local release="$1" ns="$2" chart="$3"; shift 3
    if helm status "$release" -n "$ns" --kube-context "${CLUSTER_CONTEXT}" &>/dev/null; then
        echo "⏭️  $release already installed, skipping"
        return 0
    fi
    echo "📦 Installing $release..."
    helm install "$release" "$chart" --namespace "$ns" --create-namespace --kube-context "${CLUSTER_CONTEXT}" "$@"
    echo "✅ $release installed"
}

refresh_kubeconfig() {
    echo "🔄 Refreshing kubeconfig..."
    k3d kubeconfig merge ${CLUSTER_NAME} --kubeconfig-merge-default --kubeconfig-switch-context
}

wait_for_cluster() {
    echo "⏳ Waiting for cluster..."
    for i in {1..30}; do
        if kubectl cluster-info --context ${CLUSTER_CONTEXT} --request-timeout=5s &>/dev/null; then
            echo "✅ Cluster ready"
            return 0
        fi
        echo "   Attempt $i/30..."
        sleep 2
    done
    return 1
}

ensure_cluster_accessible() {
    refresh_kubeconfig
    if kubectl cluster-info --context ${CLUSTER_CONTEXT} --request-timeout=10s &>/dev/null; then
        echo "✅ Cluster accessible"
        return 0
    fi
    echo "⚠️  Cluster not accessible. Restarting..."
    k3d cluster stop ${CLUSTER_NAME} 2>/dev/null || true
    k3d cluster start ${CLUSTER_NAME}
    refresh_kubeconfig
    wait_for_cluster
}

create_plane_cert_resources() {
    local ns="$1"
    kubectl create namespace "$ns" --dry-run=client -o yaml | kubectl apply -f -
    kubectl wait -n openchoreo-control-plane --for=condition=Ready certificate/cluster-gateway-ca --timeout=120s
    local ca
    ca=$(kubectl get secret cluster-gateway-ca -n openchoreo-control-plane -o jsonpath='{.data.ca\.crt}' | base64 -d)
    kubectl create configmap cluster-gateway-ca --from-literal=ca.crt="$ca" -n "$ns" --dry-run=client -o yaml | kubectl apply -f -
}

register_data_plane() {
    local ca_cert="$1" plane_id="$2" secret_store="$3"
    cat <<EOF | kubectl apply -f -
apiVersion: openchoreo.dev/v1alpha1
kind: ClusterDataPlane
metadata:
  name: default
  namespace: default
spec:
  planeID: "$plane_id"
  secretStoreRef:
    name: "$secret_store"
  clusterAgent:
    clientCA:
      value: |
$(echo "$ca_cert" | sed 's/^/        /')
  gateway:
    ingress:
      external:
        name: gateway-default
        namespace: openchoreo-data-plane
        http:
          host: "openchoreoapis.localhost"
          listenerName: http
          port: 19080
        https:
          host: "openchoreoapis.localhost"
          listenerName: https
          port: 19443
EOF
    echo "✅ DataPlane registered"
}

register_workflow_plane() {
    local ca_cert="$1" plane_id="$2" secret_store="$3"
    cat <<EOF | kubectl apply -f -
apiVersion: openchoreo.dev/v1alpha1
kind: ClusterWorkflowPlane
metadata:
  name: default
  namespace: default
spec:
  planeID: "$plane_id"
  secretStoreRef:
    name: "$secret_store"
  clusterAgent:
    clientCA:
      value: |
$(echo "$ca_cert" | sed 's/^/        /')
EOF
    echo "✅ WorkflowPlane registered"
}

# Ensure CoreDNS can resolve host.k3d.internal for the *.openchoreo.localhost rewrite rule.
# On Colima, Docker's embedded DNS (127.0.0.11) doesn't resolve host.k3d.internal,
# so we add the k3d loadbalancer IP to CoreDNS's NodeHosts as a fallback.
# This is idempotent and handles stale entries after Docker/Colima restarts (IP changes).
patch_coredns_host_k3d_internal() {
    echo "🔧 Ensuring host.k3d.internal resolves in CoreDNS..."
    local lb_ip
    lb_ip=$(docker inspect k3d-${CLUSTER_NAME}-serverlb \
        --format "{{(index .NetworkSettings.Networks \"k3d-${CLUSTER_NAME}\").IPAddress}}" 2>/dev/null)
    if [ -z "$lb_ip" ]; then
        echo "⚠️  Could not determine loadbalancer IP — skipping"
        return
    fi

    local current_hosts
    current_hosts=$(kubectl get cm coredns -n kube-system --context "${CLUSTER_CONTEXT}" -o jsonpath='{.data.NodeHosts}')
    if echo "$current_hosts" | grep -q "^${lb_ip} host.k3d.internal$"; then
        echo "✅ host.k3d.internal already in CoreDNS NodeHosts with correct IP (${lb_ip})"
        return
    fi

    kubectl get cm coredns -n kube-system --context "${CLUSTER_CONTEXT}" -o json | \
        python3 -c "
import json, sys, re
cm = json.load(sys.stdin)
hosts = cm['data']['NodeHosts']
hosts = re.sub(r'\n?[^\n]*host\.k3d\.internal[^\n]*', '', hosts)
cm['data']['NodeHosts'] = hosts.rstrip() + '\n${lb_ip} host.k3d.internal\n'
cm['metadata'] = {'name': cm['metadata']['name'], 'namespace': cm['metadata']['namespace']}
json.dump(cm, sys.stdout)
" | kubectl apply --context "${CLUSTER_CONTEXT}" -f -
    kubectl rollout restart deployment coredns -n kube-system --context "${CLUSTER_CONTEXT}"
    kubectl rollout status deployment coredns -n kube-system --context "${CLUSTER_CONTEXT}" --timeout=60s
    echo "✅ host.k3d.internal added to CoreDNS NodeHosts (${lb_ip})"
}

# Fix DNS on all k3d nodes. Keeps Docker's embedded DNS (127.0.0.11) as primary
# so that Docker-internal names (host.k3d.internal, container names) still resolve,
# and adds 8.8.8.8 as a fallback for external domains.
fix_node_dns() {
    echo "🔧 Fixing k3d node DNS resolution..."
    for node in $(docker ps --filter "name=k3d-${CLUSTER_NAME}" --format '{{.Names}}'); do
        docker exec "$node" sh -c 'echo "nameserver 127.0.0.11" > /etc/resolv.conf; echo "nameserver 8.8.8.8" >> /etc/resolv.conf' 2>/dev/null || true
    done
    echo "✅ Node DNS configured"
}

# Configure k3s containerd to use the workflow-plane registry via ClusterIP.
# Kubelet can't resolve Kubernetes service DNS, so we mirror the service name
# to its ClusterIP. Requires k3s restart to take effect.
configure_registry_mirror() {
    echo "🔧 Configuring k3s registry mirror for workflow-plane registry..."
    local registry_ip
    registry_ip=$(kubectl get svc registry -n openchoreo-workflow-plane --context "${CLUSTER_CONTEXT}" -o jsonpath='{.spec.clusterIP}' 2>/dev/null)
    if [ -z "$registry_ip" ]; then
        echo "⚠️  Workflow-plane registry not found — skipping"
        return 1
    fi

    for node in $(docker ps --filter "name=k3d-${CLUSTER_NAME}" --format '{{.Names}}'); do
        docker exec "$node" sh -c "
mkdir -p /etc/rancher/k3s
cat > /etc/rancher/k3s/registries.yaml <<EOF
mirrors:
  \"registry.openchoreo-workflow-plane.svc.cluster.local:10082\":
    endpoint:
      - \"http://${registry_ip}:10082\"
EOF
" 2>/dev/null || true
    done
    echo "✅ Registry mirror configured (${registry_ip}:10082)"

    # k3s must be restarted to pick up registries.yaml changes.
    # We restart k3s by sending SIGHUP to PID 1 in each node, then
    # re-apply DNS fixes that get reset during restart.
    echo "🔄 Restarting k3s to apply registry configuration..."
    for node in $(docker ps --filter "name=k3d-${CLUSTER_NAME}" --format '{{.Names}}'); do
        docker exec "$node" sh -c "kill -HUP 1" 2>/dev/null || true
    done
    sleep 15
    wait_for_cluster || { echo "❌ Cluster failed to restart"; return 1; }

    # DNS fixes are reset after k3s restart — re-apply them
    fix_node_dns
    patch_coredns_host_k3d_internal
}


# ----------------------------------------------------------------------------
# Public URL handling
# ----------------------------------------------------------------------------
# .env carries two canonical fields:
#   PUBLIC_THUNDER_URL   — public URL the browser uses to reach Thunder
#   PUBLIC_CONSOLE_URL   — public URL the browser uses to reach the console
# Everything that needs these values (Helm values, ConfigMaps, redirect URIs,
# OIDC issuer) derives from them — edit .env, re-run start.sh, done.

# Load PUBLIC_THUNDER_URL / PUBLIC_CONSOLE_URL from the project .env into the
# current shell, then derive PUBLIC_THUNDER_HOST / PORT / SCHEME from the URL.
# Exits non-zero if .env is missing or doesn't define both URLs.
load_public_urls() {
    local env_file="${1:-${SCRIPT_DIR:-.}/../.env}"
    PUBLIC_THUNDER_URL=""
    PUBLIC_CONSOLE_URL=""
    if [ -f "$env_file" ]; then
        PUBLIC_THUNDER_URL="$(grep -E '^PUBLIC_THUNDER_URL=' "$env_file" | head -1 | cut -d= -f2-)"
        PUBLIC_CONSOLE_URL="$(grep -E '^PUBLIC_CONSOLE_URL=' "$env_file" | head -1 | cut -d= -f2-)"
    fi
    # First-install fallback: .env doesn't exist yet, so use local defaults.
    : "${PUBLIC_THUNDER_URL:=http://thunder.openchoreo.localhost:8080}"
    : "${PUBLIC_CONSOLE_URL:=http://localhost:8090}"
    # Strip trailing slash for consistency
    PUBLIC_THUNDER_URL="${PUBLIC_THUNDER_URL%/}"
    PUBLIC_CONSOLE_URL="${PUBLIC_CONSOLE_URL%/}"

    # Derive scheme / host / port
    if [[ "$PUBLIC_THUNDER_URL" == https://* ]]; then
        PUBLIC_THUNDER_SCHEME="https"
        local default_port=443
    else
        PUBLIC_THUNDER_SCHEME="http"
        local default_port=80
    fi
    local hostport="${PUBLIC_THUNDER_URL#*://}"
    hostport="${hostport%%/*}"
    if [[ "$hostport" == *:* ]]; then
        PUBLIC_THUNDER_HOST="${hostport%:*}"
        PUBLIC_THUNDER_PORT="${hostport##*:}"
    else
        PUBLIC_THUNDER_HOST="$hostport"
        PUBLIC_THUNDER_PORT="$default_port"
    fi
    export PUBLIC_THUNDER_URL PUBLIC_CONSOLE_URL \
           PUBLIC_THUNDER_HOST PUBLIC_THUNDER_PORT PUBLIC_THUNDER_SCHEME
}

# Render a Helm values file with `${PUBLIC_*}` placeholders into a temp file
# (post-processing dedupes any duplicate hostnames the substitution produced —
# in local mode PUBLIC_THUNDER_HOST equals thunder.openchoreo.localhost).
# Echoes the rendered file path on stdout.
render_values_file() {
    local src="$1"
    local rendered
    rendered="$(mktemp -t "asdlc-values.XXXXXX.yaml")"
    # Only expand the public URL placeholders — bootstrap scripts contain
    # bash variables like ${SCRIPT_DIR} that must NOT be touched.
    envsubst '${PUBLIC_THUNDER_URL} ${PUBLIC_THUNDER_HOST} ${PUBLIC_THUNDER_PORT} ${PUBLIC_THUNDER_SCHEME} ${PUBLIC_CONSOLE_URL}' < "$src" > "$rendered"
    # Dedupe consecutive identical YAML list items (handles HTTPRoute hostnames)
    python3 - "$rendered" <<'PY'
import sys, pathlib
p = pathlib.Path(sys.argv[1])
out, prev = [], None
for line in p.read_text().splitlines():
    stripped = line.strip()
    if stripped.startswith("- ") and stripped == prev:
        continue
    out.append(line)
    prev = stripped if stripped.startswith("- ") else None
p.write_text("\n".join(out) + "\n")
PY
    echo "$rendered"
}

# Patch the running cluster to match the current PUBLIC_* env vars.
# Surgical kubectl patches, not `helm upgrade` — avoids field-manager conflicts
# with prior kubectl-replace/kubectl-patch operations on the same fields.
# Idempotent: skips work when the live state already matches.
apply_public_urls_to_cluster() {
    if ! kubectl get ns thunder >/dev/null 2>&1; then
        echo "⚠️  thunder namespace not found — skipping public-URL sync"
        return 0
    fi

    echo "🔄 Syncing public URLs to cluster…"
    echo "   thunder: ${PUBLIC_THUNDER_URL}"
    echo "   console: ${PUBLIC_CONSOLE_URL}"

    local current_public_url
    current_public_url="$(kubectl -n thunder get cm thunder-config-map \
        -o jsonpath='{.data.deployment\.yaml}' 2>/dev/null \
        | sed -nE 's/^[[:space:]]*public_url:[[:space:]]*"([^"]+)".*/\1/p' | head -1)"

    if [ "$current_public_url" != "$PUBLIC_THUNDER_URL" ]; then
        # Fetch each ConfigMap data field as a decoded plain string (kubectl
        # jsonpath unescapes YAML flow-scalar escapes), rewrite the URL fields
        # with real text, then rebuild the ConfigMap via dry-run + replace.
        local dep_yaml console_js gate_js
        local f_dep f_console f_gate
        f_dep="$(mktemp)"; f_console="$(mktemp)"; f_gate="$(mktemp)"
        kubectl -n thunder get cm thunder-config-map -o jsonpath='{.data.deployment\.yaml}'    > "$f_dep"
        kubectl -n thunder get cm thunder-config-map -o jsonpath='{.data.console-config\.js}'  > "$f_console"
        kubectl -n thunder get cm thunder-config-map -o jsonpath='{.data.gate-config\.js}'     > "$f_gate"

        python3 - "$f_dep" "$f_console" "$f_gate" \
                  "$PUBLIC_THUNDER_URL" "$PUBLIC_CONSOLE_URL" \
                  "$PUBLIC_THUNDER_HOST" "$PUBLIC_THUNDER_PORT" "$PUBLIC_THUNDER_SCHEME" <<'PY'
import sys, re, pathlib
(f_dep, f_console, f_gate,
 thunder_url, console_url, thunder_host,
 gate_port, gate_scheme) = sys.argv[1:]
gate_port = int(gate_port)

# Console + gate config.js: only public_url to swap
for f in (f_console, f_gate):
    p = pathlib.Path(f)
    p.write_text(re.sub(r'public_url:\s*"[^"]*"',
                        f'public_url: "{thunder_url}"', p.read_text()))

# deployment.yaml: public_url, gate_client block, cors origins
p = pathlib.Path(f_dep)
text = p.read_text()
text = re.sub(r'public_url:\s*"[^"]*"', f'public_url: "{thunder_url}"', text)

def fix_gate_client(m):
    block = m.group(0)
    block = re.sub(r'(hostname:\s*)"[^"]*"', f'\\1"{thunder_host}"', block)
    block = re.sub(r'(port:\s*)\d+', f'\\g<1>{gate_port}', block)
    block = re.sub(r'(scheme:\s*)"[^"]*"', f'\\1"{gate_scheme}"', block)
    return block
text = re.sub(r'gate_client:\n(?:\s+\S.*\n){2,6}', fix_gate_client, text, count=1)

origins = [
    "http://openchoreo.localhost:8080",
    "http://localhost:7007",
    "http://localhost:8090",
    thunder_url,
    console_url,
]
seen, dedup = set(), []
for o in origins:
    if o not in seen: seen.add(o); dedup.append(o)
new_block = "cors:\n  allowed_origins:\n" + "".join(
    f'    - "{o}"\n' for o in dedup)
text = re.sub(
    r'cors:\n\s*allowed_origins:\n(?:\s*-\s*"[^"]*"\n)+',
    new_block, text, count=1)
p.write_text(text)
PY

        # Recreate the ConfigMap from the rewritten files (dry-run + replace
        # preserves namespace + name; data is fully replaced from --from-file).
        kubectl create configmap thunder-config-map \
            --namespace=thunder --dry-run=client -o yaml \
            --from-file=deployment.yaml="$f_dep" \
            --from-file=console-config.js="$f_console" \
            --from-file=gate-config.js="$f_gate" \
            | kubectl replace -f - >/dev/null
        rm -f "$f_dep" "$f_console" "$f_gate"

        # Update Thunder's HTTPRoute so it routes the public hostname.
        local hostnames_json
        if [ "$PUBLIC_THUNDER_HOST" = "thunder.openchoreo.localhost" ]; then
            hostnames_json='["thunder.openchoreo.localhost"]'
        else
            hostnames_json="[\"thunder.openchoreo.localhost\",\"$PUBLIC_THUNDER_HOST\"]"
        fi
        kubectl -n thunder patch httproute thunder-httproute --type=merge \
            -p "{\"spec\":{\"hostnames\":${hostnames_json}}}" >/dev/null

        # Update the asdlc-console-client redirect_uris in Thunder's SQLite.
        local pod
        pod="$(kubectl -n thunder get pod -l app.kubernetes.io/name=thunder \
                -o jsonpath='{.items[0].metadata.name}' 2>/dev/null)"
        if [ -n "$pod" ]; then
            kubectl -n thunder exec -i "$pod" -- sqlite3 \
                /opt/thunder/repository/database/configdb.db <<SQL >/dev/null
UPDATE APP_OAUTH_INBOUND_CONFIG
SET OAUTH_CONFIG_JSON = json_set(
  OAUTH_CONFIG_JSON,
  '\$.redirect_uris',
  json('["http://localhost:8090","${PUBLIC_CONSOLE_URL}"]'))
WHERE CLIENT_ID = 'asdlc-console-client';
SQL
            # Clear stale OAuth/flow state from prior public URL.
            kubectl -n thunder exec -i "$pod" -- sqlite3 \
                /opt/thunder/repository/database/runtimedb.db <<'SQL' >/dev/null 2>&1 || true
DELETE FROM FLOW_CONTEXT;
DELETE FROM AUTHORIZATION_REQUEST;
DELETE FROM AUTHORIZATION_CODE;
DELETE FROM FLOW_USER_DATA;
PRAGMA wal_checkpoint(TRUNCATE);
SQL
        fi

        kubectl -n thunder rollout restart deployment thunder-deployment >/dev/null
        kubectl -n thunder rollout status deployment thunder-deployment --timeout=120s >/dev/null
        echo "   ✓ thunder ConfigMap, HTTPRoute, redirect_uris updated"
    fi

    # OpenChoreo API: only the OIDC issuer changes. Patch its ConfigMap directly.
    if kubectl get cm openchoreo-api-config -n openchoreo-control-plane >/dev/null 2>&1; then
        local current_issuer
        current_issuer="$(kubectl -n openchoreo-control-plane get cm openchoreo-api-config \
            -o jsonpath='{.data.config\.yaml}' \
            | sed -nE 's/^[[:space:]]*issuer:[[:space:]]*"([^"]+)".*/\1/p' | head -1)"
        if [ "$current_issuer" != "$PUBLIC_THUNDER_URL" ]; then
            local cm_yaml
            cm_yaml="$(mktemp)"
            kubectl -n openchoreo-control-plane get cm openchoreo-api-config -o yaml > "$cm_yaml"
            python3 - "$cm_yaml" "$PUBLIC_THUNDER_URL" <<'PY'
import sys, re, pathlib
path, issuer = sys.argv[1:]
p = pathlib.Path(path)
text = p.read_text()
text = re.sub(r'(issuer:\s*)"[^"]*"', f'\\g<1>"{issuer}"', text, count=1)
p.write_text(text)
PY
            kubectl replace -f "$cm_yaml" >/dev/null
            kubectl -n openchoreo-control-plane rollout restart deploy/openchoreo-api >/dev/null
            rm -f "$cm_yaml"
            echo "   ✓ openchoreo-api OIDC issuer updated"
        fi
    fi

    echo "✅ Public URLs synced"
}

generate_machine_ids() {
    local cluster_name="$1"
    echo "🆔 Generating machine IDs..."
    local nodes
    nodes=$(k3d node list -o json | grep -o '"name"[[:space:]]*:[[:space:]]*"[^"]*"' | sed 's/"name"[[:space:]]*:[[:space:]]*"//;s/"$//' | grep "^k3d-${cluster_name}-")
    for node in $nodes; do
        docker exec "$node" sh -c "cat /proc/sys/kernel/random/uuid | tr -d '-' > /etc/machine-id" 2>/dev/null || true
    done
    echo "✅ Machine IDs generated"
}
