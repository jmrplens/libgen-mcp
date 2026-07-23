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
	"sync/atomic"
	"time"

	xhtml "golang.org/x/net/html"
)

// ProgressFunc reports live download progress: done is the number of bytes
// written so far and total is the full expected file size (0 or negative when
// the size is unknown). It is invoked throttled while streaming plus a final
// time when the transfer completes (done == total on a size-known download). A
// nil ProgressFunc disables progress reporting.
type ProgressFunc func(done, total int64)

// progressInterval and progressFraction bound how often a ProgressFunc fires:
// at most one call per interval, or whenever progress advances by the fraction
// of the total. The final completion call always fires regardless.
const (
	progressInterval = 500 * time.Millisecond
	progressFraction = 20 // 1/20 == every ~5% of the total
)

// progressWriter wraps an io.Writer and reports throttled progress to a
// ProgressFunc as bytes flow through. done is seeded with any bytes already on
// disk (a resumed download) so reports cover the whole file, not just this run.
type progressWriter struct {
	w        io.Writer
	progress ProgressFunc
	total    int64
	done     int64
	lastAt   time.Time
	lastDone int64
}

// Write forwards p to the wrapped writer and reports throttled progress.
func (pw *progressWriter) Write(p []byte) (int, error) {
	n, err := pw.w.Write(p)
	pw.done += int64(n)
	if pw.progress != nil && pw.shouldEmit() {
		pw.progress(pw.done, pw.total)
		pw.lastAt = time.Now()
		pw.lastDone = pw.done
	}
	return n, err
}

// shouldEmit reports whether enough time has elapsed (progressInterval) or
// enough bytes have advanced (progressFraction of the total) to emit again.
func (pw *progressWriter) shouldEmit() bool {
	if time.Since(pw.lastAt) >= progressInterval {
		return true
	}
	return pw.total > 0 && pw.done-pw.lastDone >= pw.total/progressFraction
}

// emitFinal reports a final progress call at the fully written size, bypassing
// the throttle so completion is always observed.
func (pw *progressWriter) emitFinal() {
	if pw.progress != nil {
		pw.progress(pw.done, pw.total)
	}
}

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

// ExtractGetLink locates the get.php?md5=…&key=… link inside the ads.php page.
func ExtractGetLink(body []byte) (string, error) {
	m := getLinkRe.Find(body)
	if m == nil {
		return "", fmt.Errorf("%w: no get.php key link in ads page", ErrLayoutChanged)
	}
	return xhtml.UnescapeString(string(m)), nil
}

// ResolveGetURL obtains the direct download URL (with a fresh key) for an md5.
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

// DownloadResult describes a completed download: where the file landed, its size
// and mirror, and whether it was integrity-verified and/or resumed.
type DownloadResult struct {
	Path             string `json:"path" jsonschema:"absolute path of the saved file"`
	SizeBytes        int64  `json:"size_bytes" jsonschema:"final file size in bytes"`
	OriginalFilename string `json:"original_filename,omitempty" jsonschema:"the name the mirror/CDN announced, if any"`
	Mirror           string `json:"mirror" jsonschema:"the scheme://host origin that served the bytes"`
	// Source is the Name() of the DownloadSource that served the file (e.g.
	// "libgen"), identifying which provider in the chain succeeded.
	Source string `json:"source,omitempty" jsonschema:"the source that served the file (libgen randombook unpaywall or scihub)"`
	// Verified reports whether the downloaded file's MD5 digest matched the
	// requested md5 (integrity confirmed end to end). It is false when the serving
	// source did not request MD5 verification.
	Verified bool `json:"verified" jsonschema:"true when the bytes' MD5 matched the requested md5 (book downloads); false for DOI sources"`
	// Resumed reports whether the download continued from a pre-existing partial
	// (the CDN honored a Range request) rather than starting from zero.
	Resumed bool `json:"resumed" jsonschema:"true when the download resumed from a pre-existing partial via an HTTP Range request"`
}

// errIntegrityCheckFailed is returned when the downloaded content's MD5 digest
// does not match the requested md5 (corrupt or tampered download).
var errIntegrityCheckFailed = errors.New("integrity check failed: MD5 mismatch")

