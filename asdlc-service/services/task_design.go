package services

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/wso2/asdlc/asdlc-service/models"
)

// ErrComponentRemovedAfterGeneration is returned when a task references a
// component that no longer exists in the project's design.json. See
// docs/design/tech-lead-agent.md §10.4 — reconciliation auto-closes pending
// tasks for removed components on every design save, so this case should be
// rare. When it does happen, the dispatch / issue-body builder fails fast
// rather than rendering placeholders.
var ErrComponentRemovedAfterGeneration = errors.New("component removed after generation")

// resolveDesignComponent reads the project's current .asdlc/design.json and
// returns the entry whose Name matches task.ComponentName. Per design §12,
// dispatch reads the *current* design at dispatch time — not a snapshot from
// when the task was generated — so design edits between generation and
// dispatch propagate.
//
// Lookups are case-insensitive on Name to mirror toposort/lookup behaviour
// elsewhere in the codebase.
func (s *taskService) resolveDesignComponent(ctx context.Context, task *models.ComponentTask) (*models.DesignComponent, error) {
	return resolveDesignComponentVia(ctx, s.store, task)
}

func resolveDesignComponentVia(ctx context.Context, store *ArtifactStore, task *models.ComponentTask) (*models.DesignComponent, error) {
	if store == nil {
		return nil, fmt.Errorf("artifact store not configured")
	}
	design, err := store.ReadDesign(ctx, task.OrgID, task.ProjectID)
	if err != nil {
		return nil, fmt.Errorf("read design for task %s: %w", task.ID, err)
	}
	if design == nil {
		return nil, fmt.Errorf("design.json missing for project %s", task.ProjectID)
	}
	target := strings.ToLower(task.ComponentName)
	for i := range design.Components {
		if strings.EqualFold(design.Components[i].Name, target) {
			c := design.Components[i]
			return &c, nil
		}
	}
	return nil, fmt.Errorf("%w: %s", ErrComponentRemovedAfterGeneration, task.ComponentName)
}
