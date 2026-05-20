#!/bin/bash
# scripts/seed-dev.sh — local-dev convenience that pre-connects the
# `default` org's GitHub PAT and Anthropic API key by POSTing to the
# BFF's Connect endpoints with a Thunder S2S token.
#
# NOT part of install. Run manually after a fresh teardown when you
# want to skip the Settings → GitHub / Settings → Anthropic clickthrough.
#
#   cd deployments && bash scripts/seed-dev.sh
#
# Inputs (read from deployments/.env — each branch is independently optional):
#   LOCAL_DEV_ADMIN_GITHUB_PAT   PAT to register (classic or fine-grained)
#   LOCAL_DEV_ADMIN_GITHUB_OWNER GitHub login the PAT is scoped to
#   ANTHROPIC_API_KEY            Anthropic key to register
#
# Knobs (env, with defaults):
#   ENV_FILE                     defaults to deployments/.env
#   ORG_HANDLE                   defaults to "default"
#   BFF_URL                      defaults to http://localhost:9090
#   THUNDER_URL                  defaults to http://thunder.openchoreo.localhost:8080
#   SEEDER_CLIENT_ID             defaults to asdlc-local-dev-seeder
#   SEEDER_CLIENT_SECRET         defaults to asdlc-local-dev-seeder-secret
#
# The seeder client is registered by the Thunder bootstrap (one row in
# CONFIDENTIAL_APPS in single-cluster/values-thunder.yaml). If you ran
# setup.sh from a values-thunder.yaml that predates that row, helm-upgrade
# Thunder first:
#   helm upgrade thunder oci://ghcr.io/asgardeo/helm-charts/thunder \
#       --version 0.34.0 -n thunder --reuse-values \
#       -f single-cluster/values-thunder.yaml
#   kubectl -n thunder delete job thunder-setup ; kubectl -n thunder rollout status job/thunder-setup --timeout=120s
set -u

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
DEPLOY_DIR="$SCRIPT_DIR/.."

ENV_FILE="${ENV_FILE:-$DEPLOY_DIR/.env}"
ORG_HANDLE="${ORG_HANDLE:-default}"
BFF_URL="${BFF_URL:-http://localhost:9090}"
THUNDER_URL="${THUNDER_URL:-http://thunder.openchoreo.localhost:8080}"
SEEDER_CLIENT_ID="${SEEDER_CLIENT_ID:-asdlc-local-dev-seeder}"
SEEDER_CLIENT_SECRET="${SEEDER_CLIENT_SECRET:-asdlc-local-dev-seeder-secret}"

# Whitelist-load a single env key from $ENV_FILE without sourcing the
# whole file (some values legitimately contain spaces / equals signs).
_load_env_key() {
    local key="$1"
    [ -n "${!key:-}" ] && return 0
    [ -f "$ENV_FILE" ] || return 0
    local val
    val=$(grep "^${key}=" "$ENV_FILE" | head -1 | cut -d= -f2-)
    val="${val#\"}"; val="${val%\"}"
    val="${val#\'}"; val="${val%\'}"
    [ -n "$val" ] && export "$key=$val" || true
}
_load_env_key LOCAL_DEV_ADMIN_GITHUB_PAT
_load_env_key LOCAL_DEV_ADMIN_GITHUB_OWNER
_load_env_key ANTHROPIC_API_KEY

GITHUB_PAT="${LOCAL_DEV_ADMIN_GITHUB_PAT:-}"
GITHUB_OWNER="${LOCAL_DEV_ADMIN_GITHUB_OWNER:-}"
ANTHROPIC_KEY="${ANTHROPIC_API_KEY:-}"

echo "=== seed-dev (local-dev convenience) ==="
echo "  BFF:       $BFF_URL"
echo "  Thunder:   $THUNDER_URL"
echo "  Org:       $ORG_HANDLE"

# 1. Pre-flight — both services reachable
if ! curl -fsS --max-time 3 "$BFF_URL/health" > /dev/null 2>&1; then
    echo ""
    echo "❌ BFF not reachable at $BFF_URL"
    echo "   Bring the compose stack up first: cd deployments && bash scripts/start.sh"
    exit 1
