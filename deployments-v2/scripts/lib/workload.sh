#!/usr/bin/env bash
# lib/workload.sh — render_workload, apply_workload, patch_workload_image.
# Functions defined here; sourced by setup.sh / dev-cycle.sh.

set -u

render_workload() {
  local component=$1   # e.g. app-factory-api
  local src_dir=$2     # e.g. asdlc-service
  local image=$3       # e.g. asdlc.local/app-factory-api:9d5f3b8e
  local overlay="$ROOT_DIR/deployments-v2/manifests/env-overlays/${component}.yaml"
  local fragment="$src_dir/workload.yaml"

  [ -f "$fragment" ] || die "missing source workload fragment: $fragment"
  [ -f "$overlay" ]  || die "missing env overlay: $overlay"

  if ! command -v yq &>/dev/null; then
    log_warn "yq (mikefarah/yq) not installed — installing via brew"
    if command -v brew &>/dev/null; then
      brew install yq 2>/dev/null || die "brew install yq failed"
    else
      die "yq not installed and brew not available. Install yq: https://github.com/mikefarah/yq"
    fi
  fi

  yq eval-all '
    select(fileIndex == 0) as $src |
    select(fileIndex == 1) as $ovl |
    select(fileIndex == 0) |
    {
      "apiVersion": "openchoreo.dev/v1alpha1",
      "kind": "Workload",
      "metadata": {"name": $src.metadata.name},
      "spec": {
        "owner": {"componentName": $src.metadata.name, "projectName": "app-factory"},
        "endpoints": ($src.endpoints | .[] | {(.name): del(.name)}),
        "dependencies": $src.dependencies,
        "container": ({"image": "'"$image"'"} + $ovl)
      }
    }
  ' "$fragment" "$overlay" | envsubst | yq 'del(.spec.container.env[] | select(.value == ""))'
}

# Workloads land in `wso2cloud` to align with WSO2 Cloud convention —
# OC's ReleaseBinding controller resolves Workload-level secretKeyRefs
# against the Workload's namespace (controller.go:336-350), so the
# Workload, its ReleaseBinding, and its referenced SecretReferences all
# live in `wso2cloud`. Pods still run in the data-plane release namespace
# (`dp-default-app-factory-development-…`) — derived from the
# ReleaseBinding's project + environment, independent of where the
# Workload CR was applied.
WORKLOAD_NAMESPACE="${WORKLOAD_NAMESPACE:-wso2cloud}"

apply_workload() {
  local component=$1 src_dir=$2 image=$3
  render_workload "$component" "$src_dir" "$image" | kubectl apply -f - -n "$WORKLOAD_NAMESPACE"
}

patch_workload_image() {
  local component=$1 image=$2
  kubectl patch workload "$component" -n "$WORKLOAD_NAMESPACE" --type=merge \
    -p "{\"spec\":{\"container\":{\"image\":\"$image\"}}}"
}
