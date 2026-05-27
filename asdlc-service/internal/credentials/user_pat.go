package credentials

import (
	"context"
	"time"

	"golang.org/x/sync/singleflight"
)

// userPATCred is the User-PAT-mode Credential. Each row in
// org_credentials with kind='user-pat' is materialised by orgResolver
// into one of these.
//
// Token() reads from OpenBao at secret/asdlc/{ocOrgID}/github/pat with
// singleflight collapsing concurrent reads. There is NO plaintext cache
// — phase2.md §1.13 / §6.2 explicitly drops the 30-min cache the
// evolution doc speculated about. The security trade (process-memory
// retention window) was undesirable; OpenBao reads are sub-10ms and
// bursts are absorbed by singleflight. Reachability is now an
// architectural property via the startup gate, not a runbook discipline.
type userPATCred struct {
	ocOrgID     string
	githubLogin string
	identity    Identity
	store       OpenBaoStore
	flight      *singleflight.Group
}

// Token returns the stored PAT. ExpiresAt is zero (long-lived) — the
// workspace credential helper treats zero as "no refresh needed".
func (c *userPATCred) Token(ctx context.Context) (string, time.Time, error) {
	v, err, _ := c.flight.Do(c.ocOrgID, func() (interface{}, error) {
		return c.store.Get(ctx, c.ocOrgID, "github/pat")
	})
	if err != nil {
		return "", time.Time{}, err
	}
	return string(v.([]byte)), time.Time{}, nil
}

// Identity returns the PAT owner's identity (resolved via GET /user at
// connect time, refreshed on PAT replace).
func (c *userPATCred) Identity() Identity { return c.identity }

// RepoOwner returns the GitHub org/user login chosen at connect time.
// Decoupled from ocOrgID — the OC org slug and the GitHub org slug can
// differ.
func (c *userPATCred) RepoOwner() string { return c.githubLogin }

// WebhookStrategy returns WebhookPerRepo — PAT mode registers a webhook
// on each repo at provision time using the org's webhook secret list.
func (c *userPATCred) WebhookStrategy() WebhookStrategy { return WebhookPerRepo }
