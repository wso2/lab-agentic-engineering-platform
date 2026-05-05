package controllers

import (
	"encoding/json"
	"log/slog"
	"net/http"

	"github.com/wso2/asdlc/asdlc-service/models"
	"github.com/wso2/asdlc/asdlc-service/services"
	"github.com/wso2/asdlc/asdlc-service/utils"
)

type ConfigController interface {
	GetConfig(w http.ResponseWriter, r *http.Request)
	UpdateConfig(w http.ResponseWriter, r *http.Request)
}

type configController struct {
	service services.ConfigService
}

func NewConfigController(service services.ConfigService) ConfigController {
	return &configController{service: service}
}

func (c *configController) GetConfig(w http.ResponseWriter, r *http.Request) {
	org := r.PathValue("orgHandle")
	project := r.PathValue("projectName")
	component := r.PathValue("componentName")
	if !requireOrgHandle(w, org) || !requireProjectName(w, project) || !requireComponentName(w, component) {
		return
	}

	config, err := c.service.GetConfig(r.Context(), org, project, component)
	if err != nil {
		slog.ErrorContext(r.Context(), "get config failed", "error", err, "org", org, "project", project, "component", component)
		utils.WriteErrorResponse(w, http.StatusInternalServerError, "failed to get config")
		return
	}

	if config == nil {
		utils.WriteSuccessResponse(w, http.StatusOK, nil)
		return
	}
	utils.WriteSuccessResponse(w, http.StatusOK, config)
}

func (c *configController) UpdateConfig(w http.ResponseWriter, r *http.Request) {
	org := r.PathValue("orgHandle")
	project := r.PathValue("projectName")
	component := r.PathValue("componentName")
	if !requireOrgHandle(w, org) || !requireProjectName(w, project) || !requireComponentName(w, component) {
		return
	}

	var body struct {
		EnvVars models.EnvVarSlice `json:"envVars"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		utils.WriteErrorResponse(w, http.StatusBadRequest, "invalid request body")
		return
	}

	config, err := c.service.UpdateConfig(r.Context(), org, project, component, body.EnvVars)
	if err != nil {
		slog.ErrorContext(r.Context(), "update config failed", "error", err, "org", org, "project", project, "component", component)
		utils.WriteErrorResponse(w, http.StatusBadRequest, err.Error())
		return
	}

	utils.WriteSuccessResponse(w, http.StatusOK, config)
}
