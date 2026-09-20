// Package mcpsurface introspects the registered MCP surface for the command-line
// generators.
//
// Several commands under cmd/ need the same thing: the tools and prompts the
// server actually registers, read over a real MCP round-trip rather than
// described by hand, against a configuration that advertises the full capability
// set regardless of what the ambient environment enables. Keeping that in one
// place is what makes the generated artifacts identical on every machine — the
// credential placeholders in DocsConfig in particular, since a missing one
// silently trims the download tool's schema and makes the --check gates pass or
// fail by accident.
package mcpsurface