// errStartFailed marks the errors that the staged start-retry schedule retries:
// a resolve error, a connection error, a non-2xx status, or a 2xx response that
// yields no first byte. Once bytes flow the download is "begun" and later
// failures (HTML page, size cap, stall, short read, MD5 mismatch) are returned
// unwrapped so start-retries do not fire for them.
var errStartFailed = errors.New("could not begin the download")

// errDownloadCouldNotStart is the actionable error surfaced when a source (and,
// once joined by DownloadItem, every source) fails to get a download started
// within the full retry schedule. Its message guides the model on how to recover.
var errDownloadCouldNotStart = errors.New("download could not be started after the retry schedule (resolve/connect/first-byte kept failing); you can retry now, retry later once the mirror recovers, or ask the user how they want to proceed")

// errStalled is returned when a streaming download receives no bytes for the
// configured stall window. The partial is kept so a later call can resume.
var errStalled = errors.New("download stalled: no bytes received within the stall timeout")

// startErr wraps err as a start-phase failure (one the start-retry schedule
// should retry).
func startErr(err error) error {
	return fmt.Errorf("%w: %w", errStartFailed, err)
}

// FileMeta carries the bibliographic fields used to build a clean, human-readable
// download filename when the mirror announces no name. Any field may be empty;
// cleanFileName omits the empty pieces.
type FileMeta struct {
	// Author is the work's author (or authors), used as the leading name segment.
	Author string
	// Title is the work's title, the mandatory core of the filename.
	Title string
	// Year is the publication year, rendered in parentheses after the title.
	Year string
	// Ext is the file extension (without a leading dot), e.g. "pdf" or "epub".
	Ext string
}

// cleanFileName builds a human-readable filename from bibliographic metadata in
// the form "<Author> - <Title> (<Year>).<Ext>", omitting any empty piece:
// no year drops the "(<Year>)" segment, no author drops the "<Author> - " prefix,
// and no extension drops the ".<Ext>" suffix. Textual pieces have their internal
// whitespace collapsed and illegal path characters stripped via sanitizeFilename.
// It returns "" when the title is empty, so the caller can fall back to the md5.
func cleanFileName(m FileMeta) string {
	author := cleanNamePiece(m.Author)
	title := cleanNamePiece(m.Title)
	year := cleanNamePiece(m.Year)
	if title == "" {
		return ""
	}
	var b strings.Builder
	if author != "" {
		b.WriteString(author)
		b.WriteString(" - ")
	}
	b.WriteString(title)
	if year != "" {
		b.WriteString(" (")
		b.WriteString(year)
		b.WriteString(")")
	}
	if ext := cleanNamePiece(strings.TrimLeft(m.Ext, ".")); ext != "" {
		b.WriteString(".")
		b.WriteString(ext)
	}
	return b.String()
}

// cleanNamePiece collapses internal whitespace runs to single spaces (trimming
// the ends) and strips illegal path characters. It returns "" for a piece that is
// empty or all whitespace, so callers can omit it from the assembled filename.
func cleanNamePiece(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if s == "" {
		return ""
	}
	return sanitizeFilename(s)
}

// chooseFileName selects the sanitized output filename by priority: an explicit
// filename, else the CDN-announced disposition name, else a clean name built from
// meta (when non-nil and it yields a name), else the md5 (which may be empty for
// non-md5 sources, in which case sanitizeFilename yields a safe default). When the
// resulting name has no extension and fallbackExt is set, that extension is
// appended (source-provided type hint for names that would otherwise be
// extensionless).
func chooseFileName(filename, disposition string, meta *FileMeta, md5, fallbackExt string) string {
	name := filename
	if name == "" {
		name = disposition
	}
	if name == "" && meta != nil {
		name = cleanFileName(*meta)
	}
	if name == "" {
		name = md5
	}
	name = sanitizeFilename(name)
	if ext := strings.TrimLeft(fallbackExt, "."); ext != "" && filepath.Ext(name) == "" {
		name = sanitizeFilename(name + "." + ext)
	}
	return name
}

