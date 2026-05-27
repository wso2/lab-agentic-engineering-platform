package api

import (
	"net/http"

	"github.com/wso2/asdlc/asdlc-service/controllers"
)

// registerRepoOnlyRoutes wires every Service-JWT-gated repo / git-ops
// endpoint. orgScope may be nil in dev/test setups where the middleware is
// disabled (no RepoRepository wired); the routes degrade to no-op middleware
// while keeping their handler bindings.
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

	// ---- Artifacts: requirements (multi-file directory, tagged v<N>) ----
	if ac != nil {
		mux.Handle("GET /api/v1/repos/{orgId}/{projectId}/artifacts/requirements", wrap(ac.ListRequirements))
		mux.Handle("GET /api/v1/repos/{orgId}/{projectId}/artifacts/requirements/files/{name}", wrap(ac.GetRequirementFile))
		mux.Handle("PUT /api/v1/repos/{orgId}/{projectId}/artifacts/requirements/files/{name}", wrap(ac.PutRequirementFile))
		mux.Handle("DELETE /api/v1/repos/{orgId}/{projectId}/artifacts/requirements/files/{name}", wrap(ac.DeleteRequirementFile))
		mux.Handle("POST /api/v1/repos/{orgId}/{projectId}/artifacts/requirements/save", wrap(ac.SaveRequirements))
		mux.Handle("POST /api/v1/repos/{orgId}/{projectId}/artifacts/requirements/discard", wrap(ac.DiscardRequirements))
		mux.Handle("GET /api/v1/repos/{orgId}/{projectId}/artifacts/requirements/versions", wrap(ac.ListRequirementsVersions))
		mux.Handle("GET /api/v1/repos/{orgId}/{projectId}/artifacts/requirements/versions/{tag}", wrap(ac.GetRequirementsVersion))

		// Requirements snapshots (BFF-only — IDs are `t_<ulid>` for per-turn
		// undo and `sb_<ulid>` for chat session baselines).
		mux.Handle("POST /api/v1/repos/{orgId}/{projectId}/artifacts/requirements/snapshots/{id}", wrap(ac.CaptureRequirementsSnapshot))
		mux.Handle("POST /api/v1/repos/{orgId}/{projectId}/artifacts/requirements/snapshots/{id}/restore", wrap(ac.RestoreRequirementsSnapshot))
		mux.Handle("DELETE /api/v1/repos/{orgId}/{projectId}/artifacts/requirements/snapshots/{id}", wrap(ac.DeleteRequirementsSnapshot))
		mux.Handle("GET /api/v1/repos/{orgId}/{projectId}/artifacts/requirements/snapshots/{id}/files/{name}", wrap(ac.GetRequirementsSnapshotFile))

		// ---- Artifacts: design (multi-file directory, tagged v<N>-<M>) ----
		mux.Handle("GET /api/v1/repos/{orgId}/{projectId}/artifacts/design", wrap(ac.ListDesign))
		mux.Handle("GET /api/v1/repos/{orgId}/{projectId}/artifacts/design/files/{path...}", wrap(ac.GetDesignFile))
		mux.Handle("PUT /api/v1/repos/{orgId}/{projectId}/artifacts/design/files/{path...}", wrap(ac.PutDesignFile))
		mux.Handle("DELETE /api/v1/repos/{orgId}/{projectId}/artifacts/design/files/{path...}", wrap(ac.DeleteDesignFile))
		mux.Handle("DELETE /api/v1/repos/{orgId}/{projectId}/artifacts/design/directories/{path...}", wrap(ac.DeleteDesignDirectory))
		mux.Handle("POST /api/v1/repos/{orgId}/{projectId}/artifacts/design/save", wrap(ac.SaveDesign))
		mux.Handle("POST /api/v1/repos/{orgId}/{projectId}/artifacts/design/discard", wrap(ac.DiscardDesign))
		mux.Handle("GET /api/v1/repos/{orgId}/{projectId}/artifacts/design/versions", wrap(ac.ListDesignVersions))
		mux.Handle("GET /api/v1/repos/{orgId}/{projectId}/artifacts/design/versions/{tag}", wrap(ac.GetDesignVersion))
	}
}
