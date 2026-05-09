package observer

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"time"

	"github.com/wso2/asdlc/asdlc-service/clients/oauth"
)

// LogLine is the BFF's projection of a single Observer log document.
// fluent-bit's pipeline runs with `Merge_Log Off` (see
// task-execution-progress.md §11.4) so the runner's NDJSON arrives
// inside the opaque `log` field; the progress service is responsible
// for JSON.parse-ing.
type LogLine struct {
	Log       string    `json:"log"`
	Timestamp time.Time `json:"timestamp"`
}

// Client reads workflow-run logs from the OpenChoreo Observer service.
// Mirrors agent-manager-service/clients/observabilitysvc/client.go's
// auth + retry shape (Thunder client_credentials, 401 → invalidate +
// retry) but built directly on net/http so we don't pull in the
// generated OpenAPI client.
type Client interface {
	// GetWorkflowRunLogs returns logs for a specific workflow run within
	// the given namespace, restricted to lines emitted at or after
	// sinceTime. limit caps the number of lines returned; pass 0 for the
	// upstream default.
	GetWorkflowRunLogs(ctx context.Context, runName, namespace string, sinceTime time.Time, limit int) ([]LogLine, error)
}

// ErrUnavailable is returned when the Observer is unreachable. The
// controller maps this to HTTP 503 progress_unavailable.
var ErrUnavailable = errors.New("observer unavailable")

// Config configures the Observer client.
type Config struct {
	BaseURL       string
	TokenProvider *oauth.TokenProvider
	// Per-call timeout. Default 5s if zero.
	Timeout time.Duration
}

type httpClient struct {
	baseURL       string
	tokenProvider *oauth.TokenProvider
	http          *http.Client
}

// NewClient constructs an Observer client. Returns nil + nil error when
// BaseURL is empty so the BFF can boot in environments without obs-plane
// (the controller will then 503).
func NewClient(cfg Config) (Client, error) {
	if cfg.BaseURL == "" {
		return nil, nil
	}
	if cfg.TokenProvider == nil {
		return nil, errors.New("observer: TokenProvider is required")
	}
	timeout := cfg.Timeout
	if timeout == 0 {
		timeout = 5 * time.Second
	}
	return &httpClient{
		baseURL:       cfg.BaseURL,
		tokenProvider: cfg.TokenProvider,
		http:          &http.Client{Timeout: timeout},
	}, nil
}

// On-the-wire types — matches the Observer OpenAPI
// (agent-manager/.../observabilitysvc/gen/types.gen.go LogsQueryRequest +
// WorkflowSearchScope), reduced to the fields we actually send.

type workflowSearchScope struct {
	Namespace       string `json:"namespace"`
	WorkflowRunName string `json:"workflowRunName,omitempty"`
}

type logsQueryRequest struct {
	StartTime   time.Time           `json:"startTime"`
	EndTime     time.Time           `json:"endTime"`
	Limit       *int                `json:"limit,omitempty"`
	SortOrder   string              `json:"sortOrder,omitempty"`
	SearchScope workflowSearchScope `json:"searchScope"`
}

type workflowLogEntry struct {
	Log       *string    `json:"log,omitempty"`
	Timestamp *time.Time `json:"timestamp,omitempty"`
}

type logsQueryResponse struct {
	// Observer's response is a discriminated union; for workflow scope it
	// resolves to `[]WorkflowLogEntry`. We let stdlib unmarshal directly.
	Logs   []workflowLogEntry `json:"logs"`
	Total  *int               `json:"total,omitempty"`
	TookMs *int               `json:"tookMs,omitempty"`
}

func (c *httpClient) GetWorkflowRunLogs(ctx context.Context, runName, namespace string, sinceTime time.Time, limit int) ([]LogLine, error) {
	if runName == "" || namespace == "" {
		return nil, errors.New("observer: runName and namespace are required")
	}

	endTime := time.Now().UTC()
	startTime := sinceTime.UTC()
	if startTime.IsZero() || startTime.After(endTime) {
		// Default window: last 30 days, matching agent-manager's pattern.
		startTime = endTime.Add(-30 * 24 * time.Hour)
	}

	body := logsQueryRequest{
		StartTime: startTime,
		EndTime:   endTime,
		SortOrder: "asc",
		SearchScope: workflowSearchScope{
			Namespace:       namespace,
			WorkflowRunName: runName,
		},
	}
	if limit > 0 {
		body.Limit = &limit
	}

	resp, err := c.do(ctx, body)
	if err != nil {
		return nil, err
	}

	out := make([]LogLine, 0, len(resp.Logs))
	for _, e := range resp.Logs {
		line := LogLine{}
		if e.Log != nil {
			line.Log = *e.Log
		}
		if e.Timestamp != nil {
			line.Timestamp = *e.Timestamp
		}
		out = append(out, line)
	}
	return out, nil
}

// do executes a POST /api/v1/logs/query call with auth + 401 retry.
// Mirrors clientBase.send in clients/openchoreo for shape consistency.
func (c *httpClient) do(ctx context.Context, body any) (*logsQueryResponse, error) {
	url := c.baseURL + "/api/v1/logs/query"

	send := func() (*http.Response, []byte, error) {
		raw, err := json.Marshal(body)
		if err != nil {
			return nil, nil, fmt.Errorf("observer: marshal request: %w", err)
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(raw))
		if err != nil {
			return nil, nil, fmt.Errorf("observer: build request: %w", err)
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/json")
		token, err := c.tokenProvider.Token()
		if err != nil {
			return nil, nil, fmt.Errorf("observer: token fetch: %w", err)
		}
		req.Header.Set("Authorization", "Bearer "+token)

		httpResp, err := c.http.Do(req)
		if err != nil {
			return nil, nil, fmt.Errorf("%w: %v", ErrUnavailable, err)
		}
		defer httpResp.Body.Close()
		respBody, _ := io.ReadAll(httpResp.Body)
		return httpResp, respBody, nil
	}

	resp, respBody, err := send()
	if err != nil {
		return nil, err
	}
	if resp.StatusCode == http.StatusUnauthorized {
		slog.WarnContext(ctx, "observer: 401, invalidating token + retrying", "body", string(respBody))
		c.tokenProvider.Invalidate()
		resp, respBody, err = send()
		if err != nil {
			return nil, err
		}
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("observer: unexpected status %d: %s", resp.StatusCode, string(respBody))
	}

	var out logsQueryResponse
	if err := json.Unmarshal(respBody, &out); err != nil {
		return nil, fmt.Errorf("observer: decode response: %w", err)
	}
	return &out, nil
}