// Download downloads the md5 file into dir. It is a convenience wrapper over
// DownloadItem for md5-keyed (book) downloads; DOI-keyed (article) downloads go
// through DownloadItem directly. The output name is chosen in order: an explicit
// filename, else the name the CDN announces (content-disposition), else a clean
// name built from meta (when non-nil and it yields a name), else the md5; the
// chosen name is sanitized. An optional progress callback (only the first is used)
// is invoked throttled with the running and total byte counts; pass none to
// disable progress reporting.
func (c *Client) Download(ctx context.Context, md5, dir, filename string, meta *FileMeta, progress ...ProgressFunc) (*DownloadResult, error) {
	return c.DownloadItem(ctx, Item{MD5: md5, Meta: meta}, dir, filename, progress...)
}

// selectSources returns the chain to try for a download: the full configured
// chain when name is empty, or just the named source when it is set. A named
// source that is not in the chain yields an actionable "not enabled" error that
// points at the relevant configuration (and, for unpaywall, its email gate).
func (c *Client) selectSources(name string) ([]DownloadSource, error) {
	if name == "" {
		return c.sources, nil
	}
	sources := sourcesNamed(c.sources, name)
	if len(sources) == 0 {
		hint := "check LIBGEN_MCP_SOURCES"
		if strings.EqualFold(name, "unpaywall") {
			hint = "check LIBGEN_MCP_SOURCES; unpaywall also requires a contact email in LIBGEN_MCP_UNPAYWALL_EMAIL"
		}
		return nil, fmt.Errorf("download source %q is not enabled (%s)", name, hint)
	}
	return sources, nil
}

// withPerCallUnpaywall augments the selected source chain for a single call so a
// per-call Unpaywall email (supplied on demand) can pull the Unpaywall source into
// a DOI download even when the server configured no email and thus left Unpaywall
// out of the chain. It returns a NEW slice (never mutating the client's shared
// chain): when item carries an Email and a DOI, targets the whole chain (item.Source
// unset), and the chain does not already include an enabled Unpaywall source, it
// prepends an ad-hoc unpaywallSource keyed by item.Email. When Unpaywall is already
// present its Resolve honors item.Email directly, so no prepend is needed (avoiding
// trying Unpaywall twice). When item.Email is empty NOTHING changes: the chain is
// returned as-is, keeping the default (headless) behavior byte-identical to today.
func (c *Client) withPerCallUnpaywall(item Item, sources []DownloadSource) []DownloadSource {
	if item.Email == "" || item.DOI == "" || item.Source != "" {
		return sources
	}
	for _, s := range sources {
		if s.Name() == "unpaywall" {
			return sources
		}
	}
	adhoc := unpaywallSource{email: item.Email, http: c.http, baseURL: c.unpaywallBase}
	return append([]DownloadSource{adhoc}, sources...)
}

// ResolvedDownload is a direct download URL produced by ResolveLink: the bytes
// are NOT fetched, so the caller (a remote MCP client, or an agent's own fetch
// tool) can retrieve the file itself, wherever it is running. Header carries any
// request headers the URL requires (e.g. a Referer for Sci-Hub); it is nil when
// the URL is fetchable as-is (libgen and randombook resolve to bare CDN URLs).
type ResolvedDownload struct {
	URL       string
	Header    http.Header
	VerifyMD5 bool
	Ext       string
	Source    string
}

// ResolveLink resolves item to a direct download URL through the configured
// source chain (honoring item.Source) WITHOUT downloading any bytes, returning
// the first source that resolves. It is the remote-friendly counterpart of
// DownloadItem: use it when the file must be fetched by the caller rather than
// written to this server's disk (e.g. a hosted HTTP deployment, where the server
// and the user are different machines). The first supporting source that resolves
// wins; if none do, the joined per-source errors are returned.
func (c *Client) ResolveLink(ctx context.Context, item Item) (ResolvedDownload, error) {
	sources, err := c.selectSources(item.Source)
	if err != nil {
		return ResolvedDownload{}, err
	}
	sources = c.withPerCallUnpaywall(item, sources)
	var errs []error
	for _, src := range sources {
		if !src.Supports(item) {
			continue
		}
		resolved, rerr := src.Resolve(ctx, item)
		if rerr != nil {
			errs = append(errs, fmt.Errorf("source %s: %w", src.Name(), rerr))
			if ctx.Err() != nil {
				break
			}
			continue
		}
		return ResolvedDownload{
			URL:       resolved.FileURL,
			Header:    resolved.Header,
			VerifyMD5: resolved.VerifyMD5,
			Ext:       resolved.Ext,
			Source:    src.Name(),
		}, nil
	}
	if len(errs) == 0 {
		return ResolvedDownload{}, fmt.Errorf("no download source supports md5=%q doi=%q", item.MD5, item.DOI)
	}
	return ResolvedDownload{}, errors.Join(errs...)
}

