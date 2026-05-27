package codingagent

import (
	"errors"
	"fmt"
	"strings"
)

// ExternalSecretInputs produces a per-run ESO ExternalSecret manifest
// that materializes a single K8s Secret with one key from an SM-API-
// managed remote SecretReference.
//
// Why per-run instead of long-lived: the cloud topology gives each
// coding-agent Job its own NS-local Secret, scoped by the run name,
// so a leak survives only until the Job's TTL expires (24h).
type ExternalSecretInputs struct {
	// Name is the ExternalSecret name; conventionally
	// "<runName>-<entity>" (e.g. "run-ab12-anthropic").
	Name string
	// Namespace is the per-org remote-worker NS.
	Namespace string

	// TargetSecretName is the K8s Secret name materialized by ESO. The
	// Job's envFrom.secretRef.name references this.
	TargetSecretName string

	// ClusterSecretStoreName — ESO ClusterSecretStore that backs the
	// remote read. On cloud-dp-oc-dp this MUST be
	// `application-secrets-read` (AppRole
	// `approle-creds-application-read-permission` — the only one
	// scoped to `user-app-secrets/*`). `secretstore-read` on the same
	// cluster covers platform paths only and will no-op. Local k3d
	// reuses the existing `default` CSS (per WS1.1 compose wiring).
	ClusterSecretStoreName string

	// RemoteRefKey + RemoteRefProperty point at the SM-API-managed
	// SecretReference (key=KV path, property=field name). Persisted on
	// the per-org credential row by WS2.2's Connect flow. Single-field
	// shape; for multi-field secrets (WS2.4 publisher) populate
	// DataEntries instead and leave these empty.
	RemoteRefKey      string
	RemoteRefProperty string

	// LocalKey is the env var name the runner reads (e.g.
	// "ANTHROPIC_API_KEY" or "GITHUB_TOKEN"). The K8s Secret data map
	// is keyed by this string. Single-field shape; for multi-field
	// secrets populate DataEntries.
	LocalKey string

	// DataEntries is the multi-field shape. When non-empty, RemoteRefKey
	// is used as the single shared KV path and one ES data entry is
	// emitted per element. Used by WS2.4 publisher (client_id +
	// client_secret in one SM-API secret → one ES → one K8s Secret with
	// two keys). Empty falls back to the single-field shape above.
	DataEntries []ExternalSecretDataEntry

	// RefreshInterval — ESO reconcile cadence. Empty defaults to 5m,
	// which is plenty for a short-lived Job. Set to "0" to disable
	// ESO's refresh (one-shot materialization).
	RefreshInterval string

	// OwnerRunName + OwnerRunUID let the ExternalSecret be GC'd when
	// the parent Job is deleted. Optional — when empty the ES is
	// stranded after the Job's TTL expires and must be cleaned by the
	// watcher's purge step.
	OwnerRunName string
	OwnerRunUID  string
}

// ExternalSecretDataEntry is one {localKey, remoteProperty} pair in a
// multi-field ExternalSecret. All entries share the parent inputs'
// RemoteRefKey (the KV path).
type ExternalSecretDataEntry struct {
	LocalKey          string
	RemoteRefProperty string
}

// BuildExternalSecret returns the ESO ExternalSecret manifest as a
// `map[string]any` ready for clustergatewayproxy.ApplyExternalSecret.
func BuildExternalSecret(in ExternalSecretInputs) (map[string]any, error) {
	if err := validateES(in); err != nil {
		return nil, err
	}
	refresh := in.RefreshInterval
	if refresh == "" {
		refresh = "5m"
	}

	var data []map[string]any
	if len(in.DataEntries) > 0 {
		data = make([]map[string]any, 0, len(in.DataEntries))
		for _, e := range in.DataEntries {
			data = append(data, map[string]any{
				"secretKey": e.LocalKey,
				"remoteRef": map[string]any{
					"key":      in.RemoteRefKey,
					"property": e.RemoteRefProperty,
				},
			})
		}
	} else {
		data = []map[string]any{
			{
				"secretKey": in.LocalKey,
				"remoteRef": map[string]any{
					"key":      in.RemoteRefKey,
					"property": in.RemoteRefProperty,
				},
			},
		}
	}

	manifest := map[string]any{
		"apiVersion": "external-secrets.io/v1",
		"kind":       "ExternalSecret",
		"metadata": map[string]any{
			"name":      in.Name,
			"namespace": in.Namespace,
		},
		"spec": map[string]any{
			"refreshInterval": refresh,
			"secretStoreRef": map[string]any{
				"name": in.ClusterSecretStoreName,
				"kind": "ClusterSecretStore",
			},
			"target": map[string]any{
				"name":           in.TargetSecretName,
				"creationPolicy": "Owner",
			},
			"data": data,
		},
	}

	if in.OwnerRunName != "" && in.OwnerRunUID != "" {
		meta := manifest["metadata"].(map[string]any)
		meta["ownerReferences"] = []map[string]any{
			{
				"apiVersion":         "batch/v1",
				"kind":               "Job",
				"name":               in.OwnerRunName,
				"uid":                in.OwnerRunUID,
				"controller":         true,
				"blockOwnerDeletion": true,
			},
		}
	}

	return manifest, nil
}

func validateES(in ExternalSecretInputs) error {
	var missing []string
	check := func(name, v string) {
		if strings.TrimSpace(v) == "" {
			missing = append(missing, name)
		}
	}
	check("Name", in.Name)
	check("Namespace", in.Namespace)
	check("TargetSecretName", in.TargetSecretName)
	check("ClusterSecretStoreName", in.ClusterSecretStoreName)
	check("RemoteRefKey", in.RemoteRefKey)
	if len(in.DataEntries) > 0 {
		for i, e := range in.DataEntries {
			check(fmt.Sprintf("DataEntries[%d].LocalKey", i), e.LocalKey)
			check(fmt.Sprintf("DataEntries[%d].RemoteRefProperty", i), e.RemoteRefProperty)
		}
	} else {
		check("RemoteRefProperty", in.RemoteRefProperty)
		check("LocalKey", in.LocalKey)
	}
	if len(in.Name) > 253 {
		return errors.New("codingagent: ExternalSecret name exceeds 253-char DNS subdomain limit")
	}
	if len(missing) > 0 {
		return fmt.Errorf("codingagent: ExternalSecret missing required field(s): %s", strings.Join(missing, ", "))
	}
	return nil
}
