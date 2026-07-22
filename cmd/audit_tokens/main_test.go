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
// server, lists the 4 tools, and asserts a positive footprint is reported.
func TestRunEndToEnd(t *testing.T) {
	var b bytes.Buffer
	if err := run(&b); err != nil {
		t.Fatalf("run: %v", err)
	}
	out := b.String()
	for _, want := range []string{"search", "get_details", "download", "read", "TOTAL (4 tools)"} {
		if !strings.Contains(out, want) {
			t.Errorf("report missing %q; got:\n%s", want, out)
		}
	}
}
