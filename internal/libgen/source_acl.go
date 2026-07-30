package libgen

import (
	"context"
	"fmt"
	"strings"
)

// aclDocRoot is the default ACL Anthology document root; the source appends
// /<id>.pdf to it. It is overridable per source so tests can assert the built URL
// against a local base.
const aclDocRoot = "https://aclanthology.org"

// aclDOIPrefixes are the two DOI registrant prefixes the Association for
// Computational Linguistics publishes Anthology papers under. 10.18653 carries
// everything from 2015 onward plus the retro-registered older venues; 10.3115 is
// the ACL's original prefix, reused for the Anthology under the same "/v1/" shape.
var aclDOIPrefixes = []string{"10.18653/", "10.3115/"}

// aclVersionSegment is the segment an Anthology DOI's suffix opens with, ahead of
// the Anthology identifier itself.
//
// Requiring it is what separates the DOIs this source can serve from the ones it
// cannot, and the distinction is load-bearing rather than cosmetic. Only the
// "/v1/" shape embeds the Anthology id: 10.3115/v1/P14-1001 resolves to
// aclanthology.org/P14-1001, whereas the pre-2014 numeric form
// 10.3115/1072228.1072256 resolves to portal.acm.org and its suffix is an ACM
// article number that names no Anthology document at all. Those are declined here
// rather than turned into a URL that cannot exist.
const aclVersionSegment = "v1/"

// aclSource resolves an ACL Anthology paper's DOI to its PDF at aclanthology.org.
//
// The Anthology is openly licensed (CC BY for material from 2016 on, CC BY-NC-SA
// before that) and served without an account, so this is a legal open-access
// source. It covers ACL, EMNLP, NAACL, EACL, COLING, CoNLL, TACL and the workshop
// proceedings — the primary literature of computational linguistics.
//
// Resolve issues no request of its own: an Anthology DOI's suffix *is* the
// Anthology identifier, and the identifier *is* the filename, so the PDF URL
// follows from the DOI by string manipulation alone. That makes this the cheapest
// source in the chain — no lookup, no landing page, no redirect.
//
// Honest overlap caveat: Unpaywall reaches most of these too. Of thirteen real
// Anthology DOIs sampled, twelve carried a usable url_for_pdf in their Unpaywall
// record. The gain is therefore not raw coverage but keyless-default reach — the
// unpaywall source is gated on LIBGEN_MCP_UNPAYWALL_EMAIL and is absent from the
// default chain entirely, so without this source a stock configuration reaches no
// Anthology paper at all. The thirteenth case is also instructive: 10.18653/v1/
// N19-1423 (BERT, the most-cited paper in the field) is reported is_oa=true by
// Unpaywall with zero url_for_pdf across every oa_location, so even a configured
// Unpaywall cannot serve it, while aclanthology.org/N19-1423.pdf returns 786,279
// bytes of PDF.
//
// MD5 verification is disabled, as for every DOI-keyed item.
type aclSource struct {
	// docRoot overrides the Anthology document root (defaults to aclDocRoot); tests
	// set it to assert the built URL without reaching the network.
	docRoot string
}

// Compile-time assertion that aclSource satisfies the DownloadSource contract.
var _ DownloadSource = aclSource{}

// Name identifies the ACL Anthology source.
func (s aclSource) Name() string { return "acl" }

// Supports reports that this source can resolve a DOI-keyed item whose DOI is a
// well-formed ACL Anthology DOI, and only such items. A DOI under one of the ACL
// prefixes that does not carry the "/v1/" segment — the pre-2014 numeric ACM form —
// is refused here rather than resolved into a URL that cannot exist.
func (s aclSource) Supports(it Item) bool {
	_, ok := aclAnthologyID(it.DOI)
	return ok
}

// Resolve returns the paper's PDF URL in the ACL Anthology.
func (s aclSource) Resolve(_ context.Context, it Item) (Resolved, error) {
	id, ok := aclAnthologyID(it.DOI)
	if !ok {
		return Resolved{}, notIndexed(fmt.Errorf("acl: %q is not an ACL Anthology DOI", it.DOI))
	}
	root := s.docRoot
	if root == "" {
		root = aclDocRoot
	}
	return Resolved{
		FileURL:   strings.TrimRight(root, "/") + "/" + id + ".pdf",
		VerifyMD5: false,
		Ext:       "pdf",
	}, nil
}

// aclAnthologyID extracts the Anthology identifier from an ACL DOI, reporting
// whether the DOI is one at all. It requires one of the registrant prefixes, the
// "/v1/" segment, and an identifier made only of characters an Anthology id uses,
// so a DOI naming something else under the same prefix is rejected rather than
// turned into a guess.
//
// The returned id carries the case aclanthology.org serves it under, which is not
// the case the DOI necessarily arrives in. A DOI is case-insensitive by
// specification, but the Anthology's filenames are not: the volume-lettered
// identifiers are uppercase and answer only to that spelling
// (aclanthology.org/N19-1423.pdf is 200 with 786,279 bytes, n19-1423.pdf is 404),
// while the year-prefixed modern identifiers are lowercase and equally strict
// (2024.emnlp-main.856.pdf is 200 with 1,068,216 bytes, 2024.EMNLP-MAIN.856.pdf is
// 404). Uppercasing unconditionally would break every paper since 2020 and
// lowercasing unconditionally every paper before it, so the case is normalized on
// the one bit that distinguishes the two id generations: an identifier opening
// with a letter is a volume-lettered id and is uppercased, one opening with a
// digit is a year-prefixed id and is left exactly as given.
func aclAnthologyID(doi string) (string, bool) {
	suffix, ok := aclDOISuffix(doi)
	if !ok {
		return "", false
	}
	id := strings.TrimPrefix(suffix, aclVersionSegment)
	if id == "" || !aclWellFormedID(id) {
		return "", false
	}
	if isASCIILetter(rune(id[0])) {
		return strings.ToUpper(id), true
	}
	return id, true
}

// aclDOISuffix returns the DOI's suffix when it carries one of the ACL registrant
// prefixes followed by the "/v1/" segment, reporting whether it did.
func aclDOISuffix(doi string) (string, bool) {
	for _, prefix := range aclDOIPrefixes {
		if !strings.HasPrefix(doi, prefix) {
			continue
		}
		suffix := strings.TrimPrefix(doi, prefix)
		if strings.HasPrefix(suffix, aclVersionSegment) {
			return suffix, true
		}
	}
	return "", false
}

// aclWellFormedID reports whether id looks like an Anthology identifier: ASCII
// letters, digits, dots and hyphens only, and no path separator.
//
// It is a guard on what goes into a URL rather than a full grammar. The two id
// generations ("N19-1423", "2024.emnlp-main.856") share this character set, and
// rejecting anything outside it means no caller-supplied DOI suffix can escape the
// document root into another path — which matters because this is the one source
// that builds its file URL from caller input with no lookup in between.
func aclWellFormedID(id string) bool {
	for _, r := range id {
		switch {
		case isASCIILetter(r), r >= '0' && r <= '9', r == '.', r == '-':
		default:
			return false
		}
	}
	return true
}

// isASCIILetter reports whether r is an unaccented A–Z or a–z, the only letters an
// Anthology identifier uses.
func isASCIILetter(r rune) bool {
	return (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z')
}
