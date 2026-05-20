package services

// GitHub artifact-store v2 (V1 scope) HTTP methods.
//
// These methods replace the `git commit + push` flow inside SaveDesign and
// SaveRequirements. They live on the same `githubClient` struct as the rest of
// the GitHub HTTP surface so the credentials boundary (Phase 2 §2.1) is
// preserved — no token-bearing value crosses BFF↔git-service.
//
// See docs/design/artifact-store-v2.md §8 (save flows) and §9 (concurrency).

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"

	"github.com/wso2/asdlc/git-service/pkg/credentials"
)

// ----- Typed errors -----

var (
	// ErrSHAMismatch is returned by PutContents when the supplied `sha`
	// precondition differs from the blob currently at the path on `branch`.
	// Wraps a 409 from GitHub.
	ErrSHAMismatch = errors.New("github contents: sha precondition mismatch")

	// ErrRefNotFastForward is returned by UpdateRef when force=false and the
	// requested move would be non-fast-forward (ref tip moved between read
	// and write). Wraps a 422 from GitHub.
	ErrRefNotFastForward = errors.New("github ref: update is not a fast-forward")

	// (ErrTagAlreadyExists is declared in errors.go and reused here; we wrap
	// it on CreateTagRef 422 so save flows can branch on the tag-collision
	// case distinctly from generic 422s.)
)

// ----- Request / response types -----

// ContentsResult is the subset of GET /contents we consume. `BlobSHA` is the
// git blob SHA used as the OCC precondition on subsequent PutContents.
type ContentsResult struct {
	Content string // raw bytes (decoded from base64)
	BlobSHA string // blob SHA (the `sha` field GitHub returns)
	Path    string
}

// PutContentsRequest is the body of PUT /contents.
type PutContentsRequest struct {
	Path      string
	Branch    string
	Message   string
	Content   []byte
	SHA       string // OCC precondition; empty = create-only
	Author    *GitIdentity
	Committer *GitIdentity
}

// PutContentsResult exposes what callers need from the response: the new
// commit SHA (used to anchor the annotated-tag two-step) and the new blob SHA
// (used to upsert the read cache once V3 lands).
type PutContentsResult struct {
	CommitSHA string
	BlobSHA   string
}

// GitIdentity mirrors GitHub's author/committer/tagger object. Date is
// optional; GitHub defaults to the request time when omitted. Named with the
// `Git` prefix to avoid collision with the `Identity` type already declared
// in credential_service.go.
type GitIdentity struct {
	Name  string `json:"name"`
	Email string `json:"email"`
	Date  string `json:"date,omitempty"`
}

// CommitObject is the subset of GET /git/commits we consume.
type CommitObject struct {
	SHA     string
	TreeSHA string
	Parents []string
	Message string
}

// TreeEntry is the wire shape for POST /git/trees entries. SHA is empty for
// deletions (we serialise as `sha: null` on the wire).
type TreeEntry struct {
	Path string
	Mode string // "100644" file, "100755" exec, "040000" dir, "160000" submodule, "120000" symlink
	Type string // "blob" | "tree" | "commit"
	SHA  string // empty → deletion (sha:null)
}

// TreeObject is the subset of GET /git/trees we consume.
type TreeObject struct {
	SHA     string
	Entries []TreeEntryResult
}

// TreeEntryResult is one row of a tree listing. Size is only set for blobs.
type TreeEntryResult struct {
	Path string `json:"path"`
	Mode string `json:"mode"`
	Type string `json:"type"`
	SHA  string `json:"sha"`
	Size int64  `json:"size,omitempty"`
}

// CreateCommitRequest is the body of POST /git/commits.
type CreateCommitRequest struct {
	Message   string
	TreeSHA   string
	Parents   []string
	Author    *GitIdentity
	Committer *GitIdentity
}

