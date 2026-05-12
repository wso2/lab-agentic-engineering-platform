package models

import (
	"crypto/sha256"
	"encoding/hex"
	"regexp"
	"strings"
	"time"
)

// GitRepository stores metadata about a platform-provisioned git repository.
type GitRepository struct {
	ID            string `gorm:"primaryKey;type:uuid;default:gen_random_uuid()" json:"id"`
	OrgID         string `gorm:"index;not null" json:"orgId"`
	ProjectID     string `gorm:"uniqueIndex;not null" json:"projectId"`
	RepoURL       string `gorm:"not null" json:"repoUrl"`
	ClonePath     string `gorm:"type:text" json:"clonePath"`
	DefaultBranch string `gorm:"default:main" json:"defaultBranch"`
	Status        string `gorm:"default:pending" json:"status"`
	ErrorMessage  string `gorm:"type:text" json:"errorMessage,omitempty"`
	// WebhookID is the GitHub-assigned hook ID for the repo's webhook.
	// Populated at repo provision; nil for repos created before Phase 0.
	// Used to deregister on repo cleanup or re-register on rotation.
	WebhookID *int64 `json:"webhookId,omitempty"`
	// OcSecretRefName is the name of the OC SecretReference CR backing this
	// repo's git credentials. Phase 2 PR A adds the column (nullable);
	// PR C populates it via the SecretReference + mint-build flow.
	OcSecretRefName *string `gorm:"column:oc_secret_ref_name" json:"ocSecretRefName,omitempty"`
	// RepoSlug is the SecretReference slug — `lower(<owner>-<repo>)`. PR C adds
	// the column, backfilled from RepoURL. Used for OpenBao path keying
	// (`secret/asdlc/{ocOrgId}/git/{repoSlug}`) and the OC SecretReference CR
	// name (`git-{ocOrgId}-{repoSlug}`). Nullable for legacy rows that pre-date
	// PR C; the dispatch path lazy-backfills.
	RepoSlug        string    `gorm:"column:repo_slug;index" json:"repoSlug,omitempty"`
	CreatedAt       time.Time `json:"createdAt"`
	UpdatedAt       time.Time `json:"updatedAt"`
	GithubProjectID string    `gorm:"type:text" json:"githubProjectId,omitempty"`
}

// repoURLPattern extracts `<owner>/<repo>` from a GitHub HTTPS URL.
// Matches both `https://github.com/owner/repo` and `https://github.com/owner/repo.git`.
var repoURLPattern = regexp.MustCompile(`github\.com/([^/]+/[^/]+?)(?:\.git)?/?$`)

// SlugForURL returns the canonical RepoSlug for a GitHub HTTPS URL — the
// `owner/repo` path lowercased with `/` replaced by `-`. Returns empty string
// if the URL doesn't match the GitHub HTTPS pattern (caller decides whether
// to backfill or fail).
//
// Mirrors phase2.md §9.1: `slug = strings.ToLower(strings.ReplaceAll(repoFullName, "/", "-"))`.
func SlugForURL(repoURL string) string {
	m := repoURLPattern.FindStringSubmatch(repoURL)
	if len(m) < 2 {
		return ""
	}
	return strings.ToLower(strings.ReplaceAll(m[1], "/", "-"))
}

// OwnerRepoFromURL extracts (owner, repo) from a GitHub HTTPS URL, preserving
// the original case (unlike SlugForURL which lowercases). Returns empty
// strings if the URL doesn't match the GitHub HTTPS pattern. Used by the
// artifact-store v2 save flow to address the repo over the GitHub REST API.
func OwnerRepoFromURL(repoURL string) (owner, repo string) {
	m := repoURLPattern.FindStringSubmatch(repoURL)
	if len(m) < 2 {
		return "", ""
	}
	parts := strings.SplitN(m[1], "/", 2)
	if len(parts) != 2 {
		return "", ""
	}
	return parts[0], parts[1]
}

// SecretRefNameFor returns the deterministic OC SecretReference CR name for
// a repo identified by (ocOrgID, repoSlug). The name is bounded by the K8s
// DNS-label limit (63 chars); over-long names are trimmed with a SHA-256
// suffix to preserve uniqueness. Mirrors phase2.md §9.1.
func SecretRefNameFor(ocOrgID, repoSlug string) string {
	name := "git-" + ocOrgID + "-" + repoSlug
	if len(name) <= 63 {
		return name
	}
	// Truncate and append a short hash to keep uniqueness across long slugs.
	sum := sha256.Sum256([]byte(name))
	hashSuffix := hex.EncodeToString(sum[:])[:8]
	const reserved = 9 // "-" + 8-char hex
	if len(name) > 63-reserved {
		name = name[:63-reserved]
	}
	return name + "-" + hashSuffix
}
