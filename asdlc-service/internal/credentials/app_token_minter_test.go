package credentials

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func generateTestKey(t *testing.T) (*rsa.PrivateKey, []byte) {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("gen key: %v", err)
	}
	pemBytes := pem.EncodeToMemory(&pem.Block{
		Type:  "RSA PRIVATE KEY",
		Bytes: x509.MarshalPKCS1PrivateKey(key),
	})
	return key, pemBytes
}

func TestAppTokenMinter_NoMaterial_ReturnsErrAppNotConfigured(t *testing.T) {
	m, err := NewAppTokenMinter(nil)
	if err != nil {
		t.Fatalf("NewAppTokenMinter(nil): %v", err)
	}
	_, _, err = m.MintForInstallation(context.Background(), 12345)
	if !errors.Is(err, ErrAppNotConfigured) {
		t.Errorf("MintForInstallation = %v; want ErrAppNotConfigured", err)
	}
}

func TestAppTokenMinter_SignAppJWT_Shape(t *testing.T) {
	_, pemBytes := generateTestKey(t)
	m, err := NewAppTokenMinter(&AppKeyMaterial{AppID: 42, PrivateKeyPEM: pemBytes})
	if err != nil {
		t.Fatalf("NewAppTokenMinter: %v", err)
	}
	jwt, err := m.signAppJWT(time.Unix(1000000, 0))
	if err != nil {
		t.Fatalf("signAppJWT: %v", err)
	}
	parts := strings.Split(jwt, ".")
	if len(parts) != 3 {
		t.Fatalf("expected 3 JWT parts, got %d", len(parts))
	}
	headerJSON, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		t.Fatalf("decode header: %v", err)
	}
	var header map[string]string
	if err := json.Unmarshal(headerJSON, &header); err != nil {
		t.Fatalf("unmarshal header: %v", err)
	}
	if header["alg"] != "RS256" {
		t.Errorf("alg = %q; want RS256", header["alg"])
	}

	claimsJSON, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		t.Fatalf("decode claims: %v", err)
	}
	var claims map[string]interface{}
	if err := json.Unmarshal(claimsJSON, &claims); err != nil {
		t.Fatalf("unmarshal claims: %v", err)
	}
	if int(claims["iss"].(float64)) != 42 {
		t.Errorf("iss = %v; want 42", claims["iss"])
	}
	// exp - iat should be ~10 minutes (we add iat skew of -60s and exp of +600s)
	exp := int64(claims["exp"].(float64))
	iat := int64(claims["iat"].(float64))
	if exp-iat < 600 || exp-iat > 700 {
		t.Errorf("exp-iat = %d; expected ~660 (10m + 60s skew)", exp-iat)
	}
}

func TestAppTokenMinter_MintAndCacheHit(t *testing.T) {
	_, pemBytes := generateTestKey(t)

	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"token":"ghs_fake_%d","expires_at":"%s"}`,
			atomic.LoadInt32(&calls),
			time.Now().Add(1*time.Hour).Format(time.RFC3339))
	}))
	defer srv.Close()

	m, err := NewAppTokenMinter(&AppKeyMaterial{AppID: 99, PrivateKeyPEM: pemBytes})
	if err != nil {
		t.Fatalf("NewAppTokenMinter: %v", err)
	}
	// Replace the GitHub URL by hijacking httpClient with a transport that
	// rewrites the URL to the test server.
	m.httpClient = &http.Client{Transport: &rewriteTransport{target: srv.URL}}

	tok1, _, err := m.MintForInstallation(context.Background(), 7)
	if err != nil {
		t.Fatalf("MintForInstallation: %v", err)
	}
	if tok1 == "" {
		t.Fatal("empty token")
	}

	tok2, _, err := m.MintForInstallation(context.Background(), 7)
	if err != nil {
		t.Fatalf("MintForInstallation cache: %v", err)
	}
	if tok2 != tok1 {
		t.Errorf("cache miss: tok2=%q tok1=%q", tok2, tok1)
	}
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Errorf("upstream calls = %d; want 1 (cache hit on second)", got)
	}
}

func TestAppTokenMinter_Singleflight(t *testing.T) {
	_, pemBytes := generateTestKey(t)

	var calls int32
	gate := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		<-gate // hold the first request to force concurrent mints to coalesce
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"token":"ghs_singleflight_%d","expires_at":"%s"}`,
			atomic.LoadInt32(&calls),
			time.Now().Add(1*time.Hour).Format(time.RFC3339))
	}))
	defer srv.Close()

	m, err := NewAppTokenMinter(&AppKeyMaterial{AppID: 77, PrivateKeyPEM: pemBytes})
	if err != nil {
		t.Fatalf("NewAppTokenMinter: %v", err)
	}
	m.httpClient = &http.Client{Transport: &rewriteTransport{target: srv.URL}}

	const N = 5
	var wg sync.WaitGroup
	results := make([]string, N)
	wg.Add(N)
	for i := 0; i < N; i++ {
		go func(idx int) {
			defer wg.Done()
			tok, _, err := m.MintForInstallation(context.Background(), 42)
			if err != nil {
				t.Errorf("mint %d: %v", idx, err)
				return
			}
			results[idx] = tok
		}(i)
	}
	// Give the goroutines a moment to enter singleflight, then unblock.
	time.Sleep(50 * time.Millisecond)
	close(gate)
	wg.Wait()

	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Errorf("upstream calls = %d; want 1 (singleflight collapses %d mints)", got, N)
	}
	for i, r := range results {
		if r != results[0] {
			t.Errorf("results[%d] = %q; want %q (all goroutines see same token)", i, r, results[0])
		}
	}
}

func TestAppTokenCache_SafetyMargin(t *testing.T) {
	c := newAppTokenCache()
	// Token expiring in 4 minutes — within safety margin → cache miss.
	c.put(1, appTokenEntry{token: "old", expiresAt: time.Now().Add(4 * time.Minute)})
	if _, ok := c.get(1); ok {
		t.Error("got cache hit for token within safety margin; want miss")
	}
	// Token expiring in 30 minutes — outside safety margin → cache hit.
	c.put(2, appTokenEntry{token: "fresh", expiresAt: time.Now().Add(30 * time.Minute)})
	if _, ok := c.get(2); !ok {
		t.Error("got cache miss for fresh token; want hit")
	}
}

func TestAppTokenCache_EvictForcesRemint(t *testing.T) {
	c := newAppTokenCache()
	c.put(1, appTokenEntry{token: "old", expiresAt: time.Now().Add(30 * time.Minute)})
	c.evict(1)
	if _, ok := c.get(1); ok {
		t.Error("post-evict cache hit; want miss")
	}
}

// rewriteTransport rewrites every request URL to the test server. Used so
// the minter's hardcoded api.github.com URL can be redirected without
// changing production code.
type rewriteTransport struct {
	target string
}

func (rt *rewriteTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	cli := &http.Client{}
	newReq := req.Clone(req.Context())
	// Replace scheme+host with target.
	u := newReq.URL
	u.Scheme = "http"
	u.Host = strings.TrimPrefix(rt.target, "http://")
	return cli.Do(newReq)
}
