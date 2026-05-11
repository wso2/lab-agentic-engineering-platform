package services

import "testing"

func TestPathsMatchComponent(t *testing.T) {
	cases := []struct {
		name     string
		appPath  string
		paths    []string
		want     bool
	}{
		{
			name:    "empty appPath matches anything",
			appPath: "",
			paths:   []string{"greeting-api/main.go"},
			want:    true,
		},
		{
			name:    "no changed paths is pessimistic match",
			appPath: "/greeting-api",
			paths:   nil,
			want:    true,
		},
		{
			name:    "leading slash on appPath still matches",
			appPath: "/greeting-api",
			paths:   []string{"greeting-api/main.go"},
			want:    true,
		},
		{
			name:    "no slashes on appPath",
			appPath: "greeting-api",
			paths:   []string{"greeting-api/main.go"},
			want:    true,
		},
		{
			name:    "trailing slash on appPath still matches",
			appPath: "greeting-api/",
			paths:   []string{"greeting-api/main.go"},
			want:    true,
		},
		{
			name:    "exact match on appPath without trailing path",
			appPath: "/greeting-api",
			paths:   []string{"greeting-api"},
			want:    true,
		},
		{
			name:    "mismatch returns false",
			appPath: "/greeting-api",
			paths:   []string{"other-svc/main.go"},
			want:    false,
		},
		{
			name:    "prefix collision is not a match",
			appPath: "/greeting",
			paths:   []string{"greeting-api/main.go"},
			want:    false,
		},
		{
			name:    "slash-only appPath matches anything",
			appPath: "/",
			paths:   []string{"anything/foo"},
			want:    true,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := pathsMatchComponent(c.appPath, c.paths)
			if got != c.want {
				t.Errorf("pathsMatchComponent(%q, %v) = %v, want %v", c.appPath, c.paths, got, c.want)
			}
		})
	}
}
