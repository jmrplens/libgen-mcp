// Command audit_doc_names holds the names in this repository's documentation
// to the names the server actually has.
//
// A page that names a variable nobody reads, a download source nobody can
// select, or a tool nobody registered is worse than a page that says nothing:
// a reader who sets it, selects it or calls it gets silence, and the silence
// looks like their own mistake rather than the documentation's.
//
// # Why these three namespaces and not the tool names
//
// The sibling project this rule comes from matches its tool names directly,
// because they are prefixed — gitlab_list_issues occurs in prose only when it
// means the tool. This server's tools are search, get_details, download and
// read, which occur in English prose thousands of times, so matching them as
// tokens would report the word "search" in every sentence that uses it.
//
// What survives the move is the three namespaces that are distinctive enough
// to match without guessing:
//
//   - Every LIBGEN_MCP_* token. It cannot be anything but a variable, and
//     config.KnownEnvNames is the list of the ones the server reads.
//   - Every source named in a shape that means a source: a "source": "x" value
//     or a LIBGEN_MCP_SOURCES= list. A bare backticked word is not checked,
//     because a source name is an ordinary word and the exemption list would
//     become the audit.
//   - Every registered tool and prompt, checked the other way round: each one
//     has to appear somewhere in the documentation. That direction needs no
//     matching at all — the name comes from the registration — and it catches
//     the failure the first two cannot, which is a capability nobody wrote
//     about.
//
// # What is not read
//
// docs/superpowers/ is a historical tree of plans and specifications,
// superseded by design and kept for the record. It is excluded here for the
// same reason it is excluded from markdownlint: it already names a variable
// this server never shipped, and a gate whose first finding is a document
// nobody will edit teaches its reader to pass a flag rather than read the
// finding.
//
// Usage:
//
//	go run ./cmd/audit_doc_names [-check] [-dir .]
//
// -check exits non-zero when a name does not resolve. It exits 2 when the
// audit could not run at all, which is the split this repository's gates use:
// a gate that cannot run must not read as a gate that passed.
package main
