package gitservice

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/wso2/asdlc/asdlc-service/clients/auth"
	"github.com/wso2/asdlc/asdlc-service/clients/httpx"
)

// ErrArtifactNotFound is returned by the artifact endpoints when the working
// tree file does not exist (404). Spec and design services branch on this
// rather than checking for empty content.
var ErrArtifactNotFound = errors.New("artifact not found")

// Client calls the git-service.
//
// PR 0 of the repo-storage-ownership refactor introduced an orgId-prefixed
// path shape (`/api/v1/repos/{orgId}/{projectId}/...`); every call here that
// targets a specific repo now takes (orgID, projectID) and constructs the
// URL accordingly. CreateRepo is the one exception — it has no path
// projectId and the body already carries (orgId, projectId).
type Client interface {
	InitProjectComponents(ctx context.Context, req *CreateRepoRequest) (*RepoInfo, error)
	GetRepo(ctx context.Context, orgID, projectID string) (*RepoInfo, error)
	DeleteRepo(ctx context.Context, orgID, projectID string) error
	Commit(ctx context.Context, orgID, projectID string, req *CommitRequest) (*CommitResult, error)
	Push(ctx context.Context, orgID, projectID string) error
	Pull(ctx context.Context, orgID, projectID string, branch string) error
	CreateTag(ctx context.Context, orgID, projectID string, req *CreateTagRequest) (*TagResult, error)
	ListTags(ctx context.Context, orgID, projectID string, prefix string) ([]TagInfo, error)
	GetFileAtTag(ctx context.Context, orgID, projectID string, tag string, filePath string) (string, error)
	CreateIssue(ctx context.Context, orgID, projectID string, req *CreateIssueRequest) (*IssueResult, error)
	ListIssues(ctx context.Context, orgID, projectID string, labels []string) ([]IssueInfo, error)
	CloseIssue(ctx context.Context, orgID, projectID string, number int, comment string) error
	CommentIssue(ctx context.Context, orgID, projectID string, number int, body string) error
	EditIssueBody(ctx context.Context, orgID, projectID string, number int, body string) error

	// Phase 0 additions:
	CreateBranch(ctx context.Context, orgID, projectID, branch, fromRef string) (string, error)
	// SeedBranchCommit creates or updates a placeholder file on the given
	// branch so the branch has at least one commit beyond base. Required
	// before opening a draft PR (GitHub rejects PRs whose head and base are
	// at the same SHA).
	SeedBranchCommit(ctx context.Context, orgID, projectID, branch, path, message, content string) error
	CreateDraftPR(ctx context.Context, orgID, projectID string, req *CreateDraftPRRequest) (*PullRequestResult, error)
	RegisterWebhook(ctx context.Context, orgID, projectID string) (*RegisterWebhookResponse, error)
	DeregisterWebhook(ctx context.Context, orgID, projectID string) error

	// Internal credential routes (gated by Service JWT; same auth as the
	// /api/v1/* routes — the /internal/ prefix is just a path convention).
	CreateOrReplaceCredential(ctx context.Context, ocOrgID string, req ConnectRequest) (*CredentialProjection, error)
	GetCredentialProjection(ctx context.Context, ocOrgID string) (*CredentialProjection, error)
	GetCredentialIdentity(ctx context.Context, ocOrgID string) (*IdentityProjection, error)
	DisconnectCredential(ctx context.Context, ocOrgID string) error
	UninstallAppInstallation(ctx context.Context, ocOrgID string) error
	GetWebhookSecrets(ctx context.Context, ocOrgID string) ([][]byte, error)
	OrgIDByInstallationID(ctx context.Context, installationID int64) (string, error)
	OrgIDByRepoFullName(ctx context.Context, fullName string) (string, error)
	SetInstallationStatus(ctx context.Context, installationID int64, status string) error
	MergeInstallationRepos(ctx context.Context, installationID int64, added, removed []string) error
	// Phase 2 PR D — reach reconciliation Phase B (§6.8) calls back to
	// GitHub via git-service to confirm an install's current repo list
	// before cascading tasks targeting a removed repo.
	GetInstallationRepositories(ctx context.Context, installationID int64) ([]string, error)

	// ResolveUserInstallations is the only install-discovery surface.
	// Exchanges an OAuth code for a user-token (inside git-service; never
	// returned), intersects /user/installations with our App's installs,
	// and returns candidates the user actually administers. The BFF picks
	// one (1-candidate auto, 2+ via picker) and binds via the existing
	// CreateOrReplaceCredential.
	ResolveUserInstallations(ctx context.Context, ocOrgID, oauthCode, redirectURI string) ([]AppInstallationSummary, error)

	// Phase 2 PR C — build credentials (mint-build).
	MintBuildToken(ctx context.Context, ocOrgID, repoSlug string) (*MintResult, error)

	// Anthropic credentials — see docs/design/anthropic-key-dual-token.md.
	// Connect/Status/Disconnect mirror the GitHub-credentials surface;
	// ApplyAnthropicWPSecret is the per-dispatch SSA refresh.
	CreateOrReplaceAnthropic(ctx context.Context, ocOrgID string, req AnthropicConnectRequest) (*AnthropicProjection, error)
	GetAnthropicProjection(ctx context.Context, ocOrgID string) (*AnthropicProjection, error)
	DisconnectAnthropic(ctx context.Context, ocOrgID string) error
	ApplyAnthropicWPSecret(ctx context.Context, ocOrgID string) (*ApplyAnthropicWPSecretResult, error)

	// ----- Artifact endpoints -----
	//
	// Requirements live as a multi-file directory (`.asdlc/requirements/*.md`)
	// versioned together as `v<N>` tags. Design is a single file
	// (`.asdlc/design.json`) versioned as `v<N>-<M>` where N is the source
	// requirements version. The save endpoint takes no body — content is
	// staged from the working tree, populated via per-file PUT/DELETE.

	// Requirements
	ListRequirements(ctx context.Context, orgID, projectID string) (map[string]string, error)
	GetRequirementFile(ctx context.Context, orgID, projectID, name string) (*ArtifactFile, error)
	PutRequirementFile(ctx context.Context, orgID, projectID, name string, req PutFileRequest) (*PutResult, error)
	DeleteRequirementFile(ctx context.Context, orgID, projectID, name string) error
	SaveRequirements(ctx context.Context, orgID, projectID string, req SaveArtifactRequest) (*RequirementsSaveResult, error)
	DiscardRequirements(ctx context.Context, orgID, projectID string) (map[string]string, error)
	ListRequirementsVersions(ctx context.Context, orgID, projectID string) ([]RequirementsVersionInfo, error)
	GetRequirementsAtTag(ctx context.Context, orgID, projectID, tag string) (map[string]string, error)

	// Design
	GetDesign(ctx context.Context, orgID, projectID string) (*ArtifactFile, error)
	PutDesign(ctx context.Context, orgID, projectID string, req PutFileRequest) (*PutResult, error)
	SaveDesign(ctx context.Context, orgID, projectID string, req SaveArtifactRequest) (*DesignSaveResult, error)
	DiscardDesign(ctx context.Context, orgID, projectID string) (*ArtifactFile, error)
	ListDesignVersions(ctx context.Context, orgID, projectID string) ([]DesignVersionInfo, error)
	GetDesignAtTag(ctx context.Context, orgID, projectID, tag string) (*ArtifactFile, error)

	// Board operations — GitHub Project v2.
	GetBoard(ctx context.Context, projectID string) (*ProjectBoard, error)
	MoveIssueToStatus(ctx context.Context, projectID, issueURL, targetStatus string) error
}

