// Package thundersvc is the BFF-side Thunder admin client. It mints
// `scope=system` access tokens via client_credentials against a Thunder
// OAuth app (asdlc-system-client) that has the Administrator role
// assigned, then uses those tokens to manage per-org publisher OAuth
// apps via Thunder's /applications endpoint.
//
// Per docs/design/api-platform-integration.md §6 Phase 3 — Phase 3 of
// the api-platform-integration plan. Mirrors agent-manager's
// agent-manager-service/clients/thundersvc/client.go end-to-end; the
// only differences are:
//
//   - App naming convention: `asdlc-publisher-<orgHandle>` (instead of
//     `amp-publisher-<orgName>`).
//   - We don't carry an OU UUID argument — every BFF caller passes the
//     OC org handle and we look up the matching Thunder OU once at
//     first call (cached on the client).
//
// The system token has a TTL (Thunder default ~1h); the cache uses a
// 30 s skew so concurrent callers don't all hit the slow path right
// before expiry. singleflight deduplicates the slow path itself.
package thundersvc

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"golang.org/x/sync/singleflight"
)

// Client is the public surface of the Thunder admin client. All methods
// are idempotent — repeating an EnsurePublisherApp call returns the
// existing client_id without creating a duplicate; repeating Delete
// returns false when the app is gone.
type Client interface {
	// EnsurePublisherApp creates an OAuth2 app named
	// "asdlc-publisher-{orgHandle}" if it doesn't already exist.
	// Returns the clientId and (on creation only) the clientSecret —
	// Thunder doesn't expose the secret on subsequent reads, so callers
	// MUST persist it to OpenBao on the `created=true` branch. When
	// `created=false`, clientSecret is empty — the caller should look
	// it up in their secret store. When the secret was lost (e.g.
	// OpenBao was wiped), use RegenerateClientSecret to issue a new
	// one.
	EnsurePublisherApp(ctx context.Context, orgHandle string) (clientID, clientSecret string, created bool, err error)

	// DeletePublisherApp deletes the publisher app for the given org.
	// Returns true when the app existed and was deleted, false when it
	// didn't exist (idempotent — both states are success).
	DeletePublisherApp(ctx context.Context, orgHandle string) (bool, error)

	// RegenerateClientSecret issues a fresh client_secret for the
	// existing publisher app. Returns the new secret. The caller MUST
	// rotate it into OpenBao + redeploy any consumer pods that mounted
	// the old value.
	RegenerateClientSecret(ctx context.Context, orgHandle string) (string, error)

	// EnsureRedirectURIs appends the given URIs to the named OAuth2
	// app's `inboundAuthConfig[0].config.redirectUris` set, idempotent
	// on each URI (already-present entries are skipped). Used by the
	// BFF when a web-app component with `auth.kind: oidc-spa` lands
	// `deployed` — its public URL is unknown until then, but Thunder
	// rejects /oauth2/authorize requests whose redirect_uri isn't on
	// the registered list. `clientID` is the OAuth client_id string
	// (e.g. "asdlc-console-client") — NOT the application's UUID;
	// findApp resolves it. Returns true when at least one URI was
	// added, false when every URI was already present (a no-op).
	EnsureRedirectURIs(ctx context.Context, clientID string, uris []string) (added bool, err error)
}

// Config bundles the construction params. Mirrors the agent-manager
// signature for readability — three-positional-args constructors are
// annoying once the list grows past two.
type Config struct {
	BaseURL      string // e.g. http://thunder.openchoreo.localhost:8080
	ClientID     string // OAuth2 client id of the system app
	ClientSecret string // OAuth2 client secret of the system app
	// HTTPClient — optional override (tests inject a recording client).
	// Defaults to a 30 s-timeout net/http client.
	HTTPClient *http.Client
}

type client struct {
	baseURL    string
	systemID   string
	systemSec  string
	httpClient *http.Client

	mu          sync.RWMutex
	cachedToken string
	tokenExpiry time.Time
	tokenSfg    singleflight.Group

	// Default OU id, looked up once on first EnsurePublisherApp call
	// and cached. Thunder's UI nests every org under a root OU
	// ("default") so for v1 we always use that.
	muOU      sync.Mutex
	defaultOU string
}

// New builds a Thunder admin client.
func New(cfg Config) Client {
	hc := cfg.HTTPClient
	if hc == nil {
		hc = &http.Client{Timeout: 30 * time.Second}
	}
	return &client{
		baseURL:    strings.TrimRight(cfg.BaseURL, "/"),
		systemID:   cfg.ClientID,
		systemSec:  cfg.ClientSecret,
		httpClient: hc,
	}
}

