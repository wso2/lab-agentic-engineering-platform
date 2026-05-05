package httpx

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/wso2/asdlc/asdlc-service/middleware"
)

// roundTripFunc adapts a function to the http.RoundTripper interface so
// tests don't need a full http.Server.
type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }

func newContextWithCorrelationID(t *testing.T, ctx context.Context, id string) context.Context {
	t.Helper()
	return middleware.WithCorrelationID(ctx, id)
}

func TestCorrelationTransport_AddsHeaderFromContext(t *testing.T) {
	var captured string
	rt := WrapTransport(roundTripFunc(func(req *http.Request) (*http.Response, error) {
		captured = req.Header.Get(middleware.CorrelationIDHeader)
		return &http.Response{StatusCode: 200, Body: http.NoBody}, nil
	}))

	ctx := context.Background()
	// Mimic the inbound middleware shape — store correlation ID under the
	// same context key the inbound middleware uses.
	corrID := "test-corr-12345"
	ctx = newContextWithCorrelationID(t, ctx, corrID)

	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, "https://example.invalid/", nil)
	resp, err := rt.RoundTrip(req)
	if err != nil {
		t.Fatalf("RoundTrip: %v", err)
	}
	_ = resp.Body.Close()

	if captured != corrID {
		t.Errorf("X-Correlation-ID = %q, want %q", captured, corrID)
	}
}

func TestCorrelationTransport_PreservesExplicitHeader(t *testing.T) {
	var captured string
	rt := WrapTransport(roundTripFunc(func(req *http.Request) (*http.Response, error) {
		captured = req.Header.Get(middleware.CorrelationIDHeader)
		return &http.Response{StatusCode: 200, Body: http.NoBody}, nil
	}))

	ctx := newContextWithCorrelationID(t, context.Background(), "from-context")
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, "https://example.invalid/", nil)
	req.Header.Set(middleware.CorrelationIDHeader, "explicit")

	if _, err := rt.RoundTrip(req); err != nil {
		t.Fatalf("RoundTrip: %v", err)
	}
	if captured != "explicit" {
		t.Errorf("explicit header should not be overwritten: got %q", captured)
	}
}

func TestCorrelationTransport_NoOpWhenContextEmpty(t *testing.T) {
	var captured string
	rt := WrapTransport(roundTripFunc(func(req *http.Request) (*http.Response, error) {
		captured = req.Header.Get(middleware.CorrelationIDHeader)
		return &http.Response{StatusCode: 200, Body: http.NoBody}, nil
	}))

	req, _ := http.NewRequest(http.MethodGet, "https://example.invalid/", nil)
	if _, err := rt.RoundTrip(req); err != nil {
		t.Fatalf("RoundTrip: %v", err)
	}
	if captured != "" {
		t.Errorf("expected no correlation header, got %q", captured)
	}
}

// TestCorrelationTransport_EndToEnd verifies the path through a real
// httptest.Server: an inbound request carrying X-Correlation-ID lands in
// context via the inbound middleware, then an outbound call from the
// handler propagates the same ID through WrapTransport.
func TestCorrelationTransport_EndToEnd(t *testing.T) {
	upstreamCorrID := ""
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamCorrID = r.Header.Get(middleware.CorrelationIDHeader)
		w.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()

	client := &http.Client{Transport: WrapTransport(nil)}

	frontend := http.NewServeMux()
	frontend.HandleFunc("/proxy", func(w http.ResponseWriter, r *http.Request) {
		req, _ := http.NewRequestWithContext(r.Context(), http.MethodGet, upstream.URL, nil)
		resp, err := client.Do(req)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		_ = resp.Body.Close()
		w.WriteHeader(http.StatusOK)
	})

	wrapped := middleware.AddCorrelationID()(frontend)
	frontendSrv := httptest.NewServer(wrapped)
	defer frontendSrv.Close()

	req, _ := http.NewRequest(http.MethodGet, frontendSrv.URL+"/proxy", nil)
	req.Header.Set(middleware.CorrelationIDHeader, "end-to-end-id")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("client.Do: %v", err)
	}
	_ = resp.Body.Close()

	if upstreamCorrID != "end-to-end-id" {
		t.Errorf("upstream X-Correlation-ID = %q, want %q", upstreamCorrID, "end-to-end-id")
	}
}
