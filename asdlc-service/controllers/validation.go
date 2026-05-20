package controllers

import (
	"net/http"

	"github.com/wso2/asdlc/asdlc-service/utils"
	"github.com/wso2/asdlc/asdlc-service/utils/validate"
)

// requireOrgHandle validates the {orgHandle} path param. Returns true if
// validation passed; on failure writes a 400 to w. orgHandle flows into
// OpenChoreo namespace lookups, GitHub repo paths, and OpenBao keys —
// the slug invariant is the cross-tenant fence.
func requireOrgHandle(w http.ResponseWriter, v string) bool {
	if err := validate.Slug(v); err != nil {
		utils.WriteErrorResponse(w, http.StatusBadRequest, "orgHandle: "+err.Error())
		return false
	}
	return true
}

// requireProjectName validates the {projectName} path param. Same shape as
// orgHandle — DNS-label-shaped slug. Used in repo paths, k8s resource
// names, GitHub repo slugs.
func requireProjectName(w http.ResponseWriter, v string) bool {
	if err := validate.Slug(v); err != nil {
		utils.WriteErrorResponse(w, http.StatusBadRequest, "projectName: "+err.Error())
		return false
	}
	return true
}

// requireComponentName validates the {componentName} path param. DNS-label
// slug; used in workspace paths and k8s component names.
func requireComponentName(w http.ResponseWriter, v string) bool {
	if err := validate.Slug(v); err != nil {
		utils.WriteErrorResponse(w, http.StatusBadRequest, "componentName: "+err.Error())
		return false
	}
	return true
}

// requireTaskID validates the {taskId} path param as a canonical UUID.
// taskId is the only identifier in the BFF surface that's a real UUID
// (ComponentTask PK).
func requireTaskID(w http.ResponseWriter, v string) bool {
	if err := validate.UUID(v); err != nil {
		utils.WriteErrorResponse(w, http.StatusBadRequest, "taskId: "+err.Error())
		return false
	}
	return true
}

// validateSlugParam is a generic slug validator that lets a caller surface
// a parameter name in the error. Returns the validation error (or nil).
// Caller should `return` on non-nil because the 400 response is already
// written.
func validateSlugParam(w http.ResponseWriter, paramName, v string) error {
	if err := validate.Slug(v); err != nil {
		utils.WriteErrorResponse(w, http.StatusBadRequest, paramName+": "+err.Error())
		return err
	}
	return nil
}
