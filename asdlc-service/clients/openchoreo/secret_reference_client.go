package openchoreo

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	"github.com/wso2/asdlc/asdlc-service/clients/openchoreo/gen"
	"github.com/wso2/asdlc/asdlc-service/clients/secretmanagersvc"
)

//go:generate go run github.com/matryer/moq@v0.7.1 -rm -fmt goimports -pkg mocks -out mocks/secret_reference_client_mock.go . SecretReferenceClient

// SecretReferenceClient is the typed wrapper around the OC
// SecretReference CRUD surface. Mirrors agent-manager's
// `client.OpenChoreoClient` SecretReference methods 1-for-1 so callers
// that come from that codebase can be ported with only an import swap.
//
// Implements `secretmanagersvc.OpenChoreoSecretReferenceClient` so the
// secret-management high-level client (WS0.2) can drive SR upsert /
// delete without depending on the gen layer.
type SecretReferenceClient interface {
	CreateSecretReference(ctx context.Context, orgNS string, req secretmanagersvc.CreateSecretReferenceRequest) (*secretmanagersvc.SecretReference, error)
	GetSecretReference(ctx context.Context, orgNS, name string) (*secretmanagersvc.SecretReference, error)
	UpdateSecretReference(ctx context.Context, orgNS, name string, req secretmanagersvc.CreateSecretReferenceRequest) (*secretmanagersvc.SecretReference, error)
	DeleteSecretReference(ctx context.Context, orgNS, name string) error

	// ListSecretReferences returns every SR in the namespace, optionally
	// filtered by `componentName` (when non-empty, matches the
	// `openchoreo.dev/component-name` label). Used by the cleanup path
	// in CredentialService.OrgDisconnect.
	ListSecretReferences(ctx context.Context, orgNS, componentName string) ([]*secretmanagersvc.SecretReference, error)
}

type secretReferenceClient struct {
	oc *gen.ClientWithResponses
}

func NewSecretReferenceClient(cfg Config) SecretReferenceClient {
	oc, err := newGenClient(cfg)
	if err != nil {
		panic(fmt.Errorf("init openchoreo secret-reference client: %w", err))
	}
	return &secretReferenceClient{oc: oc}
}

func (c *secretReferenceClient) CreateSecretReference(ctx context.Context, orgNS string, req secretmanagersvc.CreateSecretReferenceRequest) (*secretmanagersvc.SecretReference, error) {
	body := buildSecretReferenceBody(req)
	resp, err := c.oc.CreateSecretReferenceWithResponse(ctx, orgNS, body)
	if err != nil {
		return nil, fmt.Errorf("create secret reference: %w", err)
	}
	if resp.StatusCode() != http.StatusCreated || resp.JSON201 == nil {
		hostErr := handleErrorResponse(resp.StatusCode(), ErrorResponses{
			JSON400: resp.JSON400,
			JSON401: resp.JSON401,
			JSON403: resp.JSON403,
			JSON409: resp.JSON409,
			JSON500: resp.JSON500,
		})
		// Surface OC's 409 as the secretmanagersvc.ErrConflict the
		// high-level client expects — keeps the upsert race-handling
		// branch typed.
		if errors.Is(hostErr, ErrConflict) {
			return nil, fmt.Errorf("%w: %s", secretmanagersvc.ErrConflict, hostErr.Error())
		}
		return nil, hostErr
	}
	return secretReferenceToInfo(resp.JSON201), nil
}

func (c *secretReferenceClient) GetSecretReference(ctx context.Context, orgNS, name string) (*secretmanagersvc.SecretReference, error) {
	resp, err := c.oc.GetSecretReferenceWithResponse(ctx, orgNS, name)
	if err != nil {
		return nil, fmt.Errorf("get secret reference: %w", err)
	}
	if resp.StatusCode() != http.StatusOK || resp.JSON200 == nil {
		hostErr := handleErrorResponse(resp.StatusCode(), ErrorResponses{
			JSON401: resp.JSON401,
			JSON403: resp.JSON403,
			JSON404: resp.JSON404,
			JSON500: resp.JSON500,
		})
		if errors.Is(hostErr, ErrNotFound) {
			return nil, fmt.Errorf("%w: %s", secretmanagersvc.ErrNotFound, hostErr.Error())
		}
		return nil, hostErr
	}
	return secretReferenceToInfo(resp.JSON200), nil
}

