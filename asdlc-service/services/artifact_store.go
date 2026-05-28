package services

import (
	"context"
	"errors"
	"fmt"
	"path"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/wso2/asdlc/asdlc-service/models"
)

// ArtifactStore wraps the in-process artifact service to add value beyond
// pure file I/O: external-API catalog resolution and the typed `DesignFile`
// shape (YAML split/assemble). The 7 downstream services (design_service,
// requirements_service, requirements_chat_service, project_service,
// component_service, trait_sync, runtime_config_service) consume this
// layer.
type ArtifactStore struct {
	artifactSvc  ArtifactService
	externalAPIs *ExternalAPICatalog
}

func NewArtifactStore(artifactSvc ArtifactService) *ArtifactStore {
	store := &ArtifactStore{artifactSvc: artifactSvc, externalAPIs: DefaultExternalAPICatalog()}
	registerSplitDesignCatalog(store.externalAPIs)
	return store
}

// SetExternalAPICatalog overrides the catalog the store uses to resolve
// architect-declared dependent-API names into concrete URLs. Optional —
// without it, NewArtifactStore wires the shipped default catalog.
func (s *ArtifactStore) SetExternalAPICatalog(c *ExternalAPICatalog) {
	if s == nil {
		return
	}
	s.externalAPIs = c
	registerSplitDesignCatalog(c)
}

// splitDesignCatalogRef is a process-wide pointer the free-function
// SplitDesign reads to strip catalog-resolved URLs on save. Set by
// NewArtifactStore so production paths get the catalog automatically;
// nil in tests / standalone SplitDesign callers.
var splitDesignCatalogRef *ExternalAPICatalog

func registerSplitDesignCatalog(c *ExternalAPICatalog) { splitDesignCatalogRef = c }
func splitDesignCatalog() *ExternalAPICatalog          { return splitDesignCatalogRef }

// ---- Requirements (multi-file Markdown directory) -----------------------

// RequirementsMainFile is the canonical primary requirements document. It
// cannot be deleted/renamed via the API — controllers should reject
// destructive operations on it.
const RequirementsMainFile = "requirements.md"

// ListRequirements returns the working-tree file map under
// `specs/requirements/`. A first-time project with no requirements yet
// returns an empty map (not an error).
func (s *ArtifactStore) ListRequirements(ctx context.Context, orgID, projectID string) (map[string]string, error) {
	files, err := s.artifactSvc.ListRequirementFiles(ctx, projectID)
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
	res, err := s.artifactSvc.GetFile(ctx, projectID, path.Join(RequirementsDir, name))
	if err != nil {
		return "", err
	}
	return res.Content, nil
}

// WriteRequirementFile creates or overwrites a single requirement file.
// The optional ifMatch sha (returned by the previous PUT) gives the
// streaming caller optimistic concurrency control.
func (s *ArtifactStore) WriteRequirementFile(ctx context.Context, orgID, projectID, name, content string) (sha string, err error) {
	res, err := s.artifactSvc.PutFile(ctx, projectID, path.Join(RequirementsDir, name), content, "")
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
	if err := s.artifactSvc.DeleteRequirementFile(ctx, projectID, name); err != nil {
		return fmt.Errorf("delete requirement file %q: %w", name, err)
	}
	return nil
}

// ---- Design (multi-file directory) --------------------------------------

// DesignFile is the BFF's in-memory representation of the multi-file design
// artifact. It assembles from / splits to the working-tree layout under
// `specs/design/`:
//
//	design.md                              # overview prose + sourceSpec frontmatter
//	components/<name>/design.md            # frontmatter (type, language, dependsOn,
//	                                       # buildpack, appPath, entrypoint) + body
//	                                       # (componentAgentInstructions)
//	components/<name>/openapi.yaml         # OpenAPI 3.0.3 (service components only)
type DesignFile struct {
	Overview       string                   `json:"overview"`
	Components     []models.DesignComponent `json:"components"`
	SourceSpec     string                   `json:"sourceSpec,omitempty"`
	SkillsApplied  []string                 `json:"skillsApplied,omitempty"`
}

