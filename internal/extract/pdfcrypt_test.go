package extract

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/ledongthuc/pdf"
)

// hiddenTrailer opens the fixture at path with its encryption hidden, as
// selfDecrypting does, and returns its trailer.
func hiddenTrailer(t *testing.T, path string) pdf.Value {
	t.Helper()
	h, ok := hideEncryption(docFor(t, path))
	if !ok {
		t.Fatalf("hideEncryption(%s) failed", path)
	}
	return h.r.Trailer()
}

// TestStandardCrypt_Fixtures reads the encryption dictionary of each
// encrypted fixture as the file holds it, and must derive the key of the
// empty user password for every RC4 and AES-128 one: RC4 at revision 2 and 3
// with keys of 40, 80, 88 and 128 bits and under a crypt filter, and AES-128
// with the crypt filter's length in bytes and in bits, written by qpdf,
// Ghostscript, pdfcpu and pikepdf, one leaving its metadata unencrypted. An
// AES-256 file and one that needs a user password are refused.
func TestStandardCrypt_Fixtures(t *testing.T) {
	for _, tc := range []struct {
		name   string
		ok     bool
		aes    bool
		keyLen int
	}{
		{"encrypted-rc4-40.pdf", true, false, 5},
		{"encrypted-rc4-40-titles.pdf", true, false, 5},
		{"encrypted-rc4-80.pdf", true, false, 10},
		{"encrypted-rc4-88.pdf", true, false, 11},
		{"encrypted-rc4.pdf", true, false, 16},
		{"encrypted-rc4-v4.pdf", true, false, 16},
		{"encrypted-aes128.pdf", true, true, 16},
		{"encrypted-aes128-titles.pdf", true, true, 16},
		{"encrypted-aes128-cf-bits.pdf", true, true, 16},
		{"encrypted-aes128-clear-metadata-titles.pdf", true, true, 16},
		{"encrypted-aes256.pdf", false, false, 0},
		{"encrypted-aes128-user-password.pdf", false, false, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			trailer := hiddenTrailer(t, filepath.Join("testdata", tc.name))
			c, ok := standardCrypt(trailer.Key(hiddenEncryptKey), trailer.Key("ID").Index(0).RawString())
			if ok != tc.ok || c.aes != tc.aes || len(c.key) != tc.keyLen {
				t.Errorf("standardCrypt = %v aes=%v key of %d bytes, want %v aes=%v key of %d", ok, c.aes, len(c.key), tc.ok, tc.aes, tc.keyLen)
			}
		})
	}
}

// encryptDict returns the dictionary dict, written as the catalog's /T of a
// built file, as the reader reads it.
func encryptDict(t *testing.T, dict string) pdf.Value {
	t.Helper()
	return readerFor(t, buildPDF([]string{"<</Type/Catalog/T " + dict + ">>"})).Trailer().Key("Root").Key("T")
}

