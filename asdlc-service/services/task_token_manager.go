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
