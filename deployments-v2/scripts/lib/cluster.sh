#!/usr/bin/env bash
# lib/cluster.sh — k3d cluster lifecycle (create / wait-ready / delete).
# Functions defined here; sourced by setup.sh / dev-cycle.sh / teardown.sh.

set -u

K3D_CLUSTER_NAME="openchoreo"
SUBMODULE_PATH="${ROOT_DIR}/deployments-v2/wso2cloud-deployment"
K3D_CONFIG="${SUBMODULE_PATH}/wso2cloud-local/k3d-config.yaml"

ensure_cluster_reachable() {
  kubectl cluster-info >/dev/null 2>&1 || die "no reachable k3d cluster (run setup.sh first)"
}

ensure_cluster() {
  if [ -f "$K3D_CONFIG" ]; then
    log_info "using k3d config: $K3D_CONFIG"
  else
    log_warn "k3d-config.yaml not found in submodule; using default cluster create"
    K3D_CONFIG=""
  fi

  if k3d cluster list --no-headers 2>/dev/null | awk '{print $1}' | grep -qx "$K3D_CLUSTER_NAME"; then
    log_skip "cluster '$K3D_CLUSTER_NAME' already exists"
  else
    if [ "${DRY_RUN:-0}" = 1 ]; then
      log_skip "[dry-run] would create k3d cluster '$K3D_CLUSTER_NAME'"
    else
      log_info "creating k3d cluster '$K3D_CLUSTER_NAME' (may take ~2 min)"
      if [ -n "$K3D_CONFIG" ]; then
        K3D_FIX_DNS=0 k3d cluster create --config "$K3D_CONFIG" \
          || die "k3d cluster create failed"
      else
        k3d cluster create "$K3D_CLUSTER_NAME" || die "k3d cluster create failed"
      fi
      log_info "applying machine-id fix (kine-on-SQLite needs stable id)"
      docker exec "k3d-${K3D_CLUSTER_NAME}-server-0" sh -c \
        "cat /proc/sys/kernel/random/uuid | tr -d '-' > /etc/machine-id" \
        || log_warn "machine-id fix failed (non-fatal — k3s might still work)"
    fi
  fi

  if [ "${DRY_RUN:-0}" = 1 ]; then
    log_skip "[dry-run] would wait for nodes Ready"
  else
    spinner "Waiting for cluster nodes" "1 min" -- \
      kubectl wait nodes --for=condition=Ready --all --timeout=180s
  fi
  log_ok "cluster ready"
}