// TestStandardCrypt_Refusals starts from the 40-bit fixture's encryption
// dictionary, which opens, and changes one thing at a time: a filter other
// than the standard one, a revision below 2 or above 4, an O or a U one byte
// short, a version the walk does not decrypt, a crypt filter that is neither
// AES-128 nor RC4, and a U the empty password does not give. Each is refused.
func TestStandardCrypt_Refusals(t *testing.T) {
	trailer := hiddenTrailer(t, "testdata/encrypted-rc4-40.pdf")
	enc, id := trailer.Key(hiddenEncryptKey), trailer.Key("ID").Index(0).RawString()
	o, u := hex.EncodeToString([]byte(enc.Key("O").RawString())), hex.EncodeToString([]byte(enc.Key("U").RawString()))
	p := enc.Key("P").Int64()
	dict := func(filter, v, r, o, u, extra string) string {
		return fmt.Sprintf("<</Filter/%s/V %s/R %s/Length 40/O<%s>/U<%s>/P %d%s>>", filter, v, r, o, u, p, extra)
	}
	badU := "00" + u[2:]
	for _, tc := range []struct {
		name string
		dict string
		ok   bool
	}{
		{"the fixture's own", dict("Standard", "1", "2", o, u, ""), true},
		{"another filter", dict("Adobe.PubSec", "1", "2", o, u, ""), false},
		{"revision 1", dict("Standard", "1", "1", o, u, ""), false},
		{"revision 5", dict("Standard", "1", "5", o, u, ""), false},
		{"an O one byte short", dict("Standard", "1", "2", o[2:], u, ""), false},
		{"a U one byte short", dict("Standard", "1", "2", o, u[2:], ""), false},
		{"version 3", dict("Standard", "3", "2", o, u, ""), false},
		{"version 5", dict("Standard", "5", "2", o, u, ""), false},
		{"a crypt filter of no cipher", dict("Standard", "4", "2", o, u, "/CF<</StdCF<</CFM/None>>>>/StrF/StdCF"), false},
		{"a U another password gives", dict("Standard", "1", "2", o, badU, ""), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, ok := standardCrypt(encryptDict(t, tc.dict), id); ok != tc.ok {
				t.Errorf("standardCrypt(%s) = %v, want %v", tc.dict, ok, tc.ok)
			}
		})
	}
}

// TestStringCipher reads how each version and crypt filter encrypts strings:
// RC4 throughout at versions 1 and 2, AES-128 under an AESV2 filter whatever
// its /Length, RC4 under a V2 filter with the dictionary's /Length, and
// nothing the walk decrypts under any other version, a filter of no cipher, a
// filter /StrF does not name, or an RC4 key length out of bounds.
func TestStringCipher(t *testing.T) {
	for _, tc := range []struct {
		name string
		dict string
		aes  bool
		n    int
		ok   bool
	}{
		{"version 1", "<</V 1/Length 40>>", false, 5, true},
		{"version 2", "<</V 2/Length 128>>", false, 16, true},
		{"version 2, a key out of bounds", "<</V 2/Length 136>>", false, 0, false},
		{"AES-128", "<</V 4/CF<</StdCF<</CFM/AESV2/Length 128>>>>/StrF/StdCF>>", true, 16, true},
		{"RC4 under a crypt filter", "<</V 4/Length 128/CF<</StdCF<</CFM/V2/Length 16>>>>/StrF/StdCF>>", false, 16, true},
		{"RC4 under a crypt filter, a key out of bounds", "<</V 4/Length 44/CF<</StdCF<</CFM/V2>>>>/StrF/StdCF>>", false, 0, false},
		{"a filter of no cipher", "<</V 4/CF<</StdCF<</CFM/None>>>>/StrF/StdCF>>", false, 0, false},
		{"a filter /StrF does not name", "<</V 4/CF<</StdCF<</CFM/AESV2>>>>/StrF/Other>>", false, 0, false},
		{"version 3", "<</V 3/Length 128>>", false, 0, false},
		{"version 5", "<</V 5/Length 256>>", false, 0, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			useAES, n, ok := stringCipher(encryptDict(t, tc.dict), 3)
			if useAES != tc.aes || n != tc.n || ok != tc.ok {
				t.Errorf("stringCipher = %v %d %v, want %v %d %v", useAES, n, ok, tc.aes, tc.n, tc.ok)
			}
		})
	}
}

// TestRC4KeyBytes holds the RC4 key length to the standard's: 40 bits at
// revision 2 whatever the dictionary says, 40 when it says nothing, and
// otherwise what it says, a multiple of 8 from 40 to 128.
func TestRC4KeyBytes(t *testing.T) {
	for _, tc := range []struct {
		revision, bits int64
		n              int
		ok             bool
	}{
		{2, 128, 5, true},
		{3, 0, 5, true},
		{3, 40, 5, true},
		{3, 48, 6, true},
		{4, 88, 11, true},
		{3, 128, 16, true},
		{3, 32, 0, false},
		{3, 44, 0, false},
		{3, 136, 0, false},
	} {
		t.Run(fmt.Sprintf("R=%d %d bits", tc.revision, tc.bits), func(t *testing.T) {
			if n, ok := rc4KeyBytes(tc.revision, tc.bits); n != tc.n || ok != tc.ok {
				t.Errorf("rc4KeyBytes = %d %v, want %d %v", n, ok, tc.n, tc.ok)
			}
		})
	}
}

