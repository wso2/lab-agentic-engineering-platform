package services

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/wso2/asdlc/asdlc-service/internal/credentials"
)

// GitHubClient abstracts the GitHub REST calls the git-service needs.
//
// All calls take a credentials.Credential — the only place in the codebase
// that builds Authorization: Bearer headers. The client itself is
// stateless; concurrent calls are safe.
type GitHubClient interface {
	CreateOrgRepo(ctx context.Context, cred credentials.Credential, req CreateOrgRepoRequest) (cloneURL string, err error)
	CreateIssue(ctx context.Context, owner, repo string, cred credentials.Credential, req CreateIssueRequest) (*IssueResult, error)
	ListIssues(ctx context.Context, owner, repo string, cred credentials.Credential, labels []string) ([]IssueInfo, error)
	// EnsureLabel creates a label in the repository if it does not already exist.
	// It is idempotent — a 422 Unprocessable Entity response (already exists) is treated as success.
	EnsureLabel(ctx context.Context, owner, repo string, cred credentials.Credential, name, color string) error
	// CloseIssue sets the issue state to closed with reason "completed".
	CloseIssue(ctx context.Context, owner, repo string, cred credentials.Credential, number int) error
	// CommentIssue posts a comment on the issue.
	CommentIssue(ctx context.Context, owner, repo string, cred credentials.Credential, number int, body string) error
	// EditIssueBody replaces the issue body via PATCH /issues/{number}.
	// Used by the tech-lead detail phase to write the LLM-authored body
	// after the placeholder issue was created.
	EditIssueBody(ctx context.Context, owner, repo string, cred credentials.Credential, number int, body string) error
	// CreateBranch creates a new git ref pointing at fromSHA. fromSHA may be a
	// branch name (resolved to its tip) or a commit SHA. Idempotent on the
	// branch name: returns the existing tip SHA if the ref already exists.
	CreateBranch(ctx context.Context, owner, repo string, cred credentials.Credential, branch, fromSHA string) (sha string, err error)
	// GetBranchSHA returns the tip SHA of a branch, used to seed CreateBranch.
	GetBranchSHA(ctx context.Context, owner, repo string, cred credentials.Credential, branch string) (string, error)
	// PutFileOnBranch creates or updates a file on the given branch via the
	// Contents API. Idempotent on (path, content): if the file already exists
	// with identical content, returns the existing SHA without a new commit.
	// Used to seed a placeholder commit on a fresh feature branch so a draft
	// PR can be opened against it.
	PutFileOnBranch(ctx context.Context, owner, repo string, cred credentials.Credential, branch, path, message string, content []byte) error
	// CreateDraftPR opens a draft pull request. Idempotent on (head, base):
	// returns the existing PR if one already exists.
	CreateDraftPR(ctx context.Context, owner, repo string, cred credentials.Credential, req CreateDraftPRRequest) (*PullRequestResult, error)
	// RegisterWebhook installs a repository webhook delivering to deliveryURL,
	// signed with hmacSecret. Returns the GitHub-assigned hook ID.
	RegisterWebhook(ctx context.Context, owner, repo string, cred credentials.Credential, deliveryURL, hmacSecret string, events []string) (hookID int64, err error)
	// DeregisterWebhook removes a previously-registered webhook by ID.
	DeregisterWebhook(ctx context.Context, owner, repo string, cred credentials.Credential, hookID int64) error
	// GetUser returns identity from GET /user. Used by the periodic
	// validator to probe a PAT credential for liveness and identity drift.
	// Returns an HTTPStatusError wrapping 401/404 etc. so callers can
	// trigger the disconnect cascade selectively.
	GetUser(ctx context.Context, cred credentials.Credential) (*GitHubUser, error)
	// GetAppInstallation calls GET /app/installations/{id} using the App
	// JWT directly (not an installation token) so it can reach the App-level
	// endpoint. Used by the validator's App-mode probe to refresh
	// account.login on rename and to detect 404/410 (install deleted).
	GetAppInstallation(ctx context.Context, minter *credentials.AppTokenMinter, installationID int64) (*AppInstallationInfo, error)
	// ListAppInstallations calls GET /app/installations using the App JWT.
	// Returns the full list of installations our App has across GitHub.
	// PR D-followup §6.4 — used by the discover-then-bind path to surface
	// installations the platform has no row for yet.
	ListAppInstallations(ctx context.Context, minter *credentials.AppTokenMinter) ([]AppInstallationSummary, error)
	// ExchangeOAuthCode exchanges a GitHub OAuth code for a user-to-server
	// access token via POST github.com/login/oauth/access_token. Used by
	// the discover-then-bind path to obtain a user token whose
	// /user/installations response proves the user actually administers
	// the installation they're trying to bind.
	ExchangeOAuthCode(ctx context.Context, clientID, clientSecret, code, redirectURI string) (userToken string, err error)
	// GetUserInstallations calls GET /user/installations with a user-token.
	// Returns the list of installation IDs the authenticated user has
	// admin access to (per GitHub's "explicit permission" semantics).
	// Used by BindAppInstallation to verify the user is actually an admin
	// of the installation they're binding.
	GetUserInstallations(ctx context.Context, userToken string) ([]int64, error)
	// DeleteInstallation uninstalls the App from a GitHub account by calling
	// DELETE /app/installations/{id} with the App JWT. 204 means uninstalled,
	// 404 is treated as success (already gone). Used by the disconnect cascade
	// to make platform disconnect symmetric with the GitHub side — without
	// this, disconnects leave orphan installs visible to discover.
	DeleteInstallation(ctx context.Context, minter *credentials.AppTokenMinter, installationID int64) error

	// ----- Artifact-store v2 (docs/design/artifact-store-v2.md V1) -----
	//
	// Save-path GitHub API methods. All authed via the per-org credential.
	// These replace the `git commit + push` flow in SaveDesign/SaveRequirements;
	// the working-tree clone remains the source of truth for draft content
	// (Postgres drafts are V2). Errors map onto typed sentinels so the save
	// flow can branch (ErrSHAMismatch → retry, ErrRefNotFastForward → retry,
	// ErrTagAlreadyExists → recompute next tag).

	// GetContents returns the file blob at `ref` (branch tip or tag) plus the
	// blob SHA used as the precondition on subsequent PutContents.
	// 404 → returns (nil, HTTPStatusError{404}); the caller treats it as
	// "no file at this ref yet."
	GetContents(ctx context.Context, owner, repo string, cred credentials.Credential, path, ref string) (*ContentsResult, error)

	// PutContents creates or updates a single file via the Contents API,
	// committing atomically to `branch`. `req.SHA` is the blob SHA of the
	// file's current state on `branch` (the OCC precondition); empty =
	// create-only, non-empty = update-only with CAS. A SHA mismatch returns
	// ErrSHAMismatch. `req.Author` / `req.Committer` are GitIdentity.
	PutContents(ctx context.Context, owner, repo string, cred credentials.Credential, req PutContentsRequest) (*PutContentsResult, error)

	// GetRef returns the tip SHA of a ref. `ref` is the ref name without
	// "refs/" prefix (e.g. "heads/main" or "tags/v1").
	GetRef(ctx context.Context, owner, repo string, cred credentials.Credential, ref string) (string, error)

	// GetCommit returns the tree SHA and parents of a commit object.
	GetCommit(ctx context.Context, owner, repo string, cred credentials.Credential, sha string) (*CommitObject, error)

	// GetTree returns the entries of a tree object. `recursive=true` walks
	// nested trees in one call (capped by GitHub at ~100k entries).
	GetTree(ctx context.Context, owner, repo string, cred credentials.Credential, treeSHA string, recursive bool) (*TreeObject, error)

	// CreateBlob stores raw content as a blob and returns its SHA.
	CreateBlob(ctx context.Context, owner, repo string, cred credentials.Credential, content []byte) (string, error)

	// CreateTree assembles a tree from `baseTree` plus a partial overlay of
	// entries. Entries with empty SHA represent deletions (sha: null on the
	// wire); GitHub returns 422 if the deleted path is absent in baseTree.
	CreateTree(ctx context.Context, owner, repo string, cred credentials.Credential, baseTree string, entries []TreeEntry) (string, error)

	// CreateCommit creates a commit object pointing at the given tree.
	CreateCommit(ctx context.Context, owner, repo string, cred credentials.Credential, req CreateCommitRequest) (string, error)

	// UpdateRef atomically advances a ref to `sha`. With `force=false` the
	// call is fast-forward-only — non-FF moves return ErrRefNotFastForward.
	UpdateRef(ctx context.Context, owner, repo string, cred credentials.Credential, ref, sha string, force bool) error

	// CreateTagObject creates an annotated tag object pointing at `objectSHA`
	// (typically a commit). Returns the tag object's SHA; the caller still
	// needs CreateTagRef to make the tag visible.
	CreateTagObject(ctx context.Context, owner, repo string, cred credentials.Credential, req CreateTagObjectRequest) (string, error)

	// CreateTagRef creates the refs/tags/<name> ref pointing at the supplied
	// tag-object SHA. Returns ErrTagAlreadyExists if another writer has
	// claimed the name.
	CreateTagRef(ctx context.Context, owner, repo string, cred credentials.Credential, tagName, tagObjectSHA string) error

	// ListMatchingRefs lists all refs under the given prefix (e.g. "tags/v").
	// Returns an empty slice (not 404) when no refs match.
	ListMatchingRefs(ctx context.Context, owner, repo string, cred credentials.Credential, prefix string) ([]MatchingRef, error)
}

