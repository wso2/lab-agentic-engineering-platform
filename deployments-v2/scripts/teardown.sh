#!/usr/bin/env bash
set -euo pipefail

_cleanup() { spinner_stop 2>/dev/null || true; kill 0 2>/dev/null || true; }
trap _cleanup EXIT INT TERM

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT_DIR="$(cd "$SCRIPT_DIR/../.." && pwd)"

source "$SCRIPT_DIR/lib/ui.sh"
source "$SCRIPT_DIR/lib/cluster.sh"
source "$SCRIPT_DIR/components.sh"

ALL=0
case "${1:-}" in
  --all) ALL=1 ;;
  -h|--help)
    cat <<EOF
Usage: teardown.sh [--all]

Default: remove asdlc workloads + manifests + OpenBao seeds (cluster stays).
--all:   destroy the entire k3d cluster (typed-yes confirmation required).
EOF
    exit 0 ;;
esac

if [ "$ALL" = 1 ]; then
  echo ""
  log_warn "This will destroy the entire k3d cluster '${K3D_CLUSTER_NAME}'."
  echo -n "Type 'yes' to confirm: "
  read -r confirm
  if [ "$confirm" != "yes" ]; then
    log_info "aborted"
    exit 0
  fi
  log_info "deleting k3d cluster '${K3D_CLUSTER_NAME}'..."
  k3d cluster delete "$K3D_CLUSTER_NAME" || log_warn "cluster delete failed (may not exist)"
  log_ok "cluster deleted"
  exit 0
fi

log_section "ASDLC teardown (default)"

# 1. Delete asdlc Workloads from wso2cloud namespace.
log_step "removing asdlc Workloads"
for row in "${COMPONENTS[@]}"; do
  IFS='|' read -r name _ <<<"$row"
  if kubectl get workload "$name" -n wso2cloud >/dev/null 2>&1; then
    kubectl delete workload "$name" -n wso2cloud --ignore-not-found
    log_info "deleted workload $name"
  else
    log_skip "workload $name not found"
  fi
done

# 2. Delete postgres resources.
log_step "removing postgres"
kubectl delete deployment app-factory-postgresql -n wso2cloud --ignore-not-found 2>/dev/null || true
kubectl delete service app-factory-postgresql -n wso2cloud --ignore-not-found 2>/dev/null || true
kubectl delete secret app-factory-postgresql -n wso2cloud --ignore-not-found 2>/dev/null || true
kubectl delete pvc app-factory-postgresql-pvc -n wso2cloud --ignore-not-found 2>/dev/null || true
log_info "postgres resources removed"

# 3. Remove asdlc k3d images.
log_step "pruning asdlc images from k3d"
for row in "${COMPONENTS[@]}"; do
  IFS='|' read -r name _ <<<"$row"
  # List and delete images tagged asdlc.local/$name:* from the k3d cluster's containerd.
  docker exec "k3d-${K3D_CLUSTER_NAME}-server-0" sh -c \
    "crictl images -q '*/${name}:*' | xargs -r crictl rmi" 2>/dev/null || log_skip "image prune for $name skipped"
  log_info "pruned images for $name"
done

# 4. Remove OpenBao seeds (if OpenBao pod is running).
log_step "deleting OpenBao seeds"
if kubectl get pod openbao-0 -n openbao >/dev/null 2>&1; then
  kubectl exec -n openbao openbao-0 -- sh -c "
    bao kv metadata delete secret/apps/anthropic 2>/dev/null || true
    bao kv metadata delete secret/apps/github-platform-pat 2>/dev/null || true
    bao kv metadata delete secret/apps/github-webhook 2>/dev/null || true
    bao kv metadata delete secret/apps/bff-task-signing-key 2>/dev/null || true
  " 2>/dev/null || log_warn "OpenBao seed deletion failed (cluster may be up but OpenBao not ready)"
  log_info "OpenBao seeds deleted"
else
  log_skip "OpenBao pod not running — skipping seed deletion"
fi

# 5. Clean local generated files.
log_step "cleaning local state"
rm -f "$ROOT_DIR/deployments-v2/.env" "$ROOT_DIR/deployments-v2/keys/task-signing.pem"
log_info ".env and keys removed"

log_section "Teardown complete"
log_info "Cluster '${K3D_CLUSTER_NAME}' is still running."
log_info "Run with --all to destroy it."
