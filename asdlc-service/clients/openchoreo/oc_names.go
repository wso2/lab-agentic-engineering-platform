package openchoreo

import (
	"fmt"
	"strings"
)

// DefaultBuildRegistry is the in-cluster Docker registry that OpenChoreo's
// dockerfile-builder ClusterWorkflow pushes to. OC hardcodes this in the
// workflow template, so we mirror it here. Override via OPENCHOREO_BUILD_REGISTRY
// if OC is deployed with a different registry.
const DefaultBuildRegistry = "registry.openchoreo-workflow-plane.svc.cluster.local:10082"

// BuildImageRef returns the image URL that ASDLC's dockerfile-builder workflow
// pushes for this component. The workflow names the image
// `<orgNs>-<projectName>-<scopedComponentName>` and always pushes a `:latest`
// tag in addition to the versioned tag. OC's WorkflowRun CR does not surface
// task outputs, so callers must construct this convention-based URL instead of
// reading the image back from the API.
func BuildImageRef(registry, orgNamespace, projectName, componentName string) string {
	return fmt.Sprintf("%s/%s-%s-%s:latest",
		registry, orgNamespace, projectName, ScopedComponentName(projectName, componentName))
}

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