// AppInstallationSummary is the flat projection of /app/installations[i]
// the discover endpoint returns to the BFF and console. Mirrors the wire
// shape used in the response (camelCase). Distinct from
// AppInstallationInfo (which preserves the nested account.* shape used
// by the validator's GetAppInstallation probe).
type AppInstallationSummary struct {
	InstallationID int64  `json:"installationId"`
	AccountLogin   string `json:"accountLogin"`
	AccountType    string `json:"accountType"`
}

// GitHubUser is the subset of GET /user we consume.
type GitHubUser struct {
	Login string `json:"login"`
	Name  string `json:"name"`
	Email string `json:"email"`
	ID    int64  `json:"id"`
}

// AppInstallationInfo is the subset of GET /app/installations/{id} we consume.
// account.login is the GitHub org/user the install belongs to; it can drift
// when the org is renamed on GitHub.
type AppInstallationInfo struct {
	ID      int64 `json:"id"`
	Account struct {
		Login string `json:"login"`
		Type  string `json:"type"`
	} `json:"account"`
	Suspended *string `json:"suspended_at,omitempty"`
}

// HTTPStatusError surfaces HTTP status codes from the GitHub client so the
// validator can branch on 401 / 404 / 410. Wraps the response body for
// debug logging at the call site.
type HTTPStatusError struct {
	StatusCode int
	Body       string
	URL        string
}

