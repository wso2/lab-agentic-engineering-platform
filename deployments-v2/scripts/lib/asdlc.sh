#!/usr/bin/env bash
# lib/asdlc.sh — OpenBao seeding, postgres, workload bootstrap.
# Functions defined here; sourced by setup.sh.

set -u

seed_openbao() {
  if [ "${DRY_RUN:-0}" = 1 ]; then
    log_skip "[dry-run] would seed OpenBao secrets"
    return
  fi
  spinner "Waiting for OpenBao to be ready" "1 min" -- \
    kubectl wait --for=condition=Ready pod -n openbao -l app.kubernetes.io/name=openbao --timeout=180s || true

  log_info "seeding 4 secrets via bao kv put"

  kubectl exec -n openbao openbao-0 -- sh -c "
    bao kv put secret/apps/anthropic            key='${ANTHROPIC_API_KEY}' >/dev/null
    bao kv put secret/apps/github-platform-pat  value='${GITHUB_PLATFORM_PAT}' >/dev/null
    bao kv put secret/apps/github-webhook       delivery_url='${GITHUB_WEBHOOK_DELIVERY_URL}' secret='${GITHUB_WEBHOOK_SECRET}' >/dev/null
  " || log_warn "bao kv put for text secrets failed — OpenBao may already be seeded"

  local PEM_B64
  PEM_B64=$(base64 < "$KEYS_DIR/task-signing.pem" | tr -d '\n')
  kubectl exec -n openbao openbao-0 -- sh -c "
    printf %s '${PEM_B64}' | base64 -d > /tmp/pem
    bao kv put secret/apps/bff-task-signing-key pem=@/tmp/pem >/dev/null
    rm -f /tmp/pem
  " || log_warn "bao kv put for task-signing-key failed — OpenBao may already be seeded"

  log_ok "OpenBao seeded"
}

apply_postgres() {
  local manifest="$ROOT_DIR/deployments-v2/manifests/app-factory-postgresql.yaml"
  if [ ! -f "$manifest" ]; then
    die "missing postgres manifest: $manifest"
  fi
  if [ "${DRY_RUN:-0}" = 1 ]; then
    log_skip "[dry-run] would apply postgres from $manifest"
    return
  fi
  kubectl apply -f "$manifest" -n wso2cloud
  spinner "Waiting for PostgreSQL" "2 min" -- \
    kubectl wait --for=condition=Available deployment/app-factory-postgresql -n wso2cloud --timeout=120s || true
  log_ok "postgres applied"
}

bootstrap_workloads() {
  if [ "${DRY_RUN:-0}" = 1 ]; then
    log_skip "[dry-run] would build + render + apply 5 workloads"
    return
  fi

  # Ensure env vars used in env-overlay templates are exported for envsubst.
  export GITHUB_REPO_OWNER="${GITHUB_REPO_OWNER:-}"
  export GITHUB_PLATFORM_PAT="${GITHUB_PLATFORM_PAT:-}"
  export GITHUB_WEBHOOK_DELIVERY_URL="${GITHUB_WEBHOOK_DELIVERY_URL:-}"
  export GITHUB_WEBHOOK_SECRET="${GITHUB_WEBHOOK_SECRET:-}"
  export PUBLIC_THUNDER_URL="${PUBLIC_THUNDER_URL:-}"
  export PUBLIC_CONSOLE_URL="${PUBLIC_CONSOLE_URL:-}"

  log_info "bootstrapping ${#COMPONENTS[@]} workloads (build + import + apply)"
  source "$ROOT_DIR/deployments-v2/scripts/components.sh"
  source "$ROOT_DIR/deployments-v2/scripts/lib/images.sh"
  source "$ROOT_DIR/deployments-v2/scripts/lib/workload.sh"

  for row in "${COMPONENTS[@]}"; do
    IFS='|' read -r name src dockerfile context <<<"$row"
    local hash image
    hash=$(content_hash "$ROOT_DIR/$src")
    image="asdlc.local/${name}:${hash}"
    log_step "$name"
    build_image "$name" "$ROOT_DIR/$src" "$dockerfile" "$context" "$image"
    import_image "$image"
    apply_workload "$name" "$ROOT_DIR/$src" "$image"
  done

  log_ok "workloads bootstrapped"

  if [ "${#RUNNER_IMAGES[@]}" -gt 0 ]; then
    log_info "bootstrapping ${#RUNNER_IMAGES[@]} runner image(s) (build + import)"
    for row in "${RUNNER_IMAGES[@]}"; do
      IFS='|' read -r name src dockerfile context <<<"$row"
      local image
      image="asdlc.local/${name}:local"
      log_step "$name (runner image)"
      build_image "$name" "$ROOT_DIR/$src" "$dockerfile" "$context" "$image"
      import_image "$image"
    done
    log_ok "runner images imported"
  fi
}

