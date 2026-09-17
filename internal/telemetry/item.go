// item.go decides how much a signal may say about *what* a call was about.
//
// It is the counterpart of identity.go, and on this server it is the more
// important of the two. Who asked is an address; what they asked for is a book
// or a paper, and that is the value somebody would be harmed by having
// published.

package telemetry

import (
	"go.opentelemetry.io/otel/attribute"
)

const (
	// AttrItemID carries an item identifier exactly — an md5, a DOI or an ISBN
	// — so it is only ever set under [IdentityFull].
	AttrItemID = "libgen_mcp.item.id"

	// AttrItemRef is the same item named indirectly, as a keyed digest. It is
	// deliberately a different key: a consumer reading libgen_mcp.item.id is
	// entitled to find an identifier there, and putting a digest under that name
	// would be a lie told in the schema rather than in prose.
	AttrItemRef = "libgen_mcp.item.ref"
)

// ItemRedactor decides how much a signal may say about which item a call named.
//
// # Why this follows the identity policy
//
// An operator has already made this decision once, by choosing an identity
// policy. Asking them to make it twice, through a second setting with its own
// default, produces deployments where the two disagree and nobody meant them to.
//
// So the mapping is the one the policy already implies:
//
//	none          a keyed digest: correlates every attempt at one item,
//	              names none of them
//	pseudonymous  the same digest, for the same reason it gives a caller one
//	full          the identifier itself, beside the client.address that policy
//	              already records
//
// # Why the digest is the floor rather than nothing
//
// This is where the item treatment differs from the identity treatment, and the
// difference is deliberate. A caller digest still follows a person, so recording
// none of it is a meaningful default. An item digest follows a book, and without
// it "this one item fails on every source" and "every download is failing" are
// the same picture — two conditions with opposite responses, since the first is
// a dead item and the second is a dead mirror.
//
// # What is never recorded, under any policy
//
// The search query and the record title. There is no setting that turns them on,
// because there is no operator for whom a collector holding "what this person
// searched for" is the right outcome. They are stripped by name on the export
// leg as well — see [ExportStrippedFields] — so a record that starts carrying
// one later is covered without anybody remembering.
type ItemRedactor struct {
	policy IdentityPolicy
	// keys is the same keyring the identity redactor uses, so one operator
	// secret governs both pseudonyms and neither can outlive the other.
	keys *Keyring
}

// NewItemRedactor builds the redactor for a policy.
func NewItemRedactor(policy IdentityPolicy, keys *Keyring) *ItemRedactor {
	return &ItemRedactor{policy: policy, keys: keys}
}

// Policy reports what this redactor was built for.
func (r *ItemRedactor) Policy() IdentityPolicy {
	if r == nil || r.policy == "" {
		return DefaultIdentityPolicy
	}
	return r.policy
}

// ItemAttributes returns what may be recorded about one item identifier.
//
// A nil receiver returns nothing, so a caller that never wired one records
// nothing rather than panicking, and an empty identifier returns nothing, so an
// absent value is absent rather than a digest of the empty string that would
// look like a real item.
func (r *ItemRedactor) ItemAttributes(identifier string) []attribute.KeyValue {
	if r == nil || identifier == "" {
		return nil
	}
	if r.Policy() == IdentityFull {
		return []attribute.KeyValue{attribute.String(AttrItemID, identifier)}
	}
	digest := r.keys.ItemPseudonym(identifier)
	if digest == "" {
		return nil
	}
	return []attribute.KeyValue{attribute.String(AttrItemRef, digest)}
}
