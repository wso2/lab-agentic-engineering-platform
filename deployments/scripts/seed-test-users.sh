#!/bin/bash
# scripts/seed-test-users.sh — local-dev convenience that creates a fixed
# set of test users in Thunder's `default` OU with password `admin`.
#
# Idempotent: each user is skipped if a user with the same username already
# exists. Safe to re-run after every cluster refresh.
#
#   cd deployments && bash scripts/seed-test-users.sh
#
# Knobs (env, with defaults):
#   THUNDER_URL          defaults to http://thunder.openchoreo.localhost:8080
#   SYSTEM_CLIENT_ID     defaults to asdlc-system-client
#   SYSTEM_CLIENT_SECRET defaults to asdlc-system-client-secret
#   TEST_USER_PASSWORD   defaults to admin
#
# The `asdlc-system-client` is registered by the Thunder bootstrap
# (see deployments/single-cluster/values-thunder.yaml) and bound to the
# Thunder Administrator role by 60-asdlc-system-role.sh — it can mint
# scope=system tokens that the /users admin endpoint accepts.

set -u

THUNDER_URL="${THUNDER_URL:-http://thunder.openchoreo.localhost:8080}"
SYSTEM_CLIENT_ID="${SYSTEM_CLIENT_ID:-asdlc-system-client}"
SYSTEM_CLIENT_SECRET="${SYSTEM_CLIENT_SECRET:-asdlc-system-client-secret}"
TEST_USER_PASSWORD="${TEST_USER_PASSWORD:-admin}"

# username|email|given_name
USERS=(
    "mark|mark@testorg.com|Mark"
    "john|john@testorg.com|John"
    "chris|chris@testorg.com|Chris"
    "emily|emily@testorg.com|Emily"
)

echo "=== seed-test-users ==="
echo "  Thunder: $THUNDER_URL"

# 1. Mint system token
TOKEN_RESP=$(curl -sS -X POST "${THUNDER_URL%/}/oauth2/token" \
    -H "Content-Type: application/x-www-form-urlencoded" \
    -d "grant_type=client_credentials" \
    -d "client_id=${SYSTEM_CLIENT_ID}" \
    -d "client_secret=${SYSTEM_CLIENT_SECRET}" \
    -d "scope=system" 2>/dev/null || true)
TOKEN=$(printf '%s' "$TOKEN_RESP" \
    | sed -n 's/.*"access_token"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p')

if [ -z "$TOKEN" ]; then
    echo "❌ Thunder did not return an access_token."
    echo "   Response (first 200 chars):"
    printf '   %s\n' "$(printf '%s' "$TOKEN_RESP" | head -c 200)"
    exit 1
fi
echo "✅ Token minted (client=${SYSTEM_CLIENT_ID})"

# 2. Fetch default OU
OU_RESP=$(curl -sS -H "Authorization: Bearer ${TOKEN}" \
    "${THUNDER_URL%/}/organization-units/tree/default" 2>/dev/null || true)
OU_ID=$(printf '%s' "$OU_RESP" \
    | grep -o '"handle":"default"[^}]*"id":"[^"]*"\|"id":"[^"]*"[^}]*"handle":"default"' \
    | grep -o '"id":"[^"]*"' | head -1 | cut -d'"' -f4)
if [ -z "$OU_ID" ]; then
    echo "❌ Could not resolve default OU ID."
    echo "   Response (first 200 chars):"
    printf '   %s\n' "$(printf '%s' "$OU_RESP" | head -c 200)"
    exit 1
fi
echo "✅ Default OU: $OU_ID"

# 3. List existing usernames once for idempotency
EXISTING=$(curl -sS -H "Authorization: Bearer ${TOKEN}" \
    "${THUNDER_URL%/}/users" 2>/dev/null \
    | grep -o '"username":"[^"]*"' | cut -d'"' -f4)

echo ""
for row in "${USERS[@]}"; do
    IFS='|' read -r username email given <<<"$row"

    if printf '%s\n' "$EXISTING" | grep -qx "$username"; then
        echo "⏭️  $username — already exists, skipping"
        continue
    fi

    payload=$(printf '{"type":"Person","ouId":"%s","attributes":{"username":"%s","password":"%s","sub":"%s","email":"%s","email_verified":true,"given_name":"%s","family_name":"User"}}' \
        "$OU_ID" "$username" "$TEST_USER_PASSWORD" "$username" "$email" "$given")

    resp=$(curl -sS -w '\n%{http_code}' -X POST \
        -H "Authorization: Bearer ${TOKEN}" \
        -H "Content-Type: application/json" \
        -d "$payload" \
        "${THUNDER_URL%/}/users" 2>/dev/null || echo $'\n000')
    code=$(printf '%s' "$resp" | tail -1)
    body=$(printf '%s' "$resp" | sed '$d')

    case "$code" in
        201|200)
            id=$(printf '%s' "$body" | grep -o '"id":"[^"]*"' | head -1 | cut -d'"' -f4)
            echo "✅ $username — created (id=$id)"
            ;;
        409)
            echo "⏭️  $username — already exists (409)"
            ;;
        *)
            echo "❌ $username — failed (HTTP $code)"
            printf '   %s\n' "$(printf '%s' "$body" | head -c 200)"
            ;;
    esac
done

echo ""
echo "=== seed-test-users complete ==="
