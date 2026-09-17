package telemetry

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
)

// This file is the computation. Where the keys come from and how long they
// live is [Keyring], in keyring.go.

// IdentityPseudonym returns the digest naming a caller.
func (k *Keyring) IdentityPseudonym(userID string) string {
	return k.digest(userID, false)
}

// ItemPseudonym returns the digest naming the thing a call was about: an md5, a
// DOI or an ISBN.
//
// # Why a digest rather than the identifier
//
// On this server the sensitive value is not who asked but *what was asked for*.
// A DOI names a paper, an md5 names a specific copy of a specific book, and a
// search query names an interest — which is why the query and the title are
// never exported under any policy and no setting turns them on. The identifier
// is the one item-shaped value that is worth keeping, and a digest is how it is
// kept without being published.
//
// # Why a digest rather than nothing
//
// Dropping it entirely would leave an operator unable to tell two failures
// apart, so "this one book fails on every source" and "every download is
// failing" would look identical — and those two have opposite responses: the
// first is a dead item, the second is a dead mirror.
//
// # What the digest does not protect against
//
// This is pseudonymity, not anonymity, and the gap is wider here than for a
// caller. A DOI space is enumerable and an md5 of a public book is public, so
// **anyone holding the key and a catalog can reverse a digest by trying it**.
// Without the key there is nothing to try it against, which is the whole of what
// the keyring buys. It is stated here, and in the documentation, rather than
// left to be discovered.
func (k *Keyring) ItemPseudonym(identifier string) string {
	return k.digest(identifier, true)
}

// digest is the one computation, so the two pseudonyms cannot drift apart in
// length, algorithm, or rotation behavior.
//
// Sixteen hex characters is 64 bits: past collision concern for the number of
// callers or items one deployment sees, and short enough to read in a trace
// viewer.
func (k *Keyring) digest(value string, forItem bool) string {
	if k == nil || value == "" {
		return ""
	}

	k.mu.Lock()
	if k.due() {
		if err := k.generate(); err != nil {
			// A process that cannot read randomness cannot pseudonymize, and
			// continuing with the expired key would hold a pseudonym past the
			// life the operator asked for. Emitting nothing is the honest
			// answer; falling back to an unkeyed digest would look like a
			// pseudonym while being reversible by anyone.
			k.identity, k.item = nil, nil
		}
	}
	key := k.identity
	if forItem {
		key = k.item
	}
	k.mu.Unlock()

	if key == nil {
		return ""
	}

	mac := hmac.New(sha256.New, key)
	_, _ = mac.Write([]byte(value))
	return hex.EncodeToString(mac.Sum(nil))[:16]
}
