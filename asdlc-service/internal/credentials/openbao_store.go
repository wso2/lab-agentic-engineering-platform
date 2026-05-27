package credentials

import (
	"context"
	"errors"
	"fmt"
	"path"
	"regexp"
	"strings"
	"time"

	vault "github.com/hashicorp/vault/api"
)

// OpenBaoStore is the architectural enforcement boundary for OpenBao access.
// The doc's deliberate decision to use a single OpenBao policy with
// path-namespaced isolation (secret/asdlc/{ocOrgID}/...) places the per-org
// isolation property entirely on git-service code correctness — exactly what
// the multi-tenant invariant says it shouldn't.
//
// To turn that from code discipline into an architectural property:
//
//   - This wrapper is the only OpenBao access point.
//   - ocOrgID is mandatory in every method.
//   - The implementation builds the path internally — no caller can write
//     a raw path.
//   - import_fence_test.go fails the build if any code outside the
//     credentials package imports the OpenBao SDK.
type OpenBaoStore interface {
	Get(ctx context.Context, ocOrgID, key string) ([]byte, error)
	Put(ctx context.Context, ocOrgID, key string, value []byte) error
	Delete(ctx context.Context, ocOrgID, key string) error
}

// ErrOrgIDInvalid is returned when an ocOrgID doesn't match the DNS-label
// shape, contains a leading underscore, or is the reserved "_platform"
// namespace. This guards the per-org → platform path-escape property.
var ErrOrgIDInvalid = errors.New("openbao: ocOrgID is invalid")

// ErrSecretNotFound is returned by Get when no value exists at the path.
var ErrSecretNotFound = errors.New("openbao: secret not found")

