package services

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"

	"gorm.io/gorm"

	"github.com/wso2/asdlc/asdlc-service/models"
)

// TaskSkillEntry is one row in the runner's pull response. Mirrors
// `SkillResolution` in docs/design/skills-system.md > "Coding agent".
type TaskSkillEntry struct {
	ID               string            `json:"id"`               // e.g. "builtin/api-management"
	MaterializedName string            `json:"materializedName"` // e.g. "builtin-api-management"
	Kind             string            `json:"kind"`             // builtin | custom | imported
	SkillMD          string            `json:"skillMd"`
	References       map[string]string `json:"references"`
}

// TaskSkillsResponse is the pull endpoint's wire body.
type TaskSkillsResponse struct {
	Skills []TaskSkillEntry `json:"skills"`
}

// TaskSkillsService backs the GET /api/v1/tasks/:taskId/skills endpoint.
// It reads from design_version_skill_snapshots, NEVER from the live
// `skills` table — the snapshot is the contract the agent's workspace
// must match.
type TaskSkillsService struct {
	db       *gorm.DB
	taskRepo interface {
		GetByID(ctx context.Context, id string) (*models.ComponentTask, error)
	}
}

func NewTaskSkillsService(db *gorm.DB, taskRepo interface {
	GetByID(ctx context.Context, id string) (*models.ComponentTask, error)
}) *TaskSkillsService {
	return &TaskSkillsService{db: db, taskRepo: taskRepo}
}

// SkillsForTask resolves the task → reads the snapshot rows → returns
// the wire response. Returns ErrTaskNotFound if the task doesn't exist,
// or an empty Skills list (NOT an error) when the snapshot is empty
// (pre-PR-1 backfilled task, or design with no attached skills).
func (s *TaskSkillsService) SkillsForTask(ctx context.Context, taskID string) (*TaskSkillsResponse, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("task skills service: not configured")
	}
	task, err := s.taskRepo.GetByID(ctx, taskID)
	if err != nil {
		return nil, fmt.Errorf("get task: %w", err)
	}
	if task == nil {
		return nil, ErrTaskNotFound
	}
	if task.SourceDesignVersion == "" {
		return &TaskSkillsResponse{Skills: []TaskSkillEntry{}}, nil
	}

	rows, err := readSnapshotRows(ctx, s.db, task.ProjectID, task.SourceDesignVersion)
	if err != nil {
		return nil, fmt.Errorf("read snapshot: %w", err)
	}
	if len(rows) == 0 {
		return &TaskSkillsResponse{Skills: []TaskSkillEntry{}}, nil
	}

	out := make([]TaskSkillEntry, 0, len(rows))
	for _, r := range rows {
		refs := map[string]string{}
		if len(r.ReferencesJSON) > 0 {
			_ = json.Unmarshal([]byte(r.ReferencesJSON), &refs)
		}
		out = append(out, TaskSkillEntry{
			ID:               r.SkillID,
			MaterializedName: r.MaterializedName,
			Kind:             r.Kind,
			SkillMD:          r.SkillMD,
			References:       refs,
		})
	}
	return &TaskSkillsResponse{Skills: out}, nil
}

// snapshotRow is a private row shape just for the read path. Stored
// here instead of in skill_service.go because the snapshot is conceptually
// per-design-version-skill, not per-skill-row.
type snapshotRow struct {
	ProjectID        string `gorm:"column:project_id"`
	DesignVersion    string `gorm:"column:design_version"`
	SkillID          string `gorm:"column:skill_id"`
	MaterializedName string `gorm:"column:materialized_name"`
	Kind             string `gorm:"column:kind"`
	SkillMD          string `gorm:"column:skill_md"`
	ReferencesJSON   string `gorm:"column:references"`
}

func readSnapshotRows(ctx context.Context, db *gorm.DB, projectID, designVersion string) ([]snapshotRow, error) {
	var rows []snapshotRow
	err := db.WithContext(ctx).Raw(
		`SELECT project_id, design_version, skill_id, materialized_name, kind, skill_md, "references"::text as "references"
		 FROM design_version_skill_snapshots
		 WHERE project_id = ? AND design_version = ?
		 ORDER BY kind, skill_id`,
		projectID, designVersion,
	).Scan(&rows).Error
	if err != nil {
		return nil, err
	}
	return rows, nil
}

// snapshotProjectSkills writes one snapshot row per skill currently
// attached to the project's design.md, scoped to (projectID, designVersion).
// Idempotent on the PK — duplicate inserts no-op. Called from
// task_service.ensureIssueForTask just before CreateIssue. Safe to call
// concurrently for the same key (PostgreSQL handles the conflict).
func snapshotProjectSkills(
	ctx context.Context,
	db *gorm.DB,
	store *ArtifactStore,
	skillSvc *SkillService,
	orgID, projectID, designVersion string,
) error {
	if store == nil || skillSvc == nil {
		return nil
	}
	// Read the design's currently-attached skill names. The design.md
	// we read here might be the live working tree (preferred — it
	// matches what the tech-lead just used) or the tagged version. The
	// snapshot is conceptually frozen at issue-creation moment.
	design, err := store.ReadDesign(ctx, orgID, projectID)
	if err != nil {
		return fmt.Errorf("read design: %w", err)
	}
	if design == nil {
		return nil // no design at this version yet — nothing to snapshot
	}
	if len(design.SkillsApplied) == 0 {
		return nil // nothing to snapshot
	}

	// Skip if any row already exists for this key — snapshots are
	// per-design-version, write-once.
	var existing int64
	if err := db.WithContext(ctx).
		Raw(`SELECT COUNT(*) FROM design_version_skill_snapshots WHERE project_id = ? AND design_version = ?`,
			projectID, designVersion).
		Scan(&existing).Error; err != nil {
		return fmt.Errorf("check existing snapshot: %w", err)
	}
	if existing > 0 {
		return nil
	}

	// Resolve each attached skill against the live catalogue (built-in
	// in v1; PR 2 adds custom/imported). Missing skills are logged but
	// don't fail the snapshot — they just don't get materialised.
	resolved, err := skillSvc.ResolveMany(ctx, orgID, design.SkillsApplied)
	if err != nil {
		return fmt.Errorf("resolve skills: %w", err)
	}
	if len(resolved) == 0 {
		return nil
	}

	// INSERT … ON CONFLICT DO NOTHING per row — concurrent dispatches
	// for the same key race-safe.
	for _, sk := range resolved {
		refsJSON, _ := json.Marshal(sk.References)
		err := db.WithContext(ctx).Exec(
			`INSERT INTO design_version_skill_snapshots
			   (project_id, design_version, skill_id, materialized_name, kind, skill_md, "references", created_at)
			 VALUES (?, ?, ?, ?, ?, ?, ?::jsonb, NOW())
			 ON CONFLICT (project_id, design_version, skill_id) DO NOTHING`,
			projectID, designVersion,
			PrefixedID(sk.Kind, sk.Name),
			MaterializedName(sk.Kind, sk.Name),
			sk.Kind, sk.SkillMD, string(refsJSON),
		).Error
		if err != nil {
			return fmt.Errorf("insert snapshot row %s: %w", sk.Name, err)
		}
	}
	slog.InfoContext(ctx, "snapshotted project skills",
		"projectID", projectID, "designVersion", designVersion, "count", len(resolved))
	return nil
}

// Re-export the gorm.ErrRecordNotFound sentinel as a convenience so
// callers don't need to import gorm just to detect "task missing".
var ErrSnapshotMissing = errors.New("design_version_skill_snapshots: no rows")
