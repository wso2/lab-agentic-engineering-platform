// Package jwtassertion provides JWKS-backed JWT validation middleware for
// task JWTs issued by the BFF.
package jwtassertion

import (
	"fmt"
	"log/slog"
	"net/http"
	"strings"

	"github.com/golang-jwt/jwt/v5"

	"github.com/wso2/asdlc/database-service/utils"
)

// Config configures the JWT verifier.
type Config struct {
	// JWKS is the cache used to fetch signing keys.
	JWKS *JWKSCache
	// AllowedIssuer is the expected iss claim value.
	AllowedIssuer string
	// AllowedAudience is the expected aud claim value.
	AllowedAudience string
}

// Middleware is the standard http.Handler wrapping signature.
type Middleware func(http.Handler) http.Handler

// Authenticator builds a middleware that verifies RS256 task JWTs against
// the given config. Tokens are read from the Authorization: Bearer header.
func Authenticator(cfg Config) Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			authHeader := r.Header.Get("Authorization")
			if authHeader == "" {
				utils.WriteErrorResponse(w, http.StatusUnauthorized, "missing Authorization header")
				return
			}
			tokenString := strings.TrimPrefix(authHeader, "Bearer ")
			tokenString = strings.TrimSpace(tokenString)
			if tokenString == "" || tokenString == authHeader {
				utils.WriteErrorResponse(w, http.StatusUnauthorized, "malformed Authorization header")
				return
			}

			if err := validateTaskJWT(tokenString, cfg); err != nil {
				slog.Warn("task JWT validation failed", slog.String("error", err.Error()))
				utils.WriteErrorResponse(w, http.StatusUnauthorized, "invalid token")
				return
			}

			next.ServeHTTP(w, r)
		})
	}
}

func validateTaskJWT(tokenString string, cfg Config) error {
	_, err := jwt.ParseWithClaims(tokenString, &jwt.RegisteredClaims{}, func(tok *jwt.Token) (any, error) {
		if _, ok := tok.Method.(*jwt.SigningMethodRSA); !ok {
			return nil, fmt.Errorf("unexpected signing method: %v", tok.Header["alg"])
		}
		kid, ok := tok.Header["kid"].(string)
		if !ok || kid == "" {
			return nil, fmt.Errorf("kid not found in token header")
		}
		return cfg.JWKS.PublicKeyForKid(kid)
	}, jwt.WithIssuer(cfg.AllowedIssuer), jwt.WithAudience(cfg.AllowedAudience), jwt.WithValidMethods([]string{"RS256"}))
	return err
}
