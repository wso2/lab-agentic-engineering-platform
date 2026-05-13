// Package agents is the client for the asdlc-agents-service (AI SDK v6-based,
// ANTHROPIC_API_KEY auth). Streams AI SDK UI Message Stream SSE responses for
// the streaming agents (business-analyst, architect, tech-lead).
package agents

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	"github.com/wso2/asdlc/asdlc-service/clients/auth"
	"github.com/wso2/asdlc/asdlc-service/clients/httpx"
	"github.com/wso2/asdlc/asdlc-service/models"
)

// Client calls the asdlc-agents-service.
//
// Every streaming method carries an `orgID` parameter that is sent to
// agents-service as the `X-Oc-Org-Id` header. agents-service uses this
// to resolve the effective Anthropic API key per call (org key when
// configured, platform fallback otherwise — see
// docs/design/anthropic-key-dual-token.md §6.4).
type Client interface {
	// StreamDocumentGeneration POSTs to /v1/agents/document-generation/{skillId}
	// to run a registered document-generation skill (e.g. requirements bootstrap,
	// functional-requirements derivation). The skillID selects the skill; sources
	// are the sibling files passed as context (filename → content); prompt is the
	// optional user prompt for bootstrap-style skills. Returns the raw SSE response
	// body. Caller must close.
	StreamDocumentGeneration(ctx context.Context, orgID, skillID string, req DocumentGenerationRequest) (io.ReadCloser, error)

	// StreamArchitect POSTs the requirements bundle (and optional previous design)
	// to /v1/agents/architect and returns the raw SSE response body. The stream
	// emits structured custom events (data-overview, data-requirements,
	// data-component, data-finish) as the architecture is generated.
	// Caller must close.
	StreamArchitect(ctx context.Context, orgID string, req ArchitectRequest) (io.ReadCloser, error)

	// StreamTechLeadPlan POSTs the planner input to /v1/agents/tech-lead/plan
	// and returns the raw SSE response body.
	StreamTechLeadPlan(ctx context.Context, orgID string, req TechLeadPlanRequest) (io.ReadCloser, error)

	// StreamTechLeadDetail POSTs N issued tasks to /v1/agents/tech-lead/detail.
	StreamTechLeadDetail(ctx context.Context, orgID string, req TechLeadDetailRequest) (io.ReadCloser, error)
}

// DocumentGenerationRequest is the body sent to the generic skill endpoint.
type DocumentGenerationRequest struct {
	Sources map[string]string `json:"sources,omitempty"` // sibling files: filename → content
	Prompt  string            `json:"prompt,omitempty"`  // optional user prompt
}

// ArchitectRequest is the body sent to the architect endpoint.
type ArchitectRequest struct {
	ProjectName    string           `json:"projectName"`
	Spec           string           `json:"spec"`
	PreviousDesign *ArchitectDesign `json:"previousDesign,omitempty"`
}

// ArchitectDesign mirrors the architect output shape for incremental regen.
type ArchitectDesign struct {
	Overview     string                   `json:"overview"`
	Requirements []string                 `json:"requirements"`
	Components   []models.DesignComponent `json:"components"`
}

// TechLeadPlanRequest is the body sent to /v1/agents/tech-lead/plan. Mirrors
// agents/src/agents/tech-lead/schema.ts → TechLeadPlanInput plus the optional
// validator diff context.
type TechLeadPlanRequest struct {
	ProjectName   string                          `json:"projectName"`
	Spec          string                          `json:"spec"`
	SlimDesign    []TechLeadSlimComponent         `json:"slimDesign"`
	SpecDiff      string                          `json:"specDiff,omitempty"`
	DesignDiff    string                          `json:"designDiff,omitempty"`
	ExistingTasks []TechLeadExistingTaskSummary   `json:"existingTasks,omitempty"`
	Mode          string                          `json:"mode"` // "fresh" | "incremental"
	Diff          *TechLeadValidatorDiffContext   `json:"diff,omitempty"`
}

