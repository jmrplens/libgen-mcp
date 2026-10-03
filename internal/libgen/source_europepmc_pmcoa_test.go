// Tests for the PMC Article Datasets half of the europepmc source.

package libgen

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"testing"
)

// pmcOAFixture reads a recorded bucket fixture from testdata.
func pmcOAFixture(t *testing.T, name string) []byte {
	t.Helper()
	body, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatalf("reading fixture %s: %v", name, err)
	}
	return body
}

// pmcOABucket describes what the stand-in bucket answers: the listing body (and
// a status, when non-zero, in its place), each version's metadata JSON keyed by
// version prefix, and whether a .pdf object serves PDF bytes.
type pmcOABucket struct {
	// listing is the ListObjectsV2 body served at the bucket root.
	listing []byte
	// listStatus, when non-zero, is answered at the root instead of the listing.
	listStatus int
	// meta maps a version prefix such as "PMC4991899.1" to its metadata JSON.
	meta map[string][]byte
	// metaStatus, when non-zero, is answered for every metadata request.
	metaStatus int
	// servePDF makes every .pdf object answer with PDF bytes; otherwise 403.
	servePDF bool
}

// serve starts the stand-in bucket. The handler asserts nothing, so it has no
// use for the test's T: what it is asked is checked by what Resolve returns.
func (b pmcOABucket) serve(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/":
			if b.listStatus != 0 {
				w.WriteHeader(b.listStatus)
				return
			}
			w.Header().Set("Content-Type", "application/xml")
			_, _ = w.Write(b.listing)
		case strings.HasSuffix(r.URL.Path, ".json"):
			b.serveMeta(w, r)
		case strings.HasSuffix(r.URL.Path, ".pdf") && b.servePDF:
			// The real bucket labels its PDFs binary/octet-stream, so the probe has
			// to recognize the magic number rather than the media type.
			w.Header().Set("Content-Type", "binary/octet-stream")
			_, _ = w.Write([]byte("%PDF-1.4 pmc payload"))
		default:
			w.WriteHeader(http.StatusForbidden)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

// serveMeta answers a metadata request from the version map.
func (b pmcOABucket) serveMeta(w http.ResponseWriter, r *http.Request) {
	if b.metaStatus != 0 {
		w.WriteHeader(b.metaStatus)
		return
	}
	version, _, _ := strings.Cut(strings.TrimPrefix(r.URL.Path, "/"), "/")
	body, ok := b.meta[version]
	if !ok {
		w.WriteHeader(http.StatusForbidden)
		return
	}
	_, _ = w.Write(body)
}

// pmcOAListingOf renders a listing holding the given version prefixes, the shape
// the bucket answers a delimited ListObjectsV2 with.
func pmcOAListingOf(prefixes ...string) []byte {
	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="UTF-8"?><ListBucketResult xmlns="http://s3.amazonaws.com/doc/2006-03-01/"><Name>pmc-oa-opendata</Name>`)
	for _, p := range prefixes {
		b.WriteString("<CommonPrefixes><Prefix>" + p + "</Prefix></CommonPrefixes>")
	}
	b.WriteString("</ListBucketResult>")
	return []byte(b.String())
}

// pmcOAMetaOf renders a version's metadata JSON with the three fields read.
func pmcOAMetaOf(version string, manuscript, retracted bool) []byte {
	return []byte(`{"pmcid":"PMC4991899","is_manuscript":` + strconv.FormatBool(manuscript) +
		`,"is_retracted":` + strconv.FormatBool(retracted) +
		`,"pdf_url":"s3://pmc-oa-opendata/` + version + `/` + version + `.pdf?md5=00"}`)
}

// TestPDFURL_TakesTheRecordedVersion resolves against metadata recorded from the
// real bucket on 2026-10-03, a published article and an author manuscript, and
// expects the HTTPS address of the object the metadata names, with the md5
// parameter dropped. The PLOS listing is the recorded one too.
func TestPDFURL_TakesTheRecordedVersion(t *testing.T) {
	cases := []struct {
		name  string
		pmcid string
		// listingFile names a recorded listing; empty renders one for version 1.
		listingFile string
		meta        string
	}{
		{name: "published", pmcid: "PMC4991899", listingFile: "pmcoa_list.xml", meta: "pmcoa_meta_published.json"},
		{name: "author manuscript", pmcid: "PMC3836401", meta: "pmcoa_meta_manuscript.json"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			version := tc.pmcid + ".1"
			listing := pmcOAListingOf(version + "/")
			if tc.listingFile != "" {
				listing = pmcOAFixture(t, tc.listingFile)
			}
			bucket := pmcOABucket{
				listing:  listing,
				meta:     map[string][]byte{version: pmcOAFixture(t, tc.meta)},
				servePDF: true,
			}.serve(t)
			s := europePMCSource{http: bucket.Client(), bucketBase: bucket.URL + "/"}

			got, err := s.pdfURL(context.Background(), tc.pmcid)
			if err != nil {
				t.Fatalf("pdfURL() error = %v", err)
			}
			if want := bucket.URL + "/" + version + "/" + version + ".pdf"; got != want {
				t.Errorf("pdfURL() = %q, want %q", got, want)
			}
		})
	}
}

// TestPDFURL_PrefersThePublishedVersion pins the selection order. NCBI says a
// higher version number is not a more recent one, and the usual pair is an author
// manuscript beside the published article, so the published one wins whatever its
// number, and a retracted version is never offered at all.
func TestPDFURL_PrefersThePublishedVersion(t *testing.T) {
	cases := []struct {
		name string
		meta map[string][]byte
		want string
	}{
		{
			name: "published below a manuscript",
			meta: map[string][]byte{
				"PMC4991899.1": pmcOAMetaOf("PMC4991899.1", false, false),
				"PMC4991899.2": pmcOAMetaOf("PMC4991899.2", true, false),
			},
			want: "/PMC4991899.1/PMC4991899.1.pdf",
		},
		{
			name: "manuscripts only takes the highest",
			meta: map[string][]byte{
				"PMC4991899.1": pmcOAMetaOf("PMC4991899.1", true, false),
				"PMC4991899.2": pmcOAMetaOf("PMC4991899.2", true, false),
			},
			want: "/PMC4991899.2/PMC4991899.2.pdf",
		},
		{
			name: "a retracted published version yields to the manuscript",
			meta: map[string][]byte{
				"PMC4991899.1": pmcOAMetaOf("PMC4991899.1", true, false),
				"PMC4991899.2": pmcOAMetaOf("PMC4991899.2", false, true),
			},
			want: "/PMC4991899.1/PMC4991899.1.pdf",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			bucket := pmcOABucket{
				listing:  pmcOAListingOf("PMC4991899.1/", "PMC4991899.2/"),
				meta:     tc.meta,
				servePDF: true,
			}.serve(t)
			s := europePMCSource{http: bucket.Client(), bucketBase: bucket.URL}

			got, err := s.pdfURL(context.Background(), "PMC4991899")
			if err != nil {
				t.Fatalf("pdfURL() error = %v", err)
			}
			if want := bucket.URL + tc.want; got != want {
				t.Errorf("pdfURL() = %q, want %q", got, want)
			}
		})
	}
}

// TestPDFURL_ClassifiesEachFailure pins which failures are an answer about the
// article (a clean miss, so the chain moves on at once) and which are the bucket
// failing (unavailability, which costs the source a cooldown).
func TestPDFURL_ClassifiesEachFailure(t *testing.T) {
	published := map[string][]byte{"PMC4991899.1": pmcOAFixture(t, "pmcoa_meta_published.json")}

	t.Run("an article the dataset does not hold is a clean miss", func(t *testing.T) {
		bucket := pmcOABucket{listing: pmcOAFixture(t, "pmcoa_list_empty.xml")}.serve(t)
		s := europePMCSource{http: bucket.Client(), bucketBase: bucket.URL}
		_, err := s.pdfURL(context.Background(), "PMC4991899")
		assertCleanMiss(t, err)
	})

	t.Run("an article held without a PDF is a clean miss", func(t *testing.T) {
		// Recorded: an author manuscript the dataset carries as XML and text only.
		bucket := pmcOABucket{
			listing: pmcOAListingOf("PMC12788873.1/"),
			meta:    map[string][]byte{"PMC12788873.1": pmcOAFixture(t, "pmcoa_meta_no_pdf.json")},
		}.serve(t)
		s := europePMCSource{http: bucket.Client(), bucketBase: bucket.URL}
		_, err := s.pdfURL(context.Background(), "PMC12788873")
		assertCleanMiss(t, err)
	})

	t.Run("a PDF listed but not served is unavailability", func(t *testing.T) {
		bucket := pmcOABucket{listing: pmcOAFixture(t, "pmcoa_list.xml"), meta: published}.serve(t)
		s := europePMCSource{http: bucket.Client(), bucketBase: bucket.URL}
		_, err := s.pdfURL(context.Background(), "PMC4991899")
		assertUnavailable(t, err)
	})

	t.Run("a failing listing is unavailability", func(t *testing.T) {
		bucket := pmcOABucket{listStatus: http.StatusServiceUnavailable}.serve(t)
		s := europePMCSource{http: bucket.Client(), bucketBase: bucket.URL}
		_, err := s.pdfURL(context.Background(), "PMC4991899")
		assertUnavailable(t, err)
	})

	t.Run("an unreachable bucket is unavailability", func(t *testing.T) {
		s := europePMCSource{http: refusingClient(), bucketBase: "https://bucket.invalid"}
		_, err := s.pdfURL(context.Background(), "PMC4991899")
		assertUnavailable(t, err)
	})

	t.Run("failing metadata is unavailability", func(t *testing.T) {
		bucket := pmcOABucket{
			listing:    pmcOAFixture(t, "pmcoa_list.xml"),
			metaStatus: http.StatusBadGateway,
		}.serve(t)
		s := europePMCSource{http: bucket.Client(), bucketBase: bucket.URL}
		_, err := s.pdfURL(context.Background(), "PMC4991899")
		assertUnavailable(t, err)
	})

	t.Run("an undecodable listing is neither", func(t *testing.T) {
		bucket := pmcOABucket{listing: []byte("<ListBucketResult><CommonPrefixes>")}.serve(t)
		s := europePMCSource{http: bucket.Client(), bucketBase: bucket.URL}
		_, err := s.pdfURL(context.Background(), "PMC4991899")
		if err == nil {
			t.Fatal("a truncated listing must not resolve")
		}
		if errors.Is(err, ErrNotIndexed) || errors.Is(err, ErrSourceUnavailable) {
			t.Errorf("a truncated listing was classified: %v", err)
		}
	})

	t.Run("an unbuildable bucket root fails cleanly", func(t *testing.T) {
		s := europePMCSource{http: http.DefaultClient, bucketBase: "http://\x7f-invalid"}
		if _, err := s.pdfURL(context.Background(), "PMC4991899"); err == nil {
			t.Fatal("an unbuildable bucket root must fail, not resolve")
		}
	})
}

// TestPMCOAVersionPrefixes pins the listing filter: only "<pmcid>.<n>/" survives,
// ordered by version number descending (numerically, so 10 sorts above 9) and
// capped, because each survivor costs a metadata request and builds a key.
func TestPMCOAVersionPrefixes(t *testing.T) {
	listing := pmcOAListing{}
	for _, p := range []string{
		"PMC1.9/", "PMC1.10/", "PMC1.2/", "PMC1.1/", "PMC1.3/",
		"PMC12.1/", "PMC1.x/", "PMC1.0/", "PMC1.-1/", "PMC1.01/", "PMC1./", "other/",
	} {
		listing.CommonPrefixes = append(listing.CommonPrefixes, struct {
			Prefix string `xml:"Prefix"`
		}{Prefix: p})
	}

	got := pmcOAVersionPrefixes("PMC1", listing)
	want := []string{"PMC1.10", "PMC1.9", "PMC1.3", "PMC1.2"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("pmcOAVersionPrefixes() = %v, want %v", got, want)
	}
}

// TestPMCOAObjectURL pins that the metadata can only point the download at the
// object it describes: this bucket, this version's prefix, a PDF.
func TestPMCOAObjectURL(t *testing.T) {
	const base = "https://bucket.example"
	cases := []struct {
		name   string
		s3URL  string
		want   string
		wantOK bool
	}{
		{"the version's own PDF", "s3://pmc-oa-opendata/PMC1.2/PMC1.2.pdf?md5=ab", base + "/PMC1.2/PMC1.2.pdf", true},
		{"a key needing escaping", "s3://pmc-oa-opendata/PMC1.2/a%20b.PDF", base + "/PMC1.2/a%20b.PDF", true},
		{"no PDF", "", "", false},
		{"another bucket", "s3://elsewhere/PMC1.2/PMC1.2.pdf", "", false},
		{"another scheme", "https://pmc-oa-opendata/PMC1.2/PMC1.2.pdf", "", false},
		{"another version", "s3://pmc-oa-opendata/PMC1.1/PMC1.1.pdf", "", false},
		{"not a PDF", "s3://pmc-oa-opendata/PMC1.2/PMC1.2.xml", "", false},
		{"a traversal", "s3://pmc-oa-opendata/PMC1.2/../PMC9.1/PMC9.1.pdf", "", false},
		{"unparseable", "s3://pmc-oa-opendata/%zz.pdf", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := pmcOAObjectURL(base, tc.s3URL, "PMC1.2")
			if ok != tc.wantOK || got != tc.want {
				t.Errorf("pmcOAObjectURL(%q) = %q, %v, want %q, %v", tc.s3URL, got, ok, tc.want, tc.wantOK)
			}
		})
	}
}

// TestEuropePMCSource_BucketDefaultsToTheDataset covers the default-root branch
// every other test skips by injecting a server.
func TestEuropePMCSource_BucketDefaultsToTheDataset(t *testing.T) {
	if got := (europePMCSource{}).bucket(); got != pmcOABucketBase {
		t.Errorf("bucket() = %q, want %q", got, pmcOABucketBase)
	}
}