// TestObjectKey holds an object's key to the standard's length, five bytes
// more than the file key and no more than 16, and to its inputs: another
// object, another generation, and AES's salt each give another key.
func TestObjectKey(t *testing.T) {
	for _, tc := range []struct{ fileKey, want int }{{5, 10}, {10, 15}, {11, 16}, {16, 16}} {
		t.Run(fmt.Sprintf("a %d-byte file key", tc.fileKey), func(t *testing.T) {
			if got := (stringCrypt{key: make([]byte, tc.fileKey)}).objectKey(objRef{id: 7}); len(got) != tc.want {
				t.Errorf("objectKey is %d bytes, want %d", len(got), tc.want)
			}
		})
	}
	c := stringCrypt{key: bytes.Repeat([]byte{1}, 16)}
	keys := map[string][]byte{
		"base":           c.objectKey(objRef{id: 7}),
		"another object": c.objectKey(objRef{id: 0x10007}),
		"another gen":    c.objectKey(objRef{id: 7, gen: 0x100}),
		"AES":            stringCrypt{key: c.key, aes: true}.objectKey(objRef{id: 7}),
	}
	for name, key := range keys {
		t.Run(name, func(t *testing.T) {
			if name != "base" && bytes.Equal(key, keys["base"]) {
				t.Errorf("%s gives the same key as the base", name)
			}
		})
	}
}

// encryptAES encrypts plain as a PDF string under AES-128-CBC with key and iv,
// padded as the standard pads it, and returns the vector and the blocks.
func encryptAES(t *testing.T, key, iv []byte, plain string) string {
	t.Helper()
	pad := aes.BlockSize - len(plain)%aes.BlockSize
	data := append([]byte(plain), bytes.Repeat([]byte{byte(pad)}, pad)...)
	block, err := aes.NewCipher(key)
	if err != nil {
		t.Fatal(err)
	}
	cipher.NewCBCEncrypter(block, iv).CryptBlocks(data, data)
	return string(iv) + string(data)
}

// TestStringCrypt_DecryptAES decrypts strings encrypted under an object's
// AES key: one shorter than a block and one a block long, and one in whole
// blocks whose padding is not valid, are not decrypted; an empty string is
// empty; and a string one block long, padded by a whole block, and a longer
// one, come back as they were written. A string written in no object, as one
// taken out of an object stream is, is not encrypted and comes as it is.
func TestStringCrypt_DecryptAES(t *testing.T) {
	c := stringCrypt{key: bytes.Repeat([]byte{3}, 16), aes: true}
	in := objRef{id: 9}
	key := c.objectKey(in)
	iv := bytes.Repeat([]byte{5}, aes.BlockSize)
	sixteen := encryptAES(t, key, iv, "Sixteen bytes ok")
	for _, tc := range []struct {
		name string
		s    string
		want string
		ok   bool
	}{
		{"empty", "", "", true},
		{"shorter than a block", "short", "", false},
		{"the vector alone", string(iv), "", false},
		{"not a whole number of blocks", sixteen[:len(sixteen)-1], "", false},
		{"a padding that is not valid", sixteen[:len(sixteen)-aes.BlockSize], "", false},
		{"one block, padded by a whole block", sixteen, "Sixteen bytes ok", true},
		{"a title", encryptAES(t, key, iv, "Introducción"), "Introducción", true},
		{"an empty title", encryptAES(t, key, iv, ""), "", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got, ok := c.decrypt(tc.s, in); got != tc.want || ok != tc.ok {
				t.Errorf("decrypt = %q %v, want %q %v", got, ok, tc.want, tc.ok)
			}
		})
	}
	for name, c := range map[string]stringCrypt{"AES in no object": c, "RC4 in no object": {key: []byte{1, 2, 3, 4, 5}}} {
		t.Run(name, func(t *testing.T) {
			if got, ok := c.decrypt("ภาคผนวก", objRef{}); got != "ภาคผนวก" || !ok {
				t.Errorf("decrypt = %q %v, want it as it is", got, ok)
			}
		})
	}
}

