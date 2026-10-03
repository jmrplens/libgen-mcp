// The PMC Article Datasets bucket the europepmc source takes its PDF from.

package libgen

import (
	"context"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"

	"github.com/jmrplens/libgen-mcp/v2/internal/netguard"
)

// pmcOABucketBase is the anonymous HTTPS root of the PMC Article Datasets that
// NCBI publishes on AWS Open Data (https://pmc.ncbi.nlm.nih.gov/tools/pmcaws/).
// It is world-readable and keyless by design, and NCBI's own README documents
// reading it over plain HTTPS. It is a default for europePMCSource.bucketBase so
// tests can point the source at an httptest server.
const pmcOABucketBase = "https://pmc-oa-opendata.s3.amazonaws.com"

// pmcOABucketName is the bucket the metadata's s3:// URLs name. A pdf_url naming
// any other bucket is not followed: the metadata is read from this bucket, and an
// address it hands back is only trusted inside it.
const pmcOABucketName = "pmc-oa-opendata"

// pmcOAMaxVersions caps how many article versions are inspected. NCBI says most
// articles have one, and the ones with more are an author manuscript beside the
// published version, so a cap keeps a pathological listing from costing a
// metadata request per entry.
const pmcOAMaxVersions = 4

// pmcOAMaxListBody bounds a bucket listing. A listing of one article's versions is
// a few hundred bytes, so this is generous and still refuses a hostile body.
const pmcOAMaxListBody = 64 << 10

// pmcOAMaxMetaBody bounds an article version's metadata JSON, which grows with the
// number of supplementary media files it lists.
const pmcOAMaxMetaBody = 1 << 20

// pmcOAListing is the subset of an S3 ListObjectsV2 response read here: with a
// "/" delimiter, each article version under the PMCID comes back as one common
// prefix such as "PMC4991899.1/".
type pmcOAListing struct {
	// CommonPrefixes holds one entry per article version.
	CommonPrefixes []struct {
		// Prefix is the version's key prefix, trailing slash included.
		Prefix string `xml:"Prefix"`
	} `xml:"CommonPrefixes"`
}

// pmcOAMeta is the subset of an article version's metadata JSON read here. The
// README beside the bucket documents every field.
type pmcOAMeta struct {
	// IsManuscript marks an author manuscript rather than the published version.
	IsManuscript bool `json:"is_manuscript"`
	// IsRetracted marks a retracted version. It is not reliable on its own: it
	// was measured false on a version titled "RETRACTED ARTICLE", which is why
	// Resolve also reads Europe PMC's publication type.
	IsRetracted bool `json:"is_retracted"`
	// IsPMCOpenAccess marks a version in the PMC Open Access Subset. A version
	// outside it (an author manuscript released for text mining only) is not
	// served, so the open-access claim holds for the file actually returned.
	IsPMCOpenAccess bool `json:"is_pmc_openaccess"`
	// PDFURL is the s3:// address of the PDF, present only when the publisher's
	// license lets PMC distribute one.
	PDFURL string `json:"pdf_url"`
}

// pdfURL returns a live URL for the article's PDF in the PMC Article Datasets.
//
// Europe PMC's own PDF routes (/backend/ptpmcrender.fcgi and
// /articles/<pmcid>?pdf=render) answer every automated client with a Cloudflare
// challenge since 2026-10, so the bytes are taken from the dataset NCBI publishes
// for exactly this kind of reuse instead. Only a version in the PMC Open Access
// Subset is offered, the published version is preferred over an author
// manuscript, and an article any of whose versions is flagged retracted is not
// offered at all (pmcOACandidates).
//
// An article the dataset does not hold, holds without an open-access PDF, or
// flags as retracted is a clean miss: each is an answer about this article. A
// bucket that lists a PDF and then does not serve it is the service failing,
// which is unavailability.
func (s europePMCSource) pdfURL(ctx context.Context, pmcid string) (string, error) {
	versions, err := s.pmcOAVersions(ctx, pmcid)
	if err != nil {
		return "", err
	}
	if len(versions) == 0 {
		return "", notIndexed(fmt.Errorf("europepmc: the PMC open-access dataset holds no copy of %s", pmcid))
	}
	candidates, err := s.pmcOACandidates(ctx, pmcid, versions)
	if err != nil {
		return "", err
	}
	if len(candidates) == 0 {
		return "", notIndexed(fmt.Errorf("europepmc: the PMC open-access dataset holds no PDF of %s", pmcid))
	}
	for _, c := range candidates {
		if probePDF(ctx, s.client(), c) {
			return c, nil
		}
	}
	return "", unavailable(fmt.Errorf("europepmc: the PDF of %s is listed but not served", pmcid))
}

// pmcOAVersions lists the article-version prefixes the bucket holds for pmcid,
// highest version number first, without their trailing slash.
//
// The listing prefix ends in a dot so PMC123 does not also match PMC1234, and
// every returned prefix is checked to be exactly "<pmcid>.<number>/" before it is
// used to build a key.
func (s europePMCSource) pmcOAVersions(ctx context.Context, pmcid string) ([]string, error) {
	q := url.Values{"list-type": {"2"}, "prefix": {pmcid + "."}, "delimiter": {"/"}}
	endpoint := s.bucket() + "/?" + q.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, http.NoBody)
	if err != nil {
		return nil, fmt.Errorf("europepmc: building listing for %s: %w", pmcid, netguard.RedactTransportError(err))
	}
	req.Header.Set("User-Agent", userAgent())

	resp, err := s.client().Do(req)
	if err != nil {
		return nil, unavailable(fmt.Errorf("europepmc: listing %s: %w", pmcid, netguard.RedactTransportError(err)))
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, unavailableStatus(resp.StatusCode, fmt.Errorf("europepmc: listing %s returned HTTP %d", pmcid, resp.StatusCode))
	}

	var listing pmcOAListing
	if decErr := xml.NewDecoder(io.LimitReader(resp.Body, pmcOAMaxListBody)).Decode(&listing); decErr != nil {
		return nil, fmt.Errorf("europepmc: decoding listing for %s: %w", pmcid, decErr)
	}
	return pmcOAVersionPrefixes(pmcid, listing), nil
}

