package auth

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
)

// fakeIDP is a minimal client_credentials token endpoint for tests.
type fakeIDP struct {
	calls   atomic.Int32
	token   string
	expires int64
	server  *httptest.Server
}

func newFakeIDP(token string, expiresIn int64) *fakeIDP {
	idp := &fakeIDP{token: token, expires: expiresIn}
	idp.server = httptest.NewServer(http.HandlerFunc(idp.handle))
	return idp
}

func (f *fakeIDP) handle(w http.ResponseWriter, r *http.Request) {
	f.calls.Add(1)
	if err := r.ParseForm(); err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	if r.Form.Get("grant_type") != "client_credentials" {
		http.Error(w, "wrong grant_type", 400)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Write([]byte(`{"access_token":"` + f.token + `","token_type":"Bearer","expires_in":` + itoa(f.expires) + `}`))
}

func itoa(n int64) string {
	// strconv import is overkill for tests
	if n == 0 {
		return "0"
	}
	negative := n < 0
	if negative {
		n = -n
	}
	digits := []byte{}
	for n > 0 {
		digits = append([]byte{byte('0' + n%10)}, digits...)
		n /= 10
	}
	if negative {
		return "-" + string(digits)
	}
	return string(digits)
}

func TestAuthProvider_FetchesAndCaches(t *testing.T) {
	idp := newFakeIDP("token-1", 3600)
	defer idp.server.Close()

	p := NewAuthProvider(Config{
		TokenURL:     idp.server.URL,
		ClientID:     "id",
		ClientSecret: "secret",
	})

	tok, err := p.GetToken(context.Background())
	if err != nil {
		t.Fatalf("GetToken: %v", err)
	}
	if tok != "token-1" {
		t.Errorf("token = %q, want token-1", tok)
	}

	// Second call should hit cache, not the IDP.
	tok2, err := p.GetToken(context.Background())
	if err != nil {
		t.Fatalf("GetToken (cached): %v", err)
	}
	if tok2 != "token-1" {
		t.Errorf("cached token = %q, want token-1", tok2)
	}
	if got := idp.calls.Load(); got != 1 {
		t.Errorf("IDP hit %d times, want 1 (cache miss)", got)
	}
}

func TestAuthProvider_InvalidateForcesRefetch(t *testing.T) {
	idp := newFakeIDP("token-1", 3600)
	defer idp.server.Close()

	p := NewAuthProvider(Config{
		TokenURL:     idp.server.URL,
		ClientID:     "id",
		ClientSecret: "secret",
	})

	if _, err := p.GetToken(context.Background()); err != nil {
		t.Fatalf("GetToken: %v", err)
	}
	p.Invalidate()
	if _, err := p.GetToken(context.Background()); err != nil {
		t.Fatalf("GetToken after invalidate: %v", err)
	}
	if got := idp.calls.Load(); got != 2 {
		t.Errorf("IDP hit %d times, want 2 (invalidate should trigger refetch)", got)
	}
}

func TestAuthProvider_PropagatesHostHeader(t *testing.T) {
	var seenHost atomic.Pointer[string]
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host := r.Host
		seenHost.Store(&host)
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"access_token":"x","token_type":"Bearer","expires_in":3600}`))
	}))
	defer srv.Close()

	p := NewAuthProvider(Config{
		TokenURL:     srv.URL,
		ClientID:     "id",
		ClientSecret: "secret",
		HostHeader:   "thunder.openchoreo.localhost:8080",
	})
	if _, err := p.GetToken(context.Background()); err != nil {
		t.Fatalf("GetToken: %v", err)
	}
	got := seenHost.Load()
	if got == nil || *got != "thunder.openchoreo.localhost:8080" {
		t.Errorf("Host header = %v, want thunder.openchoreo.localhost:8080", got)
	}
}

func TestAuthProvider_RejectsNon200(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "bad creds", http.StatusUnauthorized)
	}))
	defer srv.Close()

	p := NewAuthProvider(Config{
		TokenURL:     srv.URL,
		ClientID:     "id",
		ClientSecret: "secret",
	})
	_, err := p.GetToken(context.Background())
	if err == nil {
		t.Fatal("expected error from 401 token endpoint, got nil")
	}
	if !strings.Contains(err.Error(), "401") {
		t.Errorf("error should mention status 401, got %v", err)
	}
}

// TestAuthProvider_FormShape proves the body matches the standard
// application/x-www-form-urlencoded shape Thunder expects.
func TestAuthProvider_FormShape(t *testing.T) {
	var capturedBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		buf := make([]byte, 1024)
		n, _ := r.Body.Read(buf)
		capturedBody = string(buf[:n])
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"access_token":"x","token_type":"Bearer","expires_in":3600}`))
	}))
	defer srv.Close()

	p := NewAuthProvider(Config{
		TokenURL:     srv.URL,
		ClientID:     "test-id",
		ClientSecret: "test-secret",
	})
	if _, err := p.GetToken(context.Background()); err != nil {
		t.Fatalf("GetToken: %v", err)
	}

	form, err := url.ParseQuery(capturedBody)
	if err != nil {
		t.Fatalf("body not form-encoded: %v (body=%q)", err, capturedBody)
	}
	if form.Get("grant_type") != "client_credentials" {
		t.Errorf("grant_type = %q, want client_credentials", form.Get("grant_type"))
	}
	if form.Get("client_id") != "test-id" {
		t.Errorf("client_id = %q, want test-id", form.Get("client_id"))
	}
	if form.Get("client_secret") != "test-secret" {
		t.Errorf("client_secret = %q, want test-secret", form.Get("client_secret"))
	}
}
