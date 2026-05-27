// Copyright (c) 2026, WSO2 LLC. (https://www.wso2.com).
//
// WSO2 LLC. licenses this file to you under the Apache License,
// Version 2.0 (the "License"); you may not use this file except
// in compliance with the License.
// You may obtain a copy of the License at
//
// http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing,
// software distributed under the License is distributed on an
// "AS IS" BASIS, WITHOUT WARRANTIES OR CONDITIONS OF ANY
// KIND, either express or implied. See the License for the
// specific language governing permissions and limitations
// under the License.

// Package secretmanagersvc is the asdlc-service port of agent-manager's
// secretmanagersvc client (WS0.2). The provider/registry/types are
// verbatim; client.go is forked along two axes:
//
//  1. SecretLocation is reshaped from agent-manager's
//     `{org, project, agent, environment, config, entity, secretKey}`
//     to the workflows plan's `{org, project, task, entity, secretKey}`.
//     The collapse is deliberate — app-factory has no agent or env-set
//     concept at the secret layer; a coding-agent task is the smallest
//     ownership unit and per-env scoping happens at the OC
//     SecretReference level, not in the KV path.
//
//  2. OpenChoreoSecretReferenceClient is a minimal local interface
//     instead of the full `openchoreosvc.OpenChoreoClient`. The typed
//     SecretReference wrapper lands in WS0.3; until then this file
//     uses the small surface needed for upsertSecretReference /
//     DeleteSecret.
//
// Per ADR-0002 this package does NOT ship an openbao provider — local
// is served by the actual SM-API binary backed by local OpenBao, cloud
// is served by the cloud SM-API. The provider registry is shared with
// `providers/sm_api` (cloud) and `providers/sm_api_local` (local)
// when those land in Phase 1.
package secretmanagersvc

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

const (
	// DefaultManagedBy is the default ownership tag stamped onto every
	// secret written through this client. Cloud SM-API uses it to refuse
	// cross-tenant deletes; OpenBao providers use it for the same reason.
	DefaultManagedBy = "asdlc-app-factory"

	// SecretKeyAPIKey is the conventional key name for single-value
	// secrets stored as `{api-key: "..."}`. Anthropic key + GitHub PAT
	// both use this shape.
	SecretKeyAPIKey = "api-key"
)

// ErrNotFound is returned by OpenChoreoSecretReferenceClient lookups when
// no SecretReference exists for (orgNS, name). Distinct from
// ErrSecretNotFound (which is about the KV value itself).
var ErrNotFound = errors.New("not found")

// ErrConflict is returned by Create when a SecretReference with the same
// (orgNS, name) already exists, signalling a race with another writer.
var ErrConflict = errors.New("conflict")

// SecretLocation identifies where a secret lives in the KV hierarchy.
//
// Compared to agent-manager's SecretLocation, two collapses:
//
//   - {Agent, EnvironmentName, ConfigName} → {TaskID}.
//     A coding-agent task is the smallest ownership unit. The
//     dispatch path mints one ExternalSecret per task per credential;
//     per-env scoping (when needed) happens at the SecretReference
//     `environments:` slice on the OC side, not in the KV path.
//
//   - SecretRefName is derived from `{task, entity}` so two different
//     tasks in the same project don't collide when both consume the
//     same upstream credential.
//
// All segments are validated against `/` to prevent traversal +
// path collisions.
type SecretLocation struct {
	// OrgName is the OC org's namespace (e.g. `wc-<orgUUID8>-<orgHash8>`).
	// Required.
	OrgName string

	// ProjectName is the OC Project handle. Optional — empty for
	// org-scoped credentials (Anthropic platform key, App webhook
	// secret).
	ProjectName string

	// TaskID is the ComponentTask UUID. Optional — empty for
	// project-scoped credentials (per-org Anthropic key,
	// per-org GitHub PAT) and org-scoped credentials.
	TaskID string

	// EntityName is the credential kind handle: `anthropic`,
	// `github-pat`, `runner-thunder-client`, etc. Required —
	// every secret has a kind so two unrelated credentials at the
	// same scope don't collide.
	EntityName string

	// SecretKey is the field name inside the secret payload. Optional —
	// when set the KVPath addresses a single value rather than the
	// whole record.
	SecretKey string
}

func sanitizeSegment(s string) (string, error) {
	s = strings.TrimSpace(s)
	if strings.Contains(s, "/") {
		return "", fmt.Errorf("secret path segment %q contains invalid character '/'", s)
	}
	return s, nil
}

