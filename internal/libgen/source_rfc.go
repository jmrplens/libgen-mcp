package libgen

import (
	"context"
	"fmt"
	"strconv"
	"strings"
)

// rfcDocRoot is the default RFC Editor document root; the source appends
// /rfc<number>.txt to it. It is overridable per source so tests can assert the
// built URL against a local base.
const rfcDocRoot = "https://www.rfc-editor.org/rfc"

// rfcDOIPrefix is the DOI registrant prefix the RFC Editor assigns to published
// RFCs; the source claims only DOIs under it. Registered RFC DOIs are real: for
// example 10.17487/RFC9110 is "HTTP Semantics", publisher RFC Editor.
const rfcDOIPrefix = "10.17487/"

// rfcDOIToken is the token an RFC DOI's suffix opens with, ahead of the RFC
// number. It is matched case-insensitively because a DOI is case-insensitive by
// specification, so a caller may reasonably pass 10.17487/rfc9110.
const rfcDOIToken = "rfc"

// rfcSource resolves a published RFC's DOI to its full text at the RFC Editor.
//
// RFCs are free to read and redistribute, so this is a legal open-access source,
// and it fills a real hole: every other DOI source in the chain misses an RFC.
// Unpaywall reports one as not open access, Europe PMC does not index it, fatcat
// has no preserved copy, CORE and OAPEN hold nothing, and neither Sci-Hub nor
// SciDB embeds a PDF — while the RFC Editor has served the document since
// publication.
//
// Resolve issues no request of its own: the document URL follows from the DOI's
// own suffix, and unlike the ERIC case (see docs/decisions), the DOI is already a
// first-class key of both download and read, so there is nothing for a caller to
// fetch by hand instead. MD5 verification is disabled, as for every DOI-keyed
// item.
type rfcSource struct {
	// docRoot overrides the RFC Editor document root (defaults to rfcDocRoot);
	// tests set it to assert the built URL without reaching the network.
	docRoot string
}

// Compile-time assertion that rfcSource satisfies the DownloadSource contract.
var _ DownloadSource = rfcSource{}

// Name identifies the RFC Editor source.
func (s rfcSource) Name() string { return "rfc" }

// Supports reports that this source can resolve a DOI-keyed item whose DOI is a
// well-formed RFC DOI, and only such items. A 10.17487 DOI whose suffix is not
// rfc<number> is refused here rather than resolved into a URL that cannot exist.
func (s rfcSource) Supports(it Item) bool {
	_, ok := rfcNumber(it.DOI)
	return ok
}

// Resolve returns the RFC's plain-text URL at the RFC Editor.
//
// The text format is chosen over PDF deliberately. PDF coverage is irregular —
// rfc9110.pdf, rfc2616.pdf, rfc9000.pdf and rfc9457.pdf all serve real bytes
// while rfc1.pdf, rfc791.pdf and rfc8446.pdf answer 404 — whereas .txt answered
// for every RFC sampled from 1 (1969) to 9457. It is also the canonical RFC
// format and the smaller file, and extract reads it. Probing PDF first and
// falling back would add a request to every download and leave the format the
// caller receives varying from one RFC to the next.
func (s rfcSource) Resolve(_ context.Context, it Item) (Resolved, error) {
	number, ok := rfcNumber(it.DOI)
	if !ok {
		return Resolved{}, notIndexed(fmt.Errorf("rfc: %q is not an RFC DOI", it.DOI))
	}
	root := s.docRoot
	if root == "" {
		root = rfcDocRoot
	}
	return Resolved{
		FileURL:   fmt.Sprintf("%s/%s%d.txt", strings.TrimRight(root, "/"), rfcDOIToken, number),
		VerifyMD5: false,
		Ext:       "txt",
	}, nil
}

// rfcNumber extracts the RFC number from an RFC DOI, reporting whether the DOI is
// one at all. It requires the registrant prefix, the rfc token and a positive
// decimal number with nothing trailing, so a DOI naming something else under the
// same prefix is rejected rather than turned into a guess.
//
// The number is returned parsed rather than as the literal suffix text so that
// any zero padding normalizes away: the RFC Editor's filenames are unpadded
// (rfc791.txt), and while the registered DOIs are unpadded too — 10.17487/RFC791
// resolves and 10.17487/RFC0791 does not exist — a padded spelling from a caller
// still lands on the right document.
func rfcNumber(doi string) (int, bool) {
	if !strings.HasPrefix(doi, rfcDOIPrefix) {
		return 0, false
	}
	suffix := strings.TrimPrefix(doi, rfcDOIPrefix)
	if len(suffix) <= len(rfcDOIToken) || !strings.EqualFold(suffix[:len(rfcDOIToken)], rfcDOIToken) {
		return 0, false
	}
	number, err := strconv.Atoi(suffix[len(rfcDOIToken):])
	if err != nil || number <= 0 {
		return 0, false
	}
	return number, true
}