// TestReaderStrings decodes the strings the reader hands over: under AES
// without the padding it leaves on a string it decrypted, and as they are
// when the file is not encrypted with AES or the string was in no encrypted
// object, as one taken out of an object stream is, which the reader did not
// decrypt and whose last bytes only look like padding.
func TestReaderStrings(t *testing.T) {
	looksPadded := "\xfe\xff\x0e\x20\x0e\x32\x0e\x04\x0e\x1c\x0e\x19\x0e\x27\x0e\x01"
	for _, tc := range []struct {
		name string
		aes  bool
		in   objRef
		want string
	}{
		{"decrypted by the reader", true, objRef{id: 8}, looksPadded[:15]},
		{"out of an object stream", true, objRef{}, looksPadded},
		{"not AES", false, objRef{id: 8}, looksPadded},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got, ok := readerStrings(tc.aes)(looksPadded, tc.in); got != tc.want || !ok {
				t.Errorf("decoded %q %v, want %q", got, ok, tc.want)
			}
		})
	}
}

// TestStringCrypt_DecryptRC4 decrypts with RC4, which is its own inverse: a
// string decrypted twice under one object's key is the string, decrypted once
// it is other bytes, and under another object's key other bytes again.
func TestStringCrypt_DecryptRC4(t *testing.T) {
	c := stringCrypt{key: []byte{1, 2, 3, 4, 5}}
	const title = "Chapter 1 Foundations"
	once, ok := c.decrypt(title, objRef{id: 12})
	twice, _ := c.decrypt(once, objRef{id: 12})
	other, _ := c.decrypt(once, objRef{id: 13})
	if !ok || once == title || twice != title || other == title {
		t.Errorf("decrypt: once %q (%v), twice %q, another object's key %q", once, ok, twice, other)
	}
}

// TestPKCS5Unpad removes a valid padding and reports one that is not: empty
// or not whole blocks, a last byte of zero or past a block, and a last byte
// whose count the bytes before it do not repeat.
func TestPKCS5Unpad(t *testing.T) {
	block := func(head string, pad byte, n int) string { return head + strings.Repeat(string(pad), n) }
	for _, tc := range []struct {
		name string
		s    string
		want string
		ok   bool
	}{
		{"empty", "", "", false},
		{"not a whole block", "fifteen bytes!!", "", false},
		{"a last byte of zero", block("fifteen bytes!!", 0, 1), "", false},
		{"a last byte past a block", block("fifteen bytes!!", 17, 1), "", false},
		{"a count not repeated", block("fourteen bytesX", 2, 1), "", false},
		{"one byte", block("fifteen bytes!!", 1, 1), "fifteen bytes!!", true},
		{"three bytes", block("thirteen byte", 3, 3), "thirteen byte", true},
		{"a whole block", block("", 16, 16), "", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got, ok := pkcs5Unpad(tc.s); got != tc.want || ok != tc.ok {
				t.Errorf("pkcs5Unpad = %q %v, want %q %v", got, ok, tc.want, tc.ok)
			}
		})
	}
}

