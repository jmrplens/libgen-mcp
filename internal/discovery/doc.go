// Package discovery federates keyless literature sources into a single result shape
// the search tool can present and the read/download tools can act on: the open-access
// providers (arXiv, OpenAlex, Europe PMC, Crossref, OpenLibrary, Project Gutenberg),
// then the bibliographic indexes (dblp for computer science, PubMed for biomedicine),
// which describe a paper precisely without claiming it is free to read, ERIC, which
// reaches the education grey literature — reports, theses, agency documents — that has
// no DOI and so appears in none of the others, and Anna's Archive. Every provider is
// best-effort: a failing source degrades to an empty result rather than sinking a
// federated search. No source requires an API key, account, or login; OpenAlex takes
// an optional key that only raises its daily allowance. Pacing and refusal windows are
// kept per upstream for the whole process, because a provider value lives for one
// search only.
package discovery
