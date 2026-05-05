// Package webhook contains the BFF's GitHub-webhook receiver: HMAC
// validation, dedup, and per-event projection onto the ComponentTask state
// machine.
package webhook

import (
	"context"
	"sync"
	"time"
)

// SecretProvider returns the accepted HMAC keys for a given org, ordered
// current-first.
//
// Phase 0 ignored ocOrgID and returned a single platform-wide secret;
// Phase 2 PR B looks the value up via git-service against the per-org
// credential record. The receiver code path is identical: parse routing
// key → resolve ocOrgID → provider.Secrets(ocOrgID) → HMAC-validate
// against any of the returned secrets. The seam exists in Phase 0 so
// PR B fills in without rippling.
type SecretProvider interface {
	// Secrets returns the accepted HMAC keys. opts.Force=true bypasses any
	// internal cache (used after an HMAC mismatch to handle in-flight rotation;
	// see github-integration-phase0.md §6.4).
	Secrets(ctx context.Context, ocOrgID string, opts SecretOpts) ([][]byte, error)
}

// SecretOpts modifies a Secrets() call.
type SecretOpts struct {
	// Force bypasses any caching layer to refetch from the source of truth.
	Force bool
}

// ----------------------------------------------------------------------------
// GitServiceSecretProvider — Phase 2 PR B
// ----------------------------------------------------------------------------

// SecretFetcher is the dependency the GitServiceSecretProvider calls to
// fetch the accepted HMAC keys. It's gitservice.Client.GetWebhookSecrets
// in production, mocked in unit tests.
type SecretFetcher interface {
	GetWebhookSecrets(ctx context.Context, ocOrgID string) ([][]byte, error)
}

// GitServiceSecretProvider reads webhook secrets from git-service's
// /internal/credentials/orgs/{ocOrgID}/webhook-secrets endpoint. The
// kind branch lives inside git-service: PAT mode reads from
// org_credentials.webhook_secrets; App mode reads from the platform-wide
// _platform/github/app/webhook_secret list. The receiver is unaware of
// kind — it just calls Secrets(ctx, ocOrgID).
//
// 30-second LRU cache keyed on ocOrgID. Force=true bypasses + refreshes.
type GitServiceSecretProvider struct {
	fetcher SecretFetcher
	ttl     time.Duration
	mu      sync.RWMutex
	cache   map[string]secretCacheEntry
}

type secretCacheEntry struct {
	secrets  [][]byte
	expireAt time.Time
}

// NewGitServiceSecretProvider constructs the provider with the supplied
// fetcher and TTL (default 30s per phase2.md §8.3).
func NewGitServiceSecretProvider(fetcher SecretFetcher, ttl time.Duration) *GitServiceSecretProvider {
	if ttl == 0 {
		ttl = 30 * time.Second
	}
	return &GitServiceSecretProvider{
		fetcher: fetcher,
		ttl:     ttl,
		cache:   map[string]secretCacheEntry{},
	}
}

// Secrets returns the accepted HMAC keys for ocOrgID.
func (p *GitServiceSecretProvider) Secrets(ctx context.Context, ocOrgID string, opts SecretOpts) ([][]byte, error) {
	if !opts.Force {
		if entry, ok := p.lookup(ocOrgID); ok {
			return entry, nil
		}
	}
	secrets, err := p.fetcher.GetWebhookSecrets(ctx, ocOrgID)
	if err != nil {
		// On error, fall back to whatever's in the cache rather than
		// failing the verifier outright — a transient git-service blip
		// shouldn't block legitimate deliveries.
		if entry, ok := p.lookup(ocOrgID); ok {
			return entry, nil
		}
		return nil, err
	}
	p.store(ocOrgID, secrets)
	return secrets, nil
}

// Invalidate drops the cache entry for ocOrgID. Used by the install
// handlers when the webhook-secret list rotates (suspend/unsuspend,
// disconnect, App-secret rotation).
func (p *GitServiceSecretProvider) Invalidate(ocOrgID string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	delete(p.cache, ocOrgID)
}

func (p *GitServiceSecretProvider) lookup(ocOrgID string) ([][]byte, bool) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	entry, ok := p.cache[ocOrgID]
	if !ok || time.Now().After(entry.expireAt) {
		return nil, false
	}
	return entry.secrets, true
}

func (p *GitServiceSecretProvider) store(ocOrgID string, secrets [][]byte) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.cache[ocOrgID] = secretCacheEntry{
		secrets:  secrets,
		expireAt: time.Now().Add(p.ttl),
	}
}
