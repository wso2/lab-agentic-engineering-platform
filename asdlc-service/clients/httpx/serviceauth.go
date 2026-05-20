package httpx

import (
	"errors"
	"fmt"
	"net/http"

	"github.com/wso2/asdlc/asdlc-service/clients/auth"
)

// ServiceAuthTransport attaches a Service JWT (OAuth2 client_credentials)
// Authorization header on every outbound request. On a 401 response it
// invalidates the cached token and retries once with a fresh token —
// covering Thunder restarts, key rotations, and clock-skew expiries.
//
// Wrap a base RoundTripper that already includes the correlation header
// (CorrelationTransport) so both run on the same hop.
type ServiceAuthTransport struct {
	provider *auth.AuthProvider
	base     http.RoundTripper
}

// WrapWithServiceAuth returns a RoundTripper that adds Authorization to
// every request using provider's cached token. base may be nil — defaults
// to http.DefaultTransport. provider may be nil — when nil the wrapper is
// a no-op pass-through (used in dev/test where service auth is disabled).
func WrapWithServiceAuth(provider *auth.AuthProvider, base http.RoundTripper) http.RoundTripper {
	if base == nil {
		base = http.DefaultTransport
	}
	if provider == nil {
		return base
	}
	return &ServiceAuthTransport{provider: provider, base: base}
}

// ServiceTransport returns the standard outbound transport used by every
// service-to-service client: correlation-ID propagation wrapped in service
// auth (provider may be nil to disable auth in dev/test).
func ServiceTransport(provider *auth.AuthProvider) http.RoundTripper {
	return WrapWithServiceAuth(provider, WrapTransport(nil))
}

// RoundTrip attaches the bearer, executes, and on 401 invalidates the
// cached token and retries once.
func (t *ServiceAuthTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if err := t.attach(req); err != nil {
		return nil, err
	}

	resp, err := t.base.RoundTrip(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusUnauthorized {
		return resp, nil
	}

	// Drop the 401 body so the connection can be reused, then refresh.
	if resp.Body != nil {
		_ = resp.Body.Close()
	}
	t.provider.Invalidate()

	retry, err := cloneRequestForRetry(req)
	if err != nil {
		return nil, errors.Join(fmt.Errorf("clone request for 401 retry: %w", err))
	}
	if err := t.attach(retry); err != nil {
		return nil, err
	}
	return t.base.RoundTrip(retry)
}

func (t *ServiceAuthTransport) attach(req *http.Request) error {
	token, err := t.provider.GetToken(req.Context())
	if err != nil {
		return fmt.Errorf("fetch service token: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	return nil
}

// cloneRequestForRetry produces a fresh request that can be safely sent
// again. Bodyless requests need only a Header clone; bodied requests rely
// on http.Request.GetBody (set by callers that use bytes.NewReader-style
// clients we ship with).
func cloneRequestForRetry(req *http.Request) (*http.Request, error) {
	cloned := req.Clone(req.Context())
	if req.GetBody != nil {
		body, err := req.GetBody()
		if err != nil {
			return nil, err
		}
		cloned.Body = body
	}
	return cloned, nil
}
