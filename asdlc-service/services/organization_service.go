package services

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"golang.org/x/sync/singleflight"
	"gorm.io/gorm"

	"github.com/wso2/asdlc/asdlc-service/clients/openchoreo"
	"github.com/wso2/asdlc/asdlc-service/models"
)

// ensureCacheTTL bounds how long a successful EnsureForOuHandle result
// suppresses re-verification. Short enough that a deleted+recreated
// namespace re-resolves promptly; long enough to absorb the 3s/5s
// progress polls without per-request DB hits.
const ensureCacheTTL = 5 * time.Minute

// OrganizationService is the BFF's read-and-cache view of OC organizations.
//
// An organization maps 1:1 to an OpenChoreo namespace (which itself maps 1:1
// to a Kubernetes namespace). The local `organizations` table is a UUID
// side-car so other tables can foreign-key onto an org without depending on
// the namespace name.
//
// The BFF does NOT create OC namespaces. Tenant onboarding is the platform's
// job — in hosted environments, `platform-api-service` creates the namespace
// in response to Thunder's `notify_org_created` webhook. In local dev, the
// `seed-admin-org.sh` step in `setup.sh` does the equivalent at install time.
// Both paths land identical state in OC; the BFF reads it.
type OrganizationService interface {
	List(ctx context.Context) (*models.OrganizationList, error)
	// EnsureForOuHandle verifies that the OC namespace named after the
	// caller's `ouHandle` exists, and caches the local Organization row's
	// UUID for FK use. It does NOT create the namespace — if OC reports
	// 404, the call returns ErrOrganizationNotProvisioned and the auth
	// middleware passes through, letting the controller surface a
	// user-meaningful error.
	//
	// `thunderOrgUUID` is the authoritative org UUID from the JWT's `ouId`
	// claim (empty for unauthenticated or non-user-JWT paths). When
	// non-empty and missing/stale on the local row, it's persisted onto
	// `thunder_org_uuid` so downstream callers (dispatcher / SMAPIWriter)
	// can compute the same per-org NS SM-API derives.
	EnsureForOuHandle(ctx context.Context, ouHandle string, thunderOrgUUID string) error
}

// ErrOrganizationNotProvisioned signals that the inbound JWT's `ouHandle`
// has no matching OC namespace yet. In hosted that means
// platform-api-service hasn't finished onboarding the user; the user's
// next request usually succeeds. In local dev it usually means
// `seed-admin-org.sh` did not run.
var ErrOrganizationNotProvisioned = errors.New("organization namespace not provisioned")

type organizationService struct {
	db    *gorm.DB
	nsCli openchoreo.NamespaceClient

	// ensureCache memoises EnsureForOuHandle's "yes, verified" result
	// for ensureCacheTTL so the auth middleware doesn't pay a DB+OC
	// round-trip on every authenticated request. Misses + errors are
	// not cached. Mirrors agent-manager's
	// publisher_credential_provisioner pattern (singleflight per
	// orgName + short-lived in-memory cache).
	ensureMu       sync.RWMutex
	ensureCache    map[string]time.Time
	ensureInflight singleflight.Group
}

func NewOrganizationService(db *gorm.DB, nsCli openchoreo.NamespaceClient) OrganizationService {
	return &organizationService{
		db:          db,
		nsCli:       nsCli,
		ensureCache: map[string]time.Time{},
	}
}

// List returns every namespace the BFF can see in OC, joined with the local
// Organization rows. Namespaces without a local row get one inserted on the
// fly (idempotent on UNIQUE name) so OC namespaces pick up a UUID without
// an explicit migration step.
func (s *organizationService) List(ctx context.Context) (*models.OrganizationList, error) {
	views, err := s.nsCli.ListNamespaces(ctx)
	if err != nil {
		return nil, translateHTTPError(err)
	}

	if len(views) == 0 {
		return &models.OrganizationList{Items: []models.OrganizationView{}}, nil
	}

	names := make([]string, 0, len(views))
	for _, v := range views {
		names = append(names, v.Name)
	}

	var rows []models.Organization
	if err := s.db.WithContext(ctx).Where("name IN ?", names).Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("load organizations: %w", err)
	}
	byName := make(map[string]models.Organization, len(rows))
	for _, r := range rows {
		byName[r.Name] = r
	}

	for i, v := range views {
		row, ok := byName[v.Name]
		if !ok {
			row = s.backfillRow(ctx, v, "")
			if row.UUID == uuid.Nil {
				continue
			}
		}
		views[i].UUID = row.UUID
		views[i].CreatedAt = row.CreatedAt
		if views[i].DisplayName == "" {
			views[i].DisplayName = row.DisplayName
		}
	}

	return &models.OrganizationList{Items: views}, nil
}

