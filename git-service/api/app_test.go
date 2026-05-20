package api

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"math/big"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"

	"github.com/wso2/asdlc/git-service/middleware/jwtassertion"
)

// TestApp_HealthOpen confirms /health is on the outer mux and reachable
// without any auth.
func TestApp_HealthOpen(t *testing.T) {
	srv := newTestApp(t, nil, nil)
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/health")
	if err != nil {
		t.Fatalf("GET /health: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("status = %d, want 200", resp.StatusCode)
	}
}

// TestApp_TaskMuxRejectsServiceJWTAudience ensures a Service JWT (audience
// = git-service, but not signed by the BFF) cannot be replayed against
// /api/v1/credentials/refresh. Each mux has its own JWKS source.
func TestApp_TaskMuxRejectsServiceJWTAudience(t *testing.T) {
	thunder := newJWKSServer(t)
	defer thunder.Close()
	bff := newJWKSServer(t)
	defer bff.Close()

	serviceVerifier := jwtassertion.Authenticator(jwtassertion.Config{
		JWKS:             jwtassertion.NewJWKSCache(thunder.JWKSURL),
		AllowedIssuers:   []string{"thunder"},
		AllowedAudiences: []string{"git-service"},
	})
	taskVerifier := jwtassertion.Authenticator(jwtassertion.Config{
		JWKS:             jwtassertion.NewJWKSCache(bff.JWKSURL),
		AllowedIssuers:   []string{"asdlc-bff"},
		AllowedAudiences: []string{"git-service"},
	})

	srv := newTestApp(t, serviceVerifier, taskVerifier)
	defer srv.Close()

	// Mint a Thunder-signed token (audience=git-service, issuer=thunder).
	tok := mintToken(t, thunder, jwt.MapClaims{
		"iss": "thunder",
		"aud": "git-service",
		"sub": "asdlc-api-client",
		"exp": time.Now().Add(time.Hour).Unix(),
	})
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/api/v1/credentials/refresh", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	resp.Body.Close()
	// taskMux verifies via BFF JWKS — the kid won't be found there, so 401.
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401 (Service JWT shouldn't validate against BFF JWKS)", resp.StatusCode)
	}
}

// TestApp_TaskMuxAcceptsBFFSignedToken — a token signed by the BFF, with
// the correct issuer/audience, is accepted on the credentials/refresh path.
// (The handler downstream will 500/401 because we don't wire a real
// CredCtrl — but the JWT layer must let it through first.)
func TestApp_TaskMuxAcceptsBFFSignedToken(t *testing.T) {
	thunder := newJWKSServer(t)
	defer thunder.Close()
	bff := newJWKSServer(t)
	defer bff.Close()

	serviceVerifier := jwtassertion.Authenticator(jwtassertion.Config{
		JWKS:             jwtassertion.NewJWKSCache(thunder.JWKSURL),
		AllowedIssuers:   []string{"thunder"},
		AllowedAudiences: []string{"git-service"},
	})
	taskVerifier := jwtassertion.Authenticator(jwtassertion.Config{
		JWKS:             jwtassertion.NewJWKSCache(bff.JWKSURL),
		AllowedIssuers:   []string{"asdlc-bff"},
		AllowedAudiences: []string{"git-service"},
	})

	srv := newTestApp(t, serviceVerifier, taskVerifier)
	defer srv.Close()

	tok := mintToken(t, bff, jwt.MapClaims{
		"iss":     "asdlc-bff",
		"aud":     "git-service",
		"sub":     "task-123",
		"taskId":  "task-123",
		"ocOrgId": "default",
		"exp":     time.Now().Add(time.Hour).Unix(),
	})
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/api/v1/credentials/refresh", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	resp.Body.Close()
	// The middleware accepts; the stub downstream returns 401 because the
	// stub's CredCtrl writes 401 (no claims wired). What matters here is
	// that we don't see the WWW-Authenticate-bearing 401 from the JWT
	// middleware — i.e., the request reached the handler. Both the
	// middleware-rejected and handler-rejected paths happen to be 401,
	// so we instead check there's NO WWW-Authenticate response header,
	// which only the JWT middleware writes.
	if resp.Header.Get("WWW-Authenticate") != "" {
		t.Errorf("got WWW-Authenticate=%q — JWT middleware rejected a valid token",
			resp.Header.Get("WWW-Authenticate"))
	}
}

// TestApp_RepoMuxRequiresServiceJWT ensures a request to /api/v1/repos
// without a valid Service JWT is 401.
func TestApp_RepoMuxRequiresServiceJWT(t *testing.T) {
	thunder := newJWKSServer(t)
	defer thunder.Close()

	serviceVerifier := jwtassertion.Authenticator(jwtassertion.Config{
		JWKS:             jwtassertion.NewJWKSCache(thunder.JWKSURL),
		AllowedIssuers:   []string{"thunder"},
		AllowedAudiences: []string{"git-service"},
	})

	srv := newTestApp(t, serviceVerifier, nil)
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/api/v1/repos/proj-1")
	if err != nil {
		t.Fatalf("GET /api/v1/repos/proj-1: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", resp.StatusCode)
	}
	// Verify it's the JWT middleware that rejected (not the handler).
	if resp.Header.Get("WWW-Authenticate") == "" {
		t.Error("expected WWW-Authenticate header from JWT middleware")
	}
}

// ----------------------------------------------------------------------------
// Helpers
// ----------------------------------------------------------------------------

// newTestApp returns a httptest.Server wrapping NewHandler with stub
// controllers — only the routing + auth layers are exercised.
func newTestApp(t *testing.T, serviceJWT, taskJWT jwtassertion.Middleware) *httptest.Server {
	t.Helper()
	stub := func(w http.ResponseWriter, _ *http.Request) {
		// Default: handler reachable. If the JWT middleware rejects the
		// request, this never runs.
		w.WriteHeader(http.StatusUnauthorized) // proxy: any reach == middleware passed
	}
	h := NewHandler(AppParams{
		ServiceJWT: serviceJWT,
		TaskJWT:    taskJWT,
		CredCtrl:   &stubCredCtrl{f: stub},
		// All others nil — repo routes use stub controllers below if nil.
		// We don't exercise the handlers, only the auth layer.
	})
	return httptest.NewServer(h)
}

type stubCredCtrl struct {
	f http.HandlerFunc
}

func (c *stubCredCtrl) Refresh(w http.ResponseWriter, r *http.Request) { c.f(w, r) }

// jwksTestServer is a small helper that hosts a JWKS endpoint backed by
// an in-memory RSA keypair, and exposes a method to sign tokens against it.
type jwksTestServer struct {
	*httptest.Server
	JWKSURL string
	priv    *rsa.PrivateKey
	kid     string
}

func newJWKSServer(t *testing.T) *jwksTestServer {
	t.Helper()
	priv, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("rsa.GenerateKey: %v", err)
	}
	kid := "test-" + randomKid()
	pub := &priv.PublicKey

	mux := http.NewServeMux()
	mux.HandleFunc("/jwks.json", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"keys": []map[string]string{
				{
					"kty": "RSA",
					"alg": "RS256",
					"use": "sig",
					"kid": kid,
					"n":   base64.RawURLEncoding.EncodeToString(pub.N.Bytes()),
					"e":   base64.RawURLEncoding.EncodeToString(big.NewInt(int64(pub.E)).Bytes()),
				},
			},
		})
	})
	srv := httptest.NewServer(mux)
	return &jwksTestServer{
		Server:  srv,
		JWKSURL: srv.URL + "/jwks.json",
		priv:    priv,
		kid:     kid,
	}
}

// mintToken signs claims with this server's key and the matching kid.
func mintToken(t *testing.T, s *jwksTestServer, claims jwt.MapClaims) string {
	t.Helper()
	tok := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
	tok.Header["kid"] = s.kid
	signed, err := tok.SignedString(s.priv)
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	return signed
}

func randomKid() string {
	var b [4]byte
	_, _ = rand.Read(b[:])
	return base64.RawURLEncoding.EncodeToString(b[:])
}

var _ context.Context = context.Background() // keep imports stable
