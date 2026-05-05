#!/usr/bin/env bash
# webhook-relay.sh — forward GitHub webhooks from smee.io into the local
# k3d BFF. Host-side dev tool; never runs in any cluster.
#
# Why a host process: see docs/design/webhook-delivery.md. The local cluster
# has no public ingress, so something on the developer's machine has to
# bridge smee.io → cluster. This script runs `kubectl port-forward` and
# `npx smee-client` together, restarts them on transient failures, and
# cleans up on Ctrl-C.

set -uo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT_DIR="$(cd "$SCRIPT_DIR/../.." && pwd)"
ENV_FILE="$ROOT_DIR/deployments-v2/.env"
PID_FILE="$ROOT_DIR/deployments-v2/.webhook-relay.pid"
LOG_FILE="$ROOT_DIR/deployments-v2/.webhook-relay.log"

K3D_CONTEXT="k3d-openchoreo"
BFF_SERVICE_NAME="app-factory-api"
BFF_NAMESPACE=""  # discovered at runtime — OpenChoreo names dataplane namespaces dynamically
LOCAL_PORT=18080
REMOTE_PORT=8080

# ── helpers ──────────────────────────────────────────────────────────────────

c_red=$'\033[31m'; c_yellow=$'\033[33m'; c_green=$'\033[32m'; c_dim=$'\033[2m'; c_nc=$'\033[0m'
log_info() { printf "  %s\n" "$*"; }
log_ok()   { printf "  ${c_green}✓${c_nc} %s\n" "$*"; }
log_warn() { printf "  ${c_yellow}!${c_nc} %s\n" "$*"; }
log_err()  { printf "  ${c_red}✗${c_nc} %s\n" "$*" >&2; }
die() { log_err "$*"; exit 1; }

# ── prerequisite checks ──────────────────────────────────────────────────────

check_prereqs() {
  command -v kubectl >/dev/null 2>&1 || die "kubectl not found in PATH"
  command -v node    >/dev/null 2>&1 || die "node not found — install Node.js (used by npx smee-client)"
  command -v npx     >/dev/null 2>&1 || die "npx not found — install Node.js"
  command -v lsof    >/dev/null 2>&1 || log_warn "lsof not found — port-collision diagnostics will be limited"

  if ! kubectl config get-contexts -o name 2>/dev/null | grep -qx "$K3D_CONTEXT"; then
    die "kubectl context '$K3D_CONTEXT' not found — run deployments-v2/scripts/setup.sh first"
  fi

  # OpenChoreo creates a per-project dataplane namespace whose name is hashed
  # (e.g. dp-default-app-factory-development-<id>). Discover it by service name.
  BFF_NAMESPACE=$(kubectl --context "$K3D_CONTEXT" get svc -A \
    -o jsonpath="{.items[?(@.metadata.name==\"$BFF_SERVICE_NAME\")].metadata.namespace}" 2>/dev/null || true)
  if [ -z "$BFF_NAMESPACE" ]; then
    die "service '$BFF_SERVICE_NAME' not found in any namespace — is the cluster up? (kubectl get pods -A)"
  fi

  [ -f "$ENV_FILE" ] || die ".env not found at $ENV_FILE — run setup.sh first"
}

load_delivery_url() {
  # shellcheck disable=SC1090
  set -a; source "$ENV_FILE"; set +a
  if [ -z "${GITHUB_WEBHOOK_DELIVERY_URL:-}" ]; then
    die "GITHUB_WEBHOOK_DELIVERY_URL not set in .env — re-run setup.sh to auto-provision a smee channel"
  fi
  case "$GITHUB_WEBHOOK_DELIVERY_URL" in
    https://smee.io/*) ;;
    *) die "GITHUB_WEBHOOK_DELIVERY_URL is not a smee.io URL ($GITHUB_WEBHOOK_DELIVERY_URL) — relay only makes sense for local; cloud tiers deliver direct" ;;
  esac
}

