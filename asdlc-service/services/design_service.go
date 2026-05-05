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
	"regexp"
	"strings"

	"github.com/wso2/asdlc/asdlc-service/clients/agents"
	"github.com/wso2/asdlc/asdlc-service/clients/gitservice"
	"github.com/wso2/asdlc/asdlc-service/models"
)

// invalidNameChars strips anything that isn't lowercase alphanumeric or a hyphen.
var invalidNameChars = regexp.MustCompile(`[^a-z0-9-]`)

// ocEntrypoint maps the AI-generated componentType to the OC component type reference.
func ocEntrypoint(componentType string) string {
	switch strings.ToLower(componentType) {
	case "web-app":
		return "deployment/web-application"
	case "scheduled-task":
		return "cronjob/scheduled-task"
	default:
		return "deployment/service"
	}
}

// toK8sName converts a human-readable name to an RFC 1123 compliant k8s name.
func toK8sName(name string) string {
	s := strings.ToLower(strings.TrimSpace(name))
	s = strings.ReplaceAll(s, " ", "-")
	s = invalidNameChars.ReplaceAllString(s, "")
	s = strings.Trim(s, "-")
	if s == "" {
		s = "component"
	}
	return s
}

type DesignService interface {
	GetDesign(ctx context.Context, orgID, projectID string) (*models.Design, error)
	GetDesignAtVersion(ctx context.Context, orgID, projectID string, version int) (*models.Design, error)
	StreamGenerateDesign(ctx context.Context, orgID, projectID string, out io.Writer, flush func()) error
	SaveAndProceed(ctx context.Context, orgID, projectID string) (*models.Design, error)
	DiscardChanges(ctx context.Context, orgID, projectID string) (*models.Design, error)
	ListDesignVersions(ctx context.Context, orgID, projectID string) ([]models.ArtifactVersion, error)
}

type designService struct {
	store        *ArtifactStore
	agentsClient agents.Client
	gitClient    gitservice.Client
	taskSvc      TaskService // for SaveAndProceed reconciliation; may be nil in tests
}

// DesignServiceWithTaskHook lets the construction wire-up surface the
// reconciliation hook setter without polluting the public DesignService
// interface (it's a one-off internal need).
type DesignServiceWithTaskHook interface {
	DesignService
	SetTaskService(taskSvc TaskService)
}

func NewDesignService(
	store *ArtifactStore,
	agentsClient agents.Client,
	gitClient gitservice.Client,
) DesignService {
	return &designService{
		store:        store,
		agentsClient: agentsClient,
		gitClient:    gitClient,
	}
}

// SetTaskService wires the task service for the SaveAndProceed reconciliation
// hook. Done as a setter (rather than a constructor arg) to avoid the
// design ↔ task circular dependency at construction time in main.go.
func (s *designService) SetTaskService(taskSvc TaskService) {
	s.taskSvc = taskSvc
}

