package api

import (
	"net/http"

	"github.com/wso2/asdlc/asdlc-service/controllers"
)

// registerWebhookRoutes mounts the inbound GitHub webhook receiver. The route
// lives outside the JWT middleware (same pattern the now-removed /mcp/ mount
// used) — webhooks authenticate via HMAC, not JWT.
func registerWebhookRoutes(mux *http.ServeMux, c controllers.WebhookController) {
	mux.HandleFunc("POST /webhooks/github", c.Receive)
}
