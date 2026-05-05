package services

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// BearerService issues HS256 connect-state JWTs (CSRF state for the GitHub
// App OAuth + post-install callbacks). The signing key
// (OAUTH_STATE_SIGNING_KEY) never leaves the BFF — the callback handler is
// the sole verifier, so a public/private split isn't needed.
type BearerService struct {
	signingKey []byte
	ttl        time.Duration
}

// NewBearerService creates a bearer issuer. ttl is the connect-state
// lifetime; pass per-call ttl to IssueConnectState — this default is only
// here for legacy callers that don't pass one explicitly.
func NewBearerService(signingKey string, ttl time.Duration) *BearerService {
	return &BearerService{signingKey: []byte(signingKey), ttl: ttl}
}

// ----------------------------------------------------------------------------
// Connect-state bearer (App-mode connect CSRF state)
// ----------------------------------------------------------------------------

// ConnectStateClaims rides the GitHub `state` query param through every
// App-mode connect roundtrip — both OAuth (`?code=...`) and post-install
// (`?installation_id=...`) callbacks. Replaces the prior split between
// AppStateClaims (install-callback) and BindStateClaims (OAuth-bind).
//
// InstallationID is 0 when the connect flow hasn't pinned a specific
// install yet (initial OAuth roundtrip → intersect-and-route). It's non-
// zero when the user picked one from the 2+ picker — the callback then
// re-verifies the user is an admin of that exact install before binding.
//
// 15-minute TTL bounds replay; the `kind` field distinguishes from a
// task bearer so a leaked task bearer can't be substituted on the
// callback (and vice-versa).
type ConnectStateClaims struct {
	Kind           string `json:"kind"` // always "connect-state"
	OcOrgID        string `json:"ocOrgId"`
	InstallationID int64  `json:"installationId,omitempty"`
	Actor          string `json:"actor"`
	IssuedAt       int64  `json:"iat"`
	ExpiresAt      int64  `json:"exp"`
}

// IssueConnectState mints a connect-state JWT. installationID is 0 for
// the initial OAuth roundtrip and non-zero when re-issuing for a user-
// picked candidate from the picker.
func (s *BearerService) IssueConnectState(ocOrgID, actor string, installationID int64, ttl time.Duration) (string, error) {
	if len(s.signingKey) == 0 {
		return "", fmt.Errorf("OAUTH_STATE_SIGNING_KEY not configured")
	}
	now := time.Now()
	claims := ConnectStateClaims{
		Kind:           "connect-state",
		OcOrgID:        ocOrgID,
		InstallationID: installationID,
		Actor:          actor,
		IssuedAt:       now.Unix(),
		ExpiresAt:      now.Add(ttl).Unix(),
	}
	return s.signClaims(claims)
}

// VerifyConnectState parses + verifies a connect-state JWT. installationID=0
// is valid (unpinned roundtrip).
func (s *BearerService) VerifyConnectState(token string) (*ConnectStateClaims, error) {
	if len(s.signingKey) == 0 {
		return nil, fmt.Errorf("OAUTH_STATE_SIGNING_KEY not configured")
	}
	parts := splitJWT(token)
	if len(parts) != 3 {
		return nil, errors.New("malformed JWT")
	}
	signingInput := parts[0] + "." + parts[1]
	mac := hmac.New(sha256.New, s.signingKey)
	mac.Write([]byte(signingInput))
	expected := encodeSegment(mac.Sum(nil))
	if !hmac.Equal([]byte(expected), []byte(parts[2])) {
		return nil, errors.New("signature mismatch")
	}
	claimsJSON, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return nil, fmt.Errorf("decode claims: %w", err)
	}
	var claims ConnectStateClaims
	if err := json.Unmarshal(claimsJSON, &claims); err != nil {
		return nil, fmt.Errorf("unmarshal claims: %w", err)
	}
	if claims.Kind != "connect-state" {
		return nil, fmt.Errorf("wrong kind: %q (expected connect-state)", claims.Kind)
	}
	if time.Now().Unix() > claims.ExpiresAt {
		return nil, errors.New("expired")
	}
	if claims.OcOrgID == "" {
		return nil, errors.New("missing ocOrgId")
	}
	return &claims, nil
}

func (s *BearerService) signClaims(claims any) (string, error) {
	header := map[string]string{"alg": "HS256", "typ": "JWT"}
	headerJSON, _ := json.Marshal(header)
	claimsJSON, err := json.Marshal(claims)
	if err != nil {
		return "", fmt.Errorf("marshal claims: %w", err)
	}
	signingInput := encodeSegment(headerJSON) + "." + encodeSegment(claimsJSON)
	mac := hmac.New(sha256.New, s.signingKey)
	mac.Write([]byte(signingInput))
	return signingInput + "." + encodeSegment(mac.Sum(nil)), nil
}

func splitJWT(token string) []string {
	out := make([]string, 0, 3)
	start := 0
	for i := 0; i < len(token); i++ {
		if token[i] == '.' {
			out = append(out, token[start:i])
			start = i + 1
		}
	}
	out = append(out, token[start:])
	return out
}

func encodeSegment(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }
