// Command gen_doc_versions writes the current release into the docs/ pages
// wherever they show it.
//
// The Starlight pages write %%VERSION%% where a reader is shown how to pin the
// current release, and %%RELEASE_YEAR%% and %%RELEASE_MONTH%% in the citation
// sample, and the site build replaces them from VERSION and from CITATION.cff's
// date-released. docs/*.md is read raw on GitHub, so it carries the values
// itself, and this command keeps them right. It does not guess which mentions
// are current: for each token, it lines up every string of that shape in a
// docs page with the same sequence in the page's English Starlight twin
// (frontmatter excluded), and a docs mention is current exactly where the twin
// holds the token. The other mentions ("up to 2.1.0", a changelog section)
// must agree literally with the twin, so a page whose two copies have drifted
// is reported rather than half-rewritten.
//
// Usage:
//
//	go run ./cmd/gen_doc_versions/          # rewrite docs/
//	go run ./cmd/gen_doc_versions/ --check  # fail when docs/ disagrees
package main
