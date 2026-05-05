package api

import (
	"net/http"

	"github.com/wso2/asdlc/git-service/controllers"
)

// registerRepoOnlyRoutes wires every Service-JWT-gated repo / git-ops
// endpoint. Credentials-refresh moved to its own taskMux in app.go so the
// two muxes don't share auth semantics.
//
// PR 1 of the repo-storage-ownership refactor:
//   - Adds the typed artifact endpoints (`/artifacts/{spec,design,wireframes}/...`)
//     served by the new ArtifactController + ArtifactService.
//   - Drops the legacy `{projectId}`-only routes that PR 0 left as compat
//     shims. The BFF has been on the `{orgId}/{projectId}` shape since PR 0
//     so this is safe; orgScope-on-old-routes is no longer needed.
//
// orgScope may be nil in dev/test setups where the middleware is disabled
// (no RepoRepository wired); the routes degrade to no-op middleware while
// keeping their handler bindings.
func registerRepoOnlyRoutes(
	mux *http.ServeMux,
	rc controllers.RepoController,
	gc controllers.GitOpsController,
	ic controllers.IssueController,
	bc controllers.BranchController,
	pc controllers.PullRequestController,
	wc controllers.WebhookRegistrationController,
	ac controllers.ArtifactController,
	orgScope func(http.Handler) http.Handler,
) {
	wrap := func(h http.HandlerFunc) http.Handler {
		if orgScope == nil {
			return h
		}
		return orgScope(h)
	}

	// CreateRepo is the one route without a path projectId — orgId travels
	// in the body. Idempotent on (orgId, projectId).
	mux.HandleFunc("POST /api/v1/repos", rc.CreateRepo)

	// ---- Repo lifecycle ----
	mux.Handle("GET /api/v1/repos/{orgId}/{projectId}", wrap(rc.GetRepo))
	mux.Handle("DELETE /api/v1/repos/{orgId}/{projectId}", wrap(rc.DeleteRepo))

	// ---- Git ops ----
	mux.Handle("POST /api/v1/repos/{orgId}/{projectId}/commit", wrap(gc.Commit))
	mux.Handle("POST /api/v1/repos/{orgId}/{projectId}/push", wrap(gc.Push))
	mux.Handle("POST /api/v1/repos/{orgId}/{projectId}/pull", wrap(gc.Pull))
	mux.Handle("GET /api/v1/repos/{orgId}/{projectId}/status", wrap(gc.Status))

	// ---- Tag primitives (still used by build watcher etc.) ----
	mux.Handle("POST /api/v1/repos/{orgId}/{projectId}/tags", wrap(gc.CreateTag))
	mux.Handle("GET /api/v1/repos/{orgId}/{projectId}/tags", wrap(gc.ListTags))
	mux.Handle("GET /api/v1/repos/{orgId}/{projectId}/tags/{tag}/file", wrap(gc.GetFileAtTag))

	// ---- Issues ----
	mux.Handle("POST /api/v1/repos/{orgId}/{projectId}/issues", wrap(ic.CreateIssue))
	mux.Handle("GET /api/v1/repos/{orgId}/{projectId}/issues", wrap(ic.ListIssues))
	mux.Handle("POST /api/v1/repos/{orgId}/{projectId}/issues/{number}/close", wrap(ic.CloseIssue))
	mux.Handle("POST /api/v1/repos/{orgId}/{projectId}/issues/{number}/comments", wrap(ic.CommentIssue))
	mux.Handle("PATCH /api/v1/repos/{orgId}/{projectId}/issues/{number}/body", wrap(ic.EditIssueBody))

	// ---- Branch / PR / webhook ----
	mux.Handle("POST /api/v1/repos/{orgId}/{projectId}/branches", wrap(bc.CreateBranch))
	mux.Handle("POST /api/v1/repos/{orgId}/{projectId}/branches/seed-commit", wrap(bc.SeedCommit))
	mux.Handle("POST /api/v1/repos/{orgId}/{projectId}/pulls", wrap(pc.CreateDraftPR))
	mux.Handle("POST /api/v1/repos/{orgId}/{projectId}/webhooks", wrap(wc.Register))
	mux.Handle("DELETE /api/v1/repos/{orgId}/{projectId}/webhooks", wrap(wc.Deregister))

	// ---- Artifacts: spec ----
	if ac != nil {
		mux.Handle("GET /api/v1/repos/{orgId}/{projectId}/artifacts/spec", wrap(ac.GetSpec))
		mux.Handle("PUT /api/v1/repos/{orgId}/{projectId}/artifacts/spec", wrap(ac.PutSpec))
		mux.Handle("POST /api/v1/repos/{orgId}/{projectId}/artifacts/spec/save", wrap(ac.SaveSpec))
		mux.Handle("POST /api/v1/repos/{orgId}/{projectId}/artifacts/spec/discard", wrap(ac.DiscardSpec))
		mux.Handle("GET /api/v1/repos/{orgId}/{projectId}/artifacts/spec/versions", wrap(ac.ListSpecVersions))
		mux.Handle("GET /api/v1/repos/{orgId}/{projectId}/artifacts/spec/versions/{version}", wrap(ac.GetSpecVersion))

		// ---- Artifacts: design ----
		mux.Handle("GET /api/v1/repos/{orgId}/{projectId}/artifacts/design", wrap(ac.GetDesign))
		mux.Handle("PUT /api/v1/repos/{orgId}/{projectId}/artifacts/design", wrap(ac.PutDesign))
		mux.Handle("POST /api/v1/repos/{orgId}/{projectId}/artifacts/design/save", wrap(ac.SaveDesign))
		mux.Handle("POST /api/v1/repos/{orgId}/{projectId}/artifacts/design/discard", wrap(ac.DiscardDesign))
		mux.Handle("GET /api/v1/repos/{orgId}/{projectId}/artifacts/design/versions", wrap(ac.ListDesignVersions))
		mux.Handle("GET /api/v1/repos/{orgId}/{projectId}/artifacts/design/versions/{version}", wrap(ac.GetDesignVersion))

		// ---- Artifacts: wireframes (no version stream — committed by spec save) ----
		mux.Handle("GET /api/v1/repos/{orgId}/{projectId}/artifacts/wireframes", wrap(ac.ListWireframes))
		mux.Handle("GET /api/v1/repos/{orgId}/{projectId}/artifacts/wireframes/{name}", wrap(ac.GetWireframe))
		mux.Handle("PUT /api/v1/repos/{orgId}/{projectId}/artifacts/wireframes/{name}", wrap(ac.PutWireframe))
	}
}
