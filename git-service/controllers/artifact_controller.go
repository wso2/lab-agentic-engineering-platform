package controllers

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"

	"github.com/wso2/asdlc/git-service/services"
	"github.com/wso2/asdlc/git-service/utils"
)

// ArtifactController serves the typed artifact endpoints:
//   - Requirements: multi-file directory at .asdlc/requirements/, tagged
//     `v<N>` per save.
//   - Design: single file .asdlc/design.json, tagged `v<N>-<M>` per save
//     (where N is the source requirements version).
type ArtifactController interface {
	// Requirements
	ListRequirements(w http.ResponseWriter, r *http.Request)
	GetRequirementFile(w http.ResponseWriter, r *http.Request)
	PutRequirementFile(w http.ResponseWriter, r *http.Request)
	DeleteRequirementFile(w http.ResponseWriter, r *http.Request)
	SaveRequirements(w http.ResponseWriter, r *http.Request)
	DiscardRequirements(w http.ResponseWriter, r *http.Request)
	ListRequirementsVersions(w http.ResponseWriter, r *http.Request)
	GetRequirementsVersion(w http.ResponseWriter, r *http.Request)

	// Design
	GetDesign(w http.ResponseWriter, r *http.Request)
	PutDesign(w http.ResponseWriter, r *http.Request)
	SaveDesign(w http.ResponseWriter, r *http.Request)
	DiscardDesign(w http.ResponseWriter, r *http.Request)
	ListDesignVersions(w http.ResponseWriter, r *http.Request)
	GetDesignVersion(w http.ResponseWriter, r *http.Request)
}

type artifactController struct {
	svc services.ArtifactService
}

func NewArtifactController(svc services.ArtifactService) ArtifactController {
	return &artifactController{svc: svc}
}

// ----- Common helpers -----

type putBody struct {
	Content string `json:"content"`
	IfMatch string `json:"ifMatch,omitempty"`
}

func writeArtifactError(w http.ResponseWriter, r *http.Request, err error, op string) {
	switch {
	case errors.Is(err, services.ErrArtifactNotFound):
		utils.WriteErrorResponse(w, http.StatusNotFound, "artifact not found")
	case errors.Is(err, services.ErrArtifactPathInvalid):
		utils.WriteErrorResponse(w, http.StatusBadRequest, err.Error())
	case errors.Is(err, services.ErrInvalidVersionTag):
		utils.WriteErrorResponse(w, http.StatusBadRequest, err.Error())
	case errors.Is(err, services.ErrIfMatchFailed):
		utils.WriteErrorResponse(w, http.StatusPreconditionFailed, "if-match precondition failed")
	case errors.Is(err, services.ErrNoVersionToDiscard):
		utils.WriteErrorResponse(w, http.StatusNotFound, "no saved version to revert to")
	case errors.Is(err, services.ErrConcurrentTagWrite):
		utils.WriteErrorResponse(w, http.StatusConflict, err.Error())
	case errors.Is(err, services.ErrNoRequirementsBaseline):
		utils.WriteErrorResponse(w, http.StatusConflict, err.Error())
	case errors.Is(err, services.ErrRepoNotFound):
		utils.WriteErrorResponse(w, http.StatusNotFound, "repository not found")
	case errors.Is(err, services.ErrRepoNotReady):
		utils.WriteErrorResponse(w, http.StatusUnprocessableEntity, "repository is not ready")
	default:
		slog.ErrorContext(r.Context(), "artifact handler failed", "op", op, "error", err)
		utils.WriteErrorResponse(w, http.StatusInternalServerError, op+" failed")
	}
}

func projectIDFrom(r *http.Request) string { return r.PathValue("projectId") }

func decodePutBody(r *http.Request) (putBody, error) {
	var body putBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		return body, err
	}
	return body, nil
}

// ----- Requirements handlers -----

func (c *artifactController) ListRequirements(w http.ResponseWriter, r *http.Request) {
	files, err := c.svc.ListRequirementFiles(r.Context(), projectIDFrom(r))
	if err != nil {
		writeArtifactError(w, r, err, "list requirements")
		return
	}
	utils.WriteSuccessResponse(w, http.StatusOK, services.RequirementsListResult{Files: files})
}

func (c *artifactController) GetRequirementFile(w http.ResponseWriter, r *http.Request) {
	relPath, err := requirementsRelPath(r.PathValue("name"))
	if err != nil {
		writeArtifactError(w, r, err, "get requirement file")
		return
	}
	res, err := c.svc.GetFile(r.Context(), projectIDFrom(r), relPath)
	if err != nil {
		writeArtifactError(w, r, err, "get requirement file")
		return
	}
	utils.WriteSuccessResponse(w, http.StatusOK, res)
}

func (c *artifactController) PutRequirementFile(w http.ResponseWriter, r *http.Request) {
	relPath, err := requirementsRelPath(r.PathValue("name"))
	if err != nil {
		writeArtifactError(w, r, err, "put requirement file")
		return
	}
	body, err := decodePutBody(r)
	if err != nil {
		utils.WriteErrorResponse(w, http.StatusBadRequest, "invalid request body")
		return
	}
	res, err := c.svc.PutFile(r.Context(), projectIDFrom(r), relPath, body.Content, body.IfMatch)
	if err != nil {
		writeArtifactError(w, r, err, "put requirement file")
		return
	}
	utils.WriteSuccessResponse(w, http.StatusOK, res)
}

