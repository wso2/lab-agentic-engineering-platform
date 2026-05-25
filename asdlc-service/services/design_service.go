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
	"sort"
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

// ToK8sName is an exported alias for toK8sName so callers outside the
// services package (e.g. webhook/trait_sync_watcher) can produce the
// same k8s-shaped slug the dispatch path uses.
func ToK8sName(name string) string { return toK8sName(name) }

// DesignBundle is the file-map view returned to the architecture page. It
// pairs the raw per-file working-tree contents (used by the Explorer) with
// the assembled flat Design (used by the cell diagram + downstream code).
type DesignBundle struct {
	Files  map[string]string `json:"files"`
	Design *models.Design    `json:"design"`
}

type DesignService interface {
	GetDesign(ctx context.Context, orgID, projectID string) (*models.Design, error)
	GetDesignBundle(ctx context.Context, orgID, projectID string) (*DesignBundle, error)
	GetDesignAtTag(ctx context.Context, orgID, projectID, tag string) (*models.Design, error)
	GetDesignBundleAtTag(ctx context.Context, orgID, projectID, tag string) (*DesignBundle, error)
	StreamGenerateDesign(ctx context.Context, orgID, projectID string, out io.Writer, flush func()) error
	UpdateDesignFile(ctx context.Context, orgID, projectID, subPath, content string) (*DesignBundle, error)
	DeleteDesignFile(ctx context.Context, orgID, projectID, subPath string) (*DesignBundle, error)
	DeleteComponent(ctx context.Context, orgID, projectID, componentName string) (*DesignBundle, error)
	SaveAndProceed(ctx context.Context, orgID, projectID string) (*models.Design, error)
	DiscardChanges(ctx context.Context, orgID, projectID string) (*models.Design, error)
	ListDesignVersions(ctx context.Context, orgID, projectID string) ([]models.ArtifactVersion, error)
}

type designService struct {
	store        *ArtifactStore
	agentsClient agents.Client
	gitClient    gitservice.Client
	taskSvc      TaskService // for SaveAndProceed reconciliation; may be nil in tests
	// traitSync, when non-nil, is invoked after a per-component design
	// edit so an `exposesAPI.auth` toggle propagates to the OC Component +
	// ReleaseBindings without waiting for the next dispatch. Set via
	// SetTraitSync. Optional in tests.
	traitSync *TraitSyncService
	// skillSvc resolves the per-org skill catalogue for the architect input.
	// Optional in tests; nil → architect runs with no skills attached
	// (equivalent to pre-skills-system behaviour).
	skillSvc *SkillService
}

// DesignServiceWithTaskHook lets the construction wire-up surface the
// reconciliation hook setter without polluting the public DesignService
// interface.
type DesignServiceWithTaskHook interface {
	DesignService
	SetTaskService(taskSvc TaskService)
}

// DesignServiceWithTraitSync surfaces the trait_sync setter so an
// `exposesAPI.auth` toggle on design.md propagates to OC after the file is
// written. Mirrors the DesignServiceWithTaskHook pattern.
type DesignServiceWithTraitSync interface {
	DesignService
	SetTraitSync(traitSync *TraitSyncService)
}

