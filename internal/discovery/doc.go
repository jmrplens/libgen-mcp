// Package discovery federates keyless literature sources into a single result shape
// the search tool can present and the read/download tools can act on: the open-access
// providers (arXiv, Crossref, OpenLibrary, Project Gutenberg) plus the bibliographic
// indexes (dblp for computer science, PubMed for biomedicine), which describe a paper
// precisely without claiming it is free to read, and ERIC, which reaches the education
// grey literature — reports, theses, agency documents — that has no DOI and so appears
// in none of the others. Every provider is best-effort: a failing source
// degrades to an empty result rather than sinking a federated search, and no source
// requires an API key, account, or login.
package discovery
