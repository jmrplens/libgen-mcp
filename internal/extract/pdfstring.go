// pdfstring.go turns the bytes of a PDF string into the text they hold.

package extract

import (
	"strings"
	"unicode/utf16"
	"unicode/utf8"
)

// The marks a PDF text string can open with to name its encoding.
const (
	// utf16BOM opens a string in UTF-16BE, the encoding PDF 1.x defines beside
	// PDFDocEncoding.
	utf16BOM = "\xfe\xff"
	// utf8BOM opens a string in UTF-8, which PDF 2.0 adds.
	utf8BOM = "\xef\xbb\xbf"
	// pdfDocAccentBytes are the bytes at which PDFDocEncoding has accents where
	// ASCII and UTF-8 have controls.
	pdfDocAccentBytes = "\x18\x19\x1a\x1b\x1c\x1d\x1e\x1f"
)

// textString decodes the bytes of a PDF text string, which ledongthuc/pdf
// hands over as they are in the file. Its own decoding takes any string whose
// bytes all map in PDFDocEncoding as PDFDocEncoding, and UTF-8 bytes always
// do, so a title a producer wrote in UTF-8, as Ghostscript passes a UTF-8
// pdfmark through, came back as mojibake, and a UTF-8 BOM came back as three
// letters. This decodes as pdfcpu did: UTF-16BE behind its BOM, then UTF-8
// with or without the BOM PDF 2.0 defines, unless a byte is one of the eight
// accents PDFDocEncoding puts below 0x20, and otherwise PDFDocEncoding.
func textString(raw string) string {
	if body, ok := strings.CutPrefix(raw, utf16BOM); ok {
		return utf16BE(body)
	}
	raw = strings.TrimPrefix(raw, utf8BOM)
	if utf8.ValidString(raw) && !strings.ContainsAny(raw, pdfDocAccentBytes) {
		return raw
	}
	var b strings.Builder
	for i := range len(raw) {
		b.WriteRune(pdfDocRune(raw[i]))
	}
	return b.String()
}

// utf16BE decodes big-endian UTF-16 code units, a lone surrogate becoming
// U+FFFD. A trailing odd byte is half a unit and is dropped.
func utf16BE(s string) string {
	units := make([]uint16, len(s)/2)
	for i := range units {
		units[i] = uint16(s[2*i])<<8 | uint16(s[2*i+1])
	}
	return string(utf16.Decode(units))
}

// pdfDocAccents are the characters PDFDocEncoding puts at 0x18 to 0x1F: breve,
// caron, circumflex, dot above, double acute, ogonek, ring and small tilde
// (ISO 32000-1, Table D.2).
var pdfDocAccents = [len(pdfDocAccentBytes)]rune{
	0x02d8, 0x02c7, 0x02c6, 0x02d9, 0x02dd, 0x02db, 0x02da, 0x02dc,
}

// pdfDocHigh are the 33 characters PDFDocEncoding puts at 0x80 to 0xA0, where
// Latin-1 has C1 controls and a no-break space: punctuation, ligatures, the
// letters Latin-1 lacks and the euro sign. 0x9F is undefined. The length is
// written as a number because an expression in a declaration is a mutant no
// test can reach, for the reason noPDFOutlineReason gives.
var pdfDocHigh = [33]rune{
	0x2022, 0x2020, 0x2021, 0x2026, 0x2014, 0x2013, 0x0192, 0x2044,
	0x2039, 0x203a, 0x2212, 0x2030, 0x201e, 0x201c, 0x201d, 0x2018,
	0x2019, 0x201a, 0x2122, 0xfb01, 0xfb02, 0x0141, 0x0152, 0x0160,
	0x0178, 0x017d, 0x0131, 0x0142, 0x0153, 0x0161, 0x017e, utf8.RuneError,
	0x20ac,
}

// pdfDocRune is the character PDFDocEncoding gives the byte c. Every byte the
// table above does not cover is the Latin-1 character of the same number,
// except 0xAD, which the encoding leaves undefined. A control byte stays a
// control, for the caller to deal with as it deals with any other.
func pdfDocRune(c byte) rune {
	if c >= 0x18 && c <= 0x1f {
		return pdfDocAccents[int(c)-0x18]
	}
	if c >= 0x80 && c <= 0xa0 {
		return pdfDocHigh[int(c)-0x80]
	}
	if c == 0xad {
		return utf8.RuneError
	}
	return rune(c)
}

// aesBlockSize is the AES block size, which a string encrypted with AES is
// padded to a multiple of.
const aesBlockSize = 16

// unpadAES removes the PKCS#5 padding from a string decrypted with AES-CBC.
// ledongthuc/pdf decrypts a string and leaves its padding on: one to sixteen
// bytes, each holding the count. On an ASCII title it was removed as control
// characters, but in UTF-16 it decoded as letters (a full block, as
// "တ" eight times), and in PDFDocEncoding a pad byte the encoding does
// not define turned every byte above 0x7F into U+FFFD. A string whose length
// is not a whole number of blocks, or whose end is not a valid padding, was not
// decrypted that way, as a string inside an object stream is not, and is
// returned as it is.
func unpadAES(s string) string {
	if unpadded, ok := pkcs5Unpad(s); ok {
		return unpadded
	}
	return s
}

// pkcs5Unpad removes the PKCS#5 padding from s, a whole number of AES blocks
// decrypted, and reports whether s ended in a valid padding: one to sixteen
// bytes, each holding their count. A last byte of zero counts no padding,
// which no encryption writes.
func pkcs5Unpad(s string) (string, bool) {
	if s == "" || len(s)%aesBlockSize != 0 {
		return "", false
	}
	pad := s[len(s)-1]
	if pad == 0 || pad > aesBlockSize || !strings.HasSuffix(s, strings.Repeat(s[len(s)-1:], int(pad))) {
		return "", false
	}
	return s[:len(s)-int(pad)], true
}