// DownloadItem downloads the file identified by item (an md5, a DOI, or both)
// into dir, trying each supporting source in the configured chain until one
// succeeds. The output name is chosen as documented on Download. An optional
// progress callback (only the first is used) reports throttled byte counts; pass
// none to disable progress reporting.
func (c *Client) DownloadItem(ctx context.Context, item Item, dir, filename string, progress ...ProgressFunc) (*DownloadResult, error) {
	onProgress := firstProgress(progress)
	// Acquire a concurrency slot before doing any work, releasing it on return.
	// While waiting, honor context cancellation so a queued download can be
	// aborted before it ever touches the network.
	if err := c.acquireSlot(ctx); err != nil {
		return nil, err
	}
	defer c.releaseSlot()

	// When the item names a source, restrict the download to that single source;
	// otherwise try the whole configured chain in order.
	sources, selErr := c.selectSources(item.Source)
	if selErr != nil {
		return nil, selErr
	}
	sources = c.withPerCallUnpaywall(item, sources)

	req := downloadReq{item: item, dir: dir, filename: filename, onProgress: onProgress}
	// Try each supporting source in order: a source that fails to resolve or whose
	// stream is rejected (HTML page / integrity mismatch / short read) advances to
	// the next. The first success returns; if all fail, the joined errors surface.
	var errs []error
	for _, src := range sources {
		if !src.Supports(item) {
			continue
		}
		res, err := c.downloadFrom(ctx, src, req)
		if err == nil {
			return res, nil
		}
		errs = append(errs, fmt.Errorf("source %s: %w", src.Name(), err))
		// A canceled/expired context will not recover on the next source, so stop.
		if ctx.Err() != nil {
			break
		}
	}
	if len(errs) == 0 {
		if item.Source != "" {
			return nil, fmt.Errorf("source %q cannot serve md5=%q doi=%q (md5 uses libgen/randombook; doi uses unpaywall/scihub)", item.Source, item.MD5, item.DOI)
		}
		return nil, fmt.Errorf("no download source supports md5=%q doi=%q", item.MD5, item.DOI)
	}
	return nil, errors.Join(errs...)
}

// sourcesNamed returns the single source whose Name matches name (case-insensitive),
// or nil when no enabled source has that name.
func sourcesNamed(all []DownloadSource, name string) []DownloadSource {
	name = strings.ToLower(strings.TrimSpace(name))
	for _, s := range all {
		if s.Name() == name {
			return []DownloadSource{s}
		}
	}
	return nil
}