// CreateTagObjectRequest is the body of POST /git/tags.
type CreateTagObjectRequest struct {
	Tag     string
	Message string
	Object  string // commit SHA
	Type    string // typically "commit"
	Tagger  *GitIdentity
}

// MatchingRef is one row of GET /git/matching-refs/<prefix>.
type MatchingRef struct {
	Ref string `json:"ref"` // e.g. "refs/tags/v1"
	SHA string // pulled out of the nested .object.sha
}

// ----- Implementations -----

func (c *githubClient) GetContents(ctx context.Context, owner, repo string, cred credentials.Credential, path, ref string) (*ContentsResult, error) {
	url := fmt.Sprintf("https://api.github.com/repos/%s/%s/contents/%s?ref=%s", owner, repo, path, ref)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("create get-contents request: %w", err)
	}
	if err := authHeaders(ctx, req, cred); err != nil {
		return nil, err
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("github get-contents: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode == http.StatusNotFound {
		return nil, &HTTPStatusError{StatusCode: resp.StatusCode, Body: string(body), URL: url}
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("github get-contents (status %d): %s", resp.StatusCode, string(body))
	}
	var parsed struct {
		Path     string `json:"path"`
		SHA      string `json:"sha"`
		Content  string `json:"content"`
		Encoding string `json:"encoding"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil, fmt.Errorf("decode get-contents: %w", err)
	}
	var content []byte
	if parsed.Encoding == "base64" {
		// GitHub returns base64 with embedded newlines; decode with StdEncoding tolerantly.
		raw, err := base64.StdEncoding.DecodeString(stripNewlines(parsed.Content))
		if err != nil {
			return nil, fmt.Errorf("decode content base64: %w", err)
		}
		content = raw
	} else {
		content = []byte(parsed.Content)
	}
	return &ContentsResult{
		Content: string(content),
		BlobSHA: parsed.SHA,
		Path:    parsed.Path,
	}, nil
}

func (c *githubClient) PutContents(ctx context.Context, owner, repo string, cred credentials.Credential, req PutContentsRequest) (*PutContentsResult, error) {
	payload := map[string]any{
		"message": req.Message,
		"branch":  req.Branch,
		"content": base64.StdEncoding.EncodeToString(req.Content),
	}
	if req.SHA != "" {
		payload["sha"] = req.SHA
	}
	if req.Author != nil {
		payload["author"] = req.Author
	}
	if req.Committer != nil {
		payload["committer"] = req.Committer
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("marshal put-contents: %w", err)
	}
	url := fmt.Sprintf("https://api.github.com/repos/%s/%s/contents/%s", owner, repo, req.Path)
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPut, url, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("create put-contents request: %w", err)
	}
	if err := authHeaders(ctx, httpReq, cred); err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("github put-contents: %w", err)
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)
	// GitHub returns 409 for SHA-precondition mismatch and 422 for "does not match"
	// (the latter when content is unchanged on the path). The 422-unchanged case
	// is treated as success by PutFileOnBranch elsewhere; here we surface it as
	// ErrSHAMismatch so the save flow can retry with a refreshed precondition.
	if resp.StatusCode == http.StatusConflict {
		return nil, fmt.Errorf("%w: %s", ErrSHAMismatch, string(respBody))
	}
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		return nil, fmt.Errorf("github put-contents (status %d): %s", resp.StatusCode, string(respBody))
	}
	var parsed struct {
		Content struct {
			SHA string `json:"sha"`
		} `json:"content"`
		Commit struct {
			SHA string `json:"sha"`
		} `json:"commit"`
	}
	if err := json.Unmarshal(respBody, &parsed); err != nil {
		return nil, fmt.Errorf("decode put-contents: %w", err)
	}
	return &PutContentsResult{
		CommitSHA: parsed.Commit.SHA,
		BlobSHA:   parsed.Content.SHA,
	}, nil
}

func (c *githubClient) GetRef(ctx context.Context, owner, repo string, cred credentials.Credential, ref string) (string, error) {
	url := fmt.Sprintf("https://api.github.com/repos/%s/%s/git/ref/%s", owner, repo, ref)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", fmt.Errorf("create get-ref request: %w", err)
	}
	if err := authHeaders(ctx, req, cred); err != nil {
		return "", err
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("github get-ref: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode == http.StatusNotFound {
		return "", &HTTPStatusError{StatusCode: resp.StatusCode, Body: string(body), URL: url}
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("github get-ref (status %d): %s", resp.StatusCode, string(body))
	}
	var parsed struct {
		Object struct {
			SHA string `json:"sha"`
		} `json:"object"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return "", fmt.Errorf("decode get-ref: %w", err)
	}
	return parsed.Object.SHA, nil
}

