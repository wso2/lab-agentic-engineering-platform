package credentials

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"golang.org/x/sync/singleflight"
	"gorm.io/gorm"
)

// OrgNotActiveError is returned by orgResolver when a row exists but its
// status is not 'active' (e.g. 'suspended' or 'disconnected'). Callers
// surface this distinctly from "no row found" because the recovery
// differs (suspended → wait for unsuspend; disconnected → reconnect).
type OrgNotActiveError struct {
	OcOrgID string
	Status  string
}

func (e *OrgNotActiveError) Error() string {
	return fmt.Sprintf("credentials: org %q is not active (status=%s)", e.OcOrgID, e.Status)
}

// OrgNotFoundError is returned by orgResolver when no org_credentials row
// matches the supplied ocOrgID. Distinct from ErrEmptyOcOrgID (caller bug)
// and OrgNotActiveError (row exists but not active).
type OrgNotFoundError struct {
	OcOrgID string
}

func (e *OrgNotFoundError) Error() string {
	return fmt.Sprintf("credentials: no row for org %q", e.OcOrgID)
}

// orgResolver is the Phase 2 DB-backed Resolver. It looks up the
// org_credentials row for the supplied ocOrgID and dispatches by kind:
//
//   - kind='user-pat'         → userPATCred (singleflight + OpenBao)
//   - kind='app-installation' → appInstallationCred (delegates to minter)
//
// The kind switch is the only place in the codebase that branches on
// credential kind. Call sites consume the polymorphic Credential interface
// and never type-switch.
type orgResolver struct {
	db        *gorm.DB
	store     OpenBaoStore
	minter    *AppTokenMinter
	patFlight *singleflight.Group
}

// NewOrgResolver constructs the Phase 2 resolver. db, store, and minter
// must all be non-nil; in PR A the minter may be in "no app configured"
// mode (App private key not yet in OpenBao), in which case any
// app-installation row resolution falls through to ErrAppNotConfigured —
// but PR A's seed is user-pat only, so no resolutions reach the minter.
func NewOrgResolver(db *gorm.DB, store OpenBaoStore, minter *AppTokenMinter) Resolver {
	return &orgResolver{
		db:        db,
		store:     store,
		minter:    minter,
		patFlight: &singleflight.Group{},
	}
}

// orgCredentialRow mirrors models.OrgCredential without dragging the
// models package into pkg/credentials (keeping the dependency arrow
// pointing the right way: models → credentials, not the other way).
type orgCredentialRow struct {
	OcOrgID        string  `gorm:"column:oc_org_id"`
	Kind           string  `gorm:"column:kind"`
	GitHubLogin    string  `gorm:"column:github_login"`
	IdentityName   string  `gorm:"column:identity_name"`
	IdentityEmail  string  `gorm:"column:identity_email"`
	IdentityLogin  string  `gorm:"column:identity_login"`
	InstallationID *int64  `gorm:"column:installation_id"`
	Status         string  `gorm:"column:status"`
}

// TableName binds to the same table the models package owns.
func (orgCredentialRow) TableName() string { return "org_credentials" }

// Resolve looks up the credential record for ocOrgID and returns the
// matching polymorphic Credential.
func (r *orgResolver) Resolve(ctx context.Context, ocOrgID string) (Credential, error) {
	if ocOrgID == "" {
		return nil, ErrEmptyOcOrgID
	}

	var row orgCredentialRow
	err := r.db.WithContext(ctx).Where("oc_org_id = ?", ocOrgID).First(&row).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, &OrgNotFoundError{OcOrgID: ocOrgID}
		}
		return nil, fmt.Errorf("credentials: lookup org %q: %w", ocOrgID, err)
	}

	if row.Status != "active" {
		return nil, &OrgNotActiveError{OcOrgID: ocOrgID, Status: row.Status}
	}

	identity := Identity{
		Name:  row.IdentityName,
		Email: row.IdentityEmail,
		Login: row.IdentityLogin,
	}

	slog.DebugContext(ctx, "credentials.resolved",
		"kind", row.Kind,
		"ocOrgId", ocOrgID,
		"identityLogin", row.IdentityLogin)

	switch row.Kind {
	case "user-pat":
		return &userPATCred{
			ocOrgID:     ocOrgID,
			githubLogin: row.GitHubLogin,
			identity:    identity,
			store:       r.store,
			flight:      r.patFlight,
		}, nil
	case "app-installation":
		if row.InstallationID == nil {
			return nil, fmt.Errorf("credentials: app-installation row missing installation_id (org %q)", ocOrgID)
		}
		return &appInstallationCred{
			installationID: *row.InstallationID,
			accountLogin:   row.GitHubLogin,
			identity:       identity,
			minter:         r.minter,
		}, nil
	default:
		return nil, fmt.Errorf("credentials: unknown kind %q (org %q)", row.Kind, ocOrgID)
	}
}
