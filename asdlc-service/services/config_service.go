package services

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	"github.com/wso2/asdlc/asdlc-service/models"
	"github.com/wso2/asdlc/asdlc-service/repositories"
)

type ConfigService interface {
	GetConfig(ctx context.Context, orgID, projectName, componentName string) (*models.ComponentConfig, error)
	UpdateConfig(ctx context.Context, orgID, projectName, componentName string, envVars models.EnvVarSlice) (*models.ComponentConfig, error)
	GetEnvVarsForDeploy(ctx context.Context, orgID, projectName, componentName string) (models.EnvVarSlice, error)
}

type configService struct {
	repo         repositories.ConfigRepository
	componentSvc ComponentService
}

// NewConfigService wires the config repo and (optionally) a ComponentService
// for mirroring env-var updates onto the OC Component's workflow params so
// the next build picks them up. Pass nil for componentSvc to disable that
// mirror (the env vars still land in the DB; they just won't reach OC).
func NewConfigService(repo repositories.ConfigRepository, componentSvc ComponentService) ConfigService {
	return &configService{repo: repo, componentSvc: componentSvc}
}

func (s *configService) GetConfig(ctx context.Context, orgID, projectName, componentName string) (*models.ComponentConfig, error) {
	config, err := s.repo.GetByComponent(ctx, orgID, projectName, componentName)
	if err != nil {
		return nil, fmt.Errorf("get config: %w", err)
	}
	return config, nil
}

func (s *configService) UpdateConfig(ctx context.Context, orgID, projectName, componentName string, envVars models.EnvVarSlice) (*models.ComponentConfig, error) {
	// Validate env vars
	seen := make(map[string]bool, len(envVars))
	for _, ev := range envVars {
		key := strings.TrimSpace(ev.Key)
		if key == "" {
			return nil, fmt.Errorf("environment variable key cannot be empty")
		}
		if seen[key] {
			return nil, fmt.Errorf("duplicate environment variable key: %s", key)
		}
		seen[key] = true
	}

	config := &models.ComponentConfig{
		OrgID:         orgID,
		ProjectName:   projectName,
		ComponentName: componentName,
		EnvVars:       envVars,
	}

	if err := s.repo.Upsert(ctx, config); err != nil {
		return nil, fmt.Errorf("update config: %w", err)
	}

	slog.InfoContext(ctx, "updated component config",
		"org", orgID, "project", projectName, "component", componentName, "envVarCount", len(envVars))

	// Mirror onto the OC Component's workflow params so the next build
	// (push-triggered or manual) ships these env vars through
	// generate-workload-cr into the Workload CR. Best-effort — the
	// canonical record is the DB; OC mirror is recoverable next time
	// ensureOCComponent runs.
	if s.componentSvc != nil {
		wfEnvVars := make([]models.WorkflowEnvVarRef, 0, len(envVars))
		for _, ev := range envVars {
			wfEnvVars = append(wfEnvVars, models.WorkflowEnvVarRef{Key: ev.Key, Value: ev.Value})
		}
		if err := s.componentSvc.UpdateWorkflowEnvVars(ctx, orgID, projectName, componentName, wfEnvVars); err != nil {
			slog.WarnContext(ctx, "mirror env vars onto OC Component failed; DB is updated, next build may still see stale vars",
				"org", orgID, "project", projectName, "component", componentName, "error", err)
		}
	}

	return config, nil
}

func (s *configService) GetEnvVarsForDeploy(ctx context.Context, orgID, projectName, componentName string) (models.EnvVarSlice, error) {
	config, err := s.repo.GetByComponent(ctx, orgID, projectName, componentName)
	if err != nil {
		return nil, fmt.Errorf("get config for deploy: %w", err)
	}
	if config == nil || len(config.EnvVars) == 0 {
		return nil, nil
	}
	return config.EnvVars, nil
}
