package credentials

import (
	"errors"
	"strings"
	"testing"
)

func TestValidateOrgID_Empty(t *testing.T) {
	if err := validateOrgID(""); !errors.Is(err, ErrOrgIDInvalid) {
		t.Fatalf("validateOrgID(\"\") = %v; want ErrOrgIDInvalid", err)
	}
}

func TestValidateOrgID_Reserved(t *testing.T) {
	cases := []string{"_platform", "_other", "_x"}
	for _, c := range cases {
		t.Run(c, func(t *testing.T) {
			err := validateOrgID(c)
			if !errors.Is(err, ErrOrgIDInvalid) {
				t.Errorf("validateOrgID(%q) = %v; want ErrOrgIDInvalid", c, err)
			}
		})
	}
}

func TestValidateOrgID_Shape(t *testing.T) {
	cases := map[string]bool{
		"default":          true,
		"my-org":           true,
		"abc-123":          true,
		"a":                true,
		"My-Org":           false, // uppercase not allowed
		"-leading-dash":    false,
		"trailing-dash-":   true, // matches DNS-label pattern (any non-leading char OK)
		"con tains space":  false,
		"slashes/in/path":  false,
		"under_score":      false,
		strings.Repeat("a", 64): false, // exceeds 63-char limit
		strings.Repeat("a", 63): true,
	}
	for input, valid := range cases {
		err := validateOrgID(input)
		if valid && err != nil {
			t.Errorf("validateOrgID(%q) = %v; want nil", input, err)
		}
		if !valid && err == nil {
			t.Errorf("validateOrgID(%q) = nil; want error", input)
		}
	}
}

func TestOpenBaoStore_PathConstruction(t *testing.T) {
	s := &openBaoStore{mount: "secret", owner: "asdlc-git-service"}

	p, err := s.path("default", "github/pat")
	if err != nil {
		t.Fatalf("path: %v", err)
	}
	want := "secret/data/asdlc/default/github/pat"
	if p != want {
		t.Errorf("path = %q; want %q", p, want)
	}

	mp, err := s.metadataPath("default", "github/pat")
	if err != nil {
		t.Fatalf("metadataPath: %v", err)
	}
	wantMeta := "secret/metadata/asdlc/default/github/pat"
	if mp != wantMeta {
		t.Errorf("metadataPath = %q; want %q", mp, wantMeta)
	}

	pp := s.platformPath("github/app/private_key")
	wantPlat := "secret/data/asdlc/_platform/github/app/private_key"
	if pp != wantPlat {
		t.Errorf("platformPath = %q; want %q", pp, wantPlat)
	}
}

func TestOpenBaoStore_PathRejectsBadOrgID(t *testing.T) {
	s := &openBaoStore{mount: "secret", owner: "asdlc-git-service"}

	_, err := s.path("_platform", "github/pat")
	if !errors.Is(err, ErrOrgIDInvalid) {
		t.Errorf("path(_platform) = %v; want ErrOrgIDInvalid", err)
	}

	_, err = s.path("UPPERCASE", "github/pat")
	if !errors.Is(err, ErrOrgIDInvalid) {
		t.Errorf("path(UPPERCASE) = %v; want ErrOrgIDInvalid", err)
	}
}

func TestOpenBaoStore_PathRejectsBadKey(t *testing.T) {
	s := &openBaoStore{mount: "secret", owner: "asdlc-git-service"}

	_, err := s.path("default", "")
	if err == nil {
		t.Error("path with empty key = nil; want error")
	}

	_, err = s.path("default", "/abs/path")
	if err == nil {
		t.Error("path with leading-slash key = nil; want error")
	}
}

func TestRedactPath(t *testing.T) {
	cases := map[string]string{
		"secret/data/asdlc/default/github/pat":           "secret/data/asdlc/default/<redacted>",
		"secret/data/asdlc/_platform/github/app/private": "secret/data/asdlc/_platform/<redacted>",
		"unexpected/shape":                               "<redacted>",
	}
	for in, want := range cases {
		got := redactPath(in)
		if got != want {
			t.Errorf("redactPath(%q) = %q; want %q", in, got, want)
		}
	}
}