// PublisherAppName is the canonical naming function — exposed for the
// idp_service so tests can assert names without re-deriving the prefix.
func PublisherAppName(orgHandle string) string {
	return "asdlc-publisher-" + orgHandle
}

// -- system token --------------------------------------------------------

// getSystemToken returns a cached system token or fetches a new one.
// Fast path: RLock + cache hit. Slow path: singleflight dedupe so
// concurrent callers share one round-trip.
func (c *client) getSystemToken(ctx context.Context) (string, error) {
	c.mu.RLock()
	if c.cachedToken != "" && time.Now().Before(c.tokenExpiry) {
		token := c.cachedToken
		c.mu.RUnlock()
		return token, nil
	}
	c.mu.RUnlock()

	result, err, _ := c.tokenSfg.Do("system-token", func() (any, error) {
		c.mu.RLock()
		if c.cachedToken != "" && time.Now().Before(c.tokenExpiry) {
			token := c.cachedToken
			c.mu.RUnlock()
			return token, nil
		}
		c.mu.RUnlock()

		token, expiresIn, err := c.fetchSystemToken(ctx)
		if err != nil {
			return nil, err
		}
		c.mu.Lock()
		c.cachedToken = token
		const skew = 30
		if expiresIn > skew {
			c.tokenExpiry = time.Now().Add(time.Duration(expiresIn-skew) * time.Second)
		} else if expiresIn > 0 {
			c.tokenExpiry = time.Now().Add(time.Duration(expiresIn) * time.Second)
		} else {
			c.tokenExpiry = time.Now().Add(time.Minute)
		}
		c.mu.Unlock()
		return token, nil
	})
	if err != nil {
		return "", err
	}
	return result.(string), nil
}

