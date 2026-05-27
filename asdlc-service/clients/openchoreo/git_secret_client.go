package openchoreo

import (
	"context"
	"fmt"
	"net/http"

	"github.com/wso2/asdlc/asdlc-service/clients/openchoreo/gen"
)

//go:generate go run github.com/matryer/moq@v0.7.1 -rm -fmt goimports -pkg mocks -out mocks/git_secret_client_mock.go . GitSecretClient

// Default workflow plane targeting for GitSecret. The remote-worker NS
// in WS1+ doesn't run Argo so the per-org GitSecret applied here only
// matters for the legacy `dockerfile-builder` ClusterWorkflow that
// keeps running on the WP. Callers needing a different target can use
// CreateGitSecretRequest fields below.
const (
	DefaultGitSecretWorkflowPlaneKind = gen.CreateGitSecretRequestWorkflowPlaneKindClusterWorkflowPlane
	DefaultGitSecretWorkflowPlaneName = "default"
)

// GitSecretSecretType mirrors gen.CreateGitSecretRequestSecretType but
// in the client package so callers don't import gen. Values are the
// same wire strings the OC API expects.
type GitSecretSecretType string

const (
	GitSecretBasicAuth GitSecretSecretType = "basic-auth"
	GitSecretSSHAuth   GitSecretSecretType = "ssh-auth"
)

// CreateGitSecretRequest is the package-typed wire shape for GitSecret
// creation. Keeps consumers in services/ free of the gen layer.
type CreateGitSecretRequest struct {
	Name              string
	SecretType        GitSecretSecretType
	Username          string
	Token             string
	SSHKey            string
	SSHKeyID          string
	WorkflowPlaneKind gen.CreateGitSecretRequestWorkflowPlaneKind
	WorkflowPlaneName string
}

// GitSecretInfo is the typed projection of the GitSecret response.
type GitSecretInfo struct {
	Name              string
	Namespace         string
	WorkflowPlaneKind string
	WorkflowPlaneName string
}

//go:generate go run github.com/matryer/moq@v0.7.1 -rm -fmt goimports -pkg mocks -out mocks/git_secret_client_mock.go . GitSecretClient

// GitSecretClient wraps the OC GitSecret CRUD surface. Used by the
// build-credentials path to land per-org GitSecrets on the
// WorkflowPlane (today) and the new remote-worker NS (WS2 onwards).
type GitSecretClient interface {
	CreateGitSecret(ctx context.Context, orgNS string, req CreateGitSecretRequest) (*GitSecretInfo, error)
	ListGitSecrets(ctx context.Context, orgNS string) ([]*GitSecretInfo, error)
	DeleteGitSecret(ctx context.Context, orgNS, name string) error
}

type gitSecretClient struct {
	oc *gen.ClientWithResponses
}

func NewGitSecretClient(cfg Config) GitSecretClient {
	oc, err := newGenClient(cfg)
	if err != nil {
		panic(fmt.Errorf("init openchoreo git-secret client: %w", err))
	}
	return &gitSecretClient{oc: oc}
}

func (c *gitSecretClient) CreateGitSecret(ctx context.Context, orgNS string, req CreateGitSecretRequest) (*GitSecretInfo, error) {
	body := gen.CreateGitSecretJSONRequestBody{
		SecretName:        req.Name,
		SecretType:        gen.CreateGitSecretRequestSecretType(req.SecretType),
		WorkflowPlaneKind: req.WorkflowPlaneKind,
		WorkflowPlaneName: req.WorkflowPlaneName,
	}
	if body.WorkflowPlaneKind == "" {
		body.WorkflowPlaneKind = DefaultGitSecretWorkflowPlaneKind
	}
	if body.WorkflowPlaneName == "" {
		body.WorkflowPlaneName = DefaultGitSecretWorkflowPlaneName
	}
	if req.Token != "" {
		t := req.Token
		body.Token = &t
	}
	if req.Username != "" {
		u := req.Username
		body.Username = &u
	}
	if req.SSHKey != "" {
		k := req.SSHKey
		body.SshKey = &k
	}
	if req.SSHKeyID != "" {
		id := req.SSHKeyID
		body.SshKeyId = &id
	}
	resp, err := c.oc.CreateGitSecretWithResponse(ctx, orgNS, body)
	if err != nil {
		return nil, fmt.Errorf("create git secret: %w", err)
	}
	if resp.StatusCode() != http.StatusCreated || resp.JSON201 == nil {
		return nil, handleErrorResponse(resp.StatusCode(), ErrorResponses{
			JSON400: resp.JSON400,
			JSON401: resp.JSON401,
			JSON403: resp.JSON403,
			JSON409: resp.JSON409,
			JSON500: resp.JSON500,
		})
	}
	return gitSecretToInfo(resp.JSON201), nil
}

func (c *gitSecretClient) ListGitSecrets(ctx context.Context, orgNS string) ([]*GitSecretInfo, error) {
	resp, err := c.oc.ListGitSecretsWithResponse(ctx, orgNS)
	if err != nil {
		return nil, fmt.Errorf("list git secrets: %w", err)
	}
	if resp.StatusCode() != http.StatusOK {
		return nil, handleErrorResponse(resp.StatusCode(), ErrorResponses{
			JSON401: resp.JSON401,
			JSON403: resp.JSON403,
			JSON500: resp.JSON500,
		})
	}
	if resp.JSON200 == nil || len(resp.JSON200.Items) == 0 {
		return nil, nil
	}
	out := make([]*GitSecretInfo, len(resp.JSON200.Items))
	for i := range resp.JSON200.Items {
		out[i] = gitSecretToInfo(&resp.JSON200.Items[i])
	}
	return out, nil
}

func (c *gitSecretClient) DeleteGitSecret(ctx context.Context, orgNS, name string) error {
	resp, err := c.oc.DeleteGitSecretWithResponse(ctx, orgNS, name)
	if err != nil {
		return fmt.Errorf("delete git secret: %w", err)
	}
	switch resp.StatusCode() {
	case http.StatusOK, http.StatusNoContent:
		return nil
	}
	return handleErrorResponse(resp.StatusCode(), ErrorResponses{
		JSON401: resp.JSON401,
		JSON403: resp.JSON403,
		JSON404: resp.JSON404,
		JSON500: resp.JSON500,
	})
}

func gitSecretToInfo(gs *gen.GitSecretResponse) *GitSecretInfo {
	if gs == nil {
		return nil
	}
	out := &GitSecretInfo{}
	if gs.Name != nil {
		out.Name = *gs.Name
	}
	if gs.Namespace != nil {
		out.Namespace = *gs.Namespace
	}
	if gs.WorkflowPlaneKind != nil {
		out.WorkflowPlaneKind = *gs.WorkflowPlaneKind
	}
	if gs.WorkflowPlaneName != nil {
		out.WorkflowPlaneName = *gs.WorkflowPlaneName
	}
	return out
}
