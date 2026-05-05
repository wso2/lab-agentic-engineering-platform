package controllers

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"

	"github.com/golang-jwt/jwt/v5"
	"github.com/wso2/asdlc/asdlc-service/services"
	"github.com/wso2/asdlc/asdlc-service/utils"
)

// CollabController handles endpoints for real-time collaborative editing sessions.
type CollabController interface {
	GetCollabSession(w http.ResponseWriter, r *http.Request)
	ValidateCollabAccess(w http.ResponseWriter, r *http.Request)
}

type collabUserClaims struct {
	Name       string `json:"name"`
	Email      string `json:"email"`
	GivenName  string `json:"given_name"`
	FamilyName string `json:"family_name"`
	jwt.RegisteredClaims
}

type collabController struct {
	projectService services.ProjectService
}

func NewCollabController(projectService services.ProjectService) CollabController {
	return &collabController{projectService: projectService}
}

func (c *collabController) GetCollabSession(w http.ResponseWriter, r *http.Request) {
	org := r.PathValue("orgHandle")
	project := r.PathValue("projectName")
	if !requireOrgHandle(w, org) || !requireProjectName(w, project) {
		return
	}

	if _, err := c.projectService.GetProject(r.Context(), org, project); err != nil {
		slog.ErrorContext(r.Context(), "get project for collab session failed", "error", err, "org", org, "project", project)
		utils.WriteErrorResponse(w, http.StatusNotFound, "project not found")
		return
	}

	// Resolve the caller's display name from the JWT so the client can render
	// awareness/peer carets without needing to decode the ID token itself.
	name, email := parseDisplayIdentity(r.Header.Get("Authorization"))

	utils.WriteSuccessResponse(w, http.StatusOK, map[string]string{
		"roomId":   "spec-" + org + "-" + project,
		"wsUrl":    "/collab",
		"userName": name,
		"email":    email,
	})
}

func parseDisplayIdentity(authHeader string) (name, email string) {
	if authHeader == "" {
		return "", ""
	}
	parts := strings.SplitN(authHeader, " ", 2)
	if len(parts) != 2 || !strings.EqualFold(parts[0], "bearer") {
		return "", ""
	}
	claims := &collabUserClaims{}
	if _, _, err := jwt.NewParser().ParseUnverified(parts[1], claims); err != nil {
		return "", ""
	}
	name = claims.Name
	if name == "" {

		given := strings.TrimSpace(claims.GivenName)
		family := strings.TrimSpace(claims.FamilyName)
		if strings.EqualFold(family, "user") {
			family = ""
		}
		name = strings.TrimSpace(given + " " + family)
	}
	if name == "" {
		name, _ = claims.GetSubject()
	}
	return name, claims.Email
}

func (c *collabController) ValidateCollabAccess(w http.ResponseWriter, r *http.Request) {
	authHeader := r.Header.Get("Authorization")
	if authHeader == "" {
		utils.WriteErrorResponse(w, http.StatusUnauthorized, "missing Authorization header")
		return
	}
	name, email := parseDisplayIdentity(authHeader)
	if name == "" && email == "" {
		utils.WriteErrorResponse(w, http.StatusUnauthorized, "invalid JWT")
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{ //nolint:errcheck
		"name":  name,
		"email": email,
	})
}
