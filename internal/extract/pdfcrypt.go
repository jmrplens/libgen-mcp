// pdfcrypt.go decrypts the strings of a PDF that ledongthuc/pdf decrypts into
// other bytes or will not decrypt, so that the outline walk can read them.

package extract

import (
	"bufio"
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/md5" //nolint:gosec // ISO 32000-1 derives the RC4 and AES-128 keys of a PDF with MD5, which is not a choice made here.
	"crypto/rc4" //nolint:gosec // RC4 is the cipher the file was encrypted with, read here, never used to protect anything.
	"encoding/binary"
	"fmt"
	"io"
	"regexp"
	"strconv"
	"strings"

	"github.com/ledongthuc/pdf"
)

// paddingString is the 32-byte padding string of the standard security
// handler (ISO 32000-1, 7.6.3.3, Algorithm 2), which pads a password to 32
// bytes and is the whole of the empty one. One literal rather than a
// concatenation, for the reason noPDFOutlineReason gives.
const paddingString = "\x28\xbf\x4e\x5e\x4e\x75\x8a\x41\x64\x00\x4e\x56\xff\xfa\x01\x08\x2e\x2e\x00\xb6\xd0\x68\x3e\x80\x2f\x0c\xa9\xfe\x64\x53\x69\x7a"

// stringCrypt decrypts the strings and object streams of a file encrypted by
// the standard security handler that opens with an empty user password, which
// is every file this server can read: key is the file key, aes says whether
// strings are encrypted with AES-128 or with RC4, and stream is how streams
// are decrypted (streamCrypt), nil when they are not.
type stringCrypt struct {
	key    []byte
	aes    bool
	stream *stringCrypt
}

// Bounds the standard security handler sets on a file key, in bits, and how
// many times Algorithm 2 hashes it again from revision 3 on.
const (
	minFileKeyBits = 40
	maxFileKeyBits = 128
	keyRehashes    = 50
)

// standardCrypt reads enc, a file's encryption dictionary as the file holds
// it, and id, the first element of the trailer's /ID, and returns how to
// decrypt the file's strings, for the encryptions whose strings the reader
// gets wrong or refuses: RC4 (V=1 and V=2) and, under a crypt filter (V=4),
// AES-128 or RC4. Anything else, AES-256 among it, is refused, and so is a
// file the empty user password does not open, since its key is not the one
// derived here.
func standardCrypt(enc pdf.Value, id string) (stringCrypt, bool) {
	revision := enc.Key("R").Int64()
	o, u := enc.Key("O").RawString(), enc.Key("U").RawString()
	if enc.Key("Filter").Name() != "Standard" || revision < 2 || revision > 4 || len(o) != len(paddingString) || len(u) != len(paddingString) {
		return stringCrypt{}, false
	}
	useAES, n, ok := stringCipher(enc, revision)
	if !ok {
		return stringCrypt{}, false
	}
	metadata := enc.Key("EncryptMetadata")
	key := fileKey(o, enc.Key("P").Int64(), id, revision, n, metadata.Kind() != pdf.Bool || metadata.Bool())
	if !userKeyMatches(key, u, id, revision) {
		return stringCrypt{}, false
	}
	return stringCrypt{key: key, aes: useAES, stream: streamCrypt(enc, key)}, true
}

// streamCrypt returns how enc's streams are decrypted under key, the file
// key: with RC4 at versions 1 and 2, and under crypt filters (V=4) by the
// method of the filter /StmF names, whatever /StrF names. /Identity, which
// /StmF is when it is absent, and a filter of no method leave streams
// unencrypted, and so does AES-128 under a file key that is not 16 bytes,
// which no AES-128 file has: each is nil. Deciding by whether /StmF and /StrF
// named the same filter left every object stream encrypted in a file whose
// two filters are alike under two names.
func streamCrypt(enc pdf.Value, key []byte) *stringCrypt {
	if enc.Key("V").Int64() != 4 {
		return &stringCrypt{key: key}
	}
	switch enc.Key("CF").Key(enc.Key("StmF").Name()).Key("CFM").Name() {
	case "AESV2":
		if len(key) == aes.BlockSize {
			return &stringCrypt{key: key, aes: true}
		}
	case "V2":
		return &stringCrypt{key: key}
	}
	return nil
}

// stringCipher says how enc encrypts strings: with AES-128 or not, and with a
// key of how many bytes. Version 1 and 2 are RC4 throughout. Version 4 names a
// crypt filter for strings, and it is AES-128, whose key is 16 bytes whatever
// the filter's /Length says, or RC4 with the key /Length gives.
func stringCipher(enc pdf.Value, revision int64) (useAES bool, n int, ok bool) {
	switch enc.Key("V").Int64() {
	case 1, 2:
		n, ok = rc4KeyBytes(revision, enc.Key("Length").Int64())
		return false, n, ok
	case 4:
		switch enc.Key("CF").Key(enc.Key("StrF").Name()).Key("CFM").Name() {
		case "AESV2":
			return true, aes.BlockSize, true
		case "V2":
			n, ok = rc4KeyBytes(revision, enc.Key("Length").Int64())
			return false, n, ok
		}
	}
	return false, 0, false
}