// ----- Artifact wire shapes (mirrors git-service/services/artifact_service.go) -----

type ArtifactFile struct {
	Content string `json:"content"`
	SHA     string `json:"sha"`
}

type PutFileRequest struct {
	Content string `json:"content"`
	IfMatch string `json:"ifMatch,omitempty"`
}

type PutResult struct {
	SHA string `json:"sha"`
}

// SaveArtifactRequest carries an optional commit message; the file content
// is read from the working tree (populated via per-file PUTs / DELETEs).
type SaveArtifactRequest struct {
	Message string `json:"message,omitempty"`
}

type RequirementsSaveResult struct {
	Status     string `json:"status"`
	Tag        string `json:"tag"`
	Version    int    `json:"version"`
	CommitHash string `json:"commitHash,omitempty"`
}

type DesignSaveResult struct {
	Status              string `json:"status"`
	Tag                 string `json:"tag"`
	RequirementsVersion int    `json:"requirementsVersion"`
	DesignRevision      int    `json:"designRevision"`
	CommitHash          string `json:"commitHash,omitempty"`
}

type RequirementsVersionInfo struct {
	Tag        string `json:"tag"`
	Version    int    `json:"version"`
	CommitHash string `json:"commitHash"`
	Message    string `json:"message"`
}

type DesignVersionInfo struct {
	Tag                 string `json:"tag"`
	RequirementsVersion int    `json:"requirementsVersion"`
	DesignRevision      int    `json:"designRevision"`
	CommitHash          string `json:"commitHash"`
	Message             string `json:"message"`
}

