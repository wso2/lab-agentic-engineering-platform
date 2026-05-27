// Package codingagent builds the per-task K8s Job manifest the BFF
// POSTs through cluster-gateway-proxy in Phase 2 dispatch (WS2.1). The
// shape is plain `map[string]any` so it composes directly with
// clients/clustergatewayproxy.ApplyJob — no typed k8s.io/api/batch/v1
// dependency leaks here.
//
// Inputs and naming:
//
//   - runName     — short DNS-1123 label, used as the Job name and as
//                   the workflow-run identifier persisted on
//                   ComponentTask. The watcher (WS2.5) keys on this.
//   - orgNS       — per-org remote-worker namespace
//                   (`wc-<orgUUID8>-<orgHash8>-remote-worker`).
//   - anthropicSecretName / githubSecretName / publisherSecretName
//                   — K8s Secret names materialized by per-run
//                   ExternalSecrets just before the Job is applied.
//                   Mounted as envFrom so a leaked process listing
//                   won't disclose secret values.
//   - runnerImage — the asdlc remote-worker image; pinned by the BFF
//                   from cfg.AgentRunnerImage.
//   - prompt / repo URL / identity / callback URL — pre-rendered
//                   dispatch payload the runner reads from
//                   ASDLC_* env vars.
//
// Mounts:
//
//   - the runner's workspace lives on an `emptyDir` at
//     /home/asdlc/asdlc-workspace (matches remote-worker's config.ts
//     default).
//
// Restart policy:
//
//   - restartPolicy: Never + backoffLimit: 0. Failures are picked up by
//     the watcher polling Job.status, not by K8s replay. Argo-style
//     retry is intentionally absent — agent retries are policy, not
//     plumbing.
package codingagent

import (
	"fmt"
	"strings"
)

// JobInputs collects everything the template needs. All fields are
// required unless the doc-comment says otherwise; Build returns an
// error rather than producing a manifest that K8s will reject.
type JobInputs struct {
	RunName  string // Job name (DNS-1123 label, ≤ 63 chars)
	OrgNS    string // target namespace
	TaskID   string // ComponentTask UUID, stamped as a label for queries
	OrgID    string // OC org handle, stamped as a label
	ProjectID string
	ComponentName string

	// RunnerImage is the docker image the runner pod uses. Always pinned —
	// `:latest` is OK in dev but the BFF should resolve to a digest for prod.
	RunnerImage string

	// ServiceAccountName is the SA the runner pod runs as. WS2.3 ensures
	// this SA exists in OrgNS before applying the Job.
	ServiceAccountName string

	// Per-run K8s Secret names that ExternalSecrets will materialize. The
	// Job applies before the secret arrives is possible in theory; in
	// practice the BFF applies the ExternalSecret first and the per-run
	// secret is in place by the time the kubelet pulls the image.
	AnthropicSecretName       string
	GitHubSecretName          string
	// PublisherSecretName is the K8s Secret holding the per-org publisher
	// cc creds (client_id + client_secret) materialised by a per-run ES
	// from SM-API. Empty disables the WS2.4 runner-auth path; the runner
	// then falls back to ASDLC_BEARER for /credentials/refresh calls.
	PublisherSecretName       string
	// PublisherTokenURL is the Thunder /oauth2/token endpoint used by the
	// runner's cc helper. Non-secret; rides as a plain env. Set in lockstep
	// with PublisherSecretName.
	PublisherTokenURL         string

	// Dispatch payload — passed verbatim into the runner via ASDLC_* env vars.
	RepoURL        string
	Prompt         string
	IdentityName   string
	IdentityEmail  string
	IdentityLogin  string // optional
	GitServiceURL  string // ASDLC_GIT_SERVICE_URL (BFF URL reachable from the pod)
	CallbackURL    string // ASDLC_PLATFORM_URL (BFF callback URL)
	CorrelationID  string // optional; runner synthesizes one if absent

	// Bearer is the bespoke ASDLC_BEARER param. Deprecated by WS2.4 —
	// when PublisherSecretName is set this field MUST be empty so
	// the runner uses the Thunder client_credentials flow instead.
	Bearer string

	// ActiveDeadlineSeconds bounds the agent run. Zero falls back to 1h.
	ActiveDeadlineSeconds int64
}