func (c *githubClient) GetCommit(ctx context.Context, owner, repo string, cred credentials.Credential, sha string) (*CommitObject, error) {
	url := fmt.Sprintf("https://api.github.com/repos/%s/%s/git/commits/%s", owner, repo, sha)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("create get-commit request: %w", err)
	}
	if err := authHeaders(ctx, req, cred); err != nil {
		return nil, err
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("github get-commit: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("github get-commit (status %d): %s", resp.StatusCode, string(body))
	}
	var parsed struct {
		SHA  string `json:"sha"`
		Tree struct {
			SHA string `json:"sha"`
		} `json:"tree"`
		Parents []struct {
			SHA string `json:"sha"`
		} `json:"parents"`
		Message string `json:"message"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil, fmt.Errorf("decode get-commit: %w", err)
	}
	parents := make([]string, 0, len(parsed.Parents))
	for _, p := range parsed.Parents {
		parents = append(parents, p.SHA)
	}
	return &CommitObject{
		SHA:     parsed.SHA,
		TreeSHA: parsed.Tree.SHA,
		Parents: parents,
		Message: parsed.Message,
	}, nil
}

func (c *githubClient) GetTree(ctx context.Context, owner, repo string, cred credentials.Credential, treeSHA string, recursive bool) (*TreeObject, error) {
	url := fmt.Sprintf("https://api.github.com/repos/%s/%s/git/trees/%s", owner, repo, treeSHA)
	if recursive {
		url += "?recursive=true"
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("create get-tree request: %w", err)
	}
	if err := authHeaders(ctx, req, cred); err != nil {
		return nil, err
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("github get-tree: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("github get-tree (status %d): %s", resp.StatusCode, string(body))
	}
	var parsed struct {
		SHA       string            `json:"sha"`
		Tree      []TreeEntryResult `json:"tree"`
		Truncated bool              `json:"truncated"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil, fmt.Errorf("decode get-tree: %w", err)
	}
	if parsed.Truncated {
		// specs/ is tiny; truncation means a misuse (saving requirements across
		// a 100k-entry tree). Fail loud so we don't silently miss entries.
		return nil, fmt.Errorf("github get-tree: response truncated (tree too large)")
	}
	return &TreeObject{SHA: parsed.SHA, Entries: parsed.Tree}, nil
}

func (c *githubClient) CreateBlob(ctx context.Context, owner, repo string, cred credentials.Credential, content []byte) (string, error) {
	payload := map[string]any{
		"content":  base64.StdEncoding.EncodeToString(content),
		"encoding": "base64",
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return "", fmt.Errorf("marshal create-blob: %w", err)
	}
	url := fmt.Sprintf("https://api.github.com/repos/%s/%s/git/blobs", owner, repo)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return "", fmt.Errorf("create create-blob request: %w", err)
	}
	if err := authHeaders(ctx, req, cred); err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("github create-blob: %w", err)
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusCreated {
		return "", fmt.Errorf("github create-blob (status %d): %s", resp.StatusCode, string(respBody))
	}
	var parsed struct {
		SHA string `json:"sha"`
	}
	if err := json.Unmarshal(respBody, &parsed); err != nil {
		return "", fmt.Errorf("decode create-blob: %w", err)
	}
	return parsed.SHA, nil
}