check_single_instance() {
  if [ -f "$PID_FILE" ]; then
    local pid; pid=$(cat "$PID_FILE" 2>/dev/null || true)
    if [ -n "$pid" ] && kill -0 "$pid" 2>/dev/null; then
      die "another webhook-relay is already running (pid $pid). Stop it with: kill $pid"
    fi
    rm -f "$PID_FILE"  # stale
  fi
  echo $$ > "$PID_FILE"
}

check_port_free() {
  if command -v lsof >/dev/null 2>&1; then
    local holder
    holder=$(lsof -nP -iTCP:"$LOCAL_PORT" -sTCP:LISTEN 2>/dev/null | awk 'NR==2 {print $1" (pid "$2")"}' || true)
    if [ -n "$holder" ]; then
      die "local port $LOCAL_PORT is already in use by $holder — stop it or pick a different port"
    fi
  fi
}

# ── child process management ─────────────────────────────────────────────────

PORTFWD_PID=""
SMEE_PID=""

start_port_forward() {
  kubectl --context "$K3D_CONTEXT" port-forward -n "$BFF_NAMESPACE" "svc/$BFF_SERVICE_NAME" \
    "$LOCAL_PORT:$REMOTE_PORT" >>"$LOG_FILE" 2>&1 &
  PORTFWD_PID=$!
}

start_smee() {
  npx --yes smee-client \
    --url "$GITHUB_WEBHOOK_DELIVERY_URL" \
    --target "http://localhost:$LOCAL_PORT/webhooks/github" \
    >>"$LOG_FILE" 2>&1 &
  SMEE_PID=$!
}

cleanup() {
  trap '' INT TERM EXIT
  log_info "shutting down…"
  [ -n "$SMEE_PID"    ] && kill "$SMEE_PID"    2>/dev/null || true
  [ -n "$PORTFWD_PID" ] && kill "$PORTFWD_PID" 2>/dev/null || true
  wait 2>/dev/null || true
  rm -f "$PID_FILE"
  log_ok "stopped"
}

# Restart loop — if either child dies, restart it. Bail only on parent SIGTERM.
supervise() {
  local restart_count=0
  while true; do
    if ! kill -0 "$PORTFWD_PID" 2>/dev/null; then
      restart_count=$((restart_count + 1))
      log_warn "kubectl port-forward exited — restart #$restart_count"
      sleep 1
      start_port_forward
    fi
    if ! kill -0 "$SMEE_PID" 2>/dev/null; then
      restart_count=$((restart_count + 1))
      log_warn "smee-client exited — restart #$restart_count"
      sleep 1
      start_smee
    fi
    sleep 2
  done
}

# ── main ─────────────────────────────────────────────────────────────────────

main() {
  check_prereqs
  load_delivery_url
  check_single_instance
  check_port_free

  trap cleanup INT TERM EXIT

  : > "$LOG_FILE"  # truncate

  printf "\n  ${c_dim}webhook relay${c_nc}\n"
  printf "    channel : %s\n"        "$GITHUB_WEBHOOK_DELIVERY_URL"
  printf "    target  : http://localhost:%d/webhooks/github\n" "$LOCAL_PORT"
  printf "    bff     : svc/%s:%d in %s (%s)\n" "$BFF_SERVICE_NAME" "$REMOTE_PORT" "$BFF_NAMESPACE" "$K3D_CONTEXT"
  printf "    log     : %s\n"        "$LOG_FILE"
  printf "    replay  : open %s to redeliver missed events\n" "$GITHUB_WEBHOOK_DELIVERY_URL"
  echo ""

  log_info "fetching smee-client (first run downloads ~20MB)…"
  start_port_forward
  sleep 1  # give port-forward a head start so smee-client's first POST has a target
  start_smee

  log_ok "listening for events — Ctrl-C to stop"
  echo ""

  supervise
}

main "$@"