func (e *HTTPStatusError) Error() string {
	return fmt.Sprintf("github API %s: status %d: %s", e.URL, e.StatusCode, e.Body)
}

// IsHTTPStatus reports true when err is an HTTPStatusError with the given code.
func IsHTTPStatus(err error, code int) bool {
	var he *HTTPStatusError
	if errors.As(err, &he) {
		return he.StatusCode == code
	}
	return false
}

// IssueInfo represents a GitHub issue returned when listing.
type IssueInfo struct {
	Number int
	Title  string
	Body   string
	URL    string
	State  string
	Labels []string
}

// CreateOrgRepoRequest maps to the fields we send to POST /orgs/{org}/repos.
//
// The owning org/user is derived from the Credential's RepoOwner() — the
// caller does not pass it explicitly, which keeps the multi-tenant invariant
// (repo creation is parametrised by the credential, not by ambient config).
type CreateOrgRepoRequest struct {
	Name        string
	Private     bool
	AutoInit    bool
	Description string
}

// CreateIssueRequest maps to the fields we send to POST /repos/{owner}/{repo}/issues.
type CreateIssueRequest struct {
	Title  string   `json:"title"`
	Body   string   `json:"body"`
	Labels []string `json:"labels,omitempty"`
}

// IssueResult is the GitHub issue metadata returned after creation.
type IssueResult struct {
	Number int    `json:"number"`
	URL    string `json:"url"`
	NodeID string `json:"nodeId"`
}

// CreateDraftPRRequest is the body for POST /repos/{owner}/{repo}/pulls.
type CreateDraftPRRequest struct {
	Title string
	Body  string
	Head  string // feature branch name
	Base  string // default branch
}

// PullRequestResult is the GitHub PR metadata returned after creation.
type PullRequestResult struct {
	Number int    `json:"number"`
	URL    string `json:"html_url"`
}

// ErrRepoNameConflict is returned when GitHub rejects a repo name because it already exists.
var ErrRepoNameConflict = errors.New("repo name already taken")

// IsRepoNameConflict reports whether err represents a GitHub name-conflict rejection.
func IsRepoNameConflict(err error) bool {
	return errors.Is(err, ErrRepoNameConflict)
}

type githubClient struct {
	httpClient *http.Client
}

func NewGitHubClient() GitHubClient {
	return &githubClient{
		httpClient: &http.Client{Timeout: 30 * time.Second},
	}
}

// authHeaders sets the standard GitHub API headers and the Authorization
// header. Token is fetched fresh on every call from the credential — Phase 0
// long-lived PATs are a no-op here, Phase 2 short-lived App tokens refresh
// on demand through the same path.
func authHeaders(ctx context.Context, req *http.Request, cred credentials.Credential) error {
	token, _, err := cred.Token(ctx)
	if err != nil {
		return fmt.Errorf("resolve token: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	return nil
}

func (c *githubClient) CreateOrgRepo(ctx context.Context, cred credentials.Credential, req CreateOrgRepoRequest) (string, error) {
	owner := cred.RepoOwner()
	if owner == "" {
		return "", fmt.Errorf("credential has no repo owner")
	}

	payload := map[string]any{
		"name":        req.Name,
		"private":     req.Private,
		"auto_init":   req.AutoInit,
		"description": req.Description,
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return "", fmt.Errorf("marshal request: %w", err)
	}

	url := fmt.Sprintf("https://api.github.com/orgs/%s/repos", owner)
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return "", fmt.Errorf("create request: %w", err)
	}
	if err := authHeaders(ctx, httpReq, cred); err != nil {
		return "", err
	}
	httpReq.Header.Set("Content-Type", "application/json")

	// User accounts (not orgs) require the /user/repos endpoint instead. Detect
	// 404 on /orgs/.../repos and retry once against /user/repos. This keeps the
	// PAT-mode "owner is a user, not an org" path working without a separate
	// configuration knob.
	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return "", fmt.Errorf("github API request: %w", err)
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)

	if resp.StatusCode == http.StatusNotFound {
		// Owner is likely a user account, not an org — try /user/repos.
		return c.createUserRepo(ctx, cred, payload)
	}

	if resp.StatusCode == http.StatusCreated {
		var created struct {
			CloneURL string `json:"clone_url"`
		}
		if err := json.Unmarshal(respBody, &created); err != nil {
			return "", fmt.Errorf("decode response: %w", err)
		}
		if created.CloneURL == "" {
			return "", fmt.Errorf("github response missing clone_url: %s", string(respBody))
		}
		return created.CloneURL, nil
	}

	if resp.StatusCode == http.StatusUnprocessableEntity &&
		strings.Contains(string(respBody), "name already exists") {
		return "", ErrRepoNameConflict
	}

	return "", fmt.Errorf("github repo create failed (status %d): %s", resp.StatusCode, string(respBody))
}

