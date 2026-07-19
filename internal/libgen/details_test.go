package libgen

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
)

func jsonFixtureServer(t *testing.T) *httptest.Server {
	t.Helper()
	fileJSON, err := os.ReadFile("testdata/file_by_md5.json")
	if err != nil {
		t.Fatal(err)
	}
	editionJSON, err := os.ReadFile("testdata/edition.json")
	if err != nil {
		t.Fatal(err)
	}
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/json.php" {
			http.NotFound(w, r)
			return
		}
		switch r.URL.Query().Get("object") {
		case "f":
			w.Write(fileJSON)
		case "e":
			w.Write(editionJSON)
		default:
			http.Error(w, "bad object", http.StatusBadRequest)
		}
	}))
}

// TestDetailsByMD5 verifies DetailsByMD5.
func TestDetailsByMD5(t *testing.T) {
	srv := jsonFixtureServer(t)
	defer srv.Close()
	c := newTestClient(staticMirrors{srv.URL})
	file, edition, err := c.DetailsByMD5(context.Background(), "87a4ebdaf21fa6cc70009a3dd63194ee")
	if err != nil {
		t.Fatalf("DetailsByMD5() error = %v", err)
	}
	if file["md5"] != "87a4ebdaf21fa6cc70009a3dd63194ee" {
		t.Errorf("file.md5 = %v", file["md5"])
	}
	if edition == nil {
		t.Fatal("edition = nil, want the related edition")
	}
	if edition["title"] == "" || edition["title"] == nil {
		t.Errorf("edition.title is empty: %v", edition["title"])
	}
}

// TestDetailsByID verifies DetailsByID.
func TestDetailsByID(t *testing.T) {
	srv := jsonFixtureServer(t)
	defer srv.Close()
	c := newTestClient(staticMirrors{srv.URL})
	ed, err := c.DetailsByID(context.Background(), "e", "138281637")
	if err != nil {
		t.Fatalf("DetailsByID() error = %v", err)
	}
	if ed["title"] == nil {
		t.Error("edition without title")
	}
	if _, derr := c.DetailsByID(context.Background(), "x", "1"); derr == nil {
		t.Error("invalid object should fail")
	}
}

// TestDetailsByIDNotFound verifies that a by-id lookup whose json.php response is
// an empty array (`[]`, no record) returns an error rather than a nil record.
func TestDetailsByIDNotFound(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte(`[]`))
	}))
	defer srv.Close()
	c := newTestClient(staticMirrors{srv.URL})
	if _, err := c.DetailsByID(context.Background(), "e", "999999"); err == nil {
		t.Error("DetailsByID() for a missing id should fail")
	}
}

// TestDecodeObjectsGarbage verifies that a json.php body that is neither an
// object map nor an empty array is surfaced as an "unexpected response" error.
func TestDecodeObjectsGarbage(t *testing.T) {
	if _, err := decodeObjects([]byte(`not json at all`)); err == nil {
		t.Error("decodeObjects() on a non-JSON body should fail")
	}
	// A non-empty array is also unexpected (the API returns an object map or []).
	if _, err := decodeObjects([]byte(`[1,2,3]`)); err == nil {
		t.Error("decodeObjects() on a non-empty array should fail")
	}
}

// TestDetailsNotFound verifies DetailsNotFound.
func TestDetailsNotFound(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`[]`))
	}))
	defer srv.Close()
	c := newTestClient(staticMirrors{srv.URL})
	if _, _, err := c.DetailsByMD5(context.Background(), "00000000000000000000000000000000"); err == nil {
		t.Error("nonexistent md5 should fail")
	}
}

// TestDetailsByMD5GetError covers the transport-error branch: when the json.php
// request cannot be made, DetailsByMD5 surfaces the error.
func TestDetailsByMD5GetError(t *testing.T) {
	c := newTestClient(staticMirrors{"http://127.0.0.1:0"})
	if _, _, err := c.DetailsByMD5(context.Background(), "87a4ebdaf21fa6cc70009a3dd63194ee"); err == nil {
		t.Error("DetailsByMD5 should fail when the request cannot be made")
	}
}

// TestDetailsByMD5DecodeError covers the decode-error branch: a json.php body that
// is neither an object map nor an empty array surfaces as an error.
func TestDetailsByMD5DecodeError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("not json at all"))
	}))
	defer srv.Close()
	c := newTestClient(staticMirrors{srv.URL})
	if _, _, err := c.DetailsByMD5(context.Background(), "87a4ebdaf21fa6cc70009a3dd63194ee"); err == nil {
		t.Error("DetailsByMD5 should fail on a malformed json.php body")
	}
}

// TestDetailsByMD5NonMapEdition covers the editions loop's skip branch: an entry
// in the editions map that is not an object is ignored, leaving no related edition.
func TestDetailsByMD5NonMapEdition(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"file123":{"md5":"abc","editions":{"0":"not-an-object"}}}`))
	}))
	defer srv.Close()
	c := newTestClient(staticMirrors{srv.URL})
	file, edition, err := c.DetailsByMD5(context.Background(), "abc")
	if err != nil {
		t.Fatalf("DetailsByMD5() error = %v", err)
	}
	if edition != nil {
		t.Errorf("edition = %v, want nil (non-object edition skipped)", edition)
	}
	if file["md5"] != "abc" {
		t.Errorf("file.md5 = %v, want abc", file["md5"])
	}
}

// TestDetailsByIDFileObject covers the object == "f" branch, which sets addkeys on
// the query, using the file fixture.
func TestDetailsByIDFileObject(t *testing.T) {
	srv := jsonFixtureServer(t)
	defer srv.Close()
	c := newTestClient(staticMirrors{srv.URL})
	rec, err := c.DetailsByID(context.Background(), "f", "138281637")
	if err != nil {
		t.Fatalf("DetailsByID(f) error = %v", err)
	}
	if len(rec) == 0 {
		t.Error("DetailsByID(f) returned an empty record")
	}
}

// TestDetailsByIDGetError covers the transport-error branch of DetailsByID.
func TestDetailsByIDGetError(t *testing.T) {
	c := newTestClient(staticMirrors{"http://127.0.0.1:0"})
	if _, err := c.DetailsByID(context.Background(), "e", "1"); err == nil {
		t.Error("DetailsByID should fail when the request cannot be made")
	}
}

// TestDetailsByIDDecodeError covers the decode-error branch of DetailsByID.
func TestDetailsByIDDecodeError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("garbage"))
	}))
	defer srv.Close()
	c := newTestClient(staticMirrors{srv.URL})
	if _, err := c.DetailsByID(context.Background(), "e", "1"); err == nil {
		t.Error("DetailsByID should fail on a malformed json.php body")
	}
}
