package services

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/wso2/asdlc/asdlc-service/models"
)

func TestNormalizeOpenAPIYAML_KeyOrder(t *testing.T) {
	// Same content, two different key orders.
	a := `openapi: 3.0.3
info:
  title: x
  version: "1"
paths:
  /health:
    get:
      responses:
        "200":
          description: ok
`
	b := `paths:
  /health:
    get:
      responses:
        "200":
          description: ok
info:
  version: "1"
  title: x
openapi: 3.0.3
`
	na, err := normalizeOpenAPIYAML(a)
	if err != nil {
		t.Fatalf("a: %v", err)
	}
	nb, err := normalizeOpenAPIYAML(b)
	if err != nil {
		t.Fatalf("b: %v", err)
	}
	if na != nb {
		t.Errorf("expected key-order independence:\nA=%s\nB=%s", na, nb)
	}
}

func TestNormalizeOpenAPIYAML_StatusCodeCoercion(t *testing.T) {
	// Unquoted 200 (parsed as int) vs quoted "200" (parsed as string).
	a := `openapi: 3.0.3
info:
  title: x
  version: "1"
paths:
  /health:
    get:
      responses:
        200:
          description: ok
`
	b := `openapi: 3.0.3
info:
  title: x
  version: "1"
paths:
  /health:
    get:
      responses:
        "200":
          description: ok
`
	na, err := normalizeOpenAPIYAML(a)
	if err != nil {
		t.Fatalf("a: %v", err)
	}
	nb, err := normalizeOpenAPIYAML(b)
	if err != nil {
		t.Fatalf("b: %v", err)
	}
	if na != nb {
		t.Errorf("expected status-code coercion:\nA=%s\nB=%s", na, nb)
	}
	if !strings.Contains(na, `"200"`) {
		t.Errorf("expected quoted 200 in canonical form: %s", na)
	}
}

func TestNormalizeOpenAPIYAML_DropsEmptyArrays(t *testing.T) {
	a := `openapi: 3.0.3
info:
  title: x
  version: "1"
paths:
  /health:
    get:
      parameters: []
      tags: []
      responses:
        "200":
          description: ok
`
	out, err := normalizeOpenAPIYAML(a)
	if err != nil {
		t.Fatalf("%v", err)
	}
	if strings.Contains(out, "parameters:") {
		t.Errorf("expected empty 'parameters' to be dropped: %s", out)
	}
	if strings.Contains(out, "tags:") {
		t.Errorf("expected empty 'tags' to be dropped: %s", out)
	}
}

func TestNormalizeOpenAPIYAML_PreservesXExtensions(t *testing.T) {
	a := `openapi: 3.0.3
info:
  title: x
  version: "1"
  x-internal-note: confidential
paths:
  /health:
    get:
      x-rate-limit: 10
      responses:
        "200":
          description: ok
`
	out, err := normalizeOpenAPIYAML(a)
	if err != nil {
		t.Fatalf("%v", err)
	}
	if !strings.Contains(out, "x-internal-note") {
		t.Errorf("expected x-internal-note preserved: %s", out)
	}
	if !strings.Contains(out, "x-rate-limit") {
		t.Errorf("expected x-rate-limit preserved: %s", out)
	}
}

func TestNormalizeOpenAPIYAML_DealiasesAnchors(t *testing.T) {
	withAnchors := `openapi: 3.0.3
info:
  title: x
  version: "1"
paths:
  /a:
    get:
      responses:
        "200": &okResp
          description: ok
  /b:
    get:
      responses:
        "200": *okResp
`
	expanded := `openapi: 3.0.3
info:
  title: x
  version: "1"
paths:
  /a:
    get:
      responses:
        "200":
          description: ok
  /b:
    get:
      responses:
        "200":
          description: ok
`
	a, err := normalizeOpenAPIYAML(withAnchors)
	if err != nil {
		t.Fatalf("%v", err)
	}
	b, err := normalizeOpenAPIYAML(expanded)
	if err != nil {
		t.Fatalf("%v", err)
	}
	if a != b {
		t.Errorf("expected anchors dealiased to match expanded form:\nWITH ANCHORS=%s\nEXPANDED=%s", a, b)
	}
}

func TestNormalizeOpenAPIYAML_Idempotent(t *testing.T) {
	src := `openapi: 3.0.3
info:
  title: x
  version: "1"
paths:
  /health:
    get:
      responses:
        "200":
          description: ok
`
	once, err := normalizeOpenAPIYAML(src)
	if err != nil {
		t.Fatalf("%v", err)
	}
	twice, err := normalizeOpenAPIYAML(once)
	if err != nil {
		t.Fatalf("%v", err)
	}
	if once != twice {
		t.Errorf("normalization not idempotent:\nFIRST=%s\nSECOND=%s", once, twice)
	}
}

func TestNormalizeDesignJSON_Idempotent(t *testing.T) {
	df := struct {
		Overview     string                   `json:"overview"`
		Requirements []string                 `json:"requirements"`
		Components   []models.DesignComponent `json:"components"`
		SourceSpec   string                   `json:"sourceSpec,omitempty"`
	}{
		Overview:     "x",
		Requirements: []string{"r1"},
		Components: []models.DesignComponent{
			{
				Name:                       "todo-api",
				ComponentType:              "service",
				Language:                   "Go",
				DependsOn:                  []string{},
				Entrypoint:                 "deployment/service",
				Buildpack:                  "docker",
				AppPath:                    "/todo-api",
				ComponentAgentInstructions: "go",
				OpenAPISpec: `openapi: 3.0.3
info:
  title: t
  version: "1"
paths:
  /health:
    get:
      responses:
        "200":
          description: ok
`,
			},
		},
	}
	raw, err := json.MarshalIndent(df, "", "  ")
	if err != nil {
		t.Fatalf("%v", err)
	}
	once, err := normalizeDesignJSON(raw)
	if err != nil {
		t.Fatalf("%v", err)
	}
	twice, err := normalizeDesignJSON(once)
	if err != nil {
		t.Fatalf("%v", err)
	}
	if string(once) != string(twice) {
		t.Errorf("normalizeDesignJSON not idempotent:\nFIRST=%s\nSECOND=%s", once, twice)
	}
}
