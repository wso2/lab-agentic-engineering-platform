package services

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
	"gorm.io/gorm"

	"github.com/wso2/asdlc/asdlc-service/models"
)

// DesignDiff captures component-level structural differences between two
// designs (current vs baseline batch's design). Used by the tech-lead Phase 1
// prompt (incremental mode) and by the validator's coverage rules.
//
// "Modified" reports both `dependsOn` set diffs and OpenAPI op-level diffs;
// the latter is what makes a modified component require a follow-up task in
// incremental mode.
type DesignDiff struct {
	Added    []DesignDiffAdded    `json:"added"`
	Removed  []DesignDiffRemoved  `json:"removed"`
	Modified []DesignDiffModified `json:"modified"`
}

type DesignDiffAdded struct {
	Name          string `json:"name"`
	ComponentType string `json:"componentType"`
	Language      string `json:"language"`
}

type DesignDiffRemoved struct {
	Name string `json:"name"`
}

type DesignDiffModified struct {
	Name              string   `json:"name"`
	DependsOnAdded    []string `json:"dependsOnAdded,omitempty"`
	DependsOnRemoved  []string `json:"dependsOnRemoved,omitempty"`
	// Operation-level OpenAPI diffs, e.g. "+ POST /todos", "- DELETE /todos/{id}",
	// "~ PUT /todos/{id} (request schema)".
	OpenAPIOps []string `json:"openapiOps,omitempty"`
	// True iff dependsOn or openapi operations changed (drives the validator
	// "contract-affecting" coverage rule).
	ContractAffected bool `json:"contractAffected"`
}

// IsTrivial returns true when the diff has no ADDED or contract-affecting
// MODIFIED entries. Drives the validator's empty-plan-allowed rule in
// incremental mode (design §5).
func (d DesignDiff) IsTrivial() bool {
	if len(d.Added) > 0 {
		return false
	}
	for _, m := range d.Modified {
		if m.ContractAffected {
			return false
		}
	}
	return true
}

// computeDesignDiff walks current vs baseline and returns the structural
// delta. Component matching is case-insensitive on Name. OpenAPI op-level
// diffs come from a parse-and-compare of the canonicalised YAML — the same
// canonicalisation `openapi_normalize.go::normalizeOpenAPIYAML` produces
// when writing design.json, so whitespace/ordering noise is stripped.
//
// `prev` may be nil → every current component is reported ADDED.
func computeDesignDiff(prev, curr []models.DesignComponent) DesignDiff {
	prevByName := indexComponents(prev)
	currByName := indexComponents(curr)

	diff := DesignDiff{}

	for name, c := range currByName {
		if _, ok := prevByName[name]; !ok {
			diff.Added = append(diff.Added, DesignDiffAdded{
				Name:          c.Name,
				ComponentType: c.ComponentType,
				Language:      c.Language,
			})
		}
	}
	for name, c := range prevByName {
		if _, ok := currByName[name]; !ok {
			diff.Removed = append(diff.Removed, DesignDiffRemoved{Name: c.Name})
		}
	}
	for name, cc := range currByName {
		pc, ok := prevByName[name]
		if !ok {
			continue
		}
		mod := DesignDiffModified{Name: cc.Name}
		mod.DependsOnAdded, mod.DependsOnRemoved = stringSetDiff(pc.DependsOn, cc.DependsOn)

		ops, err := compareOpenAPIOps(pc.OpenAPISpec, cc.OpenAPISpec)
		if err == nil {
			mod.OpenAPIOps = ops
		}

		if len(mod.DependsOnAdded) > 0 || len(mod.DependsOnRemoved) > 0 || len(mod.OpenAPIOps) > 0 {
			mod.ContractAffected = true
			diff.Modified = append(diff.Modified, mod)
		}
	}

	// Sort outputs for stable prompts / fixtures.
	sort.Slice(diff.Added, func(i, j int) bool { return diff.Added[i].Name < diff.Added[j].Name })
	sort.Slice(diff.Removed, func(i, j int) bool { return diff.Removed[i].Name < diff.Removed[j].Name })
	sort.Slice(diff.Modified, func(i, j int) bool { return diff.Modified[i].Name < diff.Modified[j].Name })

	return diff
}

