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

// diskSpaceMargin is the extra free space required beyond the expected download
// size, covering filesystem overhead and rounding.
const diskSpaceMargin = 8 << 20 // 8 MiB

// errDownloadTooLarge is returned when a download exceeds the configured size cap.
var errDownloadTooLarge = errors.New("download exceeds the configured size limit")

// freeSpaceFn probes the free space of a directory. It is a package var so tests
// can inject a stub that simulates an insufficient amount of disk space.
var freeSpaceFn = freeSpace

// countingWriter forwards writes to w while tracking the running total, and
// aborts with an error once that total would exceed limit. A limit of 0 disables
// the cap. It guards against downloads whose size is unknown up front (no
// Content-Length) or larger than the server advertised.
type countingWriter struct {
	w       io.Writer
	limit   int64
	written int64
}

// Write forwards p to the wrapped writer, or returns an error without writing if
// doing so would push the running total past the limit.
func (cw *countingWriter) Write(p []byte) (int, error) {
	if cw.limit > 0 && cw.written+int64(len(p)) > cw.limit {
		return 0, fmt.Errorf("%w of %d bytes", errDownloadTooLarge, cw.limit)
	}
	n, err := cw.w.Write(p)
	cw.written += int64(n)
	return n, err
}

// ExtractGetLink localiza el enlace get.php?md5=…&key=… dentro de la página ads.php.
func ExtractGetLink(body []byte) (string, error) {
	m := getLinkRe.Find(body)
	if m == nil {
		return "", fmt.Errorf("%w: no get.php key link in ads page", ErrLayoutChanged)
	}
	return xhtml.UnescapeString(string(m)), nil
}

// ResolveGetURL obtiene la URL directa de descarga (con key fresca) para un md5.
func (c *Client) ResolveGetURL(ctx context.Context, md5 string) (getURL, base string, err error) {
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
	if werr := c.limiter.Wait(ctx); werr != nil {
		return nil, werr
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, fileURL, http.NoBody)
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
	// Enforce the size cap up front when the CDN advertises a Content-Length: fail
	// before creating any file so an oversized download never touches the disk.
	if c.maxDownloadBytes > 0 && resp.ContentLength > c.maxDownloadBytes {
		return nil, fmt.Errorf("%w: file is %d bytes, limit is %d bytes", errDownloadTooLarge, resp.ContentLength, c.maxDownloadBytes)
	}
	// Algunos CDN sirven páginas de error como application/octet-stream (o sin
	// Content-Type). Olfateamos los primeros bytes sin consumirlos: Peek deja
	// los bytes en el bufio.Reader para que io.Copy los vuelva a leer.
	body := bufio.NewReader(resp.Body)
	head, err := body.Peek(512)
	if err != nil && !errors.Is(err, io.EOF) {
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

	if mkErr := os.MkdirAll(dir, 0o750); mkErr != nil {
		return nil, fmt.Errorf("creating download dir: %w", mkErr)
	}
	if derr := ensureDiskSpace(dir, resp.ContentLength); derr != nil {
		return nil, derr
	}
	dest, n, err := c.streamToFile(dir, name, body, resp.ContentLength)
	if err != nil {
		return nil, err
	}
	return &DownloadResult{Path: dest, SizeBytes: n, OriginalFilename: original, Mirror: base}, nil
}

// ensureDiskSpace verifies, when the size is known, that the download fits on
// disk (plus a small margin for filesystem overhead) before streaming begins.
// Best-effort: a probe error (e.g. an unsupported platform) lets it proceed.
func ensureDiskSpace(dir string, contentLength int64) error {
	if contentLength <= 0 {
		return nil
	}
	// Best-effort: a probe error (e.g. an unsupported platform) lets it proceed.
	free, ferr := freeSpaceFn(dir)
	if ferr == nil {
		if need := uint64(contentLength) + diskSpaceMargin; need > free {
			return fmt.Errorf("not enough free disk space in %s: need ~%d bytes, have %d", dir, need, free)
		}
	}
	return nil
}

// streamToFile streams body into a temp file in dir, enforcing the size cap via a
// countingWriter, then atomically renames it to name. On any error it removes the
// temp file and returns. contentLength, when known, is checked against the bytes
// actually written to detect truncated downloads.
func (c *Client) streamToFile(dir, name string, body io.Reader, contentLength int64) (dest string, n int64, err error) {
	tmp, err := os.CreateTemp(dir, ".libgen-mcp-*")
	if err != nil {
		return "", 0, err
	}
	// countingWriter aborts if the streamed size exceeds the cap; this defends
	// against downloads with no (or a lying) Content-Length header.
	n, copyErr := io.Copy(&countingWriter{w: tmp, limit: c.maxDownloadBytes}, body)
	closeErr := tmp.Close()
	if copyErr != nil || closeErr != nil {
		os.Remove(tmp.Name())
		return "", 0, fmt.Errorf("writing file: %w", errors.Join(copyErr, closeErr))
	}
	if contentLength > 0 && n != contentLength {
		os.Remove(tmp.Name())
		return "", 0, fmt.Errorf("truncated download: got %d of %d bytes", n, contentLength)
	}
	dest = filepath.Join(dir, name)
	if rerr := os.Rename(tmp.Name(), dest); rerr != nil {
		os.Remove(tmp.Name())
		return "", 0, rerr
	}
	return dest, n, nil
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
