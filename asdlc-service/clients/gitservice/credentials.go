package gitservice

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"time"
)

// ----------------------------------------------------------------------------
// Phase 2 PR B — internal credential routes proxied from the BFF.
// ----------------------------------------------------------------------------

// ConnectRequest is the body for POST /internal/credentials/orgs/{ocOrgId}.
type ConnectRequest struct {
	Kind           string `json:"kind"`
	InstallationID int64  `json:"installationId,omitempty"`
	PAT            string `json:"pat,omitempty"`
	GitHubLogin    string `json:"githubLogin,omitempty"`
}

// CredentialProjection is the JSON shape returned by status / connect /
// replace. Mirrors git-service's services.Projection. Never contains the
// token itself.
type CredentialProjection struct {
	OcOrgID           string     `json:"ocOrgId"`
	Kind              string     `json:"kind"`
	GitHubLogin       string     `json:"githubLogin"`
	IdentityName      string     `json:"identityName,omitempty"`
	IdentityEmail     string     `json:"identityEmail,omitempty"`
	IdentityLogin     string     `json:"identityLogin"`
	InstallationID    *int64     `json:"installationId,omitempty"`
	SelectedRepos     []string   `json:"selectedRepos,omitempty"`
	Status            string     `json:"status"`
	ConnectedAt       time.Time  `json:"connectedAt"`
	LastValidatedAt   *time.Time `json:"lastValidatedAt,omitempty"`
	IdentityChangedAt *time.Time `json:"identityChangedAt,omitempty"`
	PrevIdentityLogin *string    `json:"prevIdentityLogin,omitempty"`
}

// IdentityProjection is the response shape for
// GET /internal/credentials/orgs/{ocOrgId}/identity.
type IdentityProjection struct {
	Kind        string `json:"kind"`
	Name        string `json:"name"`
	Email       string `json:"email"`
	Login       string `json:"login"`
	GitHubLogin string `json:"githubLogin"`
}

// CredentialError carries the structured response body so the BFF can
// surface specific error codes to the UI (field-level validation errors
// from the connect chain).
type CredentialError struct {
	Status int
	Code   string
	Msg    string
}

func (e *CredentialError) Error() string {
	if e.Code != "" {
		return fmt.Sprintf("git-service [%d %s]: %s", e.Status, e.Code, e.Msg)
	}
	return fmt.Sprintf("git-service [%d]: %s", e.Status, e.Msg)
}

// IsConflict returns true for 409 (cross-mode change, mode-fixed).
func IsConflict(err error) bool {
	var ce *CredentialError
	return errors.As(err, &ce) && ce.Status == http.StatusConflict
}

// IsNotFound returns true for 404 (no row / no matching install).
func IsNotFound(err error) bool {
	var ce *CredentialError
	return errors.As(err, &ce) && ce.Status == http.StatusNotFound
}

// IsValidation returns true for 400 with a structured Code from the
// validation chain (PAT scope check, membership probe, etc.).
func IsValidation(err error) bool {
	var ce *CredentialError
	return errors.As(err, &ce) && ce.Status == http.StatusBadRequest && ce.Code != ""
}

// CreateOrReplaceCredential connects or replaces the per-org credential.
func (c *client) CreateOrReplaceCredential(ctx context.Context, ocOrgID string, req ConnectRequest) (*CredentialProjection, error) {
	body, _ := json.Marshal(req)
	url := fmt.Sprintf("%s/internal/credentials/orgs/%s", c.baseURL, ocOrgID)
	httpReq, _ := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	httpReq.Header.Set("Content-Type", "application/json")

	var proj CredentialProjection
	if err := c.doInternal(httpReq, &proj); err != nil {
		return nil, err
	}
	return &proj, nil
}

// GetCredentialProjection reads the /github status endpoint.
func (c *client) GetCredentialProjection(ctx context.Context, ocOrgID string) (*CredentialProjection, error) {
	url := fmt.Sprintf("%s/internal/credentials/orgs/%s", c.baseURL, ocOrgID)
	httpReq, _ := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)

	var proj CredentialProjection
	if err := c.doInternal(httpReq, &proj); err != nil {
		return nil, err
	}
	return &proj, nil
}

// GetCredentialIdentity returns the identity-only projection used by the
// dispatch path — the only non-secret view of the org's credential.
func (c *client) GetCredentialIdentity(ctx context.Context, ocOrgID string) (*IdentityProjection, error) {
	url := fmt.Sprintf("%s/internal/credentials/orgs/%s/identity", c.baseURL, ocOrgID)
	httpReq, _ := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)

	var ident IdentityProjection
	if err := c.doInternal(httpReq, &ident); err != nil {
		return nil, err
	}
	return &ident, nil
}

// DisconnectCredential runs Phase D (status flip + OpenBao GC).
func (c *client) DisconnectCredential(ctx context.Context, ocOrgID string) error {
	url := fmt.Sprintf("%s/internal/credentials/orgs/%s", c.baseURL, ocOrgID)
	httpReq, _ := http.NewRequestWithContext(ctx, http.MethodDelete, url, nil)

	return c.doInternal(httpReq, nil)
}