# discover_console_origin polls for the console HTTPRoute hostname (OpenChoreo
# creates it asynchronously during workload bootstrap).  Returns the origin URL
# on stdout, or empty string on timeout.
discover_console_origin() {
  local hr_host start now elapsed timeout=120
  start=$(date +%s)
  while true; do
    hr_host=$(kubectl get httproute -A -o jsonpath='{.items[?(@.metadata.name=="app-factory-console-http-b5d082d5")].spec.hostnames[0]}' 2>/dev/null || true)
    if [ -n "$hr_host" ]; then
      echo "http://${hr_host}:19080"
      return 0
    fi
    now=$(date +%s)
    elapsed=$((now - start))
    if [ $elapsed -ge $timeout ]; then
      return 1
    fi
    sleep 3
  done
}

# wait_for_thunder_ready waits for the Thunder pod to be Ready (used after
# HelmRelease changes that trigger a pod restart — e.g. CORS patch).
wait_for_thunder_ready() {
  local timeout=${1:-180}
  kubectl wait --for=condition=Ready pod -n thunder -l app.kubernetes.io/name=thunder --timeout="${timeout}s" 2>/dev/null || {
    log_warn "Thunder pod did not become ready within ${timeout}s"
    return 1
  }
}

register_console_redirect_uri() {
  if [ "${DRY_RUN:-0}" = 1 ]; then
    log_skip "[dry-run] would register console redirect URI in Thunder"
    return
  fi

  local console_origin
  console_origin=$(discover_console_origin) || {
    log_warn "Could not discover console HTTPRoute hostname within timeout — skipping redirect URI registration"
    return
  }

  log_info "Registering console redirect URI in Thunder: $console_origin"

  local thunder_pod
  thunder_pod=$(kubectl get pods -n thunder -l app.kubernetes.io/name=thunder -o jsonpath='{.items[0].metadata.name}' 2>/dev/null || true)
  if [ -z "$thunder_pod" ]; then
    log_warn "Thunder pod not found — skipping redirect URI registration"
    return
  fi

  # Wait for the pod to be ready before exec (avoids race after pod restart)
  kubectl wait --for=condition=Ready pod -n thunder "$thunder_pod" --timeout=60s 2>/dev/null || {
    log_warn "Thunder pod not ready — skipping redirect URI registration"
    return
  }

  # Use sqlite3 directly (Thunder image has sqlite3 but not python3).
  # json_insert with [#] appends to the array (SQLite 3.38+).
  kubectl exec -n thunder "$thunder_pod" -- sh -c "
    sqlite3 /opt/thunder/repository/database/configdb.db \"
      UPDATE APP_OAUTH_INBOUND_CONFIG SET OAUTH_CONFIG_JSON = json_insert(
        OAUTH_CONFIG_JSON, '\\\$.redirect_uris[#]', '$console_origin'
      )
      WHERE CLIENT_ID = 'APP_FACTORY_CONSOLE'
        AND OAUTH_CONFIG_JSON IS NOT NULL
        AND OAUTH_CONFIG_JSON NOT LIKE '%$console_origin%';
    \"
  " 2>/dev/null || {
    log_warn "Redirect URI registration failed (sqlite3 error)"
    return
  }

  # Verify the URI was actually written
  local verify
  verify=$(kubectl exec -n thunder "$thunder_pod" -- sqlite3 /opt/thunder/repository/database/configdb.db \
    "SELECT OAUTH_CONFIG_JSON FROM APP_OAUTH_INBOUND_CONFIG WHERE CLIENT_ID='APP_FACTORY_CONSOLE';" 2>/dev/null || true)
  if echo "$verify" | grep -qF "$console_origin"; then
    log_ok "Redirect URI registered and verified"
  else
    log_warn "Redirect URI write could not be verified — manual check may be needed"
  fi
}

