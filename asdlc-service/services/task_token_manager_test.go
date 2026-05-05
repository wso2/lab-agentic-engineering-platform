package services

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

func writeTestKey(t *testing.T, dir string, format string) (string, *rsa.PrivateKey) {
	t.Helper()
	priv, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	var der []byte
	switch format {
	case "pkcs1":
		der = x509.MarshalPKCS1PrivateKey(priv)
		der = pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: der})
	case "pkcs8":
		d, err := x509.MarshalPKCS8PrivateKey(priv)
		if err != nil {
			t.Fatalf("marshal pkcs8: %v", err)
		}
		der = pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: d})
	default:
		t.Fatalf("unknown format %s", format)
	}
	path := filepath.Join(dir, "key.pem")
	if err := os.WriteFile(path, der, 0600); err != nil {
		t.Fatalf("write key: %v", err)
	}
	return path, priv
}

func TestTaskTokenManager_PKCS1(t *testing.T) {
	dir := t.TempDir()
	path, _ := writeTestKey(t, dir, "pkcs1")
	mgr, err := NewTaskTokenManager(TaskTokenConfig{
		PrivateKeyPath: path,
		Issuer:         "asdlc-bff",
		Audience:       "git-service",
		TTL:            1 * time.Hour,
	})
	if err != nil {
		t.Fatalf("NewTaskTokenManager: %v", err)
	}
	tok, err := mgr.Issue("task-123", "org-456", "proj-789")
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}

	// Parse and verify with the manager's public key.
	parsed, err := jwt.ParseWithClaims(tok, &TaskClaims{}, func(t *jwt.Token) (any, error) {
		return mgr.publicKey, nil
	})
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if !parsed.Valid {
		t.Fatal("token not valid")
	}
	claims, ok := parsed.Claims.(*TaskClaims)
	if !ok {
		t.Fatalf("wrong claim type: %T", parsed.Claims)
	}
	if claims.TaskID != "task-123" || claims.OcOrgID != "org-456" || claims.ProjectID != "proj-789" {
		t.Errorf("claims wrong: %+v", claims)
	}
	if claims.Issuer != "asdlc-bff" {
		t.Errorf("issuer wrong: %s", claims.Issuer)
	}
	if len(claims.Audience) != 1 || claims.Audience[0] != "git-service" {
		t.Errorf("audience wrong: %v", claims.Audience)
	}
	if parsed.Header["kid"] != mgr.KeyID() {
		t.Errorf("kid header missing or mismatched: got %v want %s", parsed.Header["kid"], mgr.KeyID())
	}
}

func TestTaskTokenManager_PKCS8(t *testing.T) {
	dir := t.TempDir()
	path, _ := writeTestKey(t, dir, "pkcs8")
	mgr, err := NewTaskTokenManager(TaskTokenConfig{
		PrivateKeyPath: path,
		Issuer:         "asdlc-bff",
		Audience:       "git-service",
		TTL:            1 * time.Hour,
	})
	if err != nil {
		t.Fatalf("NewTaskTokenManager: %v", err)
	}
	if _, err := mgr.Issue("task-1", "org-1", ""); err != nil {
		t.Errorf("Issue: %v", err)
	}
}

func TestTaskTokenManager_JWKS(t *testing.T) {
	dir := t.TempDir()
	path, _ := writeTestKey(t, dir, "pkcs1")
	mgr, err := NewTaskTokenManager(TaskTokenConfig{
		PrivateKeyPath: path,
		Issuer:         "asdlc-bff",
		Audience:       "git-service",
		TTL:            1 * time.Hour,
	})
	if err != nil {
		t.Fatalf("NewTaskTokenManager: %v", err)
	}
	jwks := mgr.JWKS()
	if len(jwks.Keys) != 1 {
		t.Fatalf("expected 1 key, got %d", len(jwks.Keys))
	}
	k := jwks.Keys[0]
	if k.Kty != "RSA" || k.Alg != "RS256" || k.Use != "sig" {
		t.Errorf("JWK fields wrong: %+v", k)
	}
	if k.Kid != mgr.KeyID() {
		t.Errorf("kid mismatch: %s vs %s", k.Kid, mgr.KeyID())
	}
	if k.N == "" || k.E == "" {
		t.Errorf("modulus/exponent missing: %+v", k)
	}
}

func TestTaskTokenManager_RejectsBadKey(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "bad.pem")
	if err := os.WriteFile(path, []byte("not a pem"), 0600); err != nil {
		t.Fatal(err)
	}
	_, err := NewTaskTokenManager(TaskTokenConfig{
		PrivateKeyPath: path,
		Issuer:         "asdlc-bff",
		Audience:       "git-service",
		TTL:            1 * time.Hour,
	})
	if err == nil {
		t.Error("expected error on malformed key")
	}
}

func TestTaskTokenManager_RejectsMissingConfig(t *testing.T) {
	if _, err := NewTaskTokenManager(TaskTokenConfig{}); err == nil {
		t.Error("expected error on empty config")
	}
}
