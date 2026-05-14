#!/usr/bin/env bash
# lib/seed-admin-github.sh — local-dev convenience that pre-connects
# the admin user's GitHub PAT through the public Connect API.
#
# This is the only script in the repo that names an org. The platform
# binary (BFF, git-service, console) is org-agnostic; the LOCAL_DEV_*
# env vars below are read here exclusively. There is no equivalent in
# any manifest. On hosted environments the tenant connects via the
# console UI (Settings → GitHub Integration). See
# docs/design/default-org-seed-removal.md §3.5.

set -u

# Inputs (read from .env via env.sh):
#   LOCAL_DEV_ADMIN_GITHUB_PAT   — PAT to register
#   LOCAL_DEV_ADMIN_GITHUB_OWNER — GitHub org login the PAT is scoped to
#   LOCAL_DEV_ADMIN_OUHANDLE     — Thunder-issued ouHandle for the admin
#                                  user (default `default` — the OU the
#                                  Thunder seed places admin in). Pinned
#                                  as a knob so a future Thunder change
#                                  that issues a different handle only
#                                  requires bumping .env.
#
# Behaviour:
#   1. No-op when LOCAL_DEV_ADMIN_GITHUB_PAT or LOCAL_DEV_ADMIN_GITHUB_OWNER
#      is empty.
#   2. Localhost guard — refuses to run unless PUBLIC_THUNDER_URL host is
#      localhost / 127.0.0.1 / *.openchoreo.localhost.
#   3. Mints a Thunder S2S token (client = local-dev-seeder) and POSTs to
#      `/api/v1/organizations/{ouHandle}/github/pat` (BFF) — the same
#      endpoint the console UI uses. The OC namespace must already exist
#      from the prior `seed-admin-org` step (or `platform-api-service` in
#      hosted).
#   4. Soft-fail: warn-and-continue on non-2xx; do not break setup.sh.
#      The user can connect via the UI as a fallback.
#   5. No PAT or token is logged.

seed_admin_github() {
  local script_name="seed-admin-github"

  if [ "${DRY_RUN:-0}" = 1 ]; then
    log_skip "[dry-run] would seed admin GitHub PAT via Connect API"
    return 0
  fi

  local pat="${LOCAL_DEV_ADMIN_GITHUB_PAT:-}"
  local owner="${LOCAL_DEV_ADMIN_GITHUB_OWNER:-}"
  local ouhandle="${LOCAL_DEV_ADMIN_OUHANDLE:-default}"

  if [ -z "$pat" ] || [ -z "$owner" ]; then
    log_info "${script_name}: LOCAL_DEV_ADMIN_GITHUB_PAT / _OWNER not set — skipping"
    return 0
  fi

  local thunder_url="${PUBLIC_THUNDER_URL:-http://thunder.openchoreo.localhost:8080}"
  if ! _seed_localhost_guard "$thunder_url"; then
    log_warn "${script_name}: refusing to run — PUBLIC_THUNDER_URL is not a localhost host"
    return 0
  fi

  # Discover the data-plane release namespace where BFF + git-service run.
  local dp_ns
  dp_ns=$(kubectl get svc -A --field-selector metadata.name=app-factory-api \
    -o jsonpath='{.items[0].metadata.namespace}' 2>/dev/null || true)
  if [ -z "$dp_ns" ]; then
    log_warn "${script_name}: app-factory-api Service not found yet — skipping"
    return 0
  fi

  # Pick a free-ish high port for the BFF port-forward. seed-admin-github
  # only ever talks to the BFF — git-service is reached transitively
  # through the BFF's PAT-connect endpoint.
  local bff_local_port=18090

  local pf_bff_pid=0
  kubectl port-forward -n "$dp_ns" svc/app-factory-api "${bff_local_port}:8080" >/dev/null 2>&1 &
  pf_bff_pid=$!
  # shellcheck disable=SC2064
  trap "kill ${pf_bff_pid} 2>/dev/null || true" RETURN

  if ! _seed_wait_port "$bff_local_port" 10; then
    log_warn "${script_name}: BFF port-forward did not come up — skipping"
    return 0
  fi

  local bff_url="http://127.0.0.1:${bff_local_port}"
  local s2s_client_id="local-dev-seeder"
  local s2s_client_secret="local-dev-seeder-secret"

  local s2s_token
  s2s_token="$(_seed_mint_token "$thunder_url" "$s2s_client_id" "$s2s_client_secret")" || {
    log_warn "${script_name}: could not mint Thunder S2S token — skipping"
    return 0
  }

  # POST PAT-mode Connect — same endpoint the console UI uses
  # (asdlc-service/api/org_github_routes.go). The OC namespace must
  # already exist (created by `seed-admin-org` locally, or by
  # `platform-api-service` in hosted). git-service is reached
  # transitively, through the BFF.
  if _seed_connect_pat "$bff_url" "$s2s_token" "$ouhandle" "$pat" "$owner"; then
    log_ok "admin org '${ouhandle}' pre-connected via local-dev seed (login=${owner})"
  else
    log_warn "${script_name}: Connect did not succeed — connect via the console UI as fallback"
  fi
}

_seed_localhost_guard() {
  local url="$1"
  case "$url" in
    http://localhost*|http://127.0.0.1*|https://localhost*|https://127.0.0.1*) return 0 ;;
    http://*.openchoreo.localhost*|https://*.openchoreo.localhost*) return 0 ;;
  esac
  return 1
}

_seed_wait_port() {
  local port="$1" deadline_s="${2:-10}"
  local start now
  start=$(date +%s)
  while true; do
    if (echo > "/dev/tcp/127.0.0.1/${port}") >/dev/null 2>&1; then
      return 0
    fi
    now=$(date +%s)
    if [ $((now - start)) -ge "$deadline_s" ]; then
      return 1
    fi
    sleep 1
  done
}

_seed_mint_token() {
  local thunder_url="$1" client_id="$2" client_secret="$3"
  set +x
  local response
  response="$(curl -sS -X POST "${thunder_url%/}/oauth2/token" \
    -H "Content-Type: application/x-www-form-urlencoded" \
    -d "grant_type=client_credentials" \
    -d "client_id=${client_id}" \
    -d "client_secret=${client_secret}" 2>/dev/null || true)"
  # Minimal JSON extraction without jq: { "access_token": "...", ... }
  local token
  token="$(printf '%s' "$response" | sed -n 's/.*"access_token"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p')"
  [ -n "$token" ] || return 1
  printf '%s' "$token"
}

_seed_connect_pat() {
  local bff_url="$1" token="$2" ouhandle="$3" pat="$4" owner="$5"
  set +x
  local body
  body="$(printf '{"pat":"%s","githubLogin":"%s"}' "$pat" "$owner")"
  local code
  code="$(curl -sS -o /dev/null -w '%{http_code}' \
    -X POST "${bff_url%/}/api/v1/organizations/${ouhandle}/github/pat" \
    -H "Authorization: Bearer ${token}" \
    -H "Content-Type: application/json" \
    -d "$body" 2>/dev/null || true)"
  case "$code" in
    2*) return 0 ;;
    409) log_info "admin org already connected — skipping local-dev PAT seed" ; return 0 ;;
    *) return 1 ;;
  esac
}
