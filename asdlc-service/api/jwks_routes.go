package api

import (
	"net/http"

	"github.com/wso2/asdlc/asdlc-service/controllers"
)

// registerJWKSRoute registers the JWKS endpoint on the provided mux. MUST
// be registered on the OUTER mux, not the JWT-gated apiMux: verifiers fetch
// the JWKS unauthenticated, before any token verification.
func registerJWKSRoute(mux *http.ServeMux, ctrl controllers.JWKSController) {
	mux.HandleFunc("GET /auth/external/jwks.json", ctrl.GetJWKS)
}