func (c *artifactController) DeleteRequirementFile(w http.ResponseWriter, r *http.Request) {
	if err := c.svc.DeleteRequirementFile(r.Context(), projectIDFrom(r), r.PathValue("name")); err != nil {
		writeArtifactError(w, r, err, "delete requirement file")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (c *artifactController) SaveRequirements(w http.ResponseWriter, r *http.Request) {
	var body services.SaveRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		// Empty body is allowed — message is optional.
		body = services.SaveRequest{}
	}
	res, err := c.svc.SaveRequirements(r.Context(), projectIDFrom(r), body)
	if err != nil {
		writeArtifactError(w, r, err, "save requirements")
		return
	}
	utils.WriteSuccessResponse(w, http.StatusOK, res)
}

func (c *artifactController) DiscardRequirements(w http.ResponseWriter, r *http.Request) {
	files, err := c.svc.DiscardRequirements(r.Context(), projectIDFrom(r))
	if err != nil {
		writeArtifactError(w, r, err, "discard requirements")
		return
	}
	utils.WriteSuccessResponse(w, http.StatusOK, services.RequirementsListResult{Files: files})
}

func (c *artifactController) ListRequirementsVersions(w http.ResponseWriter, r *http.Request) {
	versions, err := c.svc.ListRequirementsVersions(r.Context(), projectIDFrom(r))
	if err != nil {
		writeArtifactError(w, r, err, "list requirements versions")
		return
	}
	utils.WriteSuccessResponse(w, http.StatusOK, versions)
}

func (c *artifactController) GetRequirementsVersion(w http.ResponseWriter, r *http.Request) {
	tag := r.PathValue("tag")
	files, err := c.svc.GetRequirementsAtTag(r.Context(), projectIDFrom(r), tag)
	if err != nil {
		writeArtifactError(w, r, err, "get requirements version")
		return
	}
	utils.WriteSuccessResponse(w, http.StatusOK, services.VersionRequirementsResult{
		Tag:   tag,
		Files: files,
	})
}

// requirementsRelPath validates a requirement file basename and returns its
// repo-relative path. Wrapped here so the controller doesn't import the
// service-internal helper directly.
func requirementsRelPath(name string) (string, error) {
	if name == "" {
		return "", services.ErrArtifactPathInvalid
	}
	// Lean on the service's path validator — it'll catch path separators,
	// traversal, and the .md suffix requirement.
	return services.RequirementFilePath(name)
}

// ----- Design handlers -----

func (c *artifactController) GetDesign(w http.ResponseWriter, r *http.Request) {
	res, err := c.svc.GetFile(r.Context(), projectIDFrom(r), services.DesignFilePath)
	if err != nil {
		writeArtifactError(w, r, err, "get design")
		return
	}
	utils.WriteSuccessResponse(w, http.StatusOK, res)
}

func (c *artifactController) PutDesign(w http.ResponseWriter, r *http.Request) {
	body, err := decodePutBody(r)
	if err != nil {
		utils.WriteErrorResponse(w, http.StatusBadRequest, "invalid request body")
		return
	}
	res, err := c.svc.PutFile(r.Context(), projectIDFrom(r), services.DesignFilePath, body.Content, body.IfMatch)
	if err != nil {
		writeArtifactError(w, r, err, "put design")
		return
	}
	utils.WriteSuccessResponse(w, http.StatusOK, res)
}

func (c *artifactController) SaveDesign(w http.ResponseWriter, r *http.Request) {
	var body services.SaveRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		body = services.SaveRequest{}
	}
	res, err := c.svc.SaveDesign(r.Context(), projectIDFrom(r), body)
	if err != nil {
		writeArtifactError(w, r, err, "save design")
		return
	}
	utils.WriteSuccessResponse(w, http.StatusOK, res)
}

func (c *artifactController) DiscardDesign(w http.ResponseWriter, r *http.Request) {
	res, err := c.svc.DiscardDesign(r.Context(), projectIDFrom(r))
	if err != nil {
		writeArtifactError(w, r, err, "discard design")
		return
	}
	utils.WriteSuccessResponse(w, http.StatusOK, res)
}

func (c *artifactController) ListDesignVersions(w http.ResponseWriter, r *http.Request) {
	versions, err := c.svc.ListDesignVersions(r.Context(), projectIDFrom(r))
	if err != nil {
		writeArtifactError(w, r, err, "list design versions")
		return
	}
	utils.WriteSuccessResponse(w, http.StatusOK, versions)
}

func (c *artifactController) GetDesignVersion(w http.ResponseWriter, r *http.Request) {
	tag := r.PathValue("tag")
	res, err := c.svc.GetDesignAtTag(r.Context(), projectIDFrom(r), tag)
	if err != nil {
		writeArtifactError(w, r, err, "get design version")
		return
	}
	utils.WriteSuccessResponse(w, http.StatusOK, res)
}