// CreateDraftPRRequest is the request to open a draft pull request.
type CreateDraftPRRequest struct {
	Title string `json:"title"`
	Body  string `json:"body"`
	Head  string `json:"head"`
	Base  string `json:"base"`
}

// PullRequestResult is the GitHub PR metadata returned after creation.
type PullRequestResult struct {
	Number int    `json:"number"`
	URL    string `json:"html_url"`
}

// RegisterWebhookResponse describes the strategy git-service used. In Phase
// 0 the strategy is always "per-repo" with a HookID; Phase 2 App-mode
// returns "platform" with a nil HookID.
type RegisterWebhookResponse struct {
	HookID   *int64 `json:"hookId,omitempty"`
	Strategy string `json:"strategy"`
}

// CreateRepoRequest is sent to the git-service to provision a new GitHub repo and clone it.
type CreateRepoRequest struct {
	OrgID       string `json:"orgId"`
	ProjectID   string `json:"projectId"`
	ProjectName string `json:"projectName"`
}

// RepoInfo is the git repository information returned by the git-service.
type RepoInfo struct {
	ID              string  `json:"id"`
	OrgID           string  `json:"orgId"`
	ProjectID       string  `json:"projectId"`
	RepoURL         string  `json:"repoUrl"`
	ClonePath       string  `json:"clonePath"`
	DefaultBranch   string  `json:"defaultBranch"`
	Status          string  `json:"status"`
	ErrorMessage    string  `json:"errorMessage,omitempty"`
	OcSecretRefName *string `json:"ocSecretRefName,omitempty"`
	RepoSlug        string  `json:"repoSlug,omitempty"`
	CreatedAt       string  `json:"createdAt"`
	UpdatedAt       string  `json:"updatedAt"`
}

// CommitRequest describes a git commit operation.
type CommitRequest struct {
	Message     string   `json:"message"`
	Files       []string `json:"files,omitempty"`
	Directory   string   `json:"directory,omitempty"` // stage all changes under this dir
	AuthorName  string   `json:"authorName"`
	AuthorEmail string   `json:"authorEmail"`
}

// CommitResult is the result of a commit.
type CommitResult struct {
	CommitHash     string   `json:"commitHash"`
	Message        string   `json:"message"`
	FilesCommitted []string `json:"filesCommitted"`
}

// CreateTagRequest describes a git tag operation.
type CreateTagRequest struct {
	TagName string `json:"tagName"`
	Message string `json:"message"`
}

// TagResult is the result of creating a tag.
type TagResult struct {
	TagName    string `json:"tagName"`
	CommitHash string `json:"commitHash"`
}

// TagInfo describes a git tag.
type TagInfo struct {
	Name       string `json:"name"`
	CommitHash string `json:"commitHash"`
	Message    string `json:"message,omitempty"`
}

// CreateIssueRequest describes a GitHub issue to create on the project's repo.
type CreateIssueRequest struct {
	Title  string   `json:"title"`
	Body   string   `json:"body"`
	Labels []string `json:"labels,omitempty"`
}

// IssueResult is the result of creating a GitHub issue.
type IssueResult struct {
	Number int    `json:"number"`
	URL    string `json:"url"`
}

