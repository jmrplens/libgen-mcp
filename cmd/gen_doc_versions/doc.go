// Command gen_doc_versions writes the VERSION file's release into the docs/
// pages wherever they show the current one.
//
// The Starlight pages write %%VERSION%% where a reader is shown how to pin the
// current release, and the site build replaces it. docs/*.md is read raw on
// GitHub, so it carries the number itself, and this command keeps that number
// right. It does not guess which mentions are current: it lines up every
// version-shaped string of a docs page with the same sequence in the page's
// English Starlight twin, and a docs mention is current exactly where the twin
// holds the token. The other mentions ("up to 2.1.0", a changelog section) must
// agree literally with the twin, so a page whose two copies have drifted is
// reported rather than half-rewritten.
//
// Usage:
//
//	go run ./cmd/gen_doc_versions/          # rewrite docs/ from VERSION
//	go run ./cmd/gen_doc_versions/ --check  # fail when docs/ disagrees with VERSION
package main
