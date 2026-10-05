// pdfobjstm.go notices the object streams ledongthuc/pdf inflates, so the
// outline walk can tell the objects it takes out of one, and shows the reader
// their data decrypted when the file's encryption is hidden from it.

package extract

import (
	"bytes"
	"cmp"
	"io"
	"regexp"
	"slices"
)

// An object stream (ISO 32000-1, 7.5.7) holds other objects compressed
// together, and in an encrypted file it is encrypted whole: the strings of the
// objects in it are not encrypted again. The reader keeps no object number on
// a value, so which objects came out of one cannot be asked of the value. It
// can be seen in the file the reader reads, though: to resolve an object in an
// object stream, the reader reads the stream's dictionary and then inflates
// its data, reading from the byte after the stream keyword's end of line, and
// it reads nothing there for an object that stands on its own.
//
// objStmView is the file as the reader reads it for the outline walk. At a
// read that starts where the bytes before it end a stream's dictionary and
// stream keyword, it looks for the object's header before them and /Type
// /ObjStm in its dictionary, and it counts every read that starts an object
// stream's data. A walk that sees the count move while it resolves a reference
// knows the object came out of an object stream. With crypt set, which is how
// selfDecrypting opens a file whose encryption it hid from the reader, the
// view also shows the reader each object stream's data decrypted, without
// which the reader could inflate none of them, so no object in one could be
// read. The reader inflates a stream from its start, each read beginning
// where the one before it ended, so a read is in the stream being inflated
// when it begins where the last read of it ended.
type objStmView struct {
	io.ReaderAt
	// crypt decrypts the data of an object stream, or is nil for a file the
	// reader decrypts itself or that is not encrypted.
	crypt *stringCrypt
	// streams are the object streams noticed so far, by where their data
	// starts.
	streams []objStream
	// inflating is the object stream the reader is inflating, and next where
	// its next read of it begins.
	inflating objStream
	next      int64
	// shown is the object stream whose data plain holds decrypted.
	shown objStream
	plain []byte
	// inflations counts the reads that started an object stream's data.
	inflations int
}

// objStream is an object stream's data in the file: from start, the byte after
// its stream keyword's end of line, to end, where its endstream keyword is.
// in is the object the stream is, whose key encrypts it.
type objStream struct {
	start, end int64
	in         objRef
}

// Bounds on noticing an object stream. objStmHead is how far before its data
// the view looks for the stream's object header and dictionary, which for an
// object stream is a few dozen bytes. maxObjStmBytes is the largest data the
// view decrypts, far past the size producers write, which is about a hundred
// objects to a stream: one larger, like one whose endstream keyword the view
// does not find, is left as the file holds it and does not inflate.
const (
	objStmHead     = 4 << 10
	maxObjStmBytes = 64 << 20
)

// endStreamKeyword ends a stream's data.
const endStreamKeyword = "endstream"

var (
	// streamDataRE matches the end of a stream's dictionary, its stream
	// keyword and the end of line the reader takes after it, at the end of
	// the bytes before the stream's data.
	streamDataRE = regexp.MustCompile(`>>[\x00\t\n\f\r ]*stream(?:\r\n|\n|\r)$`)
	// objHeaderRE matches an indirect object's header.
	objHeaderRE = regexp.MustCompile(`(?:^|[\x00\t\n\f\r ])(\d+)[\x00\t\n\f\r ]+(\d+)[\x00\t\n\f\r ]+obj\b`)
	// objStmTypeRE matches the type of an object stream's dictionary.
	objStmTypeRE = regexp.MustCompile(`/Type[\x00\t\n\f\r ]*/ObjStm(?:[\x00\t\n\f\r ()<>\[\]{}/%]|$)`)
)

// over returns what the reader is given for f: the view of f, or f itself on
// no view.
func (v *objStmView) over(f io.ReaderAt) io.ReaderAt {
	if v == nil {
		return f
	}
	v.ReaderAt = f
	return v
}

// inflated returns how many reads have started an object stream's data, and
// 0 on no view.
func (v *objStmView) inflated() int {
	if v == nil {
		return 0
	}
	return v.inflations
}

