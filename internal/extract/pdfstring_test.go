package extract

import (
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"
)

// TestTextString covers each encoding a title can be in and the order they
// are tried in: UTF-16BE behind its BOM, with a pair of surrogates, a lone
// one and an odd trailing byte, UTF-8 with and without its BOM, PDFDocEncoding
// for bytes that are not UTF-8, and PDFDocEncoding for ASCII or UTF-8 that
// holds one of the accent bytes below 0x20.
func TestTextString(t *testing.T) {
	for _, tc := range []struct {
		name, raw, want string
	}{
		{"empty", "", ""},
		{"ASCII", "Chapter 1", "Chapter 1"},
		{"UTF-16BE", "\xfe\xff\x00E\x00s\x00p\x00a\x00\xf1\x00o\x00l", "Español"},
		{"UTF-16BE, a surrogate pair", "\xfe\xff\x00a\xd8\x3c\xdf\x08", "a\U0001F308"},
		{"UTF-16BE, a lone surrogate", "\xfe\xff\xd8\x3c\x00a", "�a"},
		{"UTF-16BE, an odd byte at the end", "\xfe\xff\x00a\x00", "a"},
		{"UTF-16BE, nothing after the BOM", "\xfe\xff", ""},
		{"UTF-8", "Introducci\xc3\xb3n", "Introducción"},
		{"UTF-8 behind its BOM", "\xef\xbb\xbfR\xc3\xa9sum\xc3\xa9", "Résumé"},
		{"a BOM before bytes that are not UTF-8", "\xef\xbb\xbfCaf\xe9", "Café"},
		{"PDFDocEncoding", "\x80 Caf\xe9 \x84 \x8dquoted\x8e", "• Café — “quoted”"},
		{"an accent byte in ASCII", "Accent \x19 caron", "Accent ˇ caron"},
		{"an accent byte in UTF-8", "\xc3\xa9\x18", "Ã©˘"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := textString(tc.raw); got != tc.want {
				t.Errorf("textString(%q) = %q, want %q", tc.raw, got, tc.want)
			}
		})
	}
}

// TestPDFDocRune_AgreesWithTheReader checks the PDFDocEncoding table byte by
// byte against ledongthuc/pdf's own, which decodes a one-byte string the
// same way when it maps the byte and hands the byte back when it does not.
// The two tables come from the same table of the standard and were written
// apart, so a byte where they differ is a typing error in one of them. A
// control byte comes back from both as itself, and a byte the encoding leaves
// undefined above 0x7F as U+FFFD.
func TestPDFDocRune_AgreesWithTheReader(t *testing.T) {
	var items strings.Builder
	for c := range 256 {
		fmt.Fprintf(&items, `(\%03o)`, c)
	}
	strs := readerFor(t, buildPDF([]string{"<</Type/Catalog/T[" + items.String() + "]>>"})).Trailer().Key("Root").Key("T")
	for i := range 256 {
		t.Run(fmt.Sprintf("%#02x", i), func(t *testing.T) {
			v := strs.Index(i)
			raw := v.RawString()
			if len(raw) != 1 || int(raw[0]) != i {
				t.Fatalf("the fixture holds %q at %d", raw, i)
			}
			want, _ := utf8.DecodeRuneInString(v.Text())
			if got := pdfDocRune(raw[0]); got != want {
				t.Errorf("pdfDocRune(%#02x) = %U, the reader's table gives %U", i, got, want)
			}
		})
	}
}

// TestUnpadAES covers what is and is not AES padding: one byte, a whole block,
// padding after a whole block of text, and the strings that end in something
// else, which come back as they are: a length that is not a whole number of
// blocks, a last byte of zero or past the block size, and a count the bytes
// before it do not repeat.
func TestUnpadAES(t *testing.T) {
	block := "Sixteen bytes ok"
	for _, tc := range []struct {
		name, in, want string
	}{
		{"empty", "", ""},
		{"not a whole block", "abc\x01", "abc\x01"},
		{"one byte", "fifteen bytes o\x01", "fifteen bytes o"},
		{"fifteen bytes", "a" + strings.Repeat("\x0f", 15), "a"},
		{"a whole block", strings.Repeat("\x10", 16), ""},
		{"after a whole block of text", block + strings.Repeat("\x10", 16), block},
		{"a last byte that is no count", block, block},
		{"a last byte of zero", "fifteen bytes o\x00", "fifteen bytes o\x00"},
		{"a count past the block size", "fifteen bytes o" + strings.Repeat("\x11", 17), "fifteen bytes o" + strings.Repeat("\x11", 17)},
		{"a count the bytes do not repeat", "fourteen bytes\x01\x02", "fourteen bytes\x01\x02"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := unpadAES(tc.in); got != tc.want {
				t.Errorf("unpadAES(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}