func (c *secretReferenceClient) UpdateSecretReference(ctx context.Context, orgNS, name string, req secretmanagersvc.CreateSecretReferenceRequest) (*secretmanagersvc.SecretReference, error) {
	body := buildSecretReferenceBody(req)
	resp, err := c.oc.UpdateSecretReferenceWithResponse(ctx, orgNS, name, body)
	if err != nil {
		return nil, fmt.Errorf("update secret reference: %w", err)
	}
	if resp.StatusCode() != http.StatusOK || resp.JSON200 == nil {
		return nil, handleErrorResponse(resp.StatusCode(), ErrorResponses{
			JSON400: resp.JSON400,
			JSON401: resp.JSON401,
			JSON403: resp.JSON403,
			JSON404: resp.JSON404,
			JSON500: resp.JSON500,
		})
	}
	return secretReferenceToInfo(resp.JSON200), nil
}

func (c *secretReferenceClient) DeleteSecretReference(ctx context.Context, orgNS, name string) error {
	resp, err := c.oc.DeleteSecretReferenceWithResponse(ctx, orgNS, name)
	if err != nil {
		return fmt.Errorf("delete secret reference: %w", err)
	}
	switch resp.StatusCode() {
	case http.StatusOK, http.StatusNoContent:
		return nil
	}
	hostErr := handleErrorResponse(resp.StatusCode(), ErrorResponses{
		JSON401: resp.JSON401,
		JSON403: resp.JSON403,
		JSON404: resp.JSON404,
		JSON500: resp.JSON500,
	})
	if errors.Is(hostErr, ErrNotFound) {
		return fmt.Errorf("%w: %s", secretmanagersvc.ErrNotFound, hostErr.Error())
	}
	return hostErr
}

func (c *secretReferenceClient) ListSecretReferences(ctx context.Context, orgNS, componentName string) ([]*secretmanagersvc.SecretReference, error) {
	resp, err := c.oc.ListSecretReferencesWithResponse(ctx, orgNS, nil)
	if err != nil {
		return nil, fmt.Errorf("list secret references: %w", err)
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
	out := make([]*secretmanagersvc.SecretReference, 0, len(resp.JSON200.Items))
	for i := range resp.JSON200.Items {
		if componentName != "" {
			labels := resp.JSON200.Items[i].Metadata.Labels
			if labels == nil || (*labels)[string(LabelKeyComponent)] != componentName {
				continue
			}
		}
		out = append(out, secretReferenceToInfo(&resp.JSON200.Items[i]))
	}
	return out, nil
}

// buildSecretReferenceBody is shared by Create + Update. The OC
// CreateSecretReferenceJSONRequestBody and UpdateSecretReferenceJSONRequestBody
// are aliases of the same SecretReference type, so one builder serves
// both paths.
func buildSecretReferenceBody(req secretmanagersvc.CreateSecretReferenceRequest) gen.SecretReference {
	data := make([]gen.SecretDataSource, len(req.SecretKeys))
	for i, k := range req.SecretKeys {
		key := k
		data[i] = gen.SecretDataSource{
			SecretKey: k,
			RemoteRef: gen.RemoteReference{
				Key:      req.KVPath,
				Property: &key,
			},
		}
	}
	labels := map[string]string{
		string(LabelKeyProjectName): req.ProjectName,
		string(LabelKeyComponent):   req.ComponentName,
	}
	spec := &gen.SecretReferenceSpec{
		Data: data,
		Template: gen.SecretTemplate{
			Metadata: &struct {
				Annotations *map[string]string `json:"annotations,omitempty"`
				Labels      *map[string]string `json:"labels,omitempty"`
			}{
				Labels: &labels,
			},
		},
	}
	if req.RefreshInterval != "" {
		ri := req.RefreshInterval
		spec.RefreshInterval = &ri
	}
	return gen.SecretReference{
		Metadata: gen.ObjectMeta{
			Name:   req.Name,
			Labels: &labels,
		},
		Spec: spec,
	}
}

func secretReferenceToInfo(sr *gen.SecretReference) *secretmanagersvc.SecretReference {
	if sr == nil {
		return nil
	}
	ns := ""
	if sr.Metadata.Namespace != nil {
		ns = *sr.Metadata.Namespace
	}
	return &secretmanagersvc.SecretReference{
		Namespace: ns,
		Name:      sr.Metadata.Name,
	}
}