// ReadAt reads p from the file at off, counting a read that starts an object
// stream's data, and, with crypt set, writing over what p holds of the data of
// the stream being inflated with that data decrypted. A read that neither
// starts a stream's data nor goes on with the one being inflated is the file
// as it is.
func (v *objStmView) ReadAt(p []byte, off int64) (int, error) {
	n, err := v.ReaderAt.ReadAt(p, off)
	if off != v.next {
		s, ok := v.streamAt(off, p[:n])
		if !ok {
			return n, err
		}
		v.inflations++
		v.inflating = s
	}
	v.next = off + int64(n)
	if v.crypt != nil {
		plain := v.decrypted(v.inflating)
		copy(p[:n], plain[min(off-v.inflating.start, int64(len(plain))):])
	}
	return n, err
}

// streamAt returns the object stream whose data starts at off, noticing one
// there, where read, what the read at off returned, begins.
func (v *objStmView) streamAt(off int64, read []byte) (objStream, bool) {
	i, found := slices.BinarySearchFunc(v.streams, off, func(s objStream, off int64) int {
		return cmp.Compare(s.start, off)
	})
	if found {
		return v.streams[i], true
	}
	s, ok := v.notice(off, read)
	if ok {
		v.streams = slices.Insert(v.streams, i, s)
	}
	return s, ok
}

// notice returns the object stream whose data starts at off, which read
// begins: the bytes before off end a dictionary with /Type /ObjStm, its stream
// keyword and an end of line, after an object header, and an endstream keyword
// follows within maxObjStmBytes. A stream keyword that ends with a carriage
// return alone is followed by its data unless read begins with a line feed,
// which the reader takes as part of the end of line.
func (v *objStmView) notice(off int64, read []byte) (objStream, bool) {
	head := make([]byte, min(off, objStmHead))
	if n, _ := v.ReaderAt.ReadAt(head, off-int64(len(head))); n != len(head) {
		return objStream{}, false
	}
	keyword := streamDataRE.FindIndex(head)
	if keyword == nil || head[len(head)-1] == '\r' && len(read) > 0 && read[0] == '\n' {
		return objStream{}, false
	}
	headers := objHeaderRE.FindAllSubmatchIndex(head[:keyword[0]], -1)
	if len(headers) == 0 {
		return objStream{}, false
	}
	h := headers[len(headers)-1]
	in, ok := headerRef(string(head[h[2]:h[3]]), string(head[h[4]:h[5]]))
	if dict := head[h[1]:keyword[0]]; !ok || !objStmTypeRE.Match(dict) || bytes.Contains(dict, []byte("endobj")) {
		return objStream{}, false
	}
	end, ok := v.endOfData(off)
	return objStream{start: off, end: end, in: in}, ok
}

// headerRef reads an object header's number and generation as a reference.
func headerRef(id, gen string) (objRef, bool) {
	ref, n := leadingRef(id + " " + gen + " R")
	return ref, n > 0
}

// endOfData returns where the endstream keyword after off is, in the
// maxObjStmBytes from off.
func (v *objStmView) endOfData(off int64) (int64, bool) {
	r := io.NewSectionReader(v.ReaderAt, off, maxObjStmBytes)
	buf := make([]byte, scanBuffer)
	kept := 0
	for from := off; ; {
		n, err := io.ReadFull(r, buf[kept:])
		if at := bytes.Index(buf[:kept+n], []byte(endStreamKeyword)); at >= 0 {
			return from + int64(at), true
		}
		if err != nil {
			return 0, false
		}
		// The last bytes are kept, so a keyword across two reads is found.
		kept = len(endStreamKeyword)
		from += int64(len(buf) - kept)
		copy(buf, buf[len(buf)-kept:])
	}
}

// decrypted returns the data of s decrypted, decrypting it on first use and
// keeping the stream read last: the reader inflates a stream from its start
// for every object it takes out of it.
func (v *objStmView) decrypted(s objStream) []byte {
	if v.shown != s {
		raw := make([]byte, s.end-s.start)
		n, _ := v.ReaderAt.ReadAt(raw, s.start)
		v.shown, v.plain = s, v.crypt.decryptStream(raw[:n], s.in)
	}
	return v.plain
}
