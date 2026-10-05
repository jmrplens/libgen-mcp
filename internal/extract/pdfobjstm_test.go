package extract

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"fmt"
	"strings"
	"testing"
)

// objStmFile returns a file holding one object, object 12, whose dictionary
// is dict, written between before and its stream keyword, then eol and data,
// with something after it, and where the data starts.
func objStmFile(before, dict, eol, data string) ([]byte, int64) {
	head := before + "12 0 obj\n<<" + dict + ">>stream" + eol
	return []byte(head + data + "\nendstream\nendobj\n13 0 obj\n(after)\nendobj\n"), int64(len(head))
}

// TestObjStmView_Notice reads at the start of a stream's data in files built
// for each edge, and must notice an object stream exactly when the bytes
// before the read end an object stream's dictionary and stream keyword with
// an end of line the reader takes, after the object's header, and an
// endstream keyword follows. A carriage return alone with nothing read after
// it yet ends the line.
func TestObjStmView_Notice(t *testing.T) {
	const objStm = "/Type/ObjStm/N 1/First 4/Length 8"
	for _, tc := range []struct {
		name         string
		before, dict string
		eol, data    string
		nothingRead  bool
		ok           bool
	}{
		{"CR LF", "%PDF-1.7\n", objStm, "\r\n", "DATADATA", false, true},
		{"LF", "%PDF-1.7\n", objStm, "\n", "DATADATA", false, true},
		{"CR alone", "%PDF-1.7\n", objStm, "\r", "DATADATA", false, true},
		{"CR alone, nothing read", "%PDF-1.7\n", objStm, "\r", "DATADATA", true, true},
		{"LF, and data that starts with a line feed", "%PDF-1.7\n", objStm, "\n", "\nDATADATA", false, true},
		{"CR then a line feed the reader takes", "%PDF-1.7\n", objStm, "\r", "\nDATADATA", false, false},
		{"spaced type", "%PDF-1.7\n", "/Type /ObjStm /N 1", "\n", "DATA", false, true},
		{"at the start of the file", "", objStm, "\n", "DATA", false, true},
		{"after another object", "%PDF-1.7\n11 0 obj\nnull\nendobj\n", objStm, "\n", "DATA", false, true},
		{"another type", "%PDF-1.7\n", "/Type/XRef/W[1 2 1]", "\n", "DATA", false, false},
		{"a longer type name", "%PDF-1.7\n", "/Type/ObjStmX", "\n", "DATA", false, false},
		{"a space before the end of line", "%PDF-1.7\n", objStm, " \n", "DATA", false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			data, start := objStmFile(tc.before, tc.dict, tc.eol, tc.data)
			v := &objStmView{ReaderAt: bytes.NewReader(data)}
			read := data[start:]
			if tc.nothingRead {
				read = nil
			}
			s, ok := v.notice(start, read)
			// The data runs to the line feed before endstream.
			want := objStream{start: start, end: start + int64(len(tc.data)) + 1, in: objRef{id: 12}}
			if ok != tc.ok || ok && s != want {
				t.Errorf("notice = %+v, %v, want %+v, %v", s, ok, want, tc.ok)
			}
		})
	}
}

// TestObjStmView_NoticeAHeaderNotItsOwn reads data with no object header
// before it, data whose dictionary holds endobj, which is not the dictionary
// of the header before it, and data whose object number or generation is past
// a reference's range. None is an object stream.
func TestObjStmView_NoticeAHeaderNotItsOwn(t *testing.T) {
	for _, tc := range []struct{ name, text string }{
		{"no header", "<</Type/ObjStm>>stream\n"},
		{"endobj in between", "12 0 obj\nnull\nendobj\n<</Type/ObjStm>>stream\n"},
		{"a number too large", "99999999999 0 obj\n<</Type/ObjStm>>stream\n"},
		{"a generation too large", "12 70000 obj\n<</Type/ObjStm>>stream\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			data := []byte(tc.text + "DATA\nendstream\n")
			v := &objStmView{ReaderAt: bytes.NewReader(data)}
			if s, ok := v.notice(int64(len(tc.text)), data[len(tc.text):]); ok {
				t.Errorf("notice = %+v, want nothing", s)
			}
		})
	}
}

