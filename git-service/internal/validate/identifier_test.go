package validate

import "testing"

func TestSlug(t *testing.T) {
	cases := []struct {
		name string
		in   string
		ok   bool
	}{
		{"valid lowercase", "myorg", true},
		{"valid hyphenated", "my-org-2", true},
		{"valid digits-only", "1234", true},
		{"max length 63", "a234567890123456789012345678901234567890123456789012345678901234"[:63], true},
		{"empty", "", false},
		{"too long 64", "a23456789012345678901234567890123456789012345678901234567890123456"[:64], false},
		{"uppercase", "MyOrg", false},
		{"path traversal", "../foo", false},
		{"slash", "foo/bar", false},
		{"url-encoded slash", "foo%2fbar", false},
		{"newline", "foo\nbar", false},
		{"null byte", "foo\x00bar", false},
		{"leading hyphen", "-foo", false},
		{"trailing dot", "foo.", false},
		{"underscore", "foo_bar", false},
		{"space", "foo bar", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := Slug(tc.in)
			if tc.ok && err != nil {
				t.Errorf("Slug(%q) = %v; want nil", tc.in, err)
			}
			if !tc.ok && err == nil {
				t.Errorf("Slug(%q) = nil; want error", tc.in)
			}
		})
	}
}

func TestUUID(t *testing.T) {
	cases := []struct {
		name string
		in   string
		ok   bool
	}{
		{"valid v4", "f47ac10b-58cc-4372-a567-0e02b2c3d479", true},
		{"valid uppercase", "F47AC10B-58CC-4372-A567-0E02B2C3D479", true},
		{"empty", "", false},
		{"missing hyphens", "f47ac10b58cc4372a5670e02b2c3d479", false},
		{"too short", "f47ac10b-58cc-4372-a567-0e02b2c3d4", false},
		{"newline injection", "f47ac10b-58cc-4372-a567-0e02b2c3d479\nfoo", false},
		{"path traversal", "../foo", false},
		{"slug-shaped", "myorg", false},
		{"surrounding braces", "{f47ac10b-58cc-4372-a567-0e02b2c3d479}", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := UUID(tc.in)
			if tc.ok && err != nil {
				t.Errorf("UUID(%q) = %v; want nil", tc.in, err)
			}
			if !tc.ok && err == nil {
				t.Errorf("UUID(%q) = nil; want error", tc.in)
			}
		})
	}
}
