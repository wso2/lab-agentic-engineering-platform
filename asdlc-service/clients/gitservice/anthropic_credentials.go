package gitservice

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"
)

// AnthropicProjection is the JSON shape returned by the per-org Anthropic
// credential routes. The raw key never crosses this boundary — only
// prefix + last4 for the UI.
type AnthropicProjection struct {
	OcOrgID         string     `json:"ocOrgId"`
	KeyPrefix       string     `json:"keyPrefix"`
	KeyLast4        string     `json:"keyLast4"`
	Status          string     `json:"status"`
	ConnectedAt     time.Time  `json:"connectedAt"`
	LastValidatedAt *time.Time `json:"lastValidatedAt,omitempty"`
	ValidationError *string    `json:"validationError,omitempty"`
}

// AnthropicConnectRequest is the body for POST .../anthropic.
type AnthropicConnectRequest struct {
	APIKey string `json:"apiKey"`
}

// ApplyAnthropicWPSecretResult mirrors git-service's services.ApplyWPSecretResult.
// The secretRefName is the K8s Secret name in workflows-<orgID> that the
// coding-agent WorkflowRun mounts via parameters.anthropic.secretRef.
type ApplyAnthropicWPSecretResult struct {
	SecretRefName string `json:"secretRefName"`
}

// ErrAnthropicKeyRequired surfaces from ApplyAnthropicWPSecret when the
// org has no active Anthropic credential row. The BFF dispatch path maps
// it to 422 with code `anthropic_key_required`.
var ErrAnthropicKeyRequired = errors.New("anthropic: org key required")

// CreateOrReplaceAnthropic — POST /internal/credentials/orgs/{org}/anthropic.
func (c *client) CreateOrReplaceAnthropic(ctx context.Context, ocOrgID string, req AnthropicConnectRequest) (*AnthropicProjection, error) {
	body, _ := json.Marshal(req)
	url := fmt.Sprintf("%s/internal/credentials/orgs/%s/anthropic", c.baseURL, ocOrgID)
	httpReq, _ := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	httpReq.Header.Set("Content-Type", "application/json")

	var proj AnthropicProjection
	if err := c.doInternal(httpReq, &proj); err != nil {
		return nil, err
	}
	return &proj, nil
}

// GetAnthropicProjection — GET /internal/credentials/orgs/{org}/anthropic.
func (c *client) GetAnthropicProjection(ctx context.Context, ocOrgID string) (*AnthropicProjection, error) {
	url := fmt.Sprintf("%s/internal/credentials/orgs/%s/anthropic", c.baseURL, ocOrgID)
	httpReq, _ := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	var proj AnthropicProjection
	if err := c.doInternal(httpReq, &proj); err != nil {
		return nil, err
	}
	return &proj, nil
}

// DisconnectAnthropic — DELETE /internal/credentials/orgs/{org}/anthropic.
func (c *client) DisconnectAnthropic(ctx context.Context, ocOrgID string) error {
	url := fmt.Sprintf("%s/internal/credentials/orgs/%s/anthropic", c.baseURL, ocOrgID)
	httpReq, _ := http.NewRequestWithContext(ctx, http.MethodDelete, url, nil)
	return c.doInternal(httpReq, nil)
}

// ApplyAnthropicWPSecret — POST .../anthropic/apply-wp-secret. Called by
// the BFF dispatch path immediately before TriggerCodingAgent. Maps the
// 422 `anthropic_key_required` to ErrAnthropicKeyRequired.
func (c *client) ApplyAnthropicWPSecret(ctx context.Context, ocOrgID string) (*ApplyAnthropicWPSecretResult, error) {
	url := fmt.Sprintf("%s/internal/credentials/orgs/%s/anthropic/apply-wp-secret", c.baseURL, ocOrgID)
	httpReq, _ := http.NewRequestWithContext(ctx, http.MethodPost, url, nil)
	var out ApplyAnthropicWPSecretResult
	if err := c.doInternal(httpReq, &out); err != nil {
		var ce *CredentialError
		if errors.As(err, &ce) && ce.Code == "anthropic_key_required" {
			return nil, ErrAnthropicKeyRequired
		}
		return nil, err
	}
	return &out, nil
}
