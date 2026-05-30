package oauth

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// TokenProvider fetches and caches OAuth2 client_credentials tokens.
type TokenProvider struct {
	tokenURL     string
	clientID     string
	clientSecret string
	hostHeader   string

	mu        sync.Mutex
	token     string
	expiresAt time.Time
}

func NewTokenProvider(tokenURL, clientID, clientSecret, hostHeader string) *TokenProvider {
	return &TokenProvider{
		tokenURL:     tokenURL,
		clientID:     clientID,
		clientSecret: clientSecret,
		hostHeader:   hostHeader,
	}
}

// Token returns a valid access token, refreshing if expired.
func (p *TokenProvider) Token() (string, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	// Return cached token if still valid (with 60s buffer)
	if p.token != "" && time.Now().Add(60*time.Second).Before(p.expiresAt) {
		return p.token, nil
	}
	return p.fetchLocked()
}

// Invalidate clears the cached token so the next Token() call fetches fresh.
// Callers should use this when a downstream service rejects the token with 401.
func (p *TokenProvider) Invalidate() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.token = ""
	p.expiresAt = time.Time{}
}

// fetchLocked retrieves a new access token. Caller must hold p.mu.
func (p *TokenProvider) fetchLocked() (string, error) {
	data := url.Values{
		"grant_type":    {"client_credentials"},
		"client_id":     {p.clientID},
		"client_secret": {p.clientSecret},
	}

	req, err := http.NewRequest(http.MethodPost, p.tokenURL, strings.NewReader(data.Encode()))
	if err != nil {
		return "", fmt.Errorf("create token request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if p.hostHeader != "" {
		req.Host = p.hostHeader
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("token request: %w", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("token endpoint returned %d: %s", resp.StatusCode, string(body))
	}

	var tokenResp struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int    `json:"expires_in"`
	}
	if err := json.Unmarshal(body, &tokenResp); err != nil {
		return "", fmt.Errorf("parse token response: %w", err)
	}

	p.token = tokenResp.AccessToken
	p.expiresAt = time.Now().Add(time.Duration(tokenResp.ExpiresIn) * time.Second)

	return p.token, nil
}
