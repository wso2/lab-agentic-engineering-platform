package codingagent

import (
	"strings"
	"testing"
)

func validInputs() JobInputs {
	return JobInputs{
		RunName:                 "run-abc12345",
		OrgNS:                   "wc-deadbeef-cafebabe-remote-worker",
		TaskID:                  "11111111-1111-1111-1111-111111111111",
		OrgID:                   "default",
		ProjectID:               "demo",
		ComponentName:           "api",
		RunnerImage:             "docker.io/xlight05/app-factory-coding-agent-runner:latest",
		ServiceAccountName:      "remote-worker-runner",
		AnthropicSecretName:     "run-abc12345-anthropic",
		GitHubSecretName:        "run-abc12345-github",
		PublisherSecretName:     "",
		RepoURL:                 "https://github.com/wso2/demo.git",
		Prompt:                  "implement /healthz",
		IdentityName:            "ASDLC Bot",
		IdentityEmail:           "bot@asdlc.dev",
		IdentityLogin:           "asdlc-bot",
		GitServiceURL:           "http://host.k3d.internal:9090",
		CallbackURL:             "http://host.k3d.internal:9090",
		CorrelationID:           "abc12345",
		Bearer:                  "task-jwt-here",
	}
}

func TestBuild_AllRequired(t *testing.T) {
	job, err := Build(validInputs())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := job["apiVersion"]; got != "batch/v1" {
		t.Fatalf("apiVersion = %v; want batch/v1", got)
	}
	spec, ok := job["spec"].(map[string]any)
	if !ok {
		t.Fatal("spec missing")
	}
	if spec["backoffLimit"].(int64) != 0 {
		t.Fatal("backoffLimit must be 0 — failures surface via watcher")
	}
	if spec["activeDeadlineSeconds"].(int64) != 3600 {
		t.Fatal("activeDeadlineSeconds should default to 3600")
	}
}

func TestBuild_MissingField(t *testing.T) {
	in := validInputs()
	in.Prompt = ""
	if _, err := Build(in); err == nil {
		t.Fatal("expected error for missing Prompt")
	} else if !strings.Contains(err.Error(), "Prompt") {
		t.Fatalf("error %v should mention Prompt", err)
	}
}

func TestBuild_BearerAndPublisherCoexist(t *testing.T) {
	// During the WS2.4 cutover Bearer + PublisherSecretName both ride on
	// the same dispatch; the runner prefers cc and falls back to Bearer.
	// Build must accept both being set.
	in := validInputs()
	in.PublisherSecretName = "run-abc12345-publisher"
	in.PublisherTokenURL = "https://thunder.example.com/oauth2/token"
	if _, err := Build(in); err != nil {
		t.Fatalf("Bearer + PublisherSecretName must coexist during WS2.4 cutover: %v", err)
	}
}

func TestBuild_PublisherAddsEnvFrom(t *testing.T) {
	in := validInputs()
	in.PublisherSecretName = "run-abc12345-publisher"
	in.PublisherTokenURL = "https://thunder.example.com/oauth2/token"
	job, err := Build(in)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	tmpl := job["spec"].(map[string]any)["template"].(map[string]any)
	containers := tmpl["spec"].(map[string]any)["containers"].([]map[string]any)
	envFrom := containers[0]["envFrom"].([]map[string]any)
	if len(envFrom) != 3 {
		t.Fatalf("envFrom should include anthropic + github + publisher (3 entries); got %d", len(envFrom))
	}
	env := containers[0]["env"].([]map[string]any)
	var sawTokenURL bool
	for _, e := range env {
		if e["name"] == "PUBLISHER_TOKEN_URL" {
			sawTokenURL = true
		}
	}
	if !sawTokenURL {
		t.Fatal("PUBLISHER_TOKEN_URL env missing")
	}
}

func TestBuildExternalSecret_Defaults(t *testing.T) {
	es, err := BuildExternalSecret(ExternalSecretInputs{
		Name:                   "run-abc-anthropic",
		Namespace:              "wc-x-y-remote-worker",
		TargetSecretName:       "run-abc-anthropic",
		ClusterSecretStoreName: "secretstore-read",
		RemoteRefKey:           "user-app-secrets/default/cred-anthropic-abc",
		RemoteRefProperty:      "api-key",
		LocalKey:               "ANTHROPIC_API_KEY",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	spec := es["spec"].(map[string]any)
	if spec["refreshInterval"] != "5m" {
		t.Fatalf("default refreshInterval should be 5m; got %v", spec["refreshInterval"])
	}
	target := spec["target"].(map[string]any)
	if target["creationPolicy"] != "Owner" {
		t.Fatalf("creationPolicy should be Owner; got %v", target["creationPolicy"])
	}
}
