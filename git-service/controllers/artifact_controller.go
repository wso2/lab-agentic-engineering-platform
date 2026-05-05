package controllers

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strconv"

	"github.com/wso2/asdlc/git-service/services"
	"github.com/wso2/asdlc/git-service/utils"
)

// ArtifactController handles HTTP for the typed artifact endpoints introduced
// in PR 1 of the repo-storage-ownership refactor: read/write working-tree
// content for spec/design/wireframes, atomic save (commit+push+tag under one
// mutex), discard, and version listing.
type ArtifactController interface {
	GetSpec(w http.ResponseWriter, r *http.Request)
	PutSpec(w http.ResponseWriter, r *http.Request)
	SaveSpec(w http.ResponseWriter, r *http.Request)
	DiscardSpec(w http.ResponseWriter, r *http.Request)
	ListSpecVersions(w http.ResponseWriter, r *http.Request)
	GetSpecVersion(w http.ResponseWriter, r *http.Request)

	GetDesign(w http.ResponseWriter, r *http.Request)
	PutDesign(w http.ResponseWriter, r *http.Request)
	SaveDesign(w http.ResponseWriter, r *http.Request)
	DiscardDesign(w http.ResponseWriter, r *http.Request)
	ListDesignVersions(w http.ResponseWriter, r *http.Request)
	GetDesignVersion(w http.ResponseWriter, r *http.Request)

	ListWireframes(w http.ResponseWriter, r *http.Request)
	GetWireframe(w http.ResponseWriter, r *http.Request)
	PutWireframe(w http.ResponseWriter, r *http.Request)
}

type artifactController struct {
	svc services.ArtifactService
}

func NewArtifactController(svc services.ArtifactService) ArtifactController {
	return &artifactController{svc: svc}
}

// ----- Common helpers -----

const (
	specRelPath   = ".asdlc/spec.md"
	designRelPath = ".asdlc/design.json"
)

func wireframeRelPath(name string) string { return ".asdlc/wireframes/" + name }

type putRequest struct {
	Content string `json:"content"`
	IfMatch string `json:"ifMatch,omitempty"`
}

