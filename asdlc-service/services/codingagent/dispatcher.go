// Package codingagent's Dispatcher is the WS2.3 orchestrator: it takes
// a single per-task dispatch request and walks the cluster-gateway-proxy
// to apply (in order):
//
//  1. Namespace             — per-org `wc-…-remote-worker`
//  2. ServiceAccount        — `remote-worker-runner`
//  3. ExternalSecret×2      — anthropic + github-pat
//  4. Job                   — the coding-agent runner pod
//
// The function is deliberately independent from the legacy dispatch
// path in services/dispatch_service.go: when the BFF is wired with a
// cluster-gateway-proxy client AND the per-org credential rows carry
// the SM-API triplet, the dispatch caller picks this orchestrator;
// otherwise the legacy ClusterWorkflow path runs. WS2.6 deletes the
// legacy branch once the new path is the only one.
package codingagent

import (
	"context"
	"errors"
	"fmt"

	"github.com/wso2/asdlc/asdlc-service/clients/clustergatewayproxy"
)

// Inputs gathers everything one dispatch call needs. All fields are
// required (verified by validate); the orchestrator returns the final
// runName the caller persists on the ComponentTask row.
type Inputs struct {
	// OrgUUID is the OC organization UUID (NOT the slug). Used to
	// derive the deterministic remote-worker NS via
	// RemoteWorkerNamespace.
	OrgUUID string

	// Job is the fully-resolved Job manifest inputs. The orchestrator
	// passes it straight to Build; callers fill in everything except
	// `OrgNS`, `AnthropicSecretName`, `GitHubSecretName`, and
	// `PublisherSecretName`, which the orchestrator computes from
	// the run name and overwrites.
	Job JobInputs

	// AnthropicSR / GitHubSR are the per-org SM-API triplets fetched
	// from `org_anthropic_credentials` / `org_credentials`. Both must
	// be populated — the orchestrator refuses to dispatch if the
	// Connect flow hasn't completed the SM-API mirror.
	AnthropicSR SecretRef
	GitHubSR    SecretRef

	// PublisherSR is WS2.4's per-org publisher cc credentials triplet
	// (`organization_idp_profiles`). When present, the orchestrator
	// emits a third per-run ExternalSecret materialising both
	// client_id + client_secret into a K8s Secret that the runner
	// mounts via envFrom (PUBLISHER_CLIENT_ID + PUBLISHER_CLIENT_SECRET).
	// Optional during the WS2.4 rollout — when absent the runner falls
	// back to ASDLC_BEARER.
	PublisherSR *SecretRef

	// ClusterSecretStoreName is the ESO CSS that backs reads. On
	// cloud-dp-oc-dp this MUST be `application-secrets-read` (AppRole
	// `approle-creds-application-read-permission` — the only one
	// scoped to `user-app-secrets/*` on the DP). `secretstore-read`
	// will silently no-op. Local k3d reuses `default`.
	ClusterSecretStoreName string
}

// SecretRef is the lookup triplet persisted on the per-org credential
// row by WS2.2's Connect flow.
type SecretRef struct {
	SecretRefName string
	KVPath        string
	Property      string
}

// Dispatcher wraps the proxy client + defaults. Construct once at boot.
type Dispatcher struct {
	proxy *clustergatewayproxy.Client

	// serviceAccount is the SA name applied to the per-org NS. Defaults
	// to "remote-worker-runner" but configurable for tests.
	serviceAccount string
}

// New constructs a Dispatcher. proxy must be non-nil — the orchestrator
// has no fallback path; the caller decides whether to construct it.
func New(proxy *clustergatewayproxy.Client) *Dispatcher {
	if proxy == nil {
		panic("codingagent.Dispatcher: proxy is required")
	}
	return &Dispatcher{
		proxy:          proxy,
		serviceAccount: "remote-worker-runner",
	}
}

// WithServiceAccount overrides the runner SA name. Returns the receiver
// for chained construction.
func (d *Dispatcher) WithServiceAccount(name string) *Dispatcher {
	if name != "" {
		d.serviceAccount = name
	}
	return d
}

