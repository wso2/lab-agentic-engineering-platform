package controllers

import (
	"encoding/json"
	"log/slog"
	"net/http"

	"github.com/wso2/asdlc/database-service/services"
	"github.com/wso2/asdlc/database-service/utils"
)

// DatabaseController handles HTTP requests for database provisioning.
type DatabaseController interface {
	ProvisionDatabase(w http.ResponseWriter, r *http.Request)
	TestConnection(w http.ResponseWriter, r *http.Request)
}

type databaseController struct {
	service services.DatabaseProvisioningService
}

func NewDatabaseController(service services.DatabaseProvisioningService) DatabaseController {
	return &databaseController{service: service}
}

type provisionDatabaseRequest struct {
	ProjectName string `json:"projectName"`
}

type provisionDatabaseResponse struct {
	Host     string `json:"host"`
	Port     int    `json:"port"`
	Database string `json:"database"`
	Username string `json:"username"`
	Password string `json:"password"`
}

// ProvisionDatabase handles POST /api/v1/databases/provision
func (c *databaseController) ProvisionDatabase(w http.ResponseWriter, r *http.Request) {
	var req provisionDatabaseRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		utils.WriteErrorResponse(w, http.StatusBadRequest, "invalid request body")
		return
	}

	if req.ProjectName == "" {
		utils.WriteErrorResponse(w, http.StatusBadRequest, "projectName is required")
		return
	}

	creds, err := c.service.ProvisionDatabase(r.Context(), req.ProjectName)
	if err != nil {
		slog.ErrorContext(r.Context(), "failed to provision database", "error", err)
		utils.WriteErrorResponse(w, http.StatusInternalServerError, "failed to provision database")
		return
	}

	resp := provisionDatabaseResponse{
		Host:     creds.Host,
		Port:     creds.Port,
		Database: creds.Database,
		Username: creds.Username,
		Password: creds.Password,
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(resp) //nolint:errcheck
}

type testConnectionRequest struct {
	Host     string `json:"host"`
	Port     int    `json:"port"`
	Database string `json:"database"`
	Username string `json:"username"`
	Password string `json:"password"`
}

type testConnectionResponse struct {
	Status  string `json:"status"`
	Message string `json:"message"`
}

// TestConnection handles POST /api/v1/databases/test-connection
func (c *databaseController) TestConnection(w http.ResponseWriter, r *http.Request) {
	var req testConnectionRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		utils.WriteErrorResponse(w, http.StatusBadRequest, "invalid request body")
		return
	}

	if req.Host == "" || req.Database == "" || req.Username == "" {
		utils.WriteErrorResponse(w, http.StatusBadRequest, "host, database, username are required")
		return
	}

	creds := &services.DatabaseCredentials{
		Host:     req.Host,
		Port:     req.Port,
		Database: req.Database,
		Username: req.Username,
		Password: req.Password,
	}

	if err := c.service.TestConnection(r.Context(), creds); err != nil {
		slog.DebugContext(r.Context(), "connection test failed", "database", req.Database, "error", err)
		resp := testConnectionResponse{
			Status:  "failed",
			Message: err.Error(),
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(resp) //nolint:errcheck
		return
	}

	resp := testConnectionResponse{
		Status:  "success",
		Message: "connection test passed",
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(resp) //nolint:errcheck
}
