package models

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
)

// k8sMaxLabel is the DNS-1123 label cap. Names that mount as
// `volume.secret.secretName` and namespaces must both fit inside 63
// chars. Same constraint applies transitively through ESO labels.
const k8sMaxLabel = 63

// WorkflowPlaneNamespace returns the workflow-plane namespace name for a
// given OC org. Mirrors OC's own `getWorkflowNamespace` shape
// (`workflows-<ocOrgID>`), so the namespace git-service writes into is
// the same one OC's WorkflowRun controller schedules build pods in.
//
// Single source of truth: every caller that needs to address the org's
// workflow-plane namespace goes through this helper, so a future rename
// is a one-line change.
//
// For long orgIDs that would overflow the K8s DNS-1123 cap, the suffix
// is truncated and a stable 8-char hash is appended to preserve
// uniqueness across collisions.
func WorkflowPlaneNamespace(ocOrgID string) string {
	return boundedDNSName("workflows-", ocOrgID)
}

// BuildSecretName returns the per-org K8s Secret name git-service writes
// (kubernetes.io/basic-auth) into WorkflowPlaneNamespace(ocOrgID). The
// build's checkout step mounts this Secret by name via a regular
// `volume.secret.secretName`.
//
// Per-org (NOT per-repo) because the underlying GitHub credential is
// per-installation (App mode) or per-PAT (PAT mode) — a single token
// grants access to every repo the install/PAT can see, so fan-out to one
// Secret per repo would be N copies of the same value.
//
// DNS-label bounded; over-long orgIDs are truncated with a SHA-256
// suffix to stay collision-free inside 63 chars.
func BuildSecretName(ocOrgID string) string {
	return boundedDNSName("git-", ocOrgID)
}

// boundedDNSName returns prefix+slug — lower-cased and trimmed — bounded
// by the K8s DNS-1123 label cap. When the natural shape exceeds 63
// chars, the slug is truncated to leave room for "-<8-hex>" where the
// 8 hex chars are SHA-256(prefix+raw-slug)[:8], preserving uniqueness
// across truncations.
//
// Why this matters: K8s native names rejected over 63 chars; OC's
// PushSecret and SecretReference paths echo the Secret name into
// resource labels (which also cap at 63), so even though basic K8s
// Secret naming allows DNS-1123 subdomain (253 chars), staying inside
// label-cap keeps every downstream code path safe.
func boundedDNSName(prefix, slug string) string {
	slug = strings.ToLower(strings.TrimSpace(slug))
	name := prefix + slug
	if len(name) <= k8sMaxLabel {
		return name
	}
	// reserve 9 chars for "-<8-hex>"; truncate slug to fit.
	const reserved = 9
	maxSlug := k8sMaxLabel - len(prefix) - reserved
	if maxSlug < 1 {
		// pathological prefix; fall back to full hash.
		sum := sha256.Sum256([]byte(slug))
		return prefix + hex.EncodeToString(sum[:])[:k8sMaxLabel-len(prefix)]
	}
	sum := sha256.Sum256([]byte(prefix + slug))
	return prefix + slug[:maxSlug] + "-" + hex.EncodeToString(sum[:])[:8]
}
