package telemetry

import (
	"bytes"
	"crypto/fips140"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// TestExpand_ReproducesRFC5869TestCase3 holds the derivation to the published
// HKDF-SHA256 vector that uses no salt and no info, which is the shape expand
// calls it in apart from the info string.
//
// expand returns 32 bytes and the vector asks for 42, so what is compared is
// the first 32: HKDF's output blocks are chained, so the first block does not
// depend on how many follow it.
func TestExpand_ReproducesRFC5869TestCase3(t *testing.T) {
	t.Parallel()

	ikm := bytes.Repeat([]byte{0x0b}, 22)
	const okmPrefix = "8da4e775a563c18f715f802a063c5a31b8a11f5c5ee1879ec3454e5f3c738d2d"

	got, err := expand(ikm, "")
	if err != nil {
		t.Fatalf("expand: %v", err)
	}
	if hex.EncodeToString(got) != okmPrefix {
		t.Errorf("expand(RFC 5869 test case 3) = %x, want %s", got, okmPrefix)
	}
}

// previousDerivationSweep is the SHA-256 of every key the previous derivation
// gave for derivationInputs under each of identityKeyInfo, itemKeyInfo and the
// empty info, in that order, the 32-byte keys concatenated: 594 of them.
//
// The previous derivation is golang.org/x/crypto/hkdf v0.57.0, which derived
// these keys through 2.1.0 and was this test's oracle until 2.2.1 removed the
// module from go.mod. The digest and the vectors in previousDerivation were
// computed by it on the last commit that still had it, agreed byte for byte
// with the standard library's crypto/hkdf there, and agree with an independent
// HKDF-SHA256 written from RFC 5869 in Python, which, like both Go
// implementations, reproduces the RFC's SHA-256 test cases 1 to 3 in full.
const previousDerivationSweep = "c2307f3134597b105069fc8e8dcd49981a0a62fc723783acfb51da5764709e51"

// previousDerivation is what the previous derivation gave for the shapes of
// secret expand is handed, under each info string: a single byte, the hash
// size, the HMAC block size and one past it, a secret long enough that HMAC
// hashes it, the two text secrets, a zero key, a long one and the first
// generated key. The sweep digest covers every input, and these name one when
// it moves.
var previousDerivation = []struct {
	secret []byte
	info   string
	key    string
}{
	{[]byte("s"), identityKeyInfo, "483a3ddd0737712a9ac291d557b2d87f1f637af43cbcde063fabece0455a8c24"},
	{bytes.Repeat([]byte("s"), 32), identityKeyInfo, "a0148bba5b6932dbbfacbcac768be3dd87c7fad3f4b3c7afd167363c1a28cb53"},
	{bytes.Repeat([]byte("s"), 64), identityKeyInfo, "222341aa5f9521dbd4549cc0ed354dc46e04793bd9920858a6307cd60239676e"},
	{bytes.Repeat([]byte("s"), 65), identityKeyInfo, "064235ceee8f5cc424848ba3afeee72af6f6d0e81be28ab5254fbd3b61de1712"},
	{bytes.Repeat([]byte("s"), 130), identityKeyInfo, "b5245ecd515ad50d8ec4f6de0bd404ac3c9ef156bf4eac94e97e230611e6f4be"},
	{[]byte("a deployment-wide secret"), identityKeyInfo, "6bf8317cd9562fe1c05c6a323ca2490caf519483ccfc86fe6a9ba02b1192b83a"},
	{[]byte("clé de pseudonymisation, ünïcödé"), identityKeyInfo, "caa6b6203e084f5d5ab886f2a8dca76b4fbbfe0b583b060ac3619d0252585c02"},
	{bytes.Repeat([]byte{0}, 32), identityKeyInfo, "d2fcd6ead526296df1ef5bc6fd06f2b8deefbb451440383496d041847acf66f7"},
	{bytes.Repeat([]byte("0123456789abcdef"), 64), identityKeyInfo, "fb98ddd492f32cc2047bc6a3dbf3cc7ac1d569fa7faa0b840c022944920f24cf"},
	{derivationInputs()[0], identityKeyInfo, "0d81c54bd1f88bfb31d9a7788570c71bc69148dadc30629585584f71e45f22f1"},
	{[]byte("s"), itemKeyInfo, "ec417be5cc59e1c93a33e108d496a34b86c7ece6da952fc2317be97fa185f525"},
	{bytes.Repeat([]byte("s"), 32), itemKeyInfo, "9dc3656527127c133b07b27bf7827cbb180a7aa96ce0323b93c6c9f44e68b42b"},
	{bytes.Repeat([]byte("s"), 64), itemKeyInfo, "ef6fd113b3e5a6806f4eb9d0ca1888cf5425926ad98579f7a35572dfb00ac11d"},
	{bytes.Repeat([]byte("s"), 65), itemKeyInfo, "06db8ebfae411bee47471fd9b3c67c9f3423626537df8eb26c66712154d8f9be"},
	{bytes.Repeat([]byte("s"), 130), itemKeyInfo, "b7ce775b20be8a77d2ee298f12bda914e071d48218ba029168393d15105e9008"},
	{[]byte("a deployment-wide secret"), itemKeyInfo, "7e0f6fb63dbad0593db2c8fcdbd42d591fa9476b315fcecefd74070bdeb321bd"},
	{[]byte("clé de pseudonymisation, ünïcödé"), itemKeyInfo, "0397473c25a0c1127735ecdec32ed83804b08be8607c67cca63037c1f85564a0"},
	{bytes.Repeat([]byte{0}, 32), itemKeyInfo, "012e36bee4e5e2e6fa53747edfca17c7395f4a2473f6485778ff98686f035b5f"},
	{bytes.Repeat([]byte("0123456789abcdef"), 64), itemKeyInfo, "bc5e8d67347aa27022f6d70b20b2bb99066e82d1343c8a257fdf1235963ec14a"},
	{derivationInputs()[0], itemKeyInfo, "29e53af1e3e318f27e06ade53dfe7b684d7135e5f706725c0c2944096d1faa0e"},
	{[]byte("s"), "", "29b32de0827049df9c7730fc83b1ceb37e15840af422a91cde8c194946ce69b0"},
	{bytes.Repeat([]byte("s"), 32), "", "763c8c7e348749ef1ec3aad2b2137d263dd4075b16cbca22ea4c94aca8c88254"},
	{bytes.Repeat([]byte("s"), 64), "", "c7ced9128fa8424ef49c84cf6d95b1a783e06bea5650f7c184f29272e828ad73"},
	{bytes.Repeat([]byte("s"), 65), "", "2c0fb0cd4374aa921fcd42315f53211222c914a7b3b1818414fe1e14369e8f3e"},
	{bytes.Repeat([]byte("s"), 130), "", "8cc608ddd86fb4306d250af049587531e5a5e9c9655bf8942a15d6d55c18e32e"},
	{[]byte("a deployment-wide secret"), "", "39c474adf519ef1bab5a97a557b8c026e5a756539f2554a3086c12d9b653736e"},
	{[]byte("clé de pseudonymisation, ünïcödé"), "", "ec548adfc2832e2be1662c919e2215574b870621f56767c7b0d29250cd454268"},
	{bytes.Repeat([]byte{0}, 32), "", "df7204546f1bee78b85324a7898ca119b387e01386d1aef037781d4a8a036aee"},
	{bytes.Repeat([]byte("0123456789abcdef"), 64), "", "00a03b6ea23efd19d24e74178d2bced9d5da7feac71083cabbb729999a1d2460"},
	{derivationInputs()[0], "", "a0d87348ea7cf4b881d25d67c8a3a629346f78d1eb30f998cb7d55989cb35cf1"},
}

// TestExpand_AgreesWithThePreviousDerivation holds the standard library's HKDF
// to what golang.org/x/crypto/hkdf derived through 2.1.0, on the inputs expand
// is actually given.
//
// Agreement is the whole requirement. A configured secret is how several
// replicas, and one deployment across restarts, give a caller one pseudonym;
// a derivation that moved by one bit would split every caller in two on the
// day of the upgrade, and a distinct-user count would double without anybody
// having changed anything. The previous code was the oracle while the module
// was in go.mod. Its answers are frozen here instead: the vectors name the
// input that moved, and the sweep digest covers every input the oracle was
// asked about.
func TestExpand_AgreesWithThePreviousDerivation(t *testing.T) {
	t.Parallel()

	for _, v := range previousDerivation {
		t.Run(fmt.Sprintf("%q %d bytes", v.info, len(v.secret)), func(t *testing.T) {
			t.Parallel()

			got, err := expand(v.secret, v.info)
			if err != nil {
				t.Fatalf("expand(%x): %v", v.secret, err)
			}
			if hex.EncodeToString(got) != v.key {
				t.Errorf("expand(%x, %q) = %x, the previous derivation gave %s", v.secret, v.info, got, v.key)
			}
		})
	}

	t.Run("every input", func(t *testing.T) {
		t.Parallel()

		sweep := sha256.New()
		// sequential: every key goes into one digest, in this order
		for _, info := range []string{identityKeyInfo, itemKeyInfo, ""} {
			for _, secret := range derivationInputs() {
				got, err := expand(secret, info)
				if err != nil {
					t.Fatalf("expand(%x, %q): %v", secret, info, err)
				}
				sweep.Write(got)
			}
		}
		if got := hex.EncodeToString(sweep.Sum(nil)); got != previousDerivationSweep {
			t.Errorf("the keys for every input hash to %s, the previous derivation's to %s", got, previousDerivationSweep)
		}
	})
}

// derivationInputs is what expand is handed in production, in each shape it
// arrives: 32 bytes of randomness for a generated key (drawn deterministically
// here, so a failure names an input it can be rerun on), and an operator's
// secret, which is text of any length, including lengths below HKDF's block
// and hash sizes and above the HMAC block size, where HMAC hashes the key.
func derivationInputs() [][]byte {
	var inputs [][]byte
	seed := sha256.Sum256([]byte("keyring derivation inputs"))
	for range 64 {
		seed = sha256.Sum256(seed[:])
		inputs = append(inputs, bytes.Clone(seed[:]))
	}
	for length := 1; length <= 130; length++ {
		inputs = append(inputs, bytes.Repeat([]byte("s"), length))
	}
	return append(inputs,
		[]byte("a deployment-wide secret"),
		[]byte("clé de pseudonymisation, ünïcödé"),
		bytes.Repeat([]byte{0}, 32),
		bytes.Repeat([]byte("0123456789abcdef"), 64),
	)
}

// TestKeyring_AConfiguredSecretKeepsTheSameKeysAndPseudonyms pins what one
// configured secret produced before the derivation moved to the standard
// library, all the way to the sixteen hex characters a collector stores.
//
// The test above holds the HKDF to the previous derivation's answers; this one
// fixes the chain around it too (the info strings, the key length, the HMAC
// and its truncation), because a pseudonym already sitting in a dashboard
// depends on every one of them. The values were computed by the
// golang.org/x/crypto derivation on the commit before the derivation moved and
// agree with an independent HKDF and HMAC-SHA256 written from RFC 5869 in
// another language.
func TestKeyring_AConfiguredSecretKeepsTheSameKeysAndPseudonyms(t *testing.T) {
	t.Parallel()

	const secret = "a deployment-wide secret"
	for _, tc := range []struct {
		info string
		key  string
	}{
		{identityKeyInfo, "6bf8317cd9562fe1c05c6a323ca2490caf519483ccfc86fe6a9ba02b1192b83a"},
		{itemKeyInfo, "7e0f6fb63dbad0593db2c8fcdbd42d591fa9476b315fcecefd74070bdeb321bd"},
	} {
		t.Run(tc.info, func(t *testing.T) {
			t.Parallel()

			got, err := expand([]byte(secret), tc.info)
			if err != nil {
				t.Fatalf("expand: %v", err)
			}
			if hex.EncodeToString(got) != tc.key {
				t.Errorf("the %q key = %x, want %s", tc.info, got, tc.key)
			}
		})
	}

	ring, err := NewKeyring(secret, 0)
	if err != nil {
		t.Fatalf("NewKeyring: %v", err)
	}
	if got, want := ring.IdentityPseudonym("203.0.113.7"), "7797cbf288501ac3"; got != want {
		t.Errorf("IdentityPseudonym(203.0.113.7) = %q, want %q", got, want)
	}
	if got, want := ring.ItemPseudonym("10.1101/2020.01.01.900000"), "9b3879e55fb8d4a1"; got != want {
		t.Errorf("ItemPseudonym(10.1101/2020.01.01.900000) = %q, want %q", got, want)
	}
}

// fips140HelperEnv marks the copy of this test binary that
// TestNewKeyring_UnderFIPS140Only_RefusesAShortSecretWithAnError starts under
// GODEBUG=fips140=only. It is a marker for the test process and nothing else.
const fips140HelperEnv = "TELEMETRY_KEYRING_FIPS140_HELPER"

// TestNewKeyring_UnderFIPS140Only_RefusesAShortSecretWithAnError covers the
// one refusal HKDF has for an input expand can be given: a secret shorter than
// 112 bits under GODEBUG=fips140=only.
//
// The standard library returns it as an error, which NewKeyring hands to its
// caller and the caller turns into a startup error naming the variable.
// golang.org/x/crypto/hkdf, which derived these keys before, panicked on the
// same input, so a FIPS-only deployment with a short configured secret crashed
// instead. The mode is read once when the process starts, so the assertions
// run in a copy of this test binary started with it set, and the copy also
// shows that a 112-bit secret and a generated key are accepted.
func TestNewKeyring_UnderFIPS140Only_RefusesAShortSecretWithAnError(t *testing.T) {
	if os.Getenv(fips140HelperEnv) == "1" {
		if !fips140.Enforced() {
			t.Fatal("the helper runs without FIPS 140-only enforcement; GODEBUG did not reach it")
		}
		_, short := NewKeyring("13 bytes long", 0)
		if short == nil || !strings.Contains(short.Error(), "112 bits") {
			t.Errorf("NewKeyring with a 104-bit secret under fips140=only = %v, want the refusal naming 112 bits", short)
		}
		if _, enough := NewKeyring("fourteen bytes", 0); enough != nil {
			t.Errorf("NewKeyring refused a 112-bit secret under fips140=only: %v", enough)
		}
		if _, generated := NewKeyring("", 0); generated != nil {
			t.Errorf("NewKeyring refused a generated key under fips140=only: %v", generated)
		}
		return
	}

	t.Parallel()

	// #nosec G204 G702 -- the program is this test binary, started again under a GODEBUG the running process cannot take on
	cmd := exec.CommandContext(t.Context(), os.Args[0], "-test.run=^"+t.Name()+"$", "-test.count=1", "-test.v")
	// A binary built for coverage writes its counters where GOCOVERDIR says,
	// and warns on its output when nothing says.
	cmd.Env = append(os.Environ(), "GODEBUG=fips140=only", fips140HelperEnv+"=1", "GOCOVERDIR="+t.TempDir())
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("the fips140=only helper failed: %v\n%s", err, out)
	}
	// A pattern that matched nothing would exit 0 too, having asserted
	// nothing, so the helper has to be seen passing by name.
	if !bytes.Contains(out, []byte("--- PASS: "+t.Name())) {
		t.Fatalf("the fips140=only helper did not run the test:\n%s", out)
	}
}

