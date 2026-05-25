package services

import (
	"strings"
	"testing"
)

// Test_renderEnvConfigJS — the file content must be deterministic + valid
// JS. Keys are sorted so identical inputs produce byte-identical files.
func Test_renderEnvConfigJS(t *testing.T) {
	values := map[string]interface{}{
		"API_BASE_URL":          "http://development-default.openchoreoapis.localhost:19080/todo-api-http",
		"TODO_API_URL":          "http://development-default.openchoreoapis.localhost:19080/todo-api-http",
		"SUPPORT_EMAIL":         "support@example.com",
		"FEATURE_NEW_DASHBOARD": false,
	}
	got := renderEnvConfigJS(values)

	for _, want := range []string{
		"window._env_ = {",
		`API_BASE_URL: "http://development-default.openchoreoapis.localhost:19080/todo-api-http"`,
		`FEATURE_NEW_DASHBOARD: false`,
		`SUPPORT_EMAIL: "support@example.com"`,
		`TODO_API_URL: "http://development-default.openchoreoapis.localhost:19080/todo-api-http"`,
		"};",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("renderEnvConfigJS output missing %q\ngot:\n%s", want, got)
		}
	}

	// Verify keys are emitted in sorted order so the rendered JS is
	// byte-stable (so equality checks against the on-cluster file
	// don't flap).
	wantOrder := []string{"API_BASE_URL", "FEATURE_NEW_DASHBOARD", "SUPPORT_EMAIL", "TODO_API_URL"}
	prev := 0
	for _, k := range wantOrder {
		i := strings.Index(got, k+":")
		if i < 0 {
			t.Fatalf("key %s not present in output", k)
		}
		if i < prev {
			t.Errorf("key %s appears before prior key (got pos %d, want > %d)\noutput:\n%s", k, i, prev, got)
		}
		prev = i
	}
}

// Test_upperSnakeKey — kebab-case + dash + camelCase normalise to safe
// JS identifier prefixes.
func Test_upperSnakeKey(t *testing.T) {
	cases := []struct{ in, want string }{
		{"todo-api", "TODO_API"},
		{"todo_api", "TODO_API"},
		{"TodoApi", "TODOAPI"},
		{"todo--api", "TODO_API"},
		{"--todo-api--", "TODO_API"},
		{"", ""},
		{"a", "A"},
		{"a-b-c", "A_B_C"},
		{"todo-api-v2", "TODO_API_V2"},
	}
	for _, c := range cases {
		got := upperSnakeKey(c.in)
		if got != c.want {
			t.Errorf("upperSnakeKey(%q) = %q; want %q", c.in, got, c.want)
		}
	}
}

// Test_originFromEndpointURL — extracts scheme://authority from a
// ListDeployments-shaped URL with trailing path/query/fragment.
func Test_originFromEndpointURL(t *testing.T) {
	cases := []struct{ in, want string }{
		{"http://host.example:19080/", "http://host.example:19080"},
		{"http://host.example:19080", "http://host.example:19080"},
		{"http://host.example:19080/path/here", "http://host.example:19080"},
		{"https://host.example/abc?q=1", "https://host.example"},
		{"", ""},
		{"not-a-url", ""},
	}
	for _, c := range cases {
		got := originFromEndpointURL(c.in)
		if got != c.want {
			t.Errorf("originFromEndpointURL(%q) = %q; want %q", c.in, got, c.want)
		}
	}
}
