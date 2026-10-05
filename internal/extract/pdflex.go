// pdflex.go shows ledongthuc/pdf the strings its lexer refuses, which ISO
// 32000-1 defines, rewritten into strings it reads as the standard does.

package extract

import (
	"bufio"
	"bytes"
	"context"
	"encoding/hex"
	"fmt"
	"io"
	"strconv"
	"strings"
)

// The reader's lexer panics on three kinds of string the standard defines
// (7.3.4): an escape it does not know, which the standard reads as the
// character after the backslash; an octal escape past 255, whose high-order
// overflow the standard ignores; and a hex string with an odd number of
// digits, whose missing last digit the standard takes for 0. One of them in an
// object the outline walk resolves, a title or a GoToR action's Windows path
// written with single backslashes, made the whole outline read as damaged,
// where pdfcpu and qpdf read it.
//
// The reader finds an object at the offset its cross-reference table gives,
// so a string shown to it rewritten must keep the length of the one in the
// file, and each of the three can. A literal string keeps every byte but the
// backslash of an escape the reader does not know, and an octal escape past
// 255 is written as its value modulo 256 in its own three digits. A hex string
// is written as the literal string of its bytes, which takes no more bytes
// than its digits did. Whatever is left over becomes spaces after the string,
// which the reader passes over between tokens.
//
// Only the file's own bytes can be shown that way. The reader inflates an
// object stream itself, so a string inside a compressed one is still refused.

// stringFix is a string of a file as the reader is shown it: the bytes from at
// on read as with, which is as long as the string in the file.
type stringFix struct {
	at   int64
	with string
}

// stringsFixed returns d as the reader is shown it with every string its
// lexer refuses rewritten, and false when d holds none, or ctx ended before
// they were all found.
func stringsFixed(ctx context.Context, d document) (document, bool) {
	s := &lexScanner{ctx: ctx, br: bufio.NewReaderSize(io.NewSectionReader(d.r, 0, d.size), scanBuffer)}
	s.scan()
	if len(s.fixes) == 0 || ctx.Err() != nil {
		return document{}, false
	}
	return document{name: d.name, r: fixedStrings{ReaderAt: d.r, fixes: s.fixes}, size: d.size}, true
}

// lexScanner reads a file for its strings, from its first byte to its last,
// and records a fix for each one the reader refuses. It passes over comments
// and the data of every stream, which are not tokens, and stops once ctx has
// ended.
type lexScanner struct {
	ctx context.Context
	br  *bufio.Reader
	// at is the offset of the next byte.
	at int64
	// buf is the string being read.
	buf []byte
	// fixes are the fixes found so far, in the order of the file.
	fixes []stringFix
}

// next returns the next byte, and false at the end of the file and once ctx
// has ended, which it asks every scanBuffer bytes.
func (s *lexScanner) next() (byte, bool) {
	c, err := s.br.ReadByte()
	if err != nil {
		return 0, false
	}
	s.at++
	if s.at%scanBuffer == 0 && s.ctx.Err() != nil {
		return 0, false
	}
	return c, true
}

// peek returns the next byte without reading it, and at the end of the file
// 0, which is neither an octal digit nor a <, the two bytes it is asked about.
func (s *lexScanner) peek() byte {
	next, _ := s.br.Peek(1)
	if len(next) == 0 {
		return 0
	}
	return next[0]
}

// discard passes over the next n bytes.
func (s *lexScanner) discard(n int) {
	m, _ := s.br.Discard(n)
	s.at += int64(m)
}

// scan reads the file to its end, reading each comment, string and stream
// where it starts. The stream keyword follows a dictionary, so it is only
// looked for after white space or a >, which leaves the s of a name such as
// /stream, and of a word such as endstream, as it is.
func (s *lexScanner) scan() {
	prev := byte('\n')
	for {
		c, ok := s.next()
		if !ok {
			return
		}
		switch c {
		case '%':
			s.comment()
		case '(':
			s.literal(s.at - 1)
		case '<':
			s.angle(s.at - 1)
		case 's':
			if prev == '>' || strings.IndexByte(pdfWhiteSpace, prev) >= 0 {
				s.streamKeyword()
			}
		}
		prev = c
	}
}

// pdfWhiteSpace is the white space of ISO 32000-1 (7.2.2).
const pdfWhiteSpace = "\x00\t\n\f\r "

// comment passes over a comment, to the end of its line.
func (s *lexScanner) comment() {
	for {
		c, ok := s.next()
		if !ok || c == '\r' || c == '\n' {
			return
		}
	}
}

// literal reads the literal string whose ( is at start, and records a fix for
// it when it holds an escape the reader refuses.
func (s *lexScanner) literal(start int64) {
	out := s.buf[:0]
	out = append(out, '(')
	refused := false
	for depth := 1; depth > 0; {
		c, ok := s.next()
		if !ok {
			return
		}
		if c == '\\' {
			var rewritten bool
			out, rewritten = s.escape(out)
			refused = refused || rewritten
			continue
		}
		depth += nesting(c)
		out = append(out, c)
	}
	s.buf = out
	if refused {
		s.fix(start, out)
	}
}

