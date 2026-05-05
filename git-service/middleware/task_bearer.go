package middleware

import (
	"context"
	"net/http"

	"github.com/wso2/asdlc/git-service/middleware/jwtassertion"
	"github.com/wso2/asdlc/git-service/pkg/credentials"
)

type bearerContextKey string

const claimsKey bearerContextKey = "taskBearerClaims"

// RequireTaskBearer returns a middleware that verifies the Authorization
// header as an RS256 Task JWT, parses claims, and stores them in the
// request context for downstream handlers.
//
// The Task JWT is signed by the BFF (RSA private key) and verified here
// against the BFF's published JWKS. Audience MUST equal "git-service" so
// a token leaked to a different service can't be replayed against this
// /credentials/refresh endpoint.
func RequireTaskBearer(verifier jwtassertion.Middleware) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return verifier(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			tc := jwtassertion.GetTokenClaims(r.Context())
			if tc == nil {
				// jwtassertion.Authenticator already wrote the 401; this
				// branch only runs in the (impossible) success-without-claims
				// path. Defensive.
				http.Error(w, "missing claims", http.StatusUnauthorized)
				return
			}
			claims := &credentials.TaskBearerClaims{
				TaskID:  tc.TaskID,
				OcOrgID: tc.OcOrgID,
			}
			if tc.IssuedAt != nil {
				claims.IssuedAt = tc.IssuedAt.Unix()
			}
			if tc.ExpiresAt != nil {
				claims.ExpiresAt = tc.ExpiresAt.Unix()
			}
			ctx := context.WithValue(r.Context(), claimsKey, claims)
			next.ServeHTTP(w, r.WithContext(ctx))
		}))
	}
}

// TaskBearerClaims pulls the verified claims out of the request context.
// Returns nil if the request did not pass through RequireTaskBearer.
func TaskBearerClaims(ctx context.Context) *credentials.TaskBearerClaims {
	if c, ok := ctx.Value(claimsKey).(*credentials.TaskBearerClaims); ok {
		return c
	}
	return nil
}