// DesignRootFile is the canonical root design document. It cannot be deleted
// via the API.
const DesignRootFile = "design.md"

// componentDirPrefix is the path prefix under specs/design/ for per-component
// directories.
const componentDirPrefix = "components/"

// ListDesignFiles returns the working-tree file map under `specs/design/`.
// Keys are paths relative to that directory, using forward slashes (e.g.
// `design.md`, `components/user-api/design.md`).
func (s *ArtifactStore) ListDesignFiles(ctx context.Context, orgID, projectID string) (map[string]string, error) {
	files, err := s.artifactSvc.ListDesignFiles(ctx, projectID)
	if err != nil {
		return nil, err
	}
	if files == nil {
		files = map[string]string{}
	}
	return files, nil
}

// ReadDesignFile reads a single design file by sub-path.
func (s *ArtifactStore) ReadDesignFile(ctx context.Context, orgID, projectID, subPath string) (string, error) {
	res, err := s.artifactSvc.GetFile(ctx, projectID, path.Join(DesignDir, subPath))
	if err != nil {
		return "", err
	}
	return res.Content, nil
}

// WriteDesignFile creates or overwrites a single design file. The path is
// relative to `specs/design/` (forward slashes; nested components allowed).
func (s *ArtifactStore) WriteDesignFile(ctx context.Context, orgID, projectID, subPath, content string) (sha string, err error) {
	res, err := s.artifactSvc.PutFile(ctx, projectID, path.Join(DesignDir, subPath), content, "")
	if err != nil {
		return "", fmt.Errorf("write design file %q: %w", subPath, err)
	}
	return res.SHA, nil
}

// DeleteDesignFile removes a single design file. Refuses to delete the root
// `design.md`.
func (s *ArtifactStore) DeleteDesignFile(ctx context.Context, orgID, projectID, subPath string) error {
	if subPath == DesignRootFile {
		return fmt.Errorf("cannot delete %s", DesignRootFile)
	}
	if err := s.artifactSvc.DeleteDesignFile(ctx, projectID, subPath); err != nil {
		return fmt.Errorf("delete design file %q: %w", subPath, err)
	}
	return nil
}

// DeleteDesignDirectory removes a directory under `specs/design/` and all
// its contents (e.g. `components/user-api` to remove a component's whole
// subtree).
func (s *ArtifactStore) DeleteDesignDirectory(ctx context.Context, orgID, projectID, subPath string) error {
	if err := s.artifactSvc.DeleteDesignDirectory(ctx, projectID, subPath); err != nil {
		return fmt.Errorf("delete design directory %q: %w", subPath, err)
	}
	return nil
}

// ReadDesign lists the working-tree design files and assembles them into the
// flat `DesignFile` shape that the rest of the BFF expects (task generation,
// OC provisioning, issue bodies, etc.). Returns
// (nil, ErrArtifactNotFound) when no design root exists yet.
func (s *ArtifactStore) ReadDesign(ctx context.Context, orgID, projectID string) (*DesignFile, error) {
	files, err := s.artifactSvc.ListDesignFiles(ctx, projectID)
	if err != nil {
		return nil, err
	}
	if len(files) == 0 || strings.TrimSpace(files[DesignRootFile]) == "" {
		return nil, nil
	}
	design, err := AssembleDesign(files)
	if err != nil {
		return nil, err
	}
	// Resolve architect-declared dependent-API names against the external
	// API catalog so the in-memory design always has concrete URLs even
	// when the on-disk frontmatter declared by intent only.
	s.resolveExternalAPIs(design)
	return design, nil
}

