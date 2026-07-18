package libgen

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestExtractGetLinkFixture(t *testing.T) {
	body, err := os.ReadFile("testdata/ads.html")
	if err != nil {
		t.Fatal(err)
	}
	link, err := ExtractGetLink(body)
	if err != nil {
		t.Fatalf("ExtractGetLink() error = %v", err)
	}
	if !strings.HasPrefix(link, "get.php?md5=87a4ebdaf21fa6cc70009a3dd63194ee&key=") {
		t.Errorf("link = %q", link)
	}
	if strings.Contains(link, "&amp;") {
		t.Errorf("link sin desescapar: %q", link)
	}
}

func TestExtractGetLinkMissing(t *testing.T) {
	if _, err := ExtractGetLink([]byte("<html>no hay enlace</html>")); err == nil {
		t.Fatal("debería fallar sin enlace get.php")
	}
}

func TestSanitizeFilename(t *testing.T) {
	cases := map[string]string{
		"a/b\\c:d*e?f\"g<h>i|j.pdf": "a_b_c_d_e_f_g_h_i_j.pdf",
		"  normal.epub  ":           "normal.epub",
		"":                          "download",
		"...":                       "download",
	}
	for in, want := range cases {
		if got := sanitizeFilename(in); got != want {
			t.Errorf("sanitizeFilename(%q) = %q, want %q", in, got, want)
		}
	}
}

func downloadTestServer(t *testing.T, payload []byte) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	var srv *httptest.Server
	mux.HandleFunc("/ads.php", func(w http.ResponseWriter, _ *http.Request) {
		// md5 fijo: los tests que usan este servidor descargan siempre el mismo.
		fmt.Fprint(w, `<html><a href="get.php?md5=87a4ebdaf21fa6cc70009a3dd63194ee&key=TESTKEY123">GET</a></html>`)
	})
	mux.HandleFunc("/get.php", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("key") != "TESTKEY123" {
			http.Error(w, "bad key", http.StatusForbidden)
			return
		}
		http.Redirect(w, r, srv.URL+"/cdn/file", http.StatusTemporaryRedirect)
	})
	mux.HandleFunc("/cdn/file", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("Content-Disposition", `attachment; filename="Author - Title (2020).pdf"`)
		w.Write(payload)
	})
	srv = httptest.NewServer(mux)
	return srv
}

func TestDownload(t *testing.T) {
	payload := []byte("%PDF-1.4 fake book content")
	srv := downloadTestServer(t, payload)
	defer srv.Close()
	c := newTestClient(staticMirrors{srv.URL})
	dir := t.TempDir()
	res, err := c.Download(context.Background(), "87a4ebdaf21fa6cc70009a3dd63194ee", dir, "")
	if err != nil {
		t.Fatalf("Download() error = %v", err)
	}
	if res.OriginalFilename != "Author - Title (2020).pdf" {
		t.Errorf("OriginalFilename = %q", res.OriginalFilename)
	}
	if res.Path != filepath.Join(dir, "Author - Title (2020).pdf") {
		t.Errorf("Path = %q", res.Path)
	}
	data, err := os.ReadFile(res.Path)
	if err != nil || string(data) != string(payload) {
		t.Errorf("contenido = %q, err = %v", data, err)
	}
	if res.SizeBytes != int64(len(payload)) {
		t.Errorf("SizeBytes = %d, want %d", res.SizeBytes, len(payload))
	}
	// sin ficheros temporales huérfanos
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 {
		t.Errorf("quedan %d entradas en dir, esperaba 1", len(entries))
	}
}

func TestDownloadCustomFilename(t *testing.T) {
	srv := downloadTestServer(t, []byte("data"))
	defer srv.Close()
	c := newTestClient(staticMirrors{srv.URL})
	dir := t.TempDir()
	res, err := c.Download(context.Background(), "87a4ebdaf21fa6cc70009a3dd63194ee", dir, "mi libro.pdf")
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Base(res.Path) != "mi libro.pdf" {
		t.Errorf("Path = %q", res.Path)
	}
}

func TestDownloadRejectsHTMLViaMagicBytes(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/ads.php", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `<a href="get.php?md5=87a4ebdaf21fa6cc70009a3dd63194ee&key=K1">x</a>`)
	})
	mux.HandleFunc("/get.php", func(w http.ResponseWriter, r *http.Request) {
		// CDN error page sin text/html: se hace pasar por el fichero.
		w.Header().Set("Content-Type", "application/octet-stream")
		fmt.Fprint(w, "<!DOCTYPE html><html><body>blocked</body></html>")
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	c := newTestClient(staticMirrors{srv.URL})
	dir := t.TempDir()
	if _, err := c.Download(context.Background(), "87a4ebdaf21fa6cc70009a3dd63194ee", dir, ""); err == nil {
		t.Fatal("página HTML servida como octet-stream debería fallar")
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 0 {
		t.Errorf("quedan %d entradas en dir, esperaba 0 (ni fichero ni temporal)", len(entries))
	}
}

func TestLooksLikeHTML(t *testing.T) {
	cases := []struct {
		name string
		in   []byte
		want bool
	}{
		{"doctype", []byte("<!DOCTYPE html>"), true},
		{"leading ws html", []byte("  \n<html>"), true},
		{"comment", []byte("<!-- comment"), true},
		{"pdf", []byte("%PDF-1.4"), false},
		{"zip epub", []byte("PK\x03\x04"), false},
		{"binary", []byte{0x00, 0x01, 0x02, 0xff}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := looksLikeHTML(tc.in); got != tc.want {
				t.Errorf("looksLikeHTML(%q) = %v, want %v", tc.in, got, tc.want)
			}
		})
	}
}

func TestDownloadRejectsHTMLResponse(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/ads.php", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `<a href="get.php?md5=87a4ebdaf21fa6cc70009a3dd63194ee&key=K1">x</a>`)
	})
	mux.HandleFunc("/get.php", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprint(w, "<html>error page</html>")
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	c := newTestClient(staticMirrors{srv.URL})
	if _, err := c.Download(context.Background(), "87a4ebdaf21fa6cc70009a3dd63194ee", t.TempDir(), ""); err == nil {
		t.Fatal("respuesta HTML debería fallar")
	}
}
