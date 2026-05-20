package jwtassertion

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestBuildBearerChallenge(t *testing.T) {
	tests := []struct {
		name                string
		resourceMetadataURL string
		errorCode           string
		want                string
	}{
		{
			name: "realm only",
			want: `Bearer realm="asdlc"`,
		},
		{
			name:      "with error code",
			errorCode: "invalid_token",
			want:      `Bearer realm="asdlc", error="invalid_token"`,
		},
		{
			name:                "with resource metadata URL",
			resourceMetadataURL: "https://asdlc.example.com/.well-known/oauth-protected-resource",
			want:                `Bearer realm="asdlc", resource_metadata="https://asdlc.example.com/.well-known/oauth-protected-resource"`,
		},
		{
			name:                "with error and resource metadata URL",
			resourceMetadataURL: "https://asdlc.example.com/.well-known/oauth-protected-resource",
			errorCode:           "invalid_token",
			want:                `Bearer realm="asdlc", error="invalid_token", resource_metadata="https://asdlc.example.com/.well-known/oauth-protected-resource"`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := buildBearerChallenge(tt.resourceMetadataURL, tt.errorCode)
			if got != tt.want {
				t.Errorf("buildBearerChallenge() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestAuthenticator_MissingHeader(t *testing.T) {
	metadataURL := "https://asdlc.example.com/.well-known/oauth-protected-resource"
	mw := Authenticator(Config{
		AllowedIssuers:      []string{"thunder"},
		AllowedAudiences:    []string{"asdlc-bff"},
		ResourceMetadataURL: metadataURL,
		IsLocalDevEnv:       true,
	})
	handler := mw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("handler should not be called when Authorization header is missing")
	}))

	req := httptest.NewRequest(http.MethodGet, "/api/v1/projects", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", rec.Code)
	}
	want := `Bearer realm="asdlc", resource_metadata="https://asdlc.example.com/.well-known/oauth-protected-resource"`
	if got := rec.Header().Get("WWW-Authenticate"); got != want {
		t.Errorf("WWW-Authenticate = %q, want %q", got, want)
	}
}

func TestAuthenticator_InvalidJWT(t *testing.T) {
	metadataURL := "https://asdlc.example.com/.well-known/oauth-protected-resource"
	mw := Authenticator(Config{
		AllowedIssuers:      []string{"thunder"},
		AllowedAudiences:    []string{"asdlc-bff"},
		ResourceMetadataURL: metadataURL,
		IsLocalDevEnv:       true,
	})
	handler := mw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("handler should not be called for invalid JWT")
	}))

	req := httptest.NewRequest(http.MethodGet, "/api/v1/projects", nil)
	req.Header.Set("Authorization", "Bearer invalid.jwt.token")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", rec.Code)
	}
	want := `Bearer realm="asdlc", error="invalid_token", resource_metadata="https://asdlc.example.com/.well-known/oauth-protected-resource"`
	if got := rec.Header().Get("WWW-Authenticate"); got != want {
		t.Errorf("WWW-Authenticate = %q, want %q", got, want)
	}
}

func TestValidateAudience(t *testing.T) {
	tests := []struct {
		name         string
		audiences    []string
		allowed      []string
		shouldAccept bool
	}{
		{
			name:         "exact match",
			audiences:    []string{"asdlc-bff"},
			allowed:      []string{"asdlc-bff"},
			shouldAccept: true,
		},
		{
			name:         "no match",
			audiences:    []string{"someone-else"},
			allowed:      []string{"asdlc-bff"},
			shouldAccept: false,
		},
		{
			name:         "prefix match",
			audiences:    []string{"asdlc-bff-v2"},
			allowed:      []string{"asdlc-bff*"},
			shouldAccept: true,
		},
		{
			name:         "multiple audiences first matches",
			audiences:    []string{"asdlc-bff", "other"},
			allowed:      []string{"asdlc-bff"},
			shouldAccept: true,
		},
		{
			name:         "rejects bare wildcard",
			audiences:    []string{"anything"},
			allowed:      []string{"*"},
			shouldAccept: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateAudience(tt.audiences, tt.allowed)
			if tt.shouldAccept && err != nil {
				t.Errorf("expected accept, got error: %v", err)
			}
			if !tt.shouldAccept && err == nil {
				t.Error("expected rejection, got nil error")
			}
		})
	}
}

func TestValidateIssuer(t *testing.T) {
	if err := validateIssuer("thunder", []string{"thunder"}); err != nil {
		t.Errorf("expected accept, got %v", err)
	}
	if err := validateIssuer("thunder", []string{"other"}); err == nil {
		t.Error("expected rejection")
	}
	if err := validateIssuer("thunder", nil); err == nil {
		t.Error("expected error when no allowed issuers configured")
	}
}