// DesignServiceWithSkills surfaces the skill-catalogue setter so the
// architect call ships the org's skill set as input.
type DesignServiceWithSkills interface {
	DesignService
	SetSkillService(svc *SkillService)
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

func (s *designService) SetTaskService(taskSvc TaskService) {
	s.taskSvc = taskSvc
}

func (s *designService) SetTraitSync(traitSync *TraitSyncService) {
	s.traitSync = traitSync
}

func (s *designService) SetSkillService(svc *SkillService) {
	s.skillSvc = svc
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

	tag := ""
	revision := 0
	parentReq := ""
	status := "draft"
	var versions []models.ArtifactVersion

	if s.gitClient != nil {
		v, err := s.gitClient.ListDesignVersions(ctx, orgID, projectID)
		if err != nil {
			slog.WarnContext(ctx, "failed to list design versions", "error", err)
		} else {
			versions = mapDesignVersions(v)
			if len(v) > 0 {
				tag = v[0].Tag
				revision = v[0].DesignRevision
				parentReq = fmt.Sprintf("v%d", v[0].RequirementsVersion)
				status = "approved"
			}
		}
	}

	// has-unsaved-changes: working-tree files vs latest-tag files (compared
	// as a flat map of trimmed contents). When no tag exists yet, any
	// working-tree content is by definition unsaved (a draft awaiting its
	// first publish).
	unsaved := false
	if tag == "" {
		unsaved = true
	} else if s.gitClient != nil {
		current, err := s.store.ListDesignFiles(ctx, orgID, projectID)
		if err == nil {
			tagged, err := s.gitClient.GetDesignAtTag(ctx, orgID, projectID, tag)
			if err == nil && !designFilesEqual(current, tagged) {
				unsaved = true
			}
		}
	}

	// SourceSpec resolution. The file's SourceSpec reflects the current working
	// copy (set at stream time); the latest tag's parent requirements version
	// reflects the last approved snapshot.
	sourceSpec := designFile.SourceSpec
	if sourceSpec == "" {
		sourceSpec = parentReq
	}

	return &models.Design{
		ProjectID:         projectID,
		OrgID:             orgID,
		Overview:          designFile.Overview,
		Components:        designFile.Components,
		Status:            status,
		Version:           revision,
		Versions:          versions,
		HasUnsavedChanges: unsaved,
		SourceSpec:        sourceSpec,
	}, nil
}

func (s *designService) GetDesignAtTag(ctx context.Context, orgID, projectID, tag string) (*models.Design, error) {
	if s.gitClient == nil {
		return nil, fmt.Errorf("git client not configured")
	}
	files, err := s.gitClient.GetDesignAtTag(ctx, orgID, projectID, tag)
	if err != nil {
		if errors.Is(err, gitservice.ErrArtifactNotFound) {
			return nil, ErrDesignNotFound
		}
		return nil, fmt.Errorf("get design at %s: %w", tag, err)
	}
	designFile, err := AssembleDesignFromFiles(files)
	if err != nil {
		return nil, fmt.Errorf("parse design at %s: %w", tag, err)
	}

	parent := ""
	if parentN, _, ok := decodeDesignTag(tag); ok {
		parent = fmt.Sprintf("v%d", parentN)
	}

	return &models.Design{
		ProjectID:  projectID,
		OrgID:      orgID,
		Overview:   designFile.Overview,
		Components: designFile.Components,
		Status:     "approved",
		SourceSpec: parent,
	}, nil
}

// streamArchitectFinishData captures the design JSON payload from the
// data-finish event emitted by the agents service.
type streamArchitectFinishData struct {
	Design struct {
		Overview   string                   `json:"overview"`
		Components []models.DesignComponent `json:"components"`
	} `json:"design"`
}

func (s *designService) StreamGenerateDesign(ctx context.Context, orgID, projectID string, out io.Writer, flush func()) error {
	// Pull every requirement document and concatenate as one input bundle —
	// the architect agent treats the whole corpus as the source of truth.
	files, err := s.store.ListRequirements(ctx, orgID, projectID)
	if err != nil {
		return fmt.Errorf("list requirements: %w", err)
	}
	if len(files) == 0 {
		return ErrSpecNotFound
	}
	specContent := concatRequirementBundle(files)
	if specContent == "" {
		return ErrSpecNotFound
	}

	// Require an approved (tagged) requirements version before generating design.
	var sourceTag string
	if s.gitClient != nil {
		versions, err := s.gitClient.ListRequirementsVersions(ctx, orgID, projectID)
		if err != nil {
			slog.WarnContext(ctx, "failed to check requirements versions", "error", err)
		} else if len(versions) == 0 {
			return ErrSpecNotApproved
		} else {
			sourceTag = versions[0].Tag
		}
	}

	var previousDesign *agents.ArchitectDesign
	existingDesign, err := s.store.ReadDesign(ctx, orgID, projectID)
	if err != nil && !IsNotFound(err) {
		slog.WarnContext(ctx, "failed to read existing design for incremental regen", "error", err)
	} else if existingDesign != nil {
		previousDesign = &agents.ArchitectDesign{
			Overview:   existingDesign.Overview,
			Components: existingDesign.Components,
		}
	}

	slog.InfoContext(ctx, "streaming design via agents service",
		"project", projectID, "hasPrevious", previousDesign != nil)

	wireframes := extractWireframeDsls(files)
	availableWireframes := make([]string, 0, len(wireframes))
	for name := range wireframes {
		availableWireframes = append(availableWireframes, name)
	}
	sort.Strings(availableWireframes)

	// Resolve the org's skill catalogue + split by kind. Builtins flow
	// in as full bodies (inlined into the architect prompt); custom +
	// imported flow in as descriptions (manifest only). See
	// docs/design/skills-system.md > "Architect".
	builtinRecords, orgDescriptions := s.resolveArchitectSkills(ctx, orgID)

	// Carry forward currently-attached skill names (or seed the defaults
	// when starting fresh). Until PR 3 ships the attach_skill tool, the
	// seed-default helper attaches all four built-ins on every new design
	// so propagation works end-to-end.
	currentApplied := []string{}
	if existingDesign != nil && len(existingDesign.SkillsApplied) > 0 {
		currentApplied = append(currentApplied, existingDesign.SkillsApplied...)
	} else {
		for _, b := range builtinRecords {
			currentApplied = append(currentApplied, b.Name)
		}
	}

	upstream, err := s.agentsClient.StreamArchitect(ctx, orgID, agents.ArchitectRequest{
		ProjectName:         projectID,
		Spec:                specContent,
		PreviousDesign:      previousDesign,
		Wireframes:          wireframes,
		AvailableWireframes: availableWireframes,
		BuiltinSkills:       builtinRecords,
		OrgSkills:           orgDescriptions,
		SkillsApplied:       currentApplied,
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
		Overview:      finalDesign.Design.Overview,
		Components:    finalDesign.Design.Components,
		SourceSpec:    sourceTag,
		SkillsApplied: seedDefaultSkillsApplied(currentApplied, builtinRecords),
	}

	// Identify components that existed in the working tree before this
	// generation but are NOT in the new design — their `components/<name>/`
	// directories must be deleted so the file tree reflects the new shape.
	existingFiles, _ := s.store.ListDesignFiles(ctx, orgID, projectID)
	keep := make(map[string]struct{}, len(designFile.Components))
	for _, c := range designFile.Components {
		keep[c.Name] = struct{}{}
	}
	for _, name := range componentNamesIn(existingFiles) {
		if _, ok := keep[name]; ok {
			continue
		}
		if err := s.store.DeleteDesignDirectory(ctx, orgID, projectID, ComponentDirPath(name)); err != nil {
			slog.WarnContext(ctx, "failed to delete removed component dir",
				"project", projectID, "component", name, "error", err)
		}
	}

	if err := s.store.WriteDesign(ctx, orgID, projectID, designFile); err != nil {
		return fmt.Errorf("write design: %w", err)
	}

	slog.InfoContext(ctx, "design written from stream",
		"project", projectID, "components", len(designFile.Components))
	return nil
}

// GetDesignBundle returns the working-tree file map alongside the assembled
// Design (so the Explorer can render the tree and the cell diagram can
// render in one round-trip).
func (s *designService) GetDesignBundle(ctx context.Context, orgID, projectID string) (*DesignBundle, error) {
	files, err := s.store.ListDesignFiles(ctx, orgID, projectID)
	if err != nil {
		return nil, err
	}
	if files == nil {
		files = map[string]string{}
	}
	d, err := s.GetDesign(ctx, orgID, projectID)
	if err != nil {
		// If there's no design yet, return an empty bundle (not an error) so
		// the page can render a "no design" state.
		if errors.Is(err, ErrDesignNotFound) {
			return &DesignBundle{Files: files, Design: nil}, nil
		}
		return nil, err
	}
	return &DesignBundle{Files: files, Design: d}, nil
}

// GetDesignBundleAtTag returns the file map + assembled Design at a specific
// version tag. Used by the version selector when browsing history.
func (s *designService) GetDesignBundleAtTag(ctx context.Context, orgID, projectID, tag string) (*DesignBundle, error) {
	if s.gitClient == nil {
		return nil, fmt.Errorf("git client not configured")
	}
	files, err := s.gitClient.GetDesignAtTag(ctx, orgID, projectID, tag)
	if err != nil {
		if errors.Is(err, gitservice.ErrArtifactNotFound) {
			return nil, ErrDesignNotFound
		}
		return nil, fmt.Errorf("get design at %s: %w", tag, err)
	}
	d, err := s.GetDesignAtTag(ctx, orgID, projectID, tag)
	if err != nil {
		return nil, err
	}
	return &DesignBundle{Files: files, Design: d}, nil
}

// UpdateDesignFile writes a single file under specs/design/ and returns
// the refreshed bundle.
//
// Side effect: when the written file is a per-component `design.md`,
// fire `SyncComponentTraits` so an `exposesAPI.auth` toggle propagates to
// the OC Component + ReleaseBindings before the next dispatch. Best-
// effort — failures are logged but never bubble (design tree is the
// canonical source; the trait_sync watcher reconciles on the next
// sweep).
func (s *designService) UpdateDesignFile(ctx context.Context, orgID, projectID, subPath, content string) (*DesignBundle, error) {
	if _, err := s.store.WriteDesignFile(ctx, orgID, projectID, subPath, content); err != nil {
		return nil, err
	}
	if compName, ok := componentNameFromDesignPath(subPath); ok && s.traitSync != nil {
		if err := s.traitSync.SyncComponentTraits(ctx, orgID, projectID, toK8sName(compName)); err != nil {
			slog.WarnContext(ctx, "design_service.UpdateDesignFile: trait_sync best-effort failed",
				"orgID", orgID,
				"projectID", projectID,
				"componentName", compName,
				"subPath", subPath,
				"error", err,
			)
		}
	}
	return s.GetDesignBundle(ctx, orgID, projectID)
}

// componentNameFromDesignPath returns the component name encoded in a
// `components/<name>/design.md` sub-path, or false for any other path
// (root design.md, openapi.yaml, etc.). Used by UpdateDesignFile to gate
// the trait_sync hook to the one path where `exposesAPI.auth` lives.
func componentNameFromDesignPath(subPath string) (string, bool) {
	const prefix = "components/"
	const suffix = "/design.md"
	if !strings.HasPrefix(subPath, prefix) || !strings.HasSuffix(subPath, suffix) {
		return "", false
	}
	name := subPath[len(prefix) : len(subPath)-len(suffix)]
	if name == "" || strings.ContainsRune(name, '/') {
		return "", false
	}
	return name, true
}

// DeleteDesignFile removes a single file under specs/design/ and returns
// the refreshed bundle. Refuses to delete the root design.md.
func (s *designService) DeleteDesignFile(ctx context.Context, orgID, projectID, subPath string) (*DesignBundle, error) {
	if err := s.store.DeleteDesignFile(ctx, orgID, projectID, subPath); err != nil {
		return nil, err
	}
	return s.GetDesignBundle(ctx, orgID, projectID)
}

// DeleteComponent removes the entire components/<name>/ directory and
// returns the refreshed bundle.
//
// Side effect: triggers the OC cascade via TraitSyncService.DeleteComponentCascade
// so the Component CR + ReleaseBindings are GC'd in OC. The OC delete is
// best-effort — failures are logged but never propagate to the user,
// since the design tree has already been mutated (cascade-mismatch is
// surfaced by the audit log). See trait_sync.DeleteComponentCascade for
// the documented owner-ref gap on trait-emitted Backend/RestApi
// resources (R3).
func (s *designService) DeleteComponent(ctx context.Context, orgID, projectID, componentName string) (*DesignBundle, error) {
	if componentName == "" {
		return nil, fmt.Errorf("component name required")
	}
	if err := s.store.DeleteDesignDirectory(ctx, orgID, projectID, ComponentDirPath(componentName)); err != nil {
		return nil, err
	}
	if s.traitSync != nil {
		k8sName := toK8sName(componentName)
		if err := s.traitSync.DeleteComponentCascade(ctx, orgID, projectID, k8sName); err != nil {
			slog.WarnContext(ctx, "design_service.DeleteComponent: OC cascade best-effort failed",
				"orgID", orgID,
				"projectID", projectID,
				"componentName", componentName,
				"error", err,
			)
		}
	}
	return s.GetDesignBundle(ctx, orgID, projectID)
}

// SaveAndProceed persists the working-tree `specs/design/` directory as
// the next `v<N>-<M>` tag where N is the latest requirements version.
// Surfaces ErrSpecNotApproved (rendered as 409 by the controller) when
// no requirements tag exists yet.
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

	res, err := s.gitClient.SaveDesign(ctx, orgID, projectID, gitservice.SaveArtifactRequest{
		Message: "Update design",
	})
	if err != nil {
		// Translate the git-service "no requirements baseline" 409 into the
		// BFF's existing not-approved sentinel so callers + UIs render the
		// same message they already do for spec-not-approved.
		if strings.Contains(err.Error(), "no requirements baseline") {
			return nil, ErrSpecNotApproved
		}
		return nil, fmt.Errorf("save design: %w", err)
	}

	versions, err := s.gitClient.ListDesignVersions(ctx, orgID, projectID)
	if err != nil {
		slog.WarnContext(ctx, "list versions after save failed", "error", err)
	}

	slog.InfoContext(ctx, "design save completed",
		"project", projectID, "tag", res.Tag, "status", res.Status)

	if s.taskSvc != nil {
		if rerr := s.taskSvc.ReconcilePendingForDesignChange(ctx, orgID, projectID); rerr != nil {
			slog.WarnContext(ctx, "task reconciliation after design save failed", "error", rerr)
		}
	}

	return &models.Design{
		ProjectID:  projectID,
		OrgID:      orgID,
		Overview:   designFile.Overview,
		Components: designFile.Components,
		Status:     "approved",
		Version:    res.DesignRevision,
		Versions:   mapDesignVersions(versions),
		SourceSpec: fmt.Sprintf("v%d", res.RequirementsVersion),
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
		return nil, nil
	}
	v, err := s.gitClient.ListDesignVersions(ctx, orgID, projectID)
	if err != nil {
		return nil, fmt.Errorf("list design versions: %w", err)
	}
	return mapDesignVersions(v), nil
}

// concatRequirementBundle joins all requirement files into a single corpus
// for agent input. Files are emitted in alphabetical order with a heading
// prefix so the LLM sees consistent boundaries between documents.
//
// Only Markdown content is included in the spec. `.excalidraw` JSON is
// noisy for the LLM (it's the rendered scene, not the DSL); `.dsl` files
// are surfaced separately via the architect's `read_wireframe` tool.
func concatRequirementBundle(files map[string]string) string {
	if len(files) == 0 {
		return ""
	}
	names := make([]string, 0, len(files))
	for k := range files {
		if !strings.HasSuffix(strings.ToLower(k), ".md") {
			continue
		}
		names = append(names, k)
	}
	sort.Strings(names)
	var sb strings.Builder
	for i, name := range names {
		if i > 0 {
			sb.WriteString("\n\n")
		}
		sb.WriteString(fmt.Sprintf("# %s\n\n", name))
		sb.WriteString(files[name])
	}
	return sb.String()
}

// extractWireframeDsls picks `.dsl` files from the requirements bundle and
// returns them keyed by canvas name (filename without the .dsl extension).
// These are passed to the architect agent so it can fetch them on demand
// via the `read_wireframe` tool.
func extractWireframeDsls(files map[string]string) map[string]string {
	out := make(map[string]string)
	for name, content := range files {
		lower := strings.ToLower(name)
		if !strings.HasSuffix(lower, ".dsl") {
			continue
		}
		canvas := strings.TrimSuffix(name, ".dsl")
		out[canvas] = content
	}
	return out
}

// resolveArchitectSkills splits the org's catalogue into (builtins with
// full bodies, org-skills with descriptions only) for the architect
// input. Returns empty slices when no SkillService is wired (tests).
func (s *designService) resolveArchitectSkills(ctx context.Context, orgID string) ([]agents.SkillRecord, []agents.SkillDescription) {
	if s.skillSvc == nil {
		return nil, nil
	}
	all, err := s.skillSvc.List(ctx, orgID)
	if err != nil {
		slog.WarnContext(ctx, "design_service: skill list failed — running without skills", "orgID", orgID, "error", err)
		return nil, nil
	}
	var builtins []agents.SkillRecord
	var orgSkills []agents.SkillDescription
	for _, sk := range all {
		if sk.Kind == "builtin" {
			builtins = append(builtins, agents.SkillRecord{
				Name:        sk.Name,
				Description: sk.Description,
				Body:        sk.SkillMD,
			})
		} else {
			orgSkills = append(orgSkills, agents.SkillDescription{
				Name:        sk.Name,
				Description: sk.Description,
			})
		}
	}
	return builtins, orgSkills
}

// seedDefaultSkillsApplied returns the skillsApplied list to persist on
// the freshly-emitted design. PR 1 behaviour: if the caller passed a
// non-empty existing set, use it; otherwise stamp every available
// built-in so propagation works end-to-end without the architect having
// an attach_skill tool yet (lands in PR 3). The seed runs at every
// `StreamArchitect` finalize per docs/design/skills-system.md > PR 1.
func seedDefaultSkillsApplied(current []string, builtins []agents.SkillRecord) []string {
	if len(current) > 0 {
		out := append([]string(nil), current...)
		sort.Strings(out)
		return out
	}
	out := make([]string, 0, len(builtins))
	for _, b := range builtins {
		out = append(out, b.Name)
	}
	sort.Strings(out)
	return out
}

// decodeDesignTag parses a `v<N>-<M>` design tag and returns (parent, revision, ok).
// Lives in this file so callers don't have to import the git-service helpers.
var designTagPattern = regexp.MustCompile(`^v(\d+)-(\d+)$`)

func decodeDesignTag(tag string) (int, int, bool) {
	m := designTagPattern.FindStringSubmatch(tag)
	if m == nil {
		return 0, 0, false
	}
	var n, r int
	if _, err := fmt.Sscanf(m[1], "%d", &n); err != nil || n < 1 {
		return 0, 0, false
	}
	if _, err := fmt.Sscanf(m[2], "%d", &r); err != nil || r < 1 {
		return 0, 0, false
	}
	return n, r, true
}
