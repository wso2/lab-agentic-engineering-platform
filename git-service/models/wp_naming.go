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

// BuildSecretNameFor returns the K8s Secret name git-service writes
// (kubernetes.io/basic-auth) into WorkflowPlaneNamespace(ocOrgID) for one
// WorkflowRun. The name matches the upstream `dockerfile-builder`
// ClusterWorkflow's expected default for `workflow.parameters.git-secret`
// — `${metadata.workflowRunName}-git-secret` (line 144 of the workflow).
//
// Per-WorkflowRun (NOT per-org) because the upstream workflow templates
// the Secret name from the WorkflowRun's metadata.name; using that exact
// shape lets us keep the shared workflow byte-identical to upstream while
// pre-staging the Secret from git-service before the build pod runs.
//
// We intentionally do NOT length-bound here. The workflow itself fails
// the Argo Workflow creation if `<workflowRunName>-git-secret` exceeds
// DNS-1123 subdomain length, so the WorkflowRun would already be invalid
// upstream — bounding here would produce a name that doesn't match what
// the workflow templates and silently break the mount.
func BuildSecretNameFor(workflowRunName string) string {
	return workflowRunName + "-git-secret"
}

// AnthropicSecretName is the fixed K8s Secret name git-service writes
// into WorkflowPlaneNamespace(ocOrgID). The Secret is unique within its
// namespace (one per WP namespace, one WP namespace per org), so no
// org-id encoding in the name is needed — different from BuildSecretName
// which retains its legacy `git-<orgID>` shape. The coding-agent pod
// mounts this Secret's ANTHROPIC_API_KEY key via secretKeyRef. Each
// dispatch SSA-overwrites the same Secret with the freshest value from
// `org_secrets`.
//
// See docs/design/anthropic-key-dual-token.md §4.3.
const AnthropicSecretName = "anthropic-credentials"

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
