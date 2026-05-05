package models

// ArtifactVersion describes a tagged version of an artifact.
type ArtifactVersion struct {
	Version      int    `json:"version"`
	TagName      string `json:"tagName"`
	CommitHash   string `json:"commitHash"`
	SourceSpec   string `json:"sourceSpec,omitempty"`
	SourceDesign string `json:"sourceDesign,omitempty"`
}