// nesting returns how much c changes the depth of parentheses in a literal
// string, which a balanced pair may hold unescaped.
func nesting(c byte) int {
	switch c {
	case '(':
		return 1
	case ')':
		return -1
	}
	return 0
}

// escape reads the escape after a backslash in a literal string and appends to
// out what the reader is to be shown for it: the escape as written when the
// reader takes it, the character after the backslash when the reader does not
// know the escape, and the value modulo 256 when it is an octal escape past
// 255. It reports whether it rewrote the escape.
func (s *lexScanner) escape(out []byte) ([]byte, bool) {
	c, ok := s.next()
	if !ok {
		return out, false
	}
	if c >= '0' && c <= '7' {
		return s.octal(out, c)
	}
	if strings.IndexByte("nrtbf()\\\r\n", c) >= 0 {
		return append(out, '\\', c), false
	}
	return append(out, c), true
}

// octal reads an octal escape whose first digit is first, and appends it as
// escape does. It has one to three digits, so only a three-digit one can pass
// 255, and that one is written in three digits again.
func (s *lexScanner) octal(out []byte, first byte) ([]byte, bool) {
	digits := []byte{first}
	for len(digits) < 3 {
		c := s.peek()
		if c < '0' || c > '7' {
			break
		}
		s.discard(1)
		digits = append(digits, c)
	}
	v, _ := strconv.ParseUint(string(digits), 8, 16)
	if v <= 0o377 {
		return append(append(out, '\\'), digits...), false
	}
	return fmt.Appendf(out, "\\%03o", v%0o400), true
}

// angle reads what the < at start opens: the << of a dictionary, which it
// passes over, or a hex string, which it records a fix for when its digits are
// odd in number. One holding anything but hex digits and white space is not a
// string the standard defines, and is left as it is.
func (s *lexScanner) angle(start int64) {
	if s.peek() == '<' {
		s.discard(1)
		return
	}
	digits := s.buf[:0]
	c, ok := s.next()
	for ok && c != '>' {
		if strings.IndexByte("0123456789abcdefABCDEF", c) >= 0 {
			digits = append(digits, c)
		} else if strings.IndexByte(pdfWhiteSpace, c) < 0 {
			return
		}
		c, ok = s.next()
	}
	s.buf = digits
	if ok {
		s.oddHex(start, digits)
	}
}

// oddHex records a fix for the hex string at start, whose digits are digits,
// when they are odd in number: the literal string of its bytes, the last one
// completed with a 0 digit. Each byte takes a character there, or two for the
// three a literal string escapes, and a byte whose low digit is 0 is none of
// them.
func (s *lexScanner) oddHex(start int64, digits []byte) {
	if len(digits)%2 == 0 {
		return
	}
	raw, _ := hex.DecodeString(string(digits) + "0")
	out := []byte{'('}
	for _, b := range raw {
		if strings.IndexByte("()\\", b) >= 0 {
			out = append(out, '\\')
		}
		out = append(out, b)
	}
	s.fix(start, append(out, ')'))
}

// fix records with for the string from start to the byte last read, padded
// with spaces to its length.
func (s *lexScanner) fix(start int64, with []byte) {
	s.fixes = append(s.fixes, stringFix{at: start, with: string(with) + strings.Repeat(" ", int(s.at-start)-len(with))})
}

// streamKeyword reads on after an s that can start the stream keyword, and
// when it does, passes over the stream's data to its endstream keyword, as
// objStmView finds the end of an object stream's.
func (s *lexScanner) streamKeyword() {
	const rest = "tream"
	next, _ := s.br.Peek(len(rest) + 1)
	if !bytes.HasPrefix(next, []byte(rest)) || len(next) == len(rest) || isRegularByte(next[len(rest)]) {
		return
	}
	s.discard(len(rest))
	for s.ctx.Err() == nil {
		data, err := s.br.Peek(scanBuffer)
		if before, _, found := bytes.Cut(data, []byte(endStreamKeyword)); found {
			s.discard(len(before) + len(endStreamKeyword))
			return
		}
		if err != nil {
			s.discard(len(data))
			return
		}
		// The keyword's length is kept, so one across two reads is found.
		s.discard(len(data) - len(endStreamKeyword))
	}
}

// fixedStrings is a file as the reader is shown it with its fixes.
type fixedStrings struct {
	io.ReaderAt
	fixes []stringFix
}

// ReadAt reads p from the file at off, and then writes each fix over whatever
// part of p it covers.
func (v fixedStrings) ReadAt(p []byte, off int64) (int, error) {
	n, err := v.ReaderAt.ReadAt(p, off)
	for _, f := range v.fixes {
		overwrite(p[:n], off, f.at, f.with)
	}
	return n, err
}
