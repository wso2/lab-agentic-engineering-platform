#!/usr/bin/env bash
# lib/seed-admin-org.sh — local-dev mirror of `platform-api-service`'s
# tenant-onboarding step.
#
# In hosted WSO2 Cloud, when a user signs up via Thunder, the IDP fires a
# `notify_org_created` webhook to `platform-api-service`, which:
#   1. Creates the per-tenant OC namespace via OC's `POST /api/v1/namespaces`
#      (the K8s ns gets labelled `openchoreo.dev/control-plane: "true"`).
#   2. Applies the per-org bootstrap manifests inside that namespace
#      (Project, DeploymentPipeline, Environments, ComponentTypes, Traits,
#      DataPlane, WorkflowPlane, ObservabilityPlane refs).
#
# Local dev does not run platform-api-service. This script does the
# equivalent work imperatively at install time, against the K8s `default`
# namespace — which is the OU `default` (Thunder-seeded) tenant ns.
#
# The platform binary (BFF) is org-agnostic: it does NOT create namespaces
# in any environment. This script is the local-only mirror; on hosted
# environments, the equivalent work is done by `platform-api-service`.

set -u

# Inputs (read from .env via env.sh):
#   LOCAL_DEV_ADMIN_OUHANDLE — Thunder-issued ouHandle for the admin user.
#                              Defaults to `default`. The single place a
#                              tenant name appears in this repo's env / scripts.

seed_admin_org() {
  local script_name="seed-admin-org"
  local ouhandle="${LOCAL_DEV_ADMIN_OUHANDLE:-default}"

  if [ "${DRY_RUN:-0}" = 1 ]; then
    log_skip "[dry-run] would label ns ${ouhandle} + apply org-default-resources"
    return 0
  fi

  # 1. Label the K8s namespace so OC's namespace API recognises it as a
  # tenant ns. OC's `GetNamespace` filters by this label; without it the
  # ns is invisible to the OC control plane.
  if ! kubectl label namespace "${ouhandle}" openchoreo.dev/control-plane=true --overwrite >/dev/null 2>&1; then
    # Namespace doesn't exist yet (only true for handles other than `default`).
    # Create it and re-label.
    kubectl create namespace "${ouhandle}" >/dev/null 2>&1 || true
    kubectl label namespace "${ouhandle}" openchoreo.dev/control-plane=true --overwrite >/dev/null 2>&1 || {
      log_warn "${script_name}: could not label namespace ${ouhandle} — skipping"
      return 0
    }
  fi

  # 2. Apply the per-org bootstrap CRs. The submodule already ships these
  # under wso2cloud-local/orgdefaultresources/ — same content
  # platform-api-service applies in hosted (org-default-resources/dev/.../v1.0/cp).
  local resources_dir="${ROOT_DIR}/deployments-v2/wso2cloud-deployment/wso2cloud-local/orgdefaultresources"
  if [ ! -d "$resources_dir" ]; then
    log_warn "${script_name}: ${resources_dir} not found — skipping bootstrap apply"
    return 0
  fi

  # Apply each manifest into the tenant namespace. The DataPlane / WorkflowPlane
  # / ObservabilityPlane CRs are cluster-scoped; the rest land in the namespace.
  local applied=0 skipped=0
  for f in "$resources_dir"/*.yaml; do
    [ -f "$f" ] || continue
    if kubectl apply -n "$ouhandle" -f "$f" >/dev/null 2>&1; then
      applied=$((applied + 1))
    else
      # Cluster-scoped resources need apply without -n; retry.
      if kubectl apply -f "$f" >/dev/null 2>&1; then
        applied=$((applied + 1))
      else
        skipped=$((skipped + 1))
        log_warn "${script_name}: failed to apply $(basename "$f")"
      fi
    fi
  done

  log_ok "tenant namespace '${ouhandle}' bootstrapped (${applied} resources applied, ${skipped} skipped)"
}
