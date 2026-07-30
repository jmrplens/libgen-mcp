package libgen

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// zenodoStub is a Zenodo stand-in covering the two endpoints the source uses: the
// per-record file listing under /api/records/<id>/files, and the human-facing
// record page under /records/<id> that the concept-DOI hop redirects through.
type zenodoStub struct {
	srv *httptest.Server
	// listings maps a record id to the body its file listing returns; an id absent
	// from the map answers with listingStatus.
	listings map[string]string
	// listingStatus is the status served for a record id with no listing, which is
	// how a concept id, a restricted record and an unknown record are simulated.
	listingStatus int
	// versions maps a concept record id to the version id its page redirects to; an
	// id absent from the map answers 200 (already a version) unless it is in
	// missingRecords.
	versions map[string]string
	// missingRecords are the ids whose record page answers 404.
	missingRecords map[string]bool
	// listingCalls counts file-listing requests, so the hop can be shown to happen
	// only when it is needed.
	listingCalls int
}

// startZenodoStub starts a Zenodo stand-in, registering its shutdown with the test.
func startZenodoStub(t *testing.T) *zenodoStub {
	t.Helper()
	stub := &zenodoStub{
		listings:       map[string]string{},
		listingStatus:  http.StatusNotFound,
		versions:       map[string]string{},
		missingRecords: map[string]bool{},
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/api/records/", stub.serveListing)
	mux.HandleFunc("/records/", stub.serveRecordPage)
	stub.srv = httptest.NewServer(mux)
	t.Cleanup(stub.srv.Close)
	return stub
}

// serveListing answers the file-listing endpoint for a record id.
func (s *zenodoStub) serveListing(w http.ResponseWriter, r *http.Request) {
	s.listingCalls++
	id := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/api/records/"), "/files")
	body, ok := s.listings[id]
	if !ok {
		w.WriteHeader(s.listingStatus)
		return
	}
	_, _ = w.Write([]byte(body))
}

// serveRecordPage answers the record page, redirecting a concept id to its version.
func (s *zenodoStub) serveRecordPage(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimPrefix(r.URL.Path, "/records/")
	if s.missingRecords[id] {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	if version, ok := s.versions[id]; ok {
		http.Redirect(w, r, "/records/"+version, http.StatusFound)
		return
	}
	w.WriteHeader(http.StatusOK)
}

// source builds a zenodoSource pointed at the stub.
func (s *zenodoStub) source() zenodoSource {
	return zenodoSource{http: s.srv.Client(), baseURL: s.srv.URL}
}

// zenodoListing renders a file listing from key/content pairs, which is the subset
// of the real response the source reads.
func zenodoListing(entries ...string) string {
	return `{"entries":[` + strings.Join(entries, ",") + `]}`
}

// zenodoFile renders one listing entry. The mimetype is deliberately always
// application/octet-stream, because that is what Zenodo reports for every format it
// does not recognize and what its file endpoint serves for everything.
func zenodoFile(key string, size int64, content string) string {
	return `{"key":"` + key + `","size":` + itoa(size) +
		`,"mimetype":"application/octet-stream","links":{"content":"` + content + `"}}`
}

// itoa renders an int64 without pulling strconv into the test's imports for one use.
func itoa(n int64) string {
	if n == 0 {
		return "0"
	}
	var digits []byte
	for n > 0 {
		digits = append([]byte{byte('0' + n%10)}, digits...)
		n /= 10
	}
	return string(digits)
}

// TestZenodoSupports verifies the source claims a well-formed Zenodo record DOI in
// either case, refuses a 10.5281 DOI that is not one, and names itself "zenodo".
func TestZenodoSupports(t *testing.T) {
	s := zenodoSource{}
	cases := []struct {
		name string
		item Item
		want bool
	}{
		{name: "record doi", item: Item{DOI: "10.5281/zenodo.3233986"}, want: true},
		{name: "uppercase token", item: Item{DOI: "10.5281/ZENODO.3233986"}, want: true},
		{name: "registrant without the zenodo token", item: Item{DOI: "10.5281/other.123"}, want: false},
		{name: "non-numeric record", item: Item{DOI: "10.5281/zenodo.abc"}, want: false},
		{name: "record with trailing junk", item: Item{DOI: "10.5281/zenodo.123v2"}, want: false},
		{name: "token but no record", item: Item{DOI: "10.5281/zenodo."}, want: false},
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
	if s.Name() != "zenodo" {
		t.Errorf("Name() = %q, want %q", s.Name(), "zenodo")
	}
}

// TestZenodoResolveVersionRecord verifies a DOI naming a version record is served
// straight from its file listing, with no version hop.
func TestZenodoResolveVersionRecord(t *testing.T) {
	stub := startZenodoStub(t)
	stub.listings["21698070"] = zenodoListing(
		zenodoFile("TRMSRV3I3I2025207.pdf", 271574, "https://example.invalid/a.pdf"))

	got, err := stub.source().Resolve(context.Background(), Item{DOI: "10.5281/zenodo.21698070"})
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	if want := "https://example.invalid/a.pdf"; got.FileURL != want {
		t.Errorf("FileURL = %q, want %q", got.FileURL, want)
	}
	if got.Ext != "pdf" {
		t.Errorf("Ext = %q, want %q", got.Ext, "pdf")
	}
	if got.VerifyMD5 {
		t.Error("VerifyMD5 = true, want false for a DOI-keyed item")
	}
	if stub.listingCalls != 1 {
		t.Errorf("listing requests = %d, want 1 (a version record needs no hop)", stub.listingCalls)
	}
}

// TestZenodoResolveConceptDOI verifies the hop that makes concept DOIs work: a
// record id whose listing 404s is re-asked through the record page, and the version
// it redirects to is listed instead. Roughly half of all Zenodo DOIs are concept
// DOIs, so without this they would all resolve to nothing.
func TestZenodoResolveConceptDOI(t *testing.T) {
	stub := startZenodoStub(t)
	stub.versions["19978417"] = "21676215"
	stub.listings["21676215"] = zenodoListing(
		zenodoFile("paper.pdf", 100, "https://example.invalid/v.pdf"))

	got, err := stub.source().Resolve(context.Background(), Item{DOI: "10.5281/zenodo.19978417"})
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	if want := "https://example.invalid/v.pdf"; got.FileURL != want {
		t.Errorf("FileURL = %q, want %q", got.FileURL, want)
	}
	if stub.listingCalls != 2 {
		t.Errorf("listing requests = %d, want 2 (concept, then version)", stub.listingCalls)
	}
}

// TestZenodoPickFileFormat pins which file of a multi-file record is served: a
// format the read tool can extract wins over one it cannot, in the order pdf, epub,
// txt; failing that the largest file wins, on the reasoning that a deposit's
// payload is its biggest file and the rest are READMEs and manifests.
func TestZenodoPickFileFormat(t *testing.T) {
	cases := []struct {
		name    string
		listing string
		want    string
		wantExt string
	}{
		{
			name: "pdf beats an earlier zip",
			listing: zenodoListing(
				zenodoFile("data.zip", 999999, "https://example.invalid/data.zip"),
				zenodoFile("paper.pdf", 10, "https://example.invalid/paper.pdf")),
			want:    "https://example.invalid/paper.pdf",
			wantExt: "pdf",
		},
		{
			name: "pdf beats epub and txt",
			listing: zenodoListing(
				zenodoFile("b.txt", 5, "https://example.invalid/b.txt"),
				zenodoFile("b.epub", 5, "https://example.invalid/b.epub"),
				zenodoFile("b.pdf", 5, "https://example.invalid/b.pdf")),
			want:    "https://example.invalid/b.pdf",
			wantExt: "pdf",
		},
		{
			name: "epub beats txt",
			listing: zenodoListing(
				zenodoFile("b.txt", 5, "https://example.invalid/b.txt"),
				zenodoFile("b.epub", 5, "https://example.invalid/b.epub")),
			want:    "https://example.invalid/b.epub",
			wantExt: "epub",
		},
		{
			name: "case in the name does not hide a readable format",
			listing: zenodoListing(
				zenodoFile("Report.PDF", 5, "https://example.invalid/Report.PDF")),
			want:    "https://example.invalid/Report.PDF",
			wantExt: "pdf",
		},
		{
			name: "with nothing readable the largest file wins",
			listing: zenodoListing(
				zenodoFile("README.md", 5928, "https://example.invalid/README.md"),
				zenodoFile("dataset.zip", 270505103, "https://example.invalid/dataset.zip"),
				zenodoFile("manifest.csv", 240, "https://example.invalid/manifest.csv")),
			want:    "https://example.invalid/dataset.zip",
			wantExt: "zip",
		},
		{
			name: "a single extensionless file is still served",
			listing: zenodoListing(
				zenodoFile("LICENSE", 1024, "https://example.invalid/LICENSE")),
			want:    "https://example.invalid/LICENSE",
			wantExt: "",
		},
		{
			name: "entries without a usable content url are skipped",
			listing: zenodoListing(
				zenodoFile("broken.pdf", 900, "/relative/broken.pdf"),
				zenodoFile("ok.zip", 5, "https://example.invalid/ok.zip")),
			want:    "https://example.invalid/ok.zip",
			wantExt: "zip",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			stub := startZenodoStub(t)
			stub.listings["1"] = tc.listing

			got, err := stub.source().Resolve(context.Background(), Item{DOI: "10.5281/zenodo.1"})
			if err != nil {
				t.Fatalf("Resolve() error = %v", err)
			}
			if got.FileURL != tc.want {
				t.Errorf("FileURL = %q, want %q", got.FileURL, tc.want)
			}
			if got.Ext != tc.wantExt {
				t.Errorf("Ext = %q, want %q", got.Ext, tc.wantExt)
			}
		})
	}
}

// TestZenodoResolveMisses verifies every outcome that means "Zenodo will not give
// us this record" is reported as a clean miss, so the chain advances without
// setting the source aside.
func TestZenodoResolveMisses(t *testing.T) {
	t.Run("restricted record", func(t *testing.T) {
		stub := startZenodoStub(t)
		stub.listingStatus = http.StatusForbidden

		_, err := stub.source().Resolve(context.Background(), Item{DOI: "10.5281/zenodo.3601385"})
		assertCleanMiss(t, err)
		if stub.listingCalls != 1 {
			t.Errorf("listing requests = %d, want 1 (a 403 is final, not a concept id)", stub.listingCalls)
		}
	})

	t.Run("record does not exist", func(t *testing.T) {
		stub := startZenodoStub(t)
		stub.missingRecords["999999999"] = true

		_, err := stub.source().Resolve(context.Background(), Item{DOI: "10.5281/zenodo.999999999"})
		assertCleanMiss(t, err)
	})

	t.Run("record page leads nowhere new", func(t *testing.T) {
		// The listing 404s and the record page answers 200 for the same id: the
		// record is a version that simply has no listing, so there is no hop to make.
		stub := startZenodoStub(t)

		_, err := stub.source().Resolve(context.Background(), Item{DOI: "10.5281/zenodo.123"})
		assertCleanMiss(t, err)
	})

	t.Run("record page redirects somewhere that is not a record", func(t *testing.T) {
		stub := startZenodoStub(t)
		stub.versions["123"] = "not-a-number"

		_, err := stub.source().Resolve(context.Background(), Item{DOI: "10.5281/zenodo.123"})
		assertCleanMiss(t, err)
	})

	t.Run("metadata-only record with no files", func(t *testing.T) {
		stub := startZenodoStub(t)
		stub.listings["123"] = `{"entries":[]}`

		_, err := stub.source().Resolve(context.Background(), Item{DOI: "10.5281/zenodo.123"})
		assertCleanMiss(t, err)
	})

	t.Run("every file lacks a usable content url", func(t *testing.T) {
		stub := startZenodoStub(t)
		stub.listings["123"] = zenodoListing(zenodoFile("a.pdf", 5, ""))

		_, err := stub.source().Resolve(context.Background(), Item{DOI: "10.5281/zenodo.123"})
		assertCleanMiss(t, err)
	})

	t.Run("wrong registrant reaches Resolve directly", func(t *testing.T) {
		stub := startZenodoStub(t)

		_, err := stub.source().Resolve(context.Background(), Item{DOI: "10.17487/RFC9110"})
		assertCleanMiss(t, err)
	})
}

// TestZenodoResolveUnavailable verifies the outcomes that say nothing about the
// item are reported as the source being unavailable, on both endpoints.
func TestZenodoResolveUnavailable(t *testing.T) {
	t.Run("listing returns a transient status", func(t *testing.T) {
		stub := startZenodoStub(t)
		stub.listingStatus = http.StatusServiceUnavailable

		_, err := stub.source().Resolve(context.Background(), Item{DOI: "10.5281/zenodo.123"})
		if !errors.Is(err, ErrSourceUnavailable) {
			t.Fatalf("Resolve() error = %v, want ErrSourceUnavailable", err)
		}
	})

	t.Run("record page returns a transient status", func(t *testing.T) {
		mux := http.NewServeMux()
		mux.HandleFunc("/api/records/", func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusNotFound)
		})
		mux.HandleFunc("/records/", func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusBadGateway)
		})
		srv := httptest.NewServer(mux)
		defer srv.Close()

		s := zenodoSource{http: srv.Client(), baseURL: srv.URL}
		_, err := s.Resolve(context.Background(), Item{DOI: "10.5281/zenodo.123"})
		if !errors.Is(err, ErrSourceUnavailable) {
			t.Fatalf("Resolve() error = %v, want ErrSourceUnavailable", err)
		}
	})

	t.Run("listing transport failure", func(t *testing.T) {
		stub := startZenodoStub(t)
		s := stub.source()
		stub.srv.Close()

		_, err := s.Resolve(context.Background(), Item{DOI: "10.5281/zenodo.123"})
		if !errors.Is(err, ErrSourceUnavailable) {
			t.Fatalf("Resolve() error = %v, want ErrSourceUnavailable", err)
		}
	})

	t.Run("record page transport failure", func(t *testing.T) {
		// The listing endpoint answers (with the 404 that triggers the hop) while the
		// record page refuses to connect, so only the hop's transport fails.
		stub := startZenodoStub(t)
		s := stub.source()
		s.http = &http.Client{Transport: hopFailingTransport{}}

		_, err := s.Resolve(context.Background(), Item{DOI: "10.5281/zenodo.123"})
		if !errors.Is(err, ErrSourceUnavailable) {
			t.Fatalf("Resolve() error = %v, want ErrSourceUnavailable", err)
		}
	})
}

