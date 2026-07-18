package libgen

import (
	"bufio"
	"bytes"
	"context"
	cryptomd5 "crypto/md5" //nolint:gosec // MD5 is the digest LibGen keys files by; used only for integrity matching.
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
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
	// Verified reports whether the downloaded file's MD5 digest matched the
	// requested md5 (integrity confirmed end to end).
	Verified bool `json:"verified"`
	// Resumed reports whether the download continued from a pre-existing partial
	// (the CDN honored a Range request) rather than starting from zero.
	Resumed bool `json:"resumed"`
}

// errIntegrityCheckFailed is returned when the downloaded content's MD5 digest
// does not match the requested md5 (corrupt or tampered download).
var errIntegrityCheckFailed = errors.New("integrity check failed: MD5 mismatch")

// Download descarga el fichero md5 a dir. Si filename está vacío usa el nombre
// que anuncia el CDN (content-disposition), saneado.
func (c *Client) Download(ctx context.Context, md5, dir, filename string) (*DownloadResult, error) {
	// Acquire a concurrency slot before doing any work, releasing it on return.
	// While waiting, honor context cancellation so a queued download can be
	// aborted before it ever touches the network.
	select {
	case c.dlSem <- struct{}{}:
		defer func() { <-c.dlSem }()
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	fileURL, base, err := c.ResolveGetURL(ctx, md5)
	if err != nil {
		return nil, err
	}
	// A stable partial path lets an interrupted download resume: if bytes are
	// already on disk, ask the CDN to continue from that offset with a Range.
	partPath := filepath.Join(dir, ".libgen-mcp-"+md5+".part")
	resumeFrom := partialSize(partPath)

	resp, err := c.fetchFile(ctx, fileURL, resumeFrom)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	// Decide whether the CDN honored the Range: a 206 continues the partial; a 200
	// (server ignored Range) forces a restart from zero, truncating the partial.
	var resume bool
	switch {
	case resumeFrom > 0 && resp.StatusCode == http.StatusPartialContent:
		resume = true
	case resp.StatusCode == http.StatusOK:
		resume = false
	default:
		return nil, fmt.Errorf("download failed: status %d from %s", resp.StatusCode, base)
	}

	// Full expected size: on a resumed 206, Content-Length covers only the range,
	// so add the bytes already on disk to enforce the cap against the whole file.
	totalLen := resp.ContentLength
	if resume && resp.ContentLength >= 0 {
		totalLen = resumeFrom + resp.ContentLength
	}
	body, original, err := c.validateFileResponse(resp, totalLen)
	if err != nil {
		return nil, err
	}

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
	dest := filepath.Join(dir, name)
	n, err := c.streamToPartAndVerify(partPath, dest, md5, body, resume, resumeFrom, resp.ContentLength)
	if err != nil {
		return nil, err
	}
	return &DownloadResult{
		Path:             dest,
		SizeBytes:        n,
		OriginalFilename: original,
		Mirror:           base,
		Verified:         true,
		Resumed:          resume,
	}, nil
}

// partialSize returns the size of a usable partial download at partPath, or 0 if
// there is none (missing, empty, or a directory).
func partialSize(partPath string) int64 {
	info, err := os.Stat(partPath)
	if err != nil || info.IsDir() {
		return 0
	}
	return info.Size()
}

// fetchFile issues the download GET, waiting on the rate limiter first. When
// resumeFrom > 0 it adds a Range header so the CDN can continue an interrupted
// download from that offset. The caller owns closing the returned body.
func (c *Client) fetchFile(ctx context.Context, fileURL string, resumeFrom int64) (*http.Response, error) {
	if werr := c.limiter.Wait(ctx); werr != nil {
		return nil, werr
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, fileURL, http.NoBody)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", userAgent)
	if resumeFrom > 0 {
		req.Header.Set("Range", "bytes="+strconv.FormatInt(resumeFrom, 10)+"-")
	}
	resp, err := c.dl.Do(req) // c.dl sin timeout global: descargas largas, gobierna ctx
	if err != nil {
		return nil, fmt.Errorf("downloading file: %w", err)
	}
	return resp, nil
}

// validateFileResponse rejects HTML error pages (by Content-Type and by sniffing
// the first bytes) and enforces the size cap against totalLen (the full expected
// file size). It returns a buffered reader positioned at the start of the
// streamed bytes and the CDN-advertised original filename.
func (c *Client) validateFileResponse(resp *http.Response, totalLen int64) (*bufio.Reader, string, error) {
	if strings.HasPrefix(resp.Header.Get("Content-Type"), "text/html") {
		return nil, "", errors.New("mirror returned an HTML page instead of the file (key expired or download blocked)")
	}
	// Enforce the size cap up front when the size is known: fail before creating
	// any file so an oversized download never touches the disk.
	if c.maxDownloadBytes > 0 && totalLen > c.maxDownloadBytes {
		return nil, "", fmt.Errorf("%w: file is %d bytes, limit is %d bytes", errDownloadTooLarge, totalLen, c.maxDownloadBytes)
	}
	// Algunos CDN sirven páginas de error como application/octet-stream (o sin
	// Content-Type). Olfateamos los primeros bytes sin consumirlos: Peek deja
	// los bytes en el bufio.Reader para que io.Copy los vuelva a leer.
	body := bufio.NewReader(resp.Body)
	head, err := body.Peek(512)
	if err != nil && !errors.Is(err, io.EOF) {
		return nil, "", fmt.Errorf("reading file header: %w", err)
	}
	if looksLikeHTML(head) {
		return nil, "", errors.New("mirror returned what looks like an HTML page instead of the file (key expired or download blocked)")
	}
	return body, filenameFromDisposition(resp.Header.Get("Content-Disposition")), nil
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

// streamToPartAndVerify streams body into the stable partial at partPath while
// computing the MD5 of the whole file, then verifies the digest against wantMD5
// and atomically renames the partial to dest on success. It returns the final
// file size.
//
// When resume is true it appends to the existing partial and primes the hash by
// re-reading the existingSize bytes already on disk, so the final digest covers
// the entire file; otherwise it truncates and starts fresh. contentLength, when
// known, is the number of bytes expected from the body (the range length on a
// resume) and is checked to detect a truncated transfer.
//
// Partial lifecycle: on an MD5 mismatch (corrupt/tampered) or an oversized
// transfer the partial is deleted; on a transient failure (network drop, short
// read) it is kept so a later call can resume from where it stopped.
func (c *Client) streamToPartAndVerify(partPath, dest, wantMD5 string, body io.Reader, resume bool, existingSize, contentLength int64) (int64, error) {
	flag := os.O_RDWR | os.O_CREATE
	var startSize int64
	if resume {
		startSize = existingSize
	} else {
		flag |= os.O_TRUNC // restart: discard any stale partial
	}
	f, err := os.OpenFile(partPath, flag, 0o600)
	if err != nil {
		return 0, err
	}

	digest := cryptomd5.New() //nolint:gosec // integrity match against the LibGen-provided md5.
	if resume {
		// Re-hash the bytes already on disk so the digest covers the whole file;
		// io.Copy also leaves the file offset at the end, ready to append.
		if _, rerr := io.Copy(digest, f); rerr != nil {
			f.Close()
			return 0, fmt.Errorf("rehashing partial: %w", rerr) // keep .part for a later resume
		}
	}

	// countingWriter aborts if the total size exceeds the cap; this defends
	// against downloads with no (or a lying) Content-Length header. The MD5 is
	// updated in lockstep with the bytes written to the file.
	cw := &countingWriter{w: io.MultiWriter(f, digest), limit: c.maxDownloadBytes, written: startSize}
	streamed, copyErr := io.Copy(cw, body)
	closeErr := f.Close()
	if copyErr != nil || closeErr != nil {
		// An oversized transfer can never succeed, so drop the partial; any other
		// (transient) failure keeps it so a later call can resume.
		if errors.Is(copyErr, errDownloadTooLarge) {
			os.Remove(partPath)
		}
		return 0, fmt.Errorf("writing file: %w", errors.Join(copyErr, closeErr))
	}
	if contentLength > 0 && streamed != contentLength {
		// Short read: keep the partial so a later call can resume from here.
		return 0, fmt.Errorf("truncated download: got %d of %d bytes", streamed, contentLength)
	}
	if got := hex.EncodeToString(digest.Sum(nil)); !strings.EqualFold(got, wantMD5) {
		os.Remove(partPath) // corrupt or tampered: the partial is useless, discard it
		return 0, fmt.Errorf("%w: got %s, want %s", errIntegrityCheckFailed, got, wantMD5)
	}
	if rerr := os.Rename(partPath, dest); rerr != nil {
		return 0, rerr // content is valid; keep the partial so a retry can rename it
	}
	return startSize + streamed, nil
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
