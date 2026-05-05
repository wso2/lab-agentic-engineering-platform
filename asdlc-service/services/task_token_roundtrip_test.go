package services

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/wso2/asdlc/asdlc-service/middleware/jwtassertion"
)

// TestTaskTokenRoundtrip exercises the full chain from token issuance to
// validation: sign with TaskTokenManager → publish JWKS → fetch via
// JWKSCache → verify through Authenticator. This is the integration test
// that none of the per-component unit tests cover.
func TestTaskTokenRoundtrip(t *testing.T) {
	dir := t.TempDir()
	keyPath, _ := writeTestKey(t, dir, "pkcs1")
	mgr, err := NewTaskTokenManager(TaskTokenConfig{
		PrivateKeyPath: keyPath,
		Issuer:         "asdlc-bff",
		Audience:       "git-service",
		TTL:            10 * time.Minute,
	})
	if err != nil {
		t.Fatalf("NewTaskTokenManager: %v", err)
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(mgr.JWKS())
	}))
	defer srv.Close()

	cache := jwtassertion.NewJWKSCache(srv.URL)
	auth := jwtassertion.Authenticator(jwtassertion.Config{
		JWKS:             cache,
		AllowedIssuers:   []string{"asdlc-bff"},
		AllowedAudiences: []string{"git-service"},
	})

	called := false
	handler := auth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		claims := jwtassertion.GetTokenClaims(r.Context())
		if claims == nil {
			t.Error("GetTokenClaims returned nil — middleware did not attach claims")
			return
		}
		if claims.TaskID != "task-abc" {
			t.Errorf("taskId claim = %q, want task-abc", claims.TaskID)
		}
		if claims.OcOrgID != "org-xyz" {
			t.Errorf("ocOrgId claim = %q, want org-xyz", claims.OcOrgID)
		}
		w.WriteHeader(http.StatusOK)
	}))

	tok, err := mgr.Issue("task-abc", "org-xyz", "proj-1")
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/v1/credentials/refresh", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d (body: %s)", rec.Code, rec.Body.String())
	}
	if !called {
		t.Error("downstream handler not called")
	}
}

// TestTaskTokenRoundtrip_KidRotation verifies that when the BFF rotates its
// signing key, a verifier holding a stale JWKS cache will hit the kid-miss
// refresh path and pick up the new key without restart.
func TestTaskTokenRoundtrip_KidRotation(t *testing.T) {
	dir := t.TempDir()

	// First key — initially serves JWKS.
	keyPath1, _ := writeTestKey(t, dir, "pkcs1")
	mgr1, err := NewTaskTokenManager(TaskTokenConfig{
		PrivateKeyPath: keyPath1, Issuer: "asdlc-bff", Audience: "git-service", TTL: 10 * time.Minute,
	})
	if err != nil {
		t.Fatalf("mgr1: %v", err)
	}

	// active is what the JWKS endpoint serves at the moment of fetch.
	var active atomic.Pointer[TaskTokenManager]
	active.Store(mgr1)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(active.Load().JWKS())
	}))
	defer srv.Close()

	cache := jwtassertion.NewJWKSCache(srv.URL)
	auth := jwtassertion.Authenticator(jwtassertion.Config{
		JWKS:             cache,
		AllowedIssuers:   []string{"asdlc-bff"},
		AllowedAudiences: []string{"git-service"},
	})
	handler := auth(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	// Warm the cache with mgr1's JWKS via a successful verify.
	tok1, _ := mgr1.Issue("t1", "o1", "")
	rec1 := httptest.NewRecorder()
	req1 := httptest.NewRequest(http.MethodGet, "/x", nil)
	req1.Header.Set("Authorization", "Bearer "+tok1)
	handler.ServeHTTP(rec1, req1)
	if rec1.Code != http.StatusOK {
		t.Fatalf("warmup: expected 200, got %d", rec1.Code)
	}

	// Rotate: a fresh key takes over. Cache still holds mgr1's keys; new
	// token's kid is unknown to the cached JWKS, which must trigger refresh.
	keyPath2, _ := writeTestKey(t, dir, "pkcs1")
	mgr2, err := NewTaskTokenManager(TaskTokenConfig{
		PrivateKeyPath: keyPath2, Issuer: "asdlc-bff", Audience: "git-service", TTL: 10 * time.Minute,
	})
	if err != nil {
		t.Fatalf("mgr2: %v", err)
	}
	if mgr2.KeyID() == mgr1.KeyID() {
		t.Fatal("rotation produced the same kid — test invariant violated")
	}
	active.Store(mgr2)

	tok2, _ := mgr2.Issue("t2", "o2", "")
	rec2 := httptest.NewRecorder()
	req2 := httptest.NewRequest(http.MethodGet, "/x", nil)
	req2.Header.Set("Authorization", "Bearer "+tok2)
	handler.ServeHTTP(rec2, req2)
	if rec2.Code != http.StatusOK {
		t.Fatalf("post-rotation: expected 200, got %d (body: %s)", rec2.Code, rec2.Body.String())
	}
}

// TestTaskTokenRoundtrip_WrongAudience confirms a token signed for one
// audience is rejected when the verifier expects a different one.
func TestTaskTokenRoundtrip_WrongAudience(t *testing.T) {
	dir := t.TempDir()
	keyPath, _ := writeTestKey(t, dir, "pkcs1")
	mgr, _ := NewTaskTokenManager(TaskTokenConfig{
		PrivateKeyPath: keyPath, Issuer: "asdlc-bff", Audience: "git-service", TTL: 10 * time.Minute,
	})

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(mgr.JWKS())
	}))
	defer srv.Close()

	cache := jwtassertion.NewJWKSCache(srv.URL)
	auth := jwtassertion.Authenticator(jwtassertion.Config{
		JWKS:             cache,
		AllowedIssuers:   []string{"asdlc-bff"},
		AllowedAudiences: []string{"some-other-service"},
	})
	handler := auth(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		t.Error("handler must not run for wrong-audience token")
	}))

	tok, _ := mgr.Issue("t", "o", "")
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/x", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", rec.Code)
	}
}

// TestTaskTokenRoundtrip_ExpiredToken confirms tokens past their exp are
// rejected. Uses a 1ms TTL and a small sleep — the manager constructor
// requires positive TTL, so we can't pre-bake a negative exp.
func TestTaskTokenRoundtrip_ExpiredToken(t *testing.T) {
	dir := t.TempDir()
	keyPath, _ := writeTestKey(t, dir, "pkcs1")
	mgr, err := NewTaskTokenManager(TaskTokenConfig{
		PrivateKeyPath: keyPath, Issuer: "asdlc-bff", Audience: "git-service", TTL: 1 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("NewTaskTokenManager: %v", err)
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(mgr.JWKS())
	}))
	defer srv.Close()

	cache := jwtassertion.NewJWKSCache(srv.URL)
	auth := jwtassertion.Authenticator(jwtassertion.Config{
		JWKS:             cache,
		AllowedIssuers:   []string{"asdlc-bff"},
		AllowedAudiences: []string{"git-service"},
	})
	handler := auth(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		t.Error("handler must not run for expired token")
	}))

	tok, _ := mgr.Issue("t", "o", "")
	time.Sleep(10 * time.Millisecond) // outlive the 1ms TTL

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/x", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 for expired token, got %d", rec.Code)
	}
}
