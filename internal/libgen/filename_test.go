package libgen

import (
	"bufio"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"unicode/utf8"
)

// realWorldNames are filenames observed arriving from live mirrors, kept here so
// the cleaning rules are exercised against the actual noise rather than an
// idealized version of it.
const (
	// nameTalebLibgen is a LibGen name carrying a series prefix that must survive,
	// a co-author separator, a truncation ellipsis, a bracketed DOI and a mirror
	// suffix.
	nameTalebLibgen = "[Incerto] Taleb, Nassim Nicholas_ Ochman, Joe - Antifragile_ Things That Gain from Disorde… " +
		"[10.1371_journal.pmed.0020124] - libgen.li.epub"
	// nameCatcher is a name that is already correct and must come through untouched.
	nameCatcher = "The Catcher in the Rye (2010).epub"
	// nameArchivePlaceholder is what the Internet Archive returns for every ISBN.
	nameArchivePlaceholder = "download.pdf"
	// namePrecedings is an ugly but genuine publisher identifier: not a placeholder,
	// so it must be kept as the evidence it is.
	namePrecedings = "npre2007361-1.pdf"
)

// TestChooseFileNameVerifiedPrefersMetadata covers the verified branch: bytes
// checked against the requested md5 are provably that record, so the built name
// wins over whatever the mirror announced.
func TestChooseFileNameVerifiedPrefersMetadata(t *testing.T) {
	meta := &FileMeta{Author: "Nassim Nicholas Taleb", Title: "Antifragile: Things That Gain from Disorder", Year: "2012", Ext: "pdf"}
	got, origin := chooseFileName(nameRequest{
		announced: nameTalebLibgen,
		sniffed:   "epub",
		item:      Item{MD5: "0123456789abcdef0123456789abcdef", Meta: meta},
		verified:  true,
	})
	const want = "Nassim Nicholas Taleb - Antifragile - Things That Gain from Disorder (2012).epub"
	if got != want {
		t.Errorf("chooseFileName() = %q, want %q", got, want)
	}
	if origin != NameFromMetadata {
		t.Errorf("origin = %q, want %q", origin, NameFromMetadata)
	}
	if !origin.Derived() {
		t.Error("a metadata name is derived")
	}
}

// TestChooseFileNameUnverifiedKeepsAnnounced covers the rule that protects a
// mis-delivery from being disguised: with no digest to check, the announced name
// is kept (minus the mirror's marks) even though richer metadata is at hand.
func TestChooseFileNameUnverifiedKeepsAnnounced(t *testing.T) {
	meta := &FileMeta{Author: "John P. A. Ioannidis", Title: "Why Most Published Research Findings Are False", Year: "2005"}
	got, origin := chooseFileName(nameRequest{
		announced: nameTalebLibgen,
		sniffed:   "epub",
		item:      Item{DOI: "10.1371/journal.pmed.0020124", Meta: meta},
	})
	const want = "[Incerto] Taleb, Nassim Nicholas_ Ochman, Joe - Antifragile_ Things That Gain from Disorde….epub"
	if got != want {
		t.Errorf("chooseFileName() = %q, want the announced name minus the mirror marks (%q)", got, want)
	}
	if origin != NameFromAnnounced {
		t.Errorf("origin = %q, want %q", origin, NameFromAnnounced)
	}
	if origin.Derived() {
		t.Error("an announced name is not derived")
	}
	if strings.Contains(got, "Ioannidis") {
		t.Error("an unverified download must never be renamed after the record that was requested")
	}
}

