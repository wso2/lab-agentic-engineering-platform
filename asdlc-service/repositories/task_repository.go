package repositories

import (
	"context"
	"errors"
	"time"

	"gorm.io/gorm"

	"github.com/wso2/asdlc/asdlc-service/models"
)

// ListByOrgFilter narrows an org-scoped list query to tasks matching the
// given status, cause, and lower-bound on the last update. Empty/nil
// fields are unconstrained.
type ListByOrgFilter struct {
	Status string
	Cause  string
	Since  *time.Time
}

type TaskRepository interface {
	GetByID(ctx context.Context, id string) (*models.ComponentTask, error)
	GetByComponentName(ctx context.Context, orgID, projectID, componentName string) (*models.ComponentTask, error)
	GetByIssueURL(ctx context.Context, issueURL string) (*models.ComponentTask, error)
	ListByProjectID(ctx context.Context, orgID, projectID string) ([]models.ComponentTask, error)
	// ListNonTerminalByOrgID returns every task under orgID whose status
	// is non-terminal. Used by the Phase 2 PR B disconnect cascade
	// (services/org_disconnect_service.go).
	ListNonTerminalByOrgID(ctx context.Context, orgID string) ([]models.ComponentTask, error)
	// ListByOrgID returns every task under orgID matching the optional
	// status / cause / since filter. Used by the PR D
	// ReachReconciliationBanner (status='abandoned', cause='repo.unselected',
	// since=now-24h) and by future audit UIs.
	ListByOrgID(ctx context.Context, orgID string, f ListByOrgFilter) ([]models.ComponentTask, error)
	Create(ctx context.Context, task *models.ComponentTask) error
	Update(ctx context.Context, task *models.ComponentTask) error
	DeleteByProjectID(ctx context.Context, orgID, projectID string) error
	DeleteAll(ctx context.Context) error
}

type taskRepository struct {
	db *gorm.DB
}

func NewTaskRepository(db *gorm.DB) TaskRepository {
	return &taskRepository{db: db}
}

func (r *taskRepository) GetByID(ctx context.Context, id string) (*models.ComponentTask, error) {
	var task models.ComponentTask
	err := r.db.WithContext(ctx).First(&task, "id = ?", id).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &task, nil
}

func (r *taskRepository) GetByComponentName(ctx context.Context, orgID, projectID, componentName string) (*models.ComponentTask, error) {
	var task models.ComponentTask
	err := r.db.WithContext(ctx).
		Where("org_id = ? AND project_id = ? AND component_name = ?", orgID, projectID, componentName).
		First(&task).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &task, nil
}

func (r *taskRepository) GetByIssueURL(ctx context.Context, issueURL string) (*models.ComponentTask, error) {
	var task models.ComponentTask
	err := r.db.WithContext(ctx).
		Where("issue_url = ?", issueURL).
		First(&task).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &task, nil
}

func (r *taskRepository) ListByProjectID(ctx context.Context, orgID, projectID string) ([]models.ComponentTask, error) {
	var tasks []models.ComponentTask
	err := r.db.WithContext(ctx).
		Where("org_id = ? AND project_id = ?", orgID, projectID).
		Order(`"order" ASC, component_name ASC`).
		Find(&tasks).Error
	if err != nil {
		return nil, err
	}
	return tasks, nil
}

func (r *taskRepository) ListNonTerminalByOrgID(ctx context.Context, orgID string) ([]models.ComponentTask, error) {
	var tasks []models.ComponentTask
	terminal := []string{
		string(models.TaskStatusDeployed),
		string(models.TaskStatusRejected),
		string(models.TaskStatusFailed),
		string(models.TaskStatusAbandoned),
	}
	err := r.db.WithContext(ctx).
		Where("org_id = ? AND status NOT IN ?", orgID, terminal).
		Find(&tasks).Error
	if err != nil {
		return nil, err
	}
	return tasks, nil
}

func (r *taskRepository) ListByOrgID(ctx context.Context, orgID string, f ListByOrgFilter) ([]models.ComponentTask, error) {
	q := r.db.WithContext(ctx).Where("org_id = ?", orgID)
	if f.Status != "" {
		q = q.Where("status = ?", f.Status)
	}
	if f.Cause != "" {
		q = q.Where("cause = ?", f.Cause)
	}
	if f.Since != nil {
		q = q.Where("last_event_at >= ?", *f.Since)
	}
	var tasks []models.ComponentTask
	if err := q.Order("last_event_at DESC").Find(&tasks).Error; err != nil {
		return nil, err
	}
	return tasks, nil
}

func (r *taskRepository) Create(ctx context.Context, task *models.ComponentTask) error {
	return r.db.WithContext(ctx).Create(task).Error
}

func (r *taskRepository) Update(ctx context.Context, task *models.ComponentTask) error {
	return r.db.WithContext(ctx).Save(task).Error
}

func (r *taskRepository) DeleteByProjectID(ctx context.Context, orgID, projectID string) error {
	return r.db.WithContext(ctx).
		Where("org_id = ? AND project_id = ?", orgID, projectID).
		Delete(&models.ComponentTask{}).Error
}

func (r *taskRepository) DeleteAll(ctx context.Context) error {
	return r.db.WithContext(ctx).Where("1=1").Delete(&models.ComponentTask{}).Error
}