func (c *client) fetchSystemToken(ctx context.Context) (string, int, error) {
	// Thunder's confidential apps are registered with
	// `tokenEndpointAuthMethod: client_secret_post` (per the bootstrap
	// script in values-thunder.yaml's `ensure_confidential_app`), so we
	// place client_id + client_secret in the form body — NOT in a
	// Basic auth header.
	data := url.Values{
		"grant_type":    {"client_credentials"},
		"scope":         {"system"},
		"client_id":     {c.systemID},
		"client_secret": {c.systemSec},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/oauth2/token", strings.NewReader(data.Encode()))
	if err != nil {
		return "", 0, fmt.Errorf("thunder token request build: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", 0, fmt.Errorf("thunder token request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return "", 0, fmt.Errorf("thunder token endpoint returned %d: %s", resp.StatusCode, string(body))
	}

	var result struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int    `json:"expires_in"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", 0, fmt.Errorf("thunder token decode: %w", err)
	}
	if result.AccessToken == "" {
		return "", 0, fmt.Errorf("thunder returned empty access_token")
	}
	return result.AccessToken, result.ExpiresIn, nil
}

// -- OU resolution --------------------------------------------------------

// getDefaultOUID returns Thunder's default organisation-unit id,
// cached after first successful lookup.
func (c *client) getDefaultOUID(ctx context.Context, token string) (string, error) {
	c.muOU.Lock()
	defer c.muOU.Unlock()
	if c.defaultOU != "" {
		return c.defaultOU, nil
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/organization-units/tree/default", nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+token)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("thunder get default OU: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("thunder get default OU returned %d: %s", resp.StatusCode, string(body))
	}

	var ou struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&ou); err != nil {
		return "", fmt.Errorf("thunder OU decode: %w", err)
	}
	if ou.ID == "" {
		return "", fmt.Errorf("thunder default OU has no id")
	}
	c.defaultOU = ou.ID
	return ou.ID, nil
}

// -- EnsurePublisherApp ---------------------------------------------------

func (c *client) EnsurePublisherApp(ctx context.Context, orgHandle string) (string, string, bool, error) {
	if orgHandle == "" {
		return "", "", false, fmt.Errorf("orgHandle required")
	}
	token, err := c.getSystemToken(ctx)
	if err != nil {
		return "", "", false, fmt.Errorf("getSystemToken: %w", err)
	}
	appName := PublisherAppName(orgHandle)

	_, existingClientID, err := c.findApp(ctx, token, appName)
	if err != nil {
		return "", "", false, err
	}
	if existingClientID != "" {
		// Already exists. Thunder doesn't expose secrets on read so
		// the caller must already have it in OpenBao; we return only
		// the clientId + created=false.
		return existingClientID, "", false, nil
	}

	ouID, err := c.getDefaultOUID(ctx, token)
	if err != nil {
		return "", "", false, err
	}

	id, secret, err := c.createApp(ctx, token, appName, ouID)
	if err != nil {
		return "", "", false, err
	}
	return id, secret, true, nil
}

func (c *client) DeletePublisherApp(ctx context.Context, orgHandle string) (bool, error) {
	if orgHandle == "" {
		return false, fmt.Errorf("orgHandle required")
	}
	token, err := c.getSystemToken(ctx)
	if err != nil {
		return false, fmt.Errorf("getSystemToken: %w", err)
	}
	appName := PublisherAppName(orgHandle)
	internalID, _, err := c.findApp(ctx, token, appName)
	if err != nil {
		return false, err
	}
	if internalID == "" {
		return false, nil
	}
	return c.deleteApp(ctx, token, internalID)
}

func (c *client) RegenerateClientSecret(ctx context.Context, orgHandle string) (string, error) {
	if orgHandle == "" {
		return "", fmt.Errorf("orgHandle required")
	}
	token, err := c.getSystemToken(ctx)
	if err != nil {
		return "", fmt.Errorf("getSystemToken: %w", err)
	}
	appName := PublisherAppName(orgHandle)
	internalID, _, err := c.findApp(ctx, token, appName)
	if err != nil {
		return "", err
	}
	if internalID == "" {
		return "", fmt.Errorf("thunder app %s not found, cannot regenerate secret", appName)
	}
	return c.regenerateSecret(ctx, token, internalID)
}

// -- EnsureRedirectURIs ---------------------------------------------------

func (c *client) EnsureRedirectURIs(ctx context.Context, clientID string, uris []string) (bool, error) {
	if clientID == "" {
		return false, fmt.Errorf("EnsureRedirectURIs: clientID required")
	}
	if len(uris) == 0 {
		return false, nil
	}
	token, err := c.getSystemToken(ctx)
	if err != nil {
		return false, fmt.Errorf("getSystemToken: %w", err)
	}
	internalID, err := c.findAppByClientID(ctx, token, clientID)
	if err != nil {
		return false, err
	}
	if internalID == "" {
		return false, fmt.Errorf("thunder app with clientId=%q not found", clientID)
	}
	app, err := c.getAppByID(ctx, token, internalID)
	if err != nil {
		return false, err
	}
	// Navigate inboundAuthConfig[0].config.redirectUris.
	cfg, err := inboundOAuthConfig(app)
	if err != nil {
		return false, fmt.Errorf("clientId=%q: %w", clientID, err)
	}
	existingAny, _ := cfg["redirectUris"].([]any)
	seen := make(map[string]struct{}, len(existingAny))
	updated := make([]any, 0, len(existingAny)+len(uris))
	for _, v := range existingAny {
		if s, ok := v.(string); ok && s != "" {
			if _, dup := seen[s]; !dup {
				seen[s] = struct{}{}
				updated = append(updated, s)
			}
		}
	}
	added := false
	for _, u := range uris {
		if u == "" {
			continue
		}
		if _, dup := seen[u]; dup {
			continue
		}
		seen[u] = struct{}{}
		updated = append(updated, u)
		added = true
	}
	if !added {
		return false, nil
	}
	cfg["redirectUris"] = updated
	if err := c.putAppByID(ctx, token, internalID, app); err != nil {
		return false, err
	}
	return true, nil
}

// findAppByClientID scans the /applications listing for the app whose
// inbound OAuth clientId matches. listAppsPage's thunderApp struct
// already surfaces the OAuth clientId at the top level (Thunder 0.34
// includes it in the list view), so we can match without fetching each
// app individually.
func (c *client) findAppByClientID(ctx context.Context, token, clientID string) (string, error) {
	const pageSize = 100
	const maxPages = 100
	for page := 0; page < maxPages; page++ {
		offset := page * pageSize
		apps, err := c.listAppsPage(ctx, token, offset, pageSize)
		if err != nil {
			return "", err
		}
		for _, app := range apps {
			if app.ClientID == clientID {
				return app.ID, nil
			}
		}
		if len(apps) < pageSize {
			return "", nil
		}
	}
	return "", fmt.Errorf("thunder list apps exceeded %d pages looking for clientId=%s", maxPages, clientID)
}

func (c *client) getAppByID(ctx context.Context, token, appID string) (map[string]any, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/applications/"+appID, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("thunder get app: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("thunder get app returned %d: %s", resp.StatusCode, string(body))
	}
	var app map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&app); err != nil {
		return nil, fmt.Errorf("thunder get app decode: %w", err)
	}
	return app, nil
}

func (c *client) putAppByID(ctx context.Context, token, appID string, app map[string]any) error {
	body, err := json.Marshal(app)
	if err != nil {
		return fmt.Errorf("thunder put app marshal: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, c.baseURL+"/applications/"+appID, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("thunder put app: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusNoContent {
		rb, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("thunder put app returned %d: %s", resp.StatusCode, string(rb))
	}
	return nil
}

// inboundOAuthConfig returns the OAuth2 inbound auth config map from a
// Thunder application body, with mutable access. Returns an error if the
// app has no inboundAuthConfig of type "oauth2" — we don't synthesize one
// because that would change the app's auth shape, which is a separate
// operation.
func inboundOAuthConfig(app map[string]any) (map[string]any, error) {
	listAny, ok := app["inboundAuthConfig"].([]any)
	if !ok || len(listAny) == 0 {
		return nil, fmt.Errorf("inboundAuthConfig missing or empty")
	}
	for _, entryAny := range listAny {
		entry, ok := entryAny.(map[string]any)
		if !ok {
			continue
		}
		if t, _ := entry["type"].(string); t != "oauth2" {
			continue
		}
		cfg, ok := entry["config"].(map[string]any)
		if !ok {
			return nil, fmt.Errorf("inboundAuthConfig[oauth2].config not an object")
		}
		return cfg, nil
	}
	return nil, fmt.Errorf("no inboundAuthConfig entry with type=oauth2")
}

// -- low-level HTTP -------------------------------------------------------

type thunderApp struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	ClientID string `json:"clientId"`
}

func (c *client) findApp(ctx context.Context, token, appName string) (internalID, clientID string, err error) {
	const pageSize = 100
	const maxPages = 100
	for page := 0; page < maxPages; page++ {
		offset := page * pageSize
		apps, perr := c.listAppsPage(ctx, token, offset, pageSize)
		if perr != nil {
			return "", "", perr
		}
		for _, app := range apps {
			if app.Name == appName {
				return app.ID, app.ClientID, nil
			}
		}
		if len(apps) < pageSize {
			return "", "", nil
		}
	}
	return "", "", fmt.Errorf("thunder list apps exceeded %d pages looking for %s", maxPages, appName)
}

func (c *client) listAppsPage(ctx context.Context, token string, offset, limit int) ([]thunderApp, error) {
	reqURL := fmt.Sprintf("%s/applications?offset=%d&limit=%d", c.baseURL, offset, limit)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("thunder list apps: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("thunder list apps returned %d: %s", resp.StatusCode, string(body))
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("thunder list apps read body: %w", err)
	}

	// Thunder can return either a bare array or a wrapped object.
	var apps []thunderApp
	if jerr := json.Unmarshal(body, &apps); jerr != nil {
		var wrapped struct {
			Applications []thunderApp `json:"applications"`
		}
		if werr := json.Unmarshal(body, &wrapped); werr != nil {
			return nil, fmt.Errorf("thunder list apps decode: %w", jerr)
		}
		apps = wrapped.Applications
	}
	return apps, nil
}

func (c *client) deleteApp(ctx context.Context, token, appID string) (bool, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, c.baseURL+"/applications/"+appID, nil)
	if err != nil {
		return false, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return false, fmt.Errorf("thunder delete app: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	switch resp.StatusCode {
	case http.StatusNotFound:
		return false, nil
	case http.StatusOK, http.StatusNoContent:
		return true, nil
	}
	body, _ := io.ReadAll(resp.Body)
	return false, fmt.Errorf("thunder delete app returned %d: %s", resp.StatusCode, string(body))
}

func (c *client) createApp(ctx context.Context, token, appName, ouID string) (string, string, error) {
	payload := map[string]any{
		"name": appName,
		"ouId": ouID,
		"inboundAuthConfig": []map[string]any{
			{
				"type": "oauth2",
				"config": map[string]any{
					"clientId":                appName,
					"grantTypes":              []string{"client_credentials"},
					"tokenEndpointAuthMethod": "client_secret_basic",
				},
			},
		},
	}
	body, _ := json.Marshal(payload)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/applications", bytes.NewReader(body))
	if err != nil {
		return "", "", err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", "", fmt.Errorf("thunder create app: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	respBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		return "", "", fmt.Errorf("thunder create app returned %d: %s", resp.StatusCode, string(respBody))
	}
	slog.Info("Thunder publisher app created", "appName", appName, "status", resp.StatusCode)

	var result struct {
		ClientID     string `json:"clientId"`
		ClientSecret string `json:"clientSecret"`
		InboundAuth  []struct {
			Config struct {
				ClientID     string `json:"clientId"`
				ClientSecret string `json:"clientSecret"`
			} `json:"config"`
		} `json:"inboundAuthConfig"`
	}
	if err := json.Unmarshal(respBody, &result); err != nil {
		return "", "", fmt.Errorf("thunder create app decode: %w", err)
	}
	cid := result.ClientID
	cs := result.ClientSecret
	if len(result.InboundAuth) > 0 {
		if cid == "" {
			cid = result.InboundAuth[0].Config.ClientID
		}
		if cs == "" {
			cs = result.InboundAuth[0].Config.ClientSecret
		}
	}
	if cid == "" {
		return "", "", fmt.Errorf("thunder create app: clientId not found in response: %s", string(respBody))
	}
	return cid, cs, nil
}

func (c *client) regenerateSecret(ctx context.Context, token, appID string) (string, error) {
	getReq, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/applications/"+appID, nil)
	if err != nil {
		return "", err
	}
	getReq.Header.Set("Authorization", "Bearer "+token)
	getResp, err := c.httpClient.Do(getReq)
	if err != nil {
		return "", fmt.Errorf("thunder get app for secret regeneration: %w", err)
	}
	defer func() { _ = getResp.Body.Close() }()
	getBody, _ := io.ReadAll(getResp.Body)
	if getResp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("thunder get app returned %d: %s", getResp.StatusCode, string(getBody))
	}

	var app map[string]any
	if err := json.Unmarshal(getBody, &app); err != nil {
		return "", fmt.Errorf("thunder get app decode: %w", err)
	}
	newSecret, err := generateRandomSecret()
	if err != nil {
		return "", fmt.Errorf("generate client secret: %w", err)
	}
	if err := setInboundClientSecret(app, newSecret); err != nil {
		return "", fmt.Errorf("set client secret in app payload: %w", err)
	}
	delete(app, "id")

	putBody, _ := json.Marshal(app)
	putReq, err := http.NewRequestWithContext(ctx, http.MethodPut, c.baseURL+"/applications/"+appID, bytes.NewReader(putBody))
	if err != nil {
		return "", err
	}
	putReq.Header.Set("Authorization", "Bearer "+token)
	putReq.Header.Set("Content-Type", "application/json")

	putResp, err := c.httpClient.Do(putReq)
	if err != nil {
		return "", fmt.Errorf("thunder put app for secret regeneration: %w", err)
	}
	defer func() { _ = putResp.Body.Close() }()
	putRespBody, _ := io.ReadAll(putResp.Body)
	if putResp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("thunder put app returned %d: %s", putResp.StatusCode, string(putRespBody))
	}

	var out struct {
		InboundAuth []struct {
			Config struct {
				ClientSecret string `json:"clientSecret"`
			} `json:"config"`
		} `json:"inboundAuthConfig"`
	}
	if err := json.Unmarshal(putRespBody, &out); err != nil {
		return "", fmt.Errorf("thunder put app response decode: %w", err)
	}
	if len(out.InboundAuth) == 0 || out.InboundAuth[0].Config.ClientSecret == "" {
		return "", fmt.Errorf("thunder put app response missing clientSecret")
	}
	slog.Info("Thunder client secret regenerated", "appID", appID)
	return out.InboundAuth[0].Config.ClientSecret, nil
}

func setInboundClientSecret(app map[string]any, secret string) error {
	inbound, ok := app["inboundAuthConfig"].([]any)
	if !ok || len(inbound) == 0 {
		return fmt.Errorf("inboundAuthConfig missing or empty")
	}
	entry, ok := inbound[0].(map[string]any)
	if !ok {
		return fmt.Errorf("inboundAuthConfig[0] is not an object")
	}
	cfg, ok := entry["config"].(map[string]any)
	if !ok {
		return fmt.Errorf("inboundAuthConfig[0].config is not an object")
	}
	cfg["clientSecret"] = secret
	return nil
}

func generateRandomSecret() (string, error) {
	b := make([]byte, 48)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.URLEncoding.WithPadding(base64.NoPadding).EncodeToString(b), nil
}
