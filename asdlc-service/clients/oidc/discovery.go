// Package oidc — minimal OIDC discovery helper. Phase 7 BYO-IDP. Given
// an issuer URL, fetches /.well-known/openid-configuration and pulls
// out the jwks_uri so the console picker can auto-populate it.
//
// This is the v1 scope of what the plan called the "generic OIDC DCR
// client". DCR (Dynamic Client Registration) is deferred to v2 — the
// publisher app registration for BYO-IDP requires creating an OAuth
// app in the user's IDP, which is per-IDP (Asgardeo Management API,
// Okta DCR, Keycloak API, etc.) and out of scope for v1. v1 expects
// the org admin to pre-register the App Factory publisher app in
// their IDP and feed the credentials back into PUT idp-profile.
package oidc

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// Metadata is the subset of the OIDC discovery document we care about.
// The full spec defines dozens of fields; for keymanager registration
// the BFF only needs issuer + jwks_uri.
type Metadata struct {
	Issuer  string `json:"issuer"`
	JWKSURI string `json:"jwks_uri"`
}

// DiscoverFromIssuer fetches the OIDC discovery document from
// `<issuer>/.well-known/openid-configuration` and returns the parsed
// shape. Honours the caller's context for deadline / cancellation.
//
// Validates that the response's `issuer` field matches the requested
// issuer URL — protects against discovery-doc impersonation per
// OIDC Discovery 1.0 §4.3.
func DiscoverFromIssuer(ctx context.Context, issuer string) (*Metadata, error) {
	if issuer == "" {
		return nil, fmt.Errorf("issuer required")
	}
	base := strings.TrimRight(issuer, "/")
	url := base + "/.well-known/openid-configuration"

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("oidc discovery build: %w", err)
	}
	req.Header.Set("Accept", "application/json")

	hc := &http.Client{Timeout: 10 * time.Second}
	resp, err := hc.Do(req)
	if err != nil {
		return nil, fmt.Errorf("oidc discovery fetch %s: %w", url, err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("oidc discovery %s returned %d: %s", url, resp.StatusCode, string(body))
	}

	var md Metadata
	if err := json.NewDecoder(resp.Body).Decode(&md); err != nil {
		return nil, fmt.Errorf("oidc discovery decode: %w", err)
	}
	if md.Issuer == "" {
		return nil, fmt.Errorf("oidc discovery: response missing issuer")
	}
	if strings.TrimRight(md.Issuer, "/") != base {
		return nil, fmt.Errorf("oidc discovery: issuer mismatch (want %q, got %q)", base, md.Issuer)
	}
	if md.JWKSURI == "" {
		return nil, fmt.Errorf("oidc discovery: response missing jwks_uri")
	}
	return &md, nil
}
