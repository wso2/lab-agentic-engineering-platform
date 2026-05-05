package controllers

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"

	"github.com/wso2/asdlc/asdlc-service/clients/requests"
	"github.com/wso2/asdlc/asdlc-service/models"
	"github.com/wso2/asdlc/asdlc-service/services"
	"github.com/wso2/asdlc/asdlc-service/utils"
)

// OrganizationController serves the unscoped /api/v1/organizations endpoints.
type OrganizationController interface {
	ListOrganizations(w http.ResponseWriter, r *http.Request)
	CreateOrganization(w http.ResponseWriter, r *http.Request)
}

type organizationController struct {
	service services.OrganizationService
}

func NewOrganizationController(service services.OrganizationService) OrganizationController {
	return &organizationController{service: service}
}

func (c *organizationController) ListOrganizations(w http.ResponseWriter, r *http.Request) {
	list, err := c.service.List(r.Context())
	if err != nil {
		if errors.Is(err, services.ErrUnauthorized) {
			utils.WriteErrorResponse(w, http.StatusUnauthorized, "invalid or expired token")
			return
		}
		slog.ErrorContext(r.Context(), "list organizations failed", "error", err)
		utils.WriteErrorResponse(w, http.StatusInternalServerError, "failed to list organizations")
		return
	}
	utils.WriteSuccessResponse(w, http.StatusOK, list)
}

func (c *organizationController) CreateOrganization(w http.ResponseWriter, r *http.Request) {
	var req models.CreateOrganizationRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		utils.WriteErrorResponse(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.Name == "" {
		utils.WriteErrorResponse(w, http.StatusBadRequest, "name is required")
		return
	}

	org, err := c.service.Create(r.Context(), &req)
	if err != nil {
		if errors.Is(err, services.ErrUnauthorized) {
			utils.WriteErrorResponse(w, http.StatusUnauthorized, "invalid or expired token")
			return
		}
		if errors.Is(err, services.ErrOrganizationExists) {
			utils.WriteErrorResponse(w, http.StatusConflict, "organization already exists")
			return
		}
		var httpErr *requests.HttpError
		if errors.As(err, &httpErr) {
			slog.ErrorContext(r.Context(), "create organization failed", "error", err, "status", httpErr.StatusCode)
			utils.WriteErrorResponse(w, httpErr.StatusCode, httpErr.Body)
			return
		}
		slog.ErrorContext(r.Context(), "create organization failed", "error", err)
		utils.WriteErrorResponse(w, http.StatusInternalServerError, "failed to create organization")
		return
	}

	utils.WriteSuccessResponse(w, http.StatusCreated, org)
}
