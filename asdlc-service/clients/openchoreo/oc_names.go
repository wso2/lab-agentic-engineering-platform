package openchoreo

import (
	"strings"
)

// ScopedComponentName is the k8s metadata name OC uses for a component. OC
// components across every project in an org share a single k8s namespace, so
// two projects can't hold the same component name unless we disambiguate.
// We prefix with the project name; the user's original name survives as the
// display-name annotation.
//
// Callers must always pass the friendly component name (never a previously
// scoped name) — call this exactly once, at the OC boundary.
func ScopedComponentName(projectName, componentName string) string {
	if projectName == "" {
		return componentName
	}
	return projectName + "-" + componentName
}

// FriendlyComponentName reverses ScopedComponentName using the owner project
// recorded on the OC component. Safe on legacy rows that were never prefixed.
func FriendlyComponentName(k8sName, projectName string) string {
	if projectName == "" {
		return k8sName
	}
	return strings.TrimPrefix(k8sName, projectName+"-")
}
