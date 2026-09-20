// Command audit_surface_quality audits the quality of libgen-mcp's MCP tool
// surface: the shape LLM clients actually see over a tools/list round-trip.
//
// It builds an in-memory MCP server with the full tool surface enabled,
// lists the tools, and enforces the repo's surface conventions:
//
//   - every tool has a Title, non-nil Annotations, and a description of at
//     least [minDescLen] characters;
//   - every tool states annotations.destructiveHint rather than leaving it
//     unset, since a client that gates on the hint reads its absence as
//     destructive;
//   - every tool's InputSchema is a JSON Schema object;
//   - every named input and output field carries a non-empty jsonschema
//     description (so the model is never handed an unlabeled parameter);
//   - every enum constrains its property to a non-empty set of values, with no
//     blank or repeated one;
//   - every value an enum accepts is named in the description beside it, so the
//     prose cannot go stale against the values the schema pins;
//   - no description outgrows its budget, since a tool definition is re-sent on
//     every request (cmd/audit_tokens prices the whole surface).
//
// The audit exits non-zero when it finds violations, so it works as a CI gate
// (see the audit-surface-quality Make target). Pass -json to emit the findings
// as a structured document instead of the Markdown report.
//
// Usage:
//
//	go run ./cmd/audit_surface_quality/            # Markdown report, gate mode
//	go run ./cmd/audit_surface_quality/ -json      # JSON findings
//	go run ./cmd/audit_surface_quality/ -no-fail   # report only, always exit 0
package main
