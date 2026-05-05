package requests

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/url"
)

// HttpRequest is a fluent builder for HTTP requests.
type HttpRequest struct {
	Name    string
	URL     string
	Method  string
	query   url.Values
	headers http.Header
	body    []byte
	host    string
}

func NewRequest(name, method, rawURL string) *HttpRequest {
	return &HttpRequest{
		Name:    name,
		URL:     rawURL,
		Method:  method,
		query:   url.Values{},
		headers: http.Header{},
	}
}

func (r *HttpRequest) SetHeader(key, value string) *HttpRequest {
	r.headers.Set(key, value)
	return r
}

// SetHost overrides the Host header sent to the server.
func (r *HttpRequest) SetHost(host string) *HttpRequest {
	r.host = host
	return r
}

func (r *HttpRequest) SetQuery(key, value string) *HttpRequest {
	r.query.Set(key, value)
	return r
}

func (r *HttpRequest) SetJSON(body any) *HttpRequest {
	data, err := json.Marshal(body)
	if err == nil {
		r.body = data
		r.headers.Set("Content-Type", "application/json")
	}
	return r
}

// Build constructs the *http.Request ready for execution.
func (r *HttpRequest) Build(ctx context.Context) (*http.Request, error) {
	rawURL := r.URL
	if len(r.query) > 0 {
		rawURL += "?" + r.query.Encode()
	}

	var bodyReader *bytes.Reader
	if r.body != nil {
		bodyReader = bytes.NewReader(r.body)
	} else {
		bodyReader = bytes.NewReader([]byte{})
	}

	req, err := http.NewRequestWithContext(ctx, r.Method, rawURL, bodyReader)
	if err != nil {
		return nil, err
	}

	req.Header = r.headers.Clone()
	req.Header.Set("Accept", "application/json")

	if r.host != "" {
		req.Host = r.host
	}

	return req, nil
}
