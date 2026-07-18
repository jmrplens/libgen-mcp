package libgen

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	xhtml "golang.org/x/net/html"
)

var getLinkRe = regexp.MustCompile(`get\.php\?md5=[0-9a-fA-F]{32}&(?:amp;)?key=[A-Za-z0-9]+`)

// ExtractGetLink localiza el enlace get.php?md5=…&key=… dentro de la página ads.php.
func ExtractGetLink(body []byte) (string, error) {
	m := getLinkRe.Find(body)
	if m == nil {
		return "", fmt.Errorf("%w: no get.php key link in ads page", ErrLayoutChanged)
	}
	return xhtml.UnescapeString(string(m)), nil
}

// ResolveGetURL obtiene la URL directa de descarga (con key fresca) para un md5.
func (c *Client) ResolveGetURL(ctx context.Context, md5 string) (string, string, error) {
	body, base, err := c.get(ctx, "/ads.php", url.Values{"md5": {md5}})
	if err != nil {
		return "", "", err
	}
	link, err := ExtractGetLink(body)
	if err != nil {
		return "", "", err
	}
	return base + "/" + link, base, nil
}

type DownloadResult struct {
	Path             string `json:"path"`
	SizeBytes        int64  `json:"size_bytes"`
	OriginalFilename string `json:"original_filename,omitempty"`
	Mirror           string `json:"mirror"`
}

// Download descarga el fichero md5 a dir. Si filename está vacío usa el nombre
// que anuncia el CDN (content-disposition), saneado.
func (c *Client) Download(ctx context.Context, md5, dir, filename string) (*DownloadResult, error) {
	fileURL, base, err := c.ResolveGetURL(ctx, md5)
	if err != nil {
		return nil, err
	}
	if err := c.limiter.Wait(ctx); err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, fileURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", userAgent)
	resp, err := c.dl.Do(req) // c.dl sin timeout global: descargas largas, gobierna ctx
	if err != nil {
		return nil, fmt.Errorf("downloading file: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("download failed: status %d from %s", resp.StatusCode, base)
	}
	if strings.HasPrefix(resp.Header.Get("Content-Type"), "text/html") {
		return nil, errors.New("mirror returned an HTML page instead of the file (key expired or download blocked)")
	}
	// Algunos CDN sirven páginas de error como application/octet-stream (o sin
	// Content-Type). Olfateamos los primeros bytes sin consumirlos: Peek deja
	// los bytes en el bufio.Reader para que io.Copy los vuelva a leer.
	body := bufio.NewReader(resp.Body)
	head, err := body.Peek(512)
	if err != nil && err != io.EOF {
		return nil, fmt.Errorf("reading file header: %w", err)
	}
	if looksLikeHTML(head) {
		return nil, errors.New("mirror returned what looks like an HTML page instead of the file (key expired or download blocked)")
	}
	original := filenameFromDisposition(resp.Header.Get("Content-Disposition"))
	name := filename
	if name == "" {
		name = original
	}
	if name == "" {
		name = md5
	}
	name = sanitizeFilename(name)

	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("creating download dir: %w", err)
	}
	tmp, err := os.CreateTemp(dir, ".libgen-mcp-*")
	if err != nil {
		return nil, err
	}
	n, copyErr := io.Copy(tmp, body)
	closeErr := tmp.Close()
	if copyErr != nil || closeErr != nil {
		os.Remove(tmp.Name())
		return nil, fmt.Errorf("writing file: %w", errors.Join(copyErr, closeErr))
	}
	if resp.ContentLength > 0 && n != resp.ContentLength {
		os.Remove(tmp.Name())
		return nil, fmt.Errorf("truncated download: got %d of %d bytes", n, resp.ContentLength)
	}
	dest := filepath.Join(dir, name)
	if err := os.Rename(tmp.Name(), dest); err != nil {
		os.Remove(tmp.Name())
		return nil, err
	}
	return &DownloadResult{Path: dest, SizeBytes: n, OriginalFilename: original, Mirror: base}, nil
}

// looksLikeHTML detecta si b (cabecera olfateada del cuerpo) empieza, tras
// recortar espacio ASCII inicial, por un marcador de documento HTML.
func looksLikeHTML(b []byte) bool {
	trimmed := bytes.TrimLeft(b, " \t\r\n\f\v")
	lower := bytes.ToLower(trimmed)
	return bytes.HasPrefix(lower, []byte("<!doctype html")) ||
		bytes.HasPrefix(lower, []byte("<html")) ||
		bytes.HasPrefix(lower, []byte("<!--"))
}

func filenameFromDisposition(header string) string {
	if header == "" {
		return ""
	}
	_, params, err := mime.ParseMediaType(header)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(params["filename"])
}

func sanitizeFilename(s string) string {
	s = strings.Map(func(r rune) rune {
		if r < 0x20 || strings.ContainsRune(`/\:*?"<>|`, r) {
			return '_'
		}
		return r
	}, s)
	s = strings.Trim(s, " .")
	if runes := []rune(s); len(runes) > 200 {
		s = string(runes[:200])
	}
	if s == "" {
		return "download"
	}
	return s
}