func indexComponents(components []models.DesignComponent) map[string]models.DesignComponent {
	out := make(map[string]models.DesignComponent, len(components))
	for _, c := range components {
		out[strings.ToLower(c.Name)] = c
	}
	return out
}

// stringSetDiff returns (added, removed) — items in `curr` not in `prev` and
// vice versa. Order-preserving on the source slices.
func stringSetDiff(prev, curr []string) (added, removed []string) {
	prevSet := make(map[string]struct{}, len(prev))
	for _, s := range prev {
		prevSet[s] = struct{}{}
	}
	currSet := make(map[string]struct{}, len(curr))
	for _, s := range curr {
		currSet[s] = struct{}{}
	}
	for _, s := range curr {
		if _, ok := prevSet[s]; !ok {
			added = append(added, s)
		}
	}
	for _, s := range prev {
		if _, ok := currSet[s]; !ok {
			removed = append(removed, s)
		}
	}
	return added, removed
}

// compareOpenAPIOps runs both YAMLs through canonicalize and reports the
// per-operation differences as "+ POST /foo", "- DELETE /bar/{id}",
// "~ PUT /baz/{id}" lines. Returns nil if either spec is empty.
func compareOpenAPIOps(prev, curr string) ([]string, error) {
	prevOps, err := extractOpenAPIOps(prev)
	if err != nil {
		return nil, err
	}
	currOps, err := extractOpenAPIOps(curr)
	if err != nil {
		return nil, err
	}

	var ops []string
	for key := range currOps {
		if _, ok := prevOps[key]; !ok {
			ops = append(ops, "+ "+key)
		}
	}
	for key := range prevOps {
		if _, ok := currOps[key]; !ok {
			ops = append(ops, "- "+key)
		}
	}
	for key, c := range currOps {
		p, ok := prevOps[key]
		if !ok {
			continue
		}
		if c != p {
			ops = append(ops, "~ "+key)
		}
	}
	sort.Strings(ops)
	return ops, nil
}

// extractOpenAPIOps returns "METHOD /path" → fingerprint of the operation's
// canonicalised body. Two specs that differ only in whitespace produce
// identical fingerprints; a request-schema change produces different ones.
func extractOpenAPIOps(spec string) (map[string]string, error) {
	if spec == "" {
		return map[string]string{}, nil
	}
	canonical, err := normalizeOpenAPIYAML(spec)
	if err != nil {
		return nil, err
	}
	// Re-parse the canonical form to walk paths.
	var doc map[string]any
	if err := yaml.Unmarshal([]byte(canonical), &doc); err != nil {
		return nil, err
	}
	out := map[string]string{}
	paths, _ := doc["paths"].(map[string]any)
	for path, item := range paths {
		opItem, ok := item.(map[string]any)
		if !ok {
			continue
		}
		for method, op := range opItem {
			ml := strings.ToLower(method)
			if !isHTTPMethod(ml) {
				continue
			}
			fp := fingerprint(op)
			out[strings.ToUpper(ml)+" "+path] = fp
		}
	}
	return out, nil
}

func isHTTPMethod(s string) bool {
	switch s {
	case "get", "post", "put", "delete", "patch", "head", "options", "trace":
		return true
	}
	return false
}

// fingerprint serializes the operation as a stable string. Cheap-but-good:
// a bytewise hash isn't needed since canonicalize already removes ordering
// noise.
func fingerprint(v any) string {
	b, err := yaml.Marshal(v)
	if err != nil {
		return ""
	}
	return string(b)
}

// computeSpecDiff returns a unified diff (only +/- lines, no ---/+++ headers)
// between prev and curr. Empty prev means "every line is added".
//
// Implementation note: a real LCS-based unified diff would be ideal but
// would pull in another dep. For our use case (specs are tens to a few
// hundred lines, prompt context budget is generous) a line-level set diff
// is enough to give the model a sense of "what changed". We emit a simple
// added/removed split rather than interleaved hunks; the model only needs
// to know what shifted, not exactly where.
func computeSpecDiff(prev, curr string) string {
	if prev == "" {
		// Whole document is new — return + lines.
		var sb strings.Builder
		for _, line := range strings.Split(curr, "\n") {
			sb.WriteString("+ ")
			sb.WriteString(line)
			sb.WriteString("\n")
		}
		return sb.String()
	}

	prevLines := strings.Split(prev, "\n")
	currLines := strings.Split(curr, "\n")

	prevSet := make(map[string]int, len(prevLines))
	for _, l := range prevLines {
		prevSet[l]++
	}
	currSet := make(map[string]int, len(currLines))
	for _, l := range currLines {
		currSet[l]++
	}

	var sb strings.Builder
	for _, l := range currLines {
		if prevSet[l] > 0 {
			prevSet[l]--
			continue
		}
		sb.WriteString("+ ")
		sb.WriteString(l)
		sb.WriteString("\n")
	}
	for _, l := range prevLines {
		if currSet[l] > 0 {
			currSet[l]--
			continue
		}
		sb.WriteString("- ")
		sb.WriteString(l)
		sb.WriteString("\n")
	}
	return sb.String()
}

