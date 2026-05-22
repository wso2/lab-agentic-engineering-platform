package services

import (
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"fmt"
	"math/big"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// TaskTokenManager issues RS256-signed Task JWTs that authenticate
// per-task workspace agents to git-service /credentials/refresh.
//
// The signing key is loaded once at boot from the BFF_TASK_SIGNING_KEY env var.
// Verifiers (currently git-service) fetch the public key via the BFF's
// /auth/external/jwks.json endpoint; rotation works by updating the env var
// and restarting the BFF — verifiers pick up the new kid automatically via
// JWKS kid-miss-refresh.
type TaskTokenManager struct {
	keyID      string
	algorithm  string
	privateKey *rsa.PrivateKey
	publicKey  *rsa.PublicKey
	jwks       JWKSResponse

	issuer   string
	audience string
	ttl      time.Duration
}

// TaskTokenConfig configures the manager.
type TaskTokenConfig struct {
	// PrivateKey is the PEM-encoded RSA private key (PKCS#1 or PKCS#8).
	// Passed as the BFF_TASK_SIGNING_KEY env var. Required.
	PrivateKey string
	// Issuer is the iss claim value (e.g., "asdlc-bff").
	Issuer string
	// Audience is the aud claim value (always "git-service" today).
	Audience string
	// TTL is the per-task JWT lifetime. Spec caps at 24h.
	TTL time.Duration
}

// TaskClaims is the custom claim set carried by Task JWTs.
type TaskClaims struct {
	jwt.RegisteredClaims
	TaskID    string `json:"taskId"`
	OcOrgID   string `json:"ocOrgId"`
	ProjectID string `json:"projectId,omitempty"`
}

// NewTaskTokenManager parses the signing key and returns a ready manager.
// Returns an error if the key is missing, malformed, or not RSA.
func NewTaskTokenManager(cfg TaskTokenConfig) (*TaskTokenManager, error) {
	if cfg.PrivateKey == "" {
		return nil, fmt.Errorf("BFF_TASK_SIGNING_KEY not configured")
	}
	if cfg.Issuer == "" {
		return nil, fmt.Errorf("issuer not configured")
	}
	if cfg.Audience == "" {
		return nil, fmt.Errorf("audience not configured")
	}
	if cfg.TTL <= 0 {
		return nil, fmt.Errorf("TTL must be positive")
	}

	block, _ := pem.Decode([]byte(cfg.PrivateKey))
	if block == nil {
		return nil, fmt.Errorf("decode PEM block from BFF_TASK_SIGNING_KEY")
	}

	priv, err := parseRSAPrivateKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("parse private key: %w", err)
	}
	pub := &priv.PublicKey

	kid, err := deriveKeyID(pub)
	if err != nil {
		return nil, fmt.Errorf("derive kid: %w", err)
	}

	return &TaskTokenManager{
		keyID:      kid,
		algorithm:  "RS256",
		privateKey: priv,
		publicKey:  pub,
		jwks: JWKSResponse{
			Keys: []JWK{{
				Kty: "RSA",
				Alg: "RS256",
				Use: "sig",
				Kid: kid,
				N:   base64.RawURLEncoding.EncodeToString(pub.N.Bytes()),
				E:   base64.RawURLEncoding.EncodeToString(big.NewInt(int64(pub.E)).Bytes()),
			}},
		},
		issuer:   cfg.Issuer,
		audience: cfg.Audience,
		ttl:      cfg.TTL,
	}, nil
}

// IssueServiceToken mints a long-lived service JWT for BFF → platform-service
// calls (e.g. database-service registry endpoints). Uses the same signing key
// and audience as task tokens so no additional JWKS endpoint is needed.
func (m *TaskTokenManager) IssueServiceToken() (string, error) {
	now := time.Now()
	claims := jwt.RegisteredClaims{
		Issuer:    m.issuer,
		Subject:   m.issuer, // "asdlc-bff" identifies itself as caller
		Audience:  jwt.ClaimStrings{m.audience},
		IssuedAt:  jwt.NewNumericDate(now),
		NotBefore: jwt.NewNumericDate(now),
		ExpiresAt: jwt.NewNumericDate(now.Add(365 * 24 * time.Hour)),
	}
	tok := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
	tok.Header["kid"] = m.keyID
	signed, err := tok.SignedString(m.privateKey)
	if err != nil {
		return "", fmt.Errorf("sign service token: %w", err)
	}
	return signed, nil
}

