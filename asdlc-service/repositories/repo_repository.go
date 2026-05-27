package repositories

import (
	"context"
	"errors"

	"github.com/wso2/asdlc/asdlc-service/models"
	"gorm.io/gorm"
)

// RepoRepository manages GitRepository persistence.
type RepoRepository interface {
	GetByProjectID(ctx context.Context, projectID string) (*models.GitRepository, error)
	GetByOrgAndProjectID(ctx context.Context, ocOrgID, projectID string) (*models.GitRepository, error)
	GetByOrgAndSlug(ctx context.Context, ocOrgID, repoSlug string) (*models.GitRepository, error)
	// ListAllReady returns every repo in `ready` status. Used by the
	// startup pre-warm path to ensure clones are on disk before traffic
	// arrives. Bounded by the table size; not paginated because the
	// caller bounds concurrency separately.
	ListAllReady(ctx context.Context) ([]models.GitRepository, error)
	Create(ctx context.Context, repo *models.GitRepository) error
	Update(ctx context.Context, repo *models.GitRepository) error
	Delete(ctx context.Context, projectID string) error
	DeleteAll(ctx context.Context) error
}

type repoRepository struct {
	db *gorm.DB
}

func NewRepoRepository(db *gorm.DB) RepoRepository {
	return &repoRepository{db: db}
}

func (r *repoRepository) GetByProjectID(ctx context.Context, projectID string) (*models.GitRepository, error) {
	var repo models.GitRepository
	if err := r.db.WithContext(ctx).Where("project_id = ?", projectID).First(&repo).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, err
	}
	return &repo, nil
}

// GetByOrgAndProjectID returns the repo row matching the (ocOrgID, projectID)
// tuple or nil. Used by the org-scope middleware on the new
// /api/v1/repos/{orgId}/{projectId}/... routes to fail loudly (404) when a
// caller passes a path that doesn't match a stored row, instead of silently
// cross-accessing another org's repo.
func (r *repoRepository) GetByOrgAndProjectID(ctx context.Context, ocOrgID, projectID string) (*models.GitRepository, error) {
	var repo models.GitRepository
	if err := r.db.WithContext(ctx).
		Where("org_id = ? AND project_id = ?", ocOrgID, projectID).
		First(&repo).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, err
	}
	return &repo, nil
}

// GetByOrgAndSlug returns the repo row matching the (ocOrgID, repoSlug) tuple
// or nil. The fence behind MintBuildToken — `repoSlug` is treated as untrusted
// input from the BFF and only resolves if there's an active matching row.
func (r *repoRepository) GetByOrgAndSlug(ctx context.Context, ocOrgID, repoSlug string) (*models.GitRepository, error) {
	var repo models.GitRepository
	if err := r.db.WithContext(ctx).
		Where("org_id = ? AND repo_slug = ?", ocOrgID, repoSlug).
		First(&repo).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, err
	}
	return &repo, nil
}

func (r *repoRepository) ListAllReady(ctx context.Context) ([]models.GitRepository, error) {
	var rows []models.GitRepository
	if err := r.db.WithContext(ctx).
		Where("status = ?", "ready").
		Find(&rows).Error; err != nil {
		return nil, err
	}
	return rows, nil
}

func (r *repoRepository) Create(ctx context.Context, repo *models.GitRepository) error {
	return r.db.WithContext(ctx).Create(repo).Error
}

func (r *repoRepository) Update(ctx context.Context, repo *models.GitRepository) error {
	return r.db.WithContext(ctx).Save(repo).Error
}

func (r *repoRepository) Delete(ctx context.Context, projectID string) error {
	return r.db.WithContext(ctx).Where("project_id = ?", projectID).Delete(&models.GitRepository{}).Error
}

func (r *repoRepository) DeleteAll(ctx context.Context) error {
	return r.db.WithContext(ctx).Where("1 = 1").Delete(&models.GitRepository{}).Error
}
