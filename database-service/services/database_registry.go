package services

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"

	_ "github.com/lib/pq"
)

// DatabaseRecord is a persisted record of a database provisioning request.
type DatabaseRecord struct {
	ID            string   `json:"id"`
	ReferenceID   string   `json:"referenceId"`
	OrgID         string   `json:"orgId"`
	ProjectID     string   `json:"projectId"`
	Components    []string `json:"components"`
	DBType        string   `json:"dbType"`
	RequestedName string   `json:"requestedName"`
	DBName        string   `json:"actualDbName,omitempty"`
	Host          string   `json:"host,omitempty"`
	Port          int      `json:"port,omitempty"`
	Username      string   `json:"username,omitempty"`
	Password      string   `json:"password,omitempty"`
	Status        string   `json:"status"`
}

// DatabaseRegistryService manages database provisioning records.
type DatabaseRegistryService interface {
	Register(ctx context.Context, orgID, projectID, referenceID, componentName, dbType, requestedName string) error
	GetByReferenceID(ctx context.Context, referenceID string) (*DatabaseRecord, error)
	ListByProject(ctx context.Context, orgID, projectID string) ([]*DatabaseRecord, error)
	UpdateStatus(ctx context.Context, referenceID, status string) error
	UpdateCredentials(ctx context.Context, referenceID, dbName, host string, port int, username, password, status string) error
	Migrate(ctx context.Context) error
}

type databaseRegistryService struct {
	db *sql.DB
}

// NewDatabaseRegistryService creates a registry service backed by PostgreSQL.
func NewDatabaseRegistryService(db *sql.DB) DatabaseRegistryService {
	return &databaseRegistryService{db: db}
}

// OpenPostgres opens a PostgreSQL connection pool and pings it.
func OpenPostgres(databaseURL string) (*sql.DB, error) {
	db, err := sql.Open("postgres", databaseURL)
	if err != nil {
		return nil, fmt.Errorf("open postgres: %w", err)
	}
	if err := db.Ping(); err != nil {
		db.Close()
		return nil, fmt.Errorf("ping postgres: %w", err)
	}
	return db, nil
}

func (s *databaseRegistryService) Migrate(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, `
		CREATE TABLE IF NOT EXISTS databases (
			id            TEXT PRIMARY KEY,
			reference_id  TEXT NOT NULL UNIQUE,
			org_id        TEXT NOT NULL,
			project_id    TEXT NOT NULL,
			components    TEXT NOT NULL DEFAULT '[]',
			db_type       TEXT NOT NULL DEFAULT '',
			requested_name TEXT NOT NULL DEFAULT '',
			actual_db_name TEXT NOT NULL DEFAULT '',
			host          TEXT NOT NULL DEFAULT '',
			port          INTEGER NOT NULL DEFAULT 0,
			username      TEXT NOT NULL DEFAULT '',
			password      TEXT NOT NULL DEFAULT '',
			status        TEXT NOT NULL DEFAULT 'pending',
			created_at    TIMESTAMPTZ NOT NULL DEFAULT NOW()
		)
	`)
	if err != nil {
		return fmt.Errorf("migrate databases table: %w", err)
	}
	slog.InfoContext(ctx, "databases table ensured")
	return nil
}

func (s *databaseRegistryService) Register(ctx context.Context, orgID, projectID, referenceID, componentName, dbType, requestedName string) error {
	components, err := json.Marshal([]string{componentName})
	if err != nil {
		return fmt.Errorf("marshal components: %w", err)
	}
	id := newUUID()
	_, err = s.db.ExecContext(ctx, `
		INSERT INTO databases (id, reference_id, org_id, project_id, components, db_type, requested_name, status)
		VALUES ($1, $2, $3, $4, $5, $6, $7, 'pending')
		ON CONFLICT (reference_id) DO NOTHING
	`, id, referenceID, orgID, projectID, string(components), dbType, requestedName)
	if err != nil {
		return fmt.Errorf("insert database record: %w", err)
	}
	return nil
}

func (s *databaseRegistryService) ListByProject(ctx context.Context, orgID, projectID string) ([]*DatabaseRecord, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, reference_id, org_id, project_id, components, db_type, requested_name,
		       actual_db_name, host, port, username, password, status
		FROM databases
		WHERE org_id = $1 AND project_id = $2
		ORDER BY created_at ASC
	`, orgID, projectID)
	if err != nil {
		return nil, fmt.Errorf("query databases: %w", err)
	}
	defer rows.Close()

	var records []*DatabaseRecord
	for rows.Next() {
		var rec DatabaseRecord
		var componentsJSON string
		if err := rows.Scan(
			&rec.ID, &rec.ReferenceID, &rec.OrgID, &rec.ProjectID,
			&componentsJSON, &rec.DBType, &rec.RequestedName,
			&rec.DBName, &rec.Host, &rec.Port, &rec.Username, &rec.Password,
			&rec.Status,
		); err != nil {
			return nil, fmt.Errorf("scan database record: %w", err)
		}
		if err := json.Unmarshal([]byte(componentsJSON), &rec.Components); err != nil {
			rec.Components = []string{}
		}
		records = append(records, &rec)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate database records: %w", err)
	}
	return records, nil
}

func (s *databaseRegistryService) GetByReferenceID(ctx context.Context, referenceID string) (*DatabaseRecord, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT id, reference_id, org_id, project_id, components, db_type, requested_name,
		       actual_db_name, host, port, username, password, status
		FROM databases WHERE reference_id = $1
	`, referenceID)
	var rec DatabaseRecord
	var componentsJSON string
	if err := row.Scan(
		&rec.ID, &rec.ReferenceID, &rec.OrgID, &rec.ProjectID,
		&componentsJSON, &rec.DBType, &rec.RequestedName,
		&rec.DBName, &rec.Host, &rec.Port, &rec.Username, &rec.Password,
		&rec.Status,
	); err != nil {
		if err.Error() == "sql: no rows in result set" {
			return nil, nil
		}
		return nil, fmt.Errorf("scan database record: %w", err)
	}
	if err := json.Unmarshal([]byte(componentsJSON), &rec.Components); err != nil {
		rec.Components = []string{}
	}
	return &rec, nil
}

func (s *databaseRegistryService) UpdateCredentials(ctx context.Context, referenceID, dbName, host string, port int, username, password, status string) error {
	result, err := s.db.ExecContext(ctx,
		`UPDATE databases SET actual_db_name=$1, host=$2, port=$3, username=$4, password=$5, status=$6
		 WHERE reference_id=$7`,
		dbName, host, port, username, password, status, referenceID,
	)
	if err != nil {
		return fmt.Errorf("update database credentials: %w", err)
	}
	n, _ := result.RowsAffected()
	if n == 0 {
		return fmt.Errorf("database record not found: %s", referenceID)
	}
	return nil
}

func (s *databaseRegistryService) UpdateStatus(ctx context.Context, referenceID, status string) error {
	result, err := s.db.ExecContext(ctx,
		`UPDATE databases SET status = $1 WHERE reference_id = $2`,
		status, referenceID,
	)
	if err != nil {
		return fmt.Errorf("update database status: %w", err)
	}
	n, _ := result.RowsAffected()
	if n == 0 {
		return fmt.Errorf("database record not found: %s", referenceID)
	}
	return nil
}

// newUUID generates a random UUID v4.
func newUUID() string {
	b := make([]byte, 16)
	rand.Read(b) //nolint:errcheck
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}
