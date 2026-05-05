package credentials

// TaskBearerClaims is the per-task bearer payload. The BFF signs an RS256
// JWT carrying these fields at dispatch time; the agent's workspace
// credential helper presents it to /api/v1/credentials/refresh.
//
// Verification (RS256, JWKS-backed) is performed by the jwtassertion
// middleware before this struct is materialised; downstream handlers
// receive a fully-validated value via middleware.TaskBearerClaims.
type TaskBearerClaims struct {
	TaskID    string `json:"taskId"`
	OcOrgID   string `json:"ocOrgId"`
	IssuedAt  int64  `json:"iat"`
	ExpiresAt int64  `json:"exp"`
}
