package openchoreo

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"slices"
	"time"

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

	// DIAGNOSTIC (revert in Plan A PR) — wrap with logging RoundTripper to
	// capture method/URL/status/elapsed per OC client call. Confirms which
	// requests 402 and the exact URL surface BFF is hitting on platform-api.
	inner := &http.Client{Transport: &diagnosticTransport{base: httpx.WrapTransport(nil)}}
	outer := requests.NewRetryableHTTPClient(inner, retryCfg)

	authEditor := func(ctx context.Context, req *http.Request) error {
		if cfg.HostHeader != "" {
			req.Host = cfg.HostHeader
		}
		req.Header.Set("X-Use-OpenAPI", "true")
		tok, src := authTokenWithSource(ctx, cfg.AuthProvider)
		if tok != "" {
			req.Header.Set("Authorization", "Bearer "+tok)
		}
		// DIAGNOSTIC (revert in Plan A PR) — tag the request context so the
		// logging RoundTripper can correlate the auth source with the eventual
		// status code.
		*req = *req.WithContext(contextWithAuthSource(req.Context(), src))
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

// authToken prefers the inbound user JWT over the M2M service token.
// Reason: platform-api-service derives the target OC namespace from the
// JWT's `ouId` claim (see backend/core/internal/cpapi/handler.go), so a
// service-credentials token routes every call to the namespace of the
// M2M client's owning OU (Admin) instead of the caller's tenant.
// Falls back to the M2M token only when no user token is present —
// preserves auth for async paths that explicitly inject a service token
// via middleware.WithAuthToken (e.g. MCP-triggered deploys).
func authToken(ctx context.Context, ap AuthProvider) string {
	tok, _ := authTokenWithSource(ctx, ap)
	return tok
}

// authTokenWithSource is a DIAGNOSTIC variant (revert in Plan A PR) that
// also returns which auth source supplied the token: "user", "m2m", or "none".
// Used to correlate request status codes with the auth path taken.
func authTokenWithSource(ctx context.Context, ap AuthProvider) (string, string) {
	if tok := middleware.GetAuthToken(ctx); tok != "" {
		return tok, "user"
	}
	if ap != nil {
		if tok, err := ap.Token(); err == nil {
			return tok, "m2m"
		} else {
			slog.ErrorContext(ctx, "openchoreo: no user token in ctx and service token fetch failed", "error", err)
		}
	}
	return "", "none"
}

// authSourceCtxKey is the context key for the auth source tag (DIAGNOSTIC).
type authSourceCtxKey struct{}

func contextWithAuthSource(ctx context.Context, src string) context.Context {
	return context.WithValue(ctx, authSourceCtxKey{}, src)
}

func authSourceFromContext(ctx context.Context) string {
	if v, ok := ctx.Value(authSourceCtxKey{}).(string); ok {
		return v
	}
	return ""
}

// diagnosticTransport is a DIAGNOSTIC RoundTripper (revert in Plan A PR) that
// logs the method, URL, status, and elapsed time of every OC client request
// alongside the auth source tag set in authEditor. Surfaces exactly which
// URLs 402 and which auth path was used.
type diagnosticTransport struct {
	base http.RoundTripper
}

func (t *diagnosticTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	start := time.Now()
	resp, err := t.base.RoundTrip(req)
	elapsed := time.Since(start)
	status := 0
	if resp != nil {
		status = resp.StatusCode
	}
	errStr := ""
	if err != nil {
		errStr = err.Error()
	}
	slog.InfoContext(req.Context(), "openchoreo: request",
		"method", req.Method,
		"url", req.URL.String(),
		"status", status,
		"elapsed_ms", elapsed.Milliseconds(),
		"auth_source", authSourceFromContext(req.Context()),
		"error", errStr,
	)
	return resp, err
}
