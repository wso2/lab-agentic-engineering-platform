package repositories

import (
	"context"
	"errors"

	"gorm.io/gorm"

	"github.com/wso2/asdlc/asdlc-service/models"
)

type ConfigRepository interface {
	GetByComponent(ctx context.Context, orgID, projectName, componentName string) (*models.ComponentConfig, error)
	Upsert(ctx context.Context, config *models.ComponentConfig) error
	DeleteAll(ctx context.Context) error
}

type configRepository struct {
	db *gorm.DB
}

func NewConfigRepository(db *gorm.DB) ConfigRepository {
	return &configRepository{db: db}
}

func (r *configRepository) GetByComponent(ctx context.Context, orgID, projectName, componentName string) (*models.ComponentConfig, error) {
	var config models.ComponentConfig
	err := r.db.WithContext(ctx).
		Where("org_id = ? AND project_name = ? AND component_name = ?", orgID, projectName, componentName).
		First(&config).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &config, nil
}

func (r *configRepository) Upsert(ctx context.Context, config *models.ComponentConfig) error {
	existing, err := r.GetByComponent(ctx, config.OrgID, config.ProjectName, config.ComponentName)
	if err != nil {
		return err
	}
	if existing != nil {
		config.ID = existing.ID
		return r.db.WithContext(ctx).Save(config).Error
	}
	return r.db.WithContext(ctx).Create(config).Error
}

func (r *configRepository) DeleteAll(ctx context.Context) error {
	return r.db.WithContext(ctx).Where("1=1").Delete(&models.ComponentConfig{}).Error
}
