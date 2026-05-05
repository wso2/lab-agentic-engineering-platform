package services

import (
	"github.com/wso2/asdlc/asdlc-service/clients/gitservice"
	"github.com/wso2/asdlc/asdlc-service/models"
)

// PR 2 of the repo-storage-ownership refactor: this file used to host the
// BFF's tag-message parsing and version-bumping logic. Both moved into
// git-service (which now returns structured Lineage in API responses), so
// what remains here is a thin adapter that maps the wire shape to the BFF's
// existing models.ArtifactVersion struct used by spec/design/UI code.
//
// Specifically deleted in this PR (do not bring back):
//   - parseLineage / buildTagMessage (tag-body format is private to git-service)
//   - hasChangedSinceLastTag (the save endpoint computes this server-side)
//   - nextVersion / latestTagVersion / latestTagName (server-side)
//   - tagsToVersions (replaced by mapArtifactVersions below — same idea, different input type)

// mapArtifactVersions converts the gitservice client wire shape to the BFF's
// existing models.ArtifactVersion. Both shapes are descending-by-version
// already; we just rename fields. Lineage flows through as flat
// SourceSpec/SourceDesign strings that the UI displays as chips.
func mapArtifactVersions(versions []gitservice.ArtifactVersionInfo) []models.ArtifactVersion {
	if len(versions) == 0 {
		return nil
	}
	out := make([]models.ArtifactVersion, 0, len(versions))
	for _, v := range versions {
		out = append(out, models.ArtifactVersion{
			Version:      v.Version,
			TagName:      v.Tag,
			CommitHash:   v.CommitHash,
			SourceSpec:   v.Lineage.SourceSpec,
			SourceDesign: v.Lineage.SourceDesign,
		})
	}
	return out
}
