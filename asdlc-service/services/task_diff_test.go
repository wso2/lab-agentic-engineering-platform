package services

import (
	"strings"
	"testing"

	"github.com/wso2/asdlc/asdlc-service/models"
)

func mkComp(name, lang string, dependsOn []string, openapi string) models.DesignComponent {
	return models.DesignComponent{
		Name:          name,
		ComponentType: "service",
		Language:      lang,
		DependsOn:     dependsOn,
		OpenAPISpec:   openapi,
	}
}

const apiV1 = `openapi: 3.0.0
paths:
  /todos:
    get:
      responses:
        "200":
          description: ok
  /todos/{id}:
    get:
      responses:
        "200":
          description: ok
`

const apiV2 = `openapi: 3.0.0
paths:
  /todos:
    get:
      responses:
        "200":
          description: ok
    post:
      responses:
        "201":
          description: created
  /todos/{id}:
    get:
      responses:
        "200":
          description: ok
`

func TestComputeDesignDiff_AddedRemoved(t *testing.T) {
	prev := []models.DesignComponent{
		mkComp("todo-api", "Go", nil, apiV1),
		mkComp("todo-web", "TypeScript", []string{"todo-api"}, ""),
	}
	curr := []models.DesignComponent{
		mkComp("todo-api", "Go", nil, apiV1),
		mkComp("notify-svc", "Go", nil, ""),
	}
	diff := computeDesignDiff(prev, curr)
	if len(diff.Added) != 1 || diff.Added[0].Name != "notify-svc" {
		t.Fatalf("added: %+v", diff.Added)
	}
	if len(diff.Removed) != 1 || diff.Removed[0].Name != "todo-web" {
		t.Fatalf("removed: %+v", diff.Removed)
	}
	if diff.IsTrivial() {
		t.Fatalf("expected non-trivial; ADDED present")
	}
}

func TestComputeDesignDiff_ModifiedOpenAPI(t *testing.T) {
	prev := []models.DesignComponent{mkComp("todo-api", "Go", nil, apiV1)}
	curr := []models.DesignComponent{mkComp("todo-api", "Go", nil, apiV2)}
	diff := computeDesignDiff(prev, curr)
	if len(diff.Modified) != 1 {
		t.Fatalf("modified: %+v", diff.Modified)
	}
	mod := diff.Modified[0]
	if !mod.ContractAffected {
		t.Fatalf("expected ContractAffected on openapi op add")
	}
	if len(mod.OpenAPIOps) == 0 {
		t.Fatalf("expected OpenAPIOps; got: %+v", mod.OpenAPIOps)
	}
	found := false
	for _, op := range mod.OpenAPIOps {
		if strings.HasPrefix(op, "+ POST /todos") {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected '+ POST /todos' in ops; got %v", mod.OpenAPIOps)
	}
}

func TestComputeDesignDiff_ModifiedDependsOn(t *testing.T) {
	prev := []models.DesignComponent{
		mkComp("todo-web", "TypeScript", []string{"todo-api"}, ""),
	}
	curr := []models.DesignComponent{
		mkComp("todo-web", "TypeScript", []string{"todo-api", "auth-svc"}, ""),
	}
	diff := computeDesignDiff(prev, curr)
	if len(diff.Modified) != 1 {
		t.Fatalf("modified: %+v", diff.Modified)
	}
	mod := diff.Modified[0]
	if len(mod.DependsOnAdded) != 1 || mod.DependsOnAdded[0] != "auth-svc" {
		t.Fatalf("expected DependsOnAdded=[auth-svc]; got %v", mod.DependsOnAdded)
	}
	if !mod.ContractAffected {
		t.Fatalf("expected ContractAffected on dependsOn change")
	}
}

func TestComputeDesignDiff_Trivial(t *testing.T) {
	c := []models.DesignComponent{mkComp("todo-api", "Go", nil, apiV1)}
	diff := computeDesignDiff(c, c)
	if !diff.IsTrivial() {
		t.Fatalf("identical designs should produce trivial diff: %+v", diff)
	}
}

func TestRenderDesignDiffForPrompt(t *testing.T) {
	d := DesignDiff{
		Added:   []DesignDiffAdded{{Name: "notify-svc", ComponentType: "service", Language: "Go"}},
		Removed: []DesignDiffRemoved{{Name: "legacy-svc"}},
		Modified: []DesignDiffModified{{
			Name:             "todo-api",
			DependsOnAdded:   []string{"auth-svc"},
			OpenAPIOps:       []string{"+ POST /todos"},
			ContractAffected: true,
		}},
	}
	out := renderDesignDiffForPrompt(d)
	if !strings.Contains(out, "ADDED component: notify-svc") {
		t.Fatalf("missing ADDED: %s", out)
	}
	if !strings.Contains(out, "REMOVED component: legacy-svc") {
		t.Fatalf("missing REMOVED: %s", out)
	}
	if !strings.Contains(out, "MODIFIED component: todo-api") {
		t.Fatalf("missing MODIFIED: %s", out)
	}
	if !strings.Contains(out, "+ POST /todos") {
		t.Fatalf("missing op line: %s", out)
	}
}

func TestComputeSpecDiff_FreshAddsAllLines(t *testing.T) {
	out := computeSpecDiff("", "hello\nworld")
	if !strings.Contains(out, "+ hello") || !strings.Contains(out, "+ world") {
		t.Fatalf("expected both lines as added; got %q", out)
	}
}

func TestComputeSpecDiff_AddRemove(t *testing.T) {
	out := computeSpecDiff("alpha\nbeta\n", "alpha\ngamma\n")
	if !strings.Contains(out, "+ gamma") {
		t.Fatalf("expected gamma added; got %q", out)
	}
	if !strings.Contains(out, "- beta") {
		t.Fatalf("expected beta removed; got %q", out)
	}
	if strings.Contains(out, "+ alpha") || strings.Contains(out, "- alpha") {
		t.Fatalf("alpha should not appear; got %q", out)
	}
}
