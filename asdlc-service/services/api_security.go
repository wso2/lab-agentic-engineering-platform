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
// Conservative OR over both the Phase-2 `exposesAPI.auth` and the legacy
// `api.security` fields: a partial migration that adds `exposesAPI: { auth:
// none }` while `api.security: required` is still on disk MUST NOT silently
// strip JWT enforcement. Either field declaring protection is binding.
//
// Invariant: nil/empty blocks ⇒ false. The platform recognises only the
// documented values; anything else also yields false.
func ResolveAPISecurityEnabled(comp models.DesignComponent) bool {
	if comp.ExposesAPI != nil {
		switch strings.TrimSpace(comp.ExposesAPI.Auth) {
		case "end-user-required", "service-required":
			return true
		}
	}
	if comp.Api != nil && strings.EqualFold(strings.TrimSpace(comp.Api.Security), APISecurityRequired) {
		return true
	}
	return false
}

// ResolveAPISecurityCallerKind returns the auth flavor for sibling-CORS
// gating. Only `end-user-required` APIs should advertise SPA origins in
// their CORS allowlist (service-to-service APIs have no browser caller).
// Returns "" when API security is not enabled.
func ResolveAPISecurityCallerKind(comp models.DesignComponent) string {
	if comp.ExposesAPI != nil {
		switch strings.TrimSpace(comp.ExposesAPI.Auth) {
		case "end-user-required":
			return "end-user"
		case "service-required":
			return "service"
		}
	}
	if comp.Api != nil && strings.EqualFold(strings.TrimSpace(comp.Api.Security), APISecurityRequired) {
		// Legacy `api.security: required` is treated as end-user (the
		// only consumer the legacy flow shipped).
		return "end-user"
	}
	return ""
}
