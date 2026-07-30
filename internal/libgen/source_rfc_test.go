package libgen

import (
	"context"
	"errors"
	"testing"
)

// TestRFCSupports verifies the source claims well-formed RFC DOIs in either case,
// refuses everything else — including a 10.17487 DOI whose suffix is not an RFC
// number — and names itself "rfc".
func TestRFCSupports(t *testing.T) {
	s := rfcSource{}
	cases := []struct {
		name string
		item Item
		want bool
	}{
		{name: "rfc doi", item: Item{DOI: "10.17487/RFC9110"}, want: true},
		{name: "lowercase token", item: Item{DOI: "10.17487/rfc791"}, want: true},
		{name: "other registrant", item: Item{DOI: "10.1101/2020.12.30.424878"}, want: false},
		{name: "right prefix wrong suffix", item: Item{DOI: "10.17487/errata9110"}, want: false},
		{name: "token but no number", item: Item{DOI: "10.17487/RFC"}, want: false},
		{name: "trailing junk", item: Item{DOI: "10.17487/RFC9110v2"}, want: false},
		{name: "zero", item: Item{DOI: "10.17487/RFC0"}, want: false},
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
	if s.Name() != "rfc" {
		t.Errorf("Name() = %q, want %q", s.Name(), "rfc")
	}
}

// TestRFCResolve verifies an RFC DOI resolves to the plain-text document at the
// RFC Editor, with a txt extension and MD5 verification off. The text format is
// asserted rather than PDF because PDF coverage is irregular across RFCs.
func TestRFCResolve(t *testing.T) {
	s := rfcSource{}
	got, err := s.Resolve(context.Background(), Item{DOI: "10.17487/RFC9110"})
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	if want := "https://www.rfc-editor.org/rfc/rfc9110.txt"; got.FileURL != want {
		t.Errorf("FileURL = %q, want %q", got.FileURL, want)
	}
	if got.Ext != "txt" {
		t.Errorf("Ext = %q, want %q", got.Ext, "txt")
	}
	if got.VerifyMD5 {
		t.Error("VerifyMD5 = true, want false for a DOI-keyed item")
	}
}

// TestRFCResolveNormalizesNumber verifies the built URL uses the unpadded RFC
// number the RFC Editor's filenames use, whatever padding or case the caller's DOI
// carried.
func TestRFCResolveNormalizesNumber(t *testing.T) {
	cases := []struct{ doi, want string }{
		{doi: "10.17487/RFC791", want: "https://example.test/rfc791.txt"},
		{doi: "10.17487/RFC0791", want: "https://example.test/rfc791.txt"},
		{doi: "10.17487/rfc1", want: "https://example.test/rfc1.txt"},
	}
	s := rfcSource{docRoot: "https://example.test/"}
	for _, tc := range cases {
		t.Run(tc.doi, func(t *testing.T) {
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

// TestRFCResolveRejectsNonRFC verifies a DOI the source does not claim yields
// ErrNotIndexed rather than a URL that could not exist, so the chain moves on
// cleanly instead of fetching a guaranteed 404.
func TestRFCResolveRejectsNonRFC(t *testing.T) {
	_, err := rfcSource{}.Resolve(context.Background(), Item{DOI: "10.17487/errata9110"})
	if !errors.Is(err, ErrNotIndexed) {
		t.Fatalf("Resolve() error = %v, want ErrNotIndexed", err)
	}
}