// resolveExternalAPIs fills in URLs for catalog-known dependent-API
// entries whose URL was left blank by the architect. Idempotent — already-
// populated URLs are left untouched.
func (s *ArtifactStore) resolveExternalAPIs(d *DesignFile) {
	if s == nil || s.externalAPIs == nil || d == nil {
		return
	}
	for i := range d.Components {
		for j := range d.Components[i].DependentApis {
			dep := &d.Components[i].DependentApis[j]
			if dep.URL != "" {
				continue
			}
			entry := s.externalAPIs.Lookup(dep.Name)
			if entry.URL == "" {
				continue
			}
			dep.URL = entry.URL
			if dep.Authentication == "" {
				dep.Authentication = entry.Authentication
			}
		}
	}
}

// WriteDesign splits the in-memory design into multiple files, then writes
// every file via the git-service. Files no longer referenced by the new
// design (e.g. components removed by a regeneration) are NOT auto-deleted —
// the caller is expected to call DeleteDesignDirectory for removed
// components separately.
func (s *ArtifactStore) WriteDesign(ctx context.Context, orgID, projectID string, design *DesignFile) error {
	files, err := SplitDesign(design)
	if err != nil {
		return fmt.Errorf("split design: %w", err)
	}
	for subPath, content := range files {
		if _, err := s.WriteDesignFile(ctx, orgID, projectID, subPath, content); err != nil {
			return fmt.Errorf("write %s: %w", subPath, err)
		}
	}
	return nil
}

// ---- Helpers ------------------------------------------------------------

// IsNotFound is sugar for callers that want to distinguish "no artifact yet"
// from a real error.
func IsNotFound(err error) bool { return errors.Is(err, ErrArtifactNotFound) }

