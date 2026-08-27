package transport

import (
	"slices"
	"testing"
)

// TestParseTrustedOrigins covers the shapes an operator can write, including
// the ones that must fail startup rather than be silently dropped.
func TestParseTrustedOrigins(t *testing.T) {
	cases := []struct {
		name    string
		raw     string
		want    []string
		wantErr bool
	}{
		{name: "empty is no origins", raw: "", want: nil},
		{name: "whitespace only", raw: "   ", want: nil},
		{name: "one origin", raw: "https://claude.ai", want: []string{"https://claude.ai"}},
		{name: "several, spaced", raw: "https://claude.ai, http://localhost:3000", want: []string{"https://claude.ai", "http://localhost:3000"}},
		{name: "wildcard alone", raw: "*", want: []string{AnyOrigin}},
		{name: "wildcard in a list is refused", raw: "https://claude.ai,*", wantErr: true},
		{name: "trailing slash can never match an Origin header", raw: "https://claude.ai/", wantErr: true},
		{name: "a path can never match either", raw: "https://claude.ai/mcp", wantErr: true},
		{name: "bare host has no scheme", raw: "claude.ai", wantErr: true},
		{name: "non-http scheme", raw: "ftp://claude.ai", wantErr: true},
		{name: "userinfo", raw: "https://user@claude.ai", wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ParseTrustedOrigins(tc.raw)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("ParseTrustedOrigins(%q) = %v, want an error", tc.raw, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("ParseTrustedOrigins(%q) error: %v", tc.raw, err)
			}
			if !slices.Equal(got, tc.want) {
				t.Errorf("ParseTrustedOrigins(%q) = %v, want %v", tc.raw, got, tc.want)
			}
		})
	}
}

// TestTrusts pins the matching rule, including that an absent Origin is never
// trusted — a non-browser client sends none, and it must reach the protection
// (which allows it) rather than be handled as a trusted browser.
func TestTrusts(t *testing.T) {
	list := []string{"https://claude.ai"}
	cases := []struct {
		origins []string
		origin  string
		want    bool
	}{
		{list, "https://claude.ai", true},
		{list, "https://evil.example", false},
		{list, "", false},
		{nil, "https://claude.ai", false},
		{[]string{AnyOrigin}, "https://anything.example", true},
		{[]string{AnyOrigin}, "", false},
	}
	for _, tc := range cases {
		if got := Trusts(tc.origins, tc.origin); got != tc.want {
			t.Errorf("Trusts(%v, %q) = %v, want %v", tc.origins, tc.origin, got, tc.want)
		}
	}
}
