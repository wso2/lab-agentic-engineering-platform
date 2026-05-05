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
	"sync"

	"github.com/wso2/asdlc/asdlc-service/clients/agents"
	"github.com/wso2/asdlc/asdlc-service/clients/gitservice"
	"github.com/wso2/asdlc/asdlc-service/models"
)

const specWireframeName = "spec.html"

type SpecService interface {
	GetSpec(ctx context.Context, orgID, projectID string) (*models.Spec, error)
	GetSpecAtVersion(ctx context.Context, orgID, projectID string, version int) (*models.Spec, error)
	UpdateSpec(ctx context.Context, orgID, projectID, content string) (*models.Spec, error)
	SaveAndProceed(ctx context.Context, orgID, projectID string) (*models.Spec, error)
	StreamGenerateSpec(ctx context.Context, orgID, projectID, prompt string, out io.Writer, flush func()) error
	DiscardChanges(ctx context.Context, orgID, projectID string) (*models.Spec, error)
	ListSpecVersions(ctx context.Context, orgID, projectID string) ([]models.ArtifactVersion, error)
	GetSpecWireframe(ctx context.Context, orgID, projectID string) (string, error)
	StartGenerateSpecWireframe(ctx context.Context, orgID, projectID string)
}

type specService struct {
	store                     *ArtifactStore
	agentsClient              agents.Client
	gitClient                 gitservice.Client
	wireframeGenerationStatus sync.Map // key: projectID, value: "generating" | "error:<msg>"
}

func NewSpecService(
	store *ArtifactStore,
	agentsClient agents.Client,
	gitClient gitservice.Client,
) SpecService {
	return &specService{
		store:        store,
		agentsClient: agentsClient,
		gitClient:    gitClient,
	}
}

