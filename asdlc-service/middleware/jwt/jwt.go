// Package jwt is the BFF inbound JWT identity layer. It wraps the
// jwtassertion middleware (JWKS-backed RS256 verification) and exposes a
// thin Claims projection for the rest of the service to consume.
package jwt

import (
	"context"
	"net/http"

	"github.com/wso2/asdlc/asdlc-service/middleware/jwtassertion"
)

// Claims is the BFF-internal projection of the verified JWT.
type Claims struct {
	Subject  string
	ClientID string
	// Organisation claims sourced from Thunder. ResolveOuHandle is the
	// only place that picks one — keep the precedence in lockstep with
	// the console.
	OuHandle string
	OuName   string
	OuId     string
}

// ResolveOuHandle returns the canonical OC org handle from a verified
// JWT, preferring `ouHandle` over `ouName` over `ouId`. Returns "" when
// the token has none of those claims (which the caller must surface as
// a fail-loud error rather than silently substitute an org).
//
// The console mirrors this precedence verbatim
// (console/src/utils/orgClaims.ts). Any change here MUST land on both
// sides simultaneously.
func ResolveOuHandle(c *Claims) string {
	if c == nil {
		return ""
	}
	if c.OuHandle != "" {
		return c.OuHandle
	}
	if c.OuName != "" {
		return c.OuName
	}
	return c.OuId
}

type claimsContextKey struct{}

// WithClaims returns a copy of ctx that carries the given Claims value.
func WithClaims(ctx context.Context, claims *Claims) context.Context {
	return context.WithValue(ctx, claimsContextKey{}, claims)
}

// ClaimsFromContext retrieves the Claims stored by WithClaims.
func ClaimsFromContext(ctx context.Context) *Claims {
	c, _ := ctx.Value(claimsContextKey{}).(*Claims)
	return c
}

// Config configures the inbound JWT verifier. It is a thin shim over
// jwtassertion.Config — same fields, names match Phase 2 wiring.
type Config struct {
	JWKS                *jwtassertion.JWKSCache
	AllowedIssuers      []string
	AllowedAudiences    []string
	ResourceMetadataURL string
	IsLocalDevEnv       bool
}

// Middleware returns an HTTP middleware that verifies the Authorization
// header against cfg, projects the verified token into a Claims record, and
// forwards the request with the projection in context.
//
// The underlying jwtassertion middleware emits RFC 9728 WWW-Authenticate
// challenges on failure. The thin projection only exists so the rest of
// the BFF doesn't have to care about the full TokenClaims shape.
func Middleware(cfg Config) func(http.Handler) http.Handler {
	verifier := jwtassertion.Authenticator(jwtassertion.Config{
		JWKS:                cfg.JWKS,
		AllowedIssuers:      cfg.AllowedIssuers,
		AllowedAudiences:    cfg.AllowedAudiences,
		ResourceMetadataURL: cfg.ResourceMetadataURL,
		IsLocalDevEnv:       cfg.IsLocalDevEnv,
	})
	return func(next http.Handler) http.Handler {
		return verifier(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			tc := jwtassertion.GetTokenClaims(r.Context())
			if tc != nil {
				ctx := WithClaims(r.Context(), &Claims{
					Subject:  tc.Subject,
					ClientID: tc.ClientID,
					OuHandle: tc.OuHandle,
					OuName:   tc.OuName,
					OuId:     tc.OuId,
				})
				r = r.WithContext(ctx)
			}
			next.ServeHTTP(w, r)
		}))
	}
}