// designFilesEqual compares two design file maps after trimming whitespace
// from each value. Used by the has-unsaved-changes check.
func designFilesEqual(a, b map[string]string) bool {
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

// rootFrontmatter is the YAML frontmatter we accept on the root `design.md`.
type rootFrontmatter struct {
	SourceSpec    string   `yaml:"sourceSpec,omitempty"`
	SkillsApplied []string `yaml:"skillsApplied,omitempty"`
}

// componentFrontmatter is the YAML frontmatter we accept on each
// `components/<name>/design.md`. Field names mirror the user-facing keys
// (snake-free) so frontmatter the architect emits is human-editable.
type componentFrontmatter struct {
	Type           string                `yaml:"type"`
	Language       string                `yaml:"language,omitempty"`
	DependsOn      []string              `yaml:"dependsOn,omitempty"`
	Buildpack      string                `yaml:"buildpack,omitempty"`
	AppPath        string                `yaml:"appPath,omitempty"`
	Entrypoint     string                `yaml:"entrypoint,omitempty"`
	ExposesAPI     *exposesAPIConfig     `yaml:"exposesAPI,omitempty"`
	CallerIdentity *callerIdentityConfig `yaml:"callerIdentity,omitempty"`
	DependentApis  []dependentApiConfig  `yaml:"dependentApis,omitempty"`
}

// exposesAPIConfig is the on-disk shape for a service component's API
// exposure policy. `Auth: "end-user-required"` ⇒ gateway validates a
// user JWT and injects UserContext upstream.
type exposesAPIConfig struct {
	Managed     bool   `yaml:"managed,omitempty"`
	Auth        string `yaml:"auth,omitempty"`
	UserContext string `yaml:"userContext,omitempty"`
}

// callerIdentityConfig is the on-disk shape for a web-app's caller-
// identity intent. `Mode: "end-user"` ⇒ the SPA performs OIDC + PKCE
// against the platform IDP.
type callerIdentityConfig struct {
	Mode string `yaml:"mode,omitempty"`
}

// dependentApiConfig is the on-disk shape for an external upstream API the
// component consumes at runtime. See models.DependentAPI for the wire shape.
type dependentApiConfig struct {
	Name           string `yaml:"name"`
	URL            string `yaml:"url"`
	Description    string `yaml:"description,omitempty"`
	Authentication string `yaml:"authentication,omitempty"`
}

// splitFrontmatter separates the leading YAML frontmatter block (delimited
// by `---` lines) from the body. If the file has no frontmatter, returns
// ("", content, nil).
func splitFrontmatter(content string) (fm string, body string, err error) {
	trimmed := strings.TrimLeft(content, " \t\r\n")
	// Strip an optional UTF-8 BOM (U+FEFF) before frontmatter detection.
	trimmed = strings.TrimPrefix(trimmed, "\ufeff")
	if !strings.HasPrefix(trimmed, "---") {
		return "", content, nil
	}
	// Find the closing fence — must be a `---` on its own line after the open.
	rest := trimmed[3:]
	// Skip optional newline directly after opening fence.
	rest = strings.TrimLeft(rest, " \t")
	if !strings.HasPrefix(rest, "\n") && !strings.HasPrefix(rest, "\r\n") {
		// Open fence was not followed by a newline — treat as no frontmatter.
		return "", content, nil
	}
	// Locate the end fence.
	end := strings.Index(rest, "\n---")
	if end < 0 {
		return "", content, fmt.Errorf("frontmatter: unterminated --- block")
	}
	fm = strings.TrimSpace(rest[:end])
	after := rest[end+len("\n---"):]
	// Drop optional newline + spaces after the closing fence.
	after = strings.TrimPrefix(after, "\r")
	after = strings.TrimPrefix(after, "\n")
	return fm, after, nil
}

// joinFrontmatter writes the YAML frontmatter block + body. If the
// frontmatter is empty, returns the body unchanged.
func joinFrontmatter(fm string, body string) string {
	fm = strings.TrimSpace(fm)
	body = strings.TrimLeft(body, "\r\n")
	if fm == "" {
		return body
	}
	return "---\n" + fm + "\n---\n\n" + body
}

// AssembleDesign reconstructs a flat DesignFile from the multi-file working
// tree map. Path separator is `/`. Returns an error if the root `design.md`
// is missing (callers handle that as "no design yet").
func AssembleDesign(files map[string]string) (*DesignFile, error) {
	root, ok := files[DesignRootFile]
	if !ok {
		return nil, fmt.Errorf("design.md missing")
	}

	fm, body, err := splitFrontmatter(root)
	if err != nil {
		return nil, fmt.Errorf("parse design.md frontmatter: %w", err)
	}
	var rfm rootFrontmatter
	if fm != "" {
		if err := yaml.Unmarshal([]byte(fm), &rfm); err != nil {
			return nil, fmt.Errorf("decode design.md frontmatter: %w", err)
		}
	}
	out := &DesignFile{
		Overview:      strings.TrimSpace(body),
		SourceSpec:    rfm.SourceSpec,
		SkillsApplied: append([]string(nil), rfm.SkillsApplied...),
	}

	// Iterate component dirs in deterministic order.
	componentNames := componentNamesIn(files)
	out.Components = make([]models.DesignComponent, 0, len(componentNames))
	for _, name := range componentNames {
		designPath := componentDirPrefix + name + "/design.md"
		raw, ok := files[designPath]
		if !ok {
			continue
		}
		comp, err := assembleComponent(name, raw, files)
		if err != nil {
			return nil, fmt.Errorf("assemble component %q: %w", name, err)
		}
		out.Components = append(out.Components, comp)
	}
	return out, nil
}

func assembleComponent(name, designMd string, files map[string]string) (models.DesignComponent, error) {
	fm, body, err := splitFrontmatter(designMd)
	if err != nil {
		return models.DesignComponent{}, fmt.Errorf("frontmatter: %w", err)
	}
	var cfm componentFrontmatter
	if fm != "" {
		if err := yaml.Unmarshal([]byte(fm), &cfm); err != nil {
			return models.DesignComponent{}, fmt.Errorf("decode frontmatter: %w", err)
		}
	}
	openapi := files[componentDirPrefix+name+"/openapi.yaml"]
	if openapi == "" {
		// Fallback: support .yml as well.
		openapi = files[componentDirPrefix+name+"/openapi.yml"]
	}
	dependsOn := append([]string(nil), cfm.DependsOn...)
	if dependsOn == nil {
		dependsOn = []string{}
	}
	var exposes *models.ExposesAPI
	if cfm.ExposesAPI != nil && (cfm.ExposesAPI.Auth != "" || cfm.ExposesAPI.Managed || cfm.ExposesAPI.UserContext != "") {
		exposes = &models.ExposesAPI{
			Managed:     cfm.ExposesAPI.Managed,
			Auth:        cfm.ExposesAPI.Auth,
			UserContext: cfm.ExposesAPI.UserContext,
		}
	}
	var caller *models.CallerIdentity
	if cfm.CallerIdentity != nil && cfm.CallerIdentity.Mode != "" {
		caller = &models.CallerIdentity{Mode: cfm.CallerIdentity.Mode}
	}
	var depApis []models.DependentAPI
	if len(cfm.DependentApis) > 0 {
		depApis = make([]models.DependentAPI, 0, len(cfm.DependentApis))
		for _, d := range cfm.DependentApis {
			if d.Name == "" {
				continue
			}
			// URL may be empty here — the architect can declare an
			// intent by name only; the ArtifactStore's catalog post-
			// process resolves it on the way out of ReadDesign.
			depApis = append(depApis, models.DependentAPI{
				Name:           d.Name,
				URL:            d.URL,
				Description:    d.Description,
				Authentication: d.Authentication,
			})
		}
	}
	return models.DesignComponent{
		Name:                       name,
		ComponentType:              cfm.Type,
		Language:                   cfm.Language,
		DependsOn:                  dependsOn,
		Entrypoint:                 cfm.Entrypoint,
		Buildpack:                  cfm.Buildpack,
		AppPath:                    cfm.AppPath,
		OpenAPISpec:                openapi,
		ComponentAgentInstructions: strings.TrimSpace(body),
		ExposesAPI:                 exposes,
		CallerIdentity:             caller,
		DependentApis:              depApis,
	}, nil
}

// componentNamesIn walks the file map and returns the unique component
// directory names found under `components/`, sorted alphabetically.
func componentNamesIn(files map[string]string) []string {
	seen := make(map[string]struct{})
	for p := range files {
		if !strings.HasPrefix(p, componentDirPrefix) {
			continue
		}
		rest := p[len(componentDirPrefix):]
		slash := strings.IndexByte(rest, '/')
		if slash <= 0 {
			continue
		}
		seen[rest[:slash]] = struct{}{}
	}
	names := make([]string, 0, len(seen))
	for n := range seen {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

// SplitDesign takes a flat in-memory design and produces the file map for
// the working tree. The caller is responsible for deleting any
// pre-existing files NOT present in the returned map (e.g. components
// removed across a regeneration).
func SplitDesign(d *DesignFile) (map[string]string, error) {
	if d == nil {
		return nil, fmt.Errorf("nil design")
	}
	out := make(map[string]string, 1+2*len(d.Components))

	// Root design.md — body + optional frontmatter. SourceSpec is encoded
	// in the design tag name (`v<N>-<M>`); we only write it to the file
	// frontmatter when there is some other field that requires the block
	// (currently: skillsApplied per docs/design/skills-system.md). The
	// console's markdown preview strips frontmatter via splitFrontmatter,
	// so the visible Overview prose is unchanged.
	if len(d.SkillsApplied) > 0 {
		// Sorted copy for stable diffs.
		sortedSkills := append([]string(nil), d.SkillsApplied...)
		sort.Strings(sortedSkills)
		rfm := rootFrontmatter{SkillsApplied: sortedSkills}
		rfmBytes, err := marshalFrontmatter(rfm)
		if err != nil {
			return nil, fmt.Errorf("encode root frontmatter: %w", err)
		}
		out[DesignRootFile] = joinFrontmatter(string(rfmBytes), strings.TrimSpace(d.Overview)+"\n")
	} else {
		out[DesignRootFile] = strings.TrimSpace(d.Overview) + "\n"
	}

	for _, comp := range d.Components {
		if comp.Name == "" {
			return nil, fmt.Errorf("component with empty name")
		}
		base := componentDirPrefix + comp.Name
		cfm := componentFrontmatter{
			Type:       comp.ComponentType,
			Language:   comp.Language,
			DependsOn:  comp.DependsOn,
			Buildpack:  comp.Buildpack,
			AppPath:    comp.AppPath,
			Entrypoint: comp.Entrypoint,
		}
		// Preserve any non-empty field — gating on `Auth != ""` would drop
		// designs that set only `managed` or `userContext`.
		if comp.ExposesAPI != nil && (comp.ExposesAPI.Auth != "" || comp.ExposesAPI.Managed || comp.ExposesAPI.UserContext != "") {
			cfm.ExposesAPI = &exposesAPIConfig{
				Managed:     comp.ExposesAPI.Managed,
				Auth:        comp.ExposesAPI.Auth,
				UserContext: comp.ExposesAPI.UserContext,
			}
		}
		if comp.CallerIdentity != nil && comp.CallerIdentity.Mode != "" {
			cfm.CallerIdentity = &callerIdentityConfig{Mode: comp.CallerIdentity.Mode}
		}
		if len(comp.DependentApis) > 0 {
			cfm.DependentApis = make([]dependentApiConfig, 0, len(comp.DependentApis))
			for _, d := range comp.DependentApis {
				if d.Name == "" {
					continue
				}
				// Drop the URL on save when it matches the current
				// catalog entry for this name — the in-memory URL came
				// from ReadDesign's catalog substitution, not from the
				// architect. Persisting it would defeat the
				// "name-only declaration" contract and break catalog
				// rotation. (catalog == nil in tests / standalone
				// SplitDesign callers — fall through to write URL.)
				url := d.URL
				if catalog := splitDesignCatalog(); catalog != nil {
					if entry := catalog.Lookup(d.Name); entry.URL != "" && entry.URL == d.URL {
						url = ""
					}
				}
				if url == "" && d.Description == "" && d.Authentication == "" {
					// Name-only declaration — emit just the name.
					cfm.DependentApis = append(cfm.DependentApis, dependentApiConfig{Name: d.Name})
					continue
				}
				cfm.DependentApis = append(cfm.DependentApis, dependentApiConfig{
					Name:           d.Name,
					URL:            url,
					Description:    d.Description,
					Authentication: d.Authentication,
				})
			}
		}
		cfmBytes, err := marshalFrontmatter(cfm)
		if err != nil {
			return nil, fmt.Errorf("encode component %q frontmatter: %w", comp.Name, err)
		}
		header := fmt.Sprintf("# %s\n\n", comp.Name)
		out[base+"/design.md"] = joinFrontmatter(string(cfmBytes), header+strings.TrimSpace(comp.ComponentAgentInstructions)+"\n")
		if openapi := strings.TrimSpace(comp.OpenAPISpec); openapi != "" {
			out[base+"/openapi.yaml"] = openapi + "\n"
		}
	}
	return out, nil
}

// ComponentDesignPath returns the design.md path for a given component name
// (relative to specs/design/). Exported so callers (design_service stream
// handlers, controllers) don't recompute the format.
func ComponentDesignPath(componentName string) string {
	return path.Join(componentDirPrefix, componentName, "design.md")
}

// ComponentOpenAPIPath returns the openapi.yaml path for a given component
// name (relative to specs/design/).
func ComponentOpenAPIPath(componentName string) string {
	return path.Join(componentDirPrefix, componentName, "openapi.yaml")
}

// ComponentDirPath returns the directory path for a given component name
// (relative to specs/design/), used by DeleteDesignDirectory.
func ComponentDirPath(componentName string) string {
	return path.Join(componentDirPrefix, componentName)
}

// marshalFrontmatter encodes v as YAML, but returns an empty string when
// the encoded form is "{}\n" (yaml.Marshal of an all-zero struct).
func marshalFrontmatter(v interface{}) ([]byte, error) {
	out, err := yaml.Marshal(v)
	if err != nil {
		return nil, err
	}
	trimmed := strings.TrimSpace(string(out))
	if trimmed == "{}" {
		return []byte{}, nil
	}
	return []byte(trimmed), nil
}
