// Package mcp implements an MCP (Model Context Protocol) server over the
// Streamable HTTP transport. AI agents call tools here without needing to
// know database credentials — the service uses its own internal connections.
package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/wso2/asdlc/database-service/services"
)

// Server is an http.Handler implementing the MCP streamable HTTP transport.
type Server struct {
	svc services.DatabaseService
}

// NewServer creates an MCP server backed by the given database service.
func NewServer(svc services.DatabaseService) *Server {
	return &Server{svc: svc}
}

// rpcRequest is a JSON-RPC 2.0 request.
type rpcRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params"`
}

// rpcResponse is a JSON-RPC 2.0 response.
type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcErr         `json:"error,omitempty"`
}

type rpcErr struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req rpcRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, nil, -32700, "parse error")
		return
	}

	slog.DebugContext(r.Context(), "mcp request", "method", req.Method)

	switch req.Method {
	case "initialize":
		s.handleInitialize(w, req)
	case "notifications/initialized":
		// Notification — no response body.
		w.WriteHeader(http.StatusAccepted)
	case "ping":
		writeResult(w, req.ID, struct{}{})
	case "tools/list":
		s.handleToolsList(w, req)
	case "tools/call":
		s.handleToolsCall(w, r.Context(), req)
	default:
		writeError(w, req.ID, -32601, "method not found: "+req.Method)
	}
}

func (s *Server) handleInitialize(w http.ResponseWriter, req rpcRequest) {
	writeResult(w, req.ID, map[string]any{
		"protocolVersion": "2024-11-05",
		"capabilities": map[string]any{
			"tools": map[string]any{},
		},
		"serverInfo": map[string]any{
			"name":    "database-service",
			"version": "1.0.0",
		},
	})
}

func (s *Server) handleToolsList(w http.ResponseWriter, req rpcRequest) {
	tools := []map[string]any{
		{
			"name":        "health_check",
			"description": "Check the health of the database service and its connectivity to MySQL and MongoDB. No parameters required.",
			"inputSchema": map[string]any{
				"type":       "object",
				"properties": map[string]any{},
			},
		},
		{
			"name":        "get_pending_database",
			"description": "Retrieve the pre-registered pending database record for this task. Call this first when executing a database provisioning task to get the database type and requested name. Use ASDLC_TASK_ID as the reference_id.",
			"inputSchema": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"reference_id": map[string]any{
						"type":        "string",
						"description": "The task ID (ASDLC_TASK_ID). Used to look up the pre-registered database record.",
					},
				},
				"required": []string{"reference_id"},
			},
		},
		{
			"name":        "create_database",
			"description": "Provision the database for this task. Looks up the pre-registered record by reference_id; if none exists, auto-registers using the supplied db_type and component_name before provisioning.",
			"inputSchema": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"reference_id": map[string]any{
						"type":        "string",
						"description": "The task ID (ASDLC_TASK_ID).",
					},
					"org_id": map[string]any{
						"type":        "string",
						"description": "Organization identifier (ASDLC_ORG_ID).",
					},
					"project_id": map[string]any{
						"type":        "string",
						"description": "Project identifier (ASDLC_PROJECT_ID).",
					},
					"db_type": map[string]any{
						"type":        "string",
						"description": "Database engine: 'mysql' or 'mongodb'. Required when no pre-registered record exists.",
					},
					"component_name": map[string]any{
						"type":        "string",
						"description": "Component name for the database (ASDLC_COMPONENT_NAME). Used as the requested database name when auto-registering.",
					},
				},
				"required": []string{"reference_id", "org_id", "project_id"},
			},
		},
		{
			"name":        "test_connection",
			"description": "Test the connection for the database provisioned for this task. Updates the database status to 'healthy' or 'faulty' and returns the status string. Call this after create_database.",
			"inputSchema": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"reference_id": map[string]any{
						"type":        "string",
						"description": "The task ID (ASDLC_TASK_ID).",
					},
				},
				"required": []string{"reference_id"},
			},
		},
		{
			"name":        "lookup_database",
			"description": "Retrieve the database credentials for a component that depends on a provisioned database. Use this in service components (not database tasks) to obtain the connection details for a database that was provisioned by another task.",
			"inputSchema": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"org_id": map[string]any{
						"type":        "string",
						"description": "Organization identifier",
					},
					"project_id": map[string]any{
						"type":        "string",
						"description": "Project identifier",
					},
					"component": map[string]any{
						"type":        "string",
						"description": "Database component name (e.g. 'order-service-db')",
					},
				},
				"required": []string{"org_id", "project_id", "component"},
			},
		},
	}
	writeResult(w, req.ID, map[string]any{"tools": tools})
}

type toolCallParams struct {
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments"`
}

func (s *Server) handleToolsCall(w http.ResponseWriter, ctx context.Context, req rpcRequest) {
	var p toolCallParams
	if err := json.Unmarshal(req.Params, &p); err != nil {
		writeError(w, req.ID, -32602, "invalid params")
		return
	}

	switch p.Name {
	case "health_check":
		s.toolHealthCheck(w, ctx, req.ID)
	case "get_pending_database":
		s.toolGetPendingDatabase(w, ctx, req.ID, p.Arguments)
	case "create_database":
		s.toolCreateDatabase(w, ctx, req.ID, p.Arguments)
	case "test_connection":
		s.toolTestConnection(w, ctx, req.ID, p.Arguments)
	case "lookup_database":
		s.toolLookupDatabase(w, ctx, req.ID, p.Arguments)
	default:
		writeError(w, req.ID, -32602, "unknown tool: "+p.Name)
	}
}