// TestFindEncryptKey finds the /Encrypt key a space or a delimiter follows,
// past a name it only begins, a slash that breaks a match, a NUL, and the end
// of the buffer the scan reads through, and nothing in text with no key.
func TestFindEncryptKey(t *testing.T) {
	long := strings.Repeat("a", scanBuffer+5)
	for _, tc := range []struct {
		name string
		text string
		at   int64
		ok   bool
	}{
		{"a reference", "/Encrypt 5 0 R", 0, true},
		{"a dictionary in place", "xx/Encrypt<</V 1>>", 2, true},
		{"after a longer name", "/EncryptMetadata false/Encrypt 3 0 R", 22, true},
		{"after a slash that breaks a match", "/Encr/Encrypt\n", 5, true},
		{"a double slash", "//Encrypt ", 1, true},
		{"a NUL after it", "/Encryp /Encrypt\x00", 8, true},
		{"a comment after it", "/Encrypt%", 0, true},
		{"past the scan's buffer", long + "/Encrypt ", int64(len(long)), true},
		{"at the end", "/Encrypt", 0, false},
		{"a longer name only", "/Encrypted ", 0, false},
		{"another letter", "/EncrYpt ", 0, false},
		{"empty", "", 0, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if at, ok := findEncryptKey(strings.NewReader(tc.text)); at != tc.at || ok != tc.ok {
				t.Errorf("findEncryptKey = %d %v, want %d %v", at, ok, tc.at, tc.ok)
			}
		})
	}
}

// TestIsRegularByte holds the bytes that end a name to ISO 32000-1's white
// space and delimiters, and every other byte to a name's.
func TestIsRegularByte(t *testing.T) {
	for i := range 256 {
		c := byte(i)
		t.Run(fmt.Sprintf("%#02x", c), func(t *testing.T) {
			want := !strings.ContainsRune(nonRegularBytes, rune(c))
			if got := isRegularByte(c); got != want {
				t.Errorf("isRegularByte(%#x) = %v, want %v", c, got, want)
			}
		})
	}
}

// shortReaderAt returns one byte less than asked for, as a file that shrank
// between its size being read and its bytes would.
type shortReaderAt struct{ io.ReaderAt }

// ReadAt reads all of p but its last byte.
func (r shortReaderAt) ReadAt(p []byte, off int64) (int, error) {
	return r.ReaderAt.ReadAt(p[:len(p)-1], off)
}

// TestTrailerEncryptAt finds the key from the offset the last startxref gives,
// and nothing when that cannot be read: no startxref, nothing after it, a
// value that is not a number or is negative, an offset past the key, or a
// tail that reads short. A startxref of 0 and one opening the tail are read.
func TestTrailerEncryptAt(t *testing.T) {
	for _, tc := range []struct {
		name string
		text string
		at   int64
		ok   bool
	}{
		{"the last startxref", "x/Encrypt 1 0 R\nstartxref 99\nstartxref 0\n%%EOF", 1, true},
		{"from where it leads", "/Encrypt 1 0 R /Encrypt 2 0 R\nstartxref 2\n%%EOF", 15, true},
		{"opening the tail", "startxref 13\n/Encrypt 1 0 R", 13, true},
		{"no startxref", "/Encrypt 1 0 R\n%%EOF", 0, false},
		{"nothing after it", "/Encrypt 1 0 R\nstartxref", 0, false},
		{"not a number", "/Encrypt 1 0 R\nstartxref x\n%%EOF", 0, false},
		{"negative", "/Encrypt 1 0 R\nstartxref -1\n%%EOF", 0, false},
		{"past the key", "/Encrypt 1 0 R\nstartxref 900\n%%EOF", 0, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := document{r: strings.NewReader(tc.text), size: int64(len(tc.text))}
			if at, ok := trailerEncryptAt(d); ok != tc.ok || ok && at != tc.at {
				t.Errorf("trailerEncryptAt = %d %v, want %d %v", at, ok, tc.at, tc.ok)
			}
		})
	}
	t.Run("bytes past the file's size", func(t *testing.T) {
		const file = "startxref 20\n%%EOF"
		d := document{r: strings.NewReader(file + "\n    /Encrypt 1 0 R"), size: int64(len(file))}
		if _, ok := trailerEncryptAt(d); ok {
			t.Error("trailerEncryptAt scanned past the file's size")
		}
	})
	t.Run("a tail that reads short", func(t *testing.T) {
		const text = "/Encrypt 1 0 R\nstartxref 0\n%%EOF"
		d := document{r: shortReaderAt{strings.NewReader(text)}, size: int64(len(text))}
		if _, ok := trailerEncryptAt(d); ok {
			t.Error("trailerEncryptAt read a tail it was given short")
		}
	})
}

