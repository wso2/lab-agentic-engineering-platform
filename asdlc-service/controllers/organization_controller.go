package controllers

import (
	"errors"
	"log/slog"
	"net/http"

	"github.com/wso2/asdlc/asdlc-service/services"
	"github.com/wso2/asdlc/asdlc-service/utils"
)

// OrganizationController serves the unscoped /api/v1/organizations endpoints.
//
// The BFF is read-only over OC namespaces. Tenant onboarding (creating the
// OC namespace + the per-org bootstrap content) is the platform's job —
// `platform-api-service` in hosted, `seed-admin-org.sh` in local. There is
// no `POST /api/v1/organizations` endpoint here.
type OrganizationController interface {
	ListOrganizations(w http.ResponseWriter, r *http.Request)
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