// UninstallAppInstallation runs Phase E — asks git-service to call GitHub's
// DELETE /app/installations/{id} so the install is removed from github.com
// alongside the platform-side row. Best-effort from the caller's POV; the
// disconnect cascade logs and continues on failure rather than rolling back.
func (c *client) UninstallAppInstallation(ctx context.Context, ocOrgID string) error {
	url := fmt.Sprintf("%s/internal/credentials/orgs/%s/uninstall", c.baseURL, ocOrgID)
	httpReq, _ := http.NewRequestWithContext(ctx, http.MethodPost, url, nil)

	return c.doInternal(httpReq, nil)
}

// GetWebhookSecrets fetches the accepted HMAC keys for ocOrgID. Used by
// the BFF's GitServiceSecretProvider in the webhook receiver pipeline.
func (c *client) GetWebhookSecrets(ctx context.Context, ocOrgID string) ([][]byte, error) {
	url := fmt.Sprintf("%s/internal/credentials/orgs/%s/webhook-secrets", c.baseURL, ocOrgID)
	httpReq, _ := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)

	var resp struct {
		Secrets []string `json:"secrets"`
	}
	if err := c.doInternal(httpReq, &resp); err != nil {
		return nil, err
	}
	out := make([][]byte, 0, len(resp.Secrets))
	for _, s := range resp.Secrets {
		out = append(out, []byte(s))
	}
	return out, nil
}

// OrgIDByInstallationID resolves an installation_id to its bound ocOrgId.
// 404 → returns ("", IsNotFound).
func (c *client) OrgIDByInstallationID(ctx context.Context, installationID int64) (string, error) {
	url := fmt.Sprintf("%s/internal/credentials/lookup/installation/%d", c.baseURL, installationID)
	httpReq, _ := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)

	var resp struct {
		OcOrgID string `json:"ocOrgId"`
	}
	if err := c.doInternal(httpReq, &resp); err != nil {
		return "", err
	}
	return resp.OcOrgID, nil
}

// OrgIDByRepoFullName resolves a "owner/repo" full name to its owning
// ocOrgId via the git_repositories table.
func (c *client) OrgIDByRepoFullName(ctx context.Context, fullName string) (string, error) {
	parts := splitRepoFullName(fullName)
	if len(parts) != 2 {
		return "", fmt.Errorf("invalid repo full name: %q", fullName)
	}
	url := fmt.Sprintf("%s/internal/credentials/lookup/repo/%s/%s", c.baseURL, parts[0], parts[1])
	httpReq, _ := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)

	var resp struct {
		OcOrgID string `json:"ocOrgId"`
	}
	if err := c.doInternal(httpReq, &resp); err != nil {
		return "", err
	}
	return resp.OcOrgID, nil
}

func splitRepoFullName(s string) []string {
	for i := 0; i < len(s); i++ {
		if s[i] == '/' {
			return []string{s[:i], s[i+1:]}
		}
	}
	return []string{s}
}

// SetInstallationStatus PATCHes the install row to suspended / active.
func (c *client) SetInstallationStatus(ctx context.Context, installationID int64, status string) error {
	body, _ := json.Marshal(map[string]string{"status": status})
	url := fmt.Sprintf("%s/internal/credentials/installations/%s/status", c.baseURL, strconv.FormatInt(installationID, 10))
	httpReq, _ := http.NewRequestWithContext(ctx, http.MethodPatch, url, bytes.NewReader(body))
	httpReq.Header.Set("Content-Type", "application/json")
	return c.doInternal(httpReq, nil)
}

// MergeInstallationRepos applies an installation_repositories merge.
func (c *client) MergeInstallationRepos(ctx context.Context, installationID int64, added, removed []string) error {
	body, _ := json.Marshal(map[string][]string{"added": added, "removed": removed})
	url := fmt.Sprintf("%s/internal/credentials/installations/%s/repos", c.baseURL, strconv.FormatInt(installationID, 10))
	httpReq, _ := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	httpReq.Header.Set("Content-Type", "application/json")
	return c.doInternal(httpReq, nil)
}

// GetInstallationRepositories asks git-service to fetch the install's
// current reach by calling GitHub directly (`GET /installation/repositories`).
// Used by Phase 2 PR D's reach-reconciliation Phase B (§6.8) to confirm
// GitHub agrees the install has shrunk before cascading tasks.
func (c *client) GetInstallationRepositories(ctx context.Context, installationID int64) ([]string, error) {
	url := fmt.Sprintf("%s/internal/credentials/installations/%s/repositories", c.baseURL, strconv.FormatInt(installationID, 10))
	httpReq, _ := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	var resp struct {
		Repositories []string `json:"repositories"`
	}
	if err := c.doInternal(httpReq, &resp); err != nil {
		return nil, err
	}
	return resp.Repositories, nil
}