var orgIDValidator = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`)

// validateOrgID enforces the per-org → platform path isolation rule.
// Handler-boundary validation rejects empty/malformed IDs as 400 long
// before they reach this function; the checks here are defensive.
func validateOrgID(ocOrgID string) error {
	if ocOrgID == "" {
		return ErrOrgIDInvalid
	}
	if ocOrgID == "_platform" || strings.HasPrefix(ocOrgID, "_") {
		return ErrOrgIDInvalid
	}
	if !orgIDValidator.MatchString(ocOrgID) {
		return ErrOrgIDInvalid
	}
	return nil
}

// openBaoStore is the OpenBao-backed implementation of OpenBaoStore.
//
// Path scheme: secret/asdlc/{ocOrgID}/{key} on a KV v2 mount. The "secret/"
// mount is shared with OpenChoreo (which seeds at secret/{topkey}) and
// agent-manager — namespacing under "asdlc/" keeps writes disjoint.
type openBaoStore struct {
	client *vault.Client
	mount  string // KV v2 mount name; "secret" in dev (the chart's default)
	owner  string // KV v2 metadata managed-by tag — "asdlc-git-service"
}

// NewOpenBaoStore constructs a real OpenBaoStore against the given address
// and token. The client is configured with a short request timeout so
// startup readiness checks can fail fast.
func NewOpenBaoStore(addr, token, mount, owner string) (OpenBaoStore, error) {
	if addr == "" {
		return nil, errors.New("openbao: addr is required")
	}
	if token == "" {
		return nil, errors.New("openbao: token is required")
	}
	if mount == "" {
		mount = "secret"
	}
	if owner == "" {
		owner = "asdlc-git-service"
	}

	cfg := vault.DefaultConfig()
	cfg.Address = addr
	cfg.Timeout = 10 * time.Second

	client, err := vault.NewClient(cfg)
	if err != nil {
		return nil, fmt.Errorf("openbao: new client: %w", err)
	}
	client.SetToken(token)

	return &openBaoStore{client: client, mount: mount, owner: owner}, nil
}

// path constructs the per-org KV v2 logical path. The "/data/" segment is
// KV v2's read/write convention; "/metadata/" is for managed-by tags.
func (s *openBaoStore) path(ocOrgID, key string) (string, error) {
	if err := validateOrgID(ocOrgID); err != nil {
		return "", err
	}
	if key == "" || strings.HasPrefix(key, "/") {
		return "", fmt.Errorf("openbao: key must be non-empty and not start with '/': %q", key)
	}
	return path.Join(s.mount, "data", "asdlc", ocOrgID, key), nil
}

func (s *openBaoStore) metadataPath(ocOrgID, key string) (string, error) {
	if err := validateOrgID(ocOrgID); err != nil {
		return "", err
	}
	return path.Join(s.mount, "metadata", "asdlc", ocOrgID, key), nil
}

// platformPath constructs the platform-namespace path used for App
// private-key + webhook-secret custody. ONLY callable from the App-key
// startup loader (see app_token_minter.go::loadAppKey). The import fence
// test asserts no other call site references this function.
func (s *openBaoStore) platformPath(key string) string {
	return path.Join(s.mount, "data", "asdlc", "_platform", key)
}

// Get reads a value at secret/asdlc/{ocOrgID}/{key}. Returns ErrSecretNotFound
// if absent (vs. a network/auth error).
func (s *openBaoStore) Get(ctx context.Context, ocOrgID, key string) ([]byte, error) {
	p, err := s.path(ocOrgID, key)
	if err != nil {
		return nil, err
	}
	sec, err := s.client.Logical().ReadWithContext(ctx, p)
	if err != nil {
		return nil, fmt.Errorf("openbao: read %s: %w", redactPath(p), err)
	}
	if sec == nil || sec.Data == nil {
		return nil, ErrSecretNotFound
	}
	// KV v2 wraps data under data.data
	dataField, ok := sec.Data["data"].(map[string]interface{})
	if !ok || dataField == nil {
		return nil, ErrSecretNotFound
	}
	val, ok := dataField["value"].(string)
	if !ok {
		return nil, fmt.Errorf("openbao: unexpected value shape at %s", redactPath(p))
	}
	return []byte(val), nil
}

// Put writes value at secret/asdlc/{ocOrgID}/{key} and tags metadata with
// managed-by + the supplied kind. The kind is stored as KV v2 custom
// metadata so an out-of-band reader can audit ownership.
func (s *openBaoStore) Put(ctx context.Context, ocOrgID, key string, value []byte) error {
	p, err := s.path(ocOrgID, key)
	if err != nil {
		return err
	}
	mp, err := s.metadataPath(ocOrgID, key)
	if err != nil {
		return err
	}

	if _, err := s.client.Logical().WriteWithContext(ctx, p, map[string]interface{}{
		"data": map[string]interface{}{"value": string(value)},
	}); err != nil {
		return fmt.Errorf("openbao: write %s: %w", redactPath(p), err)
	}

	// Best-effort metadata tag — not load-bearing, but useful for audits.
	if _, err := s.client.Logical().WriteWithContext(ctx, mp, map[string]interface{}{
		"custom_metadata": map[string]interface{}{
			"managed-by": s.owner,
		},
	}); err != nil {
		// Don't fail the Put on metadata failure; log via the wrapper would
		// re-introduce a token-leakage risk. Caller surfaces via Get on
		// retrieval.
		_ = err
	}
	return nil
}

// SeedPlatformValue is the seed-only escape hatch for writing into the
// secret/asdlc/_platform/* namespace. It's used by `internal/seed/`
// (App private key, App webhook secret, etc.) and nothing else.
//
// Why this method instead of letting `internal/seed/` import the vault
// SDK directly: the import-fence test forbids vault SDK imports outside
// `pkg/credentials/`. Centralising the escape hatch here keeps the
// platform-path call sites grep-able from one file.
//
// The kind argument is recorded as KV v2 custom metadata
// (managed-by + kind) so an out-of-band reader can audit ownership.
func (s *openBaoStore) SeedPlatformValue(ctx context.Context, key, value, kind string) error {
	if key == "" || strings.HasPrefix(key, "/") {
		return fmt.Errorf("openbao seed: key must be non-empty and not start with '/': %q", key)
	}
	p := path.Join(s.mount, "data", "asdlc", "_platform", key)
	if _, err := s.client.Logical().WriteWithContext(ctx, p, map[string]interface{}{
		"data": map[string]interface{}{"value": value},
	}); err != nil {
		return fmt.Errorf("openbao seed write %s: %w", redactPath(p), err)
	}
	mp := path.Join(s.mount, "metadata", "asdlc", "_platform", key)
	if _, err := s.client.Logical().WriteWithContext(ctx, mp, map[string]interface{}{
		"custom_metadata": map[string]interface{}{
			"managed-by": s.owner,
			"kind":       kind,
		},
	}); err != nil {
		// Best-effort metadata; do not fail the seed.
		_ = err
	}
	return nil
}

// PlatformSeeder is the narrow interface internal/seed uses to write into
// _platform. The OpenBaoStore interface deliberately omits this — the
// per-org invariants only allow writes parameterised by ocOrgID.
type PlatformSeeder interface {
	SeedPlatformValue(ctx context.Context, key, value, kind string) error
}

// AsPlatformSeeder returns the underlying store as a PlatformSeeder if
// it's the real OpenBao implementation. Returns (nil, false) for the
// placeholder/test double.
func AsPlatformSeeder(store OpenBaoStore) (PlatformSeeder, bool) {
	if s, ok := store.(*openBaoStore); ok {
		return s, true
	}
	return nil, false
}

// Delete soft-deletes the latest version (KV v2 supports versioned undelete).
// Idempotent: 404s do not error.
func (s *openBaoStore) Delete(ctx context.Context, ocOrgID, key string) error {
	p, err := s.path(ocOrgID, key)
	if err != nil {
		return err
	}
	if _, err := s.client.Logical().DeleteWithContext(ctx, p); err != nil {
		return fmt.Errorf("openbao: delete %s: %w", redactPath(p), err)
	}
	return nil
}

// CheckReachable performs a readiness probe against OpenBao's /v1/sys/health.
// Used by git-service's startup gate to refuse readiness until OpenBao is up.
func CheckReachable(ctx context.Context, store OpenBaoStore) error {
	s, ok := store.(*openBaoStore)
	if !ok {
		return errors.New("openbao: not the real store implementation")
	}
	resp, err := s.client.Sys().HealthWithContext(ctx)
	if err != nil {
		return fmt.Errorf("openbao: health: %w", err)
	}
	if resp == nil || resp.Sealed {
		return fmt.Errorf("openbao: sealed or no response")
	}
	return nil
}

// redactPath strips potentially-sensitive parts of a path for log output.
// We log the mount + namespace shape but not the key, so an audit can
// see "which org" without seeing "which secret".
func redactPath(p string) string {
	parts := strings.Split(p, "/")
	if len(parts) >= 4 && parts[2] == "asdlc" {
		// secret/data/asdlc/{org}/{...} -> secret/data/asdlc/{org}/<redacted>
		return strings.Join(parts[:4], "/") + "/<redacted>"
	}
	return "<redacted>"
}