// IssueInfo represents a GitHub issue returned when listing.
type IssueInfo struct {
	Number int      `json:"number"`
	Title  string   `json:"title"`
	Body   string   `json:"body"`
	URL    string   `json:"url"`
	State  string   `json:"state"`
	Labels []string `json:"labels"`
}

// LabelInfo holds a GitHub label's name and hex color (without the leading #).
type LabelInfo struct {
	Name  string `json:"name"`
	Color string `json:"color"`
}

// BoardItem is a single item on the GitHub Project v2 board.
type BoardItem struct {
	ID       string      `json:"id"`
	Title    string      `json:"title"`
	URL      string      `json:"url"`
	Body     string      `json:"body,omitempty"`
	Assignee string      `json:"assignee,omitempty"`
	Labels   []LabelInfo `json:"labels,omitempty"`
	Status   string      `json:"status"`
}

// ProjectBoard holds board items as returned by the git-service.
type ProjectBoard struct {
	URL   string      `json:"url"`
	Items []BoardItem `json:"items"`
}

type client struct {
	baseURL    string
	httpClient *http.Client
}

// NewClient builds a gitservice client. provider attaches a Service JWT
// to every outbound call (audience: git-service); pass nil to disable
// service-auth in tests/dev.
func NewClient(baseURL string, provider *auth.AuthProvider) Client {
	return &client{
		baseURL: baseURL,
		httpClient: &http.Client{
			Timeout:   2 * time.Minute,
			Transport: httpx.ServiceTransport(provider),
		},
	}
}

func (c *client) InitProjectComponents(ctx context.Context, req *CreateRepoRequest) (*RepoInfo, error) {
	body, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	url := c.baseURL + "/api/v1/orgs"
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("git-service request: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}

	// CreateRepo is idempotent on (orgID, projectID): git-service returns 200
	// with the existing row when the repo is already provisioned, and 201 on
	// first creation. Both are success.
	if resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("git-service error (status %d): %s", resp.StatusCode, string(respBody))
	}

	// git-service wraps the repo row in {"projectId": "<gh project node id>",
	// "repo": {<GitRepository>}} (org-neutrality refactor). Decode the
	// wrapper, then take the nested repo as the canonical RepoInfo —
	// otherwise OcSecretRefName / RepoSlug / RepoURL all end up empty and
	// the downstream SecretReference creation silently no-ops.
	var wrapper struct {
		ProjectID string    `json:"projectId"`
		Repo      *RepoInfo `json:"repo"`
	}
	if err := json.Unmarshal(respBody, &wrapper); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}
	if wrapper.Repo == nil {
		return nil, fmt.Errorf("git-service init response missing 'repo' field; body=%s", string(respBody))
	}
	return wrapper.Repo, nil
}

// repoURL builds the orgId-scoped path that PR 0 introduced. All per-repo
// calls flow through this helper so the (orgId, projectId) shape is
// consistent across methods.
func (c *client) repoURL(orgID, projectID string) string {
	return fmt.Sprintf("%s/api/v1/repos/%s/%s", c.baseURL, orgID, projectID)
}

func (c *client) GetRepo(ctx context.Context, orgID, projectID string) (*RepoInfo, error) {
	url := c.repoURL(orgID, projectID)
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("git-service request: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}

	if resp.StatusCode == http.StatusNotFound {
		return nil, nil
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("git-service error (status %d): %s", resp.StatusCode, string(respBody))
	}

	var result RepoInfo
	if err := json.Unmarshal(respBody, &result); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}

	return &result, nil
}

func (c *client) DeleteRepo(ctx context.Context, orgID, projectID string) error {
	url := c.repoURL(orgID, projectID)
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodDelete, url, nil)
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return fmt.Errorf("git-service request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		return nil // idempotent delete
	}

	if resp.StatusCode != http.StatusNoContent {
		respBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("git-service error (status %d): %s", resp.StatusCode, string(respBody))
	}

	return nil
}

