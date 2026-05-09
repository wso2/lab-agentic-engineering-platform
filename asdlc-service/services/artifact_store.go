package services

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/wso2/asdlc/asdlc-service/clients/gitservice"
	"github.com/wso2/asdlc/asdlc-service/models"
)

// ArtifactStore wraps the git-service artifact endpoints. The BFF doesn't
// touch /data/repos directly — every read/write is one HTTP call to
// git-service which is the sole owner of the working tree on disk.
type ArtifactStore struct {
	gitClient gitservice.Client
}

func NewArtifactStore(gitClient gitservice.Client) *ArtifactStore {
	return &ArtifactStore{gitClient: gitClient}
}

// ---- Requirements (multi-file Markdown directory) -----------------------

// RequirementsMainFile is the canonical primary requirements document. It
// cannot be deleted/renamed via the API — controllers should reject
// destructive operations on it.
const RequirementsMainFile = "requirements.md"

// ListRequirements returns the working-tree file map under
// `.asdlc/requirements/`. A first-time project with no requirements yet
// returns an empty map (not an error).
func (s *ArtifactStore) ListRequirements(ctx context.Context, orgID, projectID string) (map[string]string, error) {
	files, err := s.gitClient.ListRequirements(ctx, orgID, projectID)
	if err != nil {
		return nil, err
	}
	if files == nil {
		files = map[string]string{}
	}
	return files, nil
}

// ReadRequirementFile reads a single requirement file by basename.
func (s *ArtifactStore) ReadRequirementFile(ctx context.Context, orgID, projectID, name string) (string, error) {
	res, err := s.gitClient.GetRequirementFile(ctx, orgID, projectID, name)
	if err != nil {
		return "", err
	}
	return res.Content, nil
}

// WriteRequirementFile creates or overwrites a single requirement file.
// The optional ifMatch sha (returned by the previous PUT) gives the
// streaming caller optimistic concurrency control.
func (s *ArtifactStore) WriteRequirementFile(ctx context.Context, orgID, projectID, name, content string) (sha string, err error) {
	res, err := s.gitClient.PutRequirementFile(ctx, orgID, projectID, name, gitservice.PutFileRequest{Content: content})
	if err != nil {
		return "", fmt.Errorf("write requirement file %q: %w", name, err)
	}
	return res.SHA, nil
}

// DeleteRequirementFile removes a requirement file from the working tree.
// The change is persisted on the next SaveRequirements call.
func (s *ArtifactStore) DeleteRequirementFile(ctx context.Context, orgID, projectID, name string) error {
	if name == RequirementsMainFile {
		return fmt.Errorf("cannot delete %s", RequirementsMainFile)
	}
	if err := s.gitClient.DeleteRequirementFile(ctx, orgID, projectID, name); err != nil {
		return fmt.Errorf("delete requirement file %q: %w", name, err)
	}
	return nil
}

// ---- Design (JSON) ------------------------------------------------------

// DesignFile is the JSON structure stored at .asdlc/design.json.
//
// SourceSpec carries the parent requirements tag (e.g. "v2") that this
// design was generated against. It's set at stream time; SaveDesign relies
// on the latest tag list for the canonical lineage instead.
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

// ReadRawDesign returns the literal bytes of design.json (used by the
// has-unsaved-changes computation).
func (s *ArtifactStore) ReadRawDesign(ctx context.Context, orgID, projectID string) (string, error) {
	res, err := s.gitClient.GetDesign(ctx, orgID, projectID)
	if err != nil {
		return "", err
	}
	return res.Content, nil
}

// WriteDesign serializes the design and writes it. The bytes are
// canonicalised before write (alphabetical keys, status-code coercion,
// x-* preserved) so git-service's byte-equality dedup ignores harmless
// LLM whitespace/order drift across regenerations.
func (s *ArtifactStore) WriteDesign(ctx context.Context, orgID, projectID string, design *DesignFile) (sha string, err error) {
	data, err := json.MarshalIndent(design, "", "  ")
	if err != nil {
		return "", fmt.Errorf("encode design: %w", err)
	}
	canonical, err := normalizeDesignJSON(data)
	if err != nil {
		canonical = data
	}
	res, err := s.gitClient.PutDesign(ctx, orgID, projectID, gitservice.PutFileRequest{Content: string(canonical)})
	if err != nil {
		return "", fmt.Errorf("write design: %w", err)
	}
	return res.SHA, nil
}

// ---- Helpers ------------------------------------------------------------

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

// IsNotFound is sugar for callers that want to distinguish "no artifact yet"
// from a real error without importing the gitservice package.
func IsNotFound(err error) bool { return errors.Is(err, gitservice.ErrArtifactNotFound) }