func (c *githubClient) createUserRepo(ctx context.Context, cred credentials.Credential, payload map[string]any) (string, error) {
	body, err := json.Marshal(payload)
	if err != nil {
		return "", fmt.Errorf("marshal request: %w", err)
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://api.github.com/user/repos", bytes.NewReader(body))
	if err != nil {
		return "", fmt.Errorf("create request: %w", err)
	}
	if err := authHeaders(ctx, httpReq, cred); err != nil {
		return "", err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return "", fmt.Errorf("github API request: %w", err)
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode == http.StatusCreated {
		var created struct {
			CloneURL string `json:"clone_url"`
		}
		if err := json.Unmarshal(respBody, &created); err != nil {
			return "", fmt.Errorf("decode response: %w", err)
		}
		return created.CloneURL, nil
	}
	if resp.StatusCode == http.StatusUnprocessableEntity &&
		strings.Contains(string(respBody), "name already exists") {
		return "", ErrRepoNameConflict
	}
	return "", fmt.Errorf("github user repo create failed (status %d): %s", resp.StatusCode, string(respBody))
}

func (c *githubClient) CreateIssue(ctx context.Context, owner, repo string, cred credentials.Credential, req CreateIssueRequest) (*IssueResult, error) {
	body, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	url := fmt.Sprintf("https://api.github.com/repos/%s/%s/issues", owner, repo)
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	if err := authHeaders(ctx, httpReq, cred); err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("github API request: %w", err)
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(resp.Body)

	if resp.StatusCode == http.StatusCreated {
		var created struct {
			Number  int    `json:"number"`
			HTMLURL string `json:"html_url"`
			NodeID  string `json:"node_id"`
		}
		if err := json.Unmarshal(respBody, &created); err != nil {
			return nil, fmt.Errorf("decode response: %w", err)
		}
		if created.HTMLURL == "" {
			return nil, fmt.Errorf("github response missing html_url: %s", string(respBody))
		}
		return &IssueResult{Number: created.Number, URL: created.HTMLURL, NodeID: created.NodeID}, nil
	}

	return nil, fmt.Errorf("github issue create failed (status %d): %s", resp.StatusCode, string(respBody))
}

func (c *githubClient) EnsureLabel(ctx context.Context, owner, repo string, cred credentials.Credential, name, color string) error {
	payload := map[string]string{"name": name, "color": color}
	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal request: %w", err)
	}

	url := fmt.Sprintf("https://api.github.com/repos/%s/%s/labels", owner, repo)
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}
	if err := authHeaders(ctx, httpReq, cred); err != nil {
		return err
	}
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return fmt.Errorf("github API request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusCreated || resp.StatusCode == http.StatusUnprocessableEntity {
		return nil
	}

	respBody, _ := io.ReadAll(resp.Body)
	return fmt.Errorf("github label ensure failed (status %d): %s", resp.StatusCode, string(respBody))
}

func (c *githubClient) CloseIssue(ctx context.Context, owner, repo string, cred credentials.Credential, number int) error {
	payload := map[string]string{"state": "closed", "state_reason": "completed"}
	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal request: %w", err)
	}

	url := fmt.Sprintf("https://api.github.com/repos/%s/%s/issues/%d", owner, repo, number)
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPatch, url, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}
	if err := authHeaders(ctx, httpReq, cred); err != nil {
		return err
	}
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return fmt.Errorf("github API request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusOK {
		return nil
	}

	respBody, _ := io.ReadAll(resp.Body)
	return fmt.Errorf("github issue close failed (status %d): %s", resp.StatusCode, string(respBody))
}

func (c *githubClient) EditIssueBody(ctx context.Context, owner, repo string, cred credentials.Credential, number int, body string) error {
	payload := map[string]string{"body": body}
	reqBody, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal request: %w", err)
	}

	url := fmt.Sprintf("https://api.github.com/repos/%s/%s/issues/%d", owner, repo, number)
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPatch, url, bytes.NewReader(reqBody))
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}
	if err := authHeaders(ctx, httpReq, cred); err != nil {
		return err
	}
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return fmt.Errorf("github API request: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusOK {
		return nil
	}
	respBody, _ := io.ReadAll(resp.Body)
	return fmt.Errorf("github issue edit failed (status %d): %s", resp.StatusCode, string(respBody))
}