fi

# 2. Mint S2S token via Thunder client_credentials
echo ""
echo "🔐 Minting seeder token (client=$SEEDER_CLIENT_ID)..."
TOKEN_RESP=$(curl -sS -X POST "${THUNDER_URL%/}/oauth2/token" \
    -H "Content-Type: application/x-www-form-urlencoded" \
    -d "grant_type=client_credentials" \
    -d "client_id=${SEEDER_CLIENT_ID}" \
    -d "client_secret=${SEEDER_CLIENT_SECRET}" 2>/dev/null || true)
TOKEN=$(printf '%s' "$TOKEN_RESP" \
    | sed -n 's/.*"access_token"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p')

if [ -z "$TOKEN" ]; then
    echo "❌ Thunder did not return an access_token."
    echo "   Response (first 200 chars):"
    printf '   %s\n' "$(printf '%s' "$TOKEN_RESP" | head -c 200)"
    echo
    echo "   The most likely cause: the '${SEEDER_CLIENT_ID}' OAuth app is not"
    echo "   registered in Thunder. See the helm-upgrade hint in this script's"
    echo "   header comment, or re-run setup.sh from latest values-thunder.yaml."
    exit 1
fi
echo "✅ Token minted"

# Soft-fail helper. Body is read from $TMPDIR-style temp file passed in $1.
_post_connect() {
    local label="$1" url="$2" body="$3"
    local code resp_tmp
    resp_tmp=$(mktemp -t seed-dev-XXXXXX.json)
    code=$(curl -sS -o "$resp_tmp" -w '%{http_code}' \
        -X POST "$url" \
        -H "Authorization: Bearer ${TOKEN}" \
        -H "Content-Type: application/json" \
        -d "$body" 2>/dev/null || echo 000)
    case "$code" in
        2*) echo "✅ ${label} connected (HTTP $code)" ;;
        409) echo "✅ ${label} already connected (HTTP 409, no-op)" ;;
        *)
            echo "⚠️  ${label} connect failed (HTTP $code). Response (first 300 chars):"
            printf '   %s\n' "$(head -c 300 "$resp_tmp")"
            echo "   You can connect manually via the console UI."
            ;;
    esac
    rm -f "$resp_tmp"
}

# 3. Anthropic
echo ""
if [ -n "$ANTHROPIC_KEY" ]; then
    echo "🤖 Connecting Anthropic key (org=$ORG_HANDLE)..."
    body=$(printf '{"apiKey":"%s"}' "$ANTHROPIC_KEY")
    _post_connect "Anthropic" \
        "${BFF_URL%/}/api/v1/organizations/${ORG_HANDLE}/anthropic" \
        "$body"
else
    echo "⏭️  ANTHROPIC_API_KEY not set in $ENV_FILE — skipping Anthropic seed"
fi

# 4. GitHub PAT
echo ""
if [ -n "$GITHUB_PAT" ] && [ -n "$GITHUB_OWNER" ]; then
    echo "🐙 Connecting GitHub PAT (org=$ORG_HANDLE, owner=$GITHUB_OWNER)..."
    body=$(printf '{"pat":"%s","githubLogin":"%s"}' "$GITHUB_PAT" "$GITHUB_OWNER")
    _post_connect "GitHub PAT" \
        "${BFF_URL%/}/api/v1/organizations/${ORG_HANDLE}/github/pat" \
        "$body"
else
    echo "⏭️  LOCAL_DEV_ADMIN_GITHUB_PAT / LOCAL_DEV_ADMIN_GITHUB_OWNER not set in $ENV_FILE — skipping GitHub seed"
    echo "   To enable, append to $ENV_FILE:"
    echo "       LOCAL_DEV_ADMIN_GITHUB_PAT=<pat>"
    echo "       LOCAL_DEV_ADMIN_GITHUB_OWNER=<github-login>"
fi

echo ""
echo "=== seed-dev complete ==="