// TestChooseFileNameTable walks the naming rule across the identifier kinds, the
// placeholder fallbacks and the caller override.
func TestChooseFileNameTable(t *testing.T) {
	meta := &FileMeta{Author: "A", Title: "T", Year: "2020", Ext: "epub"}
	cases := []struct {
		name       string
		req        nameRequest
		want       string
		wantOrigin NameOrigin
	}{
		{
			"caller filename wins over everything",
			nameRequest{explicit: "mine.pdf", announced: nameCatcher, item: Item{MD5: "md5", Meta: meta}, verified: true},
			"mine.pdf", NameFromCaller,
		},
		{
			"verified with no metadata keeps the announced name",
			nameRequest{announced: nameCatcher, sniffed: "epub", item: Item{MD5: "md5"}, verified: true},
			nameCatcher, NameFromAnnounced,
		},
		{
			"verified with neither metadata nor a name falls back to the md5",
			nameRequest{sniffed: "pdf", item: Item{MD5: "abc123"}, verified: true},
			"abc123.pdf", NameFromIdentifier,
		},
		{
			"unverified placeholder falls back to the DOI",
			nameRequest{announced: nameArchivePlaceholder, item: Item{DOI: "10.1371/journal.pmed.0020124"}},
			"10.1371_journal.pmed.0020124.pdf", NameFromIdentifier,
		},
		{
			"unverified placeholder falls back to the ISBN",
			nameRequest{announced: nameArchivePlaceholder, item: Item{ISBN: "9789286150616"}},
			"9789286150616.pdf", NameFromIdentifier,
		},
		{
			"an ugly publisher identifier is evidence, not a placeholder",
			nameRequest{announced: namePrecedings, item: Item{DOI: "10.1038/npre.2007.361.1"}},
			namePrecedings, NameFromAnnounced,
		},
		{
			"unverified with no name and no identifier falls back to metadata",
			nameRequest{announced: nameArchivePlaceholder, item: Item{Meta: meta}},
			"A - T (2020).pdf", NameFromMetadata,
		},
		{
			"nothing at all still yields a usable name",
			nameRequest{},
			"download", NameFromIdentifier,
		},
		{
			"a verified download with nothing to go on still yields a usable name",
			nameRequest{sniffed: "pdf", verified: true},
			"download.pdf", NameFromIdentifier,
		},
		{
			"an announced name that merely restates the md5 carries nothing",
			nameRequest{
				announced: "0123456789abcdef0123456789abcdef.pdf",
				item:      Item{MD5: "0123456789abcdef0123456789abcdef"},
				verified:  true,
			},
			"0123456789abcdef0123456789abcdef.pdf", NameFromIdentifier,
		},
		{
			"an announced name that is only the md5 carries nothing",
			nameRequest{announced: "0123456789abcdef0123456789abcdef.pdf", item: Item{DOI: "10.1/x"}},
			"10.1_x.pdf", NameFromIdentifier,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, origin := chooseFileName(tc.req)
			if got != tc.want || origin != tc.wantOrigin {
				t.Errorf("chooseFileName() = (%q, %q), want (%q, %q)", got, origin, tc.want, tc.wantOrigin)
			}
		})
	}
}