register_thunder_cors_origin() {
  if [ "${DRY_RUN:-0}" = 1 ]; then
    log_skip "[dry-run] would register console CORS origin in Thunder"
    return
  fi

  local console_origin
  console_origin=$(discover_console_origin) || {
    log_warn "Could not discover console HTTPRoute hostname within timeout — skipping CORS registration"
    return
  }

  log_info "Registering console CORS origin in Thunder HelmRelease: $console_origin"

  # Check if the origin is already in the CORS list
  if kubectl get hr/thunder -n thunder -o jsonpath='{.spec.values.configuration.cors.allowedOrigins}' 2>/dev/null | grep -qF "$console_origin"; then
    log_info "CORS origin already registered: $console_origin"
    return
  fi

  # Add the origin to the Thunder HelmRelease CORS allowedOrigins list.
  # Flux will reconcile and restart Thunder to pick up the change.
  kubectl patch hr/thunder -n thunder --type=json \
    -p="[{\"op\": \"add\", \"path\": \"/spec/values/configuration/cors/allowedOrigins/-\", \"value\": \"$console_origin\"}]" 2>/dev/null || {
    log_warn "Failed to patch Thunder HelmRelease CORS"
    return
  }

  # Trigger reconcile so Thunder picks up the new CORS origin promptly.
  kubectl annotate hr/thunder -n thunder \
    reconcile.fluxcd.io/requestedAt="$(date -u +%Y-%m-%dT%H:%M:%SZ)" --overwrite 2>/dev/null || true

  log_ok "CORS origin registered — waiting for Thunder rollout"
  wait_for_thunder_ready 180
}

register_streaming_timeouts() {
  if [ "${DRY_RUN:-0}" = 1 ]; then
    log_skip "[dry-run] would apply SSE streaming timeout TrafficPolicy"
    return
  fi

  local dp_ns
  dp_ns=$(kubectl get httproute -A -o jsonpath='{.items[?(@.metadata.name=="app-factory-console-http-b5d082d5")].metadata.namespace}' 2>/dev/null || true)
  if [ -z "$dp_ns" ]; then
    log_warn "Could not discover console DP namespace — skipping streaming timeout policy"
    return
  fi

  # Clean up any stale copy from the default namespace (can't target
  # cross-namespace HTTPRoute).
  kubectl delete trafficpolicy app-factory-console-streaming -n default --ignore-not-found 2>/dev/null || true

  log_info "Applying SSE streaming timeout TrafficPolicy in $dp_ns"

  kubectl apply -f - <<EOF
apiVersion: gateway.kgateway.dev/v1alpha1
kind: TrafficPolicy
metadata:
  name: app-factory-console-streaming
  namespace: $dp_ns
spec:
  targetRefs:
    - group: gateway.networking.k8s.io
      kind: HTTPRoute
      name: app-factory-console-http-b5d082d5
  timeouts:
    request: 300s
    streamIdle: 60s
EOF
  log_ok "SSE streaming timeout policy applied"
}

