package services

import (
	"strings"
	"testing"

	"github.com/wso2/asdlc/asdlc-service/models"
)

// Round-trip frontmatter ± api block to prove Phase 1 schema is backward
// compatible: components without `api` produce identical bytes to the
// pre-Phase-1 baseline (no `api:` line in the YAML), and components with
// `api.security: required` survive Split → Assemble cleanly.
func TestComponentFrontmatterAPIRoundTrip(t *testing.T) {
	cases := []struct {
		name              string
		comp              models.DesignComponent
		wantContainsAPI   bool
		wantSecurityAfter string
	}{
		{
			name: "without api block",
			comp: models.DesignComponent{
				Name:                       "svc",
				ComponentType:              "service",
				Language:                   "Go",
				DependsOn:                  []string{},
				Entrypoint:                 "deployment/service",
				Buildpack:                  "docker",
				AppPath:                    "svc",
				ComponentAgentInstructions: "build it",
			},
			wantContainsAPI: false,
		},
		{
			name: "with api.security=required",
			comp: models.DesignComponent{
				Name:                       "svc",
				ComponentType:              "service",
				Language:                   "Go",
				DependsOn:                  []string{},
				Entrypoint:                 "deployment/service",
				Buildpack:                  "docker",
				AppPath:                    "svc",
				ComponentAgentInstructions: "build it",
				Api:                        &models.APISecurity{Security: "required"},
			},
			wantContainsAPI:   true,
			wantSecurityAfter: "required",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			d := &DesignFile{
				Overview:   "the system",
				Components: []models.DesignComponent{c.comp},
			}
			files, err := SplitDesign(d)
			if err != nil {
				t.Fatalf("SplitDesign: %v", err)
			}
			path := componentDirPrefix + c.comp.Name + "/design.md"
			content, ok := files[path]
			if !ok {
				t.Fatalf("expected %s in files; got keys: %v", path, keysOf(files))
			}
			if c.wantContainsAPI {
				if !strings.Contains(content, "api:") || !strings.Contains(content, "security: "+c.wantSecurityAfter) {
					t.Fatalf("expected api block with security=%q in:\n%s", c.wantSecurityAfter, content)
				}
			} else {
				if strings.Contains(content, "api:") || strings.Contains(content, "security:") {
					t.Fatalf("did NOT expect any api block in:\n%s", content)
				}
			}

			// Assemble the file map back into a DesignFile and check the
			// component round-trips.
			out, err := AssembleDesign(files)
			if err != nil {
				t.Fatalf("AssembleDesign: %v", err)
			}
			if len(out.Components) != 1 {
				t.Fatalf("expected 1 component, got %d", len(out.Components))
			}
			got := out.Components[0]
			if c.wantContainsAPI {
				if got.Api == nil || got.Api.Security != c.wantSecurityAfter {
					t.Fatalf("after round-trip want api.security=%q, got %+v", c.wantSecurityAfter, got.Api)
				}
			} else if got.Api != nil {
				t.Fatalf("after round-trip want nil Api, got %+v", got.Api)
			}
		})
	}
}

func keysOf(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
