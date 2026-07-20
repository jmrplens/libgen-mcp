package main

import (
	"sync"

	"github.com/tiktoken-go/tokenizer"
)

// Token counting is audit-only: it lives here with its sole consumer
// (cmd/audit_tokens) rather than in a runtime package — the MCP server itself
// never tokenizes anything.

var (
	defaultCodec tokenizer.Codec
	codecOnce    sync.Once
)

// tokenCodec lazily initializes the cl100k_base tokenizer on first use. If
// initialization fails (e.g. a corrupt vocabulary), it returns nil and
// countTokens falls back to the bytes/4 heuristic.
func tokenCodec() tokenizer.Codec {
	codecOnce.Do(func() {
		defaultCodec, _ = tokenizer.Get(tokenizer.Cl100kBase)
	})
	return defaultCodec
}

// countTokens returns the token count for data using the cl100k_base tokenizer
// (the GPT-4 / GPT-3.5 encoding, a good proxy across modern LLMs). It falls back
// to the bytes/4 heuristic when the tokenizer is unavailable.
func countTokens(data []byte) int {
	codec := tokenCodec()
	if codec == nil {
		return len(data) / 4
	}
	ids, _, err := codec.Encode(string(data))
	if err != nil {
		return len(data) / 4
	}
	return len(ids)
}