func (c *client) Commit(ctx context.Context, orgID, projectID string, req *CommitRequest) (*CommitResult, error) {
	body, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	url := c.repoURL(orgID, projectID) + "/commit"
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("git-service request: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("git-service error (status %d): %s", resp.StatusCode, string(respBody))
	}

	var result CommitResult
	if err := json.Unmarshal(respBody, &result); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}

	return &result, nil
}

func (c *client) Push(ctx context.Context, orgID, projectID string) error {
	url := c.repoURL(orgID, projectID) + "/push"
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader([]byte("{}")))
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return fmt.Errorf("git-service request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("git-service error (status %d): %s", resp.StatusCode, string(respBody))
	}

	return nil
}

func (c *client) Pull(ctx context.Context, orgID, projectID string, branch string) error {
	body := struct {
		Branch string `json:"branch"`
	}{Branch: branch}

	data, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("marshal request: %w", err)
	}

	url := c.repoURL(orgID, projectID) + "/pull"
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(data))
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return fmt.Errorf("git-service request: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("git-service error (status %d): %s", resp.StatusCode, string(respBody))
	}

	return nil
}

func (c *client) CreateTag(ctx context.Context, orgID, projectID string, req *CreateTagRequest) (*TagResult, error) {
	body, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	url := c.repoURL(orgID, projectID) + "/tags"
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("git-service request: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}

	if resp.StatusCode != http.StatusCreated {
		return nil, fmt.Errorf("git-service error (status %d): %s", resp.StatusCode, string(respBody))
	}

	var tagResult TagResult
	if err := json.Unmarshal(respBody, &tagResult); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}

	return &tagResult, nil
}

func (c *client) ListTags(ctx context.Context, orgID, projectID string, prefix string) ([]TagInfo, error) {
	url := fmt.Sprintf("%s/tags?prefix=%s", c.repoURL(orgID, projectID), prefix)
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("git-service request: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("git-service error (status %d): %s", resp.StatusCode, string(respBody))
	}

	var tags []TagInfo
	if err := json.Unmarshal(respBody, &tags); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}

	return tags, nil
}

func (c *client) GetFileAtTag(ctx context.Context, orgID, projectID string, tag string, filePath string) (string, error) {
	url := fmt.Sprintf("%s/tags/%s/file?path=%s", c.repoURL(orgID, projectID), tag, filePath)
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", fmt.Errorf("create request: %w", err)
	}

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return "", fmt.Errorf("git-service request: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("read response: %w", err)
	}

	if resp.StatusCode == http.StatusNotFound {
		return "", fmt.Errorf("file not found at tag %s: %s", tag, filePath)
	}

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("git-service error (status %d): %s", resp.StatusCode, string(respBody))
	}

	var fileResult struct {
		Content string `json:"content"`
	}
	if err := json.Unmarshal(respBody, &fileResult); err != nil {
		return "", fmt.Errorf("decode response: %w", err)
	}

	return fileResult.Content, nil
}

func (c *client) CreateIssue(ctx context.Context, orgID, projectID string, req *CreateIssueRequest) (*IssueResult, error) {
	body, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	url := c.repoURL(orgID, projectID) + "/issues"
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("git-service request: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}

	if resp.StatusCode != http.StatusCreated {
		return nil, fmt.Errorf("git-service error (status %d): %s", resp.StatusCode, string(respBody))
	}

	var result IssueResult
	if err := json.Unmarshal(respBody, &result); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}

	return &result, nil
}

func (c *client) CloseIssue(ctx context.Context, orgID, projectID string, number int, comment string) error {
	body, err := json.Marshal(map[string]string{"comment": comment})
	if err != nil {
		return fmt.Errorf("marshal request: %w", err)
	}

	url := fmt.Sprintf("%s/issues/%d/close", c.repoURL(orgID, projectID), number)
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return fmt.Errorf("git-service request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("git-service error (status %d): %s", resp.StatusCode, string(respBody))
	}
	return nil
}

func (c *client) CommentIssue(ctx context.Context, orgID, projectID string, number int, commentBody string) error {
	body, err := json.Marshal(map[string]string{"body": commentBody})
	if err != nil {
		return fmt.Errorf("marshal request: %w", err)
	}

	url := fmt.Sprintf("%s/issues/%d/comments", c.repoURL(orgID, projectID), number)
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return fmt.Errorf("git-service request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusCreated {
		respBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("git-service error (status %d): %s", resp.StatusCode, string(respBody))
	}
	return nil
}