// TestObjStmView_NoticePastTheEnd reads past the end of a file, where the
// bytes before the read cannot all be read, and notices nothing.
func TestObjStmView_NoticePastTheEnd(t *testing.T) {
	data, start := objStmFile("%PDF-1.7\n", "/Type/ObjStm", "\n", "DATA")
	v := &objStmView{ReaderAt: bytes.NewReader(data[:start])}
	if s, ok := v.notice(start+objStmHead, nil); ok {
		t.Errorf("notice = %+v, want nothing", s)
	}
}

// TestObjStmView_EndOfData finds the endstream keyword at the start of the
// data, across the boundary between two of the scan's reads, at the end of the
// file, and not past the end of a file that has none.
func TestObjStmView_EndOfData(t *testing.T) {
	across := scanBuffer - 4
	for _, tc := range []struct {
		name string
		data string
		at   int64
		ok   bool
	}{
		{"at once", endStreamKeyword, 0, true},
		{"across two reads", strings.Repeat("x", across) + endStreamKeyword, int64(across), true},
		{"after a full read", strings.Repeat("x", 3*scanBuffer) + endStreamKeyword + "\n", int64(3 * scanBuffer), true},
		{"none", strings.Repeat("x", scanBuffer+3), 0, false},
		{"cut short", strings.Repeat("x", 10) + endStreamKeyword[:5], 0, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			v := &objStmView{ReaderAt: strings.NewReader("pre" + tc.data)}
			at, ok := v.endOfData(3)
			if ok != tc.ok || ok && at != 3+tc.at {
				t.Errorf("endOfData = %d, %v, want %d, %v", at, ok, 3+tc.at, tc.ok)
			}
		})
	}
}

// TestObjStmView_ReadAt reads a file through the view the way the reader
// inflates an object stream, from the data's start on: a read at the data's
// start counts an inflation and a read further in does not, a read before the
// data that runs into it is left as the file holds it, and so is a read past
// the data. With a key, the data reads decrypted up to the endstream keyword;
// without one, as the file holds it.
func TestObjStmView_ReadAt(t *testing.T) {
	plain := "0 0 (object stream data)"
	c := stringCrypt{key: []byte{1, 2, 3, 4, 5}, streams: true}
	key := c.objectKey(objRef{id: 12})
	enc := string(rc4XOR(key, []byte(plain)))
	// The view decrypts up to endstream, the line feed before it included.
	extent := string(rc4XOR(key, []byte(enc+"\n")))
	data, start := objStmFile("%PDF-1.7\n", "/Type/ObjStm/N 1/First 4", "\n", enc)
	for _, tc := range []struct {
		name   string
		crypt  *stringCrypt
		first  int64
		off, n int64
		want   string
		counts int
	}{
		{"the data's start again, decrypted", &c, 1, start, 8, plain[:8], 1},
		{"going on inside the data, decrypted", &c, 4, start + 4, 6, plain[4:10], 0},
		{"going on into the end of line after the data", &c, int64(len(plain)) - 2, start + int64(len(plain)) - 2, 4, extent[len(plain)-2:] + "e", 0},
		{"inside the data, not going on from the last read", &c, 4, start + 6, 4, enc[6:10], 0},
		{"from before the data", &c, 1, start - 4, 8, string(data[start-4 : start+4]), 0},
		{"past the data", &c, 1, int64(len(data)) - 7, 7, "endobj\n", 0},
		{"the data's start again, counted only", nil, 1, start, 8, enc[:8], 1},
		{"going on inside the data, counted only", nil, 4, start + 4, 6, enc[4:10], 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// The reader reads the data from its start, which is where the
			// view notices the stream, before it reads on.
			v := &objStmView{ReaderAt: bytes.NewReader(data), crypt: tc.crypt}
			first := make([]byte, tc.first)
			if _, err := v.ReadAt(first, start); err != nil || tc.crypt != nil && string(first) != plain[:tc.first] {
				t.Fatalf("the first read = %q (%v), want %q", first, err, plain[:tc.first])
			}
			p := make([]byte, tc.n)
			n, err := v.ReadAt(p, tc.off)
			if err != nil || string(p[:n]) != tc.want || v.inflated() != 1+tc.counts {
				t.Errorf("ReadAt = %q (%v), %d more inflations, want %q, %d", p[:n], err, v.inflated()-1, tc.want, tc.counts)
			}
		})
	}
}

