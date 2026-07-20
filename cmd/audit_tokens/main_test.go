package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// TestCountTokens checks the tokenizer path returns a positive, sane count and
// that empty input yields zero.
func TestCountTokens(t *testing.T) {
	if n := countTokens([]byte("")); n != 0 {
		t.Errorf("empty input: got %d tokens, want 0", n)
	}
	n := countTokens([]byte("the quick brown fox jumps over the lazy dog"))
	if n <= 0 || n > 20 {
		t.Errorf("token count %d is out of the expected range for a short sentence", n)
	}
}

// TestMeasureTools sums per-tool tokens/bytes and skips nils.
func TestMeasureTools(t *testing.T) {
	list := []*mcp.Tool{
		{Name: "a", Description: "does a thing"},
		nil,
		{Name: "b", Description: "does another thing"},
	}
	infos, totalTokens, totalBytes, err := measureTools(list)
	if err != nil {
		t.Fatal(err)
	}
	if len(infos) != 2 {
		t.Fatalf("got %d tool infos, want 2 (nil skipped)", len(infos))
	}
	if totalTokens <= 0 || totalBytes <= 0 {
		t.Errorf("totals should be positive: tokens=%d bytes=%d", totalTokens, totalBytes)
	}
	sumT, sumB := 0, 0
	for _, in := range infos {
		sumT += in.Tokens
		sumB += in.Bytes
	}
	if sumT != totalTokens || sumB != totalBytes {
		t.Errorf("totals (%d/%d) do not match the per-tool sum (%d/%d)", totalTokens, totalBytes, sumT, sumB)
	}
}

// TestWriteReport renders a table with a TOTAL row and the summary line.
func TestWriteReport(t *testing.T) {
	var b bytes.Buffer
	writeReport(&b, []toolTokenInfo{{Name: "search", Tokens: 100, Bytes: 400}}, 100, 400)
	out := b.String()
	for _, want := range []string{"TOOL", "TOKENS", "search", "TOTAL (1 tools)", "adds ~100 tokens"} {
		if !strings.Contains(out, want) {
			t.Errorf("report missing %q; got:\n%s", want, out)
		}
	}
}

// TestRunEndToEnd exercises the real registration path: it builds the in-memory
// server, lists the 3 tools, and asserts a positive footprint is reported.
func TestRunEndToEnd(t *testing.T) {
	var b bytes.Buffer
	if err := run(&b); err != nil {
		t.Fatalf("run: %v", err)
	}
	out := b.String()
	for _, want := range []string{"search", "get_details", "download", "TOTAL (3 tools)"} {
		if !strings.Contains(out, want) {
			t.Errorf("report missing %q; got:\n%s", want, out)
		}
	}
}