func (c *client) EditIssueBody(ctx context.Context, orgID, projectID string, number int, issueBody string) error {
	body, err := json.Marshal(map[string]string{"body": issueBody})
	if err != nil {
		return fmt.Errorf("marshal request: %w", err)
	}
	url := fmt.Sprintf("%s/issues/%d/body", c.repoURL(orgID, projectID), number)
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPatch, url, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return fmt.Errorf("git-service request: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("git-service error (status %d): %s", resp.StatusCode, string(respBody))
	}
	return nil
}

type createBranchReq struct {
	Branch  string `json:"branch"`
	FromRef string `json:"fromRef,omitempty"`
}
type createBranchResp struct {
	Branch string `json:"branch"`
	SHA    string `json:"sha"`
}

func (c *client) CreateBranch(ctx context.Context, orgID, projectID, branch, fromRef string) (string, error) {
	body, err := json.Marshal(createBranchReq{Branch: branch, FromRef: fromRef})
	if err != nil {
		return "", fmt.Errorf("marshal request: %w", err)
	}
	url := c.repoURL(orgID, projectID) + "/branches"
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return "", fmt.Errorf("create request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return "", fmt.Errorf("git-service request: %w", err)
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusCreated {
		return "", fmt.Errorf("git-service error (status %d): %s", resp.StatusCode, string(respBody))
	}
	var r createBranchResp
	if err := json.Unmarshal(respBody, &r); err != nil {
		return "", fmt.Errorf("decode response: %w", err)
	}
	return r.SHA, nil
}

type seedCommitReq struct {
	Branch  string `json:"branch"`
	Path    string `json:"path"`
	Message string `json:"message"`
	Content string `json:"content"`
}

func (c *client) SeedBranchCommit(ctx context.Context, orgID, projectID, branch, path, message, content string) error {
	body, err := json.Marshal(seedCommitReq{Branch: branch, Path: path, Message: message, Content: content})
	if err != nil {
		return fmt.Errorf("marshal request: %w", err)
	}
	url := c.repoURL(orgID, projectID) + "/branches/seed-commit"
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return fmt.Errorf("git-service request: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("git-service error (status %d): %s", resp.StatusCode, string(respBody))
	}
	return nil
}

func (c *client) CreateDraftPR(ctx context.Context, orgID, projectID string, req *CreateDraftPRRequest) (*PullRequestResult, error) {
	body, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}
	url := c.repoURL(orgID, projectID) + "/pulls"
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("git-service request: %w", err)
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusCreated {
		return nil, fmt.Errorf("git-service error (status %d): %s", resp.StatusCode, string(respBody))
	}
	var pr PullRequestResult
	if err := json.Unmarshal(respBody, &pr); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}
	return &pr, nil
}

func (c *client) RegisterWebhook(ctx context.Context, orgID, projectID string) (*RegisterWebhookResponse, error) {
	url := c.repoURL(orgID, projectID) + "/webhooks"
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader([]byte("{}")))
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("git-service request: %w", err)
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusCreated {
		return nil, fmt.Errorf("git-service error (status %d): %s", resp.StatusCode, string(respBody))
	}
	var r RegisterWebhookResponse
	if err := json.Unmarshal(respBody, &r); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}
	return &r, nil
}

func (c *client) DeregisterWebhook(ctx context.Context, orgID, projectID string) error {
	url := c.repoURL(orgID, projectID) + "/webhooks"
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodDelete, url, nil)
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}
	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return fmt.Errorf("git-service request: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent && resp.StatusCode != http.StatusNotFound {
		respBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("git-service error (status %d): %s", resp.StatusCode, string(respBody))
	}
	return nil
}

func (c *client) ListIssues(ctx context.Context, orgID, projectID string, labels []string) ([]IssueInfo, error) {
	url := c.repoURL(orgID, projectID) + "/issues"
	if len(labels) > 0 {
		url += "?labels=" + strings.Join(labels, ",")
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("git-service request: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("git-service error (status %d): %s", resp.StatusCode, string(respBody))
	}

	var issues []IssueInfo
	if err := json.Unmarshal(respBody, &issues); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}

	return issues, nil
}