// writeArtifactError maps service-layer error sentinels to HTTP statuses.
// Centralised so spec/design/wireframe handlers all behave the same way.
func writeArtifactError(w http.ResponseWriter, r *http.Request, err error, op string) {
	switch {
	case errors.Is(err, services.ErrArtifactNotFound):
		utils.WriteErrorResponse(w, http.StatusNotFound, "artifact not found")
	case errors.Is(err, services.ErrArtifactPathInvalid):
		utils.WriteErrorResponse(w, http.StatusBadRequest, err.Error())
	case errors.Is(err, services.ErrIfMatchFailed):
		utils.WriteErrorResponse(w, http.StatusPreconditionFailed, "if-match precondition failed")
	case errors.Is(err, services.ErrNoVersionToDiscard):
		utils.WriteErrorResponse(w, http.StatusNotFound, "no saved version to revert to")
	case errors.Is(err, services.ErrConcurrentTagWrite):
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

// readPutBody decodes a `{content, ifMatch?}` body. Centralised so the size
// cap + content-required check happens once.
func readPutBody(r *http.Request) (putRequest, error) {
	var body putRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		return body, err
	}
	return body, nil
}

func projectIDFrom(r *http.Request) string { return r.PathValue("projectId") }

// ----- Spec handlers -----

func (c *artifactController) GetSpec(w http.ResponseWriter, r *http.Request) {
	res, err := c.svc.GetFile(r.Context(), projectIDFrom(r), specRelPath)
	if err != nil {
		writeArtifactError(w, r, err, "get spec")
		return
	}
	utils.WriteSuccessResponse(w, http.StatusOK, res)
}

func (c *artifactController) PutSpec(w http.ResponseWriter, r *http.Request) {
	body, err := readPutBody(r)
	if err != nil {
		utils.WriteErrorResponse(w, http.StatusBadRequest, "invalid request body")
		return
	}
	res, err := c.svc.PutFile(r.Context(), projectIDFrom(r), specRelPath, body.Content, body.IfMatch)
	if err != nil {
		writeArtifactError(w, r, err, "put spec")
		return
	}
	utils.WriteSuccessResponse(w, http.StatusOK, res)
}

func (c *artifactController) SaveSpec(w http.ResponseWriter, r *http.Request) {
	c.handleSave(w, r, services.ArtifactSpec)
}

func (c *artifactController) DiscardSpec(w http.ResponseWriter, r *http.Request) {
	c.handleDiscard(w, r, services.ArtifactSpec)
}

func (c *artifactController) ListSpecVersions(w http.ResponseWriter, r *http.Request) {
	c.handleListVersions(w, r, services.ArtifactSpec)
}

func (c *artifactController) GetSpecVersion(w http.ResponseWriter, r *http.Request) {
	c.handleGetVersion(w, r, services.ArtifactSpec)
}

// ----- Design handlers -----

func (c *artifactController) GetDesign(w http.ResponseWriter, r *http.Request) {
	res, err := c.svc.GetFile(r.Context(), projectIDFrom(r), designRelPath)
	if err != nil {
		writeArtifactError(w, r, err, "get design")
		return
	}
	utils.WriteSuccessResponse(w, http.StatusOK, res)
}

func (c *artifactController) PutDesign(w http.ResponseWriter, r *http.Request) {
	body, err := readPutBody(r)
	if err != nil {
		utils.WriteErrorResponse(w, http.StatusBadRequest, "invalid request body")
		return
	}
	res, err := c.svc.PutFile(r.Context(), projectIDFrom(r), designRelPath, body.Content, body.IfMatch)
	if err != nil {
		writeArtifactError(w, r, err, "put design")
		return
	}
	utils.WriteSuccessResponse(w, http.StatusOK, res)
}

func (c *artifactController) SaveDesign(w http.ResponseWriter, r *http.Request) {
	c.handleSave(w, r, services.ArtifactDesign)
}

func (c *artifactController) DiscardDesign(w http.ResponseWriter, r *http.Request) {
	c.handleDiscard(w, r, services.ArtifactDesign)
}

func (c *artifactController) ListDesignVersions(w http.ResponseWriter, r *http.Request) {
	c.handleListVersions(w, r, services.ArtifactDesign)
}

func (c *artifactController) GetDesignVersion(w http.ResponseWriter, r *http.Request) {
	c.handleGetVersion(w, r, services.ArtifactDesign)
}

// ----- Wireframe handlers -----

func (c *artifactController) ListWireframes(w http.ResponseWriter, r *http.Request) {
	res, err := c.svc.ListWireframes(r.Context(), projectIDFrom(r))
	if err != nil {
		writeArtifactError(w, r, err, "list wireframes")
		return
	}
	utils.WriteSuccessResponse(w, http.StatusOK, res)
}

func (c *artifactController) GetWireframe(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	res, err := c.svc.GetFile(r.Context(), projectIDFrom(r), wireframeRelPath(name))
	if err != nil {
		writeArtifactError(w, r, err, "get wireframe")
		return
	}
	utils.WriteSuccessResponse(w, http.StatusOK, res)
}

func (c *artifactController) PutWireframe(w http.ResponseWriter, r *http.Request) {
	body, err := readPutBody(r)
	if err != nil {
		utils.WriteErrorResponse(w, http.StatusBadRequest, "invalid request body")
		return
	}
	name := r.PathValue("name")
	res, err := c.svc.PutFile(r.Context(), projectIDFrom(r), wireframeRelPath(name), body.Content, body.IfMatch)
	if err != nil {
		writeArtifactError(w, r, err, "put wireframe")
		return
	}
	utils.WriteSuccessResponse(w, http.StatusOK, res)
}

// ----- Shared spec/design plumbing -----

func (c *artifactController) handleSave(w http.ResponseWriter, r *http.Request, t services.ArtifactType) {
	var body services.SaveRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		utils.WriteErrorResponse(w, http.StatusBadRequest, "invalid request body")
		return
	}
	res, err := c.svc.Save(r.Context(), projectIDFrom(r), t, body)
	if err != nil {
		writeArtifactError(w, r, err, "save "+string(t))
		return
	}
	utils.WriteSuccessResponse(w, http.StatusOK, res)
}

func (c *artifactController) handleDiscard(w http.ResponseWriter, r *http.Request, t services.ArtifactType) {
	res, err := c.svc.Discard(r.Context(), projectIDFrom(r), t)
	if err != nil {
		writeArtifactError(w, r, err, "discard "+string(t))
		return
	}
	utils.WriteSuccessResponse(w, http.StatusOK, res)
}

func (c *artifactController) handleListVersions(w http.ResponseWriter, r *http.Request, t services.ArtifactType) {
	res, err := c.svc.ListVersions(r.Context(), projectIDFrom(r), t)
	if err != nil {
		writeArtifactError(w, r, err, "list "+string(t)+" versions")
		return
	}
	utils.WriteSuccessResponse(w, http.StatusOK, res)
}

func (c *artifactController) handleGetVersion(w http.ResponseWriter, r *http.Request, t services.ArtifactType) {
	versionStr := r.PathValue("version")
	version, err := strconv.Atoi(versionStr)
	if err != nil || version < 1 {
		utils.WriteErrorResponse(w, http.StatusBadRequest, "version: must be a positive integer")
		return
	}
	res, err := c.svc.GetVersion(r.Context(), projectIDFrom(r), t, version)
	if err != nil {
		writeArtifactError(w, r, err, "get "+string(t)+" version")
		return
	}
	utils.WriteSuccessResponse(w, http.StatusOK, res)
}
