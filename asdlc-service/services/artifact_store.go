package services

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/wso2/asdlc/asdlc-service/clients/gitservice"
	"github.com/wso2/asdlc/asdlc-service/models"
)

// ArtifactStore wraps the git-service artifact endpoints (PR 1+2 of the
// repo-storage-ownership refactor). The BFF no longer mounts /data/repos —
// every read/write is one HTTP call to git-service, which is the sole owner
// of the working tree on disk.
//
// The struct keeps the same shape the rest of the BFF was using, but the
// methods route through the gitservice client instead of os.ReadFile /
// os.WriteFile. Callers that previously received `("", nil)` for "no
// content" now receive `errors.Is(err, gitservice.ErrArtifactNotFound)`.
type ArtifactStore struct {
	gitClient gitservice.Client
}

func NewArtifactStore(gitClient gitservice.Client) *ArtifactStore {
	return &ArtifactStore{gitClient: gitClient}
}

// ---- Spec (Markdown) ------------------------------------------------------

// ReadSpec returns (content, ErrArtifactNotFound) when no spec.md exists in
// the working tree, and (content, nil) otherwise. Callers branch on
// errors.Is(err, gitservice.ErrArtifactNotFound) rather than empty content.
func (s *ArtifactStore) ReadSpec(ctx context.Context, orgID, projectID string) (string, error) {
	res, err := s.gitClient.GetSpec(ctx, orgID, projectID)
	if err != nil {
		return "", err
	}
	return res.Content, nil
}

// WriteSpec writes spec.md to the working tree. The optional ifMatch sha
// (returned by the previous PUT) gives the streaming caller optimistic
// concurrency control; a stale value yields a 412 from git-service.
func (s *ArtifactStore) WriteSpec(ctx context.Context, orgID, projectID, content string) (sha string, err error) {
	res, err := s.gitClient.PutSpec(ctx, orgID, projectID, gitservice.PutFileRequest{Content: content})
	if err != nil {
		return "", fmt.Errorf("write spec: %w", err)
	}
	return res.SHA, nil
}

// ---- Design (JSON) --------------------------------------------------------

// DesignFile is the JSON structure stored at .asdlc/design.json.
//
// The PUT/GET endpoints are bytes-in / bytes-out — git-service does not
// parse the JSON or rewrite SourceSpec. The BFF still uses this struct for
// in-process serialization and for surfacing the in-file lineage as the
// "draft was generated from" UI label.
type DesignFile struct {
	Overview     string                   `json:"overview"`
	Requirements []string                 `json:"requirements"`
	Components   []models.DesignComponent `json:"components"`
	SourceSpec   string                   `json:"sourceSpec,omitempty"`
}

// ReadDesign decodes the working tree's design.json. Returns
// (nil, ErrArtifactNotFound) when no file exists yet.
func (s *ArtifactStore) ReadDesign(ctx context.Context, orgID, projectID string) (*DesignFile, error) {
	res, err := s.gitClient.GetDesign(ctx, orgID, projectID)
	if err != nil {
		return nil, err
	}
	return parseDesignJSON(res.Content)
}

// ReadRawDesign returns the literal bytes of design.json (used by the UI's
// has-unsaved-changes computation, which compares marshalled-and-back to
// the latest tag's content — going through DesignFile would re-serialize
// and lose canonical formatting).
func (s *ArtifactStore) ReadRawDesign(ctx context.Context, orgID, projectID string) (string, error) {
	res, err := s.gitClient.GetDesign(ctx, orgID, projectID)
	if err != nil {
		return "", err
	}
	return res.Content, nil
}

// WriteDesign serializes the design and writes it. The bytes are
// canonicalized before write (alphabetical keys, status-code coercion,
// x-* preserved) so git-service's byte-equality dedup ignores harmless
// LLM whitespace/order drift across regenerations. See
// openapi_normalize.go for the rule set.
func (s *ArtifactStore) WriteDesign(ctx context.Context, orgID, projectID string, design *DesignFile) (sha string, err error) {
	data, err := json.MarshalIndent(design, "", "  ")
	if err != nil {
		return "", fmt.Errorf("encode design: %w", err)
	}
	canonical, err := normalizeDesignJSON(data)
	if err != nil {
		// Don't fail the save if normalization breaks — fall back to the
		// raw bytes. This keeps the save reliable even for adversarial
		// payloads; the cost is a possible spurious tag bump on next save.
		canonical = data
	}
	res, err := s.gitClient.PutDesign(ctx, orgID, projectID, gitservice.PutFileRequest{Content: string(canonical)})
	if err != nil {
		return "", fmt.Errorf("write design: %w", err)
	}
	return res.SHA, nil
}

// ---- Wireframes -----------------------------------------------------------

// ReadWireframe returns ("", ErrArtifactNotFound) when the named wireframe
// has not been generated yet.
func (s *ArtifactStore) ReadWireframe(ctx context.Context, orgID, projectID, name string) (string, error) {
	res, err := s.gitClient.GetWireframe(ctx, orgID, projectID, name)
	if err != nil {
		return "", err
	}
	return res.Content, nil
}

// WriteWireframe writes a single wireframe HTML file by name.
func (s *ArtifactStore) WriteWireframe(ctx context.Context, orgID, projectID, name, content string) error {
	_, err := s.gitClient.PutWireframe(ctx, orgID, projectID, name, gitservice.PutFileRequest{Content: content})
	if err != nil {
		return fmt.Errorf("write wireframe %s: %w", name, err)
	}
	return nil
}

// ListWireframes returns the file names under .asdlc/wireframes/ (no
// directories, sorted by the filesystem). Empty slice when none.
func (s *ArtifactStore) ListWireframes(ctx context.Context, orgID, projectID string) ([]string, error) {
	entries, err := s.gitClient.ListWireframes(ctx, orgID, projectID)
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		names = append(names, e.Name)
	}
	return names, nil
}

// ---- Helpers --------------------------------------------------------------

// parseDesignJSON is shared between ReadDesign and ParseDesignJSON
// (re-exported for callers like task_service that receive design content
// out-of-band — e.g. from a tagged version fetched directly).
func parseDesignJSON(data string) (*DesignFile, error) {
	if data == "" {
		return nil, nil
	}
	var design DesignFile
	if err := json.Unmarshal([]byte(data), &design); err != nil {
		return nil, fmt.Errorf("decode design: %w", err)
	}
	return &design, nil
}

// IsNotFound is sugar for callers that want to distinguish "no spec yet"
// from a real error without importing the gitservice package.
func IsNotFound(err error) bool { return errors.Is(err, gitservice.ErrArtifactNotFound) }