// withTrailer returns a built file with extra written into its trailer
// dictionary, after /Root. The trailer follows the cross-reference table, so
// every object stays where the table says.
func withTrailer(data []byte, extra string) []byte {
	i := bytes.LastIndex(data, []byte("/Root 1 0 R>>")) + len("/Root 1 0 R")
	return slices.Concat(data[:i], []byte(extra), data[i:])
}

// docOf returns data as a document.
func docOf(data []byte) document {
	return document{r: bytes.NewReader(data), size: int64(len(data))}
}

// TestHiddenEncryption_Dict reads, with the encryption hidden, an encryption
// dictionary that is an object of its own, which starts where that object
// does, and one written in the trailer, which starts at its key. Each is read
// as the file holds it.
func TestHiddenEncryption_Dict(t *testing.T) {
	const enc = "<</Filter/Standard/V 4/R 4>>"
	indirect := withTrailer(buildPDF([]string{"<</Type/Catalog>>", enc}), "/Encrypt 2 0 R")
	direct := withTrailer(buildPDF([]string{"<</Type/Catalog>>"}), "/Encrypt"+enc)
	for _, tc := range []struct {
		name string
		data []byte
		at   string
	}{
		{"an object of its own", indirect, "2 0 obj"},
		{"in the trailer", direct, "/Encrypt<<"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, ok := hideEncryption(docOf(tc.data))
			if !ok {
				t.Fatal("hideEncryption failed")
			}
			dict, from := h.dict()
			if want := int64(bytes.Index(tc.data, []byte(tc.at))); from != want || dict.Key("V").Int64() != 4 {
				t.Errorf("dict = %v at %d, want V 4 at %d", dict, from, want)
			}
		})
	}
}

// TestHideEncryption_Refusals opens nothing for a file with no /Encrypt key
// in its newest trailer, and for one whose bytes do not open as a PDF once
// the key is hidden.
func TestHideEncryption_Refusals(t *testing.T) {
	const notAPDF = "startxref 0\n/Encrypt 1 0 R\n%%EOF"
	for _, tc := range []struct {
		name string
		d    document
	}{
		{"no key", docFor(t, sectionsPDF)},
		{"not a PDF", docOf([]byte(notAPDF))},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, ok := hideEncryption(tc.d); ok {
				t.Error("hideEncryption opened it")
			}
		})
	}
}

// viewBytes reads all of size bytes of view.
func viewBytes(t *testing.T, view io.ReaderAt, size int64) string {
	t.Helper()
	p := make([]byte, size)
	if n, err := view.ReadAt(p, 0); int64(n) != size && !errors.Is(err, io.EOF) {
		t.Fatalf("ReadAt = %d %v", n, err)
	}
	return string(p)
}

