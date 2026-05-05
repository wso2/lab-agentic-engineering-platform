// Package validate provides handler-boundary input validators for identifiers
// (orgHandle, projectName, componentName, taskId, ocOrgId) before they reach
// services / repositories / OpenChoreo proxies / shell templates. Validation
// here is conservative — anything that can land in a filesystem path, OpenBao
// path, or k8s resource name has to pass these checks.
package validate

import (
	"errors"
	"regexp"
)

// ErrInvalidSlug is returned when a value fails slug validation.
var ErrInvalidSlug = errors.New("invalid identifier: must be a DNS-label-shaped slug (lowercase alphanumeric or '-', 1-63 chars, must start with alphanumeric)")

// ErrInvalidUUID is returned when a value fails UUID validation.
var ErrInvalidUUID = errors.New("invalid identifier: must be a canonical RFC 4122 UUID")

// slugRE matches a DNS-label-shaped slug: lowercase, must start with
// alphanumeric, alphanumeric + hyphen otherwise, max 63 chars. Mirrors
// git-service's pkg/credentials/openbao_store.go regex and remote-worker's
// lib/uuid.ts isSlug.
var slugRE = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`)

// uuidRE matches an RFC 4122 UUID in canonical 8-4-4-4-12 hyphenated form.
// Stricter than google/uuid.Parse, which also accepts no-hyphen and braced
// forms — those would slip through as different identifiers in our path
// segments and audit logs.
var uuidRE = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

// Slug enforces that v is a DNS-label-shaped slug. Used at handler
// boundaries for orgHandle / projectName / componentName / ocOrgId —
// anything that can land in a filesystem path or storage key. Rejects
// path traversal (`..`, `/`), shell metacharacters, uppercase, embedded
// nulls / newlines, and overlong values.
func Slug(v string) error {
	if !slugRE.MatchString(v) {
		return ErrInvalidSlug
	}
	return nil
}

// UUID enforces that v is a canonical RFC 4122 UUID (hyphenated 8-4-4-4-12,
// case-insensitive). Used for taskId — the only identifier in the BFF's
// surface that's a real UUID.
func UUID(v string) error {
	if !uuidRE.MatchString(v) {
		return ErrInvalidUUID
	}
	return nil
}
