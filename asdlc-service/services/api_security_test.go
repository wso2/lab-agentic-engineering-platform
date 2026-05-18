package services

import (
	"testing"

	"github.com/wso2/asdlc/asdlc-service/models"
)

func TestResolveAPISecurityEnabled(t *testing.T) {
	cases := []struct {
		name string
		api  *models.APISecurity
		want bool
	}{
		{"nil block", nil, false},
		{"empty security", &models.APISecurity{Security: ""}, false},
		{"none", &models.APISecurity{Security: "none"}, false},
		{"required", &models.APISecurity{Security: "required"}, true},
		{"REQUIRED — case insensitive", &models.APISecurity{Security: "REQUIRED"}, true},
		{"whitespace tolerant", &models.APISecurity{Security: "  required  "}, true},
		{"unrecognised value defensive false", &models.APISecurity{Security: "yes"}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := ResolveAPISecurityEnabled(models.DesignComponent{Api: c.api})
			if got != c.want {
				t.Fatalf("got %v, want %v", got, c.want)
			}
		})
	}
}

func TestNormalizeExternalURL(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"", ""},
		{"/", "/"},
		{"/foo", "/foo/"},
		{"/foo/", "/foo/"},
		{"/foo///", "/foo/"},
		{"http://x/y/z", "http://x/y/z/"},
		{"http://x/y/z/", "http://x/y/z/"},
	}
	for _, c := range cases {
		t.Run(c.in, func(t *testing.T) {
			if got := NormalizeExternalURL(c.in); got != c.want {
				t.Fatalf("NormalizeExternalURL(%q) = %q, want %q", c.in, got, c.want)
			}
		})
	}
}
