package discovery

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

// mustReadFixture reads a testdata file or fails the test.
func mustReadFixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// TestParseAnnasRecordZlib verifies the fields extracted from a real Z-Library
// record page: the ones every page carries (title, author, language, year,
// content type, collection, size, path) plus the ones only some do (ISBNs, an
// IPFS CID).
func TestParseAnnasRecordZlib(t *testing.T) {
	const md5 = "00dd2b0b58e81e3c6e7cb9e7b72dee23"
	rec := parseAnnasRecord(mustReadFixture(t, "annas_md5_zlib.html"), md5)
	if rec == nil {
		t.Fatal("parseAnnasRecord() = nil, want a record")
	}
	for _, tc := range []struct{ field, got, want string }{
		{"MD5", rec.MD5, md5},
		{"Title", rec.Title, "Sejarah Indonesia Masa Persebaran Islam sampai Zaman VOC"},
		{"Author", rec.Author, "Tim Penyusun"},
		{"Language", rec.Language, "id"},
		{"Year", rec.Year, "2022"},
		{"ContentType", rec.ContentType, "book_unknown"},
		{"Collection", rec.Collection, "zlib"},
		{"Filesize", rec.Filesize, "1293251"},
		{"Extension", rec.Extension, "pdf"},
		{"ISBN13", rec.ISBN13, "978-602-282-497-8"},
		{"IPFSCID", rec.IPFSCID, "QmQg2L4vJwKFPiaDpdSY5d42EZ4XFkg8Dzpcuwxk23noyF"},
	} {
		if tc.got != tc.want {
			t.Errorf("%s = %q, want %q", tc.field, tc.got, tc.want)
		}
	}
}

// TestParseAnnasRecordMagzdb verifies a record from a different collection, which
// publishes neither an ISBN nor an IPFS CID: the always-present fields must still
// come through, and the absent ones must stay empty rather than pick up a
// neighboring field's value.
func TestParseAnnasRecordMagzdb(t *testing.T) {
	const md5 = "0cbd8f4647bc1964b7740912374042e2"
	rec := parseAnnasRecord(mustReadFixture(t, "annas_md5_magzdb.html"), md5)
	if rec == nil {
		t.Fatal("parseAnnasRecord() = nil, want a record")
	}
	if rec.Title != "CQ Amateur Radio 1987 No 11" {
		t.Errorf("Title = %q", rec.Title)
	}
	if rec.Language != "en" || rec.Year != "1987" {
		t.Errorf("Language/Year = %q/%q, want en/1987", rec.Language, rec.Year)
	}
	if rec.Filesize != "41740323" {
		t.Errorf("Filesize = %q, want 41740323", rec.Filesize)
	}
	if rec.ISBN13 != "" || rec.ISBN10 != "" || rec.IPFSCID != "" {
		t.Errorf("absent fields should stay empty, got isbn13=%q isbn10=%q cid=%q",
			rec.ISBN13, rec.ISBN10, rec.IPFSCID)
	}
}

// TestParseAnnasRecordNotAPage verifies a body that is not a record page yields no
// record, so a mirror serving an error or a block page is not mistaken for data.
func TestParseAnnasRecordNotAPage(t *testing.T) {
	if rec := parseAnnasRecord([]byte(`<html><body>nope</body></html>`), "deadbeef"); rec != nil {
		t.Errorf("parseAnnasRecord() on a non-record page = %+v, want nil", rec)
	}
}

// TestAnnasDetailsTriesEveryMirror verifies Details walks the mirror list past a
// failing mirror, so one unreachable host does not deny the record.
func TestAnnasDetailsTriesEveryMirror(t *testing.T) {
	dead := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer dead.Close()
	page := mustReadFixture(t, "annas_md5_zlib.html")
	live := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/md5/") {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_, _ = w.Write(page)
	}))
	defer live.Close()

	p := NewAnnas(staticMirrors{dead.URL, live.URL})
	rec, err := p.Details(context.Background(), "00dd2b0b58e81e3c6e7cb9e7b72dee23")
	if err != nil {
		t.Fatalf("Details() error = %v", err)
	}
	if rec.Title == "" {
		t.Error("Details() returned a record with no title")
	}
}