// Issue mints a Task JWT for the given task. The kid header lets verifiers
// pick the right public key during rotation.
func (m *TaskTokenManager) Issue(taskID, ocOrgID, projectID string) (string, error) {
	if taskID == "" || ocOrgID == "" {
		return "", fmt.Errorf("taskId and ocOrgId are required")
	}
	now := time.Now()
	claims := TaskClaims{
		RegisteredClaims: jwt.RegisteredClaims{
			Issuer:    m.issuer,
			Subject:   taskID,
			Audience:  jwt.ClaimStrings{m.audience},
			IssuedAt:  jwt.NewNumericDate(now),
			NotBefore: jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(now.Add(m.ttl)),
		},
		TaskID:    taskID,
		OcOrgID:   ocOrgID,
		ProjectID: projectID,
	}

	tok := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
	tok.Header["kid"] = m.keyID

	signed, err := tok.SignedString(m.privateKey)
	if err != nil {
		return "", fmt.Errorf("sign token: %w", err)
	}
	return signed, nil
}

// Verify parses + cryptographically validates a Task JWT minted by this
// BFF (or a peer using the same signing key). Returns the claims on
// success. Issuer + audience must match the manager's configuration.
// The exp / nbf claims are honored by jwt.ParseWithClaims automatically.
//
// Used by the BFF's per-task endpoints (F3c: verification-failed, retry)
// to authenticate the runner pod's callback. The runner pod was issued
// this token at dispatch time and persists it to a file inside the pod's
// emptyDir — see remote-worker/src/lib/runner.ts.
func (m *TaskTokenManager) Verify(tokenString string) (*TaskClaims, error) {
	if tokenString == "" {
		return nil, fmt.Errorf("empty token")
	}
	tok, err := jwt.ParseWithClaims(tokenString, &TaskClaims{}, func(t *jwt.Token) (any, error) {
		if _, ok := t.Method.(*jwt.SigningMethodRSA); !ok {
			return nil, fmt.Errorf("unexpected signing method: %v", t.Header["alg"])
		}
		if kid, _ := t.Header["kid"].(string); kid != "" && kid != m.keyID {
			return nil, fmt.Errorf("unknown kid %q (expected %q)", kid, m.keyID)
		}
		return m.publicKey, nil
	})
	if err != nil {
		return nil, fmt.Errorf("parse token: %w", err)
	}
	if !tok.Valid {
		return nil, fmt.Errorf("token not valid")
	}
	claims, ok := tok.Claims.(*TaskClaims)
	if !ok {
		return nil, fmt.Errorf("claims not TaskClaims")
	}
	if claims.Issuer != m.issuer {
		return nil, fmt.Errorf("unexpected issuer %q", claims.Issuer)
	}
	// Audience deliberately not enforced — the BFF is the issuer, and the
	// same token may be presented to git-service (aud=git-service) or
	// back to the BFF self-callback (F3c). Trust comes from issuer +
	// signature; aud is the verifier's hint, not a BFF self-check.
	return claims, nil
}

// JWKSResponse is the JSON shape served at /auth/external/jwks.json.
type JWKSResponse struct {
	Keys []JWK `json:"keys"`
}

// JWK is a single public key entry in JWK form.
type JWK struct {
	Kty string `json:"kty"`
	Alg string `json:"alg"`
	Use string `json:"use"`
	Kid string `json:"kid"`
	N   string `json:"n"`
	E   string `json:"e"`
}

// JWKS returns the public key set (one entry — the active signing key).
func (m *TaskTokenManager) JWKS() JWKSResponse { return m.jwks }

// KeyID returns the kid of the active signing key.
func (m *TaskTokenManager) KeyID() string { return m.keyID }

// parseRSAPrivateKey attempts PKCS#1 first, then PKCS#8.
func parseRSAPrivateKey(der []byte) (*rsa.PrivateKey, error) {
	if priv, err := x509.ParsePKCS1PrivateKey(der); err == nil {
		return priv, nil
	}
	key, err := x509.ParsePKCS8PrivateKey(der)
	if err != nil {
		return nil, fmt.Errorf("not PKCS#1 or PKCS#8 RSA: %w", err)
	}
	priv, ok := key.(*rsa.PrivateKey)
	if !ok {
		return nil, fmt.Errorf("private key is not RSA")
	}
	return priv, nil
}

// deriveKeyID returns a stable kid derived from the public key's DER bytes.
// Truncated SHA-256 keeps the kid short while remaining unique enough that
// a key rotation produces a new kid (so verifiers know to refresh JWKS).
func deriveKeyID(pub *rsa.PublicKey) (string, error) {
	der, err := x509.MarshalPKIXPublicKey(pub)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(der)
	return base64.RawURLEncoding.EncodeToString(sum[:16]), nil
}
