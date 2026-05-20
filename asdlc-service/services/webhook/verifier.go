package webhook

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
)

// Verifier validates an X-Hub-Signature-256 header against the accepted
// HMAC keys for the routing-resolved org.
//
// Cache-miss-then-refetch on HMAC mismatch (github-integration-phase0.md
// §6.4): when the cached secret list rejects an event, we ask the provider
// to refetch fresh secrets and re-validate. This closes the rotation hole
// where the cache holds the old list and the sender (GitHub) starts signing
// with the new one. Legitimate rotation propagates within one event;
// repeated forgery just thrashes the refetch path.
//
// Phase 2 PR B adds a token-bucket rate-limit (§8.4) on the forced-refetch
// path so a forged stream can't amplify into git-service load.
type Verifier struct {
	provider SecretProvider
	limiter  *RefetchLimiter // optional; nil = unbounded refetch
}

// ErrSignatureMismatch is returned when no accepted secret produces a
// matching signature. The receiver responds 401.
var ErrSignatureMismatch = errors.New("webhook signature mismatch")

// ErrSignatureMalformed is returned when the X-Hub-Signature-256 header is
// missing or doesn't match the expected sha256=<hex> shape.
var ErrSignatureMalformed = errors.New("webhook signature malformed")

// NewVerifier constructs a verifier with the given provider. The
// limiter is optional — nil means refetch is always allowed (useful in
// tests).
func NewVerifier(provider SecretProvider) *Verifier {
	return &Verifier{provider: provider}
}

// WithRefetchLimiter attaches a rate limiter to the verifier. Per
// phase2.md §8.4: 1 refetch/s, burst 5, keyed on (ocOrgID, sourceIP).
func (v *Verifier) WithRefetchLimiter(l *RefetchLimiter) *Verifier {
	v.limiter = l
	return v
}

// Verify validates the signature header against the body using the secrets
// resolved for ocOrgID. limiterKey is consumed only when a forced refetch
// is needed; supply the (ocOrgID, sourceIP) pair to scope the bucket.
func (v *Verifier) Verify(ctx context.Context, ocOrgID, signatureHeader string, body []byte) error {
	return v.VerifyWithKey(ctx, ocOrgID, ocOrgID, signatureHeader, body)
}

// VerifyWithKey is Verify but lets the caller supply an explicit
// limiterKey distinct from ocOrgID. Used by the webhook controller to
// scope refetch limits to (ocOrgID, sourceIP).
func (v *Verifier) VerifyWithKey(ctx context.Context, ocOrgID, limiterKey, signatureHeader string, body []byte) error {
	const prefix = "sha256="
	if !strings.HasPrefix(signatureHeader, prefix) {
		return ErrSignatureMalformed
	}
	provided, err := hex.DecodeString(signatureHeader[len(prefix):])
	if err != nil {
		return fmt.Errorf("%w: %v", ErrSignatureMalformed, err)
	}

	// First attempt: cached/normal secrets.
	secrets, err := v.provider.Secrets(ctx, ocOrgID, SecretOpts{Force: false})
	if err != nil {
		return fmt.Errorf("fetch secrets: %w", err)
	}
	if matchesAny(secrets, body, provided) {
		return nil
	}

	// Mismatch: rate-limited forced refetch.
	if v.limiter != nil && !v.limiter.Allow(limiterKey) {
		return ErrSignatureMismatch
	}
	fresh, err := v.provider.Secrets(ctx, ocOrgID, SecretOpts{Force: true})
	if err != nil {
		return fmt.Errorf("fetch secrets (force): %w", err)
	}
	if matchesAny(fresh, body, provided) {
		return nil
	}
	return ErrSignatureMismatch
}

func matchesAny(secrets [][]byte, body, provided []byte) bool {
	for _, secret := range secrets {
		if len(secret) == 0 {
			continue
		}
		mac := hmac.New(sha256.New, secret)
		mac.Write(body)
		expected := mac.Sum(nil)
		if hmac.Equal(expected, provided) {
			return true
		}
	}
	return false
}