// AppInstallationSummary mirrors the git-service-side type. Wire-shape
// is identical (camelCase JSON).
type AppInstallationSummary struct {
	InstallationID int64  `json:"installationId"`
	AccountLogin   string `json:"accountLogin"`
	AccountType    string `json:"accountType"`
}

// ErrAppBindNotConfigured surfaces when git-service can't run the OAuth-
// driven connect path — App private key or OAuth client_secret missing.
var ErrAppBindNotConfigured = errors.New("app bind not configured on git-service")

// ResolveUserInstallations exchanges an OAuth code for a user-token in
// git-service, intersects /user/installations with our App's installs,
// and returns candidates the user actually administers (filtered to
// drop installs bound to other ASDLC orgs). The user-token never crosses
// the BFF↔git-service boundary in either direction.
//
// Replaces the prior DiscoverUnboundInstalls + BindAppInstallation pair.
// The bind itself is done by calling CreateOrReplaceCredential after
// the BFF picks one of the returned candidates.
func (c *client) ResolveUserInstallations(ctx context.Context, ocOrgID, oauthCode, redirectURI string) ([]AppInstallationSummary, error) {
	body, _ := json.Marshal(map[string]string{
		"ocOrgId":     ocOrgID,
		"oauthCode":   oauthCode,
		"redirectUri": redirectURI,
	})
	url := fmt.Sprintf("%s/internal/credentials/app/resolve-user-installations", c.baseURL)
	httpReq, _ := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	httpReq.Header.Set("Content-Type", "application/json")
	var resp struct {
		Candidates []AppInstallationSummary `json:"candidates"`
	}
	if err := c.doInternal(httpReq, &resp); err != nil {
		var ce *CredentialError
		if errors.As(err, &ce) && ce.Code == "app_bind_not_configured" {
			return nil, ErrAppBindNotConfigured
		}
		return nil, err
	}
	return resp.Candidates, nil
}

// ----------------------------------------------------------------------------
// Build-credential staging (replaces mint-build).
// ----------------------------------------------------------------------------

// StageResult is the response from POST
// /internal/credentials/orgs/{ocOrgId}/stage-build-secret. Returns only the
// K8s Secret name git-service materialised in workflows-<ocOrgID>; the
// token itself never crosses the BFF↔git-service boundary. The Secret name
// matches the upstream `dockerfile-builder` ClusterWorkflow's expected
// default (`${metadata.workflowRunName}-git-secret`) so the BFF doesn't
// need to thread it onto the WorkflowRun spec.
type StageResult struct {
	SecretName string `json:"secretName"`
}

// Errors with stable codes the build pipeline reacts to.
var (
	ErrRepoNotInOrg    = errors.New("stage-build-secret: repo not in org")
	ErrOrgDisconnected = errors.New("stage-build-secret: org disconnected")
)

// StageBuildSecret asks git-service to pre-stage a per-WorkflowRun
// `kubernetes.io/basic-auth` Secret named `<workflowRunName>-git-secret`
// in workflows-<ocOrgID>, carrying the org's GitHub credential. Called by
// the dispatch path immediately before POSTing the WorkflowRun. See
// docs/design/build-credential-injection.md.
func (c *client) StageBuildSecret(ctx context.Context, ocOrgID, repoSlug, workflowRunName string) (*StageResult, error) {
	body, _ := json.Marshal(map[string]string{
		"repoSlug":        repoSlug,
		"workflowRunName": workflowRunName,
	})
	url := fmt.Sprintf("%s/internal/credentials/orgs/%s/stage-build-secret", c.baseURL, ocOrgID)
	httpReq, _ := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	httpReq.Header.Set("Content-Type", "application/json")

	var res StageResult
	if err := c.doInternal(httpReq, &res); err != nil {
		var ce *CredentialError
		if errors.As(err, &ce) {
			switch ce.Code {
			case "repo_not_in_org":
				return nil, fmt.Errorf("%w: %s", ErrRepoNotInOrg, ce.Msg)
			case "org_disconnected":
				return nil, fmt.Errorf("%w: %s", ErrOrgDisconnected, ce.Msg)
			}
		}
		return nil, err
	}
	return &res, nil
}

// doInternal executes the request and decodes the response into out (if
// non-nil). Wraps non-2xx into CredentialError with the structured
// {error, code} body so call sites can route by status + code.
func (c *client) doInternal(req *http.Request, out any) error {
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("git-service request: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)

	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		if out == nil || len(body) == 0 {
			return nil
		}
		if err := json.Unmarshal(body, out); err != nil {
			return fmt.Errorf("decode: %w", err)
		}
		return nil
	}

	// Try to extract {error, code} from the body.
	var ferr struct {
		Error string `json:"error"`
		Code  string `json:"code"`
	}
	_ = json.Unmarshal(body, &ferr)
	if ferr.Error == "" {
		ferr.Error = string(body)
	}
	return &CredentialError{Status: resp.StatusCode, Code: ferr.Code, Msg: ferr.Error}
}