// TestAESLengthFixed shows the reader every AES-128 crypt filter's /Length as
// 16 at the width it was written in, in either order of the filter's keys and
// wherever the filter comes among the others, and leaves alone the
// dictionary's own /Length, a filter that is not AES-128, a length already
// 16, and a file with no encryption dictionary.
func TestAESLengthFixed(t *testing.T) {
	const enc = "<</Filter/Standard/V 4/R 4/Length 128/CF<</Other<</CFM/V2/Length 128>>/StdCF<</Length 128/CFM/AESV2>>/Bytes<</CFM/AESV2/Length 16>>/EFF<</CFM/AESV2/Length 0128>>>>>>"
	const fixed = "<</Filter/Standard/V 4/R 4/Length 128/CF<</Other<</CFM/V2/Length 128>>/StdCF<</Length 16 /CFM/AESV2>>/Bytes<</CFM/AESV2/Length 16>>/EFF<</CFM/AESV2/Length 16  >>>>>>"
	built := withTrailer(buildPDF([]string{"<</Type/Catalog>>", enc}), "/Encrypt 2 0 R")
	if got := viewBytes(t, aesLengthFixed(docOf(built)), int64(len(built))); got != strings.Replace(string(built), enc, fixed, 1) {
		t.Errorf("the built file reads:\n%s", got)
	}
	pdfcpu := mustRead(t, "testdata/encrypted-aes128-cf-bits.pdf")
	shown := viewBytes(t, aesLengthFixed(docOf(pdfcpu)), int64(len(pdfcpu)))
	if shown != strings.Replace(string(pdfcpu), "/AESV2/Length 128>>", "/AESV2/Length 16 >>", 1) {
		t.Error("pdfcpu's filter does not read as /Length 16")
	}
	for _, path := range []string{sectionsPDF, "testdata/encrypted-rc4-v4.pdf", "testdata/encrypted-aes128.pdf"} {
		t.Run(filepath.Base(path), func(t *testing.T) {
			data := mustRead(t, path)
			if viewBytes(t, aesLengthFixed(docOf(data)), int64(len(data))) != string(data) {
				t.Error("the file reads otherwise than it is")
			}
		})
	}
}

// TestSelfDecrypting opens with its encryption hidden a file the walk
// decrypts, with a view that decrypts its object streams, and nothing for a
// file with no /Encrypt key, one that does not open as a PDF with the key
// hidden, and one encrypted with AES-256. A file whose /StmF names another
// crypt filter than its /StrF, which leaves streams unencrypted when it is
// /Identity, opens with a view that only counts them.
func TestSelfDecrypting(t *testing.T) {
	const notAPDF = "startxref 0\n/Encrypt 1 0 R\n%%EOF"
	identity := bytes.Replace(mustRead(t, "testdata/encrypted-aes128-titles.pdf"), []byte("/StmF /StdCF"), []byte("/StmF /Ident"), 1)
	for _, tc := range []struct {
		name    string
		d       document
		ok      bool
		streams bool
	}{
		{"40-bit RC4", docFor(t, "testdata/encrypted-rc4-40.pdf"), true, true},
		{"streams under another filter", docOf(identity), true, false},
		{"not encrypted", docFor(t, sectionsPDF), false, false},
		{"not a PDF", document{r: strings.NewReader(notAPDF), size: int64(len(notAPDF))}, false, false},
		{"AES-256", docFor(t, "testdata/encrypted-aes256.pdf"), false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			src := selfDecrypting(tc.d)
			if (src.r != nil) != tc.ok || (src.strs != nil) != tc.ok || (src.view != nil && src.view.crypt != nil) != tc.streams {
				t.Errorf("selfDecrypting = %v, %v, %+v, want a reader: %v, streams decrypted: %v", src.r, src.strs != nil, src.view, tc.ok, tc.streams)
			}
		})
	}
}

// TestSelfDecryptable names the refusals selfDecrypting is asked about: a
// password the reader could not match, however wrapped, a crypt filter it
// does not take, and a trailer with no /ID, and no other, nor a damaged file
// whose error quotes the words.
func TestSelfDecryptable(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		want bool
	}{
		{"a password", pdf.ErrInvalidPassword, true},
		{"a password, wrapped", fmt.Errorf("opening: %w", pdf.ErrInvalidPassword), true},
		{"a crypt filter", errors.New(v4Refusal + " <<...>>"), true},
		{"no /ID", errors.New(missingIDRefusal), true},
		{"damage ending in the words", errors.New("malformed PDF: unexpected " + missingIDRefusal), false},
		{"AES-256", errors.New("malformed PDF: 256-bit encryption key"), false},
		{"version 5", errors.New("unsupported PDF: encryption version V=5; <<...>>"), false},
		{"damage quoting the words", errors.New("malformed PDF: unexpected " + v4Refusal), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := selfDecryptable(tc.err); got != tc.want {
				t.Errorf("selfDecryptable = %v, want %v", got, tc.want)
			}
		})
	}
}
