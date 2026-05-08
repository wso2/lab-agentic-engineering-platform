package api

import (
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/wso2/asdlc/asdlc-service/config"
	"github.com/wso2/asdlc/asdlc-service/controllers"
	"github.com/wso2/asdlc/asdlc-service/services"
)

// TestJWKSRoute_PublishesActiveKey verifies the BFF's JWKS endpoint is
// reachable on the OUTER mux (no JWT required) and serves the active
// signing key in the standard JWK shape verifiers expect.
func TestJWKSRoute_PublishesActiveKey(t *testing.T) {
	priv := mustGenerateRSAKey(t)
	pemKey := string(encodePKCS1(t, priv))

	mgr, err := services.NewTaskTokenManager(services.TaskTokenConfig{
		PrivateKey: pemKey,
		Issuer:     "test-iss",
		Audience:   "test-aud",
		TTL:        time.Hour,
	})
	if err != nil {
		t.Fatalf("NewTaskTokenManager: %v", err)
	}

	handler := NewHandler(AppParams{
		Config:         config.Config{TestMode: false},
		JWKSController: controllers.NewJWKSController(mgr),
	})

	srv := httptest.NewServer(handler)
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/auth/external/jwks.json")
	if err != nil {
		t.Fatalf("GET jwks: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}

	var body services.JWKSResponse
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if len(body.Keys) != 1 {
		t.Fatalf("len(keys) = %d, want 1", len(body.Keys))
	}
	k := body.Keys[0]
	if k.Kty != "RSA" || k.Alg != "RS256" || k.Use != "sig" {
		t.Errorf("unexpected JWK shape: %+v", k)
	}
	if k.Kid == "" || k.N == "" || k.E == "" {
		t.Errorf("JWK missing kid/n/e: %+v", k)
	}
	if k.Kid != mgr.KeyID() {
		t.Errorf("kid = %q, want %q (manager's key id)", k.Kid, mgr.KeyID())
	}
}

// TestJWKSRoute_NotGatedByJWT confirms the JWKS endpoint isn't behind the
// /api/* JWT middleware. A verifier that hadn't yet fetched the JWKS would
// otherwise deadlock against itself.
func TestJWKSRoute_NotGatedByJWT(t *testing.T) {
	priv := mustGenerateRSAKey(t)
	pemKey := string(encodePKCS1(t, priv))

	mgr, err := services.NewTaskTokenManager(services.TaskTokenConfig{
		PrivateKey: pemKey,
		Issuer:     "iss",
		Audience:   "aud",
		TTL:        time.Hour,
	})
	if err != nil {
		t.Fatalf("NewTaskTokenManager: %v", err)
	}

	// Build a handler with NO JWKS configured for inbound auth — the
	// middleware would reject every /api/* request. The JWKS route must
	// still respond.
	handler := NewHandler(AppParams{
		Config:         config.Config{TestMode: false},
		JWKSController: controllers.NewJWKSController(mgr),
		ThunderJWKS:    nil, // no inbound auth wired
	})

	srv := httptest.NewServer(handler)
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/auth/external/jwks.json")
	if err != nil {
		t.Fatalf("GET jwks: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
}

func mustGenerateRSAKey(t *testing.T) *rsa.PrivateKey {
	t.Helper()
	priv, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("rsa.GenerateKey: %v", err)
	}
	return priv
}