// KVPath builds the KV-store path from non-empty segments.
//
// Shapes (each ends with EntityName, optionally suffixed by SecretKey):
//
//	org/entity                          (org-scoped)
//	org/entity/key                      (org-scoped + key)
//	org/project/entity                  (project-scoped)
//	org/project/entity/key              (project-scoped + key)
//	org/project/task/entity             (task-scoped)
//	org/project/task/entity/key         (task-scoped + key)
func (l SecretLocation) KVPath() (string, error) {
	if strings.TrimSpace(l.OrgName) == "" {
		return "", fmt.Errorf("SecretLocation.OrgName is required")
	}
	if strings.TrimSpace(l.EntityName) == "" {
		return "", fmt.Errorf("SecretLocation.EntityName is required")
	}
	if l.TaskID != "" && l.ProjectName == "" {
		return "", fmt.Errorf("SecretLocation.TaskID requires ProjectName")
	}

	orgSeg, err := sanitizeSegment(l.OrgName)
	if err != nil {
		return "", fmt.Errorf("invalid OrgName: %w", err)
	}
	parts := []string{orgSeg}
	if l.ProjectName != "" {
		seg, err := sanitizeSegment(l.ProjectName)
		if err != nil {
			return "", fmt.Errorf("invalid ProjectName: %w", err)
		}
		parts = append(parts, seg)
	}
	if l.TaskID != "" {
		seg, err := sanitizeSegment(l.TaskID)
		if err != nil {
			return "", fmt.Errorf("invalid TaskID: %w", err)
		}
		parts = append(parts, seg)
	}
	entitySeg, err := sanitizeSegment(l.EntityName)
	if err != nil {
		return "", fmt.Errorf("invalid EntityName: %w", err)
	}
	parts = append(parts, entitySeg)
	if l.SecretKey != "" {
		seg, err := sanitizeSegment(l.SecretKey)
		if err != nil {
			return "", fmt.Errorf("invalid SecretKey: %w", err)
		}
		parts = append(parts, seg)
	}
	return strings.Join(parts, "/"), nil
}

// SecretRefName derives the OC SecretReference name from the location.
// Sanitized to a DNS-label (lowercase, max 63 chars). Includes TaskID
// when set so per-task secrets don't collide with per-project ones.
func (l SecretLocation) SecretRefName() string {
	var name string
	switch {
	case l.TaskID != "":
		name = fmt.Sprintf("%s-%s-secrets",
			sanitizeForK8sName(l.TaskID),
			sanitizeForK8sName(l.EntityName))
	default:
		name = fmt.Sprintf("%s-secrets", sanitizeForK8sName(l.EntityName))
	}
	if len(name) > 63 {
		name = strings.TrimRight(name[:63], "-")
	}
	return name
}

func sanitizeForK8sName(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	var b strings.Builder
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' {
			b.WriteRune(r)
		} else {
			b.WriteRune('-')
		}
	}
	return strings.Trim(b.String(), "-")
}

// ParseKVPath inverts KVPath. Only the six legal shapes above are
// recognized; anything else returns an error so callers don't have to
// guess.
func ParseKVPath(kvPath string) (SecretLocation, error) {
	parts := strings.Split(kvPath, "/")
	switch len(parts) {
	case 2:
		return SecretLocation{OrgName: parts[0], EntityName: parts[1]}, nil
	case 3:
		// Two legal shapes have 3 segments: org/entity/key and
		// org/project/entity. Disambiguate by treating segment-2 as
		// a key if it looks like one (lower-snake, short) — but
		// there's no way to be sure without context, so we treat
		// 3-segment paths as `org/project/entity` and require callers
		// that want `org/entity/key` to use the explicit form
		// `org//entity/key` (rejected by sanitizeSegment) or
		// construct SecretLocation directly. ParseKVPath is
		// best-effort for logging/introspection.
		return SecretLocation{
			OrgName:     parts[0],
			ProjectName: parts[1],
			EntityName:  parts[2],
		}, nil
	case 4:
		return SecretLocation{
			OrgName:     parts[0],
			ProjectName: parts[1],
			EntityName:  parts[2],
			SecretKey:   parts[3],
		}, nil
	case 5:
		return SecretLocation{
			OrgName:     parts[0],
			ProjectName: parts[1],
			TaskID:      parts[2],
			EntityName:  parts[3],
			SecretKey:   parts[4],
		}, nil
	default:
		return SecretLocation{}, fmt.Errorf("unrecognized KV path format: %s (expected 2-5 segments, got %d)", kvPath, len(parts))
	}
}