// TestStripSourceMarks covers each mirror/scraper mark the cleaner knows, and —
// just as importantly — the strings it must leave alone.
func TestStripSourceMarks(t *testing.T) {
	cases := []struct{ in, want string }{
		{"Great Book - libgen.li", "Great Book"},
		{"Great Book - libgen", "Great Book"},
		{"Great Book -- Anna's Archive", "Great Book"},
		{"Great Book _ Z-Library", "Great Book"},
		{"Great Book (Z-Library)", "Great Book"},
		{"Great Book [libgen.is]", "Great Book"},
		{"Great Book [10.1371_journal.pmed.0020124]", "Great Book"},
		{"Great Book [9780123456789]", "Great Book"},
		{"Great Book -- 9780123456789", "Great Book"},
		{"Great Book 0123456789abcdef0123456789abcdef", "Great Book"},
		{"Great Book - [0123456789abcdef0123456789abcdef]", "Great Book"},
		{"libgen.li_Great Book", "Great Book"},
		{"Great Book [10.1371_x] - libgen.li", "Great Book"},
		// Must survive: a series name, an in-title year, a subtitle dash and a
		// hyphenated identifier that is not a mark.
		{"[Incerto] Antifragile", "[Incerto] Antifragile"},
		{"The Catcher in the Rye (2010)", "The Catcher in the Rye (2010)"},
		{"Go - The Complete Reference", "Go - The Complete Reference"},
		{"npre2007361-1", "npre2007361-1"},
		{"Structure and Interpretation (2nd ed.)", "Structure and Interpretation (2nd ed.)"},
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			if got := stripSourceMarks(tc.in); got != tc.want {
				t.Errorf("stripSourceMarks(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// TestMetadataName covers the built-name shape: every piece present, each
// optional piece omitted, a colon turned into a storable separator, and a
// multi-author field reduced to its first author.
func TestMetadataName(t *testing.T) {
	cases := []struct {
		name string
		meta *FileMeta
		ext  string
		want string
	}{
		{"all pieces", &FileMeta{Author: "Jane Doe", Title: "Great Book", Year: "2020"}, "pdf", "Jane Doe - Great Book (2020).pdf"},
		{"no year", &FileMeta{Author: "Jane Doe", Title: "Great Book"}, "pdf", "Jane Doe - Great Book.pdf"},
		{"no author", &FileMeta{Title: "Great Book", Year: "2020"}, "pdf", "Great Book (2020).pdf"},
		{"no title yields nothing", &FileMeta{Author: "Jane Doe", Year: "2020"}, "pdf", ""},
		{"nil meta yields nothing", nil, "pdf", ""},
		{"no extension", &FileMeta{Author: "Jane Doe", Title: "Great Book", Year: "2020"}, "", "Jane Doe - Great Book (2020)"},
		{"colon becomes a storable separator", &FileMeta{Title: "Antifragile: Things That Gain"}, "epub", "Antifragile - Things That Gain.epub"},
		{"several authors collapse", &FileMeta{Author: "Taleb, N.; Ochman, J.; Doe, J.", Title: "Antifragile"}, "epub", "Taleb, N. et al. - Antifragile.epub"},
		{"whitespace collapsed", &FileMeta{Author: " Jane   Doe ", Title: "Great\t\nBook ", Year: " 2020 "}, "pdf", "Jane Doe - Great Book (2020).pdf"},
		{"the catalog extension is never used", &FileMeta{Title: "Great Book", Ext: "epub"}, "", "Great Book"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := metadataName(tc.meta, tc.ext); got != tc.want {
				t.Errorf("metadataName() = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestSanitizeFilename covers each safety rule: path separators and
// Windows-illegal punctuation, control and bidi-override characters, leading
// dots and traversal, Windows device names, and the empty result.
func TestSanitizeFilename(t *testing.T) {
	cases := []struct{ name, in, want string }{
		{"path and illegal punctuation", `a/b\c:d*e?f"g<h>i|j.pdf`, "a_b_c_d_e_f_g_h_i_j.pdf"},
		{"surrounding whitespace", "  normal.epub  ", "normal.epub"},
		{"empty", "", "download"},
		{"only dots", "...", "download"},
		{"parent traversal", "..", "download"},
		{"absolute path", "/etc/passwd", "_etc_passwd"},
		{"relative traversal", "../../.ssh/authorized_keys", "_.._.ssh_authorized_keys"},
		{"control characters dropped", "book\x00\x1b\x07name.pdf", "bookname.pdf"},
		{"bidi override dropped", "invoice\u202efdp.exe", "invoicefdp.exe"},
		{"zero-width space dropped", "bo\u200bok.pdf", "book.pdf"},
		{"leading dot", ".hidden.pdf", "hidden.pdf"},
		{"windows device name", "NUL.pdf", "_NUL.pdf"},
		{"windows serial device", "com1", "_com1"},
		{"a name that merely contains a device word", "conference.pdf", "conference.pdf"},
		{"newlines collapse", "line\nbreak.pdf", "line break.pdf"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := sanitizeFilename(tc.in); got != tc.want {
				t.Errorf("sanitizeFilename(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// TestSanitizeFilenameNormalizesToNFC verifies that the two Unicode spellings of
// the same accented title produce one identical filename.
func TestSanitizeFilenameNormalizesToNFC(t *testing.T) {
	composed := sanitizeFilename("Camiño.pdf")    // ñ as one rune
	decomposed := sanitizeFilename("Camiño.pdf") // n + combining tilde
	if composed != decomposed {
		t.Errorf("NFC normalization: %q != %q", composed, decomposed)
	}
}

// TestSanitizeFilenameCapsBytes verifies the length cap is measured in bytes (the
// unit every filesystem enforces), keeps the extension, and never leaves a
// half-written multi-byte rune behind.
func TestSanitizeFilenameCapsBytes(t *testing.T) {
	cases := []struct{ name, in string }{
		{"ascii", strings.Repeat("a", 400) + ".pdf"},
		{"three-byte runes", strings.Repeat("漢", 300) + ".epub"},
		{"four-byte runes", strings.Repeat("𝔘", 200) + ".pdf"},
		{"no extension", strings.Repeat("漢", 300)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := sanitizeFilename(tc.in)
			if len(got) > maxFilenameBytes {
				t.Errorf("sanitizeFilename() is %d bytes, want at most %d", len(got), maxFilenameBytes)
			}
			if !utf8.ValidString(got) {
				t.Errorf("sanitizeFilename() split a rune: %q", got)
			}
			if ext := filepath.Ext(tc.in); ext != "" && !strings.HasSuffix(got, ext) {
				t.Errorf("sanitizeFilename() = %q, want the %q extension preserved", got, ext)
			}
		})
	}
}

// TestSanitizeFilenameCapIsWritable verifies the cap is not merely arithmetic:
// the capped name can actually be created on this filesystem, which a 200-rune
// (600-byte) name could not.
func TestSanitizeFilenameCapIsWritable(t *testing.T) {
	name := sanitizeFilename(strings.Repeat("漢", 300) + ".epub")
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte("x"), 0o600); err != nil {
		t.Fatalf("a capped filename must be creatable: %v", err)
	}
}

// TestNormalizeExt covers what counts as an extension: real ones are accepted,
// the numeric tail of a dotted identifier is not.
func TestNormalizeExt(t *testing.T) {
	cases := map[string]string{
		".pdf": "pdf", "EPUB": "epub", ".azw3": "azw3", ".djvu": "djvu",
		"": "", ".": "", ".0020124": "", ".x": "", ".toolong": "", ".7z": "", ".p df": "",
	}
	for in, want := range cases {
		if got := normalizeExt(in); got != want {
			t.Errorf("normalizeExt(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestExtensionPrecedence verifies the extension comes from the bytes first, the
// announced name second and the source's hint last — and never from the catalog.
func TestExtensionPrecedence(t *testing.T) {
	cases := []struct {
		name string
		req  nameRequest
		want string
	}{
		{"sniffed content wins", nameRequest{sniffed: "epub", announced: "x.pdf", ext: "mobi"}, "epub"},
		{"announced next", nameRequest{announced: "x.pdf", ext: "mobi"}, "pdf"},
		{"source hint last", nameRequest{ext: ".MOBI"}, "mobi"},
		{"a dotted identifier is not an extension", nameRequest{announced: "10.1371_journal.pmed.0020124"}, ""},
		{"the catalog extension is not a candidate", nameRequest{item: Item{Meta: &FileMeta{Ext: "epub"}}}, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.req.extension(); got != tc.want {
				t.Errorf("extension() = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestSniffExt covers each format the sniffer recognizes plus the containers it
// deliberately refuses to guess at.
func TestSniffExt(t *testing.T) {
	epub := append([]byte("PK\x03\x04"), []byte("....................mimetypeapplication/epub+zip")...)
	mobi := append(make([]byte, 60), []byte("BOOKMOBI____")...)
	djvu := append([]byte("AT&TFORM"), []byte("\x00\x00\x00\x00DJVU")...)
	cases := []struct {
		name string
		head []byte
		want string
	}{
		{"pdf", []byte("%PDF-1.7\n..."), "pdf"},
		{"epub", epub, "epub"},
		{"mobi", mobi, "mobi"},
		{"djvu", djvu, "djvu"},
		{"rtf", []byte(`{\rtf1\ansi`), "rtf"},
		{"jpeg2000", []byte("\x00\x00\x00\x0cjP  \r\n"), "jp2"},
		{"a bare zip is not guessed at", []byte("PK\x03\x04nothing here"), ""},
		{"plain text", []byte("just some words"), ""},
		{"empty", nil, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := sniffExt(tc.head); got != tc.want {
				t.Errorf("sniffExt() = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestSniffStart verifies the sniffer is consulted only for a transfer that
// starts at byte zero: a resumed body begins mid-document, where a header-shaped
// run of bytes would name the file after whatever it happened to contain.
func TestSniffStart(t *testing.T) {
	c := &Client{}
	fresh := bufio.NewReader(strings.NewReader("%PDF-1.7 a whole file"))
	if got := c.sniffStart(fresh, false); got != "pdf" {
		t.Errorf("sniffStart(fresh) = %q, want %q", got, "pdf")
	}
	resumed := bufio.NewReader(strings.NewReader("%PDF-1.7 an embedded stream, mid-file"))
	if got := c.sniffStart(resumed, true); got != "" {
		t.Errorf("sniffStart(resumed) = %q, want no guess", got)
	}
	if b, _ := fresh.Peek(5); string(b) != "%PDF-" {
		t.Error("sniffStart must not consume the bytes it looks at")
	}
}

// TestUniqueDest verifies the collision strategy: a free path is used as-is, a
// same-size file is treated as the same download arriving again, and a different
// file is never overwritten.
func TestUniqueDest(t *testing.T) {
	dir := t.TempDir()
	dest := filepath.Join(dir, "Author - Title (2020).epub")
	const digest = "abcdef0123456789abcdef0123456789"

	got, err := uniqueDest(dest, 10, digest)
	if err != nil || got != dest {
		t.Fatalf("uniqueDest() on a free path = (%q, %v), want the path itself", got, err)
	}

	if werr := os.WriteFile(dest, make([]byte, 10), 0o600); werr != nil {
		t.Fatal(werr)
	}
	if got, err = uniqueDest(dest, 10, digest); err != nil || got != dest {
		t.Errorf("uniqueDest() on a same-size file = (%q, %v), want the same path (an idempotent re-download)", got, err)
	}

	got, err = uniqueDest(dest, 20, digest)
	want := filepath.Join(dir, "Author - Title (2020) (abcdef01).epub")
	if err != nil || got != want {
		t.Errorf("uniqueDest() on a different file = (%q, %v), want %q", got, err, want)
	}

	if err = os.WriteFile(want, make([]byte, 30), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err = uniqueDest(dest, 20, digest)
	if err != nil || got != filepath.Join(dir, "Author - Title (2020) (abcdef01) (2).epub") {
		t.Errorf("uniqueDest() with the digest taken too = (%q, %v), want a counter suffix", got, err)
	}
}

// TestUniqueDestWithoutDigest covers the counter-only path (no digest available)
// and the exhaustion error.
func TestUniqueDestWithoutDigest(t *testing.T) {
	dir := t.TempDir()
	dest := filepath.Join(dir, "book.pdf")
	if err := os.WriteFile(dest, make([]byte, 5), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := uniqueDest(dest, 9, "")
	if err != nil || got != filepath.Join(dir, "book (2).pdf") {
		t.Errorf("uniqueDest() without a digest = (%q, %v), want a counter suffix", got, err)
	}
	// Fill every alternative so the exhaustion branch is reached.
	for n := 2; n < maxCollisionSuffix; n++ {
		name := filepath.Join(dir, "book ("+strconv.Itoa(n)+").pdf")
		if werr := os.WriteFile(name, make([]byte, 5), 0o600); werr != nil {
			t.Fatal(werr)
		}
	}
	if _, err = uniqueDest(dest, 9, ""); err == nil {
		t.Error("uniqueDest() should fail rather than overwrite when every alternative is taken")
	}
}

// TestUniqueDestIgnoresADirectory verifies a directory sitting at the
// destination never counts as "the same file".
func TestUniqueDestIgnoresADirectory(t *testing.T) {
	dir := t.TempDir()
	dest := filepath.Join(dir, "book.pdf")
	if err := os.Mkdir(dest, 0o750); err != nil {
		t.Fatal(err)
	}
	got, err := uniqueDest(dest, 0, "")
	if err != nil || got == dest {
		t.Errorf("uniqueDest() over a directory = (%q, %v), want a different path", got, err)
	}
}

// TestSuggestFilename verifies the resolve-only/preview namer applies the same
// verified-versus-unverified rule as the saved-file path.
func TestSuggestFilename(t *testing.T) {
	meta := &FileMeta{Author: "Jane Doe", Title: "Great Book", Year: "2020"}
	if got := SuggestFilename(Item{MD5: "abc", Meta: meta}, "", "epub", true); got != "Jane Doe - Great Book (2020).epub" {
		t.Errorf("verified suggestion = %q", got)
	}
	if got := SuggestFilename(Item{DOI: "10.1/x", Meta: meta}, "", "pdf", false); got != "10.1_x.pdf" {
		t.Errorf("unverified suggestion = %q, want the identifier rather than the requested record's title", got)
	}
	if got := SuggestFilename(Item{MD5: "abc"}, "given.pdf", "epub", true); got != "given.pdf" {
		t.Errorf("caller suggestion = %q", got)
	}
}

// TestIsPlaceholderStem covers the placeholder list, its counter suffixes, and
// the real titles that must not be mistaken for one.
func TestIsPlaceholderStem(t *testing.T) {
	for _, in := range []string{"download", "Download", "file", "fulltext", "download (1)", "download_2", "untitled"} {
		if !isPlaceholderStem(in) {
			t.Errorf("isPlaceholderStem(%q) = false, want true", in)
		}
	}
	for _, in := range []string{"Volume 2", "npre2007361-1", "Downloading the Future", "Dataism"} {
		if isPlaceholderStem(in) {
			t.Errorf("isPlaceholderStem(%q) = true, want false", in)
		}
	}
}