// renderDesignDiffForPrompt formats the structural diff in the human-friendly
// shape expected by the Phase 1 user prompt (design §7).
func renderDesignDiffForPrompt(d DesignDiff) string {
	if len(d.Added) == 0 && len(d.Removed) == 0 && len(d.Modified) == 0 {
		return "(no design changes)"
	}
	var sb strings.Builder
	for _, a := range d.Added {
		sb.WriteString(fmt.Sprintf("- ADDED component: %s (%s, %s)\n", a.Name, a.ComponentType, a.Language))
	}
	for _, r := range d.Removed {
		sb.WriteString(fmt.Sprintf("- REMOVED component: %s\n", r.Name))
	}
	for _, m := range d.Modified {
		sb.WriteString(fmt.Sprintf("- MODIFIED component: %s\n", m.Name))
		if len(m.DependsOnAdded) > 0 || len(m.DependsOnRemoved) > 0 {
			parts := []string{}
			for _, d := range m.DependsOnAdded {
				parts = append(parts, "+ "+d)
			}
			for _, d := range m.DependsOnRemoved {
				parts = append(parts, "- "+d)
			}
			sb.WriteString(fmt.Sprintf("    - dependsOn: %s\n", strings.Join(parts, ", ")))
		}
		if len(m.OpenAPIOps) > 0 {
			sb.WriteString("    - openapi: ")
			sb.WriteString(strings.Join(m.OpenAPIOps, ", "))
			sb.WriteString("\n")
		}
	}
	return sb.String()
}

// BaselineBatch identifies the most recent live-tasked batch for a project,
// per design §7. The diff is computed against this batch's
// (SourceSpecVersion, SourceDesignVersion). Returns (nil, nil) if no
// baseline exists — caller treats as fresh-iteration mode.
type BaselineBatch struct {
	BatchID             string
	SourceSpecVersion   string
	SourceDesignVersion string
}

// loadBaselineBatch implements the SQL from design §7. "Live" means tasks
// in {pending, in_progress, ready_for_review, merged, building, deployed}.
// rejected / failed / abandoned do NOT qualify a batch as a baseline. All
// tasks in a batch share source_spec_version / source_design_version, so
// the GROUP BY is safe.
func loadBaselineBatch(ctx context.Context, db *gorm.DB, projectID string) (*BaselineBatch, error) {
	type row struct {
		BatchID             *string
		SourceSpecVersion   *string
		SourceDesignVersion *string
	}
	var r row
	q := `
		SELECT batch_id, source_spec_version, source_design_version
		FROM component_tasks
		WHERE project_id = ?
		  AND batch_id IS NOT NULL
		  AND status IN ('pending','in_progress','ready_for_review','merged','building','deployed')
		GROUP BY batch_id, source_spec_version, source_design_version
		ORDER BY MAX(created_at) DESC
		LIMIT 1
	`
	res := db.WithContext(ctx).Raw(q, projectID).Scan(&r)
	if res.Error != nil {
		return nil, fmt.Errorf("baseline batch query: %w", res.Error)
	}
	if res.RowsAffected == 0 || r.BatchID == nil {
		return nil, nil
	}
	out := &BaselineBatch{BatchID: *r.BatchID}
	if r.SourceSpecVersion != nil {
		out.SourceSpecVersion = *r.SourceSpecVersion
	}
	if r.SourceDesignVersion != nil {
		out.SourceDesignVersion = *r.SourceDesignVersion
	}
	return out, nil
}