// rc4KeyBytes returns the length in bytes of an RC4 file key: 5 at revision
// 2, whatever bits says, and otherwise bits, a multiple of 8 from 40 to 128,
// or 40 when the dictionary gives none.
func rc4KeyBytes(revision, bits int64) (int, bool) {
	if revision == 2 || bits == 0 {
		return minFileKeyBits / 8, true
	}
	if bits%8 != 0 || bits < minFileKeyBits || bits > maxFileKeyBits {
		return 0, false
	}
	return int(bits / 8), true
}

// fileKey computes the n-byte file key of the empty user password from the
// encryption dictionary's O and P, and id (ISO 32000-1, 7.6.3.3, Algorithm
// 2). P is a 32-bit signed integer, hashed as its four bytes, low first. From
// revision 4, a file that leaves its metadata unencrypted says so in the key.
func fileKey(o string, p int64, id string, revision int64, n int, encryptMetadata bool) []byte {
	h := md5.New() //nolint:gosec // Algorithm 2 is defined with MD5.
	h.Write([]byte(paddingString + o))
	h.Write(binary.LittleEndian.AppendUint32(nil, uint32(p))) //nolint:gosec // G115: P is 32 bits, and its low four bytes are what is hashed.
	h.Write([]byte(id))
	if revision >= 4 && !encryptMetadata {
		h.Write([]byte{0xff, 0xff, 0xff, 0xff})
	}
	key := h.Sum(nil)
	if revision >= 3 {
		for range keyRehashes {
			sum := md5.Sum(key[:n]) //nolint:gosec // Algorithm 2 is defined with MD5.
			key = sum[:]
		}
	}
	return key[:n]
}

// userKeyMatches reports whether key is the key of the empty user password,
// by computing the U entry it gives and comparing it with u: all 32 bytes at
// revision 2 (Algorithm 4), the first 16 from revision 3 (Algorithm 5).
func userKeyMatches(key []byte, u, id string, revision int64) bool {
	if revision == 2 {
		return string(rc4XOR(key, []byte(paddingString))) == u
	}
	sum := md5.Sum([]byte(paddingString + id)) //nolint:gosec // Algorithm 5 is defined with MD5.
	got := sum[:]
	for i := range 20 {
		k := bytes.Clone(key)
		for j := range k {
			k[j] ^= byte(i)
		}
		got = rc4XOR(k, got)
	}
	return u[:len(got)] == string(got)
}

// rc4XOR returns data encrypted, or decrypted, with RC4 under key, which is 1
// to 256 bytes long.
func rc4XOR(key, data []byte) []byte {
	c, _ := rc4.NewCipher(key) //nolint:gosec // decrypting what the file holds.
	out := make([]byte, len(data))
	c.XORKeyStream(out, data)
	return out
}

// objectKey returns the key that encrypts the strings and streams of the
// object in: the MD5 of the file key, three bytes of the object number and two
// of the generation, and "sAlT" for AES, cut to 5 bytes more than the file key
// and to no more than 16 (ISO 32000-1, 7.6.2, Algorithm 1). ledongthuc/pdf
// keeps all 16 bytes, which is the same key only from an 11-byte file key up.
func (c stringCrypt) objectKey(in objRef) []byte {
	h := md5.New() //nolint:gosec // Algorithm 1 is defined with MD5.
	h.Write(c.key)
	h.Write(binary.LittleEndian.AppendUint32(nil, in.id)[:3])
	h.Write(binary.LittleEndian.AppendUint16(nil, in.gen))
	if c.aes {
		h.Write([]byte("sAlT"))
	}
	return h.Sum(nil)[:min(len(c.key)+5, md5.Size)]
}

// decrypt returns the string s, written in the object in, decrypted. A string
// written in no object, the trailer's or one taken out of an object stream,
// which is decrypted whole, is not encrypted and is returned as it is. An AES
// string is its 16-byte initialization vector and whole blocks after it, the
// last of them ending in its padding, and one that is not, the vector alone
// among them, is reported as not decrypted. An empty string is empty either
// way, which is how MuPDF writes one into an encrypted file.
func (c stringCrypt) decrypt(s string, in objRef) (string, bool) {
	if in == (objRef{}) {
		return s, true
	}
	key := c.objectKey(in)
	if !c.aes {
		return string(rc4XOR(key, []byte(s))), true
	}
	if s == "" {
		return "", true
	}
	if len(s)%aes.BlockSize != 0 {
		return "", false
	}
	block, _ := aes.NewCipher(key)
	plain := []byte(s[aes.BlockSize:])
	// NOSONAR: S5542 asks for an authenticated mode. ISO 32000-1 encrypts a
	// string with AES-128 in CBC mode and PKCS#5 padding, and this reads what
	// a file holds rather than protecting anything.
	cipher.NewCBCDecrypter(block, []byte(s[:aes.BlockSize])).CryptBlocks(plain, plain) // NOSONAR
	return pkcs5Unpad(string(plain))
}

