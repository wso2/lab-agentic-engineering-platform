package services

import (
	"testing"

	"github.com/wso2/asdlc/asdlc-service/models"
)

func TestResolveAPISecurityEnabled(t *testing.T) {
	cases := []struct {
		name    string
		exposes *models.ExposesAPI
		want    bool
	}{
		{"nil block", nil, false},
		{"empty auth", &models.ExposesAPI{Auth: ""}, false},
		{"none", &models.ExposesAPI{Auth: "none"}, false},
		{"end-user-required", &models.ExposesAPI{Auth: "end-user-required"}, true},
		{"service-required", &models.ExposesAPI{Auth: "service-required"}, true},
		{"whitespace tolerant", &models.ExposesAPI{Auth: "  end-user-required  "}, true},
		{"unrecognised value defensive false", &models.ExposesAPI{Auth: "yes"}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := ResolveAPISecurityEnabled(models.DesignComponent{ExposesAPI: c.exposes})
			if got != c.want {
				t.Fatalf("got %v, want %v", got, c.want)
			}
		})
	}
}

func TestResolveAPISecurityCallerKind(t *testing.T) {
	cases := []struct {
		name    string
		exposes *models.ExposesAPI
		want    string
	}{
		{"nil block", nil, ""},
		{"none", &models.ExposesAPI{Auth: "none"}, ""},
		{"end-user-required", &models.ExposesAPI{Auth: "end-user-required"}, "end-user"},
		{"service-required", &models.ExposesAPI{Auth: "service-required"}, "service"},
		{"unrecognised", &models.ExposesAPI{Auth: "yes"}, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := ResolveAPISecurityCallerKind(models.DesignComponent{ExposesAPI: c.exposes})
			if got != c.want {
				t.Fatalf("got %q, want %q", got, c.want)
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
