// Package jwtassertion provides JWKS-backed JWT validation middleware. The
// per-instance JWKSCache lets a single service verify tokens from multiple
// issuers without refreshes cross-contaminating one another.
package jwtassertion

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"

	"github.com/wso2/asdlc/asdlc-service/utils"
)

// TokenClaims holds the verified claims extracted from a JWT.
type TokenClaims struct {
	Sub      string `json:"sub"`
	Scope    string `json:"scope"`
	OuId     string `json:"ouId"`
	OuHandle string `json:"ouHandle"`
	ClientID string `json:"client_id"`
	// Task JWT–specific custom claims. Empty for User and Service JWTs.
	OcOrgID   string `json:"ocOrgId,omitempty"`
	TaskID    string `json:"taskId,omitempty"`
	ProjectID string `json:"projectId,omitempty"`
	jwt.RegisteredClaims
}

type tokenClaimsCtxKey struct{}
type jwtTokenCtxKey struct{}
type scopesCtxKey struct{}

var (
	claimsKey tokenClaimsCtxKey
	tokenKey  jwtTokenCtxKey
	scopesKey scopesCtxKey
)

// Config configures one JWT verifier instance.
type Config struct {
	// JWKS is the cache used to fetch signing keys.
	JWKS *JWKSCache
	// AllowedIssuers is the set of acceptable iss claim values.
	AllowedIssuers []string
	// AllowedAudiences is the set of acceptable aud claim values. Entries
	// ending with "*" are treated as prefix matches.
	AllowedAudiences []string
	// ResourceMetadataURL is included in the WWW-Authenticate challenge per
	// RFC 9728 (OAuth Protected Resource Metadata). Empty disables the hint.
	ResourceMetadataURL string
	// IsLocalDevEnv enables claim extraction without signature validation when
	// JWKS is nil. Fails closed in any other configuration.
	IsLocalDevEnv bool
}

// Middleware is the standard http.Handler wrapping signature.
type Middleware func(http.Handler) http.Handler

