// Package secretmanagerapi is the SM-API provider for the asdlc
// secretmanagersvc client (WS1.3). Ported from
// agent-platform/agent-manager-service/secrets/ with adaptations:
//
//   - SecretLocation reshaped from agent-manager's
//     {org, project, env, agent, config, entity} to asdlc's
//     {org, project, task, entity}. buildLabelsFromLocation and
//     labelSelectorFromLocation drop the env/agent/config trio and add
//     `task` for per-task secret materialization (per-run Anthropic
//     key + GitHub PAT ExternalSecrets).
//
//   - Inbound user JWT is read from the asdlc jwtassertion middleware
//     instead of agent-manager's.
//
// Per ADR-0002 this provider is the only path to the Secret Manager API
// in both local and cloud deployments — there is no openbao provider.
// The same binary is built once and the composition root constructs
// this provider in both build configs.
package secretmanagerapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/wso2/asdlc/asdlc-service/clients/secretmanagersvc"
	"github.com/wso2/asdlc/asdlc-service/middleware/jwtassertion"
)

// ProviderName is the registry key under which Register registers this
// provider. Mirrors the OpenChoreo / external-secrets convention.
const ProviderName = "sm-api"

// Compile-time interface assertions.
var (
	_ secretmanagersvc.Provider               = (*Provider)(nil)
	_ secretmanagersvc.SecretReferenceManager = (*Provider)(nil)
	_ secretmanagersvc.SecretsClient          = (*Client)(nil)
)

// Config holds configuration for the Secret Manager API client.
type Config struct {
	// BaseURL is the Secret Manager API base URL (e.g.
	// "http://secret-manager-api.openchoreo.localhost:8080" locally,
	// "https://secret-manager-api.openchoreo.dp.${cloud_base_domain}"
	// on cloud).
	BaseURL string
	// Timeout is the HTTP client timeout (default: 30s).
	Timeout time.Duration
}

// Provider implements secretmanagersvc.Provider for the SM-API.
type Provider struct {
	config Config
}

// NewProvider builds a SM-API provider. Timeout defaults to 30s.
func NewProvider(cfg Config) *Provider {
	if cfg.Timeout == 0 {
		cfg.Timeout = 30 * time.Second
	}
	return &Provider{config: cfg}
}

// Capabilities is WriteOnly — the SM-API never returns secret values.
func (p *Provider) Capabilities() secretmanagersvc.StoreCapabilities {
	return secretmanagersvc.StoreCapabilityWriteOnly
}

// ManagesSecretReferences signals that SM-API server creates + updates
// SecretReference CRDs internally; the high-level SecretManagementClient
// must NOT make its own SR CRUD calls when this provider is in use.
func (p *Provider) ManagesSecretReferences() bool { return true }

func (p *Provider) NewClient(_ *secretmanagersvc.StoreConfig) (secretmanagersvc.SecretsClient, error) {
	return &Client{
		baseURL:    p.config.BaseURL,
		httpClient: &http.Client{Timeout: p.config.Timeout},
	}, nil
}

func (p *Provider) ValidateConfig(_ *secretmanagersvc.StoreConfig) error {
	if p.config.BaseURL == "" {
		return errors.New("sm-api: BaseURL is required")
	}
	return nil
}

// Client implements secretmanagersvc.SecretsClient against the SM-API.
type Client struct {
	baseURL    string
	httpClient *http.Client
}