type TechLeadSlimComponent struct {
	Name          string   `json:"name"`
	ComponentType string   `json:"componentType"`
	Language      string   `json:"language"`
	DependsOn     []string `json:"dependsOn"`
}

type TechLeadExistingTaskSummary struct {
	IssueNumber   *int   `json:"issueNumber,omitempty"`
	Title         string `json:"title"`
	ComponentName string `json:"componentName"`
	Status        string `json:"status"`
}

type TechLeadValidatorDiffContext struct {
	Added                    []string `json:"added"`
	ContractAffectedModified []string `json:"contractAffectedModified"`
	Removed                  []string `json:"removed"`
}

// TechLeadDetailRequest is the body sent to /v1/agents/tech-lead/detail.
type TechLeadDetailRequest struct {
	ProjectName string                  `json:"projectName"`
	Spec        string                  `json:"spec"`
	Items       []TechLeadDetailItem    `json:"items"`
}

type TechLeadDetailItem struct {
	TaskID                     string                  `json:"taskId"`
	ComponentName              string                  `json:"componentName"`
	Title                      string                  `json:"title"`
	Rationale                  string                  `json:"rationale"`
	DesignSlice                string                  `json:"designSlice"`
	DepSummaries               []TechLeadSlimComponent `json:"depSummaries"`
	ExistingTitlesForComponent []TechLeadExistingTitle `json:"existingTitlesForComponent"`
}

type TechLeadExistingTitle struct {
	Title  string `json:"title"`
	Status string `json:"status"`
}

type client struct {
	baseURL    string
	httpClient *http.Client
}

// NewClient builds an agents-service client. provider attaches a Service
// JWT to every outbound call (audience: agents-service); pass nil to disable
// service-auth in tests/dev.
func NewClient(baseURL string, provider *auth.AuthProvider) Client {
	return &client{
		baseURL: baseURL,
		// No client-side timeout — streaming responses can take minutes.
		// Cancellation flows via ctx.
		httpClient: &http.Client{
			Transport: httpx.ServiceTransport(provider),
		},
	}
}

func (c *client) StreamDocumentGeneration(ctx context.Context, orgID, skillID string, req DocumentGenerationRequest) (io.ReadCloser, error) {
	return c.streamSSE(ctx, orgID, "/v1/agents/document-generation/"+skillID, req)
}

func (c *client) StreamArchitect(ctx context.Context, orgID string, req ArchitectRequest) (io.ReadCloser, error) {
	body, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	url := c.baseURL + "/v1/agents/architect"
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "text/event-stream")
	httpReq.Header.Set("X-Oc-Org-Id", orgID)

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("agents service request: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		defer resp.Body.Close()
		msg, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("agents service error (status %d): %s", resp.StatusCode, string(msg))
	}

	return resp.Body, nil
}

func (c *client) StreamTechLeadPlan(ctx context.Context, orgID string, req TechLeadPlanRequest) (io.ReadCloser, error) {
	return c.streamSSE(ctx, orgID, "/v1/agents/tech-lead/plan", req)
}

func (c *client) StreamTechLeadDetail(ctx context.Context, orgID string, req TechLeadDetailRequest) (io.ReadCloser, error) {
	return c.streamSSE(ctx, orgID, "/v1/agents/tech-lead/detail", req)
}

// streamSSE is the shared POST + SSE wrapper used by every streaming agent
// route. Caller must close the returned body. No client-side timeout —
// streams can run for minutes; cancellation flows via ctx. The orgID is
// sent as the `X-Oc-Org-Id` header for the Anthropic-key resolver.
func (c *client) streamSSE(ctx context.Context, orgID, path string, body any) (io.ReadCloser, error) {
	payload, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+path, bytes.NewReader(payload))
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "text/event-stream")
	httpReq.Header.Set("X-Oc-Org-Id", orgID)

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("agents service request: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		defer resp.Body.Close()
		msg, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("agents service error (status %d): %s", resp.StatusCode, string(msg))
	}
	return resp.Body, nil
}

