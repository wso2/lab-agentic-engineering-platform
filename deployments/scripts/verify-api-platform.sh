#!/bin/bash
# End-to-end POC verification for WSO2 API Platform + Thunder JWT.
#
# Updated after first POC run — see deployments/POC-API-PLATFORM.md
# for the gotchas this version fixes:
#   - Reconciled resources land in a per-environment data-plane namespace
#     (`dp-<hash>`), NOT in the Component's `default` namespace. We discover
#     it from ReleaseBinding status.
#   - Thunder admin API auth isn't trivially scriptable from outside the
#     image's own bootstrap path. We mint tokens against an already-
#     bootstrapped client (`asdlc-api-client`) instead — the trait's
#     `jwt-auth v0` policy emits no audience filter so any cluster-signed
#     token passes.

set -e
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
cd "$SCRIPT_DIR"
source "$SCRIPT_DIR/env.sh"

POC_MANIFESTS="${SCRIPT_DIR}/../manifests/poc-api-platform"

# Already-bootstrapped Thunder client from values-thunder.yaml.
# (Adding a dedicated POC client would mean editing values-thunder.yaml
# and re-running setup-openchoreo.sh — see POC-API-PLATFORM.md gotcha #3.)
CLIENT_ID="${POC_CLIENT_ID:-asdlc-api-client}"
CLIENT_SECRET="${POC_CLIENT_SECRET:-asdlc-api-client-secret}"
THUNDER_PUBLIC="${THUNDER_PUBLIC:-http://thunder.openchoreo.localhost:8080}"

echo "=== POC: API Platform + Thunder JWT verification ==="

