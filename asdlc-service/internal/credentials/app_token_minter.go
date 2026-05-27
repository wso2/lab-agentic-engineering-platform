package credentials

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"golang.org/x/sync/singleflight"
)

// ErrAppNotConfigured is returned by AppTokenMinter methods when no App
// private key is loaded — either because OpenBao has nothing at the
// _platform path (Phase 2 PR A: dev seed is user-PAT only, App config lands
// in PR B) or because the loader was skipped at startup. Callers must
// handle this gracefully — the resolver should never reach the minter
// unless there's an active app-installation row.
var ErrAppNotConfigured = errors.New("app credentials: not configured")

// AppTokenMinter owns the GitHub App's RSA private key (the only consumer
// of it in the codebase) and mints installation access tokens on demand.
//
// One instance per git-service process. Concurrent mints for the same
// installation are deduplicated via singleflight; tokens are cached
// per-installation with a 5-minute safety margin against the
// GitHub-supplied expires_at.
type AppTokenMinter struct {
	appID      int64
	privateKey *rsa.PrivateKey
	cache      *appTokenCache
	flight     singleflight.Group
	httpClient *http.Client

	// botIdentity is the App's bot identity from GET /app, populated lazily
	// on first successful mint. PR A leaves this empty (no app-installation
	// rows exist); PR B's connect flow populates it as part of App-mode
	// connect.
	botIdentity Identity
	identityMu  sync.RWMutex

	// bao is set via WithOpenBao after construction. PR B post-startup
	// platform reads (webhook secret list) go through this. The
	// import-fence test enforces this is one of the only platformPath
	// touchpoints.
	bao OpenBaoStore
}

// AppKeyMaterial holds the raw bytes loaded from OpenBao's _platform path.
// AppID is the App's numeric ID (also stored in OpenBao); private key is
// the PEM-encoded RSA key issued by GitHub.
type AppKeyMaterial struct {
	AppID         int64
	PrivateKeyPEM []byte
}

// NewAppTokenMinter constructs the minter. material may be nil — in that
// case all mint calls return ErrAppNotConfigured. This is the PR A shape:
// the resolver still constructs the minter so the wiring is identical,
// but no app-installation rows exist so it's never called.
func NewAppTokenMinter(material *AppKeyMaterial) (*AppTokenMinter, error) {
	m := &AppTokenMinter{
		cache:      newAppTokenCache(),
		httpClient: &http.Client{Timeout: 15 * time.Second},
	}
	if material == nil {
		return m, nil
	}
	key, err := parseRSAPrivateKey(material.PrivateKeyPEM)
	if err != nil {
		return nil, fmt.Errorf("app minter: parse key: %w", err)
	}
	m.appID = material.AppID
	m.privateKey = key
	return m, nil
}

// MintForInstallation returns a fresh-or-cached installation access token.
// Returns ErrAppNotConfigured if the minter has no App private key.
//
// Cache hit if remaining TTL > 5 min. Otherwise singleflight-collapsed to
// one mint per (process, installation, deadline-window).
func (m *AppTokenMinter) MintForInstallation(ctx context.Context, installationID int64) (string, time.Time, error) {
	if m.privateKey == nil {
		return "", time.Time{}, ErrAppNotConfigured
	}

	if entry, ok := m.cache.get(installationID); ok {
		return entry.token, entry.expiresAt, nil
	}

	key := fmt.Sprintf("%d", installationID)
	v, err, _ := m.flight.Do(key, func() (interface{}, error) {
		// Re-check the cache inside the singleflight in case another
		// goroutine already minted.
		if entry, ok := m.cache.get(installationID); ok {
			return entry, nil
		}
		entry, err := m.mintInstallationToken(ctx, installationID)
		if err != nil {
			return nil, err
		}
		m.cache.put(installationID, entry)
		return entry, nil
	})
	if err != nil {
		return "", time.Time{}, err
	}
	entry := v.(appTokenEntry)
	return entry.token, entry.expiresAt, nil
}

