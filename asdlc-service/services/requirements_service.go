package services

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"

	"github.com/wso2/asdlc/asdlc-service/clients/agents"
	"github.com/wso2/asdlc/asdlc-service/clients/gitservice"
	"github.com/wso2/asdlc/asdlc-service/models"
)

// RequirementsService manages the multi-file requirements bundle stored at
// `.asdlc/requirements/*.md`. The bundle is versioned together as `v<N>`
// tags (one bump per save). Generation is skill-routed: `requirements.md`
// is bootstrapped from a user prompt; sibling docs (functional, NFR, user
// stories) are derived from existing files via document-generation skills.
type RequirementsService interface {
	GetRequirements(ctx context.Context, orgID, projectID string) (*models.RequirementsBundle, error)
	GetRequirementsAtTag(ctx context.Context, orgID, projectID, tag string) (*models.RequirementsBundle, error)
	UpdateRequirementFile(ctx context.Context, orgID, projectID, name, content string) (*models.RequirementsBundle, error)
	DeleteRequirementFile(ctx context.Context, orgID, projectID, name string) (*models.RequirementsBundle, error)
	SaveAndProceed(ctx context.Context, orgID, projectID string) (*models.RequirementsBundle, error)
	DiscardChanges(ctx context.Context, orgID, projectID string) (*models.RequirementsBundle, error)
	ListVersions(ctx context.Context, orgID, projectID string) ([]models.ArtifactVersion, error)

	// StreamGenerate runs a document-generation skill against the named
	// target file. `skillID` is supplied by the caller (looked up from the
	// document-type registry on the BFF side); `sourceNames` enumerates
	// sibling requirement files to read as context. `prompt` is the
	// optional user prompt for bootstrap skills (ignored when sources are
	// provided).
	StreamGenerate(ctx context.Context, orgID, projectID, name, skillID string, sourceNames []string, prompt string, out io.Writer, flush func()) error
}

type requirementsService struct {
	store        *ArtifactStore
	agentsClient agents.Client
	gitClient    gitservice.Client
}

func NewRequirementsService(
	store *ArtifactStore,
	agentsClient agents.Client,
	gitClient gitservice.Client,
) RequirementsService {
	return &requirementsService{
		store:        store,
		agentsClient: agentsClient,
		gitClient:    gitClient,
	}
}

// GetRequirements returns the working-tree file map plus version metadata.
// Empty directory yields a draft with no files (callers can render the
// "no requirements yet" state). Has-unsaved-changes is computed by
// comparing the working-tree file map to the latest tagged snapshot.
func (s *requirementsService) GetRequirements(ctx context.Context, orgID, projectID string) (*models.RequirementsBundle, error) {
	files, err := s.store.ListRequirements(ctx, orgID, projectID)
	if err != nil {
		return nil, fmt.Errorf("list requirements: %w", err)
	}

	out := &models.RequirementsBundle{
		ProjectID: projectID,
		Files:     files,
		Status:    "draft",
	}
	if len(files) == 0 {
		return out, nil
	}

	if s.gitClient == nil {
		return out, nil
	}

	versions, err := s.gitClient.ListRequirementsVersions(ctx, orgID, projectID)
	if err != nil {
		slog.WarnContext(ctx, "list requirements versions failed", "error", err)
		return out, nil
	}
	out.Versions = mapRequirementsVersions(versions)
	if len(versions) > 0 {
		out.Status = "approved"
		out.Version = versions[0].Version

		// Has-unsaved-changes: compare working tree to snapshot at latest tag.
		tagged, err := s.gitClient.GetRequirementsAtTag(ctx, orgID, projectID, versions[0].Tag)
		if err == nil && !fileMapsEqual(tagged, files) {
			out.HasUnsavedChanges = true
		}
	}
	return out, nil
}