// hopFailingTransport lets the file-listing request through to the real transport
// and refuses the record-page request, so the version hop's transport failure can
// be exercised on its own.
type hopFailingTransport struct{}

// RoundTrip serves an /api/ request normally and refuses everything else.
func (t hopFailingTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	if strings.HasPrefix(r.URL.Path, "/api/") {
		return http.DefaultTransport.RoundTrip(r)
	}
	return refusingTransport{}.RoundTrip(r)
}

// TestZenodoResolveDecodeFailure verifies a listing body that is not the expected
// JSON fails without being reported as either a clean miss or unavailability: a
// response we could not read is no verdict on the item, and no verdict on the
// source either.
func TestZenodoResolveDecodeFailure(t *testing.T) {
	stub := startZenodoStub(t)
	stub.listings["123"] = "<html>not json</html>"

	_, err := stub.source().Resolve(context.Background(), Item{DOI: "10.5281/zenodo.123"})
	if err == nil {
		t.Fatal("Resolve() should fail on an undecodable listing")
	}
	if errors.Is(err, ErrNotIndexed) {
		t.Error("an undecodable body must not read as the record being unheld")
	}
	if !strings.Contains(err.Error(), "decoding") {
		t.Errorf("error = %v, want a decode failure", err)
	}
}

// TestZenodoResolveRejectsUnbuildableRequests verifies a base that cannot go into a
// URL fails before any request is attempted, on both endpoints.
func TestZenodoResolveRejectsUnbuildableRequests(t *testing.T) {
	t.Run("listing", func(t *testing.T) {
		s := zenodoSource{baseURL: "http://\x7f.invalid"}
		_, err := s.Resolve(context.Background(), Item{DOI: "10.5281/zenodo.123"})
		if err == nil || !strings.Contains(err.Error(), "building request") {
			t.Fatalf("error = %v, want a request-building failure", err)
		}
	})

	t.Run("version hop", func(t *testing.T) {
		// Only the hop can be reached with an unbuildable id, since Resolve rejects a
		// non-numeric record before either request, so it is called directly.
		s := startZenodoStub(t).source()
		_, err := s.latestVersion(context.Background(), "\x7f")
		if err == nil || !strings.Contains(err.Error(), "building version request") {
			t.Fatalf("error = %v, want a request-building failure", err)
		}
	})
}