register_service_oauth_clients() {
  if [ "${DRY_RUN:-0}" = 1 ]; then
    log_skip "[dry-run] would register service-to-service OAuth clients in Thunder"
    return
  fi

  local thunder_pod
  thunder_pod=$(kubectl get pods -n thunder -l app.kubernetes.io/name=thunder -o jsonpath='{.items[0].metadata.name}' 2>/dev/null || true)
  if [ -z "$thunder_pod" ]; then
    log_warn "Thunder pod not found — skipping service OAuth client registration"
    return
  fi

  kubectl wait --for=condition=Ready pod -n thunder "$thunder_pod" --timeout=60s 2>/dev/null || true

  # Resolve the APPLICATION ID (the app all OAuth clients belong to).
  local app_id
  app_id=$(kubectl exec -n thunder "$thunder_pod" -- sqlite3 /opt/thunder/repository/database/configdb.db \
    "SELECT ID FROM APPLICATION LIMIT 1;" 2>/dev/null || true)
  if [ -z "$app_id" ]; then
    log_warn "No Thunder APPLICATION found — skipping service OAuth client registration"
    return
  fi

  # Service-to-service client config (client_credentials grant).
  local oauth_config='{"redirect_uris":null,"grant_types":["client_credentials"],"response_types":[],"token_endpoint_auth_method":"client_secret_post","pkce_required":false,"public_client":false,"token":{"access_token":{"validity_period":3600},"id_token":{"validity_period":3600}},"user_info":{"response_type":"JSON"}}'

  local registered=0 skipped=0
  for pair in \
    "asdlc-bff-to-agents-service:asdlc-bff-to-agents-service-secret" \
    "asdlc-bff-to-git-service:asdlc-bff-to-git-service-secret"; do
    local client_id="${pair%%:*}" client_secret="${pair##*:}"

    local exists
    exists=$(kubectl exec -n thunder "$thunder_pod" -- sqlite3 /opt/thunder/repository/database/configdb.db \
      "SELECT COUNT(*) FROM APP_OAUTH_INBOUND_CONFIG WHERE CLIENT_ID='$client_id';" 2>/dev/null || echo 0)

    if [ "$exists" != "0" ]; then
      log_info "service OAuth client already registered: $client_id"
      skipped=$((skipped + 1))
      continue
    fi

    # Thunder stores client secrets as base64(SHA-256(plaintext)),
    # matching the format of the seeded openchoreo-system-app.
    # Direct SQL insert with plaintext will fail — endpoints return
    # "invalid_client" because the stored hash doesn't match.
    local secret_hash
    secret_hash=$(echo -n "$client_secret" | shasum -a 256 | cut -d' ' -f1 | xxd -r -p | base64)

    # Use the same DEPLOYMENT_ID as all other registry-scoped OAuth clients.
    kubectl exec -n thunder "$thunder_pod" -- sqlite3 /opt/thunder/repository/database/configdb.db \
      "INSERT INTO APP_OAUTH_INBOUND_CONFIG (DEPLOYMENT_ID, CLIENT_ID, CLIENT_SECRET, APP_ID, OAUTH_CONFIG_JSON)
       VALUES ('default-deployment', '$client_id', '$secret_hash', '$app_id', '$oauth_config');" 2>/dev/null && {
      log_info "registered service OAuth client: $client_id"
      registered=$((registered + 1))
    } || log_warn "failed to register service OAuth client: $client_id"
  done

  local total=$((registered + skipped))
  if [ $total -gt 0 ]; then
    log_ok "service OAuth clients: $registered registered, $skipped already present"
  fi
}

print_login_banner() {
  local console_url="${PUBLIC_CONSOLE_URL:-http://localhost:8090}"
  local admin_user="${ADMIN_USERNAME:-admin@openchoreo.dev}"
  local admin_pass="${ADMIN_PASSWORD:-Admin@123}"

  # Try to discover the actual console HTTPRoute hostname from the cluster.
  if [ "${DRY_RUN:-0}" != 1 ]; then
    local hr_host
    hr_host=$(kubectl get httproute -A -o jsonpath='{.items[?(@.metadata.name=="app-factory-console-http-b5d082d5")].spec.hostnames[0]}' 2>/dev/null || true)
    if [ -n "$hr_host" ]; then
      console_url="http://${hr_host}:19080"
    fi
  fi

  local dim='\033[2m'
  local nc='\033[0m'

  echo ""
  printf "  ${dim}%s${nc}\n" "$console_url"
  printf "  ${dim}%s / %s${nc}\n" "$admin_user" "$admin_pass"
  echo ""
  log_info "Stream logs:"
  log_info "  bash deployments-v2/scripts/logs.sh"
  log_info ""
  log_info "Rebuild after source changes:"
  log_info "  bash deployments-v2/scripts/dev-cycle.sh"
  log_info ""
  log_info "Webhook relay (run in another terminal for PR/push/merge testing):"
  log_info "  bash deployments-v2/scripts/webhook-relay.sh"
  log_info ""
  printf "  ${dim}OpenChoreo: http://openchoreo.localhost:19080  (same login)${nc}\n"
  echo ""
}