// EvictInstallation forces the next mint for an installation to bypass
// the cache. Used by the 401-on-cached-token recovery path.
func (m *AppTokenMinter) EvictInstallation(installationID int64) {
	m.cache.evict(installationID)
}

// AppID returns the configured App ID, or 0 if not configured.
func (m *AppTokenMinter) AppID() int64 { return m.appID }

// BotIdentity returns the App's bot identity. Empty if not yet populated
// (App not connected, or first connect flow hasn't completed).
func (m *AppTokenMinter) BotIdentity() Identity {
	m.identityMu.RLock()
	defer m.identityMu.RUnlock()
	return m.botIdentity
}

// SetBotIdentity caches the App's bot identity (Name/Email/Login from
// GET /app). Called by the App-key startup loader on first reach.
func (m *AppTokenMinter) SetBotIdentity(id Identity) {
	m.identityMu.Lock()
	defer m.identityMu.Unlock()
	m.botIdentity = id
}

// SignAppJWT builds an RS256 App JWT with iss=appID. iat is back-dated 60s
// for clock skew tolerance; exp is 9 minutes in the future per GitHub's
// recommendation ("no more than 9 minutes" — the strict 10-minute ceiling
// rejects any JWT where validation-time clock is even slightly ahead of
// signing-time clock). Exported so the connect path can call /app and
// /app/installations/{id} during App-mode connect; that path needs the
// JWT but doesn't have an installation_id yet.
func (m *AppTokenMinter) SignAppJWT(now time.Time) (string, error) {
	return m.signAppJWT(now)
}

// signAppJWT is the internal implementation. Kept private so the only
// public surface is SignAppJWT — call sites reaching for it stand out
// during code review.
func (m *AppTokenMinter) signAppJWT(now time.Time) (string, error) {
	if m.privateKey == nil || m.appID == 0 {
		return "", ErrAppNotConfigured
	}
	header := map[string]string{"alg": "RS256", "typ": "JWT"}
	headerJSON, _ := json.Marshal(header)
	headerPart := base64.RawURLEncoding.EncodeToString(headerJSON)

	claims := map[string]interface{}{
		"iat": now.Add(-60 * time.Second).Unix(),
		"exp": now.Add(9 * time.Minute).Unix(),
		"iss": m.appID,
	}
	claimsJSON, _ := json.Marshal(claims)
	claimsPart := base64.RawURLEncoding.EncodeToString(claimsJSON)

	signingInput := headerPart + "." + claimsPart
	hashed := sha256.Sum256([]byte(signingInput))
	sig, err := rsa.SignPKCS1v15(rand.Reader, m.privateKey, crypto.SHA256, hashed[:])
	if err != nil {
		return "", fmt.Errorf("sign: %w", err)
	}
	return signingInput + "." + base64.RawURLEncoding.EncodeToString(sig), nil
}

