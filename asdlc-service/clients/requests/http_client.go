package requests

import "net/http"

// HttpClient is the minimal interface oapi-codegen's generated clients ask
// for via `gen.WithHTTPClient`. Lets us swap a plain *http.Client for a
// RetryableHTTPClient transparently. Matches agent-manager's `clients/requests`
// shape.
type HttpClient interface {
	Do(req *http.Request) (*http.Response, error)
}

// Compile-time assertion that *http.Client satisfies the interface.
var _ HttpClient = (*http.Client)(nil)