func (c *githubClient) CommentIssue(ctx context.Context, owner, repo string, cred credentials.Credential, number int, body string) error {
	payload := map[string]string{"body": body}
	reqBody, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal request: %w", err)
	}

	url := fmt.Sprintf("https://api.github.com/repos/%s/%s/issues/%d/comments", owner, repo, number)
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(reqBody))
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}
	if err := authHeaders(ctx, httpReq, cred); err != nil {
		return err
	}
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return fmt.Errorf("github API request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusCreated {
		return nil
	}

	respBody, _ := io.ReadAll(resp.Body)
	return fmt.Errorf("github issue comment failed (status %d): %s", resp.StatusCode, string(respBody))
}

func (c *githubClient) ListIssues(ctx context.Context, owner, repo string, cred credentials.Credential, labels []string) ([]IssueInfo, error) {
	url := fmt.Sprintf("https://api.github.com/repos/%s/%s/issues?state=all&per_page=100", owner, repo)
	if len(labels) > 0 {
		url += "&labels=" + strings.Join(labels, ",")
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	if err := authHeaders(ctx, httpReq, cred); err != nil {
		return nil, err
	}

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("github API request: %w", err)
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(resp.Body)

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("github list issues failed (status %d): %s", resp.StatusCode, string(respBody))
	}

	var raw []struct {
		Number  int    `json:"number"`
		Title   string `json:"title"`
		Body    string `json:"body"`
		HTMLURL string `json:"html_url"`
		State   string `json:"state"`
		Labels  []struct {
			Name string `json:"name"`
		} `json:"labels"`
	}
	if err := json.Unmarshal(respBody, &raw); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}

	issues := make([]IssueInfo, 0, len(raw))
	for _, r := range raw {
		labelNames := make([]string, 0, len(r.Labels))
		for _, l := range r.Labels {
			labelNames = append(labelNames, l.Name)
		}
		issues = append(issues, IssueInfo{
			Number: r.Number,
			Title:  r.Title,
			Body:   r.Body,
			URL:    r.HTMLURL,
			State:  r.State,
			Labels: labelNames,
		})
	}
	return issues, nil
}

func (c *githubClient) GetBranchSHA(ctx context.Context, owner, repo string, cred credentials.Credential, branch string) (string, error) {
	url := fmt.Sprintf("https://api.github.com/repos/%s/%s/git/ref/heads/%s", owner, repo, branch)
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", fmt.Errorf("create request: %w", err)
	}
	if err := authHeaders(ctx, httpReq, cred); err != nil {
		return "", err
	}
	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return "", fmt.Errorf("github API request: %w", err)
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("github get branch failed (status %d): %s", resp.StatusCode, string(respBody))
	}
	var ref struct {
		Object struct {
			SHA string `json:"sha"`
		} `json:"object"`
	}
	if err := json.Unmarshal(respBody, &ref); err != nil {
		return "", fmt.Errorf("decode response: %w", err)
	}
	return ref.Object.SHA, nil
}

func (c *githubClient) CreateBranch(ctx context.Context, owner, repo string, cred credentials.Credential, branch, fromSHA string) (string, error) {
	payload := map[string]string{"ref": "refs/heads/" + branch, "sha": fromSHA}
	body, err := json.Marshal(payload)
	if err != nil {
		return "", fmt.Errorf("marshal request: %w", err)
	}
	url := fmt.Sprintf("https://api.github.com/repos/%s/%s/git/refs", owner, repo)
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return "", fmt.Errorf("create request: %w", err)
	}
	if err := authHeaders(ctx, httpReq, cred); err != nil {
		return "", err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return "", fmt.Errorf("github API request: %w", err)
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode == http.StatusCreated {
		var ref struct {
			Object struct {
				SHA string `json:"sha"`
			} `json:"object"`
		}
		if err := json.Unmarshal(respBody, &ref); err != nil {
			return "", fmt.Errorf("decode response: %w", err)
		}
		return ref.Object.SHA, nil
	}
	// Idempotency: branch already exists. Look it up and return its tip SHA.
	if resp.StatusCode == http.StatusUnprocessableEntity &&
		strings.Contains(string(respBody), "Reference already exists") {
		return c.GetBranchSHA(ctx, owner, repo, cred, branch)
	}
	return "", fmt.Errorf("github create branch failed (status %d): %s", resp.StatusCode, string(respBody))
}

func (c *githubClient) CreateDraftPR(ctx context.Context, owner, repo string, cred credentials.Credential, req CreateDraftPRRequest) (*PullRequestResult, error) {
	payload := map[string]any{
		"title": req.Title,
		"body":  req.Body,
		"head":  req.Head,
		"base":  req.Base,
		"draft": true,
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}
	url := fmt.Sprintf("https://api.github.com/repos/%s/%s/pulls", owner, repo)
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	if err := authHeaders(ctx, httpReq, cred); err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("github API request: %w", err)
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode == http.StatusCreated {
		var pr PullRequestResult
		if err := json.Unmarshal(respBody, &pr); err != nil {
			return nil, fmt.Errorf("decode response: %w", err)
		}
		return &pr, nil
	}
	// Idempotency: PR already exists for head/base. Look it up.
	if resp.StatusCode == http.StatusUnprocessableEntity &&
		strings.Contains(string(respBody), "A pull request already exists") {
		return c.findPullByHead(ctx, owner, repo, cred, req.Head)
	}
	return nil, fmt.Errorf("github create PR failed (status %d): %s", resp.StatusCode, string(respBody))
}

