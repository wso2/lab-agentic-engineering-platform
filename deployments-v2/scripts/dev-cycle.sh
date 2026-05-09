#!/usr/bin/env bash
set -euo pipefail

_cleanup() { spinner_stop 2>/dev/null || true; }
trap _cleanup EXIT INT TERM

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT_DIR="$(cd "$SCRIPT_DIR/../.." && pwd)"

source "$SCRIPT_DIR/lib/ui.sh"
source "$SCRIPT_DIR/lib/env.sh"
source "$SCRIPT_DIR/lib/cluster.sh"
source "$SCRIPT_DIR/lib/images.sh"
source "$SCRIPT_DIR/lib/workload.sh"
source "$SCRIPT_DIR/components.sh"

FILTER=""
NO_ROLLOUT_WAIT=0

for arg in "$@"; do
  case "$arg" in
    -h|--help)
      cat <<EOF
Usage: dev-cycle.sh [<component>] [--no-rollout-wait]

Detects source changes per component, rebuilds + imports + patches Workload.

  <component>   Optional: process only that one component (e.g. app-factory-api).
                Without arg: iterate all 5, skip unchanged.

  --no-rollout-wait  Don't wait for rollout to complete after patching.
EOF
      exit 0 ;;
    --no-rollout-wait) NO_ROLLOUT_WAIT=1 ;;
    *) FILTER="$arg" ;;
  esac
done

ensure_env_loaded
ensure_cluster_reachable

_dc_cycle() {
  local total=0 rebuilt=0 name src dockerfile context hash_paths src_dir hash image current_image current_hash dp_ns hash_dirs p
  for row in "${COMPONENTS[@]}"; do
    IFS='|' read -r name src dockerfile context hash_paths <<<"$row"
    [ -z "$hash_paths" ] && hash_paths="$src"

    if [ -n "$FILTER" ] && [ "$name" != "$FILTER" ]; then
      continue
    fi

    total=$((total + 1))
    src_dir="$ROOT_DIR/$src"
    hash_dirs=()
    for p in $hash_paths; do hash_dirs+=("$ROOT_DIR/$p"); done
    hash=$(content_hash "${hash_dirs[@]}")
    image="asdlc.local/${name}:${hash}"

    log_step "$name"

    current_image=$(kubectl get workload "$name" -n default -o jsonpath='{.spec.container.image}' 2>/dev/null || echo "")
    current_hash="${current_image##*:}"

    if [ "$current_hash" = "$hash" ] && [ -n "$current_image" ]; then
      log_skip "no changes (hash: $hash)"
      continue
    fi

    rebuilt=$((rebuilt + 1))
    log_info "changes detected — rebuilding (hash: $hash, was: ${current_hash:-none})"

    build_image "$name" "$src_dir" "$dockerfile" "$context" "$image"
    import_image "$image"

    if kubectl get workload "$name" -n default >/dev/null 2>&1; then
      log_info "patching existing Workload"
      patch_workload_image "$name" "$image"
    else
      log_info "applying new Workload (bootstrap)"
      apply_workload "$name" "$src_dir" "$image"
    fi

    if [ "$NO_ROLLOUT_WAIT" = 0 ]; then
      kubectl rollout status deployment -l "openchoreo.dev/component=$name" -A --timeout=120s 2>/dev/null || log_warn "rollout status timed out for $name"
    fi

    log_ok "$name deployed"
  done

  echo ""
  if [ "$rebuilt" -eq 0 ]; then
    log_ok "$total/$total components unchanged"
  elif [ "$total" -gt 0 ]; then
    log_info "$rebuilt/$total components rebuilt"
  fi
}

_dc_cycle
