package api

import (
	"net/http"

	"github.com/wso2/asdlc/asdlc-service/controllers"
)

// registerOrgGitHubRoutes wires the per-org GitHub integration surface.
//
// Org-scoped routes mount under the existing /api/v1/organizations/{orgHandle}
// prefix and inherit the JWT middleware that protects every other org-scoped
// route. The connect callback is unscoped — GitHub's single configured
// callback URL has no orgHandle to thread, so the JWT-carried `state`
// parameter is the org-binding signal.
func registerOrgGitHubRoutes(mux *http.ServeMux, c controllers.OrgGitHubController) {
	mux.HandleFunc("POST /api/v1/organizations/{orgHandle}/github/connect/start", c.StartConnect)
	mux.HandleFunc("POST /api/v1/organizations/{orgHandle}/github/pat", c.ConnectPAT)
	mux.HandleFunc("GET /api/v1/organizations/{orgHandle}/github", c.GetStatus)
	mux.HandleFunc("DELETE /api/v1/organizations/{orgHandle}/github", c.Disconnect)
}

// registerConnectCallbackRoute mounts the App-mode connect callback
// OUTSIDE the JWT-protected mux. GitHub redirects the user's browser
// here with the OAuth code or post-install installation_id; we verify
// the connect-state JWT (issued by StartConnect) instead of the console
// JWT.
//
// The deprecated old callback path /api/v1/github/app/callback returns
// 410 Gone — any in-flight install URLs from prior PR D-followup setup
// configurations fail loudly instead of 404'ing silently.
func registerConnectCallbackRoute(mux *http.ServeMux, c controllers.OrgGitHubController) {
	mux.HandleFunc("GET /api/v1/github/connect/callback", c.HandleConnectCallback)
	mux.HandleFunc("GET /api/v1/github/app/callback", func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "endpoint moved to /api/v1/github/connect/callback; update GitHub App setup + callback URLs", http.StatusGone)
	})
}
