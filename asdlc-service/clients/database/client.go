package database

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// Client calls the database-service.
type Client interface {
	ProvisionDatabase(ctx context.Context, projectName string) (*DatabaseCredentials, error)
	TestConnection(ctx context.Context, creds *DatabaseCredentials) error
}

// DatabaseCredentials contains the database connection details.
type DatabaseCredentials struct {
	Host     string `json:"host"`
	Port     int    `json:"port"`
	Database string `json:"database"`
	Username string `json:"username"`
	Password string `json:"password"`
}

// ProvisionDatabaseRequest is sent to the database-service to provision a new database.
type ProvisionDatabaseRequest struct {
	ProjectName string `json:"projectName"`
}

// ProvisionDatabaseResponse is the response from provisioning a database.
type ProvisionDatabaseResponse struct {
	Host     string `json:"host"`
	Port     int    `json:"port"`
	Database string `json:"database"`
	Username string `json:"username"`
	Password string `json:"password"`
}

// TestConnectionRequest is sent to test a database connection.
type TestConnectionRequest struct {
	Host     string `json:"host"`
	Port     int    `json:"port"`
	Database string `json:"database"`
	Username string `json:"username"`
	Password string `json:"password"`
}

// TestConnectionResponse is the response from testing a connection.
type TestConnectionResponse struct {
	Status  string `json:"status"`
	Message string `json:"message"`
}

type client struct {
	baseURL    string
	httpClient *http.Client
}

func NewClient(baseURL string) Client {
	return &client{
		baseURL: baseURL,
		httpClient: &http.Client{
			Timeout: 30 * time.Second,
		},
	}
}

func (c *client) ProvisionDatabase(ctx context.Context, projectName string) (*DatabaseCredentials, error) {
	req := ProvisionDatabaseRequest{
		ProjectName: projectName,
	}

	body, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, "POST", c.baseURL+"/api/v1/databases/provision", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}

	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("provision database request failed: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read response body: %w", err)
	}

	if resp.StatusCode != http.StatusCreated {
		return nil, fmt.Errorf("provision database failed with status %d: %s", resp.StatusCode, string(respBody))
	}

	var provResp ProvisionDatabaseResponse
	if err := json.Unmarshal(respBody, &provResp); err != nil {
		return nil, fmt.Errorf("unmarshal response: %w", err)
	}

	return &DatabaseCredentials{
		Host:     provResp.Host,
		Port:     provResp.Port,
		Database: provResp.Database,
		Username: provResp.Username,
		Password: provResp.Password,
	}, nil
}

func (c *client) TestConnection(ctx context.Context, creds *DatabaseCredentials) error {
	req := TestConnectionRequest{
		Host:     creds.Host,
		Port:     creds.Port,
		Database: creds.Database,
		Username: creds.Username,
		Password: creds.Password,
	}

	body, err := json.Marshal(req)
	if err != nil {
		return fmt.Errorf("marshal request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, "POST", c.baseURL+"/api/v1/databases/test-connection", bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}

	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return fmt.Errorf("test connection request failed: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("read response body: %w", err)
	}

	var testResp TestConnectionResponse
	if err := json.Unmarshal(respBody, &testResp); err != nil {
		return fmt.Errorf("unmarshal response: %w", err)
	}

	if testResp.Status != "success" {
		return fmt.Errorf("connection test failed: %s", testResp.Message)
	}

	return nil
}
