// Package secretmanagerapi is the SM-API provider for the asdlc
// secretmanagersvc client (WS1.3). Ported from
// agent-platform/agent-manager-service/secrets/ with the SecretLocation
// shape adapted to the asdlc fork (org/project/task/entity).
package secretmanagerapi

// CreateSecretRequest is the request body for POST /secrets.
type CreateSecretRequest struct {
	Metadata SecretMetadataRequest `json:"metadata"`
	Spec     SecretSpecRequest     `json:"spec"`
}

type SecretMetadataRequest struct {
	Name   string            `json:"name"`
	Labels map[string]string `json:"labels,omitempty"`
}

type SecretSpecRequest struct {
	Data map[string]string `json:"data"`
}

// PatchSecretRequest is the request body for PATCH /secrets/{id}.
type PatchSecretRequest struct {
	Spec PatchSecretSpecRequest `json:"spec"`
}

type PatchSecretSpecRequest struct {
	Data map[string]any `json:"data"`
}

// SecretResponse is the typed response from POST/GET/PATCH /secrets[/...].
type SecretResponse struct {
	Metadata SecretMetadataResponse `json:"metadata"`
	Spec     SecretSpecResponse     `json:"spec"`
}

type SecretMetadataResponse struct {
	ID                string            `json:"id"`
	Name              string            `json:"name"`
	Namespace         string            `json:"namespace"`
	Labels            map[string]string `json:"labels,omitempty"`
	CreationTimestamp string            `json:"creationTimestamp,omitempty"`
}

// SecretSpecResponse — SM-API returns the K8s-shaped `secretReferenceName`
// it created so callers can wire it into ESO/ExternalSecret without a
// second round-trip.
type SecretSpecResponse struct {
	Keys                []string `json:"keys"`
	SecretReferenceName string   `json:"secretReferenceName"`
}

type ListSecretsResponse struct {
	Items []SecretResponse `json:"items"`
}

type ErrorResponse struct {
	Error   string `json:"error,omitempty"`
	Message string `json:"message,omitempty"`
}