func (c *githubClient) findPullByHead(ctx context.Context, owner, repo string, cred credentials.Credential, head string) (*PullRequestResult, error) {
	// Searching by head requires owner:branch form.
	url := fmt.Sprintf("https://api.github.com/repos/%s/%s/pulls?state=open&head=%s:%s", owner, repo, owner, head)
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	if err := authHeaders(ctx, httpReq, cred); err != nil {
		return nil, err
	}
	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("github API request: %w", err)
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("github list PRs failed (status %d): %s", resp.StatusCode, string(respBody))
	}
	var prs []PullRequestResult
	if err := json.Unmarshal(respBody, &prs); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}
	if len(prs) == 0 {
		return nil, fmt.Errorf("no open PR found for head=%s", head)
	}
	return &prs[0], nil
}

func (c *githubClient) RegisterWebhook(ctx context.Context, owner, repo string, cred credentials.Credential, deliveryURL, hmacSecret string, events []string) (int64, error) {
	payload := map[string]any{
		"name":   "web",
		"active": true,
		"events": events,
		"config": map[string]string{
			"url":          deliveryURL,
			"content_type": "json",
			"secret":       hmacSecret,
			"insecure_ssl": "0",
		},
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return 0, fmt.Errorf("marshal request: %w", err)
	}
	url := fmt.Sprintf("https://api.github.com/repos/%s/%s/hooks", owner, repo)
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return 0, fmt.Errorf("create request: %w", err)
	}
	if err := authHeaders(ctx, httpReq, cred); err != nil {
		return 0, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return 0, fmt.Errorf("github API request: %w", err)
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode == http.StatusCreated {
		var hook struct {
			ID int64 `json:"id"`
		}
		if err := json.Unmarshal(respBody, &hook); err != nil {
			return 0, fmt.Errorf("decode response: %w", err)
		}
		return hook.ID, nil
	}
	// Idempotency: GitHub returns 422 "Hook already exists" when the same URL is
	// registered twice. Look up the existing hook and return its ID.
	if resp.StatusCode == http.StatusUnprocessableEntity &&
		strings.Contains(string(respBody), "Hook already exists") {
		return c.findHookByURL(ctx, owner, repo, cred, deliveryURL)
	}
	return 0, fmt.Errorf("github register webhook failed (status %d): %s", resp.StatusCode, string(respBody))
}

func (c *githubClient) findHookByURL(ctx context.Context, owner, repo string, cred credentials.Credential, deliveryURL string) (int64, error) {
	url := fmt.Sprintf("https://api.github.com/repos/%s/%s/hooks?per_page=100", owner, repo)
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return 0, fmt.Errorf("create request: %w", err)
	}
	if err := authHeaders(ctx, httpReq, cred); err != nil {
		return 0, err
	}
	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return 0, fmt.Errorf("github API request: %w", err)
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("github list hooks failed (status %d): %s", resp.StatusCode, string(respBody))
	}
	var hooks []struct {
		ID     int64             `json:"id"`
		Config map[string]string `json:"config"`
	}
	if err := json.Unmarshal(respBody, &hooks); err != nil {
		return 0, fmt.Errorf("decode response: %w", err)
	}
	for _, h := range hooks {
		if h.Config["url"] == deliveryURL {
			return h.ID, nil
		}
	}
	return 0, fmt.Errorf("hook for url %s not found", deliveryURL)
}

func (c *githubClient) DeregisterWebhook(ctx context.Context, owner, repo string, cred credentials.Credential, hookID int64) error {
	url := fmt.Sprintf("https://api.github.com/repos/%s/%s/hooks/%d", owner, repo, hookID)
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodDelete, url, nil)
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}
	if err := authHeaders(ctx, httpReq, cred); err != nil {
		return err
	}
	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return fmt.Errorf("github API request: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNoContent || resp.StatusCode == http.StatusNotFound {
		return nil
	}
	respBody, _ := io.ReadAll(resp.Body)
	return fmt.Errorf("github deregister webhook failed (status %d): %s", resp.StatusCode, string(respBody))
}