// TestAnnasDetailsNoMirrorAnswers verifies an error is returned — rather than an
// empty record — when no mirror serves the page, so the caller can distinguish
// "Anna's does not have it" from "Anna's could not be reached".
func TestAnnasDetailsNoMirrorAnswers(t *testing.T) {
	dead := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer dead.Close()
	p := NewAnnas(staticMirrors{dead.URL})
	if _, err := p.Details(context.Background(), "00dd2b0b58e81e3c6e7cb9e7b72dee23"); err == nil {
		t.Error("Details() with no reachable mirror should fail")
	}
}

// TestAnnasDetails_NilClientUsesTheDefault covers the nil-client guard. A
// provider built as a struct literal — which the tools layer and these tests both
// do — has no http.Client, and dereferencing that instead of falling back would
// panic on the first lookup. The context is canceled so the fallback is
// selected and the request then fails before any dial.
func TestAnnasDetails_NilClientUsesTheDefault(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	p := &AnnasProvider{mirrors: staticMirrors{"http://127.0.0.1:0"}} // http is nil
	if _, err := p.Details(ctx, "d41d8cd98f00b204e9800998ecf8427e"); err == nil {
		t.Fatal("a canceled context must fail the lookup")
	}
}

// TestAnnasDetails_NoMirrorsIsAnError covers the branch where the loop never
// runs, so no per-mirror error was recorded. Returning nil there would tell the
// caller a record was found.
func TestAnnasDetails_NoMirrorsIsAnError(t *testing.T) {
	p := &AnnasProvider{mirrors: staticMirrors{}, http: http.DefaultClient}
	rec, err := p.Details(context.Background(), "d41d8cd98f00b204e9800998ecf8427e")
	if err == nil {
		t.Fatal("no mirrors must be an error, not a silent miss")
	}
	if rec != nil {
		t.Fatalf("no record can be returned alongside an error, got %+v", rec)
	}
	if !strings.Contains(err.Error(), "no mirror available") {
		t.Fatalf("error should say no mirror was available, got %v", err)
	}
}

// TestAnnasDetails_MirrorServingNoRecordIsReported covers the "served a page but
// it parsed to nothing" branch, which is distinct from an unreachable mirror: the
// caller has to be able to tell "Anna's has no such record" from "Anna's could
// not be reached".
func TestAnnasDetails_MirrorServingNoRecordIsReported(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("<html><body>nothing resembling a record</body></html>"))
	}))
	defer srv.Close()

	p := &AnnasProvider{mirrors: staticMirrors{srv.URL}, http: srv.Client()}
	_, err := p.Details(context.Background(), "d41d8cd98f00b204e9800998ecf8427e")
	if err == nil {
		t.Fatal("a mirror that serves no record must be an error")
	}
	if !strings.Contains(err.Error(), "served no record") {
		t.Fatalf("error should distinguish an empty record from a dead mirror, got %v", err)
	}
}

// TestAnnasDetails_CancellationDuringFetchStopsImmediately covers the branch that
// aborts the mirror loop on a canceled context instead of trying every remaining
// mirror with a context that can no longer succeed.
func TestAnnasDetails_CancellationDuringFetchStopsImmediately(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	var hits int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits++
		cancel() // cancel while the first mirror is being read
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	p := &AnnasProvider{mirrors: staticMirrors{srv.URL, srv.URL, srv.URL}, http: srv.Client()}
	if _, err := p.Details(ctx, "d41d8cd98f00b204e9800998ecf8427e"); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if hits != 1 {
		t.Fatalf("the loop kept going after cancellation: %d mirrors tried, want 1", hits)
	}
}
