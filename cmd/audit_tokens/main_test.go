package main

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/tiktoken-go/tokenizer"
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

// errCodec is a tokenizer.Codec whose Encode always fails, exercising the
// countTokensWith error fallback.
type errCodec struct{}

func (errCodec) GetName() string           { return "err" }
func (errCodec) Count(string) (int, error) { return 0, errors.New("count unavailable") }
func (errCodec) Encode(string) ([]uint, []string, error) {
	return nil, nil, errors.New("encode unavailable")
}
func (errCodec) Decode([]uint) (string, error) { return "", errors.New("decode unavailable") }

// TestCountTokensWith_Fallbacks verifies both bytes/4 fallback branches: a nil
// codec and an encode error. Both must return len(data)/4.
func TestCountTokensWith_Fallbacks(t *testing.T) {
	data := []byte("abcdefgh") // 8 bytes -> 8/4 == 2
	if n := countTokensWith(nil, data); n != 2 {
		t.Errorf("nil codec: got %d, want 2 (bytes/4)", n)
	}
	if n := countTokensWith(errCodec{}, data); n != 2 {
		t.Errorf("encode error: got %d, want 2 (bytes/4)", n)
	}
}

// TestCountTokensWith_RealCodec confirms the real codec path is used through
// countTokensWith and returns a positive count.
func TestCountTokensWith_RealCodec(t *testing.T) {
	codec, err := tokenizer.Get(tokenizer.Cl100kBase)
	if err != nil {
		t.Fatalf("get codec: %v", err)
	}
	if n := countTokensWith(codec, []byte("hello world")); n <= 0 {
		t.Errorf("real codec: got %d, want positive", n)
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

// TestMeasureTools_ExcludesIcons verifies Icons never reach the measured
// count: a client's own UI reads them, never the LLM the report exists to
// account for, and a base64 data: URI would otherwise dwarf the rest of a
// small tool's definition.
func TestMeasureTools_ExcludesIcons(t *testing.T) {
	plain := &mcp.Tool{Name: "a", Description: "does a thing"}
	withIcon := &mcp.Tool{
		Name: "a", Description: "does a thing",
		Icons: []mcp.Icon{{Source: "data:image/svg+xml;base64," + strings.Repeat("QQ", 200), MIMEType: "image/svg+xml"}},
	}

	plainInfos, plainTokens, plainBytes, err := measureTools([]*mcp.Tool{plain})
	if err != nil {
		t.Fatal(err)
	}
	iconInfos, iconTokens, iconBytes, err := measureTools([]*mcp.Tool{withIcon})
	if err != nil {
		t.Fatal(err)
	}

	if iconTokens != plainTokens || iconBytes != plainBytes {
		t.Errorf("measureTools with Icons set = (%d tokens, %d bytes), want the icon-free result (%d tokens, %d bytes)",
			iconTokens, iconBytes, plainTokens, plainBytes)
	}
	if iconInfos[0] != plainInfos[0] {
		t.Errorf("per-tool info = %+v, want %+v", iconInfos[0], plainInfos[0])
	}
}

// TestMeasureTools_MarshalError verifies a tool that cannot be JSON-serialized
// (a channel in the InputSchema is unmarshalable) surfaces a wrapped error.
func TestMeasureTools_MarshalError(t *testing.T) {
	list := []*mcp.Tool{{Name: "bad", InputSchema: make(chan int)}}
	_, _, _, err := measureTools(list)
	if err == nil {
		t.Fatal("measureTools() error = nil, want marshal failure")
	}
	if !strings.Contains(err.Error(), "marshal tool") || !strings.Contains(err.Error(), "bad") {
		t.Fatalf("measureTools() error = %v, want marshal tool \"bad\"", err)
	}
}

// TestMeasurePrompts sums per-prompt tokens/bytes and skips nils, mirroring
// TestMeasureTools for the prompt catalog.
func TestMeasurePrompts(t *testing.T) {
	list := []*mcp.Prompt{
		{Name: "a", Description: "does a thing"},
		nil,
		{Name: "b", Description: "does another thing"},
	}
	infos, totalTokens, totalBytes, err := measurePrompts(list)
	if err != nil {
		t.Fatal(err)
	}
	if len(infos) != 2 {
		t.Fatalf("got %d prompt infos, want 2 (nil skipped)", len(infos))
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
		t.Errorf("totals (%d/%d) do not match the per-prompt sum (%d/%d)", totalTokens, totalBytes, sumT, sumB)
	}
}

// TestMeasurePrompts_ExcludesIcons mirrors TestMeasureTools_ExcludesIcons for
// prompts: their Icons are also client-UI-only and must not inflate the
// measured footprint.
func TestMeasurePrompts_ExcludesIcons(t *testing.T) {
	plain := &mcp.Prompt{Name: "a", Description: "does a thing"}
	withIcon := &mcp.Prompt{
		Name: "a", Description: "does a thing",
		Icons: []mcp.Icon{{Source: "data:image/svg+xml;base64," + strings.Repeat("QQ", 200), MIMEType: "image/svg+xml"}},
	}

	plainInfos, plainTokens, plainBytes, err := measurePrompts([]*mcp.Prompt{plain})
	if err != nil {
		t.Fatal(err)
	}
	iconInfos, iconTokens, iconBytes, err := measurePrompts([]*mcp.Prompt{withIcon})
	if err != nil {
		t.Fatal(err)
	}

	if iconTokens != plainTokens || iconBytes != plainBytes {
		t.Errorf("measurePrompts with Icons set = (%d tokens, %d bytes), want the icon-free result (%d tokens, %d bytes)",
			iconTokens, iconBytes, plainTokens, plainBytes)
	}
	if iconInfos[0] != plainInfos[0] {
		t.Errorf("per-prompt info = %+v, want %+v", iconInfos[0], plainInfos[0])
	}
}

// TestMeasurePrompts_MarshalError verifies a prompt that cannot be
// JSON-serialized (a channel in _meta is unmarshalable) surfaces a wrapped
// error.
func TestMeasurePrompts_MarshalError(t *testing.T) {
	list := []*mcp.Prompt{{Name: "bad", Meta: mcp.Meta{"x": make(chan int)}}}
	_, _, _, err := measurePrompts(list)
	if err == nil {
		t.Fatal("measurePrompts() error = nil, want marshal failure")
	}
	if !strings.Contains(err.Error(), "marshal prompt") || !strings.Contains(err.Error(), "bad") {
		t.Fatalf("measurePrompts() error = %v, want marshal prompt \"bad\"", err)
	}
}

// TestRun_ConfigLoadFallback forces config.Load to fail (invalid timeout) and
// verifies run still succeeds via the empty-config fallback, producing a report.
func TestRun_ConfigLoadFallback(t *testing.T) {
	t.Setenv("LIBGEN_MCP_TIMEOUT", "not-a-duration")
	var b bytes.Buffer
	if err := run(&b); err != nil {
		t.Fatalf("run() with unusable config: %v", err)
	}
	if !strings.Contains(b.String(), "TOTAL (4 tools)") {
		t.Fatalf("report missing TOTAL row; got:\n%s", b.String())
	}
}

// TestRun_MirrorManagerError forces mirrors.NewManager to fail by removing the
// environment it needs to resolve a cache directory. config.Load also fails
// under these conditions, so this covers the config fallback and the mirror
// manager error return together.
func TestRun_MirrorManagerError(t *testing.T) {
	t.Setenv("HOME", "")
	t.Setenv("XDG_CACHE_HOME", "")
	t.Setenv("LIBGEN_MCP_DOWNLOAD_DIR", "")
	var b bytes.Buffer
	err := run(&b)
	if err == nil {
		t.Skip("os.UserCacheDir resolved without HOME on this platform; cannot trigger the mirror manager error")
	}
	if !strings.Contains(err.Error(), "create mirror manager") {
		t.Fatalf("run() error = %v, want create mirror manager", err)
	}
}

// TestWriteReport renders both tables with their TOTAL rows and the combined
// summary line.
func TestWriteReport(t *testing.T) {
	var b bytes.Buffer
	writeReport(&b,
		[]entryTokenInfo{{Name: "search", Tokens: 100, Bytes: 400}}, 100, 400,
		[]entryTokenInfo{{Name: "acquire_book", Tokens: 50, Bytes: 200}}, 50, 200,
	)
	out := b.String()
	for _, want := range []string{
		"TOOL", "search", "TOTAL (1 tools)",
		"PROMPT", "acquire_book", "TOTAL (1 prompts)",
		"adds ~150 tokens", "~100 for its 1 tool definitions", "~50 for its 1 prompt definitions",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("report missing %q; got:\n%s", want, out)
		}
	}
}

// TestRunEndToEnd exercises the real registration path: it builds the in-memory
// server, lists the 4 tools and 4 prompts, and asserts a positive footprint is
// reported for both.
func TestRunEndToEnd(t *testing.T) {
	var b bytes.Buffer
	if err := run(&b); err != nil {
		t.Fatalf("run: %v", err)
	}
	out := b.String()
	for _, want := range []string{
		"search", "get_details", "download", "read", "TOTAL (4 tools)",
		"acquire_book", "research_topic", "get_paper", "download_troubleshoot", "TOTAL (4 prompts)",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("report missing %q; got:\n%s", want, out)
		}
	}
}