func (c *githubClient) PutFileOnBranch(ctx context.Context, owner, repo string, cred credentials.Credential, branch, path, message string, content []byte) error {
	// Look up an existing file at the same path on the branch to get its SHA
	// (required when updating an existing file). Missing file → create.
	getURL := fmt.Sprintf("https://api.github.com/repos/%s/%s/contents/%s?ref=%s",
		owner, repo, path, branch)
	getReq, err := http.NewRequestWithContext(ctx, http.MethodGet, getURL, nil)
	if err != nil {
		return fmt.Errorf("create get-file request: %w", err)
	}
	if err := authHeaders(ctx, getReq, cred); err != nil {
		return err
	}
	getResp, err := c.httpClient.Do(getReq)
	if err != nil {
		return fmt.Errorf("github get-file request: %w", err)
	}
	defer getResp.Body.Close()
	getBody, _ := io.ReadAll(getResp.Body)

	var existingSHA string
	if getResp.StatusCode == http.StatusOK {
		var existing struct {
			SHA string `json:"sha"`
		}
		if err := json.Unmarshal(getBody, &existing); err == nil {
			existingSHA = existing.SHA
		}
	} else if getResp.StatusCode != http.StatusNotFound {
		return fmt.Errorf("github get-file failed (status %d): %s", getResp.StatusCode, string(getBody))
	}

	payload := map[string]any{
		"message": message,
		"branch":  branch,
		"content": base64.StdEncoding.EncodeToString(content),
	}
	if existingSHA != "" {
		payload["sha"] = existingSHA
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal request: %w", err)
	}
	putURL := fmt.Sprintf("https://api.github.com/repos/%s/%s/contents/%s", owner, repo, path)
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPut, putURL, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("create put-file request: %w", err)
	}
	if err := authHeaders(ctx, httpReq, cred); err != nil {
		return err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return fmt.Errorf("github put-file request: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusOK || resp.StatusCode == http.StatusCreated {
		return nil
	}
	respBody, _ := io.ReadAll(resp.Body)
	// 422 unprocessable when content matches — treat as success (idempotent).
	if resp.StatusCode == http.StatusUnprocessableEntity &&
		strings.Contains(string(respBody), "does not match") {
		// content already up-to-date; nothing to do
		return nil
	}
	return fmt.Errorf("github put-file failed (status %d): %s", resp.StatusCode, string(respBody))
}

// GetUser performs GET /user using the credential's token. Returns
// HTTPStatusError for non-2xx responses so the validator can branch on
// 401 (revoked) vs 5xx (transient).
func (c *githubClient) GetUser(ctx context.Context, cred credentials.Credential) (*GitHubUser, error) {
	url := "https://api.github.com/user"
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	if err := authHeaders(ctx, httpReq, cred); err != nil {
		return nil, err
	}

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("github API request: %w", err)
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)

	if resp.StatusCode != http.StatusOK {
		return nil, &HTTPStatusError{StatusCode: resp.StatusCode, Body: string(respBody), URL: url}
	}
	var user GitHubUser
	if err := json.Unmarshal(respBody, &user); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}
	return &user, nil
}

// GetAppInstallation calls GET /app/installations/{id}. Authenticated by
// the App JWT (not an installation token) — App-level endpoints reject
// installation tokens. The minter exposes the JWT through SignAppJWT().
func (c *githubClient) GetAppInstallation(ctx context.Context, minter *credentials.AppTokenMinter, installationID int64) (*AppInstallationInfo, error) {
	if minter == nil {
		return nil, fmt.Errorf("app minter required")
	}
	jwt, err := minter.SignAppJWT(time.Now())
	if err != nil {
		return nil, fmt.Errorf("sign app JWT: %w", err)
	}
	url := fmt.Sprintf("https://api.github.com/app/installations/%d", installationID)
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	httpReq.Header.Set("Authorization", "Bearer "+jwt)
	httpReq.Header.Set("Accept", "application/vnd.github+json")
	httpReq.Header.Set("X-GitHub-Api-Version", "2022-11-28")

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("github API request: %w", err)
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)

	if resp.StatusCode != http.StatusOK {
		return nil, &HTTPStatusError{StatusCode: resp.StatusCode, Body: string(respBody), URL: url}
	}
	var info AppInstallationInfo
	if err := json.Unmarshal(respBody, &info); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}
	return &info, nil
}

func (c *githubClient) DeleteInstallation(ctx context.Context, minter *credentials.AppTokenMinter, installationID int64) error {
	if minter == nil {
		return fmt.Errorf("app minter required")
	}
	jwt, err := minter.SignAppJWT(time.Now())
	if err != nil {
		return fmt.Errorf("sign app JWT: %w", err)
	}
	url := fmt.Sprintf("https://api.github.com/app/installations/%d", installationID)
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodDelete, url, nil)
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}
	httpReq.Header.Set("Authorization", "Bearer "+jwt)
	httpReq.Header.Set("Accept", "application/vnd.github+json")
	httpReq.Header.Set("X-GitHub-Api-Version", "2022-11-28")

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return fmt.Errorf("github API request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNoContent || resp.StatusCode == http.StatusNotFound {
		return nil
	}
	respBody, _ := io.ReadAll(resp.Body)
	return &HTTPStatusError{StatusCode: resp.StatusCode, Body: string(respBody), URL: url}
}

