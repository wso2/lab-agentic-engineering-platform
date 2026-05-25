package services

import (
	"strings"

	"github.com/wso2/asdlc/asdlc-service/models"
)

// ResolveAPISecurityEnabled is the single source of truth for "is JWT
// validation enforced on this component's HTTP endpoint?" — used by the
// trait emitter, the watcher, and any UI that surfaces the badge.
//
// Invariant: nil/empty `ExposesAPI` ⇒ false. The platform recognises
// only the documented `Auth` values; anything else also yields false.
func ResolveAPISecurityEnabled(comp models.DesignComponent) bool {
	if comp.ExposesAPI == nil {
		return false
	}
	switch strings.ToLower(strings.TrimSpace(comp.ExposesAPI.Auth)) {
	case "end-user-required", "service-required":
		return true
	}
	return false
}

// ResolveAPISecurityCallerKind returns the auth flavor for sibling-CORS
// gating. Only `end-user-required` APIs should advertise SPA origins in
// their CORS allowlist (service-to-service APIs have no browser caller).
// Returns "" when API security is not enabled.
func ResolveAPISecurityCallerKind(comp models.DesignComponent) string {
	if comp.ExposesAPI == nil {
		return ""
	}
	switch strings.ToLower(strings.TrimSpace(comp.ExposesAPI.Auth)) {
	case "end-user-required":
		return "end-user"
	case "service-required":
		return "service"
	}
	return ""
}