// pmcOAVersionPrefixes keeps the listing's well-formed version prefixes for
// pmcid, sorted by version number descending and capped at pmcOAMaxVersions.
func pmcOAVersionPrefixes(pmcid string, listing pmcOAListing) []string {
	type version struct {
		prefix string
		n      int
	}
	var found []version
	for _, cp := range listing.CommonPrefixes {
		prefix := strings.TrimSuffix(cp.Prefix, "/")
		n, err := strconv.Atoi(strings.TrimPrefix(prefix, pmcid+"."))
		if err != nil || n <= 0 || prefix != pmcid+"."+strconv.Itoa(n) {
			continue
		}
		found = append(found, version{prefix: prefix, n: n})
	}
	sort.Slice(found, func(i, j int) bool { return found[i].n > found[j].n })
	if len(found) > pmcOAMaxVersions {
		found = found[:pmcOAMaxVersions]
	}
	out := make([]string, len(found))
	for i, v := range found {
		out[i] = v.prefix
	}
	return out
}

// pmcOAVersionMeta is one version's prefix together with its metadata.
type pmcOAVersionMeta struct {
	// version is the version prefix, such as "PMC4991899.1".
	version string
	// meta is what the version's metadata JSON says about it.
	meta pmcOAMeta
}

// pmcOACandidates reads each version's metadata and returns the HTTPS URLs of the
// PDFs worth offering: published versions first, then author manuscripts, each
// group in the order the versions were given (highest number first, which is the
// newest when the journal itself versions the article).
//
// A version outside the PMC Open Access Subset or without a PDF contributes
// nothing. A retracted flag on any version declines the whole article, since a
// retraction is a statement about the work rather than about one file of it. A
// version whose metadata cannot be read is skipped, and that failure is returned
// only when no version yielded a candidate, so it is never mistaken for an
// answer about the article.
func (s europePMCSource) pmcOACandidates(ctx context.Context, pmcid string, versions []string) ([]string, error) {
	metas, lastErr := s.pmcOAReadVersions(ctx, versions)
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	var published, manuscripts []string
	for _, vm := range metas {
		if vm.meta.IsRetracted {
			return nil, notIndexed(fmt.Errorf("europepmc: the PMC dataset marks %s retracted, so it is not served", pmcid))
		}
		u, ok := pmcOAObjectURL(s.bucket(), vm.meta.PDFURL, vm.version)
		if !ok || !vm.meta.IsPMCOpenAccess {
			continue
		}
		if vm.meta.IsManuscript {
			manuscripts = append(manuscripts, u)
		} else {
			published = append(published, u)
		}
	}
	published = append(published, manuscripts...)
	if len(published) == 0 && lastErr != nil {
		return nil, lastErr
	}
	return published, nil
}

// pmcOAReadVersions fetches the metadata of every version it can, returning the
// ones it read and the last failure among the ones it could not.
func (s europePMCSource) pmcOAReadVersions(ctx context.Context, versions []string) ([]pmcOAVersionMeta, error) {
	var (
		metas   []pmcOAVersionMeta
		lastErr error
	)
	for _, v := range versions {
		var meta pmcOAMeta
		fetch := jsonFetch{client: s.http, source: "europepmc", subject: v, maxBody: pmcOAMaxMetaBody}
		if err := fetch.get(ctx, s.bucket()+"/"+v+"/"+v+".json", &meta); err != nil {
			lastErr = err
			continue
		}
		metas = append(metas, pmcOAVersionMeta{version: v, meta: meta})
	}
	return metas, lastErr
}

// pmcOAObjectURL maps the metadata's s3:// PDF address onto the bucket's HTTPS
// root. It refuses an address outside this bucket, outside the version's own
// prefix, or not naming a PDF, so the metadata can only ever point the download
// at the object it describes. The md5 query parameter is dropped: the download
// pipeline verifies digests for md5-keyed items only.
func pmcOAObjectURL(base, s3URL, version string) (string, bool) {
	if s3URL == "" {
		return "", false
	}
	u, err := url.Parse(s3URL)
	if err != nil || u.Scheme != "s3" || u.Host != pmcOABucketName {
		return "", false
	}
	key := strings.TrimPrefix(u.Path, "/")
	if !strings.HasPrefix(key, version+"/") || !strings.HasSuffix(strings.ToLower(key), ".pdf") || strings.Contains(key, "..") {
		return "", false
	}
	return base + "/" + (&url.URL{Path: key}).EscapedPath(), true
}

// bucket returns the configured bucket root, or the production one, without a
// trailing slash.
func (s europePMCSource) bucket() string {
	base := s.bucketBase
	if base == "" {
		base = pmcOABucketBase
	}
	return strings.TrimRight(base, "/")
}