// ListAppInstallations pages through GET /app/installations using the
// App JWT. Returns the flat AppInstallationSummary projection. Caps
// the walk at 10 pages × 100 = 1000 installations as a defensive bound;
// real-world dev/single-tenant installs are an order of magnitude under.
func (c *githubClient) ListAppInstallations(ctx context.Context, minter *credentials.AppTokenMinter) ([]AppInstallationSummary, error) {
	if minter == nil {
		return nil, fmt.Errorf("app minter required")
	}
	jwt, err := minter.SignAppJWT(time.Now())
	if err != nil {
		return nil, fmt.Errorf("sign app JWT: %w", err)
	}

	type pageItem struct {
		ID      int64 `json:"id"`
		Account struct {
			Login string `json:"login"`
			Type  string `json:"type"`
		} `json:"account"`
	}

	all := make([]AppInstallationSummary, 0, 16)
	page := 1
	for {
		url := fmt.Sprintf("https://api.github.com/app/installations?per_page=100&page=%d", page)
		httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if err != nil {
			return nil, fmt.Errorf("create request: %w", err)
		}
		httpReq.Header.Set("Authorization", "Bearer "+jwt)
		httpReq.Header.Set("Accept", "application/vnd.github+json")
		httpReq.Header.Set("X-GitHub-Api-Version", "2022-11-28")

		resp, err := c.httpClient.Do(httpReq)
		if err != nil {
			return nil, fmt.Errorf("github API request: %w", err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return nil, &HTTPStatusError{StatusCode: resp.StatusCode, Body: string(body), URL: url}
		}
		var items []pageItem
		if err := json.Unmarshal(body, &items); err != nil {
			return nil, fmt.Errorf("decode response: %w", err)
		}
		for _, it := range items {
			all = append(all, AppInstallationSummary{
				InstallationID: it.ID,
				AccountLogin:   it.Account.Login,
				AccountType:    it.Account.Type,
			})
		}
		if len(items) < 100 {
			break
		}
		page++
		if page > 10 {
			break
		}
	}
	return all, nil
}

// ExchangeOAuthCode exchanges a GitHub OAuth code for a user-to-server
// access token. Distinct from the App JWT path: this uses the App's
// OAuth client_id + client_secret (Basic-auth style on the form-encoded
// body, per GitHub's docs) and calls the github.com endpoint (not api.github.com).
//
// Returns the access_token string. Empty token + nil error means the
// exchange came back with no token (user revoked or invalid code; GitHub
// returns 200 with an `error` field rather than non-2xx). Caller treats
// empty as authorisation failure.
func (c *githubClient) ExchangeOAuthCode(ctx context.Context, clientID, clientSecret, code, redirectURI string) (string, error) {
	if clientID == "" || clientSecret == "" {
		return "", fmt.Errorf("oauth: client_id/secret required")
	}
	form := strings.NewReader(fmt.Sprintf(
		"client_id=%s&client_secret=%s&code=%s&redirect_uri=%s",
		clientID, clientSecret, code, redirectURI,
	))
	url := "https://github.com/login/oauth/access_token"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, form)
	if err != nil {
		return "", fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("oauth exchange: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return "", &HTTPStatusError{StatusCode: resp.StatusCode, Body: string(body), URL: url}
	}
	var out struct {
		AccessToken      string `json:"access_token"`
		Error            string `json:"error"`
		ErrorDescription string `json:"error_description"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return "", fmt.Errorf("decode oauth response: %w", err)
	}
	if out.Error != "" {
		// GitHub returns 200 + {"error":"bad_verification_code", ...} on
		// invalid/expired codes. Treat as auth failure (empty token).
		return "", fmt.Errorf("oauth exchange: %s: %s", out.Error, out.ErrorDescription)
	}
	return out.AccessToken, nil
}

// GetUserInstallations pages through GET /user/installations with a
// user-to-server access token. Returns the list of installation IDs the
// authenticated user has admin access to (per GitHub's "explicit
// permission" semantics — for orgs that means org admin; for user
// accounts, the user's own account).
//
// Used by BindAppInstallation to verify the user is actually an admin
// of the installation they're trying to bind, closing the cross-tenant
// race on the bind path.
func (c *githubClient) GetUserInstallations(ctx context.Context, userToken string) ([]int64, error) {
	if userToken == "" {
		return nil, fmt.Errorf("user token required")
	}

	type pageResp struct {
		TotalCount    int `json:"total_count"`
		Installations []struct {
			ID int64 `json:"id"`
		} `json:"installations"`
	}

	all := make([]int64, 0, 4)
	page := 1
	for {
		url := fmt.Sprintf("https://api.github.com/user/installations?per_page=100&page=%d", page)
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if err != nil {
			return nil, fmt.Errorf("create request: %w", err)
		}
		req.Header.Set("Authorization", "Bearer "+userToken)
		req.Header.Set("Accept", "application/vnd.github+json")
		req.Header.Set("X-GitHub-Api-Version", "2022-11-28")

		resp, err := c.httpClient.Do(req)
		if err != nil {
			return nil, fmt.Errorf("github API request: %w", err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return nil, &HTTPStatusError{StatusCode: resp.StatusCode, Body: string(body), URL: url}
		}
		var pr pageResp
		if err := json.Unmarshal(body, &pr); err != nil {
			return nil, fmt.Errorf("decode response: %w", err)
		}
		for _, it := range pr.Installations {
			all = append(all, it.ID)
		}
		if len(pr.Installations) < 100 {
			break
		}
		page++
		if page > 10 {
			break
		}
	}
	return all, nil
}

