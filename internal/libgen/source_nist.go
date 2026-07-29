package libgen

import (
	"context"
	"fmt"
	"strings"
)

// nistDOIBase is the default DOI resolver root used to reach a NIST publication.
// It is overridable per source so tests can assert the built URL against a local
// base.
const nistDOIBase = "https://doi.org"

// nistDOIPrefix is the DOI registrant prefix assigned to the National Institute
// of Standards and Technology; the source claims only DOIs under it.
const nistDOIPrefix = "10.6028/"

// nistSource resolves a NIST publication's DOI to its PDF in NIST's own
// repository, nvlpubs.nist.gov.
//
// NIST publications are US government works served without an account, so this is
// a legal open-access source covering the Special Publication series (including
// the 800-series security standards), FIPS, Internal/Interagency Reports,
// Technical Notes and the Cybersecurity White Papers.
//
// Resolve hands over the DOI resolver URL and lets the download pipeline follow
// the redirect chain, because a NIST DOI redirects all the way to the file:
// https://doi.org/10.6028/NIST.SP.800-53r5 ends at
// nvlpubs.nist.gov/nistpubs/SpecialPublications/NIST.SP.800-53r5.pdf serving
// application/pdf. That is what keeps this source small — the repository path is
// otherwise not derivable from the DOI, since the IR series is partitioned by
// publication year (/nistpubs/ir/2020/NIST.IR.8259.pdf is served, the same
// filename under 2019/ is not) and the DOI carries no year. Resolving through
// doi.org needs neither a series→path table nor a year lookup, and it keeps
// working when NIST reorganizes the repository.
//
// MD5 verification is disabled, as for every DOI-keyed item.
type nistSource struct {
	// doiBase overrides the DOI resolver root (defaults to nistDOIBase); tests set
	// it to assert the built URL without reaching the network.
	doiBase string
}

// Compile-time assertion that nistSource satisfies the DownloadSource contract.
var _ DownloadSource = nistSource{}

// Name identifies the NIST source.
func (s nistSource) Name() string { return "nist" }

// Supports reports that this source can resolve a DOI-keyed item whose DOI carries
// the NIST registrant prefix, and only such items.
//
// The whole prefix is claimed rather than only the suffixes beginning "NIST.",
// because the Journal of Research of NIST shares the prefix and resolves to a PDF
// the same way (10.6028/jres.121.004 ends at
// nvlpubs.nist.gov/nistpubs/jres/121/jres.121.004.pdf). Narrowing the check would
// drop those articles for no gain.
func (s nistSource) Supports(it Item) bool {
	return it.DOI != "" && strings.HasPrefix(it.DOI, nistDOIPrefix)
}

// Resolve returns the DOI resolver URL for the publication, which redirects to its
// PDF in NIST's repository.
//
// No HEAD pre-check is made first: nvlpubs answers 404 to HEAD on a URL whose GET
// serves the file, so pre-checking would report every NIST publication as missing.
// The download's own GET is therefore the existence check, and a DOI NIST does not
// hold fails there — which costs nothing, since no other source in the chain
// claims this prefix.
func (s nistSource) Resolve(_ context.Context, it Item) (Resolved, error) {
	if !s.Supports(it) {
		return Resolved{}, notIndexed(fmt.Errorf("nist: %q is not a NIST DOI", it.DOI))
	}
	base := s.doiBase
	if base == "" {
		base = nistDOIBase
	}
	return Resolved{
		// The DOI's slashes stay literal in the path (the resolver keys by the
		// unescaped DOI); escapeDOIPath encodes only other URL-unsafe characters.
		FileURL:   strings.TrimRight(base, "/") + "/" + escapeDOIPath(it.DOI),
		VerifyMD5: false,
		Ext:       "pdf",
	}, nil
}
