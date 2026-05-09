#!/usr/bin/env bash
set -euo pipefail

# Cleanup on exit / interrupt — mainly stops the spinner background job
# so the terminal doesn't keep scrolling spurious output after Ctrl+C.
_cleanup() {
  spinner_stop 2>/dev/null || true
  kill 0 2>/dev/null || true
}
trap _cleanup EXIT INT TERM

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT_DIR="$(cd "$SCRIPT_DIR/../.." && pwd)"

source "$SCRIPT_DIR/lib/ui.sh"
source "$SCRIPT_DIR/lib/env.sh"
source "$SCRIPT_DIR/lib/submodule.sh"
source "$SCRIPT_DIR/lib/cluster.sh"
source "$SCRIPT_DIR/lib/platform.sh"
source "$SCRIPT_DIR/lib/asdlc.sh"
source "$SCRIPT_DIR/lib/seed-admin-org.sh"
source "$SCRIPT_DIR/lib/seed-admin-github.sh"

DRY_RUN=0
case "${1:-}" in
  --dry-run) DRY_RUN=1 ;;
  -h|--help)
    cat <<EOF
Usage: setup.sh [--dry-run]

Brings up wso2cloud + asdlc on local k3d. Idempotent — re-run after any failure.

Options:
  --dry-run   Walk each phase and print planned actions; don't apply.
EOF
    exit 0 ;;
esac
export DRY_RUN

log_section "ASDLC deployments-v2 — setup"

log_step "Phase 0: env + submodule" "1 min"
ensure_env
ensure_submodule

log_step "Phase 1: cluster" "3 min"
ensure_cluster

log_step "Phase 2: platform (wso2cloud)" "12 min"
apply_platform

log_step "Phase 3: asdlc" "8 min"
seed_openbao
apply_postgres
bootstrap_workloads
register_streaming_timeouts
register_console_redirect_uri
register_thunder_cors_origin
register_service_oauth_clients
seed_admin_org
seed_admin_github

log_section "Done"
print_login_banner
