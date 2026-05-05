package requests

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
)

// Result wraps an HTTP response for convenient scanning.
type Result struct {
	StatusCode int
	body       []byte
	headers    http.Header
	Err        error
}

// SendRequest executes the given HttpRequest using the provided http.Client.
func SendRequest(ctx context.Context, client *http.Client, req *HttpRequest) *Result {
	httpReq, err := req.Build(ctx)
	if err != nil {
		return &Result{Err: fmt.Errorf("build request %s: %w", req.Name, err)}
	}

	resp, err := client.Do(httpReq)
	if err != nil {
		return &Result{Err: fmt.Errorf("send request %s: %w", req.Name, err)}
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return &Result{Err: fmt.Errorf("read response body %s: %w", req.Name, err)}
	}

	return &Result{
		StatusCode: resp.StatusCode,
		body:       body,
		headers:    resp.Header,
	}
}

// ScanResponse unmarshals the response body into dest if the status code matches expectedStatus.
func (r *Result) ScanResponse(dest any, expectedStatus int) error {
	if r.Err != nil {
		return r.Err
	}
	if r.StatusCode != expectedStatus {
		return &HttpError{StatusCode: r.StatusCode, Body: string(r.body)}
	}
	if dest == nil || len(r.body) == 0 {
		return nil
	}
	if err := json.Unmarshal(r.body, dest); err != nil {
		return fmt.Errorf("unmarshal response: %w", err)
	}
	return nil
}

// GetHeader returns a response header value.
func (r *Result) GetHeader(key string) string {
	if r.headers == nil {
		return ""
	}
	return r.headers.Get(key)
}
