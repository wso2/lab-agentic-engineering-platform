package observability

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/wso2/asdlc/asdlc-service/middleware"
	"github.com/wso2/asdlc/asdlc-service/models"
)

// Client fetches build logs from the observability service.
type Client interface {
	GetBuildLogs(ctx context.Context, orgName, projectName, componentName, buildName string) (*models.BuildLogs, error)
}

type observabilityClient struct {
	baseURL    string
	httpClient *http.Client
}

// NewClient creates a new observability client.
// baseURL is the observability service base URL (e.g. http://host:port).
func NewClient(baseURL string) Client {
	return &observabilityClient{
		baseURL:    baseURL,
		httpClient: &http.Client{Timeout: 30 * time.Second},
	}
}

type buildLogsRequest struct {
	ComponentName string    `json:"componentName"`
	NamespaceName string    `json:"namespaceName"`
	ProjectName   string    `json:"projectName"`
	StartTime     time.Time `json:"startTime"`
	EndTime       time.Time `json:"endTime"`
	Limit         int       `json:"limit,omitempty"`
	SortOrder     string    `json:"sortOrder,omitempty"`
}

type logEntry struct {
	Timestamp *time.Time `json:"timestamp,omitempty"`
	Log       *string    `json:"log,omitempty"`
	Level     *string    `json:"level,omitempty"`
}

type buildLogsResponse struct {
	Logs       *[]logEntry `json:"logs,omitempty"`
	TotalCount *int        `json:"totalCount,omitempty"`
}

func (c *observabilityClient) GetBuildLogs(ctx context.Context, orgName, projectName, componentName, buildName string) (*models.BuildLogs, error) {
	now := time.Now()
	body := buildLogsRequest{
		ComponentName: componentName,
		NamespaceName: orgName,
		ProjectName:   projectName,
		StartTime:     now.Add(-30 * 24 * time.Hour),
		EndTime:       now,
		Limit:         1000,
		SortOrder:     "asc",
	}

	bodyBytes, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("observability: marshal request: %w", err)
	}

	url := fmt.Sprintf("%s/api/logs/build/%s", c.baseURL, buildName)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(bodyBytes))
	if err != nil {
		return nil, fmt.Errorf("observability: create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if token := middleware.GetAuthToken(ctx); token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("observability: request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("observability: unexpected status %d", resp.StatusCode)
	}

	var result buildLogsResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("observability: decode response: %w", err)
	}

	logs := &models.BuildLogs{}
	if result.TotalCount != nil {
		logs.TotalCount = *result.TotalCount
	}
	if result.Logs != nil {
		for _, e := range *result.Logs {
			entry := models.BuildLogEntry{}
			if e.Timestamp != nil {
				entry.Timestamp = e.Timestamp.UTC().Format(time.RFC3339)
			}
			if e.Log != nil {
				entry.Log = *e.Log
			}
			if e.Level != nil {
				entry.Level = *e.Level
			}
			logs.Logs = append(logs.Logs, entry)
		}
	}
	if logs.Logs == nil {
		logs.Logs = []models.BuildLogEntry{}
	}
	return logs, nil
}
