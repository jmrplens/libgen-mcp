package libgen

import (
	"context"
	"errors"
	"testing"
)

// TestACLSupports verifies the source claims well-formed ACL Anthology DOIs under
// either registrant prefix, refuses everything else — including the pre-2014
// numeric form that shares the 10.3115 prefix but names an ACM article rather than
// an Anthology document — and names itself "acl".
func TestACLSupports(t *testing.T) {
	s := aclSource{}
	cases := []struct {
		name string
		item Item
		want bool
	}{
		{name: "modern id", item: Item{DOI: "10.18653/v1/2024.emnlp-main.856"}, want: true},
		{name: "volume-lettered id", item: Item{DOI: "10.18653/v1/N19-1423"}, want: true},
		{name: "legacy prefix with v1", item: Item{DOI: "10.3115/v1/P14-1001"}, want: true},
		{name: "lowercase volume letter", item: Item{DOI: "10.18653/v1/n19-1423"}, want: true},
		{name: "numeric acm form", item: Item{DOI: "10.3115/1072228.1072256"}, want: false},
		{name: "no version segment", item: Item{DOI: "10.18653/N19-1423"}, want: false},
		{name: "version segment only", item: Item{DOI: "10.18653/v1/"}, want: false},
		{name: "nested path in id", item: Item{DOI: "10.18653/v1/a/b"}, want: false},
		{name: "non-ascii id", item: Item{DOI: "10.18653/v1/Ñ19-1423"}, want: false},
		{name: "space in id", item: Item{DOI: "10.18653/v1/N19 1423"}, want: false},
		{name: "other registrant", item: Item{DOI: "10.17487/RFC9110"}, want: false},
		{name: "md5 only", item: Item{MD5: "87a4ebdaf21fa6cc70009a3dd63194ee"}, want: false},
		{name: "empty", item: Item{}, want: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := s.Supports(tc.item); got != tc.want {
				t.Errorf("Supports(%+v) = %v, want %v", tc.item, got, tc.want)
			}
		})
	}
	if s.Name() != "acl" {
		t.Errorf("Name() = %q, want %q", s.Name(), "acl")
	}
}

// TestACLResolveCaseRule pins the one rule the whole source turns on: a
// volume-lettered identifier is uppercased and a year-prefixed one is left alone,
// because aclanthology.org serves each generation under exactly one spelling and
// answers 404 to the other. Both halves are asserted, since normalizing in either
// direction unconditionally would break one id generation entirely.
func TestACLResolveCaseRule(t *testing.T) {
	cases := []struct{ name, doi, want string }{
		{
			name: "volume-lettered id is uppercased",
			doi:  "10.18653/v1/n19-1423",
			want: "https://example.test/N19-1423.pdf",
		},
		{
			name: "volume-lettered id already uppercase is unchanged",
			doi:  "10.18653/v1/N19-1423",
			want: "https://example.test/N19-1423.pdf",
		},
		{
			name: "year-prefixed id keeps its lowercase",
			doi:  "10.18653/v1/2024.emnlp-main.856",
			want: "https://example.test/2024.emnlp-main.856.pdf",
		},
		{
			name: "year-prefixed id is never uppercased",
			doi:  "10.18653/v1/2021.naacl-main.41",
			want: "https://example.test/2021.naacl-main.41.pdf",
		},
		{
			name: "legacy registrant resolves the same way",
			doi:  "10.3115/v1/p14-1001",
			want: "https://example.test/P14-1001.pdf",
		},
	}
	s := aclSource{docRoot: "https://example.test/"}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := s.Resolve(context.Background(), Item{DOI: tc.doi})
			if err != nil {
				t.Fatalf("Resolve() error = %v", err)
			}
			if got.FileURL != tc.want {
				t.Errorf("FileURL = %q, want %q", got.FileURL, tc.want)
			}
		})
	}
}

// TestACLResolve verifies an Anthology DOI resolves against the production
// document root with a pdf extension and MD5 verification off.
func TestACLResolve(t *testing.T) {
	got, err := aclSource{}.Resolve(context.Background(), Item{DOI: "10.18653/v1/N19-1423"})
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	if want := "https://aclanthology.org/N19-1423.pdf"; got.FileURL != want {
		t.Errorf("FileURL = %q, want %q", got.FileURL, want)
	}
	if got.Ext != "pdf" {
		t.Errorf("Ext = %q, want %q", got.Ext, "pdf")
	}
	if got.VerifyMD5 {
		t.Error("VerifyMD5 = true, want false for a DOI-keyed item")
	}
}

// TestACLResolveRejectsNonAnthology verifies every DOI shape the source declines
// yields ErrNotIndexed rather than a URL that could not exist, so the chain moves
// on cleanly instead of fetching a guaranteed 404.
func TestACLResolveRejectsNonAnthology(t *testing.T) {
	for _, doi := range []string{
		"10.3115/1072228.1072256",
		"10.18653/N19-1423",
		"10.18653/v1/",
		"10.18653/v1/a/b",
		"10.17487/RFC9110",
	} {
		t.Run(doi, func(t *testing.T) {
			_, err := aclSource{}.Resolve(context.Background(), Item{DOI: doi})
			if !errors.Is(err, ErrNotIndexed) {
				t.Fatalf("Resolve(%q) error = %v, want ErrNotIndexed", doi, err)
			}
		})
	}
}
