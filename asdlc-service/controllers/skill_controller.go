// Package controllers — org-scoped skills catalogue surface.
//
// Routes (all under the Org JWT middleware):
//
//	GET    /api/v1/organizations/{orgHandle}/skills          — list summaries
//	GET    /api/v1/organizations/{orgHandle}/skills/{name}   — full skill
//	POST   /api/v1/organizations/{orgHandle}/skills          — create custom
//	PUT    /api/v1/organizations/{orgHandle}/skills/{name}   — update custom
//	DELETE /api/v1/organizations/{orgHandle}/skills/{name}   — delete custom/imported
//	POST   /api/v1/organizations/{orgHandle}/skills/import   — import tarball
//
// Built-ins are read-only — PUT/DELETE against them return 403
// SKILL_NOT_EDITABLE. See docs/design/skills-system.md > "REST API".
package controllers

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"

	"github.com/wso2/asdlc/asdlc-service/services"
	"github.com/wso2/asdlc/asdlc-service/utils"
	"github.com/wso2/asdlc/asdlc-service/utils/validate"
)

// importMaxUploadBytes caps the multipart upload the BFF buffers before
// handing the file to the import service (which has its own decompressed
// budget).
const importMaxUploadBytes = 4 << 20 // 4 MiB

// SkillController owns the org-scoped skills catalogue.
type SkillController interface {
	List(w http.ResponseWriter, r *http.Request)
	Get(w http.ResponseWriter, r *http.Request)
	Create(w http.ResponseWriter, r *http.Request)
	Update(w http.ResponseWriter, r *http.Request)
	Delete(w http.ResponseWriter, r *http.Request)
	Import(w http.ResponseWriter, r *http.Request)
}

type skillController struct {
	skills   *services.SkillService
	mutation *services.SkillMutationService
	importer *services.SkillImportService
}

// NewSkillController wires the catalogue read surface plus the mutation +
// import services.
func NewSkillController(
	skills *services.SkillService,
	mutation *services.SkillMutationService,
	importer *services.SkillImportService,
) SkillController {
	return &skillController{skills: skills, mutation: mutation, importer: importer}
}

// skillDetail is the full single-skill response — the resolved Skill plus
// the derived `editable` flag.
type skillDetail struct {
	services.Skill
	Editable bool `json:"editable"`
}

// actorFromRequest returns a stable actor identifier for the audit log.
// Falls back to the org handle when no finer-grained subject is available.
func actorFromRequest(r *http.Request, orgHandle string) string {
	// TODO(org-rbac): use the JWT subject once an org_admin role exists.
	return orgHandle
}

func (c *skillController) List(w http.ResponseWriter, r *http.Request) {
	org := r.PathValue("orgHandle")
	if !requireOrgHandle(w, org) {
		return
	}
	summaries, err := c.skills.ListSummaries(r.Context(), org)
	if err != nil {
		slog.ErrorContext(r.Context(), "skill list failed", "error", err, "org", org)
		utils.WriteErrorResponse(w, http.StatusInternalServerError, "failed to list skills")
		return
	}
	utils.WriteSuccessResponse(w, http.StatusOK, map[string]any{"skills": summaries})
}

func (c *skillController) Get(w http.ResponseWriter, r *http.Request) {
	org := r.PathValue("orgHandle")
	if !requireOrgHandle(w, org) {
		return
	}
	name := r.PathValue("name")
	if err := validate.Slug(name); err != nil {
		utils.WriteErrorResponse(w, http.StatusBadRequest, "name: "+err.Error())
		return
	}
	sk, err := c.skills.Resolve(r.Context(), org, name)
	if err != nil {
		slog.ErrorContext(r.Context(), "skill get failed", "error", err, "org", org, "name", name)
		utils.WriteErrorResponse(w, http.StatusInternalServerError, "failed to get skill")
		return
	}
	if sk == nil {
		utils.WriteErrorResponse(w, http.StatusNotFound, "skill not found")
		return
	}
	utils.WriteSuccessResponse(w, http.StatusOK, skillDetail{Skill: *sk, Editable: sk.Kind != "builtin"})
}

