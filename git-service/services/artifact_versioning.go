package services

import (
	"fmt"
	"regexp"
	"sort"
	"strconv"
)

// Tag scheme:
//   - Requirements: `v<N>` (e.g. `v1`, `v2`)
//   - Design:       `v<N>-<M>` where N is the source requirements version and
//     M is the design revision under that N (e.g. `v1-1`, `v2-3`)
//
// Lineage is encoded in the tag name itself — there are no `source-*:`
// annotation lines. The annotation body is a free-text save message.

var (
	requirementsTagRE = regexp.MustCompile(`^v(\d+)$`)
	designTagRE       = regexp.MustCompile(`^v(\d+)-(\d+)$`)
)

// requirementsTagFor returns the canonical tag name for a requirements
// version (e.g. requirementsTagFor(2) == "v2").
func requirementsTagFor(version int) string {
	return fmt.Sprintf("v%d", version)
}

// designTagFor returns the canonical tag name for a design (parent, revision)
// pair (e.g. designTagFor(1, 2) == "v1-2").
func designTagFor(parentVersion, revision int) string {
	return fmt.Sprintf("v%d-%d", parentVersion, revision)
}

// parseRequirementsTag returns the version number for a `v<N>` tag, or
// (0, false) if the name doesn't match.
func parseRequirementsTag(name string) (int, bool) {
	m := requirementsTagRE.FindStringSubmatch(name)
	if m == nil {
		return 0, false
	}
	n, err := strconv.Atoi(m[1])
	if err != nil || n < 1 {
		return 0, false
	}
	return n, true
}

// parseDesignTag returns (parentVersion, revision) for a `v<N>-<M>` tag, or
// (0, 0, false) if the name doesn't match.
func parseDesignTag(name string) (int, int, bool) {
	m := designTagRE.FindStringSubmatch(name)
	if m == nil {
		return 0, 0, false
	}
	n, err := strconv.Atoi(m[1])
	if err != nil || n < 1 {
		return 0, 0, false
	}
	r, err := strconv.Atoi(m[2])
	if err != nil || r < 1 {
		return 0, 0, false
	}
	return n, r, true
}

// latestRequirementsVersion returns the highest N from any `v<N>` tag, or 0
// if none match.
func latestRequirementsVersion(tags []TagInfo) int {
	max := 0
	for _, t := range tags {
		if n, ok := parseRequirementsTag(t.Name); ok && n > max {
			max = n
		}
	}
	return max
}

// nextRequirementsTag returns the next `v<N+1>` tag name and its version.
func nextRequirementsTag(tags []TagInfo) (int, string) {
	next := latestRequirementsVersion(tags) + 1
	return next, requirementsTagFor(next)
}

// latestDesignRevision returns the highest M for any `v<parentVersion>-<M>`
// tag, or 0 if none match.
func latestDesignRevision(tags []TagInfo, parentVersion int) int {
	max := 0
	for _, t := range tags {
		n, r, ok := parseDesignTag(t.Name)
		if !ok || n != parentVersion {
			continue
		}
		if r > max {
			max = r
		}
	}
	return max
}

// nextDesignTag returns the next `v<parentVersion>-<M+1>` tag name and its
// revision number for the supplied parent requirements version.
func nextDesignTag(tags []TagInfo, parentVersion int) (int, string) {
	next := latestDesignRevision(tags, parentVersion) + 1
	return next, designTagFor(parentVersion, next)
}

// latestRequirementsTag returns the full tag name of the highest-versioned
// `v<N>` tag, or "" when none exist.
func latestRequirementsTag(tags []TagInfo) string {
	max := latestRequirementsVersion(tags)
	if max == 0 {
		return ""
	}
	return requirementsTagFor(max)
}

// latestDesignTag returns the highest-revision `v<N>-<M>` tag (across any
// parent N) by lexical (N, M) order, or "" when none exist.
func latestDesignTag(tags []TagInfo) string {
	bestN, bestR := 0, 0
	for _, t := range tags {
		n, r, ok := parseDesignTag(t.Name)
		if !ok {
			continue
		}
		if n > bestN || (n == bestN && r > bestR) {
			bestN, bestR = n, r
		}
	}
	if bestN == 0 {
		return ""
	}
	return designTagFor(bestN, bestR)
}

// RequirementsVersionInfo is the wire shape returned for each `v<N>` tag.
type RequirementsVersionInfo struct {
	Tag        string `json:"tag"`
	Version    int    `json:"version"`
	CommitHash string `json:"commitHash"`
	Message    string `json:"message"`
}

// DesignVersionInfo is the wire shape returned for each `v<N>-<M>` tag.
type DesignVersionInfo struct {
	Tag                 string `json:"tag"`
	RequirementsVersion int    `json:"requirementsVersion"`
	DesignRevision      int    `json:"designRevision"`
	CommitHash          string `json:"commitHash"`
	Message             string `json:"message"`
}

// tagsToRequirementsVersions filters + sorts a tag list into the
// requirements-only version list (descending by N).
func tagsToRequirementsVersions(tags []TagInfo) []RequirementsVersionInfo {
	out := make([]RequirementsVersionInfo, 0, len(tags))
	for _, t := range tags {
		n, ok := parseRequirementsTag(t.Name)
		if !ok {
			continue
		}
		out = append(out, RequirementsVersionInfo{
			Tag:        t.Name,
			Version:    n,
			CommitHash: t.CommitHash,
			Message:    t.Message,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Version > out[j].Version })
	return out
}

// tagsToDesignVersions filters + sorts a tag list into the design-only
// version list (descending by (N, M)).
func tagsToDesignVersions(tags []TagInfo) []DesignVersionInfo {
	out := make([]DesignVersionInfo, 0, len(tags))
	for _, t := range tags {
		n, r, ok := parseDesignTag(t.Name)
		if !ok {
			continue
		}
		out = append(out, DesignVersionInfo{
			Tag:                 t.Name,
			RequirementsVersion: n,
			DesignRevision:      r,
			CommitHash:          t.CommitHash,
			Message:             t.Message,
		})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].RequirementsVersion != out[j].RequirementsVersion {
			return out[i].RequirementsVersion > out[j].RequirementsVersion
		}
		return out[i].DesignRevision > out[j].DesignRevision
	})
	return out
}
