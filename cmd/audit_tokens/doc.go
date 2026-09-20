// Command audit_tokens measures the LLM context-window footprint of libgen-mcp's
// MCP tools and prompts. It builds an in-memory MCP server, lists both catalogs
// over real tools/list and prompts/list round-trips, serializes each definition
// to JSON, and counts tokens with the cl100k_base tokenizer (see countTokens),
// falling back to a bytes/4 heuristic. This is the fixed context cost every
// request pays for having libgen-mcp loaded — useful for judging how "cheap" the
// server is to keep on.
//
// The full tool surface is measured (all download sources enabled), so the number
// is deterministic and represents the upper bound.
//
// Usage:
//
//	go run ./cmd/audit_tokens/
package main