// Dispatch walks the four-step apply chain. Returns the runName (echo
// of `in.Job.RunName`) so callers don't have to thread it back.
//
// Idempotency: namespace + SA + ExternalSecrets use Ensure* / Apply*
// semantics (POST → 409 tolerated). Jobs are immutable: ApplyJob does
// a DELETE+POST on 409 so a re-dispatch with the same runName starts a
// fresh Job (use case: operator-triggered retry that intentionally
// reuses the run name).
func (d *Dispatcher) Dispatch(ctx context.Context, in Inputs) (string, error) {
	ns := RemoteWorkerNamespace(in.OrgUUID)
	if ns == "" {
		return "", errors.New("codingagent dispatcher: failed to derive NS — empty OrgUUID")
	}
	if err := d.validate(in); err != nil {
		return "", err
	}

	// 1) Namespace.
	if err := d.proxy.EnsureNamespace(ctx, clustergatewayproxy.NamespaceMeta{
		Name: ns,
		Labels: map[string]string{
			"app.kubernetes.io/managed-by": "asdlc",
			"asdlc.io/purpose":             "remote-worker",
			"asdlc.io/org-uuid":            in.OrgUUID,
		},
	}); err != nil {
		return "", fmt.Errorf("dispatcher: ensure namespace %s: %w", ns, err)
	}

	// 2) ServiceAccount.
	if err := d.proxy.EnsureServiceAccount(ctx, ns, d.serviceAccount); err != nil {
		return "", fmt.Errorf("dispatcher: ensure SA %s/%s: %w", ns, d.serviceAccount, err)
	}

	// Pre-compute per-run secret names so the Job and the ExternalSecrets
	// agree on a single source of truth.
	runName := in.Job.RunName
	anthropicSecret := runName + "-anthropic"
	githubSecret := runName + "-github"
	publisherSecret := ""
	if in.PublisherSR != nil {
		publisherSecret = runName + "-publisher"
	}

	// 3) ExternalSecrets.
	if err := d.applyExternalSecret(ctx, in, ns, runName+"-anthropic-es", anthropicSecret, "ANTHROPIC_API_KEY", in.AnthropicSR); err != nil {
		return "", fmt.Errorf("dispatcher: apply anthropic ExternalSecret: %w", err)
	}
	if err := d.applyExternalSecret(ctx, in, ns, runName+"-github-es", githubSecret, "GITHUB_TOKEN", in.GitHubSR); err != nil {
		return "", fmt.Errorf("dispatcher: apply github-pat ExternalSecret: %w", err)
	}
	if in.PublisherSR != nil {
		if err := d.applyPublisherExternalSecret(ctx, in, ns, runName+"-publisher-es", publisherSecret, *in.PublisherSR); err != nil {
			return "", fmt.Errorf("dispatcher: apply publisher ExternalSecret: %w", err)
		}
	}

	// 4) Job — fill the secret names + NS + SA into the job inputs the
	// caller pre-populated, then build + apply.
	job := in.Job
	job.OrgNS = ns
	job.ServiceAccountName = d.serviceAccount
	job.AnthropicSecretName = anthropicSecret
	job.GitHubSecretName = githubSecret
	job.PublisherSecretName = publisherSecret
	manifest, err := Build(job)
	if err != nil {
		return "", fmt.Errorf("dispatcher: build Job manifest: %w", err)
	}
	if err := d.proxy.ApplyJob(ctx, ns, manifest); err != nil {
		return "", fmt.Errorf("dispatcher: apply Job: %w", err)
	}
	return runName, nil
}

func (d *Dispatcher) applyExternalSecret(ctx context.Context, in Inputs, ns, esName, secretName, localKey string, ref SecretRef) error {
	manifest, err := BuildExternalSecret(ExternalSecretInputs{
		Name:                   esName,
		Namespace:              ns,
		TargetSecretName:       secretName,
		ClusterSecretStoreName: in.ClusterSecretStoreName,
		RemoteRefKey:           ref.KVPath,
		RemoteRefProperty:      ref.Property,
		LocalKey:               localKey,
	})
	if err != nil {
		return err
	}
	return d.proxy.ApplyExternalSecret(ctx, ns, manifest)
}

// applyPublisherExternalSecret emits one ExternalSecret with two data
// entries — client_id and client_secret share the SM-API secret at
// ref.KVPath but materialise into distinct K8s Secret keys (PUBLISHER_
// CLIENT_ID + PUBLISHER_CLIENT_SECRET) for envFrom in the Job pod.
// Token URL is non-secret and rides as a plain env from the Job template.
func (d *Dispatcher) applyPublisherExternalSecret(ctx context.Context, in Inputs, ns, esName, secretName string, ref SecretRef) error {
	manifest, err := BuildExternalSecret(ExternalSecretInputs{
		Name:                   esName,
		Namespace:              ns,
		TargetSecretName:       secretName,
		ClusterSecretStoreName: in.ClusterSecretStoreName,
		RemoteRefKey:           ref.KVPath,
		DataEntries: []ExternalSecretDataEntry{
			{LocalKey: "PUBLISHER_CLIENT_ID", RemoteRefProperty: "client_id"},
			{LocalKey: "PUBLISHER_CLIENT_SECRET", RemoteRefProperty: "client_secret"},
		},
	})
	if err != nil {
		return err
	}
	return d.proxy.ApplyExternalSecret(ctx, ns, manifest)
}

func (d *Dispatcher) validate(in Inputs) error {
	if in.OrgUUID == "" {
		return errors.New("codingagent dispatcher: OrgUUID required")
	}
	if in.ClusterSecretStoreName == "" {
		return errors.New("codingagent dispatcher: ClusterSecretStoreName required")
	}
	if in.AnthropicSR.KVPath == "" || in.AnthropicSR.Property == "" {
		return errors.New("codingagent dispatcher: AnthropicSR not populated — Connect flow must complete the SM-API mirror first")
	}
	if in.GitHubSR.KVPath == "" || in.GitHubSR.Property == "" {
		return errors.New("codingagent dispatcher: GitHubSR not populated — Connect flow must complete the SM-API mirror first")
	}
	return nil
}