// Build returns the Job manifest as a map[string]any ready to hand to
// clustergatewayproxy.ApplyJob. Validates required fields up-front so
// the proxy doesn't surface a 422 from k8s.
func Build(in JobInputs) (map[string]any, error) {
	if err := validate(in); err != nil {
		return nil, err
	}

	activeDeadline := in.ActiveDeadlineSeconds
	if activeDeadline <= 0 {
		activeDeadline = 3600 // 1h default
	}

	envVars := []map[string]any{
		{"name": "ASDLC_TASK_ID", "value": in.TaskID},
		{"name": "ASDLC_ORG_ID", "value": in.OrgID},
		{"name": "ASDLC_PROJECT_ID", "value": in.ProjectID},
		{"name": "ASDLC_COMPONENT_NAME", "value": in.ComponentName},
		{"name": "ASDLC_REPO_URL", "value": in.RepoURL},
		{"name": "ASDLC_PROMPT", "value": in.Prompt},
		{"name": "ASDLC_GIT_SERVICE_URL", "value": in.GitServiceURL},
		{"name": "ASDLC_PLATFORM_URL", "value": in.CallbackURL},
		{"name": "ASDLC_IDENTITY_NAME", "value": in.IdentityName},
		{"name": "ASDLC_IDENTITY_EMAIL", "value": in.IdentityEmail},
		{"name": "ASDLC_IDENTITY_LOGIN", "value": in.IdentityLogin},
		{"name": "ASDLC_CORRELATION_ID", "value": in.CorrelationID},
		{"name": "WORKSPACE_BASE_PATH", "value": "/home/asdlc/asdlc-workspace"},
	}
	if in.Bearer != "" {
		envVars = append(envVars, map[string]any{
			"name": "ASDLC_BEARER", "value": in.Bearer,
		})
	}

	envFrom := []map[string]any{
		{"secretRef": map[string]any{"name": in.AnthropicSecretName}},
		{"secretRef": map[string]any{"name": in.GitHubSecretName}},
	}
	if in.PublisherSecretName != "" {
		envFrom = append(envFrom, map[string]any{
			"secretRef": map[string]any{"name": in.PublisherSecretName},
		})
		if in.PublisherTokenURL != "" {
			envVars = append(envVars, map[string]any{
				"name": "PUBLISHER_TOKEN_URL", "value": in.PublisherTokenURL,
			})
		}
	}

	labels := map[string]string{
		"app.kubernetes.io/name":    "remote-worker",
		"app.kubernetes.io/part-of": "asdlc",
		"asdlc.io/task":             in.TaskID,
		"asdlc.io/org":              in.OrgID,
		"asdlc.io/project":          in.ProjectID,
		"asdlc.io/component":        in.ComponentName,
	}

	return map[string]any{
		"apiVersion": "batch/v1",
		"kind":       "Job",
		"metadata": map[string]any{
			"name":      in.RunName,
			"namespace": in.OrgNS,
			"labels":    labels,
		},
		"spec": map[string]any{
			"backoffLimit":            int64(0),
			"activeDeadlineSeconds":   activeDeadline,
			"ttlSecondsAfterFinished": int64(86400), // 24h cleanup grace
			"template": map[string]any{
				"metadata": map[string]any{
					"labels": labels,
				},
				"spec": map[string]any{
					"restartPolicy":      "Never",
					"serviceAccountName": in.ServiceAccountName,
					"containers": []map[string]any{
						{
							"name":    "remote-worker",
							"image":   in.RunnerImage,
							"env":     envVars,
							"envFrom": envFrom,
							"volumeMounts": []map[string]any{
								{"name": "workspace", "mountPath": "/home/asdlc/asdlc-workspace"},
								{"name": "tmp", "mountPath": "/tmp"},
							},
						},
					},
					"volumes": []map[string]any{
						{"name": "workspace", "emptyDir": map[string]any{}},
						{"name": "tmp", "emptyDir": map[string]any{}},
					},
				},
			},
		},
	}, nil
}

func validate(in JobInputs) error {
	var missing []string
	check := func(name, v string) {
		if strings.TrimSpace(v) == "" {
			missing = append(missing, name)
		}
	}
	check("RunName", in.RunName)
	check("OrgNS", in.OrgNS)
	check("TaskID", in.TaskID)
	check("OrgID", in.OrgID)
	check("ProjectID", in.ProjectID)
	check("ComponentName", in.ComponentName)
	check("RunnerImage", in.RunnerImage)
	check("ServiceAccountName", in.ServiceAccountName)
	check("AnthropicSecretName", in.AnthropicSecretName)
	check("GitHubSecretName", in.GitHubSecretName)
	check("RepoURL", in.RepoURL)
	check("Prompt", in.Prompt)
	check("IdentityName", in.IdentityName)
	check("IdentityEmail", in.IdentityEmail)
	check("GitServiceURL", in.GitServiceURL)
	check("CallbackURL", in.CallbackURL)

	if len(in.RunName) > 63 {
		return fmt.Errorf("codingagent: RunName %q exceeds K8s 63-char DNS label limit", in.RunName)
	}
	if len(missing) > 0 {
		return fmt.Errorf("codingagent: missing required field(s): %s", strings.Join(missing, ", "))
	}
	return nil
}
