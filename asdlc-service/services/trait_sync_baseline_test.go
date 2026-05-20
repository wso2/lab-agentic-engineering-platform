package services

import (
	"testing"

	"github.com/wso2/asdlc/asdlc-service/models"
)

// TestBaseline_NoAPIBlock_ProducesNoTrait is the §13 baseline-diff
// guarantee: a component whose design.md has no `api` block in
// frontmatter produces zero traits + no env config entries. That keeps
// the on-cluster Component CR + ReleaseBindings bit-identical to the
// pre-Phase-2 baseline for the corpus of existing unprotected
// components — so flipping FEATURE_EMIT_API_TRAIT on is a no-op for
// every component the user hasn't explicitly marked protected.
func TestBaseline_NoAPIBlock_ProducesNoTrait(t *testing.T) {
	cases := []struct {
		name string
		api  *models.APISecurity
	}{
		{"nil api block", nil},
		{"empty security string", &models.APISecurity{Security: ""}},
		{"explicit none", &models.APISecurity{Security: "none"}},
		{"unrecognised value defensive none", &models.APISecurity{Security: "yes"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			comp := models.DesignComponent{
				Name:          "svc",
				ComponentType: "service",
				Api:           c.api,
			}
			enabled := ResolveAPISecurityEnabled(comp)
			if enabled {
				t.Fatalf("ResolveAPISecurityEnabled = true for %s (api=%+v); want false", c.name, c.api)
			}
			traits, configs := DesiredAPIConfigurationTrait("svc", enabled)
			if len(traits) != 0 {
				t.Errorf("want zero traits for %s, got %d", c.name, len(traits))
			}
			// configs may contain a tombstone entry (`{"svc-http": nil}`)
			// — that's the explicit-clear marker the OC client merge
			// logic uses to strip stale `enabled: true` from RBs of a
			// component that flipped from required → none. The
			// tombstone is OK for the baseline because:
			//   1. RBs without a prior entry are unaffected (merge skips delete-of-absent).
			//   2. RBs WITH a prior entry get cleaned, which is the desired behaviour.
			// What we MUST NOT see is a populated parameters map.
			for inst, params := range configs {
				if len(params) > 0 {
					t.Errorf("baseline must not populate env config for %s; got %+v", inst, params)
				}
			}
		})
	}
}

// TestProtected_ProducesCanonicalTrait — paired contract: a component
// marked `api.security: required` produces exactly the trait shape the
// canonical wso2cloud `api-configuration` ClusterTrait expects. Pins
// the on-cluster CR contents so a future refactor of the helper can't
// silently change the wire shape.
func TestProtected_ProducesCanonicalTrait(t *testing.T) {
	comp := models.DesignComponent{
		Name:          "todo-api",
		ComponentType: "service",
		Api:           &models.APISecurity{Security: "required"},
	}
	if !ResolveAPISecurityEnabled(comp) {
		t.Fatal("ResolveAPISecurityEnabled should be true for security=required")
	}
	traits, configs := DesiredAPIConfigurationTrait("todo-api", true)
	if len(traits) != 1 {
		t.Fatalf("want exactly 1 trait, got %d", len(traits))
	}
	trait := traits[0]
	if trait.Name != "api-configuration" {
		t.Errorf("trait.Name = %q, want api-configuration", trait.Name)
	}
	if trait.Kind != "ClusterTrait" {
		t.Errorf("trait.Kind = %q, want ClusterTrait", trait.Kind)
	}
	if trait.InstanceName != "todo-api-http" {
		t.Errorf("trait.InstanceName = %q, want todo-api-http", trait.InstanceName)
	}
	if got := trait.Parameters["endpointName"]; got != "http" {
		t.Errorf("endpointName = %v, want http", got)
	}

	// Per-env config must enable both cors + jwtAuth.
	cfg, ok := configs["todo-api-http"]
	if !ok {
		t.Fatalf("missing env config for todo-api-http; got keys: %v", keysOfAny(configs))
	}
	jwt, ok := cfg["jwtAuth"].(map[string]interface{})
	if !ok {
		t.Fatalf("jwtAuth missing/wrong type: %#v", cfg["jwtAuth"])
	}
	if jwt["enabled"] != true {
		t.Errorf("jwtAuth.enabled = %v, want true", jwt["enabled"])
	}
	cors, ok := cfg["cors"].(map[string]interface{})
	if !ok {
		t.Fatalf("cors missing/wrong type: %#v", cfg["cors"])
	}
	if cors["enabled"] != true {
		t.Errorf("cors.enabled = %v, want true", cors["enabled"])
	}
}
