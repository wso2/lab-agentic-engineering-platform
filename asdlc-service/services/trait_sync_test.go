package services

import (
	"reflect"
	"testing"
)

func TestAPIConfigurationInstanceName(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"poc-public", "poc-public-http"},
		{"user-api", "user-api-http"},
		{"  trimmed  ", "trimmed-http"},
		{"", "component-http"},
	}
	for _, c := range cases {
		if got := APIConfigurationInstanceName(c.in); got != c.want {
			t.Errorf("APIConfigurationInstanceName(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// TestDesiredAPIConfigurationTrait_Enabled — protected component shape.
// Trait carries `endpointName: http`; per-env configs enable cors + jwtAuth.
// This is the schema the AP gateway-runtime expects (see
// deployments/manifests/api-platform/api-configuration-trait.yaml).
func TestDesiredAPIConfigurationTrait_Enabled(t *testing.T) {
	traits, configs := DesiredAPIConfigurationTrait("svc", true)
	if len(traits) != 1 {
		t.Fatalf("want 1 trait, got %d", len(traits))
	}
	tr := traits[0]
	if tr.InstanceName != "svc-http" || tr.Kind != "ClusterTrait" || tr.Name != "api-configuration" {
		t.Fatalf("unexpected trait shape: %+v", tr)
	}
	if got, want := tr.Parameters["endpointName"], "http"; got != want {
		t.Errorf("endpointName = %v, want %v", got, want)
	}

	cfg, ok := configs["svc-http"]
	if !ok {
		t.Fatalf("missing config for svc-http; got keys %v", keysOfAny(configs))
	}
	jwt, ok := cfg["jwtAuth"].(map[string]interface{})
	if !ok {
		t.Fatalf("jwtAuth missing or wrong type: %#v", cfg["jwtAuth"])
	}
	if jwt["enabled"] != true {
		t.Errorf("jwtAuth.enabled = %v, want true", jwt["enabled"])
	}
	// Phase 6 — emit `issuers` + `audience` as empty arrays so the v1
	// emission stays semantically equivalent to v0 until Phase 7 fills
	// them with per-org values.
	if iss, ok := jwt["issuers"].([]interface{}); !ok {
		t.Errorf("jwtAuth.issuers should be []interface{}, got %T", jwt["issuers"])
	} else if len(iss) != 0 {
		t.Errorf("jwtAuth.issuers should default empty, got %v", iss)
	}
	if aud, ok := jwt["audience"].([]interface{}); !ok {
		t.Errorf("jwtAuth.audience should be []interface{}, got %T", jwt["audience"])
	} else if len(aud) != 0 {
		t.Errorf("jwtAuth.audience should default empty, got %v", aud)
	}
	cors, ok := cfg["cors"].(map[string]interface{})
	if !ok {
		t.Fatalf("cors missing or wrong type: %#v", cfg["cors"])
	}
	if cors["enabled"] != true {
		t.Errorf("cors.enabled = %v, want true", cors["enabled"])
	}
}

// TestDesiredAPIConfigurationTrait_Disabled — public component shape.
// No trait + a tombstone entry in configs so the OC client's merge logic
// removes any previously-set trait instance from each RB.
func TestDesiredAPIConfigurationTrait_Disabled(t *testing.T) {
	traits, configs := DesiredAPIConfigurationTrait("svc", false)
	if traits != nil {
		t.Fatalf("want nil traits when disabled, got %+v", traits)
	}
	want := map[string]map[string]interface{}{"svc-http": nil}
	if !reflect.DeepEqual(configs, want) {
		t.Fatalf("configs = %#v, want %#v", configs, want)
	}
}

// TestComponentNameFromDesignPath — only `components/<name>/design.md`
// triggers trait_sync; root design.md and openapi.yaml are ignored. Gate
// for the design-edit write site.
func TestComponentNameFromDesignPath(t *testing.T) {
	cases := []struct {
		in       string
		wantName string
		wantOK   bool
	}{
		{"components/svc/design.md", "svc", true},
		{"components/user-api/design.md", "user-api", true},
		{"design.md", "", false},
		{"components/svc/openapi.yaml", "", false},
		{"components//design.md", "", false},
		{"components/a/b/design.md", "", false},
		{"", "", false},
	}
	for _, c := range cases {
		name, ok := componentNameFromDesignPath(c.in)
		if name != c.wantName || ok != c.wantOK {
			t.Errorf("componentNameFromDesignPath(%q) = (%q,%v), want (%q,%v)",
				c.in, name, ok, c.wantName, c.wantOK)
		}
	}
}

func keysOfAny[T any](m map[string]T) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