// mintInstallationToken POSTs to /app/installations/{id}/access_tokens.
// Caller is responsible for caching.
func (m *AppTokenMinter) mintInstallationToken(ctx context.Context, installationID int64) (appTokenEntry, error) {
	jwt, err := m.signAppJWT(time.Now())
	if err != nil {
		return appTokenEntry{}, err
	}

	url := fmt.Sprintf("https://api.github.com/app/installations/%d/access_tokens", installationID)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, nil)
	if err != nil {
		return appTokenEntry{}, err
	}
	req.Header.Set("Authorization", "Bearer "+jwt)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")

	resp, err := m.httpClient.Do(req)
	if err != nil {
		return appTokenEntry{}, fmt.Errorf("mint: http: %w", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return appTokenEntry{}, fmt.Errorf("mint: status %d: %s", resp.StatusCode, truncateBody(body))
	}

	var out struct {
		Token     string    `json:"token"`
		ExpiresAt time.Time `json:"expires_at"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return appTokenEntry{}, fmt.Errorf("mint: decode: %w", err)
	}
	if out.Token == "" {
		return appTokenEntry{}, errors.New("mint: empty token in response")
	}
	return appTokenEntry{token: out.Token, expiresAt: out.ExpiresAt}, nil
}

// parseRSAPrivateKey accepts both PKCS#1 and PKCS#8 PEM forms (GitHub
// distributes PKCS#1 by default).
func parseRSAPrivateKey(pemBytes []byte) (*rsa.PrivateKey, error) {
	block, _ := pem.Decode(pemBytes)
	if block == nil {
		return nil, errors.New("no PEM block found")
	}
	if k, err := x509.ParsePKCS1PrivateKey(block.Bytes); err == nil {
		return k, nil
	}
	if k, err := x509.ParsePKCS8PrivateKey(block.Bytes); err == nil {
		if rk, ok := k.(*rsa.PrivateKey); ok {
			return rk, nil
		}
		return nil, errors.New("PKCS8 key is not an RSA key")
	}
	return nil, errors.New("unsupported PEM key format")
}

// truncateBody bounds error-message body so we don't dump multi-MB
// responses into logs. 200 chars is enough for GitHub's structured errors.
func truncateBody(body []byte) string {
	s := string(body)
	if len(s) > 200 {
		s = s[:200] + "…"
	}
	return strings.ReplaceAll(s, "\n", " ")
}

// LoadAppWebhookSecrets reads the App-wide webhook secret list from
// secret/asdlc/_platform/github/app/webhook_secret. Stored as a JSON list
// of {secret, added_at} entries (phase2.md §7.6 rotation shape).
//
// Returned as raw byte secrets, current-first, suitable for HMAC compare.
// Returns an empty slice with nil error if no row exists yet (the App may
// be configured but webhooks not yet enabled — fresh-deploy state).
//
// One of three platformPath callers — the others are LoadAppKeyFromOpenBao
// and LoadAppBotIdentity. The import-fence test gates this list.
func (m *AppTokenMinter) LoadAppWebhookSecrets(ctx context.Context) ([][]byte, error) {
	if m.bao == nil {
		return nil, nil
	}
	raw, err := readPlatformValue(ctx, m.bao, "github/app/webhook_secret")
	if err != nil {
		return nil, err
	}
	if len(raw) == 0 {
		return nil, nil
	}
	// Try JSON list first, fall back to raw single-secret string.
	var entries []struct {
		Secret  string    `json:"secret"`
		AddedAt time.Time `json:"added_at"`
	}
	if err := json.Unmarshal(raw, &entries); err == nil && len(entries) > 0 {
		out := make([][]byte, 0, len(entries))
		for _, e := range entries {
			if e.Secret != "" {
				out = append(out, []byte(e.Secret))
			}
		}
		return out, nil
	}
	// Single-secret legacy/fallback shape.
	return [][]byte{raw}, nil
}

// LoadAppClientSecret reads the App's OAuth client_secret from
// secret/asdlc/_platform/github/app/client_secret. PR D-followup §6.4 —
// consumed by CredentialService.BindAppInstallation to exchange OAuth
// codes for user tokens during the bind path.
//
// Returns "" + nil error if no row exists yet (deployment didn't seed it).
// Callers gate the bind path on a non-empty value.
//
// One of the platformPath callers — gated by the import fence.
func (m *AppTokenMinter) LoadAppClientSecret(ctx context.Context) (string, error) {
	if m.bao == nil {
		return "", nil
	}
	raw, err := readPlatformValue(ctx, m.bao, "github/app/client_secret")
	if err != nil {
		return "", err
	}
	return string(raw), nil
}

// LoadAppBotIdentity is invoked once at startup, after the private key has
// been parsed, to fetch GET /app and cache the bot identity on the minter.
// Best-effort — failure leaves botIdentity empty and the connect path
// re-tries lazily on first App-mode connect.
//
// One of three platformPath callers (transitively, via SignAppJWT).
func (m *AppTokenMinter) LoadAppBotIdentity(ctx context.Context, githubAPI string) error {
	if m.privateKey == nil {
		return ErrAppNotConfigured
	}
	jwt, err := m.signAppJWT(time.Now())
	if err != nil {
		return err
	}
	if githubAPI == "" {
		githubAPI = "https://api.github.com"
	}
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, githubAPI+"/app", nil)
	req.Header.Set("Authorization", "Bearer "+jwt)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	resp, err := m.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 {
		return fmt.Errorf("GET /app: %d %s", resp.StatusCode, truncateBody(body))
	}
	var info struct {
		Slug string `json:"slug"`
		Name string `json:"name"`
	}
	if err := json.Unmarshal(body, &info); err != nil {
		return err
	}
	if info.Slug == "" {
		return errors.New("/app: empty slug")
	}
	m.SetBotIdentity(Identity{
		Name:  info.Name,
		Email: fmt.Sprintf("%s[bot]@users.noreply.github.com", info.Slug),
		Login: fmt.Sprintf("%s[bot]", info.Slug),
	})
	return nil
}

// readPlatformValue is the single helper that gates platformPath access
// for the post-startup readers (webhook-secret list, future per-feature
// platform reads). The AppTokenMinter's Bao reference is set at
// construction time via WithOpenBao; the minter holds the only reference
// outside the import fence (which the import_fence_test enforces).
func readPlatformValue(ctx context.Context, store OpenBaoStore, key string) ([]byte, error) {
	s, ok := store.(*openBaoStore)
	if !ok {
		return nil, nil
	}
	resp, err := s.client.Logical().ReadWithContext(ctx, s.platformPath(key))
	if err != nil {
		return nil, fmt.Errorf("openbao platform read: %w", err)
	}
	if resp == nil || resp.Data == nil {
		return nil, nil
	}
	dataField, ok := resp.Data["data"].(map[string]interface{})
	if !ok || dataField == nil {
		return nil, nil
	}
	val, ok := dataField["value"].(string)
	if !ok {
		return nil, nil
	}
	return []byte(val), nil
}

// WithOpenBao caches the OpenBaoStore on the minter so post-startup
// reads (webhook-secret list, etc.) can reach _platform/* without the
// CredentialService importing the SDK directly.
func (m *AppTokenMinter) WithOpenBao(store OpenBaoStore) {
	m.bao = store
}

// LoadAppKeyFromOpenBao reads the App's appID + private-key from the
// _platform namespace at startup. The only call site referencing
// platformPath. Returns nil + nil if no App is configured (PR A: that's
// expected; PR B's operator runbook seeds these).
//
// IMPORTANT: this is one of three callers of platformPath. The other two
// are LoadAppWebhookSecrets and LoadAppBotIdentity (transitively via
// SignAppJWT). The import_fence_test.go enforces this.
func LoadAppKeyFromOpenBao(ctx context.Context, store OpenBaoStore) (*AppKeyMaterial, error) {
	s, ok := store.(*openBaoStore)
	if !ok {
		// Non-real store (placeholder) — same as "no App configured".
		return nil, nil
	}
	pemBytes, err := s.client.Logical().ReadWithContext(ctx, s.platformPath("github/app/private_key"))
	if err != nil {
		return nil, fmt.Errorf("load app key: %w", err)
	}
	if pemBytes == nil || pemBytes.Data == nil {
		return nil, nil // no App configured
	}
	dataField, ok := pemBytes.Data["data"].(map[string]interface{})
	if !ok {
		return nil, nil
	}
	pem, _ := dataField["value"].(string)
	if pem == "" {
		return nil, nil
	}

	idResp, err := s.client.Logical().ReadWithContext(ctx, s.platformPath("github/app/app_id"))
	if err != nil {
		return nil, fmt.Errorf("load app id: %w", err)
	}
	if idResp == nil || idResp.Data == nil {
		return nil, errors.New("app key present but app_id missing")
	}
	idData, _ := idResp.Data["data"].(map[string]interface{})
	idStr, _ := idData["value"].(string)
	var appID int64
	if _, err := fmt.Sscanf(idStr, "%d", &appID); err != nil || appID == 0 {
		return nil, fmt.Errorf("invalid app_id: %q", idStr)
	}
	return &AppKeyMaterial{AppID: appID, PrivateKeyPEM: []byte(pem)}, nil
}