// ----- Artifact endpoint implementations -----

func (c *client) artifactURL(orgID, projectID, kind string) string {
	return fmt.Sprintf("%s/artifacts/%s", c.repoURL(orgID, projectID), kind)
}

func (c *client) doJSON(ctx context.Context, method, url string, body any, out any, okStatus int) (int, error) {
	var reader io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return 0, fmt.Errorf("marshal: %w", err)
		}
		reader = bytes.NewReader(data)
	}
	req, err := http.NewRequestWithContext(ctx, method, url, reader)
	if err != nil {
		return 0, fmt.Errorf("create request: %w", err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return 0, fmt.Errorf("git-service request: %w", err)
	}
	defer resp.Body.Close()
	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return resp.StatusCode, fmt.Errorf("read response: %w", err)
	}
	if resp.StatusCode == http.StatusNotFound {
		return resp.StatusCode, ErrArtifactNotFound
	}
	if resp.StatusCode != okStatus {
		return resp.StatusCode, fmt.Errorf("git-service error (status %d): %s", resp.StatusCode, string(respBody))
	}
	if out != nil && len(respBody) > 0 {
		if err := json.Unmarshal(respBody, out); err != nil {
			return resp.StatusCode, fmt.Errorf("decode response: %w", err)
		}
	}
	return resp.StatusCode, nil
}

// Requirements (multi-file directory under .asdlc/requirements/, tagged v<N>)

type requirementsListResp struct {
	Files map[string]string `json:"files"`
}

type requirementsVersionResp struct {
	Tag     string            `json:"tag"`
	Version int               `json:"version"`
	Files   map[string]string `json:"files"`
}

func (c *client) ListRequirements(ctx context.Context, orgID, projectID string) (map[string]string, error) {
	url := c.artifactURL(orgID, projectID, "requirements")
	var out requirementsListResp
	if _, err := c.doJSON(ctx, http.MethodGet, url, nil, &out, http.StatusOK); err != nil {
		return nil, err
	}
	if out.Files == nil {
		out.Files = map[string]string{}
	}
	return out.Files, nil
}

func (c *client) GetRequirementFile(ctx context.Context, orgID, projectID, name string) (*ArtifactFile, error) {
	url := fmt.Sprintf("%s/files/%s", c.artifactURL(orgID, projectID, "requirements"), name)
	var out ArtifactFile
	if _, err := c.doJSON(ctx, http.MethodGet, url, nil, &out, http.StatusOK); err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *client) PutRequirementFile(ctx context.Context, orgID, projectID, name string, req PutFileRequest) (*PutResult, error) {
	url := fmt.Sprintf("%s/files/%s", c.artifactURL(orgID, projectID, "requirements"), name)
	var out PutResult
	if _, err := c.doJSON(ctx, http.MethodPut, url, req, &out, http.StatusOK); err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *client) DeleteRequirementFile(ctx context.Context, orgID, projectID, name string) error {
	url := fmt.Sprintf("%s/files/%s", c.artifactURL(orgID, projectID, "requirements"), name)
	if _, err := c.doJSON(ctx, http.MethodDelete, url, nil, nil, http.StatusNoContent); err != nil {
		return err
	}
	return nil
}

func (c *client) SaveRequirements(ctx context.Context, orgID, projectID string, req SaveArtifactRequest) (*RequirementsSaveResult, error) {
	url := c.artifactURL(orgID, projectID, "requirements") + "/save"
	var out RequirementsSaveResult
	if _, err := c.doJSON(ctx, http.MethodPost, url, req, &out, http.StatusOK); err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *client) DiscardRequirements(ctx context.Context, orgID, projectID string) (map[string]string, error) {
	url := c.artifactURL(orgID, projectID, "requirements") + "/discard"
	var out requirementsListResp
	if _, err := c.doJSON(ctx, http.MethodPost, url, nil, &out, http.StatusOK); err != nil {
		return nil, err
	}
	if out.Files == nil {
		out.Files = map[string]string{}
	}
	return out.Files, nil
}

