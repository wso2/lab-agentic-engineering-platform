package webhook

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"testing"
)

// staticProvider is a tiny test double for SecretProvider — returns the
// configured list of secrets regardless of ocOrgID. The Phase 0
// EnvSecretProvider has been replaced by GitServiceSecretProvider in PR
// B; verifier tests don't need the cache layer, so they use this stub
// directly.
type staticProvider struct {
	secrets [][]byte
}

func (p *staticProvider) Secrets(_ context.Context, _ string, _ SecretOpts) ([][]byte, error) {
	return p.secrets, nil
}

func newStaticProvider(secret string) *staticProvider {
	if secret == "" {
		return &staticProvider{}
	}
	return &staticProvider{secrets: [][]byte{[]byte(secret)}}
}

func sign(secret string, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}

func TestVerifierAcceptsValidSignature(t *testing.T) {
	secret := "shhh"
	body := []byte(`{"action":"opened"}`)
	v := NewVerifier(newStaticProvider(secret))

	if err := v.Verify(context.Background(), "platform", sign(secret, body), body); err != nil {
		t.Fatalf("expected accept, got %v", err)
	}
}

func TestVerifierRejectsBadSignature(t *testing.T) {
	v := NewVerifier(newStaticProvider("good"))
	body := []byte(`{}`)
	err := v.Verify(context.Background(), "platform", sign("bad", body), body)
	if !errors.Is(err, ErrSignatureMismatch) {
		t.Fatalf("expected mismatch, got %v", err)
	}
}

func TestVerifierRejectsMalformedHeader(t *testing.T) {
	v := NewVerifier(newStaticProvider("x"))
	err := v.Verify(context.Background(), "platform", "invalid", []byte(`{}`))
	if !errors.Is(err, ErrSignatureMalformed) {
		t.Fatalf("expected malformed, got %v", err)
	}
}

func TestVerifierEmptySecretRejects(t *testing.T) {
	// staticProvider with no secrets — verifier sees no candidates → mismatch.
	v := NewVerifier(newStaticProvider(""))
	err := v.Verify(context.Background(), "platform", sign("x", []byte(`{}`)), []byte(`{}`))
	if !errors.Is(err, ErrSignatureMismatch) {
		t.Fatalf("expected mismatch for empty provider, got %v", err)
	}
}
