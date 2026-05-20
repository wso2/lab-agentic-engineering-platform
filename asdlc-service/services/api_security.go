package services

import (
	"strings"

	"github.com/wso2/asdlc/asdlc-service/models"
)

// APISecurityRequired is the canonical value for `api.security` that flips
// JWT enforcement on at the AP gateway. See docs/design/api-platform-integration.md
// section 5.1.
const APISecurityRequired = "required"

// ResolveAPISecurityEnabled is the single source of truth for "is JWT
// validation enforced on this component's HTTP endpoint?" — used by the
// trait emitter, the watcher, and any UI that surfaces the badge.
//
// Invariant: nil/empty `Api` block ⇒ false. Any value other than "required"
// also yields false (defensive — the frontmatter accepts a free string but
// the platform recognises only the documented value).
func ResolveAPISecurityEnabled(comp models.DesignComponent) bool {
	if comp.Api == nil {
		return false
	}
	return strings.EqualFold(strings.TrimSpace(comp.Api.Security), APISecurityRequired)
}
