package services

import (
	"context"
	"fmt"
	"log/slog"
)

// HealthStatus reports liveness of each backing database engine.
type HealthStatus struct {
	MySQL   EngineHealth `json:"mysql"`
	MongoDB EngineHealth `json:"mongodb"`
}

// EngineHealth is a per-engine ok/message pair.
type EngineHealth struct {
	OK      bool   `json:"ok"`
	Message string `json:"message,omitempty"`
}

// CreateDatabaseRequest is the input to CreateDatabase.
type CreateDatabaseRequest struct {
	ReferenceID   string
	OrgID         string
	ProjectID     string
	// DBType and ComponentName are used to auto-register a pending record when
	// the BFF's pre-registration step was skipped or failed.
	DBType        string
	ComponentName string
}

// DatabaseService combines the registry and provisioning services into a single
// interface for MCP tool calls. The agent calls tools here without needing to
// know database credentials — the service uses its own internal connections.
type DatabaseService interface {
	HealthCheck(ctx context.Context) HealthStatus
	// GetPendingDatabase returns the pre-registered record for a task.
	// Returns (nil, nil) when no record exists for referenceID.
	GetPendingDatabase(ctx context.Context, referenceID string) (*DatabaseRecord, error)
	// CreateDatabase provisions the pre-registered database for referenceID,
	// stores credentials in the registry, and returns the updated record.
	CreateDatabase(ctx context.Context, req CreateDatabaseRequest) (*DatabaseRecord, error)
	// TestDatabase tests the connection for the database identified by
	// referenceID. Returns "healthy" or "faulty" and updates the registry.
	TestDatabase(ctx context.Context, referenceID string) (string, error)
	// LookupDatabase returns the provisioned database record for a component
	// within a project. Returns (nil, nil) when none is found.
	LookupDatabase(ctx context.Context, orgID, projectID, component string) (*DatabaseRecord, error)
}

type databaseService struct {
	registry    DatabaseRegistryService
	provisioner DatabaseProvisioningService
	mysqlHost   string
	mysqlPort   int
}

// NewDatabaseService creates a DatabaseService that wraps the registry and
// provisioning services. mysqlHost/Port are used for the health check.
func NewDatabaseService(registry DatabaseRegistryService, provisioner DatabaseProvisioningService, mysqlHost string, mysqlPort int) DatabaseService {
	return &databaseService{
		registry:    registry,
		provisioner: provisioner,
		mysqlHost:   mysqlHost,
		mysqlPort:   mysqlPort,
	}
}

func (s *databaseService) HealthCheck(ctx context.Context) HealthStatus {
	status := HealthStatus{}
	if err := s.provisioner.PingRoot(ctx); err != nil {
		status.MySQL = EngineHealth{OK: false, Message: err.Error()}
	} else {
		status.MySQL = EngineHealth{OK: true}
	}
	// MongoDB not configured in this deployment — report as not applicable.
	status.MongoDB = EngineHealth{OK: false, Message: "not configured"}
	return status
}

func (s *databaseService) GetPendingDatabase(ctx context.Context, referenceID string) (*DatabaseRecord, error) {
	return s.registry.GetByReferenceID(ctx, referenceID)
}

func (s *databaseService) CreateDatabase(ctx context.Context, req CreateDatabaseRequest) (*DatabaseRecord, error) {
	record, err := s.registry.GetByReferenceID(ctx, req.ReferenceID)
	if err != nil {
		return nil, fmt.Errorf("get pending database: %w", err)
	}
	if record == nil {
		// No pre-registered record. Auto-register if the caller supplied enough info.
		if req.DBType == "" {
			return nil, fmt.Errorf("no pending database found for reference_id=%s; supply db_type to auto-register", req.ReferenceID)
		}
		name := req.ComponentName
		if name == "" {
			name = req.ReferenceID
		}
		slog.InfoContext(ctx, "auto-registering database record", "referenceID", req.ReferenceID, "dbType", req.DBType, "name", name)
		if err := s.registry.Register(ctx, req.OrgID, req.ProjectID, req.ReferenceID, name, req.DBType, name); err != nil {
			return nil, fmt.Errorf("auto-register database: %w", err)
		}
		record, err = s.registry.GetByReferenceID(ctx, req.ReferenceID)
		if err != nil || record == nil {
			return nil, fmt.Errorf("fetch after auto-register: %w", err)
		}
	}
	if record.Status != "pending" && record.Status != "provisioning" {
		// Already provisioned — return existing record.
		return record, nil
	}

	name := record.RequestedName
	if name == "" {
		name = req.ReferenceID
	}

	creds, err := s.provisioner.ProvisionDatabase(ctx, name)
	if err != nil {
		slog.ErrorContext(ctx, "provision database failed", "referenceID", req.ReferenceID, "error", err)
		_ = s.registry.UpdateStatus(ctx, req.ReferenceID, "faulty")
		return nil, fmt.Errorf("provision database: %w", err)
	}

	if err := s.registry.UpdateCredentials(ctx, req.ReferenceID,
		creds.Database, creds.Host, creds.Port, creds.Username, creds.Password, "provisioning",
	); err != nil {
		return nil, fmt.Errorf("store database credentials: %w", err)
	}

	// Re-fetch the updated record so the caller gets the full picture.
	updated, err := s.registry.GetByReferenceID(ctx, req.ReferenceID)
	if err != nil {
		return nil, fmt.Errorf("re-fetch after provision: %w", err)
	}
	return updated, nil
}

func (s *databaseService) TestDatabase(ctx context.Context, referenceID string) (string, error) {
	record, err := s.registry.GetByReferenceID(ctx, referenceID)
	if err != nil {
		return "", fmt.Errorf("get database record: %w", err)
	}
	if record == nil {
		return "", fmt.Errorf("no database found for reference_id=%s", referenceID)
	}
	if record.Host == "" {
		return "", fmt.Errorf("database not provisioned yet for reference_id=%s", referenceID)
	}

	creds := &DatabaseCredentials{
		Host:     record.Host,
		Port:     record.Port,
		Database: record.DBName,
		Username: record.Username,
		Password: record.Password,
	}

	status := "healthy"
	if err := s.provisioner.TestConnection(ctx, creds); err != nil {
		slog.WarnContext(ctx, "database connection test failed", "referenceID", referenceID, "error", err)
		status = "faulty"
	}

	if err := s.registry.UpdateStatus(ctx, referenceID, status); err != nil {
		slog.WarnContext(ctx, "failed to update database status after test", "referenceID", referenceID, "error", err)
	}
	return status, nil
}

func (s *databaseService) LookupDatabase(ctx context.Context, orgID, projectID, component string) (*DatabaseRecord, error) {
	records, err := s.registry.ListByProject(ctx, orgID, projectID)
	if err != nil {
		return nil, fmt.Errorf("list databases: %w", err)
	}
	for _, rec := range records {
		for _, c := range rec.Components {
			if c == component {
				return rec, nil
			}
		}
		// Also match on RequestedName as a fallback.
		if rec.RequestedName == component {
			return rec, nil
		}
	}
	return nil, nil
}