func (c *githubClient) CreateTree(ctx context.Context, owner, repo string, cred credentials.Credential, baseTree string, entries []TreeEntry) (string, error) {
	// Wire entries: deletions go as `sha: null` (not omitted) so GitHub
	// removes the path from the resulting tree. Encode them with a custom
	// shape to make `sha: null` explicit.
	type wireEntry struct {
		Path string  `json:"path"`
		Mode string  `json:"mode"`
		Type string  `json:"type"`
		SHA  *string `json:"sha"` // pointer so a nil shows as `null`
	}
	wire := make([]wireEntry, 0, len(entries))
	for _, e := range entries {
		w := wireEntry{Path: e.Path, Mode: e.Mode, Type: e.Type}
		if e.SHA != "" {
			s := e.SHA
			w.SHA = &s
		}
		wire = append(wire, w)
	}
	payload := map[string]any{
		"tree": wire,
	}
	if baseTree != "" {
		payload["base_tree"] = baseTree
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return "", fmt.Errorf("marshal create-tree: %w", err)
	}
	url := fmt.Sprintf("https://api.github.com/repos/%s/%s/git/trees", owner, repo)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return "", fmt.Errorf("create create-tree request: %w", err)
	}
	if err := authHeaders(ctx, req, cred); err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("github create-tree: %w", err)
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusCreated {
		return "", fmt.Errorf("github create-tree (status %d): %s", resp.StatusCode, string(respBody))
	}
	var parsed struct {
		SHA string `json:"sha"`
	}
	if err := json.Unmarshal(respBody, &parsed); err != nil {
		return "", fmt.Errorf("decode create-tree: %w", err)
	}
	return parsed.SHA, nil
}

func (c *githubClient) CreateCommit(ctx context.Context, owner, repo string, cred credentials.Credential, req CreateCommitRequest) (string, error) {
	payload := map[string]any{
		"message": req.Message,
		"tree":    req.TreeSHA,
		"parents": req.Parents,
	}
	if req.Author != nil {
		payload["author"] = req.Author
	}
	if req.Committer != nil {
		payload["committer"] = req.Committer
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return "", fmt.Errorf("marshal create-commit: %w", err)
	}
	url := fmt.Sprintf("https://api.github.com/repos/%s/%s/git/commits", owner, repo)
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return "", fmt.Errorf("create create-commit request: %w", err)
	}
	if err := authHeaders(ctx, httpReq, cred); err != nil {
		return "", err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return "", fmt.Errorf("github create-commit: %w", err)
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusCreated {
		return "", fmt.Errorf("github create-commit (status %d): %s", resp.StatusCode, string(respBody))
	}
	var parsed struct {
		SHA string `json:"sha"`
	}
	if err := json.Unmarshal(respBody, &parsed); err != nil {
		return "", fmt.Errorf("decode create-commit: %w", err)
	}
	return parsed.SHA, nil
}

func (c *githubClient) UpdateRef(ctx context.Context, owner, repo string, cred credentials.Credential, ref, sha string, force bool) error {
	payload := map[string]any{
		"sha":   sha,
		"force": force,
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal update-ref: %w", err)
	}
	url := fmt.Sprintf("https://api.github.com/repos/%s/%s/git/refs/%s", owner, repo, ref)
	req, err := http.NewRequestWithContext(ctx, http.MethodPatch, url, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("create update-ref request: %w", err)
	}
	if err := authHeaders(ctx, req, cred); err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("github update-ref: %w", err)
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode == http.StatusOK {
		return nil
	}
	// Non-fast-forward returns 422 with a descriptive message. Treat any 422
	// on this endpoint as a CAS failure so the retry loop can re-anchor.
	if resp.StatusCode == http.StatusUnprocessableEntity {
		return fmt.Errorf("%w: %s", ErrRefNotFastForward, string(respBody))
	}
	return fmt.Errorf("github update-ref (status %d): %s", resp.StatusCode, string(respBody))
}

