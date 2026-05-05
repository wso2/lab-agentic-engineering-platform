package services

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/wso2/asdlc/asdlc-service/clients/openchoreo"
	"github.com/wso2/asdlc/asdlc-service/middleware/jwt"
	"github.com/wso2/asdlc/asdlc-service/models"
)

// ErrOrganizationExists is returned when a Create attempt collides with an
// existing organization name (DB UNIQUE) or namespace (OC 409). Maps to 409.
var ErrOrganizationExists = errors.New("organization already exists")

// OrganizationService handles ASDLC organization operations.
//
// An ASDLC organization maps 1:1 to an OpenChoreo namespace. The local
// `organizations` table is a UUID side-car so other tables can foreign-key
// onto an org without depending on the namespace name.
type OrganizationService interface {
	List(ctx context.Context) (*models.OrganizationList, error)
	Create(ctx context.Context, req *models.CreateOrganizationRequest) (*models.OrganizationView, error)
}

type organizationService struct {
	db    *gorm.DB
	nsCli openchoreo.NamespaceClient
}

func NewOrganizationService(db *gorm.DB, nsCli openchoreo.NamespaceClient) OrganizationService {
	return &organizationService{db: db, nsCli: nsCli}
}

// List returns every namespace the BFF can see in OC, joined with the local
// Organization rows. Namespaces without a local row get one inserted on the
// fly (idempotent on UNIQUE name) so older OC namespaces and the seeded
// `default` org pick up a UUID without an explicit migration step.
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
			// Backfill: an OC namespace exists but we have no local row yet.
			row = models.Organization{
				UUID:        uuid.New(),
				Name:        v.Name,
				DisplayName: v.DisplayName,
			}
			if err := s.db.WithContext(ctx).Create(&row).Error; err != nil {
				if !isUniqueViolation(err) {
					slog.WarnContext(ctx, "backfill organization row failed", "name", v.Name, "error", err)
					continue
				}
				// Lost the race with a concurrent List; re-read to pick up the UUID.
				if err := s.db.WithContext(ctx).Where("name = ?", v.Name).First(&row).Error; err != nil {
					slog.WarnContext(ctx, "re-read after race lost", "name", v.Name, "error", err)
					continue
				}
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

// Create inserts a placeholder local row, calls OC to create the namespace,
// and rolls back the row on OC failure. The DB UNIQUE on `name` makes
// concurrent creates of the same handle safe — first INSERT wins, OC is
// only called by the winner.
func (s *organizationService) Create(ctx context.Context, req *models.CreateOrganizationRequest) (*models.OrganizationView, error) {
	if req == nil || strings.TrimSpace(req.Name) == "" {
		return nil, fmt.Errorf("name is required")
	}
	name := strings.TrimSpace(req.Name)
	displayName := strings.TrimSpace(req.DisplayName)
	description := strings.TrimSpace(req.Description)

	createdBy := ""
	if claims := jwt.ClaimsFromContext(ctx); claims != nil {
		createdBy = claims.Subject
	}

	row := models.Organization{
		UUID:        uuid.New(),
		Name:        name,
		DisplayName: displayName,
		CreatedBy:   createdBy,
	}
	if err := s.db.WithContext(ctx).Create(&row).Error; err != nil {
		if isUniqueViolation(err) {
			return nil, ErrOrganizationExists
		}
		return nil, fmt.Errorf("insert organization: %w", err)
	}

	view, err := s.nsCli.CreateNamespace(ctx, name, displayName, description)
	if err != nil {
		// Rollback: delete the placeholder row so a retry can re-attempt.
		if delErr := s.db.WithContext(ctx).Delete(&models.Organization{}, "name = ?", name).Error; delErr != nil {
			slog.ErrorContext(ctx, "rollback organization row failed", "name", name, "error", delErr)
		}
		return nil, translateHTTPError(err)
	}

	view.UUID = row.UUID
	view.CreatedAt = row.CreatedAt
	if view.DisplayName == "" {
		view.DisplayName = displayName
	}
	if view.Description == "" {
		view.Description = description
	}
	return view, nil
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
