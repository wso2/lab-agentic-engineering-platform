package controllers

import (
	"encoding/json"
	"log/slog"
	"net/http"

	"github.com/wso2/asdlc/database-service/services"
	"github.com/wso2/asdlc/database-service/utils"
)

// RegistryController handles database registry endpoints used by the BFF.
type RegistryController interface {
	RegisterDatabase(w http.ResponseWriter, r *http.Request)
	ListDatabases(w http.ResponseWriter, r *http.Request)
	UpdateDatabaseStatus(w http.ResponseWriter, r *http.Request)
}

type registryController struct {
	svc services.DatabaseRegistryService
}

func NewRegistryController(svc services.DatabaseRegistryService) RegistryController {
	return &registryController{svc: svc}
}

type registerDatabaseRequest struct {
	ReferenceID   string `json:"referenceId"`
	OrgID         string `json:"orgId"`
	ProjectID     string `json:"projectId"`
	DBType        string `json:"dbType"`
	RequestedName string `json:"requestedName"`
	ComponentName string `json:"componentName"`
}

// RegisterDatabase handles POST /api/v1/databases/register
func (c *registryController) RegisterDatabase(w http.ResponseWriter, r *http.Request) {
	var req registerDatabaseRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		utils.WriteErrorResponse(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.ReferenceID == "" || req.OrgID == "" || req.ProjectID == "" {
		utils.WriteErrorResponse(w, http.StatusBadRequest, "referenceId, orgId, projectId are required")
		return
	}

	if err := c.svc.Register(r.Context(), req.OrgID, req.ProjectID, req.ReferenceID, req.ComponentName, req.DBType, req.RequestedName); err != nil {
		slog.ErrorContext(r.Context(), "register database failed", "error", err)
		utils.WriteErrorResponse(w, http.StatusInternalServerError, "failed to register database")
		return
	}

	w.WriteHeader(http.StatusCreated)
}

// ListDatabases handles GET /api/v1/databases?org_id=...&project_id=...
func (c *registryController) ListDatabases(w http.ResponseWriter, r *http.Request) {
	orgID := r.URL.Query().Get("org_id")
	projectID := r.URL.Query().Get("project_id")
	if orgID == "" || projectID == "" {
		utils.WriteErrorResponse(w, http.StatusBadRequest, "org_id and project_id query params required")
		return
	}

	records, err := c.svc.ListByProject(r.Context(), orgID, projectID)
	if err != nil {
		slog.ErrorContext(r.Context(), "list databases failed", "error", err)
		utils.WriteErrorResponse(w, http.StatusInternalServerError, "failed to list databases")
		return
	}
	if records == nil {
		records = []*services.DatabaseRecord{}
	}

	type response struct {
		Databases []*services.DatabaseRecord `json:"databases"`
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(response{Databases: records}) //nolint:errcheck
}

type updateStatusRequest struct {
	Status string `json:"status"`
}

// UpdateDatabaseStatus handles PATCH /api/v1/databases/{referenceID}/status
func (c *registryController) UpdateDatabaseStatus(w http.ResponseWriter, r *http.Request) {
	referenceID := r.PathValue("referenceID")
	if referenceID == "" {
		utils.WriteErrorResponse(w, http.StatusBadRequest, "referenceID path param required")
		return
	}

	var req updateStatusRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		utils.WriteErrorResponse(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.Status == "" {
		utils.WriteErrorResponse(w, http.StatusBadRequest, "status is required")
		return
	}

	if err := c.svc.UpdateStatus(r.Context(), referenceID, req.Status); err != nil {
		slog.ErrorContext(r.Context(), "update database status failed", "error", err, "referenceID", referenceID)
		utils.WriteErrorResponse(w, http.StatusInternalServerError, "failed to update database status")
		return
	}

	w.WriteHeader(http.StatusOK)
}