// withReadRandom replaces the randomness seam for the duration of the test.
//
// A test using it or [withDeriveKey] must not run in parallel: the seam is a
// package variable every keyring reads.
func withReadRandom(t *testing.T, read func([]byte) (int, error)) {
	t.Helper()

	previous := readRandom
	readRandom = read
	t.Cleanup(func() { readRandom = previous })
}

// withDeriveKey replaces the HKDF seam for the duration of the test, failing
// every derivation whose info string is refuse and passing the rest through.
func withDeriveKey(t *testing.T, refuse string) {
	t.Helper()

	previous := deriveKey
	deriveKey = func(h func() hash.Hash, secret, salt []byte, info string, length int) ([]byte, error) {
		if info == refuse {
			return nil, errTestDerivation
		}
		return previous(h, secret, salt, info, length)
	}
	t.Cleanup(func() { deriveKey = previous })
}

// errTestDerivation is the failure the seams inject.
var errTestDerivation = errors.New("injected derivation failure")

// TestNewKeyring_AConfiguredSecretThatCannotBeDerived_IsRefused covers each of
// the two derivations failing, since a keyring holding one key and not the
// other would pseudonymize callers while silently recording no items, or the
// reverse.
func TestNewKeyring_AConfiguredSecretThatCannotBeDerived_IsRefused(t *testing.T) {
	for _, info := range []string{identityKeyInfo, itemKeyInfo} {
		t.Run(info, func(t *testing.T) {
			withDeriveKey(t, info)

			ring, err := NewKeyring("a deployment-wide secret", 0)
			if !errors.Is(err, errTestDerivation) {
				t.Fatalf("NewKeyring = %v, want the derivation failure", err)
			}
			if ring != nil {
				t.Errorf("NewKeyring returned a keyring beside the error: %+v", ring)
			}
			if !strings.Contains(err.Error(), info) {
				t.Errorf("the error %q does not name the key that failed, %q", err, info)
			}
		})
	}
}