# ───────────────────────────────────────────────────────────────────────────
# 1. Apply manifests
# ───────────────────────────────────────────────────────────────────────────
echo ""
echo "1️⃣  Applying POC manifests..."
for f in "${POC_MANIFESTS}"/*.yaml; do
    kubectl --context "${CLUSTER_CONTEXT}" apply -f "$f"
done

# ───────────────────────────────────────────────────────────────────────────
# 2. Wait for ReleaseBinding readiness, then discover the dp-* namespace
# ───────────────────────────────────────────────────────────────────────────
echo ""
echo "2️⃣  Waiting for ReleaseBindings to reach Ready..."

wait_rb_ready() {
    local rb="$1"
    for i in $(seq 1 120); do
        cond=$(kubectl --context "${CLUSTER_CONTEXT}" get releasebinding "$rb" -n default \
            -o jsonpath='{.status.conditions[?(@.type=="Ready")].status}' 2>/dev/null)
        if [ "$cond" = "True" ]; then
            echo "   ✅ ReleaseBinding $rb Ready"
            return 0
        fi
        sleep 1
    done
    echo "   ❌ ReleaseBinding $rb did not become Ready" >&2
    kubectl --context "${CLUSTER_CONTEXT}" describe releasebinding "$rb" -n default >&2
    return 1
}

wait_rb_ready poc-public-development
wait_rb_ready poc-protected-development

# Discover the data-plane namespace from the ReleaseBinding status.
# Pattern: poc-public.dp-<hash>.svc.cluster.local → extract dp-<hash>.
DP_HOST=$(kubectl --context "${CLUSTER_CONTEXT}" get releasebinding poc-public-development -n default \
    -o jsonpath='{.status.endpoints[0].serviceURL.host}')
DP_NS=$(echo "$DP_HOST" | sed -E 's|^[^.]+\.([^.]+)\..*|\1|')
PUBLIC_URL=$(kubectl --context "${CLUSTER_CONTEXT}" get releasebinding poc-public-development -n default \
    -o jsonpath='{.status.endpoints[0].externalURLs.http.scheme}://{.status.endpoints[0].externalURLs.http.host}:{.status.endpoints[0].externalURLs.http.port}{.status.endpoints[0].externalURLs.http.path}')
PROTECTED_URL=$(kubectl --context "${CLUSTER_CONTEXT}" get releasebinding poc-protected-development -n default \
    -o jsonpath='{.status.endpoints[0].externalURLs.http.scheme}://{.status.endpoints[0].externalURLs.http.host}:{.status.endpoints[0].externalURLs.http.port}{.status.endpoints[0].externalURLs.http.path}')
# Gotcha: AP router 404s on the bare context path; trailing slash is required.
# See POC-API-PLATFORM.md gotcha "Trailing slash on the route".
PUBLIC_URL="${PUBLIC_URL%/}/"
PROTECTED_URL="${PROTECTED_URL%/}/"
echo "   data-plane namespace: ${DP_NS}"
echo "   public URL:           ${PUBLIC_URL}"
echo "   protected URL:        ${PROTECTED_URL}"

# Sanity-check the trait actually produced the gateway resources.
echo ""
echo "   Trait-produced resources in ${DP_NS}:"
kubectl --context "${CLUSTER_CONTEXT}" get restapi,backend,httproute -n "${DP_NS}" --no-headers 2>/dev/null | sed 's/^/     /'

# ───────────────────────────────────────────────────────────────────────────
# 3. Mint token from an already-bootstrapped client
# ───────────────────────────────────────────────────────────────────────────
echo ""
echo "3️⃣  Minting client_credentials token from Thunder (${CLIENT_ID})..."
TOKEN_RESPONSE=$(curl -sS -X POST "${THUNDER_PUBLIC}/oauth2/token" \
    -H 'Content-Type: application/x-www-form-urlencoded' \
    -d "grant_type=client_credentials&client_id=${CLIENT_ID}&client_secret=${CLIENT_SECRET}")
ACCESS_TOKEN=$(echo "$TOKEN_RESPONSE" | grep -o '"access_token":"[^"]*"' | head -1 | cut -d'"' -f4)
if [ -z "$ACCESS_TOKEN" ]; then
    echo "❌ Failed to mint token. Response:" >&2
    echo "$TOKEN_RESPONSE" >&2
    exit 1
fi
echo "   ✅ Got token (length ${#ACCESS_TOKEN})"

# ───────────────────────────────────────────────────────────────────────────
# 4. Truth table
# ───────────────────────────────────────────────────────────────────────────
echo ""
echo "4️⃣  Truth table:"

curl_status() {
    local url="$1" token="$2"
    if [ -n "$token" ]; then
        curl -sS -o /dev/null -w '%{http_code}' -H "Authorization: Bearer ${token}" "$url"
    else
        curl -sS -o /dev/null -w '%{http_code}' "$url"
    fi
}

assert_eq() {
    local label="$1" expected="$2" actual="$3"
    if [ "$expected" = "$actual" ]; then
        printf "   ✅ %-30s expected %s, got %s\n" "$label" "$expected" "$actual"
    else
        printf "   ❌ %-30s expected %s, got %s\n" "$label" "$expected" "$actual"
        EXIT_CODE=1
    fi
}

EXIT_CODE=0

assert_eq "public + no token"       200 "$(curl_status "$PUBLIC_URL"    "")"
assert_eq "public + valid token"    200 "$(curl_status "$PUBLIC_URL"    "$ACCESS_TOKEN")"
assert_eq "protected + no token"    401 "$(curl_status "$PROTECTED_URL" "")"
assert_eq "protected + valid token" 200 "$(curl_status "$PROTECTED_URL" "$ACCESS_TOKEN")"

echo ""
if [ "$EXIT_CODE" = 0 ]; then
    echo "✅ All four cells passed."
else
    echo "❌ One or more cells failed. Inspect:"
    echo "   kubectl get restapi,backend,httproute -n ${DP_NS}"
    echo "   kubectl logs -n openchoreo-data-plane -l app.kubernetes.io/component=gateway-runtime --tail=200"
    echo "   kubectl logs -n openchoreo-data-plane -l app.kubernetes.io/component=controller --tail=200"
fi
exit "$EXIT_CODE"
