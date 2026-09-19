// Command audit_gateway_chars scans everything a client receives from
// tools/list and prompts/list for characters that MCP gateway validators
// reject.
//
// The rule it enforces is pure ASCII prose, plus a short list of rejected
// ASCII characters — today just the semicolon. That list is deliberately not a
// codepoint inventory: a gateway that refuses "unsafe characters" matches a
// character *class*, so holding the served surface to a class is the only
// version of clean the next gateway cannot surprise. A sibling project was
// refused onboarding with `Description contains unsafe characters: ';'`, over
// semicolons that were ordinary English punctuation. The gateway is the door,
// and the door's rules win.
//
// It measures the served surface rather than grepping the source, because what
// matters is what crosses the wire: a tool description here is assembled from
// half a dozen source strings and a schema's descriptions come from struct
// tags, so a character that survives assembly is a rejection wherever it came
// from.
//
// The line it does not cross is payload text. `internal/tools/citations.go`
// truncates a citation with U+2026 into result *content*, which is data a
// client asked for rather than text the catalog serves, and is outside the
// policy.
//
// Usage:
//
//	go run ./cmd/audit_gateway_chars [-check] [-full]
//
// -check exits non-zero when any offending character is served, which is the
// CI gate. -full prints each offending string whole rather than as an excerpt,
// which is the shape for grepping the source while fixing one.
package main
