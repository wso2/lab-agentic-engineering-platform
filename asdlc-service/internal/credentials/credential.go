// Package credentials is the single seam for GitHub authentication in
// git-service. Every code path that calls GitHub or runs `git` against a
// remote routes through Resolver.Resolve(ocOrgID) to obtain a Credential,
// then asks the credential for a token, identity, repo-owner, or webhook
// strategy as needed.
//
// The Phase 0 implementation (PlatformPATResolver) returns a single platform
// PAT for every org. Phase 2 introduces App-installation and per-org
// user-PAT kinds; call sites do not change because they consume the
// polymorphic Credential surface.
//
// Three architectural rules these types enforce:
//
//   1. No call site type-switches on Credential.
//   2. No call site reads identity, repo-owner, or token from any other
//      source — not env, not the GitRepository row.
//   3. Every external GitHub operation passes ocOrgID explicitly. Resolvers
//      refuse an empty ocOrgID.
package credentials

import (
	"context"
	"errors"
	"time"
)

// Credential is a polymorphic surface over the ways the platform can
// authenticate to GitHub. Phase 0 has one implementation (platform PAT);
// Phase 2 adds App-installation and per-org user-PAT.
//
// Callers MUST NOT type-switch on the implementation.
type Credential interface {
	// Token returns a usable GitHub token and the time at which it stops
	// being valid. Long-lived kinds may return time.Time{} (zero) to
	// indicate "never expires" — callers treat zero as "no refresh needed".
	Token(ctx context.Context) (token string, expiresAt time.Time, err error)

	// Identity returns the committer attribution this credential maps to.
	Identity() Identity

	// RepoOwner returns the GitHub org/user login under which new repos are
	// provisioned. App mode: the install's account login. PAT mode: the
	// GitHub org chosen at connect time.
	RepoOwner() string

	// WebhookStrategy says how the platform should arrange event delivery
	// for repos using this credential. Some kinds answer "register a
	// per-repo hook"; others answer "rely on platform-level delivery, do
	// nothing." Callers dispatch the strategy without inspecting which
	// kind it is.
	WebhookStrategy() WebhookStrategy
}

// Identity is the committer attribution surfaced by a Credential. The Login
// field is the GitHub user/bot login (used for hosts.yml + audit); Name and
// Email are what go on git commit author/committer headers.
type Identity struct {
	Name  string
	Email string
	Login string
}

// WebhookStrategy enumerates how the platform arranges event delivery for
// repos backed by a given Credential.
type WebhookStrategy int

const (
	// WebhookPerRepo says: register a webhook on each repo at provision time.
	// Phase 0 platform-PAT and Phase 2 user-PAT use this strategy.
	WebhookPerRepo WebhookStrategy = iota
	// WebhookPlatform says: event delivery is platform-wide (a GitHub App's
	// configured callback). Phase 2 App-installation uses this strategy.
	WebhookPlatform
)

// Resolver resolves the credential for a given organisation.
//
// Phase 0 ignores ocOrgID (there is one credential, shared across all
// orgs); Phase 2 looks it up against the per-org connection record. The
// parameter is MANDATORY in Phase 0 even though it's unused — call sites
// that pass it from day one don't need to be revisited when Phase 2 lands.
type Resolver interface {
	Resolve(ctx context.Context, ocOrgID string) (Credential, error)
}

// ErrEmptyOcOrgID is returned by resolvers when an empty ocOrgID is passed.
// This is the multi-tenant invariant — every external GitHub op names the
// org it acts for.
var ErrEmptyOcOrgID = errors.New("credentials: ocOrgID is required")
