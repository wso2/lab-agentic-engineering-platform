package openchoreo

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strings"

	"github.com/wso2/asdlc/asdlc-service/clients/oauth"
	"github.com/wso2/asdlc/asdlc-service/clients/requests"
	"github.com/wso2/asdlc/asdlc-service/middleware"
)

// clientBase holds state shared by every OC sub-client. Embed it to get
// newRequest/send with service-token auth and retry-on-401 behavior.
type clientBase struct {
	baseURL       string
	hostHeader    string
	httpClient    *http.Client
	tokenProvider *oauth.TokenProvider
	// nsMap maps org handles to actual K8s namespaces. Used when running
	// under WSO2Cloud where namespaces are auto-generated (e.g.
	// admin → dp-wso2cloud-core-development-54e3d6ff).
	nsMap map[string]string
}

// resolveNamespace returns the actual K8s namespace for orgHandle, or
// orgHandle itself if no override is configured.
func (c *clientBase) resolveNamespace(orgHandle string) string {
	if c.nsMap == nil {
		return orgHandle
	}
	if ns, ok := c.nsMap[orgHandle]; ok {
		return ns
	}
	return orgHandle
}

// parseNamespaceOverride parses a comma-separated list of "org=ns" pairs.
// Empty input returns nil (no override).
func parseNamespaceOverride(raw string) map[string]string {
	if raw == "" {
		return nil
	}
	m := make(map[string]string)
	for _, pair := range strings.Split(raw, ",") {
		pair = strings.TrimSpace(pair)
		if pair == "" {
			continue
		}
		kv := strings.SplitN(pair, "=", 2)
		if len(kv) == 2 {
			org := strings.TrimSpace(kv[0])
			ns := strings.TrimSpace(kv[1])
			if org != "" && ns != "" {
				m[org] = ns
			}
		}
	}
	if len(m) == 0 {
		return nil
	}
	return m
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