func (c *githubClient) CreateTagObject(ctx context.Context, owner, repo string, cred credentials.Credential, req CreateTagObjectRequest) (string, error) {
	objType := req.Type
	if objType == "" {
		objType = "commit"
	}
	payload := map[string]any{
		"tag":     req.Tag,
		"message": req.Message,
		"object":  req.Object,
		"type":    objType,
	}
	if req.Tagger != nil {
		payload["tagger"] = req.Tagger
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return "", fmt.Errorf("marshal create-tag-object: %w", err)
	}
	url := fmt.Sprintf("https://api.github.com/repos/%s/%s/git/tags", owner, repo)
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return "", fmt.Errorf("create create-tag-object request: %w", err)
	}
	if err := authHeaders(ctx, httpReq, cred); err != nil {
		return "", err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return "", fmt.Errorf("github create-tag-object: %w", err)
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusCreated {
		return "", fmt.Errorf("github create-tag-object (status %d): %s", resp.StatusCode, string(respBody))
	}
	var parsed struct {
		SHA string `json:"sha"`
	}
	if err := json.Unmarshal(respBody, &parsed); err != nil {
		return "", fmt.Errorf("decode create-tag-object: %w", err)
	}
	return parsed.SHA, nil
}

func (c *githubClient) CreateTagRef(ctx context.Context, owner, repo string, cred credentials.Credential, tagName, tagObjectSHA string) error {
	payload := map[string]any{
		"ref": "refs/tags/" + tagName,
		"sha": tagObjectSHA,
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal create-tag-ref: %w", err)
	}
	url := fmt.Sprintf("https://api.github.com/repos/%s/%s/git/refs", owner, repo)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("create create-tag-ref request: %w", err)
	}
	if err := authHeaders(ctx, req, cred); err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("github create-tag-ref: %w", err)
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode == http.StatusCreated {
		return nil
	}
	// 422 with "Reference already exists" is the tag-collision case.
	if resp.StatusCode == http.StatusUnprocessableEntity {
		return fmt.Errorf("%w: %s", ErrTagAlreadyExists, string(respBody))
	}
	return fmt.Errorf("github create-tag-ref (status %d): %s", resp.StatusCode, string(respBody))
}

func (c *githubClient) ListMatchingRefs(ctx context.Context, owner, repo string, cred credentials.Credential, prefix string) ([]MatchingRef, error) {
	url := fmt.Sprintf("https://api.github.com/repos/%s/%s/git/matching-refs/%s", owner, repo, prefix)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("create list-matching-refs request: %w", err)
	}
	if err := authHeaders(ctx, req, cred); err != nil {
		return nil, err
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("github list-matching-refs: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode == http.StatusNotFound {
		// Some App installations return 404 instead of 200 + empty list when
		// no refs match. Normalise to "empty list" — same caller intent.
		return []MatchingRef{}, nil
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("github list-matching-refs (status %d): %s", resp.StatusCode, string(body))
	}
	var parsed []struct {
		Ref    string `json:"ref"`
		Object struct {
			SHA string `json:"sha"`
		} `json:"object"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil, fmt.Errorf("decode list-matching-refs: %w", err)
	}
	out := make([]MatchingRef, 0, len(parsed))
	for _, r := range parsed {
		out = append(out, MatchingRef{Ref: r.Ref, SHA: r.Object.SHA})
	}
	return out, nil
}

// stripNewlines removes literal CR/LF from a base64 string (GitHub returns
// them in the contents response for human-readable wrapping).
func stripNewlines(s string) string {
	out := make([]byte, 0, len(s))
	for i := 0; i < len(s); i++ {
		if s[i] == '\n' || s[i] == '\r' {
			continue
		}
		out = append(out, s[i])
	}
	return string(out)
}