func (s *Server) toolHealthCheck(w http.ResponseWriter, ctx context.Context, id json.RawMessage) {
	status := s.svc.HealthCheck(ctx)
	text := fmt.Sprintf(
		"MySQL: ok=%v%s\nMongoDB: ok=%v%s",
		status.MySQL.OK, msgSuffix(status.MySQL.Message),
		status.MongoDB.OK, msgSuffix(status.MongoDB.Message),
	)
	writeToolResult(w, id, text, false)
}

func (s *Server) toolGetPendingDatabase(w http.ResponseWriter, ctx context.Context, id json.RawMessage, args json.RawMessage) {
	var a struct {
		ReferenceID string `json:"reference_id"`
	}
	if err := json.Unmarshal(args, &a); err != nil || a.ReferenceID == "" {
		writeToolResult(w, id, "reference_id is required", true)
		return
	}

	record, err := s.svc.GetPendingDatabase(ctx, a.ReferenceID)
	if err != nil {
		writeToolResult(w, id, fmt.Sprintf("failed to get pending database: %v", err), true)
		return
	}
	if record == nil {
		writeToolResult(w, id, fmt.Sprintf("no pending database found for reference_id=%s", a.ReferenceID), true)
		return
	}

	out, _ := json.MarshalIndent(record, "", "  ")
	writeToolResult(w, id, string(out), false)
}

func (s *Server) toolCreateDatabase(w http.ResponseWriter, ctx context.Context, id json.RawMessage, args json.RawMessage) {
	var a struct {
		ReferenceID   string `json:"reference_id"`
		OrgID         string `json:"org_id"`
		ProjectID     string `json:"project_id"`
		DBType        string `json:"db_type"`
		ComponentName string `json:"component_name"`
	}
	if err := json.Unmarshal(args, &a); err != nil {
		writeToolResult(w, id, "invalid arguments", true)
		return
	}
	if a.ReferenceID == "" || a.OrgID == "" || a.ProjectID == "" {
		writeToolResult(w, id, "reference_id, org_id, and project_id are required", true)
		return
	}

	record, err := s.svc.CreateDatabase(ctx, services.CreateDatabaseRequest{
		ReferenceID:   a.ReferenceID,
		OrgID:         a.OrgID,
		ProjectID:     a.ProjectID,
		DBType:        a.DBType,
		ComponentName: a.ComponentName,
	})
	if err != nil {
		writeToolResult(w, id, fmt.Sprintf("failed to create database: %v", err), true)
		return
	}

	out, _ := json.MarshalIndent(record, "", "  ")
	writeToolResult(w, id, string(out), false)
}

func (s *Server) toolTestConnection(w http.ResponseWriter, ctx context.Context, id json.RawMessage, args json.RawMessage) {
	var a struct {
		ReferenceID string `json:"reference_id"`
	}
	if err := json.Unmarshal(args, &a); err != nil || a.ReferenceID == "" {
		writeToolResult(w, id, "reference_id is required", true)
		return
	}

	const maxAttempts = 3
	const retryDelay = 3 * time.Second

	var lastErr string
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		status, err := s.svc.TestDatabase(ctx, a.ReferenceID)
		if err != nil {
			lastErr = err.Error()
		} else if status == "healthy" {
			writeToolResult(w, id, "healthy", false)
			return
		} else {
			lastErr = fmt.Sprintf("connection test returned status: %s", status)
		}
		slog.InfoContext(ctx, "test_connection attempt failed", "attempt", attempt, "referenceID", a.ReferenceID, "error", lastErr)
		if attempt < maxAttempts {
			time.Sleep(retryDelay)
		}
	}

	writeToolResult(w, id, fmt.Sprintf("test_connection failed after %d attempts: %s", maxAttempts, lastErr), true)
}

func (s *Server) toolLookupDatabase(w http.ResponseWriter, ctx context.Context, id json.RawMessage, args json.RawMessage) {
	var a struct {
		OrgID     string `json:"org_id"`
		ProjectID string `json:"project_id"`
		Component string `json:"component"`
	}
	if err := json.Unmarshal(args, &a); err != nil {
		writeToolResult(w, id, "invalid arguments", true)
		return
	}
	if a.OrgID == "" || a.ProjectID == "" || a.Component == "" {
		writeToolResult(w, id, "org_id, project_id, and component are required", true)
		return
	}

	record, err := s.svc.LookupDatabase(ctx, a.OrgID, a.ProjectID, a.Component)
	if err != nil {
		writeToolResult(w, id, fmt.Sprintf("lookup failed: %v", err), true)
		return
	}
	if record == nil {
		writeToolResult(w, id, fmt.Sprintf(
			"no provisioned database found for org=%s project=%s component=%s",
			a.OrgID, a.ProjectID, a.Component,
		), true)
		return
	}

	out, _ := json.MarshalIndent(record, "", "  ")
	writeToolResult(w, id, string(out), false)
}

// --- JSON-RPC helpers ---

func writeResult(w http.ResponseWriter, id json.RawMessage, result any) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(rpcResponse{JSONRPC: "2.0", ID: id, Result: result}) //nolint:errcheck
}

func writeError(w http.ResponseWriter, id json.RawMessage, code int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(rpcResponse{ //nolint:errcheck
		JSONRPC: "2.0",
		ID:      id,
		Error:   &rpcErr{Code: code, Message: msg},
	})
}

// writeToolResult formats a tool call result per the MCP content spec.
func writeToolResult(w http.ResponseWriter, id json.RawMessage, text string, isError bool) {
	result := map[string]any{
		"content": []map[string]any{
			{"type": "text", "text": text},
		},
	}
	if isError {
		result["isError"] = true
	}
	writeResult(w, id, result)
}

func msgSuffix(msg string) string {
	if msg == "" {
		return ""
	}
	return " (" + msg + ")"
}
