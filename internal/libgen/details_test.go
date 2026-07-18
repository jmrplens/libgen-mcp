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
		t.Fatal("edition = nil, esperaba la edición relacionada")
	}
	if edition["title"] == "" || edition["title"] == nil {
		t.Errorf("edition.title vacío: %v", edition["title"])
	}
}

func TestDetailsByID(t *testing.T) {
	srv := jsonFixtureServer(t)
	defer srv.Close()
	c := newTestClient(staticMirrors{srv.URL})
	ed, err := c.DetailsByID(context.Background(), "e", "138281637")
	if err != nil {
		t.Fatalf("DetailsByID() error = %v", err)
	}
	if ed["title"] == nil {
		t.Error("edition sin title")
	}
	if _, derr := c.DetailsByID(context.Background(), "x", "1"); derr == nil {
		t.Error("object inválido debería fallar")
	}
}

func TestDetailsNotFound(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`[]`))
	}))
	defer srv.Close()
	c := newTestClient(staticMirrors{srv.URL})
	if _, _, err := c.DetailsByMD5(context.Background(), "00000000000000000000000000000000"); err == nil {
		t.Error("md5 inexistente debería fallar")
	}
}
