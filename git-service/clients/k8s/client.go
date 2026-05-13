// Package k8s wraps the controller-runtime client used to write build-credential
// Secrets directly into each org's workflow-plane namespace.
//
// Architectural context: BuildCredentialsService stores the per-org credential
// in Postgres (post-2f26614). To make that credential reachable by the build
// pod's checkout step — which mounts a `kubernetes.io/basic-auth` Secret as a
// volume — git-service writes the Secret straight into `workflows-<ocOrgID>`,
// bypassing OpenBao + External-Secrets + SecretReference entirely for git
// creds. The dockerfile-builder ClusterWorkflow now passes the WP Secret name
// to checkout-source as a regular volume.secret.secretName.
//
// RBAC: see deployments-v2/manifests/git-service-wp-rbac.yaml. The grant is
// cluster-wide because k8s RBAC doesn't natively support name-prefix
// matchers (`workflows-*`); operational discipline keeps writes confined to
// the workflow-plane namespaces, and the namespace name is derived
// deterministically from the request context inside MintBuildToken.
package k8s

import (
	"fmt"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/config"
)

// FieldOwner is the Server-Side Apply field-manager string git-service stamps
// on objects it owns. Picking a stable, distinctive value makes audit + drift
// reconciliation tractable.
const FieldOwner = "app-factory-git-service"

// NewInClusterClient returns a controller-runtime client wired against the
// in-cluster service-account token. Returns an error if no rest.Config is
// available (e.g. running outside a pod with no KUBECONFIG).
//
// The returned client knows core/v1 (Secret, Namespace); we don't register
// the OC v1alpha1 scheme because git-service intentionally writes only
// native K8s objects.
func NewInClusterClient() (client.Client, error) {
	cfg, err := config.GetConfig()
	if err != nil {
		return nil, fmt.Errorf("k8s: load rest.Config: %w", err)
	}
	sch := runtime.NewScheme()
	utilruntime.Must(scheme.AddToScheme(sch))
	utilruntime.Must(corev1.AddToScheme(sch))
	c, err := client.New(cfg, client.Options{Scheme: sch})
	if err != nil {
		return nil, fmt.Errorf("k8s: construct client: %w", err)
	}
	return c, nil
}
