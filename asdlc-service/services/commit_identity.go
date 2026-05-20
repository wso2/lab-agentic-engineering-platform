package services

import (
	"context"
	"log/slog"

	"github.com/wso2/asdlc/asdlc-service/clients/gitservice"
)

// commitIdentity returns the (authorName, authorEmail) tuple to use for
// platform-driven git commits (spec save, design save, etc.) under
// orgID.
//
// Phase 2 PR B: identity now comes from the org's active credential
// record — App mode returns the bot identity (asdlc-platform[bot]);
// PAT mode returns the PAT owner. Phase 0 hardcoded "ASDLC Bot /
// bot@asdlc.dev"; PR B replaces the hardcode while keeping the same
// shape as a fallback for the case where the lookup fails (newly-
// connected org with empty cache, transient git-service hiccup).
func commitIdentity(ctx context.Context, gitClient gitservice.Client, orgID string) (name, email string) {
	const fallbackName = "ASDLC Bot"
	const fallbackEmail = "bot@asdlc.dev"

	if gitClient == nil || orgID == "" {
		return fallbackName, fallbackEmail
	}
	ident, err := gitClient.GetCredentialIdentity(ctx, orgID)
	if err != nil {
		slog.WarnContext(ctx, "commit identity lookup failed; falling back to default",
			"orgId", orgID, "error", err)
		return fallbackName, fallbackEmail
	}
	if ident == nil || ident.Name == "" {
		return fallbackName, fallbackEmail
	}
	return ident.Name, ident.Email
}
