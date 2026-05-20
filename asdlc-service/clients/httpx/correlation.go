// Package httpx provides shared HTTP plumbing for outbound clients.
package httpx

import (
	"net/http"

	"github.com/wso2/asdlc/asdlc-service/middleware"
)

// CorrelationTransport propagates the X-Correlation-ID header from the
// request context onto every outbound request. Wrap an existing transport
// (or pass nil to wrap http.DefaultTransport) and assign to http.Client.Transport.
type CorrelationTransport struct {
	Base http.RoundTripper
}

// RoundTrip implements http.RoundTripper. Per the contract it must not
// mutate the caller's request, so we clone before adding the header.
func (t *CorrelationTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	base := t.Base
	if base == nil {
		base = http.DefaultTransport
	}
	id := middleware.GetCorrelationID(req.Context())
	if id == "" || req.Header.Get(middleware.CorrelationIDHeader) != "" {
		return base.RoundTrip(req)
	}
	cloned := req.Clone(req.Context())
	cloned.Header.Set(middleware.CorrelationIDHeader, id)
	return base.RoundTrip(cloned)
}

// WrapTransport returns t wrapped with correlation-ID propagation. nil is
// treated as http.DefaultTransport.
func WrapTransport(t http.RoundTripper) http.RoundTripper {
	return &CorrelationTransport{Base: t}
}