// decryptStream returns data, a stream's data written in the object in,
// decrypted: under RC4 all of it, and under AES the whole blocks after its
// 16-byte initialization vector, the padding left on. The data of an object
// stream is compressed, and the filter that inflates it stops at its own end,
// as it does at the end of line before the endstream keyword, which data may
// hold too.
func (c stringCrypt) decryptStream(data []byte, in objRef) []byte {
	key := c.objectKey(in)
	if !c.aes {
		return rc4XOR(key, data)
	}
	if len(data) < 2*aes.BlockSize {
		return nil
	}
	blocks := data[aes.BlockSize : len(data)-len(data)%aes.BlockSize]
	block, _ := aes.NewCipher(key)
	plain := make([]byte, len(blocks))
	// NOSONAR: S5542, for the reason decrypt gives.
	cipher.NewCBCDecrypter(block, data[:aes.BlockSize]).CryptBlocks(plain, blocks) // NOSONAR
	return plain
}

// v4Refusal is how the reader's error begins when it refuses a crypt filter
// (V=4) it does not decrypt: one that is not AES-128, or AES-128 whose filter
// gives /Length in bits rather than bytes, or is opened on another event than
// the document's opening.
const v4Refusal = "unsupported PDF: encryption version V=4;"

// startxrefTail is how much of the end of a file is searched for its last
// startxref, which gives the offset of its newest cross-reference section.
const startxrefTail = 1024

// encryptKey is the trailer key that names a file's encryption dictionary,
// and hiddenEncryptKey is how hideEncryption shows it to the reader.
const (
	encryptKey       = "/Encrypt"
	hiddenEncryptKey = "Encrypx"
)

// scanBuffer is how much of the file trailerEncryptAt reads at a time.
const scanBuffer = 1 << 16

// trailerEncryptAt returns the offset of the /Encrypt key in the newest
// trailer of d, which is the one the reader reads. That trailer is in the
// cross-reference section the file's last startxref leads to: after the table,
// or the dictionary of the cross-reference stream. The first /Encrypt from
// there on that stands as a key, followed by a space or a delimiter, is it.
func trailerEncryptAt(d document) (int64, bool) {
	tail := min(d.size, startxrefTail)
	buf := make([]byte, tail)
	if n, _ := d.r.ReadAt(buf, d.size-tail); int64(n) != tail {
		return 0, false
	}
	_, after, found := bytes.CutLast(buf, []byte("startxref"))
	if !found {
		return 0, false
	}
	fields := bytes.Fields(after)
	if len(fields) == 0 {
		return 0, false
	}
	from, err := strconv.ParseInt(string(fields[0]), 10, 64)
	if err != nil || from < 0 {
		return 0, false
	}
	// A section of negative length reads without end, so one past the
	// file's end is read as empty.
	at, ok := findEncryptKey(io.NewSectionReader(d.r, from, max(d.size-from, 0)))
	return from + at, ok
}

// findEncryptKey returns the offset in r of the first /Encrypt that a space or
// a delimiter follows, read one byte at a time.
func findEncryptKey(r io.Reader) (int64, bool) {
	br := bufio.NewReaderSize(r, scanBuffer)
	matched := 0
	for at := int64(0); ; at++ {
		c, err := br.ReadByte()
		if err != nil {
			return 0, false
		}
		if matched == len(encryptKey) && !isRegularByte(c) {
			return at - int64(len(encryptKey)), true
		}
		matched = nextMatch(matched, c)
	}
}

// nextMatch returns how many bytes of encryptKey are matched once c follows
// matched of them. The key's slash appears in it only first, so a byte that
// breaks a match starts the next one if it is a slash, and nothing otherwise.
func nextMatch(matched int, c byte) int {
	if matched < len(encryptKey) && c == encryptKey[matched] {
		return matched + 1
	}
	if c == '/' {
		return 1
	}
	return 0
}

// nonRegularBytes are the bytes that end a PDF name: white space and the
// delimiters (ISO 32000-1, 7.2.2).
const nonRegularBytes = "\x00\t\n\f\r ()<>[]{}/%"

// isRegularByte reports whether c can be part of a PDF name.
func isRegularByte(c byte) bool {
	return strings.IndexByte(nonRegularBytes, c) < 0
}