func (s *designService) GetDesign(ctx context.Context, orgID, projectID string) (*models.Design, error) {
	designFile, err := s.store.ReadDesign(ctx, orgID, projectID)
	if err != nil {
		if IsNotFound(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read design: %w", err)
	}
	if designFile == nil {
		return nil, nil
	}

	version := 0
	status := "draft"
	var versions []models.ArtifactVersion

	if s.gitClient != nil {
		v, err := s.gitClient.ListDesignVersions(ctx, orgID, projectID)
		if err != nil {
			slog.WarnContext(ctx, "failed to list design versions", "error", err)
		} else {
			versions = mapArtifactVersions(v)
			if len(v) > 0 {
				version = v[0].Version
				status = "approved"
			}
		}
	}

	// hasUnsavedChanges: compare working-tree raw bytes to latest tag's
	// raw bytes. The BFF used to do this via parseLineage + ReadRawFile;
	// now both halves come over HTTP with structured shapes.
	unsaved := false
	if version > 0 && s.gitClient != nil {
		raw, err := s.store.ReadRawDesign(ctx, orgID, projectID)
		if err == nil {
			tagged, err := s.gitClient.GetDesignVersion(ctx, orgID, projectID, version)
			if err == nil && strings.TrimSpace(tagged.Content) != strings.TrimSpace(raw) {
				unsaved = true
			}
		}
	}

	// Lineage resolution. The file's SourceSpec reflects the current
	// working copy (set at stream/write time); the latest tag's SourceSpec
	// reflects the last approved snapshot. When the working copy has
	// diverged from the last tag we report the file's lineage; otherwise
	// fall back to the latest version's structured lineage.
	sourceSpec := designFile.SourceSpec
	if sourceSpec == "" && len(versions) > 0 {
		sourceSpec = versions[0].SourceSpec
	}

	return &models.Design{
		ProjectID:         projectID,
		OrgID:             orgID,
		Overview:          designFile.Overview,
		Requirements:      designFile.Requirements,
		Components:        designFile.Components,
		Status:            status,
		Version:           version,
		Versions:          versions,
		HasUnsavedChanges: unsaved,
		SourceSpec:        sourceSpec,
	}, nil
}

func (s *designService) GetDesignAtVersion(ctx context.Context, orgID, projectID string, version int) (*models.Design, error) {
	if s.gitClient == nil {
		return nil, fmt.Errorf("git client not configured")
	}

	res, err := s.gitClient.GetDesignVersion(ctx, orgID, projectID, version)
	if err != nil {
		if errors.Is(err, gitservice.ErrArtifactNotFound) {
			return nil, ErrDesignNotFound
		}
		return nil, fmt.Errorf("get design at version %d: %w", version, err)
	}

	designFile, err := ParseDesignJSON(res.Content)
	if err != nil {
		return nil, fmt.Errorf("parse design at version %d: %w", version, err)
	}

	return &models.Design{
		ProjectID:    projectID,
		OrgID:        orgID,
		Overview:     designFile.Overview,
		Requirements: designFile.Requirements,
		Components:   designFile.Components,
		Status:       "approved",
		Version:      version,
		SourceSpec:   res.Lineage.SourceSpec,
	}, nil
}

// streamArchitectFinishData captures the design JSON payload from the
// data-finish event emitted by the agents service.
type streamArchitectFinishData struct {
	Design struct {
		Overview     string                   `json:"overview"`
		Requirements []string                 `json:"requirements"`
		Components   []models.DesignComponent `json:"components"`
	} `json:"design"`
}

func (s *designService) StreamGenerateDesign(ctx context.Context, orgID, projectID string, out io.Writer, flush func()) error {
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

	// Check spec is tagged (approved) — at least one spec-v* version must exist.
	var sourceSpecTag string
	if s.gitClient != nil {
		specVersions, err := s.gitClient.ListSpecVersions(ctx, orgID, projectID)
		if err != nil {
			slog.WarnContext(ctx, "failed to check spec versions", "error", err)
		} else if len(specVersions) == 0 {
			return ErrSpecNotApproved
		} else {
			sourceSpecTag = specVersions[0].Tag
		}
	}

	// Read existing design to pass as context for incremental regeneration.
	var previousDesign *agents.ArchitectDesign
	existingDesign, err := s.store.ReadDesign(ctx, orgID, projectID)
	if err != nil && !IsNotFound(err) {
		slog.WarnContext(ctx, "failed to read existing design for incremental regen", "error", err)
	} else if existingDesign != nil {
		previousDesign = &agents.ArchitectDesign{
			Overview:     existingDesign.Overview,
			Requirements: existingDesign.Requirements,
			Components:   existingDesign.Components,
		}
	}

	slog.InfoContext(ctx, "streaming design via agents service",
		"project", projectID, "hasPrevious", previousDesign != nil)

	upstream, err := s.agentsClient.StreamArchitect(ctx, agents.ArchitectRequest{
		ProjectName:    projectID,
		Spec:           specContent,
		PreviousDesign: previousDesign,
	})
	if err != nil {
		return fmt.Errorf("agents service request: %w", err)
	}
	defer upstream.Close()

	var finalDesign *streamArchitectFinishData
	var streamErr string

	scanner := bufio.NewScanner(upstream)
	scanner.Buffer(make([]byte, 1024*1024), 16*1024*1024)

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

		var head struct {
			Type      string `json:"type"`
			ErrorText string `json:"errorText"`
		}
		if err := json.Unmarshal(payload, &head); err != nil {
			continue
		}
		switch head.Type {
		case "data-finish":
			var frame struct {
				Data streamArchitectFinishData `json:"data"`
			}
			if err := json.Unmarshal(payload, &frame); err != nil {
				slog.WarnContext(ctx, "failed to parse data-finish frame", "error", err)
				continue
			}
			d := frame.Data
			finalDesign = &d
		case "error":
			streamErr = head.ErrorText
		}
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("read upstream: %w", err)
	}

	if streamErr != "" {
		return fmt.Errorf("agents service error: %s", streamErr)
	}
	if finalDesign == nil {
		return fmt.Errorf("agents service closed stream without finishing")
	}

	designFile := &DesignFile{
		Overview:     finalDesign.Design.Overview,
		Requirements: finalDesign.Design.Requirements,
		Components:   finalDesign.Design.Components,
		SourceSpec:   sourceSpecTag,
	}

	if _, err := s.store.WriteDesign(ctx, orgID, projectID, designFile); err != nil {
		return fmt.Errorf("write design: %w", err)
	}

	slog.InfoContext(ctx, "design written from stream",
		"project", projectID, "components", len(designFile.Components))
	return nil
}

