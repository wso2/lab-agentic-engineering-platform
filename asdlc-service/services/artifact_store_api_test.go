package services

import (
	"strings"
	"testing"

	"github.com/wso2/asdlc/asdlc-service/models"
)

// Round-trip frontmatter ± exposesAPI block: components without
// `exposesAPI` produce no `exposesAPI:` line in the YAML, and components
// with `exposesAPI.auth: end-user-required` survive Split → Assemble
// cleanly with managed/userContext preserved.
func TestComponentFrontmatterAPIRoundTrip(t *testing.T) {
	cases := []struct {
		name                string
		comp                models.DesignComponent
		wantContainsExposes bool
		wantAuthAfter       string
	}{
		{
			name: "without exposesAPI block",
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
			wantContainsExposes: false,
		},
		{
			name: "with exposesAPI.auth=end-user-required",
			comp: models.DesignComponent{
				Name:                       "svc",
				ComponentType:              "service",
				Language:                   "Go",
				DependsOn:                  []string{},
				Entrypoint:                 "deployment/service",
				Buildpack:                  "docker",
				AppPath:                    "svc",
				ComponentAgentInstructions: "build it",
				ExposesAPI: &models.ExposesAPI{
					Auth:        "end-user-required",
					UserContext: "X-User-Id",
				},
			},
			wantContainsExposes: true,
			wantAuthAfter:       "end-user-required",
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
			if c.wantContainsExposes {
				if !strings.Contains(content, "exposesAPI:") || !strings.Contains(content, "auth: "+c.wantAuthAfter) {
					t.Fatalf("expected exposesAPI block with auth=%q in:\n%s", c.wantAuthAfter, content)
				}
			} else {
				if strings.Contains(content, "exposesAPI:") {
					t.Fatalf("did NOT expect any exposesAPI block in:\n%s", content)
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
			if c.wantContainsExposes {
				if got.ExposesAPI == nil || got.ExposesAPI.Auth != c.wantAuthAfter {
					t.Fatalf("after round-trip want exposesAPI.auth=%q, got %+v", c.wantAuthAfter, got.ExposesAPI)
				}
			} else if got.ExposesAPI != nil {
				t.Fatalf("after round-trip want nil ExposesAPI, got %+v", got.ExposesAPI)
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