// readStarts is a file that records the offset each read of it starts at.
type readStarts struct {
	io.ReaderAt
	starts []int64
}

// ReadAt records off and reads p from the file at off.
func (f *readStarts) ReadAt(p []byte, off int64) (int, error) {
	f.starts = append(f.starts, off)
	return f.ReaderAt.ReadAt(p, off)
}

// hiddenEncryption is a file opened with its encryption hidden from the
// reader: the last letter of the newest trailer's /Encrypt key, at key, reads
// as hiddenEncryptKey's, so the reader takes the file for one that is not
// encrypted, hands each string over as the file holds it, and reads the
// encryption dictionary under hiddenEncryptKey. reads records where it reads,
// and view is the file under it, which notices its object streams.
type hiddenEncryption struct {
	r     *pdf.Reader
	reads *readStarts
	view  *objStmView
	key   int64
}

// hideEncryption opens d with its encryption hidden, and reports false for a
// file whose newest trailer it cannot find the key in, or that does not open
// that way.
func hideEncryption(d document) (hiddenEncryption, bool) {
	at, ok := trailerEncryptAt(d)
	if !ok {
		return hiddenEncryption{}, false
	}
	shown := overwritten{ReaderAt: pdfBytes(d), at: at + int64(len(encryptKey)) - 1, with: hiddenEncryptKey[len(hiddenEncryptKey)-1:]}
	view := &objStmView{ReaderAt: shown}
	reads := &readStarts{ReaderAt: view}
	r, err := pdf.NewReader(reads, d.size)
	if err != nil {
		return hiddenEncryption{}, false
	}
	return hiddenEncryption{r: r, reads: reads, view: view, key: at}, true
}

// dict returns the encryption dictionary as the file holds it, and where its
// text starts: at the object the trailer refers to, which is the first thing
// the reader reads to resolve it, or, for a dictionary written in the trailer
// itself, at its key.
func (h hiddenEncryption) dict() (enc pdf.Value, from int64) {
	h.reads.starts = nil
	enc = h.r.Trailer().Key(hiddenEncryptKey)
	if len(h.reads.starts) == 0 {
		return enc, h.key
	}
	return enc, h.reads.starts[0]
}

// selfDecrypting opens d for the outline walk with its encryption hidden from
// the reader: the walk decrypts the strings it is handed, and the view shows
// the reader each object stream decrypted, when streams are encrypted. It
// returns no reader for a file hideEncryption does not open, or whose strings
// standardCrypt does not decrypt.
func selfDecrypting(d document) outlineSource {
	h, ok := hideEncryption(d)
	if !ok {
		return outlineSource{}
	}
	enc, _ := h.dict()
	c, ok := standardCrypt(enc, h.r.Trailer().Key("ID").Index(0).RawString())
	if !ok {
		return outlineSource{}
	}
	h.view.crypt = c.stream
	return outlineSource{r: h.r, strs: c.decrypt, view: h.view}
}

// cryptFilterWindow is how much of the file, from where its encryption
// dictionary starts, aesLengthFixed searches for crypt filters. A standard
// dictionary is a few hundred bytes.
const cryptFilterWindow = 1 << 14

var (
	// flatDictRE matches a dictionary with none inside it, as a crypt filter
	// is.
	flatDictRE = regexp.MustCompile(`<<[^<>]*>>`)
	// aesFilterRE matches a crypt filter's method when it is AES-128.
	aesFilterRE = regexp.MustCompile(`/CFM\s*/AESV2\b`)
	// filterLengthRE matches a crypt filter's /Length when it is long enough
	// to be written over with 16.
	filterLengthRE = regexp.MustCompile(`/Length\s+(\d\d+)\b`)
)

// aesLengthFixed returns d as the reader is shown it with the /Length of
// every AES-128 crypt filter in its encryption dictionary read as 16, the key
// length in bytes the reader takes, padded with spaces to the width it is
// written in. pdfcpu writes 128, the length in bits, and the reader refuses
// that, though an AES-128 key is 16 bytes whatever the filter says. It returns
// d as pdfBytes shows it when the dictionary cannot be found, which the reader
// refuses as it did.
func aesLengthFixed(d document) io.ReaderAt {
	view := pdfBytes(d)
	h, ok := hideEncryption(d)
	if !ok {
		return view
	}
	_, from := h.dict()
	window := make([]byte, cryptFilterWindow)
	n, _ := d.r.ReadAt(window, from)
	for _, m := range flatDictRE.FindAllIndex(window[:n], -1) {
		filter := window[m[0]:m[1]]
		length := filterLengthRE.FindSubmatchIndex(filter)
		if aesFilterRE.Match(filter) && length != nil {
			view = overwritten{ReaderAt: view, at: from + int64(m[0]+length[2]), with: fmt.Sprintf("%-*s", length[3]-length[2], "16")}
		}
	}
	return view
}
