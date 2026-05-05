#!/bin/sh
set -e

echo "App Factory Console — Initializing runtime configuration..."

ASDLC_API_PROXY_URL="${ASDLC_API_PROXY_URL:-http://localhost:9090}"
THUNDER_URL="${VITE_THUNDER_URL:-}"
THUNDER_CLIENT_ID="${VITE_THUNDER_CLIENT_ID:-}"
THUNDER_SCOPES="${VITE_THUNDER_SCOPES:-openid profile email}"
SIGN_IN_REDIRECT_URL="${VITE_SIGN_IN_REDIRECT_URL:-}"
SIGN_OUT_REDIRECT_URL="${VITE_SIGN_OUT_REDIRECT_URL:-}"
DEV_BYPASS_AUTH="${VITE_DEV_BYPASS_AUTH:-}"

# env-config.js is generated at start so the SPA can read runtime config.
# The heredoc is unquoted so $VAR and ${VAR:-default} expand. We fall back
# to /asdlc-api-service for VITE_CORE_API_BASE_URL because that's the
# nginx-side proxy path; the rest are passed through from the env.
VITE_CORE_API_BASE_URL_VAL="${VITE_CORE_API_BASE_URL:-/asdlc-api-service}"
NOW="$(date -u +"%Y-%m-%dT%H:%M:%SZ")"
cat > /usr/share/nginx/html/env-config.js <<EOF_INNER || echo "env-config.js is read-only, skipping write"
// Runtime environment configuration
// Generated at: ${NOW}

window._env_ = {
  VITE_CORE_API_BASE_URL: "${VITE_CORE_API_BASE_URL_VAL}",
  VITE_THUNDER_URL: "${THUNDER_URL}",
  VITE_THUNDER_CLIENT_ID: "${THUNDER_CLIENT_ID}",
  VITE_THUNDER_SCOPES: "${THUNDER_SCOPES}",
  VITE_DEV_BYPASS_AUTH: "${DEV_BYPASS_AUTH}",
EOF_INNER

if [ -n "$SIGN_IN_REDIRECT_URL" ]; then
  cat >> /usr/share/nginx/html/env-config.js <<EOF_INNER
  VITE_SIGN_IN_REDIRECT_URL: "${SIGN_IN_REDIRECT_URL}",
EOF_INNER
fi
if [ -n "$SIGN_OUT_REDIRECT_URL" ]; then
  cat >> /usr/share/nginx/html/env-config.js <<EOF_INNER
  VITE_SIGN_OUT_REDIRECT_URL: "${SIGN_OUT_REDIRECT_URL}",
EOF_INNER
fi

echo '};' >> /usr/share/nginx/html/env-config.js

echo "Runtime configuration generated"

sed -i "s|__ASDLC_API_PROXY_URL__|${ASDLC_API_PROXY_URL}|g" /etc/nginx/nginx.conf

# Extract host:port from the proxy URL for variable-based proxy_pass
# (nginx variable triggers runtime DNS resolution via the resolver,
# avoiding stale cached IPs after upstream pod restarts).
ASDLC_API_BACKEND="$(echo "${ASDLC_API_PROXY_URL}" | sed 's|^https\{0,1\}://||' | sed 's|/.*||')"
sed -i "s|__ASDLC_API_BACKEND__|${ASDLC_API_BACKEND}|g" /etc/nginx/nginx.conf

# Collab-server upstream. nginx.conf uses `set $collab_backend host:port`
# in the /collab/ block so DNS is resolved at request time (the container
# starts even when the upstream is missing — a /collab/ request will then
# 502 instead of preventing nginx from starting at all). Default upstream
# matches docker-compose's service name; OC overrides via COLLAB_SERVER_URL.
COLLAB_SERVER_URL="${COLLAB_SERVER_URL:-}"
if [ -n "$COLLAB_SERVER_URL" ]; then
    # Strip scheme + trailing slash so we end up with "host:port".
    COLLAB_BACKEND="${COLLAB_SERVER_URL#*://}"
    COLLAB_BACKEND="${COLLAB_BACKEND%/}"
    sed -i "s|set \$collab_backend [^;]*;|set \$collab_backend ${COLLAB_BACKEND};|" /etc/nginx/nginx.conf
fi

echo "Configuration summary:"
echo "  API Proxy:     /asdlc-api-service/ -> ${ASDLC_API_PROXY_URL}/"
echo "  Thunder URL:   ${THUNDER_URL:-[NOT SET]}"
echo "  Client ID:     ${THUNDER_CLIENT_ID:-[NOT SET]}"
echo "  BYPASS_AUTH:   ${DEV_BYPASS_AUTH:-[OFF]}"
echo "  Collab Server: ${COLLAB_SERVER_URL:-[default: collab-server:3400 via lazy DNS — 502s if upstream missing]}"

echo "Starting nginx on port 3000..."
exec "$@"
