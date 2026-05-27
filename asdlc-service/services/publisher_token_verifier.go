package services

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/golang-jwt/jwt/v5"

	"github.com/wso2/asdlc/asdlc-service/middleware/jwtassertion"
)

// PublisherTokenVerifier validates Thunder-issued client_credentials
// access tokens whose audience identifies a per-org publisher OAuth app
// ("asdlc-publisher-{orgHandle}"). Mirrors agent-manager's
// PublisherClientAuthMiddleware at
// agent-manager-service/middleware/jwtassertion/auth.go:106 — same
// regex shape, same cross-org defense (subject's embedded org handle
// MUST match the ouHandle claim).
//
// Used by the runner-callback handlers (Skills, VerificationFailed,
// Refresh) to accept WS2.4's per-org publisher cc tokens alongside the
// legacy BFF-signed TaskJWT.
type PublisherTokenVerifier struct {
	jwks           *jwtassertion.JWKSCache
	expectedIssuer string
	audiencePrefix string
	subjectPattern *regexp.Regexp
}

// PublisherClaims is the projection of a verified publisher cc token.
// OrgHandle is the canonical OC org handle (extracted from subject +
// cross-checked against the ouHandle claim).
type PublisherClaims struct {
	jwt.RegisteredClaims
	OuId      string `json:"ouId"`
	OuName    string `json:"ouName"`
	OuHandle  string `json:"ouHandle"`
	OrgHandle string `json:"-"`
}

// NewPublisherTokenVerifier returns a verifier. audiencePrefix is the
// shared prefix on every per-org publisher client (e.g. "asdlc-publisher-").
// jwks must be a populated cache pointed at the platform IDP's JWKS
// endpoint; expectedIssuer is the iss claim Thunder stamps (e.g.
// "platform-idp"). Returns nil when any input is empty — composition root
// uses this to disable WS2.4 cc verification cleanly in dev.
func NewPublisherTokenVerifier(jwks *jwtassertion.JWKSCache, expectedIssuer, audiencePrefix string) *PublisherTokenVerifier {
	if jwks == nil || strings.TrimSpace(expectedIssuer) == "" || strings.TrimSpace(audiencePrefix) == "" {
		return nil
	}
	escaped := regexp.QuoteMeta(audiencePrefix)
	pat := regexp.MustCompile("^" + escaped + `[a-zA-Z0-9][a-zA-Z0-9._-]*$`)
	return &PublisherTokenVerifier{
		jwks:           jwks,
		expectedIssuer: expectedIssuer,
		audiencePrefix: audiencePrefix,
		subjectPattern: pat,
	}
}

// Verify parses + validates the publisher cc token. Returns canonical
// claims on success; rejects with a descriptive error otherwise. Caller
// is responsible for the in-handler scope check (e.g. validating that
// the runner-presented taskID belongs to claims.OrgHandle).
func (v *PublisherTokenVerifier) Verify(tokenString string) (*PublisherClaims, error) {
	if v == nil {
		return nil, fmt.Errorf("publisher verifier not configured")
	}
	if tokenString == "" {
		return nil, fmt.Errorf("empty token")
	}
	tok, err := jwt.ParseWithClaims(tokenString, &PublisherClaims{}, func(t *jwt.Token) (any, error) {
		if _, ok := t.Method.(*jwt.SigningMethodRSA); !ok {
			return nil, fmt.Errorf("unexpected signing method: %v", t.Header["alg"])
		}
		kid, _ := t.Header["kid"].(string)
		if kid == "" {
			return nil, fmt.Errorf("kid missing from token header")
		}
		return v.jwks.PublicKeyForKid(kid)
	})
	if err != nil {
		return nil, fmt.Errorf("parse publisher token: %w", err)
	}
	if !tok.Valid {
		return nil, fmt.Errorf("publisher token not valid")
	}
	claims, ok := tok.Claims.(*PublisherClaims)
	if !ok {
		return nil, fmt.Errorf("claims not PublisherClaims")
	}
	if claims.Issuer != v.expectedIssuer {
		return nil, fmt.Errorf("unexpected issuer %q", claims.Issuer)
	}

	// Identify the publisher app via the audience claim (Thunder stamps
	// the cc client_id as aud per the agent-manager pattern at
	// agent-manager/agent-manager-service/middleware/jwtassertion/auth.go:93).
	// Subject is a Thunder-generated UUID, not the client_id.
	var publisherAud string
	for _, a := range claims.Audience {
		if v.subjectPattern.MatchString(a) {
			publisherAud = a
			break
		}
	}
	if publisherAud == "" {
		return nil, fmt.Errorf("no publisher audience present (expected %s*)", v.audiencePrefix)
	}
	orgHandle := strings.TrimPrefix(publisherAud, v.audiencePrefix)

	// Cross-org defense: the org handle embedded in the audience MUST
	// match the ouHandle claim. Defends against an attacker who steals
	// org A's publisher cc token and tries to act on org B's data by
	// hand-rolling a token with a mismatched ouHandle (which would fail
	// signature verification, but layered defense).
	if claims.OuHandle == "" {
		return nil, fmt.Errorf("ouHandle claim missing")
	}
	if claims.OuHandle != orgHandle {
		return nil, fmt.Errorf("ouHandle %q does not match audience org %q", claims.OuHandle, orgHandle)
	}
	claims.OrgHandle = orgHandle
	return claims, nil
}