func (c *client) ListRequirementsVersions(ctx context.Context, orgID, projectID string) ([]RequirementsVersionInfo, error) {
	url := c.artifactURL(orgID, projectID, "requirements") + "/versions"
	var out []RequirementsVersionInfo
	if _, err := c.doJSON(ctx, http.MethodGet, url, nil, &out, http.StatusOK); err != nil {
		return nil, err
	}
	return out, nil
}

func (c *client) GetRequirementsAtTag(ctx context.Context, orgID, projectID, tag string) (map[string]string, error) {
	url := fmt.Sprintf("%s/versions/%s", c.artifactURL(orgID, projectID, "requirements"), tag)
	var out requirementsVersionResp
	if _, err := c.doJSON(ctx, http.MethodGet, url, nil, &out, http.StatusOK); err != nil {
		return nil, err
	}
	if out.Files == nil {
		out.Files = map[string]string{}
	}
	return out.Files, nil
}

// Design (single file at .asdlc/design.json, tagged v<N>-<M>)

func (c *client) GetDesign(ctx context.Context, orgID, projectID string) (*ArtifactFile, error) {
	var out ArtifactFile
	if _, err := c.doJSON(ctx, http.MethodGet, c.artifactURL(orgID, projectID, "design"), nil, &out, http.StatusOK); err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *client) PutDesign(ctx context.Context, orgID, projectID string, req PutFileRequest) (*PutResult, error) {
	var out PutResult
	if _, err := c.doJSON(ctx, http.MethodPut, c.artifactURL(orgID, projectID, "design"), req, &out, http.StatusOK); err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *client) SaveDesign(ctx context.Context, orgID, projectID string, req SaveArtifactRequest) (*DesignSaveResult, error) {
	url := c.artifactURL(orgID, projectID, "design") + "/save"
	var out DesignSaveResult
	if _, err := c.doJSON(ctx, http.MethodPost, url, req, &out, http.StatusOK); err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *client) DiscardDesign(ctx context.Context, orgID, projectID string) (*ArtifactFile, error) {
	url := c.artifactURL(orgID, projectID, "design") + "/discard"
	var out ArtifactFile
	if _, err := c.doJSON(ctx, http.MethodPost, url, nil, &out, http.StatusOK); err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *client) ListDesignVersions(ctx context.Context, orgID, projectID string) ([]DesignVersionInfo, error) {
	url := c.artifactURL(orgID, projectID, "design") + "/versions"
	var out []DesignVersionInfo
	if _, err := c.doJSON(ctx, http.MethodGet, url, nil, &out, http.StatusOK); err != nil {
		return nil, err
	}
	return out, nil
}

func (c *client) GetDesignAtTag(ctx context.Context, orgID, projectID, tag string) (*ArtifactFile, error) {
	url := fmt.Sprintf("%s/versions/%s", c.artifactURL(orgID, projectID, "design"), tag)
	var out ArtifactFile
	if _, err := c.doJSON(ctx, http.MethodGet, url, nil, &out, http.StatusOK); err != nil {
		return nil, err
	}
	return &out, nil
}

// Board

func (c *client) GetBoard(ctx context.Context, projectID string) (*ProjectBoard, error) {
	url := fmt.Sprintf("%s/api/v1/repos/%s/board", c.baseURL, projectID)
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("git-service request: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("git-service error (status %d): %s", resp.StatusCode, string(respBody))
	}

	var result ProjectBoard
	if err := json.Unmarshal(respBody, &result); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}

	return &result, nil
}

func (c *client) MoveIssueToStatus(ctx context.Context, projectID, issueURL, targetStatus string) error {
	body, err := json.Marshal(map[string]string{"issueUrl": issueURL, "targetStatus": targetStatus})
	if err != nil {
		return fmt.Errorf("marshal request: %w", err)
	}

	url := fmt.Sprintf("%s/api/v1/repos/%s/board/move", c.baseURL, projectID)
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return fmt.Errorf("git-service request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("git-service error (status %d): %s", resp.StatusCode, string(respBody))
	}
	return nil
}