// acquireSlot takes a download concurrency slot, honoring context cancellation so
// a queued download can be aborted before it ever touches the network.
func (c *Client) acquireSlot(ctx context.Context) error {
	select {
	case c.dlSem <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// releaseSlot returns a previously acquired download concurrency slot.
func (c *Client) releaseSlot() { <-c.dlSem }

// downloadReq bundles the per-call download inputs (identity, destination,
// output name and progress sink) so they can be threaded through the source
// pipeline without a long parameter list.
type downloadReq struct {
	item       Item
	dir        string
	filename   string
	onProgress ProgressFunc
}

// downloadFrom runs a single source under the staged start-retry schedule: it
// keeps re-attempting to get the download STARTED (resolve a fresh URL, connect,
// and pull the first byte) on the configured wait schedule, and once bytes flow
// it hands off to streaming. A start that never succeeds within the schedule
// yields the actionable errDownloadCouldNotStart; a failure after streaming began
// (HTML page, stall, short read, MD5 mismatch, ...) is returned as-is so the
// caller can advance to the next source. Context cancellation between or within
// waits aborts immediately.
func (c *Client) downloadFrom(ctx context.Context, src DownloadSource, req downloadReq) (*DownloadResult, error) {
	waits := c.startRetryWaits
	var lastErr error
	for attempt := 0; ; attempt++ {
		res, err := c.startAttempt(ctx, src, req)
		if err == nil {
			return res, nil
		}
		// Only resolve/connect/status/no-first-byte failures are start-retryable;
		// anything raised once bytes were flowing is returned to the source loop.
		if !errors.Is(err, errStartFailed) {
			return nil, err
		}
		lastErr = err
		if attempt >= len(waits) {
			return nil, fmt.Errorf("%w: %w", errDownloadCouldNotStart, lastErr)
		}
		if werr := waitOrCancel(ctx, waits[attempt]); werr != nil {
			return nil, werr
		}
	}
}

// startAttempt makes one attempt to start and complete a download from src: it
// resolves the item afresh (so an expired key is renewed on each retry), takes
// the per-partial lock, and streams. Start-phase failures are wrapped with
// errStartFailed so downloadFrom retries them on the schedule.
func (c *Client) startAttempt(ctx context.Context, src DownloadSource, req downloadReq) (*DownloadResult, error) {
	resolved, err := src.Resolve(ctx, req.item)
	if err != nil {
		return nil, startErr(err)
	}
	// A stable partial path lets an interrupted download resume: if bytes are
	// already on disk, ask the CDN to continue from that offset with a Range. It is
	// keyed by md5 for md5 items (historical LibGen path) and by a DOI/URL hash
	// otherwise, so resume and locking work for every source. The serving source's
	// Name() is included so each source owns a distinct .part: different sources can
	// deliver different byte streams for the same logical item (e.g. Unpaywall's OA
	// PDF vs Sci-Hub's PDF for one DOI), and without this a partial left by one
	// source could be appended onto by the next via a resumed Range, silently
	// corrupting the file. This costs the libgen/randombook shared-resume
	// optimization (they now use distinct .part files), which is an acceptable
	// trade for cross-source correctness.
	partPath := filepath.Join(req.dir, ".libgen-mcp-"+sanitizeForPart(src.Name())+"-"+partialKey(req.item, resolved)+".part")
	if abs, aerr := filepath.Abs(partPath); aerr == nil {
		partPath = abs
	}
	// Serialize downloads that target the same partial file. The .part path is
	// deterministic, so two concurrent downloads of the same key into the same dir
	// would open/rehash/truncate/append the same file through separate fds and
	// corrupt each other (the semaphore only serializes when
	// MaxConcurrentDownloads==1). A per-path mutex makes them run one after
	// another; a duplicate concurrent request simply re-downloads and overwrites,
	// which is acceptable. The lock is refcounted so its map entry is removed once
	// the last holder releases.
	release := c.acquirePartialLock(partPath)
	defer release()

	return c.streamResolved(ctx, src, req, resolved, partPath)
}

// waitOrCancel blocks for d or until ctx is canceled, returning the context error
// when canceled so the start-retry schedule aborts immediately on cancellation.
func waitOrCancel(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// streamResolved runs the shared download pipeline for one resolved source:
// fetch (with resume), validate (HTML sniff + size cap), stream to the partial
// with optional MD5 verification, then atomically rename into place. It returns a
// completed DownloadResult tagged with the serving source's name.
func (c *Client) streamResolved(ctx context.Context, src DownloadSource, req downloadReq, resolved Resolved, partPath string) (*DownloadResult, error) {
	base := mirrorOf(resolved.FileURL)
	resumeFrom := partialSize(partPath)

	// Derive a cancelable context for this transfer: the stall guard cancels it
	// when no bytes arrive within the window, which unblocks the in-flight body
	// read. Caller cancellation still propagates through it as well.
	streamCtx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)

	resp, err := c.fetchFile(streamCtx, resolved.FileURL, resumeFrom, resolved.Header)
	if err != nil {
		return nil, startErr(err) // connection error: retryable
	}
	defer resp.Body.Close()

	resume, err := resumeDecision(resp, resumeFrom, base)
	if err != nil {
		return nil, startErr(err) // non-2xx status: retryable
	}

	// Full expected size: on a resumed 206, Content-Length covers only the range,
	// so add the bytes already on disk to enforce the cap against the whole file.
	totalLen := resp.ContentLength
	if resume && resp.ContentLength >= 0 {
		totalLen = resumeFrom + resp.ContentLength
	}
	// validateFileResponse tags a no-first-byte response with errStartFailed
	// (retryable); an HTML page or size-cap breach is returned unwrapped.
	body, original, err := c.validateFileResponse(resp, totalLen)
	if err != nil {
		return nil, err
	}

	name := chooseFileName(req.filename, original, req.item.Meta, req.item.MD5, resolved.Ext)

	if mkErr := os.MkdirAll(req.dir, 0o750); mkErr != nil {
		return nil, fmt.Errorf("creating download dir: %w", mkErr)
	}
	if derr := ensureDiskSpace(req.dir, resp.ContentLength); derr != nil {
		return nil, derr
	}
	dest := filepath.Join(req.dir, name)
	// Bytes are flowing: guard the stream against stalls (no wall-clock cap), then
	// stream to the partial. A stall aborts via streamCtx and keeps the .part.
	n, err := c.streamToPartAndVerify(partPath, dest, req.item.MD5, c.guardStall(body, cancel), streamOpts{
		resume: resume, existingSize: resumeFrom, contentLength: resp.ContentLength,
		total: totalLen, verify: resolved.VerifyMD5, progress: req.onProgress,
	})
	if err != nil {
		return nil, err
	}
	return &DownloadResult{
		Path:             dest,
		SizeBytes:        n,
		OriginalFilename: original,
		Mirror:           base,
		Source:           src.Name(),
		Verified:         resolved.VerifyMD5,
		Resumed:          resume,
	}, nil
}

// guardStall wraps r in a stall-detecting reader when a stall timeout is
// configured. The reader cancels the transfer (via cancel) when no bytes arrive
// within the window, aborting the in-flight body read; a non-positive timeout
// leaves r unwrapped so slow-but-progressing transfers are never capped by a
// wall-clock deadline.
func (c *Client) guardStall(r io.Reader, cancel context.CancelCauseFunc) io.Reader {
	if c.stallTimeout <= 0 {
		return r
	}
	return &stallReader{r: r, timeout: c.stallTimeout, cancel: cancel}
}

// stallReader enforces a progress-resetting read deadline: the timer is armed for
// timeout at the start of each Read and re-armed on the next, so a transfer is
// aborted only when NO bytes arrive within the window — never for being slow.
// When the timer fires it records the stall and cancels the transfer context
// (via cancel) with errStalled, which unblocks the underlying body read; the
// resulting error is reported as errStalled so callers see a clear stall rather
// than a bare context cancellation.
type stallReader struct {
	r       io.Reader
	timeout time.Duration
	cancel  context.CancelCauseFunc
	timer   *time.Timer
	stalled atomic.Bool
}

// Read forwards to the wrapped reader while keeping the stall timer armed, and
// translates a stall-triggered cancellation into errStalled.
func (s *stallReader) Read(p []byte) (int, error) {
	if s.timer == nil {
		s.timer = time.AfterFunc(s.timeout, func() { s.stalled.Store(true); s.cancel(errStalled) })
	} else {
		s.timer.Reset(s.timeout)
	}
	n, err := s.r.Read(p)
	if err != nil {
		s.timer.Stop()
		if s.stalled.Load() {
			return n, errStalled
		}
	}
	return n, err
}

// resumeDecision inspects the download response against the bytes already on disk
// and reports whether to append (resume) or restart from zero. A 206 whose
// Content-Range start matches resumeFrom resumes; a 200 restarts (server ignored
// the Range); any other status is a download failure. A 206 with a mismatched (or
// missing/unparseable) Content-Range restarts from zero rather than risk
// appending misaligned bytes onto the existing partial.
func resumeDecision(resp *http.Response, resumeFrom int64, base string) (bool, error) {
	switch {
	case resumeFrom > 0 && resp.StatusCode == http.StatusPartialContent:
		if start, ok := parseContentRangeStart(resp.Header.Get("Content-Range")); ok && start == resumeFrom {
			return true, nil
		}
		return false, nil
	case resp.StatusCode == http.StatusOK:
		return false, nil
	default:
		return false, fmt.Errorf("download failed: status %d from %s", resp.StatusCode, base)
	}
}

// firstProgress returns the first progress callback from the variadic optional
// argument, or nil when none was supplied.
func firstProgress(progress []ProgressFunc) ProgressFunc {
	if len(progress) > 0 {
		return progress[0]
	}
	return nil
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

// parseContentRangeStart parses the start offset from a Content-Range response
// header of the form "bytes <start>-<end>/<total>". It reports ok=false when the
// header is empty or does not match that shape, so callers can be conservative.
func parseContentRangeStart(header string) (start int64, ok bool) {
	const prefix = "bytes "
	if !strings.HasPrefix(header, prefix) {
		return 0, false
	}
	spec := strings.TrimPrefix(header, prefix)
	startStr, _, found := strings.Cut(spec, "-")
	if !found {
		return 0, false
	}
	n, err := strconv.ParseInt(strings.TrimSpace(startStr), 10, 64)
	if err != nil {
		return 0, false
	}
	return n, true
}

// fetchFile issues the download GET, waiting on the rate limiter first. Any
// source-supplied headers are applied on top of the default User-Agent. When
// resumeFrom > 0 it adds a Range header so the CDN can continue an interrupted
// download from that offset. The caller owns closing the returned body.
func (c *Client) fetchFile(ctx context.Context, fileURL string, resumeFrom int64, header http.Header) (*http.Response, error) {
	if werr := c.limiter.Wait(ctx); werr != nil {
		return nil, werr
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, fileURL, http.NoBody)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", userAgent)
	// Apply any source-specific headers (e.g. a Referer) on top of the defaults.
	for k, vs := range header {
		for _, v := range vs {
			req.Header.Add(k, v)
		}
	}
	if resumeFrom > 0 {
		req.Header.Set("Range", "bytes="+strconv.FormatInt(resumeFrom, 10)+"-")
	}
	resp, err := c.dl.Do(req) // c.dl has no global timeout: long downloads are governed by ctx
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
	// Some CDNs serve error pages as application/octet-stream (or with no
	// Content-Type). Sniff the first bytes without consuming them: Peek leaves
	// the bytes in the bufio.Reader so io.Copy can read them again. A read error
	// before any byte, or a 2xx that yields no bytes at all, is a "did not begin"
	// failure the start-retry schedule should retry, so it is tagged accordingly.
	body := bufio.NewReader(resp.Body)
	head, err := body.Peek(512)
	if err != nil && !errors.Is(err, io.EOF) {
		return nil, "", startErr(fmt.Errorf("reading first bytes: %w", err))
	}
	if len(head) == 0 {
		return nil, "", startErr(errors.New("response yielded no bytes"))
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

// streamOpts carries the streaming parameters for streamToPartAndVerify: how to
// treat any existing partial (resume vs. restart), the expected sizes, whether to
// enforce MD5 verification, and the progress sink.
type streamOpts struct {
	// resume appends to the existing partial (priming the hash from existingSize
	// bytes on disk) instead of truncating and starting fresh.
	resume bool
	// existingSize is the number of bytes already on disk when resuming.
	existingSize int64
	// contentLength, when > 0, is the number of bytes expected from the body (the
	// range length on a resume) and is checked to detect a truncated transfer.
	contentLength int64
	// total is the full expected file size, used to report absolute progress.
	total int64
	// verify enables the final MD5 digest check against wantMD5. It is disabled for
	// sources whose files are not keyed by md5.
	verify bool
	// progress is the throttled progress sink; nil disables progress reporting.
	progress ProgressFunc
}

// openPartForStream opens (or creates) the partial file at partPath for
// streaming and reports how many bytes are already present. On a resume it
// re-hashes the bytes already on disk into digest so the final digest covers the
// whole file and leaves the file offset at the end, ready to append; on a fresh
// download it truncates any stale partial and reports a zero starting size. On a
// rehash failure it keeps the partial so a later call can resume.
func openPartForStream(partPath string, opts streamOpts, digest io.Writer) (*os.File, int64, error) {
	flag := os.O_RDWR | os.O_CREATE
	if !opts.resume {
		flag |= os.O_TRUNC // restart: discard any stale partial
	}
	f, err := os.OpenFile(partPath, flag, 0o600)
	if err != nil {
		return nil, 0, err
	}
	if !opts.resume {
		return f, 0, nil
	}
	// Re-hash the bytes already on disk so the digest covers the whole file;
	// io.Copy also leaves the file offset at the end, ready to append.
	if _, rerr := io.Copy(digest, f); rerr != nil {
		f.Close()
		return nil, 0, fmt.Errorf("rehashing partial: %w", rerr) // keep .part for a later resume
	}
	return f, opts.existingSize, nil
}

// streamToPartAndVerify streams body into the stable partial at partPath while
// computing the MD5 of the whole file, then (when opts.verify) checks the digest
// against wantMD5 and atomically renames the partial to dest on success. It
// returns the final file size.
//
// When opts.resume is true it appends to the existing partial and primes the hash
// by re-reading the existingSize bytes already on disk, so the final digest covers
// the entire file; otherwise it truncates and starts fresh. The re-hash also
// advances the file offset to the end for appending, so it runs whether or not
// verification is requested. contentLength, when known, is the number of bytes
// expected from the body (the range length on a resume) and is checked to detect
// a truncated transfer.
//
// Partial lifecycle: on an MD5 mismatch (corrupt/tampered) or an oversized
// transfer the partial is deleted; on a transient failure (network drop, short
// read) it is kept so a later call can resume from where it stopped.
func (c *Client) streamToPartAndVerify(partPath, dest, wantMD5 string, body io.Reader, opts streamOpts) (int64, error) {
	digest := cryptomd5.New() //nolint:gosec // integrity match against the LibGen-provided md5.
	f, startSize, err := openPartForStream(partPath, opts, digest)
	if err != nil {
		return 0, err
	}

	// countingWriter aborts if the total size exceeds the cap; this defends
	// against downloads with no (or a lying) Content-Length header. The MD5 is
	// updated in lockstep with the bytes written to the file.
	cw := &countingWriter{w: io.MultiWriter(f, digest), limit: c.maxDownloadBytes, written: startSize}
	// progressWriter reports throttled progress over the whole file: seed done
	// with the bytes already on disk so a resumed download reports absolute totals.
	pw := &progressWriter{w: cw, progress: opts.progress, total: opts.total, done: startSize, lastAt: time.Now(), lastDone: startSize}
	streamed, copyErr := io.Copy(pw, body)
	closeErr := f.Close()
	if copyErr != nil || closeErr != nil {
		// An oversized transfer can never succeed, so drop the partial; any other
		// (transient) failure keeps it so a later call can resume.
		if errors.Is(copyErr, errDownloadTooLarge) {
			os.Remove(partPath)
		}
		return 0, fmt.Errorf("writing file: %w", errors.Join(copyErr, closeErr))
	}
	if opts.contentLength > 0 && streamed != opts.contentLength {
		// Short read: keep the partial so a later call can resume from here.
		return 0, fmt.Errorf("truncated download: got %d of %d bytes", streamed, opts.contentLength)
	}
	pw.emitFinal() // report completion (done == total) regardless of throttle
	// MD5 verification is conditional: only sources keyed by md5 request it. The
	// size cap, HTML sniff, resume and atomic rename apply to every source.
	if opts.verify {
		if got := hex.EncodeToString(digest.Sum(nil)); !strings.EqualFold(got, wantMD5) {
			os.Remove(partPath) // corrupt or tampered: the partial is useless, discard it
			return 0, fmt.Errorf("%w: got %s, want %s", errIntegrityCheckFailed, got, wantMD5)
		}
	}
	if rerr := os.Rename(partPath, dest); rerr != nil {
		return 0, rerr // content is valid; keep the partial so a retry can rename it
	}
	return startSize + streamed, nil
}

// looksLikeHTML reports whether b (a sniffed body header) begins, after trimming
// leading ASCII whitespace, with an HTML document marker.
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
