package jwtassertion

import (
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log/slog"
	"math/big"
	"net/http"
	"regexp"
	"sync"
	"time"

	"golang.org/x/sync/singleflight"
)

// JWKS represents a JSON Web Key Set.
type JWKS struct {
	Keys []JSONWebKey `json:"keys"`
}

// JSONWebKey represents a single key in a JWKS.
type JSONWebKey struct {
	Kty string   `json:"kty"`
	Kid string   `json:"kid"`
	Use string   `json:"use"`
	N   string   `json:"n"`
	E   string   `json:"e"`
	Alg string   `json:"alg"`
	X5c []string `json:"x5c,omitempty"`
}

// validKidPattern allows alphanumeric, hyphens, underscores, dots, colons,
// equals (base64 padding), plus, forward slash, and tilde — covering base64
// standard and URL-safe encodings commonly used in kid values.
var validKidPattern = regexp.MustCompile(`^[a-zA-Z0-9._:=+/~-]{1,256}$`)

const (
	jwksCacheTTL       = 1 * time.Hour
	minRefreshInterval = 30 * time.Second
)

// JWKSCache caches a single JWKS source.
type JWKSCache struct {
	url                string
	httpClient         *http.Client
	minRefreshInterval time.Duration

	mu            sync.RWMutex
	keys          *JWKS
	fetchedAt     time.Time
	lastRefreshAt time.Time

	refreshGroup singleflight.Group
}

// NewJWKSCache returns a new cache for the given JWKS URL.
func NewJWKSCache(url string) *JWKSCache {
	return &JWKSCache{
		url:                url,
		httpClient:         &http.Client{Timeout: 10 * time.Second},
		minRefreshInterval: minRefreshInterval,
	}
}

// PublicKeyForKid returns the RSA public key for the given kid, refreshing
// the cache once on miss before failing.
func (c *JWKSCache) PublicKeyForKid(kid string) (*rsa.PublicKey, error) {
	jwks, err := c.fetch()
	if err != nil {
		return nil, fmt.Errorf("failed to fetch JWKS: %w", err)
	}
	if key := findKey(jwks, kid); key != nil {
		return convertJWKToPublicKey(key)
	}

	if !validKidPattern.MatchString(kid) {
		return nil, fmt.Errorf("unable to find key with kid (invalid format)")
	}

	slog.Warn("kid not found in JWKS, attempting refresh", slog.String("kid", kid), slog.String("jwks_url", c.url))
	refreshed, err := c.refresh()
	if err != nil {
		return nil, fmt.Errorf("failed to refresh JWKS: %w", err)
	}
	if key := findKey(refreshed, kid); key != nil {
		return convertJWKToPublicKey(key)
	}
	return nil, fmt.Errorf("unable to find key with kid after JWKS refresh")
}

func (c *JWKSCache) fetch() (*JWKS, error) {
	c.mu.RLock()
	if c.keys != nil && time.Since(c.fetchedAt) < jwksCacheTTL {
		cached := c.keys
		c.mu.RUnlock()
		return cached, nil
	}
	c.mu.RUnlock()

	result, err, _ := c.refreshGroup.Do("fetch", func() (any, error) {
		c.mu.RLock()
		if c.keys != nil && time.Since(c.fetchedAt) < jwksCacheTTL {
			cached := c.keys
			c.mu.RUnlock()
			return cached, nil
		}
		c.mu.RUnlock()

		jwks, err := c.doFetch()
		if err != nil {
			return nil, err
		}

		c.mu.Lock()
		c.keys = jwks
		c.fetchedAt = time.Now()
		c.mu.Unlock()
		return jwks, nil
	})
	if err != nil {
		return nil, err
	}
	return result.(*JWKS), nil
}

func (c *JWKSCache) refresh() (*JWKS, error) {
	c.mu.RLock()
	if c.keys != nil && !c.lastRefreshAt.IsZero() && time.Since(c.lastRefreshAt) < c.minRefreshInterval {
		cached := c.keys
		c.mu.RUnlock()
		return cached, nil
	}
	c.mu.RUnlock()

	result, err, _ := c.refreshGroup.Do("refresh", func() (any, error) {
		c.mu.RLock()
		if c.keys != nil && !c.lastRefreshAt.IsZero() && time.Since(c.lastRefreshAt) < c.minRefreshInterval {
			cached := c.keys
			c.mu.RUnlock()
			return cached, nil
		}
		c.mu.RUnlock()

		jwks, err := c.doFetch()
		now := time.Now()
		if err != nil {
			c.mu.Lock()
			c.lastRefreshAt = now
			c.mu.Unlock()
			return nil, err
		}

		c.mu.Lock()
		c.keys = jwks
		c.fetchedAt = now
		c.lastRefreshAt = now
		c.mu.Unlock()
		return jwks, nil
	})
	if err != nil {
		return nil, err
	}
	return result.(*JWKS), nil
}

func (c *JWKSCache) doFetch() (*JWKS, error) {
	resp, err := c.httpClient.Get(c.url)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch JWKS: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("JWKS endpoint returned status: %d", resp.StatusCode)
	}

	var jwks JWKS
	if err := json.NewDecoder(resp.Body).Decode(&jwks); err != nil {
		return nil, fmt.Errorf("failed to decode JWKS: %w", err)
	}
	return &jwks, nil
}

func findKey(jwks *JWKS, kid string) *JSONWebKey {
	for i := range jwks.Keys {
		if jwks.Keys[i].Kid == kid {
			return &jwks.Keys[i]
		}
	}
	return nil
}

func convertJWKToPublicKey(jwk *JSONWebKey) (*rsa.PublicKey, error) {
	nBytes, err := base64.RawURLEncoding.DecodeString(jwk.N)
	if err != nil {
		return nil, fmt.Errorf("failed to decode modulus: %w", err)
	}
	eBytes, err := base64.RawURLEncoding.DecodeString(jwk.E)
	if err != nil {
		return nil, fmt.Errorf("failed to decode exponent: %w", err)
	}
	n := new(big.Int).SetBytes(nBytes)
	var e int
	for _, b := range eBytes {
		e = e<<8 + int(b)
	}
	return &rsa.PublicKey{N: n, E: e}, nil
}
