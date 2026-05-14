// Package orgensure is the verify-and-cache seam for tenant identity. On
// every authenticated request that reaches an org-scoped route, the
// middleware resolves the OC org handle from the JWT claims (via
// middleware/jwt.ResolveOuHandle) and verifies the corresponding OC
// namespace exists, caching the local Organization row's UUID for FK
// use. It does NOT create namespaces — that is the platform's job
// (`platform-api-service` in hosted, `seed-admin-org.sh` in local).
//
// Behaviour:
//   - No claims in context (request did not pass through the JWT verifier
//     — e.g. /webhooks, /auth/external/jwks.json) → pass through.
//   - Claims present but no ouHandle/ouName/ouId → pass through. Route
//     handlers that need an org context surface their own 4xx.
//   - Namespace not yet provisioned → log and pass through so the
//     downstream controller can render a user-meaningful error rather
//     than 5xx the request.
package orgensure

import (
	"log/slog"
	"net/http"

	"github.com/wso2/asdlc/asdlc-service/middleware/jwt"
	"github.com/wso2/asdlc/asdlc-service/services"
)

// Middleware verifies the inbound request's ouHandle has a matching OC
// namespace and warms the local row cache. Best-effort — never 5xx the
// user's request: missing namespace, transient OC blip, and DB issues
// all log and pass through.
func Middleware(svc services.OrganizationService) func(http.Handler) http.Handler {
	if svc == nil {
		// Tests + dev configurations may run without a DB; the
		// middleware is a passthrough in that case.
		return func(next http.Handler) http.Handler { return next }
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			claims := jwt.ClaimsFromContext(r.Context())
			ouHandle := jwt.ResolveOuHandle(claims)
			if ouHandle == "" {
				next.ServeHTTP(w, r)
				return
			}
			if err := svc.EnsureForOuHandle(r.Context(), ouHandle); err != nil {
				slog.WarnContext(r.Context(), "org-ensure verify failed; continuing", "ouHandle", ouHandle, "error", err)
			}
			next.ServeHTTP(w, r)
		})
	}
}