// EnsureForOuHandle is the auth-middleware verify-and-cache path. It
// confirms the OC namespace named `ouHandle` exists and that we have a
// local row for it. On success the next handler runs with the cache
// warmed; on missing namespace it returns ErrOrganizationNotProvisioned
// which the middleware logs and lets through.
func (s *organizationService) EnsureForOuHandle(ctx context.Context, ouHandle string, thunderOrgUUID string) error {
	if ouHandle == "" {
		return fmt.Errorf("ouHandle is required")
	}

	// Hot path: recently-verified ouHandle. The thunder UUID
	// backfill is not gated by the cache — it runs once per (ouHandle,
	// thunderUUID) pair via singleflight so a stale row gets fixed up
	// even when the verify cache is warm.
	s.ensureMu.RLock()
	verifiedAt, ok := s.ensureCache[ouHandle]
	s.ensureMu.RUnlock()
	cacheWarm := ok && time.Since(verifiedAt) < ensureCacheTTL

	if !cacheWarm {
		// Coalesce concurrent first-sights of the same handle into one
		// DB+OC verify.
		if _, err, _ := s.ensureInflight.Do(ouHandle, func() (any, error) {
			// Re-check the cache inside the singleflight critical
			// section — a sibling call may have just populated it.
			s.ensureMu.RLock()
			verifiedAt, ok := s.ensureCache[ouHandle]
			s.ensureMu.RUnlock()
			if ok && time.Since(verifiedAt) < ensureCacheTTL {
				return nil, nil
			}
			if err := s.verifyForOuHandle(ctx, ouHandle, thunderOrgUUID); err != nil {
				return nil, err
			}
			s.ensureMu.Lock()
			s.ensureCache[ouHandle] = time.Now()
			s.ensureMu.Unlock()
			return nil, nil
		}); err != nil {
			return err
		}
	}

	// Best-effort: backfill thunder_org_uuid if it's missing on the row
	// (idempotent — the SQL is a no-op when already set to the right
	// value, and logs a warning on drift). Cheap UPDATE so we don't
	// guard it behind the verify cache.
	if thunderOrgUUID != "" {
		s.ensureThunderUUID(ctx, ouHandle, thunderOrgUUID)
	}
	return nil
}

func (s *organizationService) verifyForOuHandle(ctx context.Context, ouHandle, thunderOrgUUID string) error {
	var row models.Organization
	switch err := s.db.WithContext(ctx).Where("name = ?", ouHandle).First(&row).Error; {
	case err == nil:
		return nil
	case errors.Is(err, gorm.ErrRecordNotFound):
		// fall through to OC verify
	default:
		return fmt.Errorf("lookup organization: %w", err)
	}

	view, err := s.nsCli.GetNamespace(ctx, ouHandle)
	if err != nil {
		if errors.Is(err, openchoreo.ErrNotFound) {
			return fmt.Errorf("%w: %s", ErrOrganizationNotProvisioned, ouHandle)
		}
		return translateHTTPError(err)
	}

	s.backfillRow(ctx, *view, thunderOrgUUID)
	return nil
}

// ensureThunderUUID upserts the Thunder org UUID onto the local row.
// No-op when the row already carries the same value; logs+overwrites
// on drift (Thunder is authoritative).
func (s *organizationService) ensureThunderUUID(ctx context.Context, ouHandle, thunderOrgUUID string) {
	parsed, err := uuid.Parse(thunderOrgUUID)
	if err != nil {
		slog.WarnContext(ctx, "ensureThunderUUID: invalid UUID in JWT", "ouHandle", ouHandle, "thunderOrgUUID", thunderOrgUUID, "error", err)
		return
	}
	var row models.Organization
	if err := s.db.WithContext(ctx).Where("name = ?", ouHandle).First(&row).Error; err != nil {
		// Row may not exist yet (verify failed earlier); caller already logged.
		return
	}
	if row.ThunderOrgUUID != nil && *row.ThunderOrgUUID == parsed {
		return
	}
	if row.ThunderOrgUUID != nil {
		slog.WarnContext(ctx, "ensureThunderUUID: row UUID differs from JWT — overwriting (Thunder is authoritative)",
			"ouHandle", ouHandle, "current", row.ThunderOrgUUID.String(), "newFromJWT", parsed.String())
	}
	if err := s.db.WithContext(ctx).
		Model(&models.Organization{}).
		Where("name = ?", ouHandle).
		Update("thunder_org_uuid", parsed).Error; err != nil {
		slog.WarnContext(ctx, "ensureThunderUUID: update failed", "ouHandle", ouHandle, "error", err)
	}
}

// backfillRow inserts a local row for an OC namespace we just discovered.
// Returns the resulting (possibly racing) row; on hard failure returns a
// zero row and logs.
func (s *organizationService) backfillRow(ctx context.Context, view models.OrganizationView, thunderOrgUUID string) models.Organization {
	row := models.Organization{
		UUID:        uuid.New(),
		Name:        view.Name,
		DisplayName: view.DisplayName,
	}
	if thunderOrgUUID != "" {
		if parsed, perr := uuid.Parse(thunderOrgUUID); perr == nil {
			row.ThunderOrgUUID = &parsed
		} else {
			slog.WarnContext(ctx, "backfillRow: invalid Thunder UUID in JWT — leaving column NULL", "name", view.Name, "thunderOrgUUID", thunderOrgUUID, "error", perr)
		}
	}
	err := s.db.WithContext(ctx).Create(&row).Error
	if err == nil {
		return row
	}
	if isUniqueViolation(err) {
		// Lost the race with a concurrent caller; re-read.
		if rerr := s.db.WithContext(ctx).Where("name = ?", view.Name).First(&row).Error; rerr == nil {
			return row
		}
	}
	slog.WarnContext(ctx, "backfill organization row failed", "name", view.Name, "error", err)
	return models.Organization{}
}

func isUniqueViolation(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, gorm.ErrDuplicatedKey) {
		return true
	}
	msg := err.Error()
	return strings.Contains(msg, "duplicate key") || strings.Contains(msg, "unique constraint")
}
