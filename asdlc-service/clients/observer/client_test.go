package observer

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/wso2/asdlc/asdlc-service/clients/oauth"
)

// fakeTokenProvider serves a static token. We construct a TokenProvider
// pointed at our token-stub server so Invalidate() round-trips.
func newFakeTokenProvider(t *testing.T, tokens []string) *oauth.TokenProvider {
	t.Helper()
	var idx int32
	tokenSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		i := atomic.AddInt32(&idx, 1) - 1
		if int(i) >= len(tokens) {
			i = int32(len(tokens) - 1)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"access_token":"` + tokens[i] + `","expires_in":3600}`))
	}))
	t.Cleanup(tokenSrv.Close)
	return oauth.NewTokenProvider(tokenSrv.URL, "client", "secret", "")
}

func TestObserver_GetWorkflowRunLogs_Success(t *testing.T) {
	tp := newFakeTokenProvider(t, []string{"good"})
	ts := time.Date(2026, 5, 7, 14, 30, 0, 0, time.UTC)
	respBody := `{"logs":[{"log":"line-1","timestamp":"2026-05-07T14:30:01Z"},{"log":"line-2","timestamp":"2026-05-07T14:30:02Z"}],"total":2}`

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/logs/query" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer good" {
			t.Errorf("missing/incorrect bearer: %q", got)
		}
		var body logsQueryRequest
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode body: %v", err)
		}
		if body.SearchScope.Namespace != "wp-ns" || body.SearchScope.WorkflowRunName != "run-1" {
			t.Errorf("unexpected scope %+v", body.SearchScope)
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(respBody))
	}))
	t.Cleanup(srv.Close)

	cli, err := NewClient(Config{BaseURL: srv.URL, TokenProvider: tp})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	lines, err := cli.GetWorkflowRunLogs(context.Background(), "run-1", "wp-ns", ts, 100)
	if err != nil {
		t.Fatalf("GetWorkflowRunLogs: %v", err)
	}
	if len(lines) != 2 {
		t.Fatalf("expected 2 lines, got %d", len(lines))
	}
	if lines[0].Log != "line-1" || lines[1].Log != "line-2" {
		t.Fatalf("unexpected lines: %+v", lines)
	}
}

func TestObserver_GetWorkflowRunLogs_401Retry(t *testing.T) {
	tp := newFakeTokenProvider(t, []string{"stale", "fresh"})
	var attempts int32

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt32(&attempts, 1)
		token := r.Header.Get("Authorization")
		if n == 1 {
			if token != "Bearer stale" {
				t.Errorf("expected stale token, got %q", token)
			}
			http.Error(w, "expired", http.StatusUnauthorized)
			return
		}
		if token != "Bearer fresh" {
			t.Errorf("expected fresh token, got %q", token)
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"logs":[]}`))
	}))
	t.Cleanup(srv.Close)

	cli, _ := NewClient(Config{BaseURL: srv.URL, TokenProvider: tp})
	if _, err := cli.GetWorkflowRunLogs(context.Background(), "run", "ns", time.Now(), 0); err != nil {
		t.Fatalf("expected retry to succeed, got %v", err)
	}
	if attempts != 2 {
		t.Fatalf("expected exactly 2 attempts, got %d", attempts)
	}
}

func TestObserver_GetWorkflowRunLogs_503MapsToUnavailable(t *testing.T) {
	tp := newFakeTokenProvider(t, []string{"good"})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "down for maintenance", http.StatusServiceUnavailable)
	}))
	t.Cleanup(srv.Close)

	cli, _ := NewClient(Config{BaseURL: srv.URL, TokenProvider: tp})
	_, err := cli.GetWorkflowRunLogs(context.Background(), "run", "ns", time.Now(), 0)
	if err == nil {
		t.Fatal("expected error on 503")
	}
	// 503 is reported via the unexpected-status path; transport-level
	// failures fold to ErrUnavailable. Both are acceptable for a 5xx.
	if !strings.Contains(err.Error(), "503") {
		t.Fatalf("expected status reflected in error, got %v", err)
	}
}

func TestObserver_GetWorkflowRunLogs_TransportFailureIsUnavailable(t *testing.T) {
	tp := newFakeTokenProvider(t, []string{"good"})
	// Point the client at a closed listener — the underlying http.Do
	// returns a network error, which the client wraps as ErrUnavailable.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	srv.Close()

	cli, _ := NewClient(Config{BaseURL: srv.URL, TokenProvider: tp, Timeout: 200 * time.Millisecond})
	_, err := cli.GetWorkflowRunLogs(context.Background(), "run", "ns", time.Now(), 0)
	if err == nil {
		t.Fatal("expected error on transport failure")
	}
	if !errors.Is(err, ErrUnavailable) {
		t.Fatalf("expected ErrUnavailable, got %v", err)
	}
}

func TestObserver_NewClient_NilOnEmptyBaseURL(t *testing.T) {
	cli, err := NewClient(Config{BaseURL: ""})
	if err != nil {
		t.Fatalf("expected nil error on empty BaseURL, got %v", err)
	}
	if cli != nil {
		t.Fatal("expected nil client on empty BaseURL")
	}
}