func (s *requirementsService) GetRequirementsAtTag(ctx context.Context, orgID, projectID, tag string) (*models.RequirementsBundle, error) {
	if s.gitClient == nil {
		return nil, fmt.Errorf("git client not configured")
	}
	files, err := s.gitClient.GetRequirementsAtTag(ctx, orgID, projectID, tag)
	if err != nil {
		if errors.Is(err, gitservice.ErrArtifactNotFound) {
			return nil, ErrSpecNotFound
		}
		return nil, fmt.Errorf("get requirements at %s: %w", tag, err)
	}
	return &models.RequirementsBundle{
		ProjectID: projectID,
		Files:     files,
		Status:    "approved",
	}, nil
}

func (s *requirementsService) UpdateRequirementFile(ctx context.Context, orgID, projectID, name, content string) (*models.RequirementsBundle, error) {
	if _, err := s.store.WriteRequirementFile(ctx, orgID, projectID, name, content); err != nil {
		return nil, err
	}
	return s.GetRequirements(ctx, orgID, projectID)
}

func (s *requirementsService) DeleteRequirementFile(ctx context.Context, orgID, projectID, name string) (*models.RequirementsBundle, error) {
	if err := s.store.DeleteRequirementFile(ctx, orgID, projectID, name); err != nil {
		return nil, err
	}
	return s.GetRequirements(ctx, orgID, projectID)
}

// SaveAndProceed persists the working-tree directory as a new `v<N>` tag.
// Requires `requirements.md` to exist (enforced by git-service).
func (s *requirementsService) SaveAndProceed(ctx context.Context, orgID, projectID string) (*models.RequirementsBundle, error) {
	if s.gitClient == nil {
		return nil, fmt.Errorf("git client not configured")
	}
	res, err := s.gitClient.SaveRequirements(ctx, orgID, projectID, gitservice.SaveArtifactRequest{
		Message: "Update requirements",
	})
	if err != nil {
		return nil, fmt.Errorf("save requirements: %w", err)
	}
	slog.InfoContext(ctx, "requirements save completed",
		"project", projectID, "tag", res.Tag, "status", res.Status)
	return s.GetRequirements(ctx, orgID, projectID)
}

func (s *requirementsService) DiscardChanges(ctx context.Context, orgID, projectID string) (*models.RequirementsBundle, error) {
	if s.gitClient == nil {
		return nil, fmt.Errorf("git client not configured")
	}
	if _, err := s.gitClient.DiscardRequirements(ctx, orgID, projectID); err != nil {
		if errors.Is(err, gitservice.ErrArtifactNotFound) {
			return nil, fmt.Errorf("no saved version to revert to")
		}
		return nil, fmt.Errorf("discard requirements: %w", err)
	}
	return s.GetRequirements(ctx, orgID, projectID)
}

func (s *requirementsService) ListVersions(ctx context.Context, orgID, projectID string) ([]models.ArtifactVersion, error) {
	if s.gitClient == nil {
		return nil, nil
	}
	versions, err := s.gitClient.ListRequirementsVersions(ctx, orgID, projectID)
	if err != nil {
		return nil, fmt.Errorf("list requirements versions: %w", err)
	}
	return mapRequirementsVersions(versions), nil
}

