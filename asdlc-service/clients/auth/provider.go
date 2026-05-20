// Package auth provides an OAuth2 client_credentials token provider for
// service-to-service authentication. The optional hostHeader field is
// needed because OpenChoreo's local k3d setup routes by HTTP Host header
// and the Thunder token endpoint sits behind that gateway.
package auth

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// expiryBuffer is the slack we leave before a token's real expiry. Any token
// that would expire within this window is treated as already expired so
// callers never present a token that could die mid-request.
const expiryBuffer = 30 * time.Second

// Config configures one AuthProvider instance.
type Config struct {
	// TokenURL is the OAuth2 token endpoint.
	TokenURL string
	// ClientID is the OAuth2 client identifier registered with the IDP.
	ClientID string
	// ClientSecret is the OAuth2 client secret.
	ClientSecret string
	// HostHeader, if non-empty, is set on the HTTP request's Host field.
	// Used by OpenChoreo's k3d gateway which routes by Host header.
	HostHeader string
	// HTTPClient overrides the default http.Client. Optional.
	HTTPClient *http.Client
}

// AuthProvider fetches and caches OAuth2 client_credentials tokens. Safe for
// concurrent use; refreshes are double-checked-locked so a flood of callers
// at expiry produces exactly one token request.
type AuthProvider struct {
	config     Config
	httpClient *http.Client

	mu          sync.RWMutex
	accessToken string
	expiresAt   time.Time
}

// NewAuthProvider returns a new provider with the given configuration.
func NewAuthProvider(cfg Config) *AuthProvider {
	hc := cfg.HTTPClient
	if hc == nil {
		hc = &http.Client{Timeout: 30 * time.Second}
	}
	return &AuthProvider{config: cfg, httpClient: hc}
}

// GetToken returns a valid access token, fetching a new one if needed.
func (p *AuthProvider) GetToken(ctx context.Context) (string, error) {
	// Fast path under a read lock.
	p.mu.RLock()
	if p.isTokenValidLocked() {
		token := p.accessToken
		p.mu.RUnlock()
		return token, nil
	}
	p.mu.RUnlock()

	// Slow path: acquire write lock and re-check before fetching.
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.isTokenValidLocked() {
		return p.accessToken, nil
	}

	slog.Debug("auth: fetching new token", slog.String("token_url", p.config.TokenURL))
	token, expiresIn, err := p.fetchToken(ctx)
	if err != nil {
		return "", fmt.Errorf("fetch token: %w", err)
	}
	p.accessToken = token
	p.expiresAt = time.Now().Add(time.Duration(expiresIn) * time.Second)
	slog.Info("auth: fetched new access token",
		slog.String("expires_at", p.expiresAt.Format(time.RFC3339)))
	return p.accessToken, nil
}

// Invalidate clears the cached token. Callers should invalidate when a
// downstream service rejects the current token with 401 so the next call
// fetches a fresh one.
func (p *AuthProvider) Invalidate() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.accessToken = ""
	p.expiresAt = time.Time{}
}

func (p *AuthProvider) isTokenValidLocked() bool {
	if p.accessToken == "" {
		return false
	}
	return time.Now().Add(expiryBuffer).Before(p.expiresAt)
}

type tokenResponse struct {
	AccessToken string `json:"access_token"`
	TokenType   string `json:"token_type"`
	ExpiresIn   int64  `json:"expires_in"`
	Scope       string `json:"scope,omitempty"`
}

func (p *AuthProvider) fetchToken(ctx context.Context) (string, int64, error) {
	form := url.Values{
		"grant_type":    {"client_credentials"},
		"client_id":     {p.config.ClientID},
		"client_secret": {p.config.ClientSecret},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.config.TokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return "", 0, fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if p.config.HostHeader != "" {
		req.Host = p.config.HostHeader
	}

	resp, err := p.httpClient.Do(req)
	if err != nil {
		return "", 0, fmt.Errorf("token request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return "", 0, fmt.Errorf("token endpoint returned %d: %s", resp.StatusCode, string(body))
	}

	var tr tokenResponse
	if err := json.Unmarshal(body, &tr); err != nil {
		return "", 0, fmt.Errorf("parse token response: %w", err)
	}
	if tr.AccessToken == "" {
		return "", 0, fmt.Errorf("empty access_token in response")
	}
	if tr.ExpiresIn <= 0 {
		return "", 0, fmt.Errorf("invalid expires_in: %d", tr.ExpiresIn)
	}
	return tr.AccessToken, tr.ExpiresIn, nil
}
