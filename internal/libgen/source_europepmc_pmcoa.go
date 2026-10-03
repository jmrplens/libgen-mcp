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
	// IsRetracted marks a retracted version, which is never served.
	IsRetracted bool `json:"is_retracted"`
	// PDFURL is the s3:// address of the PDF, present only when the publisher's
	// license lets PMC distribute one.
	PDFURL string `json:"pdf_url"`
}

// pdfURL returns a live URL for the article's PDF in the PMC Article Datasets.
//
// Europe PMC's own PDF routes (/backend/ptpmcrender.fcgi and
// /articles/<pmcid>?pdf=render) answer every automated client with a Cloudflare
// challenge since 2026-10, so the bytes are taken from the dataset NCBI publishes
// for exactly this kind of reuse instead. The published version is preferred over
// an author manuscript, and a retracted version is never offered.
//
// An article the dataset does not hold, or holds without a PDF, is a clean miss:
// both are answers about this article. A bucket that lists a PDF and then does not
// serve it is the service failing, which is unavailability.
func (s europePMCSource) pdfURL(ctx context.Context, pmcid string) (string, error) {
	versions, err := s.pmcOAVersions(ctx, pmcid)
	if err != nil {
		return "", err
	}
	if len(versions) == 0 {
		return "", notIndexed(fmt.Errorf("europepmc: the PMC open-access dataset holds no copy of %s", pmcid))
	}
	candidates, err := s.pmcOACandidates(ctx, versions)
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

// pmcOACandidates reads each version's metadata and returns the HTTPS URLs of the
// PDFs worth offering: published versions first, then author manuscripts, each
// group in the order the versions were given. A retracted version or one without
// a PDF contributes nothing.
func (s europePMCSource) pmcOACandidates(ctx context.Context, versions []string) ([]string, error) {
	var published, manuscripts []string
	for _, v := range versions {
		var meta pmcOAMeta
		fetch := jsonFetch{client: s.http, source: "europepmc", subject: v, maxBody: pmcOAMaxMetaBody}
		if err := fetch.get(ctx, s.bucket()+"/"+v+"/"+v+".json", &meta); err != nil {
			return nil, err
		}
		u, ok := pmcOAObjectURL(s.bucket(), meta.PDFURL, v)
		if !ok || meta.IsRetracted {
			continue
		}
		if meta.IsManuscript {
			manuscripts = append(manuscripts, u)
		} else {
			published = append(published, u)
		}
	}
	return append(published, manuscripts...), nil
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
