// Package openalex is the one place every request to the OpenAlex API is prepared
// and every OpenAlex response is accounted for, whichever package issues it: the
// download chain's openalex source in internal/libgen and the openalex search
// provider in internal/discovery both import it, and neither imports the other.
//
// It owns two things that must not exist twice. The optional API key
// (LIBGEN_MCP_OPENALEX_KEY) is attached by [NewRequest], in the Authorization
// header and never in the URL, so no error, log line or trace can carry it. And
// the daily credit budget is observed by [Budget], once per process, because
// OpenAlex meters every caller by address and the download source and the search
// provider draw on the same allowance: a keyless search costs ten credits of a
// thousand a day, and a search provider that spent the last of them would leave
// the download chain's lookups refused until midnight UTC.
package openalex