// TestNewKeyring_AGeneratedKeyThatCannotBeMade_IsRefused covers the generated
// path's two failures: no randomness, and randomness HKDF would not take.
func TestNewKeyring_AGeneratedKeyThatCannotBeMade_IsRefused(t *testing.T) {
	t.Run("randomness", func(t *testing.T) {
		withReadRandom(t, func([]byte) (int, error) { return 0, errTestDerivation })

		ring, err := NewKeyring("", 0)
		if !errors.Is(err, errTestDerivation) || ring != nil {
			t.Fatalf("NewKeyring = %v, %v; want no keyring and the randomness failure", ring, err)
		}
		if !strings.Contains(err.Error(), "generating the pseudonymisation key") {
			t.Errorf("the error %q does not say it was generating the key", err)
		}
	})

	t.Run("derivation", func(t *testing.T) {
		withDeriveKey(t, identityKeyInfo)

		ring, err := NewKeyring("", 0)
		if !errors.Is(err, errTestDerivation) || ring != nil {
			t.Fatalf("NewKeyring = %v, %v; want no keyring and the derivation failure", ring, err)
		}
	})
}

// TestKeyring_ARotationThatFails_RecordsNothingUntilOneSucceeds covers the
// rotation branch of digest when the new key cannot be made.
//
// Carrying on with the expired key would hold a pseudonym past the life the
// operator asked for, so the answer is nothing, for both pseudonyms. And the
// failure must not stick: the instant of the last rotation is not moved, so
// the next call is still due and tries again, and a keyring whose randomness
// comes back records again.
func TestKeyring_ARotationThatFails_RecordsNothingUntilOneSucceeds(t *testing.T) {
	ring, err := NewKeyring("", time.Hour)
	if err != nil {
		t.Fatalf("NewKeyring: %v", err)
	}
	clock := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	ring.mu.Lock()
	ring.now = func() time.Time { return clock }
	ring.rotatedAt = clock
	ring.mu.Unlock()
	before := ring.IdentityPseudonym("203.0.113.7")

	clock = clock.Add(2 * time.Hour)
	failing := true
	withReadRandom(t, func(b []byte) (int, error) {
		if failing {
			return 0, errTestDerivation
		}
		return rand.Read(b)
	})

	if got := ring.IdentityPseudonym("203.0.113.7"); got != "" {
		t.Errorf("IdentityPseudonym after a failed rotation = %q, want nothing", got)
	}
	if got := ring.ItemPseudonym("10.1101/2020.01.01.900000"); got != "" {
		t.Errorf("ItemPseudonym after a failed rotation = %q, want nothing", got)
	}

	failing = false
	after := ring.IdentityPseudonym("203.0.113.7")
	if after == "" || after == before {
		t.Errorf("IdentityPseudonym once randomness returned = %q (before the rotation %q), want a new pseudonym", after, before)
	}
}
