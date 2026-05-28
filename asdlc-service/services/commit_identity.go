package services

import (
	"context"
	"log/slog"
)

// commitIdentity returns the (authorName, authorEmail) tuple to use for
// platform-driven git commits (spec save, design save, etc.) under
// orgID.
//
// Phase 2 PR B: identity now comes from the org's active credential
// record — App mode returns the bot identity (asdlc-platform[bot]);
// PAT mode returns the PAT owner. The fallback covers newly-connected
// orgs with empty cache and transient lookup hiccups.
func commitIdentity(ctx context.Context, credentialSvc *CredentialService, orgID string) (name, email string) {
	const fallbackName = "ASDLC Bot"
	const fallbackEmail = "bot@asdlc.dev"

	if credentialSvc == nil || orgID == "" {
		return fallbackName, fallbackEmail
	}
	ident, err := credentialSvc.IdentityFor(ctx, orgID)
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