// GetSpec returns the current working-tree spec plus version metadata.
//
// PR 2 changes: ArtifactStore is HTTP-backed, so ReadSpec is one round-trip.
// ListSpecVersions returns structured Lineage. The "has unsaved changes"
// flag is computed by comparing the working-tree content to the latest
// tagged content (one extra round-trip when versions exist).
func (s *specService) GetSpec(ctx context.Context, orgID, projectID string) (*models.Spec, error) {
	content, err := s.store.ReadSpec(ctx, orgID, projectID)
	if err != nil {
		if IsNotFound(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read spec: %w", err)
	}
	if content == "" {
		return nil, nil
	}

	if s.gitClient == nil {
		return &models.Spec{ProjectID: projectID, Content: content, Status: "draft"}, nil
	}

	versions, err := s.gitClient.ListSpecVersions(ctx, orgID, projectID)
	if err != nil {
		slog.WarnContext(ctx, "failed to list spec versions", "error", err)
		return &models.Spec{ProjectID: projectID, Content: content, Status: "draft"}, nil
	}

	if len(versions) == 0 {
		return &models.Spec{
			ProjectID: projectID,
			Content:   content,
			Status:    "draft",
		}, nil
	}

	latest := versions[0]
	unsaved := false
	if latest.CommitHash != "" {
		taggedContent, err := s.gitClient.GetSpecVersion(ctx, orgID, projectID, latest.Version)
		if err == nil && strings.TrimSpace(taggedContent.Content) != strings.TrimSpace(content) {
			unsaved = true
		}
	}

	return &models.Spec{
		ProjectID:         projectID,
		Content:           content,
		Status:            "approved",
		Version:           latest.Version,
		Versions:          mapArtifactVersions(versions),
		HasUnsavedChanges: unsaved,
	}, nil
}

func (s *specService) GetSpecAtVersion(ctx context.Context, orgID, projectID string, version int) (*models.Spec, error) {
	if s.gitClient == nil {
		return nil, fmt.Errorf("git client not configured")
	}
	res, err := s.gitClient.GetSpecVersion(ctx, orgID, projectID, version)
	if err != nil {
		if errors.Is(err, gitservice.ErrArtifactNotFound) {
			return nil, ErrSpecNotFound
		}
		return nil, fmt.Errorf("get spec at version %d: %w", version, err)
	}
	return &models.Spec{
		ProjectID: projectID,
		Content:   res.Content,
		Status:    "approved",
		Version:   version,
	}, nil
}

func (s *specService) UpdateSpec(ctx context.Context, orgID, projectID, content string) (*models.Spec, error) {
	if _, err := s.store.WriteSpec(ctx, orgID, projectID, content); err != nil {
		return nil, fmt.Errorf("write spec: %w", err)
	}
	return &models.Spec{
		ProjectID: projectID,
		Content:   content,
		Status:    "draft",
	}, nil
}

func (s *specService) StreamGenerateSpec(ctx context.Context, orgID, projectID, prompt string, out io.Writer, flush func()) error {
	slog.InfoContext(ctx, "streaming spec via agents service", "project", projectID)

	composedPrompt := fmt.Sprintf(
		"Project: %s\n\n## What the user wants to build\n%s\n\nProduce a complete requirements document for this project.",
		projectID, prompt,
	)

	upstream, err := s.agentsClient.StreamBusinessAnalyst(ctx, composedPrompt)
	if err != nil {
		return fmt.Errorf("agents service request: %w", err)
	}
	defer upstream.Close()

	var accumulated strings.Builder
	var sawFinish bool
	var streamErr string

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
			Type      string `json:"type"`
			Delta     string `json:"delta"`
			ErrorText string `json:"errorText"`
		}
		if err := json.Unmarshal(payload, &chunk); err != nil {
			continue
		}
		switch chunk.Type {
		case "text-delta":
			accumulated.WriteString(chunk.Delta)
		case "finish":
			sawFinish = true
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

	if _, err := s.store.WriteSpec(ctx, orgID, projectID, content); err != nil {
		return fmt.Errorf("write spec: %w", err)
	}
	slog.InfoContext(ctx, "spec written from stream", "project", projectID, "bytes", len(content))
	return nil
}

// SaveAndProceed collapses to a single SaveSpec call that runs the full
// atomic flow on git-service (write + stage + commit + push + tag) under
// one mutex hold. Replaces the BFF's old multi-step
// Commit→Push→ListTags→hasChanged→CreateTag chain.
func (s *specService) SaveAndProceed(ctx context.Context, orgID, projectID string) (*models.Spec, error) {
	if s.gitClient == nil {
		return nil, fmt.Errorf("git client not configured")
	}

	content, err := s.store.ReadSpec(ctx, orgID, projectID)
	if err != nil {
		if IsNotFound(err) {
			return nil, ErrSpecNotFound
		}
		return nil, fmt.Errorf("read spec: %w", err)
	}
	if content == "" {
		return nil, ErrSpecNotFound
	}

	res, err := s.gitClient.SaveSpec(ctx, orgID, projectID, gitservice.SaveArtifactRequest{
		Content: content,
		Message: "Add ASDLC specification",
	})
	if err != nil {
		return nil, fmt.Errorf("save spec: %w", err)
	}

	versions, err := s.gitClient.ListSpecVersions(ctx, orgID, projectID)
	if err != nil {
		slog.WarnContext(ctx, "list versions after save failed", "error", err)
	}

	slog.InfoContext(ctx, "spec save completed",
		"project", projectID, "tag", res.Tag, "status", res.Status)

	return &models.Spec{
		ProjectID: projectID,
		Content:   content,
		Status:    "approved",
		Version:   res.Version,
		Versions:  mapArtifactVersions(versions),
	}, nil
}

func (s *specService) DiscardChanges(ctx context.Context, orgID, projectID string) (*models.Spec, error) {
	if s.gitClient == nil {
		return nil, fmt.Errorf("git client not configured")
	}

	if _, err := s.gitClient.DiscardSpec(ctx, orgID, projectID); err != nil {
		if errors.Is(err, gitservice.ErrArtifactNotFound) {
			return nil, fmt.Errorf("no saved version to revert to")
		}
		return nil, fmt.Errorf("discard spec: %w", err)
	}

	return s.GetSpec(ctx, orgID, projectID)
}

func (s *specService) ListSpecVersions(ctx context.Context, orgID, projectID string) ([]models.ArtifactVersion, error) {
	if s.gitClient == nil {
		return []models.ArtifactVersion{}, nil
	}
	versions, err := s.gitClient.ListSpecVersions(ctx, orgID, projectID)
	if err != nil {
		return nil, fmt.Errorf("list spec versions: %w", err)
	}
	return mapArtifactVersions(versions), nil
}

func (s *specService) GetSpecWireframe(ctx context.Context, orgID, projectID string) (string, error) {
	if val, ok := s.wireframeGenerationStatus.Load(projectID); ok {
		status := val.(string)
		if status == "generating" {
			return "", ErrWireframeGenerating
		}
		if msg, ok := strings.CutPrefix(status, "error:"); ok {
			return "", fmt.Errorf("%s", msg)
		}
	}

	content, err := s.store.ReadWireframe(ctx, orgID, projectID, specWireframeName)
	if err != nil {
		if IsNotFound(err) {
			return "", ErrWireframeNotGenerated
		}
		return "", fmt.Errorf("read wireframe: %w", err)
	}
	if content == "" {
		return "", ErrWireframeNotGenerated
	}
	return content, nil
}

func (s *specService) StartGenerateSpecWireframe(ctx context.Context, orgID, projectID string) {
	s.wireframeGenerationStatus.Store(projectID, "generating")

	go func() {
		err := s.generateSpecWireframeSync(context.Background(), orgID, projectID)
		if err != nil {
			slog.Error("spec wireframe generation failed", "project", projectID, "error", err)
			s.wireframeGenerationStatus.Store(projectID, "error:"+err.Error())
		} else {
			s.wireframeGenerationStatus.Delete(projectID)
		}
	}()
}

func (s *specService) generateSpecWireframeSync(ctx context.Context, orgID, projectID string) error {
	specContent, err := s.store.ReadSpec(ctx, orgID, projectID)
	if err != nil {
		if IsNotFound(err) {
			return ErrSpecNotFound
		}
		return fmt.Errorf("read spec: %w", err)
	}
	if specContent == "" {
		return ErrSpecNotFound
	}

	// Pass previous tagged spec if one exists — gives the prompt a delta
	// to focus on. Best-effort; missing previous version is normal on
	// first-generation runs.
	var previousSpec string
	if s.gitClient != nil {
		versions, listErr := s.gitClient.ListSpecVersions(ctx, orgID, projectID)
		if listErr == nil && len(versions) > 0 {
			latest := versions[0]
			prev, _ := s.gitClient.GetSpecVersion(ctx, orgID, projectID, latest.Version)
			if prev != nil && prev.Content != specContent {
				previousSpec = prev.Content
			}
		}
	}

	slog.InfoContext(ctx, "generating spec wireframe", "project", projectID, "hasPrevious", previousSpec != "")

	result, err := s.agentsClient.GenerateWireframe(ctx, agents.WireframeRequest{
		Spec:         specContent,
		PreviousSpec: previousSpec,
	})
	if err != nil {
		return fmt.Errorf("agent wireframe generation: %w", err)
	}

	if err := s.store.WriteWireframe(ctx, orgID, projectID, specWireframeName, result.Content); err != nil {
		return fmt.Errorf("write wireframe: %w", err)
	}

	slog.InfoContext(ctx, "spec wireframe generated", "project", projectID, "bytes", len(result.Content))
	return nil
}

// ParseDesignJSON re-exposes the artifact-store's internal parser for
// callers that receive design content out of band (e.g. task generation
// reading a design from a tagged version).
func ParseDesignJSON(data string) (*DesignFile, error) {
	df, err := parseDesignJSON(data)
	if err != nil {
		return nil, err
	}
	if df == nil {
		return nil, fmt.Errorf("decode design: empty content")
	}
	return df, nil
}
