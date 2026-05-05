package middleware

import (
	"context"
	"errors"
	"log/slog"
	"net/http"

	"github.com/wso2/asdlc/git-service/internal/validate"
	"github.com/wso2/asdlc/git-service/models"
	"github.com/wso2/asdlc/git-service/repositories"
	"github.com/wso2/asdlc/git-service/utils"
)

type orgScopeContextKey string

const scopedRepoKey orgScopeContextKey = "orgScopedRepo"

// ErrRepoNotInScope is returned when the (orgId, projectId) pair on the path
// does not match a stored repo. The middleware translates this to a 404 so
// the existence of the project is not leaked across orgs.
var ErrRepoNotInScope = errors.New("repository not found for org+project")

// RequireOrgScope returns a middleware that enforces the new
// /api/v1/repos/{orgId}/{projectId}/... path-shape contract:
//
//  1. Both {orgId} and {projectId} are valid slugs (else 400 — malformed
//     identifier; not an existence question).
//  2. A repo row exists for that exact (orgId, projectId) pair (else 404).
//
// The looked-up repo is stashed in context so downstream handlers can read
// it via ScopedRepo() without re-querying. The middleware deliberately does
// NOT differentiate "wrong org" from "no such project" in its response: both
// return 404 with the same body, matching the design's "do not leak
// existence" rule.
//
// Path-side enforcement only. JWT.OcOrgID matching is a follow-up; today's
// Service JWT has no per-org claim. See docs/design/repo-storage-ownership.md
// "Auth" section for the deferred work.
func RequireOrgScope(repos repositories.RepoRepository) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			orgID := r.PathValue("orgId")
			projectID := r.PathValue("projectId")

			if err := validate.Slug(orgID); err != nil {
				utils.WriteErrorResponse(w, http.StatusBadRequest, "orgId: "+err.Error())
				return
			}
			if err := validate.Slug(projectID); err != nil {
				utils.WriteErrorResponse(w, http.StatusBadRequest, "projectId: "+err.Error())
				return
			}

			repo, err := repos.GetByOrgAndProjectID(r.Context(), orgID, projectID)
			if err != nil {
				slog.ErrorContext(r.Context(), "org-scope repo lookup failed",
					"orgId", orgID, "projectId", projectID, "error", err)
				utils.WriteErrorResponse(w, http.StatusInternalServerError, "lookup failed")
				return
			}
			if repo == nil {
				// Could be "no such project" or "project exists in another
				// org". Same response for both — never leak which.
				utils.WriteErrorResponse(w, http.StatusNotFound, "repository not found")
				return
			}

			ctx := context.WithValue(r.Context(), scopedRepoKey, repo)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// ScopedRepo returns the repo row attached to context by RequireOrgScope, or
// nil if the request did not pass through that middleware.
func ScopedRepo(ctx context.Context) *models.GitRepository {
	if r, ok := ctx.Value(scopedRepoKey).(*models.GitRepository); ok {
		return r
	}
	return nil
}
