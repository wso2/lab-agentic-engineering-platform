package database

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"
)

type DatabaseInfo struct {
	ID            string   `json:"id"`
	ReferenceID   string   `json:"referenceId"`
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

// Client calls the database-service.
type Client interface {
	// RegisterDatabase pre-registers a database in pending state after task generation.
	RegisterDatabase(ctx context.Context, orgID, projectID, referenceID, componentName, dbType, requestedName string) error
	// ListByProject returns all database records for a project.
	ListByProject(ctx context.Context, orgID, projectID string) ([]*DatabaseInfo, error)
	// UpdateDatabaseStatus sets the status of the database identified by referenceID.
	UpdateDatabaseStatus(ctx context.Context, referenceID, status string) error
}

type client struct {
	baseURL    string
	bearer     string
	httpClient *http.Client
}

func NewClient(baseURL, bearer string) Client {
	return &client{
		baseURL: baseURL,
		bearer:  bearer,
		httpClient: &http.Client{
			Timeout: 30 * time.Second,
		},
	}
}

func (c *client) authorize(req *http.Request) {
	if c.bearer != "" {
		req.Header.Set("Authorization", "Bearer "+c.bearer)
	}
}

type registerDatabaseRequest struct {
	ReferenceID   string `json:"referenceId"`
	OrgID         string `json:"orgId"`
	ProjectID     string `json:"projectId"`
	DBType        string `json:"dbType"`
	RequestedName string `json:"requestedName"`
	ComponentName string `json:"componentName"`
}

func (c *client) RegisterDatabase(ctx context.Context, orgID, projectID, referenceID, componentName, dbType, requestedName string) error {
	body, err := json.Marshal(registerDatabaseRequest{
		ReferenceID:   referenceID,
		OrgID:         orgID,
		ProjectID:     projectID,
		DBType:        dbType,
		RequestedName: requestedName,
		ComponentName: componentName,
	})
	if err != nil {
		return fmt.Errorf("marshal request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, "POST", c.baseURL+"/api/v1/databases/register", bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	c.authorize(req)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("register database request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusCreated {
		b, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("register database failed with status %d: %s", resp.StatusCode, string(b))
	}
	return nil
}

func (c *client) UpdateDatabaseStatus(ctx context.Context, referenceID, status string) error {
	body, err := json.Marshal(map[string]string{"status": status})
	if err != nil {
		return fmt.Errorf("marshal request: %w", err)
	}

	url := fmt.Sprintf("%s/api/v1/databases/%s/status", c.baseURL, referenceID)
	req, err := http.NewRequestWithContext(ctx, "PATCH", url, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	c.authorize(req)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("update database status request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("update database status failed with status %d: %s", resp.StatusCode, string(b))
	}
	return nil
}

func (c *client) ListByProject(ctx context.Context, orgID, projectID string) ([]*DatabaseInfo, error) {
	base, err := url.Parse(c.baseURL + "/api/v1/databases")
	if err != nil {
		return nil, fmt.Errorf("parse base url: %w", err)
	}
	q := url.Values{}
	q.Set("org_id", orgID)
	q.Set("project_id", projectID)
	base.RawQuery = q.Encode()
	req, err := http.NewRequestWithContext(ctx, "GET", base.String(), nil)
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	c.authorize(req)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("list databases request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("list databases failed with status %d: %s", resp.StatusCode, string(body))
	}

	var result struct {
		Databases []*DatabaseInfo `json:"databases"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("unmarshal response: %w", err)
	}
	return result.Databases, nil
}