// PushSecret creates a new secret via POST /secrets. Returns the
// SM-API-generated secretReferenceName (e.g. "cred-anthropic-a1b2c3d4").
func (c *Client) PushSecret(ctx context.Context, location secretmanagersvc.SecretLocation, value []byte, metadata *secretmanagersvc.SecretMetadata) (string, error) {
	jwt := jwtassertion.GetJWTFromContext(ctx)
	if jwt == "" {
		return "", errors.New("sm-api: no JWT in context")
	}
	kvPath, err := location.KVPath()
	if err != nil {
		return "", fmt.Errorf("sm-api: derive secret name: %w", err)
	}
	secretName := sanitizeForK8sLabel(kvPath)

	var secretData map[string]string
	if err := json.Unmarshal(value, &secretData); err != nil {
		return "", fmt.Errorf("sm-api: unmarshal secret data: %w", err)
	}

	labels := buildLabelsFromLocation(location, metadataLabels(metadata))
	body, err := json.Marshal(CreateSecretRequest{
		Metadata: SecretMetadataRequest{Name: secretName, Labels: labels},
		Spec:     SecretSpecRequest{Data: secretData},
	})
	if err != nil {
		return "", fmt.Errorf("sm-api: marshal request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/secrets", bytes.NewReader(body))
	if err != nil {
		return "", fmt.Errorf("sm-api: build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+jwt)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("sm-api: send request: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusOK {
		return "", c.parseError(resp)
	}
	var sr SecretResponse
	if err := json.NewDecoder(resp.Body).Decode(&sr); err != nil {
		return "", fmt.Errorf("sm-api: decode response: %w", err)
	}
	slog.DebugContext(ctx, "sm-api: secret created",
		"name", secretName,
		"secretReferenceName", sr.Spec.SecretReferenceName)
	return sr.Spec.SecretReferenceName, nil
}

func (c *Client) PatchSecret(ctx context.Context, location secretmanagersvc.SecretLocation, value []byte, metadata *secretmanagersvc.SecretMetadata) (string, error) {
	jwt := jwtassertion.GetJWTFromContext(ctx)
	if jwt == "" {
		return "", errors.New("sm-api: no JWT in context")
	}
	secretID, err := c.resolveSecretID(ctx, jwt, location)
	if err != nil {
		return "", fmt.Errorf("sm-api: resolve secret ID: %w", err)
	}

	var patchData map[string]any
	if err := json.Unmarshal(value, &patchData); err != nil {
		return "", fmt.Errorf("sm-api: unmarshal patch data: %w", err)
	}
	body, err := json.Marshal(PatchSecretRequest{Spec: PatchSecretSpecRequest{Data: patchData}})
	if err != nil {
		return "", fmt.Errorf("sm-api: marshal request: %w", err)
	}

	endpoint := fmt.Sprintf("%s/secrets/%s", c.baseURL, url.PathEscape(secretID))
	req, err := http.NewRequestWithContext(ctx, http.MethodPatch, endpoint, bytes.NewReader(body))
	if err != nil {
		return "", fmt.Errorf("sm-api: build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/merge-patch+json")
	req.Header.Set("Authorization", "Bearer "+jwt)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("sm-api: send request: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return "", secretmanagersvc.ErrSecretNotFound
	}
	if resp.StatusCode != http.StatusOK {
		return "", c.parseError(resp)
	}
	var sr SecretResponse
	if err := json.NewDecoder(resp.Body).Decode(&sr); err != nil {
		return "", fmt.Errorf("sm-api: decode response: %w", err)
	}
	return sr.Spec.SecretReferenceName, nil
}

func (c *Client) DeleteSecret(ctx context.Context, location secretmanagersvc.SecretLocation, _ *secretmanagersvc.SecretMetadata) error {
	jwt := jwtassertion.GetJWTFromContext(ctx)
	if jwt == "" {
		return errors.New("sm-api: no JWT in context")
	}
	secretID, err := c.resolveSecretID(ctx, jwt, location)
	if err != nil {
		if errors.Is(err, secretmanagersvc.ErrSecretNotFound) {
			return nil
		}
		return fmt.Errorf("sm-api: resolve secret ID: %w", err)
	}
	endpoint := fmt.Sprintf("%s/secrets/%s", c.baseURL, url.PathEscape(secretID))
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, endpoint, nil)
	if err != nil {
		return fmt.Errorf("sm-api: build request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+jwt)
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("sm-api: send request: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNoContent || resp.StatusCode == http.StatusNotFound {
		return nil
	}
	return c.parseError(resp)
}

func (c *Client) GetSecret(ctx context.Context, location secretmanagersvc.SecretLocation) (*secretmanagersvc.SecretInfo, error) {
	jwt := jwtassertion.GetJWTFromContext(ctx)
	if jwt == "" {
		return nil, errors.New("sm-api: no JWT in context")
	}
	secretID, err := c.resolveSecretID(ctx, jwt, location)
	if err != nil {
		return nil, err
	}
	endpoint := fmt.Sprintf("%s/secrets/%s", c.baseURL, url.PathEscape(secretID))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("sm-api: build request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+jwt)
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("sm-api: send request: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return nil, secretmanagersvc.ErrSecretNotFound
	}
	if resp.StatusCode != http.StatusOK {
		return nil, c.parseError(resp)
	}
	var sr SecretResponse
	if err := json.NewDecoder(resp.Body).Decode(&sr); err != nil {
		return nil, fmt.Errorf("sm-api: decode response: %w", err)
	}
	return &secretmanagersvc.SecretInfo{
		ID:        sr.Metadata.ID,
		Name:      sr.Metadata.Name,
		Keys:      sr.Spec.Keys,
		Labels:    sr.Metadata.Labels,
		CreatedAt: sr.Metadata.CreationTimestamp,
	}, nil
}

// GetSecretWithValue always returns ErrNotSupported — SM-API is WriteOnly.
func (c *Client) GetSecretWithValue(_ context.Context, _ secretmanagersvc.SecretLocation) ([]byte, error) {
	return nil, secretmanagersvc.ErrNotSupported
}

func (c *Client) Close(_ context.Context) error { return nil }

// resolveSecretID looks up an SM-API-generated secret ID by labels
// derived from the location. Needed because POST/PATCH/DELETE require
// the server-generated ID and the BFF only stores logical coordinates.
func (c *Client) resolveSecretID(ctx context.Context, jwt string, location secretmanagersvc.SecretLocation) (string, error) {
	selector := labelSelectorFromLocation(location)
	if selector == "" {
		return "", errors.New("sm-api: cannot resolve secret without any labels")
	}
	endpoint := fmt.Sprintf("%s/secrets?labelSelector=%s", c.baseURL, url.QueryEscape(selector))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return "", fmt.Errorf("sm-api: build list request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+jwt)
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("sm-api: list request: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", c.parseError(resp)
	}
	var list ListSecretsResponse
	if err := json.NewDecoder(resp.Body).Decode(&list); err != nil {
		return "", fmt.Errorf("sm-api: decode list response: %w", err)
	}
	if len(list.Items) == 0 {
		return "", secretmanagersvc.ErrSecretNotFound
	}
	return list.Items[0].Metadata.ID, nil
}

// buildLabelsFromLocation maps the asdlc SecretLocation onto the SM-API
// label set. Drops agent-manager's env/agent/config labels (no longer
// part of our shape); adds `task` so per-task ExternalSecrets are
// addressable.
func buildLabelsFromLocation(location secretmanagersvc.SecretLocation, existing map[string]string) map[string]string {
	labels := make(map[string]string, len(existing)+4)
	for k, v := range existing {
		labels[k] = v
	}
	if location.OrgName != "" {
		labels["org"] = location.OrgName
	}
	if location.ProjectName != "" {
		labels["project"] = location.ProjectName
	}
	if location.TaskID != "" {
		labels["task"] = location.TaskID
	}
	if location.EntityName != "" {
		labels["entity"] = location.EntityName
	}
	return labels
}

func labelSelectorFromLocation(location secretmanagersvc.SecretLocation) string {
	var parts []string
	if location.OrgName != "" {
		parts = append(parts, "org="+location.OrgName)
	}
	if location.ProjectName != "" {
		parts = append(parts, "project="+location.ProjectName)
	}
	if location.TaskID != "" {
		parts = append(parts, "task="+location.TaskID)
	}
	if location.EntityName != "" {
		parts = append(parts, "entity="+location.EntityName)
	}
	return strings.Join(parts, ",")
}

func sanitizeForK8sLabel(s string) string {
	s = strings.ReplaceAll(s, "/", "-")
	s = strings.Trim(s, "-")
	if len(s) > 63 {
		s = strings.TrimRight(s[:63], "-")
	}
	return s
}

func metadataLabels(m *secretmanagersvc.SecretMetadata) map[string]string {
	if m == nil {
		return nil
	}
	return m.Labels
}

func (c *Client) parseError(resp *http.Response) error {
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("sm-api: status %d (body read failed)", resp.StatusCode)
	}
	var er ErrorResponse
	if err := json.Unmarshal(body, &er); err != nil {
		return fmt.Errorf("sm-api: status %d: %s", resp.StatusCode, string(body))
	}
	msg := er.Error
	if msg == "" {
		msg = er.Message
	}
	if msg == "" {
		msg = string(body)
	}
	return fmt.Errorf("sm-api: status %d: %s", resp.StatusCode, msg)
}

// Register installs this provider in the secretmanagersvc registry under
// ProviderName. Callers invoke once at startup.
func Register(cfg Config) {
	secretmanagersvc.Register(ProviderName, NewProvider(cfg))
}
