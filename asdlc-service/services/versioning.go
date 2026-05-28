package services

import (
	"fmt"

	"github.com/wso2/asdlc/asdlc-service/models"
)

// Tag scheme:
//   - Requirements: `v<N>` (versioned independently)
//   - Design:       `v<N>-<M>` where N is the source requirements version and
//     M is the design revision under that N. The "lineage" of a design
//     version is encoded in its tag name — no annotation parsing required.
//
// The BFF's models.ArtifactVersion is a flat shape used by both UIs. For
// design versions we surface the parent requirements tag in `SourceSpec` so
// the lineage label on the architecture page can render "Based on
// requirements v<N>" without duplicating the decode logic frontend-side.

// mapRequirementsVersions converts the artifact-service requirements version
// list to the BFF's flat ArtifactVersion shape.
func mapRequirementsVersions(versions []RequirementsVersionInfo) []models.ArtifactVersion {
	if len(versions) == 0 {
		return nil
	}
	out := make([]models.ArtifactVersion, 0, len(versions))
	for _, v := range versions {
		out = append(out, models.ArtifactVersion{
			Version:    v.Version,
			TagName:    v.Tag,
			CommitHash: v.CommitHash,
		})
	}
	return out
}

// mapDesignVersions converts the artifact-service design version list. The
// per-row Version field carries the design revision number (M); the
// SourceSpec field exposes the parent requirements tag (`v<N>`) so the UI
// can render lineage without re-parsing tag names.
func mapDesignVersions(versions []DesignVersionInfo) []models.ArtifactVersion {
	if len(versions) == 0 {
		return nil
	}
	out := make([]models.ArtifactVersion, 0, len(versions))
	for _, v := range versions {
		out = append(out, models.ArtifactVersion{
			Version:    v.DesignRevision,
			TagName:    v.Tag,
			CommitHash: v.CommitHash,
			SourceSpec: fmt.Sprintf("v%d", v.RequirementsVersion),
		})
	}
	return out
}
