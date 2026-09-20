//go:build eval

// Command eval is a LIVE, LLM-driven eval harness for libgen-mcp. It drives a
// small Anthropic model (claude-haiku-4-5) over the real libgen-mcp tools,
// registered on an in-process MCP server, and grades whether the model selects
// the right tool with well-formed arguments and gets a usable real response.
//
// It is LIVE: it spends Anthropic API tokens, hits real Library Genesis mirrors
// and article sources, and downloads real files into a temporary directory. It
// is compiled only under the "eval" build tag and, even then, refuses to run
// unless LIBGEN_EVAL=1 and ANTHROPIC_API_KEY are both set.
//
// Usage:
//
//	LIBGEN_EVAL=1 ANTHROPIC_API_KEY=sk-... go run -tags eval ./cmd/eval
//	go run -tags eval ./cmd/eval --only S1,S6 --results-doc out.md --keep-downloads
package main
