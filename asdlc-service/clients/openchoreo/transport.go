package openchoreo

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"slices"

	"github.com/wso2/asdlc/asdlc-service/clients/httpx"
	"github.com/wso2/asdlc/asdlc-service/clients/openchoreo/gen"
	"github.com/wso2/asdlc/asdlc-service/clients/requests"
	"github.com/wso2/asdlc/asdlc-service/middleware"
)

// AuthProvider is the auth-token contract the OC client depends on. Lets us
// swap `*oauth.TokenProvider` (the only production impl) for a fake in
// tests without touching the oauth package. Method signatures intentionally
// match `*oauth.TokenProvider` so it satisfies the interface as-is.
//
// Mirrors agent-manager's `client.AuthProvider` with one difference: their
// GetToken takes ctx and we don't — our cached service-token fetch doesn't
// need cancellation (sync, in-memory once warmed, refreshed lazily) and
// matching the existing TokenProvider shape lets it satisfy the interface
// with zero changes to the oauth package.
type AuthProvider interface {
	Token() (string, error)
	Invalidate()
}

// Config drives the OpenChoreo client construction. Mirrors agent-manager's
// `client.Config` so callers see the same shape; HostHeader is the only
// addition (local k3d gateway routing — agent-manager talks to OC over plain
// DNS and doesn't need it).
type Config struct {
	BaseURL      string
	HostHeader   string
	AuthProvider AuthProvider
	RetryConfig  requests.RequestRetryConfig
}

// newGenClient builds a *gen.ClientWithResponses with the three-layer
// transport stack:
//
//	1. httpx.WrapTransport (innermost) — stamps X-Correlation-ID for tracing
//	2. RetryableHTTPClient (middle) — jittered exponential backoff on
//	   transient codes; 401 invalidates the cached service token and retries
//	3. RequestEditorFn (outermost, oapi-codegen hook) — sets Authorization,
//	   Host, and X-Use-OpenAPI on every request
//
// Auth lives in the editor (not a RoundTripper) so the retry middleware sees
// a fresh token after invalidation: the editor runs on every attempt and
// re-calls AuthProvider.Token(), which returns the newly-fetched token after
// the 401 callback called Invalidate().
func newGenClient(cfg Config) (*gen.ClientWithResponses, error) {
	if cfg.BaseURL == "" {
		return nil, errors.New("openchoreo: Config.BaseURL is required")
	}

	retryCfg := cfg.RetryConfig
	if retryCfg.RetryOnStatus == nil {
		// Default OC policy: invalidate token on 401, retry; otherwise fall
		// back to the transient-error set. We intentionally don't expose this
		// as the package default — passing it via Config keeps the rule next
		// to its only valid caller (AuthProvider.Invalidate).
		retryCfg.RetryOnStatus = func(status int) bool {
			if status == http.StatusUnauthorized {
				if cfg.AuthProvider != nil {
					slog.Info("openchoreo: 401, invalidating cached token and retrying")
					cfg.AuthProvider.Invalidate()
				}
				return true
			}
			return slices.Contains(requests.TransientHTTPErrorCodes, status)
		}
	}

	inner := &http.Client{Transport: httpx.WrapTransport(nil)}
	outer := requests.NewRetryableHTTPClient(inner, retryCfg)

	authEditor := func(ctx context.Context, req *http.Request) error {
		if cfg.HostHeader != "" {
			req.Host = cfg.HostHeader
		}
		req.Header.Set("X-Use-OpenAPI", "true")
		if tok := authToken(ctx, cfg.AuthProvider); tok != "" {
			req.Header.Set("Authorization", "Bearer "+tok)
		}
		return nil
	}

	c, err := gen.NewClientWithResponses(
		cfg.BaseURL,
		gen.WithHTTPClient(outer),
		gen.WithRequestEditorFn(authEditor),
	)
	if err != nil {
		return nil, fmt.Errorf("openchoreo: build gen client: %w", err)
	}
	return c, nil
}

// authToken prefers the cached service token (client_credentials) over any
// user token in ctx — OC authorises by the service subject. Falls back to
// the inbound user token when the service token is unobtainable so calls
// don't silently lose auth during a Thunder outage.
func authToken(ctx context.Context, ap AuthProvider) string {
	if ap != nil {
		if tok, err := ap.Token(); err == nil {
			return tok
		} else {
			slog.ErrorContext(ctx, "openchoreo: service token fetch failed, falling back to user token", "error", err)
		}
	}
	return middleware.GetAuthToken(ctx)
}
