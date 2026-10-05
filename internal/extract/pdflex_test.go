package extract

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"testing"
)

// shownFixed returns data as stringsFixed shows it to the reader, read whole,
// and false when it fixes nothing.
func shownFixed(t *testing.T, ctx context.Context, data string) (string, bool) {
	t.Helper()
	fixed, ok := stringsFixed(ctx, docOf([]byte(data)))
	if !ok {
		return "", false
	}
	if fixed.size != int64(len(data)) {
		t.Fatalf("the fixed file is %d bytes, want %d", fixed.size, len(data))
	}
	return viewBytes(t, fixed.r, fixed.size), true
}

// TestStringsFixed_Strings rewrites each string the reader's lexer refuses
// into one it reads as ISO 32000-1 does, at the same length, and leaves every
// string it takes as it is: an escape the reader does not know loses its
// backslash, an octal escape past 255 becomes its value modulo 256, and a hex
// string with an odd number of digits becomes the literal string of its
// bytes. Whatever is left over is spaces after the string.
func TestStringsFixed_Strings(t *testing.T) {
	for _, tc := range []struct {
		name, in, want string
	}{
		{"an escape the reader does not know", `1 0 obj (Results \& discussion) endobj`, `1 0 obj (Results & discussion)  endobj`},
		{"two of them", `(\a\b\c)`, `(a\bc)  `},
		{"the digit after 7", `(\8)`, `(8) `},
		{"the byte before 0", `(\/)`, `(/) `},
		{"a Windows path", `/F (..\docs\annex.pdf)`, `/F (..docsannex.pdf)  `},
		{"every escape the reader takes", "(\\n\\r\\t\\b\\f\\(\\)\\\\\\0\\7\\377\\1234 x\\\r\ny\\\nz\\\r)", ""},
		{"an octal escape past 255", `(Caf\4351 menu)`, `(Caf\0351 menu)`},
		{"the first past 255", `(\400)`, `(\000)`},
		{"the last of three digits", `(\777)`, `(\377)`},
		{"one digit then an 8", `(\48\&)`, `(\48&) `},
		{"two digits at the end of the file", `(\43`, ""},
		{"parentheses in balanced pairs", `(a (b \& c) d) (e)`, `(a (b & c) d)  (e)`},
		{"a string never closed", `(abc\&`, ""},
		{"a backslash at the end of the file", `(abc\`, ""},
		{"an odd hex string", `<526573756C747320413>`, `(Results A0)         `},
		{"an odd hex string with white space", "<4 1\x004>", "(A@)   "},
		{"an odd hex string of a 0", `<0>`, "(\x00)"},
		{"an odd hex string of bytes a literal string escapes", `<28295C4>`, `(\(\)\\@)`},
		{"an even hex string", `<4142>`, ""},
		{"an empty hex string", `<>`, ""},
		{"a hex string holding a letter past F", `<41G> (\&)`, `<41G> (&) `},
		{"a hex string never closed", `<414`, ""},
		{"a dictionary", `<</T (\&)>>`, `<</T (&) >>`},
		{"a comment", "% (\\&)\n(\\&)", "% (\\&)\n(&) "},
		{"a comment ending in a carriage return", "%(\\&)\r(\\&)", "%(\\&)\r(&) "},
		{"a comment at the end of the file", "(\\&) % (\\&", "(&)  % (\\&"},
		{"a stream", "<</Length 9>>stream\n(x\\&y) Tj\nendstream (\\&)", "<</Length 9>>stream\n(x\\&y) Tj\nendstream (&) "},
		{"a stream after white space", "<<>> stream\r\n(\\&)\nendstream (\\&)", "<<>> stream\r\n(\\&)\nendstream (&) "},
		{"a stream at the start of the file", "stream\n(\\&)\nendstream (\\&)", "stream\n(\\&)\nendstream (&) "},
		{"a stream after a NUL", "\x00stream\n(\\&)\nendstream (\\&)", "\x00stream\n(\\&)\nendstream (&) "},
		{"a < at the end of the file", "(\\&) <", "(&)  <"},
		{"a stream with no endstream", ">>stream\n(\\&)", ""},
		{"a name that spells stream", "/stream (\\&)\nendstream", "/stream (&) \nendstream"},
		{"a word that ends in stream", "xstream\n(\\&) endstream", "xstream\n(&)  endstream"},
		{"a word that starts with stream", ">>streamer (\\&) endstream", ">>streamer (&)  endstream"},
		{"stream at the end of the file", "(\\&) >>stream", "(&)  >>stream"},
		{"part of stream at the end of the file", "(\\&) >>strea", "(&)  >>strea"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := shownFixed(t, context.Background(), tc.in)
			if ok != (tc.want != "") || got != tc.want {
				t.Errorf("shown %q (%v), want %q", got, ok, tc.want)
			}
		})
	}
}

// TestStringsFixed_LongStreams passes over the data of a stream longer than
// one read of it, to its endstream keyword, and finds that keyword when it
// lies across two reads. A refused string inside the data is not a string,
// and the one after the stream is fixed.
func TestStringsFixed_LongStreams(t *testing.T) {
	inside := strings.Repeat("a", scanBuffer+100) + `(\&)` + strings.Repeat("a", 100)
	// The data starts at the line feed after the keyword, so the endstream
	// keyword starts four bytes before the end of the first read.
	across := strings.Repeat("a", scanBuffer-5)
	for _, tc := range []struct {
		name, data string
	}{
		{"a refused string past the first read", inside},
		{"endstream across two reads", across},
	} {
		t.Run(tc.name, func(t *testing.T) {
			in := ">>stream\n" + tc.data + "endstream (\\&)"
			want := ">>stream\n" + tc.data + "endstream (&) "
			if got, ok := shownFixed(t, context.Background(), in); !ok || got != want {
				t.Errorf("shown %d bytes ending %q (%v), want them to end %q", len(got), got[max(0, len(got)-20):], ok, want[len(want)-20:])
			}
		})
	}
}

// scanned scans data under ctx and returns the offsets of the fixes found.
func scanned(ctx context.Context, data string) []int64 {
	s := &lexScanner{ctx: ctx, br: bufio.NewReaderSize(strings.NewReader(data), scanBuffer)}
	s.scan()
	var at []int64
	for _, f := range s.fixes {
		at = append(at, f.at)
	}
	return at
}

// TestLexScanner_ContextEnds asks the context every scanBuffer bytes, and
// stops at the first ask after it has ended: a scan whose context ends after
// its first ask finds the refused strings before the second and none after.
// A context that has ended stops a stream's data being passed over, and
// stringsFixed then fixes nothing.
func TestLexScanner_ContextEnds(t *testing.T) {
	refused := `(\&)`
	data := refused + strings.Repeat(" ", 100_000) + refused + strings.Repeat(" ", 40_000) + refused
	if got := fmt.Sprint(scanned(passErr(1), data)); got != "[0 100004]" {
		t.Errorf("fixes at %s, want [0 100004]", got)
	}
	stream := ">>stream\n" + refused + "\nendstream " + refused
	if got := fmt.Sprint(scanned(passErr(0), stream)); got != "[9 24]" {
		t.Errorf("fixes at %s, want both strings once the stream is not passed over", got)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, ok := stringsFixed(ctx, docOf([]byte(data))); ok {
		t.Error("stringsFixed fixed a file under a context that has ended")
	}
}

// TestFixedStrings_ReadAt reads a file with two fixes in pieces of every size
// from one byte to the whole, so each read starts and ends inside a fix,
// before one and after one, and every piece reads as the fixed file does.
func TestFixedStrings_ReadAt(t *testing.T) {
	const file = "ab(\\&)cd(\\&)ef"
	const want = "ab(&) cd(&) ef"
	view := fixedStrings{ReaderAt: strings.NewReader(file), fixes: []stringFix{{at: 2, with: "(&) "}, {at: 8, with: "(&) "}}}
	for size := 1; size <= len(file); size++ {
		t.Run(strconv.Itoa(size), func(t *testing.T) {
			var got bytes.Buffer
			p := make([]byte, size)
			for off := int64(0); off < int64(len(file)); off += int64(size) {
				n, err := view.ReadAt(p, off)
				if err != nil && !errors.Is(err, io.EOF) {
					t.Fatalf("ReadAt(%d) = %v", off, err)
				}
				got.Write(p[:n])
			}
			if got.String() != want {
				t.Errorf("read %q, want %q", got.String(), want)
			}
		})
	}
}

// TestOutline_PDFStringsTheLexerRefuses reads an outline whose items hold a
// string ISO 32000-1 defines and ledongthuc/pdf's lexer refuses: a title with
// an escape it does not know, one with an octal escape past 255, one written
// as a hex string with an odd number of digits, and a GoToR action whose file
// is a Windows path written with single backslashes. The lexer's panic made
// the whole outline damaged; every entry is listed now, each title as the
// standard reads it.
func TestOutline_PDFStringsTheLexerRefuses(t *testing.T) {
	for _, tc := range []struct {
		name, item, want string
	}{
		{"an escape it does not know", `<</Title(Results \& discussion)/Dest[4 0 R/Fit]/Next 10 0 R>>`, "0 2 Results & discussion\n"},
		{"an octal escape past 255", `<</Title(Caf\4351 menu)/Dest[4 0 R/Fit]/Next 10 0 R>>`, "0 2 Caf˛1 menu\n"},
		{"an odd hex string", `<</Title<526573756C747320413>/Dest[4 0 R/Fit]/Next 10 0 R>>`, "0 2 Results A0\n"},
		{"a Windows path in a GoToR action", `<</Title(Annex)/A<</S/GoToR/F(..\docs\annex.pdf)/D[0/Fit]>>/Next 10 0 R>>`, "0 0 Annex\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			res := outlineOf(t, outlinePDF("", tc.item, "<</Title(Last)/Dest[5 0 R/Fit]>>"))
			if got := entryLines(res.Entries); got != tc.want+"0 3 Last\n" || res.Reason != "" {
				t.Errorf("outline:\n%s(%q)\nwant:\n%s0 3 Last", got, res.Reason, tc.want)
			}
		})
	}
}