// OpenChoreoSecretReferenceClient is the small slice of the future OC
// client (WS0.3) that this package needs. It covers SecretReference
// CRUD only; the GitSecret CRUD lives elsewhere. Implementations are
// expected to map ErrNotFound / ErrConflict on the namespaced errors
// they return.
type OpenChoreoSecretReferenceClient interface {
	GetSecretReference(ctx context.Context, orgNS, name string) (*SecretReference, error)
	CreateSecretReference(ctx context.Context, orgNS string, req CreateSecretReferenceRequest) (*SecretReference, error)
	UpdateSecretReference(ctx context.Context, orgNS, name string, req CreateSecretReferenceRequest) (*SecretReference, error)
	DeleteSecretReference(ctx context.Context, orgNS, name string) error
}

// CreateSecretReferenceRequest mirrors the field set the cluster-gateway
// proxy accepts on POST /apis/openchoreo.dev/v1alpha1/.../secretreferences.
// Kept minimal — the typed wrapper in WS0.3 fills in the rest from
// per-call context (refreshInterval, target ClusterSecretStore).
type CreateSecretReferenceRequest struct {
	Namespace       string
	Name            string
	ProjectName     string
	ComponentName   string
	KVPath          string
	SecretKeys      []string
	RefreshInterval string
}

// SecretReference is the projection of the OC SecretReference CR this
// package needs back from the server. Fully-typed CR lands in WS0.3.
type SecretReference struct {
	Namespace string
	Name      string
}

// SecretManagementClient is the asdlc port of the high-level interface.
// Same semantics as agent-manager's:
//   - CreateSecret REPLACES the whole record.
//   - PatchSecret merges (server-side merge-patch).
//   - DeleteSecret is idempotent and managed-by-fenced.
type SecretManagementClient interface {
	CreateSecret(ctx context.Context, location SecretLocation, data map[string]string) (string, error)
	PatchSecret(ctx context.Context, location SecretLocation, data map[string]string, keysToDelete []string) (string, error)
	DeleteSecret(ctx context.Context, location SecretLocation, secretRefName string) error
	GetSecret(ctx context.Context, kvPath string) (*SecretInfo, error)
	GetSecretWithValue(ctx context.Context, kvPath string) (map[string]string, error)
}

type secretManagementClient struct {
	lowLevelClient  SecretsClient
	managedBy       string
	ocClient        OpenChoreoSecretReferenceClient
	refreshInterval string
}

// SecretManagementClientConfig configures NewSecretManagementClientWithConfig.
// OCClient nil → the underlying Provider must implement
// SecretReferenceManager and ManagesSecretReferences()==true (Secret
// Manager API does); otherwise SecretReferences are upserted via OCClient.
type SecretManagementClientConfig struct {
	StoreConfig     *StoreConfig
	Provider        Provider
	OCClient        OpenChoreoSecretReferenceClient
	RefreshInterval string
}

// NewSecretManagementClient constructs a client with the default
// managed-by tag and no OCClient — appropriate for the SM-API provider
// in both local and cloud.
func NewSecretManagementClient(cfg *StoreConfig, provider Provider) (SecretManagementClient, error) {
	return NewSecretManagementClientWithConfig(SecretManagementClientConfig{
		StoreConfig: cfg,
		Provider:    provider,
	})
}

// NewSecretManagementClientWithConfig is the full-control constructor.
func NewSecretManagementClientWithConfig(cfg SecretManagementClientConfig) (SecretManagementClient, error) {
	if cfg.StoreConfig == nil {
		return nil, fmt.Errorf("config is required")
	}
	if cfg.Provider == nil {
		return nil, fmt.Errorf("provider is required")
	}
	lowLevelClient, err := cfg.Provider.NewClient(cfg.StoreConfig)
	if err != nil {
		return nil, fmt.Errorf("failed to create secrets client: %w", err)
	}
	return &secretManagementClient{
		lowLevelClient:  lowLevelClient,
		managedBy:       DefaultManagedBy,
		ocClient:        cfg.OCClient,
		refreshInterval: cfg.RefreshInterval,
	}, nil
}

