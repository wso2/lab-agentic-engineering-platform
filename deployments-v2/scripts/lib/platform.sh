#!/usr/bin/env bash
# lib/platform.sh — ordered kubectl apply -k against submodule layers.
# Functions defined here; sourced by setup.sh.

set -u

apply_platform() {
  local SUB="$SUBMODULE_PATH/wso2cloud-local"

  _apply_kustomize() {
    local path=$1
    log_info "applying $path"
    if [ "${DRY_RUN:-0}" = 1 ]; then
      log_skip "[dry-run] would apply $path"
      return
    fi
    local CHR_NAME="${CONSOLE_HTTPROUTE_NAME:-app-factory-console-http-b5d082d5}"
    local console_fallback="http://localhost:8090"
    kubectl kustomize "$path" \
      | PUBLIC_THUNDER_URL="${PUBLIC_THUNDER_URL:-http://thunder.openchoreo.localhost:8080}" \
        PUBLIC_CONSOLE_URL="${PUBLIC_CONSOLE_URL:-$console_fallback}" \
        CONSOLE_HTTPROUTE_NAME="$CHR_NAME" \
        envsubst '$PUBLIC_THUNDER_URL $PUBLIC_CONSOLE_URL $CONSOLE_HTTPROUTE_NAME' \
      | kubectl apply -f -
  }

  # _wait_for_hr <name> <namespace> [timeout_seconds]
  # Waits for a HelmRelease to become Ready with periodic status checks.
  # Default timeout 900s; pass a 3rd arg for HelmReleases with a long first-pull
  # (e.g. observability-plane / OpenSearch needs ~30 min cold).
  # On failure shows the HelmRelease status (conditions + events) so the
  # operator can see why reconciliation is not progressing (e.g. image pull,
  # dependency wait, cert issues).
  _wait_for_hr() {
    local name=$1 ns=$2 timeout_s=${3:-900}
    if [ "${DRY_RUN:-0}" = 1 ]; then
      log_skip "[dry-run] would wait for HelmRelease $name/$ns"
      return
    fi
    local timeout_label="${timeout_s}s"
    if [ "$timeout_s" -ge 60 ]; then
      timeout_label="$((timeout_s / 60)) min"
    fi
    log_step "Waiting for HelmRelease $name/$ns" "$timeout_label"
    if kubectl wait --for=condition=Ready "hr/$name" -n "$ns" --timeout="${timeout_s}s" 2>&1; then
      log_ok "HelmRelease $name/$ns ready"
      return 0
    fi
    log_warn "HelmRelease $name/$ns timed out after ${timeout_s}s — dumping status:"
    echo ""
    kubectl describe "hr/$name" -n "$ns" 2>/dev/null || true
    echo ""
    kubectl get events -n "$ns" --sort-by='.lastTimestamp' 2>/dev/null | tail -30 || true
    echo ""
    die "HelmRelease $name/$ns failed — check output above"
  }

  log_section "Platform bring-up"

  log_info "Gateway API CRDs (prerequisite for Gateways in layer-2)"
  if [ "${DRY_RUN:-0}" != 1 ]; then
    kubectl apply -k "$SUB/init/layer-0/gateway-api" --server-side=true
  else
    log_skip "[dry-run] would apply Gateway API CRDs"
  fi

  log_info "Installing Flux controllers (required for HelmRelease resources)"
  if [ "${DRY_RUN:-0}" != 1 ]; then
    if kubectl get crd helmreleases.helm.toolkit.fluxcd.io >/dev/null 2>&1; then
      log_skip "Flux already installed"
    else
      FLUX_INSTALL_URL="${FLUX_INSTALL_URL:-https://github.com/fluxcd/flux2/releases/latest/download/install.yaml}"
      kubectl apply -f "$FLUX_INSTALL_URL"
      log_ok "Flux manifests applied"
      spinner "Waiting for Flux controllers (optional)" "1 min" -- \
        kubectl wait --for=condition=Available deployment --all -n flux-system --timeout=120s \
        || true
    fi
  fi

  log_info "Layer 0: namespaces + Helm repos + tools"
  _apply_kustomize "$SUB/init/layer-0"

  # Patch ESO HelmRelease to remove dual CRD installation (install.crds + values.installCRDs
  # conflict — the chart handles CRDs via values.installCRDs: true; Flux's install.crds
  # CreateReplace races with it). Keep upgrade.crds intact.
  if [ "${DRY_RUN:-0}" != 1 ]; then
    kubectl patch helmrelease external-secrets -n external-secrets --type=json \
      -p '[{"op": "remove", "path": "/spec/install/crds"}]' 2>/dev/null || true
  fi

  # Wait for ESO CRDs and webhook before layer-1 (ClusterSecretStore depends on them).
  # Flux reconciles HelmRelease CRs asynchronously; image pulls can take 5+ min
  # on a fresh cluster.  We poll until the CRD exists and the webhook has endpoints.
  if [ "${DRY_RUN:-0}" != 1 ]; then
    local ESO_CRD="clustersecretstores.external-secrets.io" WAITED=0

    spinner_start "Waiting for ESO CRDs + webhook" "10 min"
    until kubectl get crd "$ESO_CRD" >/dev/null 2>&1; do
      sleep 10
      WAITED=$((WAITED + 10))
      if [ $WAITED -ge 600 ]; then
        spinner_stop
        log_warn "Timed out waiting for ESO CRD $ESO_CRD after ${WAITED}s"
        break 2
      fi
    done
    spinner_stop
    if kubectl get crd "$ESO_CRD" >/dev/null 2>&1; then
      log_ok "ESO CRD $ESO_CRD is available (waited ${WAITED}s)"
    else
      log_warn "ESO CRD $ESO_CRD not available — layer-1 may fail"
    fi

    # Wait for ESO webhook pod + endpoints together.  The webhook pod label
    # is app.kubernetes.io/name=external-secrets-webhook (not external-secrets)
    # and there is no component= label — so we match on the deployment directly.
    spinner "Waiting for ESO webhook" "10 min" -- \
      kubectl wait --for=condition=Available deployment/external-secrets-webhook \
        -n external-secrets --timeout=900s \
      || true

    spinner "Waiting for cert-manager" "2 min" -- \
      kubectl wait --for=condition=Available deployment --all -n cert-manager --timeout=300s || true
    spinner "Waiting for OpenChoreo control-plane" "1 min" -- \
      kubectl wait --for=condition=Available deployment --all -n openchoreo-control-plane --timeout=300s || true
    spinner "Waiting for OpenBao" "1 min" -- \
      kubectl wait --for=condition=Ready pod --all -n openbao --timeout=300s || true
  fi

  log_step "Layer 1: CSStores + Backstage + CoreDNS" "2 min"
  if [ "${DRY_RUN:-0}" != 1 ]; then
    # Retry layer-1 up to 10 times with 10s backoff; the webhook can take time
    # to start serving after the pod becomes Ready.
    local ATTEMPT=0
    until _apply_kustomize "$SUB/init/layer-1"; do
      ATTEMPT=$((ATTEMPT + 1))
      if [ $ATTEMPT -ge 10 ]; then
        log_warn "Layer 1 failed after $ATTEMPT attempts — continuing anyway"
        break
      fi
      log_warn "Layer 1 attempt $ATTEMPT failed — retrying in 10s..."
      sleep 10
    done
    kubectl rollout restart deployment -n kube-system coredns 2>/dev/null || true
    kubectl rollout status deployment -n kube-system coredns --timeout=120s 2>/dev/null || true
  else
    _apply_kustomize "$SUB/init/layer-1"
  fi

  # Wait for kgateway CRDs before layer-2 (layer-2 resources reference them).
  if [ "${DRY_RUN:-0}" != 1 ]; then
    local GW_PARAMS_CRD="gatewayparameters.gateway.kgateway.dev" WAITED=0
    spinner_start "Waiting for kgateway CRDs" "5 min"
    until kubectl get crd "$GW_PARAMS_CRD" >/dev/null 2>&1; do
      sleep 10
      WAITED=$((WAITED + 10))
      if [ $WAITED -ge 600 ]; then
        spinner_stop
        log_warn "Timed out waiting for kgateway CRD $GW_PARAMS_CRD after ${WAITED}s"
        break
      fi
    done
    spinner_stop
    if kubectl get crd "$GW_PARAMS_CRD" >/dev/null 2>&1; then
      log_ok "kgateway CRD $GW_PARAMS_CRD available (waited ${WAITED}s)"
    else
      log_warn "kgateway CRD $GW_PARAMS_CRD not available — layer-2 may fail"
    fi
  fi

  log_info "Layer 2: DP + WP + registry + WS gateway policy"
  _apply_kustomize "$SUB/init/layer-2"

  log_info "Observability plane: Observer + OpenSearch + Fluent Bit"
  # The HelmRelease has dependsOn: control-plane (observability-plane.yaml:35-37),
  # so applying it here is fine — Flux gates the actual install on CP being Ready.
  # In production WSO2 Cloud this is reconciled via the Flux GitRepository
  # picking up wso2cloud-local/kustomizations/observability-plane.yaml.
  # Local k3d has no GitRepository watching the submodule, so we apply
  # imperatively the same way every other layer is applied.
  _apply_kustomize "$SUB/init/observability-plane"

  if [ "${DRY_RUN:-0}" != 1 ]; then
    # control-plane dependsOn: thunder — wait for Thunder first so the
    # control-plane HelmRelease can actually start reconciling (instead of
    # sitting in a dependency-wait loop for the full control-plane timeout).
    spinner "Waiting for Thunder" "5 min" -- \
      kubectl wait --for=condition=Ready hr/thunder -n thunder --timeout=900s || true

    _wait_for_hr control-plane openchoreo-control-plane
    _wait_for_hr workflow-plane openchoreo-workflow-plane
    # OpenSearch first-pull is the long pole (~30 min cold). Match the Flux
    # Kustomization timeout in wso2cloud-local/kustomizations/observability-plane.yaml.
    _wait_for_hr observability-plane openchoreo-observability-plane 2400

    # ClusterObservabilityPlane CR is deferred until the HR is Ready —
    # otherwise its CRD doesn't exist yet and the apply fails (race
    # against Helm's CRD install on first install). The manifest has no
    # envsubst placeholders, so a direct apply is sufficient.
    log_info "Registering ClusterObservabilityPlane with the control plane"
    kubectl apply -f "$SUB/init/observability-plane/cluster-observability-plane.yaml" \
      || log_warn "ClusterObservabilityPlane apply failed; retry on next setup"

    # Wait for Argo CRDs to actually exist before applying domain resources
    local WAITED=0 ARGO_CRD="workflowtemplates.argoproj.io"
    spinner_start "Waiting for Argo Workflow CRDs" "5 min"
    until kubectl get crd "$ARGO_CRD" >/dev/null 2>&1; do
      sleep 10
      WAITED=$((WAITED + 10))
      if [ $WAITED -ge 600 ]; then
        spinner_stop
        log_warn "Timed out waiting for Argo CRD $ARGO_CRD after ${WAITED}s"
        break
      fi
    done
    spinner_stop
    if kubectl get crd "$ARGO_CRD" >/dev/null 2>&1; then
      log_ok "Argo CRD $ARGO_CRD is available (waited ${WAITED}s)"
    else
      log_warn "Argo CRD $ARGO_CRD not available — domain apply may fail"
    fi
  fi

  log_info "Layer 3: ClusterDataPlane + ClusterWorkflowPlane registrations"
  _apply_kustomize "$SUB/init/layer-3"

  log_info "Platform domain: cluster types + traits"
  _apply_kustomize "$SUB/domains/platform/cluster-shared" || log_warn "cluster-shared domain apply had errors (some CRDs may not be ready yet)"

  log_info "wso2cloud namespace + definitions + release-bindings"
  _apply_kustomize "$SUB/domains/platform/namespaces/wso2cloud" || log_warn "wso2cloud domain apply had errors"

  log_info "app-factory project + components (developers domain)"
  _apply_kustomize "$SUB/domains/developers/namespaces/wso2cloud/projects/app-factory" || log_warn "app-factory domain apply had errors"

  # Cross-namespace copy retired: previously this projected
  # ReleaseBinding/SecretReference/DeploymentPipeline/Environment from
  # `wso2cloud` into `default` because app-factory Workloads were
  # applied to `default`. With Workloads now applied to `wso2cloud`
  # (deployments-v2/scripts/lib/workload.sh — WORKLOAD_NAMESPACE), the
  # OC controller resolves everything within a single namespace and the
  # copy is unnecessary. Cleaning up any leftover orphans in `default`
  # from previous runs:
  if [ "${DRY_RUN:-0}" != 1 ]; then
    # Anchored / boundary-bounded match — `platform` as a substring would
    # eat unrelated names (`cluster-platform-config` etc.) on a non-`--all`
    # re-run.
    for kind in releasebinding secretreference deploymentpipeline environment; do
      for r in $(kubectl get "$kind" -n default -o name 2>/dev/null \
        | grep -E '/(app-factory-|billing-|cloud-console-|platform-(api|idp)-|sample-console-|task-)' \
        || true); do
        kubectl delete "$r" -n default --ignore-not-found 2>/dev/null || true
      done
    done
  fi

  if [ "${DRY_RUN:-0}" != 1 ]; then
    spinner "Waiting for Thunder IDP" "5 min" -- \
      kubectl wait --for=condition=Ready hr/thunder -n thunder --timeout=900s || true
  fi

  log_ok "platform up"
}
