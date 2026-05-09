#!/usr/bin/env bash
# logs.sh — stream logs from all app-factory components (or a single one).
# Usage:
#   bash deployments-v2/scripts/logs.sh              # all components
#   bash deployments-v2/scripts/logs.sh api           # just the api
#   bash deployments-v2/scripts/logs.sh --since 10m   # last 10 minutes
#   bash deployments-v2/scripts/logs.sh api --tail 50 # last 50 lines of api
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
ROOT="$(cd "$SCRIPT_DIR/../.." && pwd)"
# shellcheck disable=SC1091
[ -f "${SCRIPT_DIR}/lib/utils.sh" ] && source "${SCRIPT_DIR}/lib/utils.sh" || true

# ── colour helpers (inline so script is self-contained) ─────────────────────

RED='\033[0;31m';    GRN='\033[0;32m';    YLW='\033[0;33m'
BLU='\033[0;34m';    MAG='\033[0;35m';    CYN='\033[0;36m'
WHT='\033[0;37m';    NC='\033[0m'

COLOURS=("$RED" "$GRN" "$YLW" "$BLU" "$MAG" "$CYN" "$WHT")
COMPONENT_COLOUR() {
  local i="$1"
  echo "${COLOURS[$((i % ${#COLOURS[@]}))]}"
}

# ── discover DP namespace ───────────────────────────────────────────────────

discover_dp_ns() {
  local ns
  ns=$(kubectl get pods -l 'openchoreo.dev/project=app-factory' -A -o jsonpath='{.items[0].metadata.namespace}' 2>/dev/null || echo "")
  if [ -z "$ns" ]; then
    echo "ERROR: no app-factory pods running — is setup.sh complete?" >&2
    exit 1
  fi
  echo "$ns"
}

# ── resolve component name → k8s component label value ──────────────────────

resolve_component_label() {
  local short="$1"
  case "$short" in
    api|bff)               echo "app-factory-api" ;;
    console|ui)            echo "app-factory-console" ;;
    git|git-service)       echo "app-factory-git-service" ;;
    agents|agents-service) echo "app-factory-agents-service" ;;
    postgres|db)           echo "app-factory-postgresql" ;;
    *)                     echo "" ;;
  esac
}

# ── usage ───────────────────────────────────────────────────────────────────

usage() {
  cat <<EOF
Usage: $(basename "$0") [COMPONENT] [OPTIONS]

Stream logs from app-factory components in your local OpenChoreo cluster.

COMPONENT (optional):
  all               All components (default)
  api, bff          app-factory-api
  console, ui       app-factory-console
  git, git-service  app-factory-git-service
  agents            app-factory-agents-service
  postgres, db      app-factory-postgresql

OPTIONS:
  --since TIME      Show logs since relative time (e.g. 5m, 1h)
  --tail N          Number of lines to show (default: follow mode, -1)
  --timestamps      Include timestamps (on by default)

Examples:
  $(basename "$0")                  # follow all components
  $(basename "$0") api              # follow only the API
  $(basename "$0") api --tail 50    # show last 50 lines of API
  $(basename "$0") --since 10m      # last 10 min of all components
EOF
  exit 0
}

# ── main ────────────────────────────────────────────────────────────────────

COMPONENT_SHORT="all"
SINCE=""
TAIL="-1"        # -1 = follow mode (kubectl logs -f)
TIMESTAMPS=true
POSITIONAL=()

while [[ $# -gt 0 ]]; do
  case "$1" in
    --help|-h) usage ;;
    --since)   SINCE="$2"; shift 2 ;;
    --tail)    TAIL="$2";  shift 2 ;;
    --timestamps) TIMESTAMPS=true; shift ;;
    -*) echo "Unknown flag: $1"; usage ;;
    *) POSITIONAL+=("$1"); shift ;;
  esac
done

if [ ${#POSITIONAL[@]} -gt 0 ]; then
  COMPONENT_SHORT="${POSITIONAL[0]}"
fi

DP_NS=$(discover_dp_ns)

# ── build label selector ────────────────────────────────────────────────────

if [ "$COMPONENT_SHORT" = "all" ]; then
  SELECTOR="openchoreo.dev/project=app-factory"
elif [ "$COMPONENT_SHORT" = "postgres" ] || [ "$COMPONENT_SHORT" = "db" ]; then
  DP_NS="wso2cloud"
  SELECTOR="app=app-factory-postgresql"
else
  COMP_LABEL=$(resolve_component_label "$COMPONENT_SHORT")
  if [ -z "$COMP_LABEL" ]; then
    echo "ERROR: unknown component '$COMPONENT_SHORT'" >&2
    echo "       valid: api, console, git, agents, postgres, all" >&2
    exit 1
  fi
  SELECTOR="openchoreo.dev/component=$COMP_LABEL"
fi

# ── build selector / namespace ───────────────────────────────────────────────

# Base selector args (valid for kubectl get, stern, kubectl logs)
BASE_FLAGS=(-n "$DP_NS" -l "$SELECTOR")
# Log-specific flags (only valid for kubectl logs / stern)
LOG_FLAGS=()
if [ "$TIMESTAMPS" = true ]; then
  LOG_FLAGS+=(--timestamps)
fi
if [ -n "$SINCE" ]; then
  LOG_FLAGS+=(--since="$SINCE")
fi

log_cmd() {
  echo -e "${WHT}▶ $DP_NS / $SELECTOR${NC}"
}

# ── streaming ────────────────────────────────────────────────────────────────

# Prefer stern for multi-pod follow (cleaner UX, auto-colours).
if command -v stern &>/dev/null; then
  log_cmd
  if [ "$TAIL" = "-1" ]; then
    exec stern "${BASE_FLAGS[@]}" "${LOG_FLAGS[@]}" --all-containers --tail=10
  else
    exec stern "${BASE_FLAGS[@]}" "${LOG_FLAGS[@]}" --all-containers --tail="$TAIL"
  fi
fi

# Fallback: stream each pod in parallel, prefixed with [pod/container].
log_cmd
echo -e "${WHT}(install 'stern' for prettier multi-pod output — brew install stern)${NC}"
echo ""

PODS=$(kubectl get pods "${BASE_FLAGS[@]}" -o jsonpath='{range .items[*]}{.metadata.name}{"\n"}{end}' 2>/dev/null)

if [ -z "$PODS" ]; then
  echo "No pods found matching $SELECTOR in $DP_NS"
  exit 0
fi

i=0
for pod in $PODS; do
  col=$(COMPONENT_COLOUR "$i")
  prefix_fmt="${col}[%s/%s]${NC}"
  containers=$(kubectl get pod "$pod" -n "$DP_NS" -o jsonpath='{.spec.containers[*].name}' 2>/dev/null)
  for c in $containers; do
    if [ "$TAIL" = "-1" ]; then
      # follow mode: background each pod
      {
        kubectl logs -f "$pod" -c "$c" -n "$DP_NS" "${LOG_FLAGS[@]}" 2>/dev/null | while IFS= read -r line; do
          printf "${prefix_fmt} %s\n" "$pod" "$c" "$line"
        done
      } &
    else
      # snapshot mode: print inline, no background needed
      kubectl logs "$pod" -c "$c" -n "$DP_NS" "${LOG_FLAGS[@]}" --tail="$TAIL" 2>/dev/null | while IFS= read -r line; do
        printf "${prefix_fmt} %s\n" "$pod" "$c" "$line"
      done
    fi
  done
  i=$((i + 1))
done

if [ "$TAIL" = "-1" ]; then
  trap 'kill 0 2>/dev/null' EXIT INT TERM
  wait
fi