func (c *secretManagementClient) upsertSecretReference(ctx context.Context, location SecretLocation, kvPath string, secretKeys []string) (string, error) {
	name := location.SecretRefName()
	req := CreateSecretReferenceRequest{
		Namespace:       location.OrgName,
		Name:            name,
		ProjectName:     location.ProjectName,
		ComponentName:   location.EntityName,
		KVPath:          kvPath,
		SecretKeys:      secretKeys,
		RefreshInterval: c.refreshInterval,
	}
	_, getErr := c.ocClient.GetSecretReference(ctx, location.OrgName, name)
	if getErr != nil {
		if !errors.Is(getErr, ErrNotFound) {
			return "", fmt.Errorf("check SecretReference: %w", getErr)
		}
		if _, createErr := c.ocClient.CreateSecretReference(ctx, location.OrgName, req); createErr != nil {
			if errors.Is(createErr, ErrConflict) {
				if _, updateErr := c.ocClient.UpdateSecretReference(ctx, location.OrgName, name, req); updateErr != nil {
					return "", fmt.Errorf("update after create conflict: %w", updateErr)
				}
			} else {
				return "", fmt.Errorf("create SecretReference: %w", createErr)
			}
		}
	} else {
		if _, updateErr := c.ocClient.UpdateSecretReference(ctx, location.OrgName, name, req); updateErr != nil {
			return "", fmt.Errorf("update SecretReference: %w", updateErr)
		}
	}
	return name, nil
}

func (c *secretManagementClient) CreateSecret(ctx context.Context, location SecretLocation, data map[string]string) (string, error) {
	raw, err := json.Marshal(data)
	if err != nil {
		return "", fmt.Errorf("marshal secret data: %w", err)
	}
	metadata := &SecretMetadata{ManagedBy: c.managedBy}
	secretRef, err := c.lowLevelClient.PushSecret(ctx, location, raw, metadata)
	if err != nil {
		return "", fmt.Errorf("upsert secret: %w", err)
	}
	if c.ocClient != nil {
		keys := make([]string, 0, len(data))
		for k := range data {
			keys = append(keys, k)
		}
		return c.upsertSecretReference(ctx, location, secretRef, keys)
	}
	return secretRef, nil
}

func (c *secretManagementClient) PatchSecret(ctx context.Context, location SecretLocation, data map[string]string, keysToDelete []string) (string, error) {
	patch := make(map[string]any, len(data)+len(keysToDelete))
	for k, v := range data {
		patch[k] = v
	}
	for _, k := range keysToDelete {
		patch[k] = nil
	}
	raw, err := json.Marshal(patch)
	if err != nil {
		return "", fmt.Errorf("marshal patch data: %w", err)
	}
	metadata := &SecretMetadata{ManagedBy: c.managedBy}
	secretRef, err := c.lowLevelClient.PatchSecret(ctx, location, raw, metadata)
	if err != nil {
		return "", fmt.Errorf("patch secret: %w", err)
	}
	if c.ocClient != nil {
		info, err := c.lowLevelClient.GetSecret(ctx, location)
		if err != nil {
			return "", fmt.Errorf("get secret keys after patch: %w", err)
		}
		return c.upsertSecretReference(ctx, location, secretRef, info.Keys)
	}
	return secretRef, nil
}

func (c *secretManagementClient) DeleteSecret(ctx context.Context, location SecretLocation, secretRefName string) error {
	metadata := &SecretMetadata{ManagedBy: c.managedBy}
	if err := c.lowLevelClient.DeleteSecret(ctx, location, metadata); err != nil {
		return fmt.Errorf("delete secret: %w", err)
	}
	if c.ocClient != nil {
		if err := c.ocClient.DeleteSecretReference(ctx, location.OrgName, secretRefName); err != nil {
			if !errors.Is(err, ErrNotFound) {
				return fmt.Errorf("delete SecretReference: %w", err)
			}
		}
	}
	return nil
}

func (c *secretManagementClient) GetSecret(ctx context.Context, kvPath string) (*SecretInfo, error) {
	location, err := ParseKVPath(kvPath)
	if err != nil {
		return nil, fmt.Errorf("parse KV path %q: %w", kvPath, err)
	}
	info, err := c.lowLevelClient.GetSecret(ctx, location)
	if err != nil {
		return nil, fmt.Errorf("get secret info at %q: %w", kvPath, err)
	}
	return info, nil
}

func (c *secretManagementClient) GetSecretWithValue(ctx context.Context, kvPath string) (map[string]string, error) {
	location, err := ParseKVPath(kvPath)
	if err != nil {
		return nil, fmt.Errorf("parse KV path %q: %w", kvPath, err)
	}
	raw, err := c.lowLevelClient.GetSecretWithValue(ctx, location)
	if err != nil {
		return nil, fmt.Errorf("get secret at %q: %w", kvPath, err)
	}
	var out map[string]string
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("unmarshal secret data: %w", err)
	}
	return out, nil
}
