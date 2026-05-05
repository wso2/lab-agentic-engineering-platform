package services

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// Tag prefix constants. One source of truth for the versioning conventions
// (mirrored on the BFF side for *referencing* tags by name; the format itself
// is enforced here). PR 1 of the repo-storage-ownership refactor moved these
// from the BFF when versioning logic became git-service's responsibility.
const (
	specTagPrefix   = "spec-v"
	designTagPrefix = "design-v"
)

// Lineage carries upstream-version metadata about an artifact tag. Stored as
// `source-spec: spec-vN` / `source-design: design-vM` lines in the tag's
// annotated message; structured here so the API can return it without the
// BFF having to parse tag bodies.
type Lineage struct {
	SourceSpec   string `json:"sourceSpec,omitempty"`
	SourceDesign string `json:"sourceDesign,omitempty"`
}

// nextVersion inspects a list of tags with the given prefix (e.g. "spec-v")
// and returns the next version number and full tag name.
func nextVersion(tags []TagInfo, prefix string) (int, string) {
	max := latestTagVersion(tags, prefix)
	next := max + 1
	return next, fmt.Sprintf("%s%d", prefix, next)
}

// latestTagVersion returns the highest version number for tags with the
// given prefix, or 0 if none match.
func latestTagVersion(tags []TagInfo, prefix string) int {
	maxVersion := 0
	for _, t := range tags {
		if !strings.HasPrefix(t.Name, prefix) {
			continue
		}
		numStr := strings.TrimPrefix(t.Name, prefix)
		if n, err := strconv.Atoi(numStr); err == nil && n > maxVersion {
			maxVersion = n
		}
	}
	return maxVersion
}

// latestTagName returns the highest-versioned tag's full name for the given
// prefix, or "" when no tag matches.
func latestTagName(tags []TagInfo, prefix string) string {
	max := latestTagVersion(tags, prefix)
	if max == 0 {
		return ""
	}
	return fmt.Sprintf("%s%d", prefix, max)
}

// parseLineage extracts the structured upstream metadata from an annotated
// tag's body. The on-tag format is private to git-service — callers consume
// the typed Lineage via the API.
func parseLineage(message string) Lineage {
	var l Lineage
	for _, line := range strings.Split(message, "\n") {
		line = strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(line, "source-spec:"):
			l.SourceSpec = strings.TrimSpace(strings.TrimPrefix(line, "source-spec:"))
		case strings.HasPrefix(line, "source-design:"):
			l.SourceDesign = strings.TrimSpace(strings.TrimPrefix(line, "source-design:"))
		}
	}
	return l
}

// buildLineageMessage assembles a tag annotation body: the human-readable
// description on the first line, then any non-empty lineage fields as
// `source-X: <tag>` lines. Symmetric with parseLineage above.
func buildLineageMessage(description string, l Lineage) string {
	var sb strings.Builder
	sb.WriteString(description)
	if l.SourceSpec != "" {
		sb.WriteString("\nsource-spec: ")
		sb.WriteString(l.SourceSpec)
	}
	if l.SourceDesign != "" {
		sb.WriteString("\nsource-design: ")
		sb.WriteString(l.SourceDesign)
	}
	return sb.String()
}

// VersionInfo is the wire shape the API returns for each annotated tag in a
// versions list.
type VersionInfo struct {
	Tag        string  `json:"tag"`
	Version    int     `json:"version"`
	CommitHash string  `json:"commitHash"`
	Message    string  `json:"message"`
	Lineage    Lineage `json:"lineage"`
}

// tagsToVersions converts a list of TagInfo into sorted VersionInfo
// (descending by version) for a given prefix.
func tagsToVersions(tags []TagInfo, prefix string) []VersionInfo {
	versions := make([]VersionInfo, 0, len(tags))
	for _, t := range tags {
		if !strings.HasPrefix(t.Name, prefix) {
			continue
		}
		numStr := strings.TrimPrefix(t.Name, prefix)
		n, err := strconv.Atoi(numStr)
		if err != nil {
			continue
		}
		versions = append(versions, VersionInfo{
			Tag:        t.Name,
			Version:    n,
			CommitHash: t.CommitHash,
			Message:    t.Message,
			Lineage:    parseLineage(t.Message),
		})
	}
	sort.Slice(versions, func(i, j int) bool {
		return versions[i].Version > versions[j].Version
	})
	return versions
}