// TestZenodoUsesProductionBase verifies a source with no base override targets the
// real Zenodo root and the robots-permitted file-listing path, so the default
// configuration reaches Zenodo rather than nothing and stays inside what
// zenodo.org/robots.txt allows. The URL is observed through a transport that
// refuses every request, so nothing leaves the machine.
func TestZenodoUsesProductionBase(t *testing.T) {
	s := zenodoSource{http: refusingClient()}
	_, err := s.Resolve(context.Background(), Item{DOI: "10.5281/zenodo.3233986"})
	if err == nil {
		t.Fatal("Resolve() should fail when every request is refused")
	}
	if want := "https://zenodo.org/api/records/3233986/files"; !strings.Contains(err.Error(), want) {
		t.Errorf("error = %v, want it to name %q", err, want)
	}
}

// TestZenodoIsDigits covers the record-id shape check directly, including the empty
// string. Resolve cannot reach that case — it rejects a DOI with no suffix before
// asking — but an empty run of digits must not read as a valid record id, and the
// guard that makes it so is otherwise untested.
func TestZenodoIsDigits(t *testing.T) {
	cases := map[string]bool{
		"":      false,
		"0":     true,
		"12345": true,
		"12a":   false,
		"-1":    false,
		" 1":    false,
	}
	for in, want := range cases {
		if got := isDigits(in); got != want {
			t.Errorf("isDigits(%q) = %v, want %v", in, got, want)
		}
	}
}