// StreamGenerate runs the named skill against the working tree, streaming
// SSE deltas to `out` and writing the final accumulated content into the
// target file. The skill is responsible for the prompt; this service is
// just the glue layer that fetches sources, posts to agents-service, and
// persists the result.
func (s *requirementsService) StreamGenerate(
	ctx context.Context,
	orgID, projectID, name, skillID string,
	sourceNames []string,
	prompt string,
	out io.Writer,
	flush func(),
) error {
	if skillID == "" {
		return fmt.Errorf("skillID is required")
	}
	if name == "" {
		return fmt.Errorf("target filename is required")
	}

	// Gather source files. Missing source files are tolerated — skills can
	// degrade gracefully (e.g. "user-stories" works with just `requirements.md`
	// if `functional-requirements.md` doesn't exist yet).
	sources := make(map[string]string, len(sourceNames))
	for _, src := range sourceNames {
		content, err := s.store.ReadRequirementFile(ctx, orgID, projectID, src)
		if err != nil {
			if errors.Is(err, gitservice.ErrArtifactNotFound) {
				continue
			}
			return fmt.Errorf("read source %q: %w", src, err)
		}
		sources[src] = content
	}

	slog.InfoContext(ctx, "streaming document generation",
		"project", projectID, "skill", skillID, "target", name,
		"sources", len(sources), "hasPrompt", prompt != "")

	upstream, err := s.agentsClient.StreamDocumentGeneration(ctx, orgID, skillID, agents.DocumentGenerationRequest{
		Sources: sources,
		Prompt:  prompt,
	})
	if err != nil {
		return fmt.Errorf("agents service request: %w", err)
	}
	defer upstream.Close()

	var accumulated strings.Builder
	var sawFinish bool
	var streamErr string
	// siblings is populated when the agents-service skill's post-processor
	// emits additional output files alongside the primary stream (e.g.
	// wireframes/domain-model writing both `<name>.dsl` and
	// `<name>.excalidraw`).
	var siblings map[string]string

	scanner := bufio.NewScanner(upstream)
	scanner.Buffer(make([]byte, 1024*1024), 4*1024*1024)

	for scanner.Scan() {
		line := scanner.Bytes()
		if _, err := out.Write(line); err != nil {
			return fmt.Errorf("write downstream: %w", err)
		}
		if _, err := out.Write([]byte("\n")); err != nil {
			return fmt.Errorf("write downstream: %w", err)
		}
		flush()

		if !bytes.HasPrefix(line, []byte("data: ")) {
			continue
		}
		payload := bytes.TrimPrefix(line, []byte("data: "))
		if bytes.Equal(payload, []byte("[DONE]")) {
			continue
		}
		var chunk struct {
			Type      string            `json:"type"`
			Delta     string            `json:"delta"`
			ErrorText string            `json:"errorText"`
			Siblings  map[string]string `json:"siblings,omitempty"`
			// Replace, when set, signals that the skill has post-processed
			// the live deltas and this delta carries the final payload to
			// persist (e.g. wireframes/domain-model: DSL -> Excalidraw JSON).
			// We discard everything previously accumulated.
			Replace bool `json:"replace,omitempty"`
		}
		if err := json.Unmarshal(payload, &chunk); err != nil {
			continue
		}
		switch chunk.Type {
		case "text-delta":
			if chunk.Replace {
				accumulated.Reset()
			}
			accumulated.WriteString(chunk.Delta)
		case "finish":
			sawFinish = true
			if len(chunk.Siblings) > 0 {
				siblings = chunk.Siblings
			}
		case "error":
			streamErr = chunk.ErrorText
		}
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("read upstream: %w", err)
	}
	if streamErr != "" {
		return fmt.Errorf("agents service error: %s", streamErr)
	}
	if !sawFinish {
		return fmt.Errorf("agents service closed stream without finishing")
	}

	content := accumulated.String()
	if content == "" {
		return fmt.Errorf("agents service returned no content")
	}

	if _, err := s.store.WriteRequirementFile(ctx, orgID, projectID, name, content); err != nil {
		return fmt.Errorf("write %s: %w", name, err)
	}
	for sName, sContent := range siblings {
		if sName == name {
			continue
		}
		if _, err := s.store.WriteRequirementFile(ctx, orgID, projectID, sName, sContent); err != nil {
			slog.WarnContext(ctx, "failed to write sibling file",
				"target", sName, "error", err)
		}
	}
	slog.InfoContext(ctx, "document written from stream",
		"project", projectID, "target", name, "bytes", len(content),
		"siblings", len(siblings))
	return nil
}

// fileMapsEqual compares two filename→content maps for byte-equality
// (after trimming surrounding whitespace, which matches git-service's
// unchanged-detection on save).
func fileMapsEqual(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for k, va := range a {
		vb, ok := b[k]
		if !ok {
			return false
		}
		if strings.TrimSpace(va) != strings.TrimSpace(vb) {
			return false
		}
	}
	return true
}

// AssembleDesignFromFiles wraps the artifact-store assembler and rejects an
// empty file map. Used by callers that receive a tagged design file map out
// of band (e.g. task generation reading a design from a tagged version).
func AssembleDesignFromFiles(files map[string]string) (*DesignFile, error) {
	if len(files) == 0 {
		return nil, fmt.Errorf("decode design: empty file map")
	}
	return AssembleDesign(files)
}
