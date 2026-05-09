package openchoreo

import (
	"context"
	"errors"
	"log/slog"
	"net/http"

	"github.com/wso2/asdlc/asdlc-service/clients/oauth"
	"github.com/wso2/asdlc/asdlc-service/clients/requests"
	"github.com/wso2/asdlc/asdlc-service/middleware"
)

// clientBase holds state shared by every OC sub-client. Embed it to get
// newRequest/send with service-token auth and retry-on-401 behavior.
//
// The OC namespace name is the OC org handle directly. Per-call sites
// pass `orgName` (== ouHandle) to the namespace-shaped OC API; there is
// no override, no fallback, no map.
type clientBase struct {
	baseURL       string
	hostHeader    string
	httpClient    *http.Client
	tokenProvider *oauth.TokenProvider
}

// newRequest builds an HttpRequest without auth. send() attaches the bearer
// token at send time so a 401 retry can use a refreshed token.
func (c *clientBase) newRequest(_ context.Context, name, method, url string) *requests.HttpRequest {
	req := requests.NewRequest(name, method, url)
	if c.hostHeader != "" {
		req.SetHost(c.hostHeader)
	}
	return req
}

// authToken prefers the service token (client_credentials) over any user
// token in ctx — OC authorizes requests by the service subject.
func (c *clientBase) authToken(ctx context.Context) string {
	if c.tokenProvider != nil {
		token, err := c.tokenProvider.Token()
		if err == nil {
			return token
		}
		slog.ErrorContext(ctx, "service token fetch failed, falling back to user token", "error", err)
	}
	return middleware.GetAuthToken(ctx)
}

// send attaches auth, executes the request, and scans the response. On 401 it
// invalidates the cached service token and retries once with a fresh token —
// which covers stale caches, signing-key rotation, and Thunder restarts.
func (c *clientBase) send(ctx context.Context, req *requests.HttpRequest, dest any, expectedStatus int) error {
	attach := func(r *requests.HttpRequest) *requests.HttpRequest {
		if token := c.authToken(ctx); token != "" {
			r.SetHeader("Authorization", "Bearer "+token)
		}
		return r
	}
	result := requests.SendRequest(ctx, c.httpClient, attach(req))
	err := result.ScanResponse(dest, expectedStatus)
	if err == nil {
		return nil
	}
	var httpErr *requests.HttpError
	if c.tokenProvider == nil || !errors.As(err, &httpErr) || httpErr.StatusCode != http.StatusUnauthorized {
		return err
	}
	slog.WarnContext(ctx, "service token rejected, invalidating and retrying", "body", httpErr.Body)
	c.tokenProvider.Invalidate()
	result = requests.SendRequest(ctx, c.httpClient, attach(req))
	return result.ScanResponse(dest, expectedStatus)
}
