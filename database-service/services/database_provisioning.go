package services

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"math/rand"
	"strings"
	"time"

	"github.com/go-sql-driver/mysql"
)

// DatabaseCredentials holds the connection details for a provisioned database.
type DatabaseCredentials struct {
	Host     string `json:"host"`
	Port     int    `json:"port"`
	Database string `json:"database"`
	Username string `json:"username"`
	Password string `json:"password"`
}

// DatabaseProvisioningService handles database provisioning and management.
type DatabaseProvisioningService interface {
	ProvisionDatabase(ctx context.Context, projectName string) (*DatabaseCredentials, error)
	TestConnection(ctx context.Context, creds *DatabaseCredentials) error
	// PingRoot opens a connection to the root MySQL DSN and pings it.
	// Used for health checks — does not create any databases or users.
	PingRoot(ctx context.Context) error
}

type databaseProvisioningService struct {
	mysqlRootURL string
	mysqlHost    string
	mysqlPort    int
}

// NewDatabaseProvisioningService creates a new database provisioning service.
// mysqlRootURL should be in format: root:password@tcp(host:port)/
// Example: root:root@tcp(localhost:3306)/
func NewDatabaseProvisioningService(mysqlRootURL, mysqlHost string, mysqlPort int) DatabaseProvisioningService {
	return &databaseProvisioningService{
		mysqlRootURL: mysqlRootURL,
		mysqlHost:    mysqlHost,
		mysqlPort:    mysqlPort,
	}
}

// ProvisionDatabase creates a new database and user with random credentials.
func (s *databaseProvisioningService) ProvisionDatabase(ctx context.Context, projectName string) (*DatabaseCredentials, error) {
	// Sanitize project name for use in MySQL identifiers
	dbName := strings.ToLower(strings.ReplaceAll(projectName, "-", "_"))
	if len(dbName) > 30 {
		dbName = dbName[:30]
	}

	// Generate random username and password
	username := fmt.Sprintf("user_%s_%s", dbName, randString(6))
	password := generateSecurePassword()

	// Connect as root to create database and user
	rootConn, err := sql.Open("mysql", s.mysqlRootURL)
	if err != nil {
		return nil, fmt.Errorf("connect to mysql root: %w", err)
	}
	defer rootConn.Close()

	// Test connection
	if err := rootConn.PingContext(ctx); err != nil {
		return nil, fmt.Errorf("ping root connection: %w", err)
	}

	// Create database
	createDBSQL := fmt.Sprintf("CREATE DATABASE IF NOT EXISTS `%s`", dbName)
	if _, err := rootConn.ExecContext(ctx, createDBSQL); err != nil {
		return nil, fmt.Errorf("create database: %w", err)
	}

	// Create user with privileges
	createUserSQL := fmt.Sprintf("CREATE USER IF NOT EXISTS '%s'@'%%' IDENTIFIED BY '%s'", username, password)
	if _, err := rootConn.ExecContext(ctx, createUserSQL); err != nil {
		return nil, fmt.Errorf("create user: %w", err)
	}

	// Grant privileges
	grantSQL := fmt.Sprintf("GRANT ALL PRIVILEGES ON `%s`.* TO '%s'@'%%'", dbName, username)
	if _, err := rootConn.ExecContext(ctx, grantSQL); err != nil {
		return nil, fmt.Errorf("grant privileges: %w", err)
	}

	// Flush privileges
	if _, err := rootConn.ExecContext(ctx, "FLUSH PRIVILEGES"); err != nil {
		return nil, fmt.Errorf("flush privileges: %w", err)
	}

	slog.InfoContext(ctx, "database provisioned successfully",
		"database", dbName,
		"username", username,
		"project", projectName)

	return &DatabaseCredentials{
		Host:     s.mysqlHost,
		Port:     s.mysqlPort,
		Database: dbName,
		Username: username,
		Password: password,
	}, nil
}

// PingRoot opens a connection to the root MySQL DSN and pings it without
// creating any databases or users. Used by HealthCheck.
func (s *databaseProvisioningService) PingRoot(ctx context.Context) error {
	conn, err := sql.Open("mysql", s.mysqlRootURL)
	if err != nil {
		return fmt.Errorf("open root connection: %w", err)
	}
	defer conn.Close()
	if err := conn.PingContext(ctx); err != nil {
		return fmt.Errorf("ping root connection: %w", err)
	}
	return nil
}

// TestConnection verifies that the provided credentials can connect to the database.
func (s *databaseProvisioningService) TestConnection(ctx context.Context, creds *DatabaseCredentials) error {
	cfg := mysql.Config{
		User:      creds.Username,
		Passwd:    creds.Password,
		Net:       "tcp",
		Addr:      fmt.Sprintf("%s:%d", creds.Host, creds.Port),
		DBName:    creds.Database,
		ParseTime: true,
		Loc:       time.Local,
	}
	conn, err := sql.Open("mysql", cfg.FormatDSN())
	if err != nil {
		return fmt.Errorf("open connection: %w", err)
	}
	defer conn.Close()

	if err := conn.PingContext(ctx); err != nil {
		return fmt.Errorf("ping database: %w", err)
	}

	// Test basic query
	var result string
	if err := conn.QueryRowContext(ctx, "SELECT DATABASE()").Scan(&result); err != nil {
		return fmt.Errorf("query database: %w", err)
	}

	slog.InfoContext(ctx, "database connection test successful",
		"database", creds.Database,
		"username", creds.Username)

	return nil
}

// generateSecurePassword creates a random 16-character password with mixed character types.
func generateSecurePassword() string {
	const (
		lowercase = "abcdefghijklmnopqrstuvwxyz"
		uppercase = "ABCDEFGHIJKLMNOPQRSTUVWXYZ"
		digits    = "0123456789"
		special   = "!@#$%^&*()_+-=[]{}|;:,.<>?"
	)

	chars := lowercase + uppercase + digits + special
	password := make([]byte, 16)
	for i := range password {
		password[i] = chars[rand.Intn(len(chars))]
	}

	return string(password)
}

// randString generates a random string of specified length.
func randString(length int) string {
	const charset = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"
	result := make([]byte, length)
	for i := range result {
		result[i] = charset[rand.Intn(len(charset))]
	}
	return string(result)
}

func init() {
	rand.Seed(time.Now().UnixNano())
}
