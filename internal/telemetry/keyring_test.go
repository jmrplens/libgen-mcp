package telemetry

import (
	"bytes"
	"crypto/fips140"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"hash"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	xhkdf "golang.org/x/crypto/hkdf"
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

// TestExpand_AgreesWithThePreviousDerivation holds the standard library's HKDF
// to golang.org/x/crypto/hkdf, which derived these keys through 2.1.0, on the
// inputs expand is actually given.
//
// Agreement is the whole requirement. A configured secret is how several
// replicas, and one deployment across restarts, give a caller one pseudonym;
// a derivation that moved by one bit would split every caller in two on the
// day of the upgrade, and a distinct-user count would double without anybody
// having changed anything. The oracle is the previous code verbatim, so this
// test fails if either side ever stops producing the other's bytes.
func TestExpand_AgreesWithThePreviousDerivation(t *testing.T) {
	t.Parallel()

	for _, info := range []string{identityKeyInfo, itemKeyInfo, ""} {
		t.Run(info, func(t *testing.T) {
			t.Parallel()

			for _, secret := range derivationInputs() {
				want := make([]byte, identitySaltBytes)
				if _, err := xhkdf.New(sha256.New, secret, nil, []byte(info)).Read(want); err != nil {
					t.Fatalf("the previous derivation failed on %x: %v", secret, err)
				}
				got, err := expand(secret, info)
				if err != nil {
					t.Fatalf("expand(%x): %v", secret, err)
				}
				if !bytes.Equal(got, want) {
					t.Errorf("expand(%x, %q) = %x, the previous derivation gave %x", secret, info, got, want)
				}
			}
		})
	}
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
// The oracle test above compares the two HKDF implementations; this one fixes
// the chain around them too (the info strings, the key length, the HMAC and
// its truncation), because a pseudonym already sitting in a dashboard depends
// on every one of them. The values were computed by the golang.org/x/crypto
// derivation on the commit before this change and agree with an independent
// HKDF and HMAC-SHA256 written from RFC 5869 in another language.
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