// Authenticator builds a middleware that verifies JWTs against the given
// config. Tokens are read from the Authorization header.
func Authenticator(cfg Config) Middleware {
	issuers := compileIssuers(cfg.AllowedIssuers)
	audiences := compileAudiences(cfg.AllowedAudiences)
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			authHeader := r.Header.Get("Authorization")
			if authHeader == "" {
				w.Header().Set("WWW-Authenticate", buildBearerChallenge(cfg.ResourceMetadataURL, ""))
				utils.WriteErrorResponse(w, http.StatusUnauthorized, "missing Authorization header")
				return
			}
			tokenString := strings.TrimPrefix(authHeader, "Bearer ")
			tokenString = strings.TrimSpace(tokenString)
			if tokenString == "" || tokenString == authHeader {
				w.Header().Set("WWW-Authenticate", buildBearerChallenge(cfg.ResourceMetadataURL, "invalid_token"))
				utils.WriteErrorResponse(w, http.StatusUnauthorized, "malformed Authorization header")
				return
			}

			claims, err := validateJWT(tokenString, cfg, issuers, audiences)
			if err != nil {
				slog.Warn("JWT validation failed", slog.String("error", err.Error()))
				w.Header().Set("WWW-Authenticate", buildBearerChallenge(cfg.ResourceMetadataURL, "invalid_token"))
				utils.WriteErrorResponse(w, http.StatusUnauthorized, "invalid jwt")
				return
			}

			ctx := r.Context()
			ctx = context.WithValue(ctx, claimsKey, claims)
			ctx = context.WithValue(ctx, tokenKey, tokenString)
			ctx = context.WithValue(ctx, scopesKey, claims.Scope)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// GetTokenClaims returns the claims placed in context by Authenticator, or nil
// if the request was not authenticated.
func GetTokenClaims(ctx context.Context) *TokenClaims {
	claims, ok := ctx.Value(claimsKey).(*TokenClaims)
	if !ok {
		return nil
	}
	return claims
}

// GetJWTFromContext returns the raw bearer token, or "" if absent.
func GetJWTFromContext(ctx context.Context) string {
	tok, _ := ctx.Value(tokenKey).(string)
	return tok
}

// HasAllScopes returns true if every scope in required is present on the
// token in context. Returns false if the request is unauthenticated.
func HasAllScopes(ctx context.Context, required []string) bool {
	scopes, ok := ctx.Value(scopesKey).(string)
	if !ok {
		return false
	}
	have := make(map[string]struct{})
	for _, s := range strings.Fields(scopes) {
		have[s] = struct{}{}
	}
	for _, want := range required {
		if _, ok := have[want]; !ok {
			return false
		}
	}
	return true
}

// buildBearerChallenge formats a WWW-Authenticate header value per RFC 6750
// and RFC 9728. Empty errorCode omits the error param; empty resource URL
// omits the resource_metadata hint.
func buildBearerChallenge(resourceMetadataURL, errorCode string) string {
	parts := []string{`realm="asdlc"`}
	if errorCode != "" {
		parts = append(parts, `error="`+errorCode+`"`)
	}
	if resourceMetadataURL != "" {
		parts = append(parts, `resource_metadata="`+resourceMetadataURL+`"`)
	}
	return "Bearer " + strings.Join(parts, ", ")
}

func validateJWT(tokenString string, cfg Config, issuers compiledIssuers, audiences compiledAudiences) (*TokenClaims, error) {
	var claims *TokenClaims

	if cfg.JWKS != nil {
		token, err := jwt.ParseWithClaims(tokenString, &TokenClaims{}, func(tok *jwt.Token) (any, error) {
			if _, ok := tok.Method.(*jwt.SigningMethodRSA); !ok {
				return nil, fmt.Errorf("unexpected signing method: %v", tok.Header["alg"])
			}
			kid, ok := tok.Header["kid"].(string)
			if !ok || kid == "" {
				return nil, fmt.Errorf("kid not found in token header")
			}
			return cfg.JWKS.PublicKeyForKid(kid)
		})
		if err != nil {
			return nil, fmt.Errorf("failed to parse token: %w", err)
		}
		if !token.Valid {
			return nil, fmt.Errorf("token is not valid")
		}
		c, ok := token.Claims.(*TokenClaims)
		if !ok {
			return nil, fmt.Errorf("failed to extract claims")
		}
		claims = c
	} else if cfg.IsLocalDevEnv {
		extracted, err := extractClaimsUnverified(tokenString)
		if err != nil {
			return nil, err
		}
		if extracted.ExpiresAt != nil && !extracted.ExpiresAt.After(time.Now()) {
			return nil, fmt.Errorf("token has expired")
		}
		claims = extracted
	} else {
		return nil, fmt.Errorf("JWKS not configured")
	}

	if err := issuers.match(claims.Issuer); err != nil {
		return nil, err
	}
	if err := audiences.match(claims.Audience); err != nil {
		return nil, err
	}
	return claims, nil
}

// compiledIssuers is the per-request-friendly form of cfg.AllowedIssuers —
// trimmed and indexed once at Authenticator construction.
type compiledIssuers struct {
	allowed map[string]struct{}
}

func compileIssuers(allowed []string) compiledIssuers {
	set := make(map[string]struct{}, len(allowed))
	for _, a := range allowed {
		set[strings.TrimSpace(a)] = struct{}{}
	}
	return compiledIssuers{allowed: set}
}

func (c compiledIssuers) match(issuer string) error {
	if len(c.allowed) == 0 {
		return fmt.Errorf("no allowed issuers configured")
	}
	if _, ok := c.allowed[strings.TrimSpace(issuer)]; ok {
		return nil
	}
	return fmt.Errorf("invalid issuer: got %s", issuer)
}

// compiledAudiences is the per-request-friendly form of cfg.AllowedAudiences.
// badConfig captures bare-wildcard / empty misconfiguration so every request
// fails closed with a 401, matching the prior eager-validation behaviour.
type compiledAudiences struct {
	exact     map[string]struct{}
	prefixes  []string
	badConfig error
}

func compileAudiences(allowed []string) compiledAudiences {
	if len(allowed) == 0 {
		return compiledAudiences{badConfig: fmt.Errorf("no allowed audiences configured")}
	}
	exact := make(map[string]struct{}, len(allowed))
	var prefixes []string
	for _, a := range allowed {
		t := strings.TrimSpace(a)
		if t == "*" {
			return compiledAudiences{badConfig: fmt.Errorf("bare wildcard not allowed in audience configuration")}
		}
		if strings.HasSuffix(t, "*") {
			p := strings.TrimSuffix(t, "*")
			if p == "" {
				return compiledAudiences{badConfig: fmt.Errorf("bare wildcard not allowed in audience configuration")}
			}
			prefixes = append(prefixes, p)
			continue
		}
		exact[t] = struct{}{}
	}
	return compiledAudiences{exact: exact, prefixes: prefixes}
}

func (c compiledAudiences) match(audiences jwt.ClaimStrings) error {
	if c.badConfig != nil {
		return c.badConfig
	}
	for _, aud := range audiences {
		t := strings.TrimSpace(aud)
		if _, ok := c.exact[t]; ok {
			return nil
		}
		for _, p := range c.prefixes {
			if strings.HasPrefix(t, p) {
				return nil
			}
		}
	}
	return fmt.Errorf("invalid audience: got %v", audiences)
}

// validateIssuer and validateAudience are thin wrappers preserved for tests
// that exercise the matcher directly with a raw allowed-list.
func validateIssuer(issuer string, allowed []string) error {
	return compileIssuers(allowed).match(issuer)
}

func validateAudience(audiences jwt.ClaimStrings, allowed []string) error {
	return compileAudiences(allowed).match(audiences)
}

func extractClaimsUnverified(tokenString string) (*TokenClaims, error) {
	parts := strings.Split(tokenString, ".")
	if len(parts) != 3 {
		return nil, fmt.Errorf("invalid jwt: %d parts", len(parts))
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return nil, fmt.Errorf("invalid jwt payload encoding: %w", err)
	}
	var c TokenClaims
	if err := json.Unmarshal(payload, &c); err != nil {
		return nil, fmt.Errorf("invalid jwt payload: %w", err)
	}
	return &c, nil
}
