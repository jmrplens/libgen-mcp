package libgen

import (
	"context"
	"errors"
	"testing"
)

// TestNISTSupports verifies the source claims every 10.6028 DOI — the standards
// series and the Journal of Research alike, since both resolve to a PDF — refuses
// other registrants, and names itself "nist".
func TestNISTSupports(t *testing.T) {
	s := nistSource{}
	cases := []struct {
		name string
		item Item
		want bool
	}{
		{name: "special publication", item: Item{DOI: "10.6028/NIST.SP.800-53r5"}, want: true},
		{name: "fips", item: Item{DOI: "10.6028/NIST.FIPS.197"}, want: true},
		{name: "internal report", item: Item{DOI: "10.6028/NIST.IR.8259"}, want: true},
		{name: "journal of research", item: Item{DOI: "10.6028/jres.121.004"}, want: true},
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
	if s.Name() != "nist" {
		t.Errorf("Name() = %q, want %q", s.Name(), "nist")
	}
}

// TestNISTResolve verifies a NIST DOI resolves to its DOI-resolver URL — which
// redirects to the PDF in NIST's repository — with a pdf extension and MD5
// verification off.
func TestNISTResolve(t *testing.T) {
	const doi = "10.6028/NIST.SP.800-53r5"
	got, err := nistSource{}.Resolve(context.Background(), Item{DOI: doi})
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	if want := "https://doi.org/" + doi; got.FileURL != want {
		t.Errorf("FileURL = %q, want %q (DOI slashes must stay literal)", got.FileURL, want)
	}
	if got.Ext != "pdf" {
		t.Errorf("Ext = %q, want %q", got.Ext, "pdf")
	}
	if got.VerifyMD5 {
		t.Error("VerifyMD5 = true, want false for a DOI-keyed item")
	}
}

// TestNISTResolveEscapesDOI verifies the resolver URL is built against the
// configured base and percent-encodes a DOI's unsafe characters while leaving its
// slashes literal.
func TestNISTResolveEscapesDOI(t *testing.T) {
	got, err := nistSource{doiBase: "https://example.test/"}.Resolve(
		context.Background(), Item{DOI: "10.6028/NIST.SP.800 53"})
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	if want := "https://example.test/10.6028/NIST.SP.800%2053"; got.FileURL != want {
		t.Errorf("FileURL = %q, want %q", got.FileURL, want)
	}
}

// TestNISTResolveRejectsOtherRegistrant verifies a DOI the source does not claim
// yields ErrNotIndexed instead of a resolver URL for someone else's publication.
func TestNISTResolveRejectsOtherRegistrant(t *testing.T) {
	_, err := nistSource{}.Resolve(context.Background(), Item{DOI: "10.1101/2020.12.30.424878"})
	if !errors.Is(err, ErrNotIndexed) {
		t.Fatalf("Resolve() error = %v, want ErrNotIndexed", err)
	}
}
