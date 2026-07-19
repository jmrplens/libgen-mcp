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