// SaveAndProceed collapses to a single SaveDesign call. Lineage from the
// in-file SourceSpec (set at stream-time) is passed in the request — git-service
// uses it as authoritative for the tag annotation. The in-file
// SourceSpec is otherwise ignored by the save handler.
func (s *designService) SaveAndProceed(ctx context.Context, orgID, projectID string) (*models.Design, error) {
	if s.gitClient == nil {
		return nil, fmt.Errorf("git client not configured")
	}

	designFile, err := s.store.ReadDesign(ctx, orgID, projectID)
	if err != nil {
		if IsNotFound(err) {
			return nil, ErrDesignNotFound
		}
		return nil, fmt.Errorf("read design: %w", err)
	}
	if designFile == nil {
		return nil, ErrDesignNotFound
	}

	raw, err := s.store.ReadRawDesign(ctx, orgID, projectID)
	if err != nil {
		return nil, fmt.Errorf("read raw design: %w", err)
	}

	// Lineage: prefer in-file SourceSpec (set during streaming generation);
	// fall back to the current latest spec tag if the file's lineage is
	// empty.
	sourceSpecTag := designFile.SourceSpec
	if sourceSpecTag == "" {
		specVersions, _ := s.gitClient.ListSpecVersions(ctx, orgID, projectID)
		if len(specVersions) > 0 {
			sourceSpecTag = specVersions[0].Tag
		}
	}

	res, err := s.gitClient.SaveDesign(ctx, orgID, projectID, gitservice.SaveArtifactRequest{
		Content: raw,
		Message: "Add ASDLC architecture design",
		Lineage: gitservice.ArtifactLineage{SourceSpec: sourceSpecTag},
	})
	if err != nil {
		return nil, fmt.Errorf("save design: %w", err)
	}

	versions, err := s.gitClient.ListDesignVersions(ctx, orgID, projectID)
	if err != nil {
		slog.WarnContext(ctx, "list versions after save failed", "error", err)
	}

	slog.InfoContext(ctx, "design save completed",
		"project", projectID, "tag", res.Tag, "status", res.Status)

	// Reconciliation hook (docs/design/tech-lead-agent.md §10.4): close any
	// pending tasks whose components were just removed from the design.
	// Best-effort — a failure here doesn't fail the save.
	if s.taskSvc != nil {
		if rerr := s.taskSvc.ReconcilePendingForDesignChange(ctx, orgID, projectID); rerr != nil {
			slog.WarnContext(ctx, "task reconciliation after design save failed", "error", rerr)
		}
	}

	return &models.Design{
		ProjectID:    projectID,
		OrgID:        orgID,
		Overview:     designFile.Overview,
		Requirements: designFile.Requirements,
		Components:   designFile.Components,
		Status:       "approved",
		Version:      res.Version,
		Versions:     mapArtifactVersions(versions),
		SourceSpec:   res.Lineage.SourceSpec,
	}, nil
}

func (s *designService) DiscardChanges(ctx context.Context, orgID, projectID string) (*models.Design, error) {
	if s.gitClient == nil {
		return nil, fmt.Errorf("git client not configured")
	}

	if _, err := s.gitClient.DiscardDesign(ctx, orgID, projectID); err != nil {
		if errors.Is(err, gitservice.ErrArtifactNotFound) {
			return nil, fmt.Errorf("no saved version to revert to")
		}
		return nil, fmt.Errorf("discard design: %w", err)
	}

	return s.GetDesign(ctx, orgID, projectID)
}

func (s *designService) ListDesignVersions(ctx context.Context, orgID, projectID string) ([]models.ArtifactVersion, error) {
	if s.gitClient == nil {
		return []models.ArtifactVersion{}, nil
	}
	v, err := s.gitClient.ListDesignVersions(ctx, orgID, projectID)
	if err != nil {
		return nil, fmt.Errorf("list design versions: %w", err)
	}
	return mapArtifactVersions(v), nil
}