func (c *skillController) Create(w http.ResponseWriter, r *http.Request) {
	org := r.PathValue("orgHandle")
	if !requireOrgHandle(w, org) {
		return
	}
	if c.mutation == nil {
		utils.WriteErrorResponse(w, http.StatusServiceUnavailable, "skill mutation not configured")
		return
	}
	var in services.CreateSkillInput
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		utils.WriteErrorResponse(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	sk, err := c.mutation.Create(r.Context(), org, actorFromRequest(r, org), in)
	if err != nil {
		c.writeMutationError(w, r, err, "create")
		return
	}
	utils.WriteSuccessResponse(w, http.StatusCreated, skillDetail{Skill: *sk, Editable: true})
}

func (c *skillController) Update(w http.ResponseWriter, r *http.Request) {
	org := r.PathValue("orgHandle")
	if !requireOrgHandle(w, org) {
		return
	}
	if c.mutation == nil {
		utils.WriteErrorResponse(w, http.StatusServiceUnavailable, "skill mutation not configured")
		return
	}
	name := r.PathValue("name")
	if err := validate.Slug(name); err != nil {
		utils.WriteErrorResponse(w, http.StatusBadRequest, "name: "+err.Error())
		return
	}
	var in services.UpdateSkillInput
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		utils.WriteErrorResponse(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	sk, err := c.mutation.Update(r.Context(), org, actorFromRequest(r, org), name, in)
	if err != nil {
		c.writeMutationError(w, r, err, "update")
		return
	}
	utils.WriteSuccessResponse(w, http.StatusOK, skillDetail{Skill: *sk, Editable: true})
}

func (c *skillController) Delete(w http.ResponseWriter, r *http.Request) {
	org := r.PathValue("orgHandle")
	if !requireOrgHandle(w, org) {
		return
	}
	if c.mutation == nil {
		utils.WriteErrorResponse(w, http.StatusServiceUnavailable, "skill mutation not configured")
		return
	}
	name := r.PathValue("name")
	if err := validate.Slug(name); err != nil {
		utils.WriteErrorResponse(w, http.StatusBadRequest, "name: "+err.Error())
		return
	}
	if err := c.mutation.Delete(r.Context(), org, actorFromRequest(r, org), name); err != nil {
		c.writeMutationError(w, r, err, "delete")
		return
	}
	utils.WriteSuccessResponse(w, http.StatusOK, map[string]string{"status": "deleted", "name": name})
}

func (c *skillController) Import(w http.ResponseWriter, r *http.Request) {
	org := r.PathValue("orgHandle")
	if !requireOrgHandle(w, org) {
		return
	}
	if c.importer == nil {
		utils.WriteErrorResponse(w, http.StatusServiceUnavailable, "skill import not configured")
		return
	}
	// ParseMultipartForm's argument only bounds in-memory file bytes, not
	// the total request size — wrap the body in a hard cap and map the
	// overflow to 413.
	r.Body = http.MaxBytesReader(w, r.Body, importMaxUploadBytes)
	if err := r.ParseMultipartForm(importMaxUploadBytes); err != nil {
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			utils.WriteErrorResponse(w, http.StatusRequestEntityTooLarge, "upload too large")
			return
		}
		utils.WriteErrorResponse(w, http.StatusBadRequest, "invalid multipart upload")
		return
	}
	file, _, err := r.FormFile("file")
	if err != nil {
		utils.WriteErrorResponse(w, http.StatusBadRequest, "missing 'file' field (tarball)")
		return
	}
	defer file.Close()

	result, err := c.importer.Import(r.Context(), org, actorFromRequest(r, org), file)
	if err != nil {
		c.writeMutationError(w, r, err, "import")
		return
	}
	utils.WriteSuccessResponse(w, http.StatusCreated, result)
}

// writeMutationError maps service-layer sentinels + structured validation
// errors onto HTTP status codes with stable error codes the console keys on.
func (c *skillController) writeMutationError(w http.ResponseWriter, r *http.Request, err error, op string) {
	var verr *services.SkillValidationError
	switch {
	case errors.As(err, &verr):
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write(services.MarshalValidationIssues(verr))
	case errors.Is(err, services.ErrSkillNameCollision):
		writeCodedError(w, http.StatusConflict, "NAME_COLLISION", err.Error())
	case errors.Is(err, services.ErrSkillNotEditable):
		writeCodedError(w, http.StatusForbidden, "SKILL_NOT_EDITABLE", "built-in skills are read-only")
	case errors.Is(err, services.ErrSkillNotFound):
		utils.WriteErrorResponse(w, http.StatusNotFound, "skill not found")
	case errors.Is(err, services.ErrImportedSkillInUse):
		writeCodedError(w, http.StatusConflict, "IMPORTED_SKILL_IN_USE",
			"imported skill is referenced by in-flight tasks")
	default:
		slog.ErrorContext(r.Context(), "skill mutation failed", "op", op, "error", err)
		utils.WriteErrorResponse(w, http.StatusInternalServerError, "failed to "+op+" skill")
	}
}

// writeCodedError renders { error, code, message } so the console can switch
// on a stable code rather than parsing the message.
func writeCodedError(w http.ResponseWriter, status int, code, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{
		"error":   http.StatusText(status),
		"code":    code,
		"message": message,
	})
}
