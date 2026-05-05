package remoteworker

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/wso2/asdlc/asdlc-service/clients/auth"
	"github.com/wso2/asdlc/asdlc-service/clients/httpx"
)

// Client dispatches implementation tasks to a remote-worker service.
type Client interface {
	Dispatch(ctx context.Context, req *DispatchRequest) (*DispatchResponse, error)
}

// IdentityPayload is the committer attribution + GitHub login the workspace
// configures `.git/config` and `gh` config with. Resolved from the org's
// credential at dispatch time.
type IdentityPayload struct {
	Name  string `json:"name"`
	Email string `json:"email"`
	Login string `json:"login"`
}

// DispatchRequest is the payload sent to the remote-worker service.
//
// The remote-worker provisions a per-task workspace under
// ~/asdlc-workspace/<orgId>/<projectId>/<taskId>/ and configures git + gh
// to authenticate via /credentials/refresh on git-service. The bearer is
// the agent's authn for that endpoint; the token itself is never put on
// the wire here.
type DispatchRequest struct {
	TaskID         string          `json:"taskId"`
	OrgID          string          `json:"orgId"`
	ProjectID      string          `json:"projectId"`
	ComponentName  string          `json:"componentName"`
	BranchName     string          `json:"branchName"`
	RepoURL        string          `json:"repoUrl"`
	Bearer         string          `json:"bearer"`
	Identity       IdentityPayload `json:"identity"`
	GitServiceURL  string          `json:"gitServiceUrl"`
	Prompt         string          `json:"prompt"`
}

// DispatchResponse is the response from the remote-worker service.
type DispatchResponse struct {
	TaskID        string `json:"taskId"`
	WorkspacePath string `json:"workspacePath"`
	Status        string `json:"status"` // "running" | "failed"
	Error         string `json:"error,omitempty"`
}

type client struct {
	baseURL    string
	httpClient *http.Client
}

// NewClient builds a remote-worker client. provider attaches a Service JWT
// to every outbound call (audience: remote-worker); pass nil to disable
// service-auth in tests/dev.
func NewClient(baseURL string, provider *auth.AuthProvider) Client {
	return &client{
		baseURL: baseURL,
		httpClient: &http.Client{
			Timeout:   30 * time.Second, // dispatch should return quickly (async spawn)
			Transport: httpx.ServiceTransport(provider),
		},
	}
}

func (c *client) Dispatch(ctx context.Context, req *DispatchRequest) (*DispatchResponse, error) {
	body, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	url := c.baseURL + "/dispatch"
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("remote-worker request: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("remote-worker error (status %d): %s", resp.StatusCode, string(respBody))
	}

	var result DispatchResponse
	if err := json.Unmarshal(respBody, &result); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}

	return &result, nil
}