// TestObjStmView_ReadAtTheFileStart reads a file from its first byte, where
// no stream has been inflated yet, through a view that decrypts: the bytes
// are the file's.
func TestObjStmView_ReadAtTheFileStart(t *testing.T) {
	c := stringCrypt{key: []byte{1, 2, 3, 4, 5}, streams: true}
	v := &objStmView{ReaderAt: strings.NewReader("%PDF-1.7\n"), crypt: &c}
	p := make([]byte, 8)
	if n, err := v.ReadAt(p, 0); err != nil || string(p[:n]) != "%PDF-1.7" || v.inflated() != 0 {
		t.Errorf("ReadAt = %q (%v), %d inflations, want the header and none", p[:n], err, v.inflated())
	}
}

// TestObjStmView_StreamsInOrder notices three object streams out of the order
// they are in the file, reads each one twice, and must count six inflations
// and find each stream again by where its data starts, keeping the list
// ordered by it.
func TestObjStmView_StreamsInOrder(t *testing.T) {
	var file strings.Builder
	file.WriteString("%PDF-1.7\n")
	starts := make([]int64, 3)
	for i := range starts {
		fmt.Fprintf(&file, "%d 0 obj\n<</Type/ObjStm>>stream\n", 20+i)
		starts[i] = int64(file.Len())
		file.WriteString("DATA\nendstream\nendobj\n")
	}
	v := &objStmView{ReaderAt: strings.NewReader(file.String())}
	p := make([]byte, 4)
	// sequential: each read notices a stream the later ones find again
	for _, i := range []int{2, 0, 1, 1, 0, 2} {
		if _, err := v.ReadAt(p, starts[i]); err != nil {
			t.Fatal(err)
		}
	}
	if v.inflated() != 6 || len(v.streams) != 3 {
		t.Fatalf("%d inflations of %d streams, want 6 of 3", v.inflated(), len(v.streams))
	}
	for i, s := range v.streams {
		if s.start != starts[i] || s.in.id != uint32(20+i) {
			t.Errorf("stream %d = %+v, want the data of object %d at %d", i, s, 20+i, starts[i])
		}
	}
}

// TestObjStmView_NoView holds the view's two answers on no view: the file
// itself is what the reader is given, and nothing has been inflated.
func TestObjStmView_NoView(t *testing.T) {
	var v *objStmView
	f := strings.NewReader("file")
	if v.over(f) != f || v.inflated() != 0 {
		t.Error("no view must give the file as it is and count nothing")
	}
}

// TestStringCrypt_DecryptStream decrypts a stream's data: under RC4 every
// byte, and under AES the whole blocks after the vector, with their padding,
// whatever end of line follows them, and nothing from data too short to hold
// a block after the vector.
func TestStringCrypt_DecryptStream(t *testing.T) {
	in := objRef{id: 7}
	rc4 := stringCrypt{key: []byte{1, 2, 3, 4, 5}}
	if got := rc4.decryptStream(rc4XOR(rc4.objectKey(in), []byte("stream data\n")), in); string(got) != "stream data\n" {
		t.Errorf("RC4 decryptStream = %q", got)
	}
	c := stringCrypt{key: bytes.Repeat([]byte{3}, 16), aes: true}
	block, err := aes.NewCipher(c.objectKey(in))
	if err != nil {
		t.Fatal(err)
	}
	iv := bytes.Repeat([]byte{9}, aes.BlockSize)
	padded := append([]byte("compressed data"), 1, 16, 16, 16, 16, 16, 16, 16, 16, 16, 16, 16, 16, 16, 16, 16, 16)
	sealed := make([]byte, len(padded))
	cipher.NewCBCEncrypter(block, iv).CryptBlocks(sealed, padded)
	for _, tc := range []struct {
		name string
		data []byte
		want []byte
	}{
		{"whole blocks", append(iv, sealed...), padded},
		{"one block", append(iv, sealed[:aes.BlockSize]...), padded[:aes.BlockSize]},
		{"and a line feed", append(append(iv, sealed...), '\n'), padded},
		{"and a carriage return and a line feed", append(append(iv, sealed...), '\r', '\n'), padded},
		{"the vector alone", iv, nil},
		{"the vector and part of a block", append(iv, sealed[:15]...), nil},
		{"the vector and two bytes", append(iv, sealed[:2]...), nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// Nothing decrypted is nil, not an empty slice.
			if got := c.decryptStream(tc.data, in); !bytes.Equal(got, tc.want) || (got == nil) != (tc.want == nil) {
				t.Errorf("AES decryptStream = %q, want %q", got, tc.want)
			}
		})
	}
}
