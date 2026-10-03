// The related argument of get_details: references and citing works.

package tools

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/jmrplens/libgen-mcp/v2/internal/config"
	"github.com/jmrplens/libgen-mcp/v2/internal/libgen"
)

// relatedAsk is a validated related request: the kind, and how many works.
// The zero value asks for nothing.
type relatedAsk struct {
	kind  string
	limit int
}

// relatedRequest validates related and related_limit. The schema pins the
// same enum and bounds, so the refusals are for a client that does not check
// against it.
func relatedRequest(kind string, limit int) (relatedAsk, error) {
	kind = strings.ToLower(strings.TrimSpace(kind))
	switch {
	case kind == "" && limit != 0:
		return relatedAsk{}, fmt.Errorf("related_limit is only read with related (%s)", strings.Join(libgen.RelatedKinds(), " or "))
	case kind == "":
		return relatedAsk{}, nil
	case !slices.Contains(libgen.RelatedKinds(), kind):
		return relatedAsk{}, fmt.Errorf("related takes %s, not %q", strings.Join(libgen.RelatedKinds(), " or "), truncateRunes(oneLine(kind), 40))
	case limit < 0 || limit > libgen.RelatedMaxLimit:
		return relatedAsk{}, fmt.Errorf("related_limit is 1 to %d, not %d", libgen.RelatedMaxLimit, limit)
	case limit == 0:
		limit = libgen.RelatedDefaultLimit
	}
	return relatedAsk{kind: kind, limit: limit}, nil
}

// relatedLooker lists a DOI's related works. It is an interface so the
// assembly below can be exercised offline.
type relatedLooker interface {
	Related(ctx context.Context, doi, kind string, limit int) libgen.RelatedWorks
}

// attachRelated adds the related works the call asked for. It looks them up by
// the same DOI the citation styles may send to doi.org, and for the same
// reason: a catalog DOI that failed corroboration belongs to another work, and
// listing that work's references under this record would be the fabrication
// the corroboration exists to stop. A record without such a DOI gets a note
// saying the list is not available, rather than nothing.
func attachRelated(ctx context.Context, allowed bool, looker relatedLooker, out *DetailsOutput, ask relatedAsk) {
	if ask.kind == "" {
		return
	}
	doi := negotiableDOI(*out)
	switch {
	case doi == "":
		out.Related = &libgen.RelatedWorks{Kind: ask.kind, Note: "Not available: " + relatedNoDOIReason(out.Citations)}
	case !allowed:
		out.Related = &libgen.RelatedWorks{
			Kind: ask.kind,
			Note: "Not available: this server does not reach OpenAlex for metadata (" + config.EnvName("ENRICH") + "=false).",
		}
	default:
		related := looker.Related(ctx, doi, ask.kind, ask.limit)
		out.Related = &related
	}
}

// relatedNoDOIReason says why a record has no DOI to look its related works
// up by.
func relatedNoDOIReason(c *Citations) string {
	if c != nil && c.DOIStatus != "" && c.DOIStatus != string(libgen.DOIConfirmed) {
		return "the record's DOI was not confirmed to name this work, and OpenAlex is asked by DOI."
	}
	return "the record has no DOI, and OpenAlex is asked by DOI."
}
