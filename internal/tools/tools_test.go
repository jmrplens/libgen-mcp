package tools

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/jmrplens/libgen-mcp/internal/config"
	"github.com/jmrplens/libgen-mcp/internal/libgen"
)

type staticMirrors []string

func (s staticMirrors) Mirrors(context.Context) []string { return s }

// newSession levanta servidor MCP + cliente in-memory con un mirror httptest
// que sirve las fixtures del paquete libgen.
func newSession(t *testing.T) *mcp.ClientSession {
	t.Helper()
	searchHTML, err := os.ReadFile("../libgen/testdata/search_books.html")
	if err != nil {
		t.Fatal(err)
	}
	fileJSON, _ := os.ReadFile("../libgen/testdata/file_by_md5.json")
	editionJSON, _ := os.ReadFile("../libgen/testdata/edition.json")
	mux := http.NewServeMux()
	mux.HandleFunc("/index.php", func(w http.ResponseWriter, r *http.Request) { w.Write(searchHTML) })
	mux.HandleFunc("/json.php", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("object") == "f" {
			w.Write(fileJSON)
		} else {
			w.Write(editionJSON)
		}
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	client := libgen.New(staticMirrors{srv.URL}, 5*time.Second)
	cfg := &config.Config{DownloadDir: t.TempDir(), Timeout: 5 * time.Second}
	server := mcp.NewServer(&mcp.Implementation{Name: "test", Version: "0.0.1"}, nil)
	Register(server, client, cfg)

	st, ct := mcp.NewInMemoryTransports()
	ctx := context.Background()
	if _, err := server.Connect(ctx, st, nil); err != nil {
		t.Fatal(err)
	}
	mcpClient := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "0.0.1"}, nil)
	session, err := mcpClient.Connect(ctx, ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { session.Close() })
	return session
}

func TestToolsRegistered(t *testing.T) {
	session := newSession(t)
	res, err := session.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	names := map[string]bool{}
	for _, tool := range res.Tools {
		names[tool.Name] = true
	}
	for _, want := range []string{"search", "get_details", "download"} {
		if !names[want] {
			t.Errorf("falta la tool %q; registradas: %v", want, names)
		}
	}
	if len(res.Tools) != 3 {
		t.Errorf("hay %d tools, esperaba 3", len(res.Tools))
	}
}

func TestSearchTool(t *testing.T) {
	session := newSession(t)
	res, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      "search",
		Arguments: map[string]any{"query": "golang", "topics": []string{"nonfiction"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.IsError {
		t.Fatalf("tool error: %v", res.Content)
	}
	data, _ := json.Marshal(res.StructuredContent)
	var out struct {
		Results []struct {
			MD5   string `json:"md5"`
			Title string `json:"title"`
		} `json:"results"`
		HasMore bool `json:"has_more"`
	}
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatal(err)
	}
	if len(out.Results) == 0 || out.Results[0].MD5 == "" {
		t.Errorf("resultados inesperados: %+v", out)
	}
}

func TestSearchToolBadTopic(t *testing.T) {
	session := newSession(t)
	res, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      "search",
		Arguments: map[string]any{"query": "x", "topics": []string{"cooking"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !res.IsError {
		t.Fatal("topic inválido debería devolver tool error")
	}
}

func TestGetDetailsTool(t *testing.T) {
	session := newSession(t)
	res, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      "get_details",
		Arguments: map[string]any{"md5": "87a4ebdaf21fa6cc70009a3dd63194ee"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.IsError {
		t.Fatalf("tool error: %v", res.Content)
	}
	data, _ := json.Marshal(res.StructuredContent)
	if !strings.Contains(string(data), "87a4ebdaf21fa6cc70009a3dd63194ee") {
		t.Errorf("salida sin md5: %s", data)
	}
}

func TestGetDetailsToolValidation(t *testing.T) {
	session := newSession(t)
	for _, args := range []map[string]any{
		{},
		{"md5": "87a4ebdaf21fa6cc70009a3dd63194ee", "id": "1"},
	} {
		res, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: "get_details", Arguments: args})
		if err != nil {
			t.Fatal(err)
		}
		if !res.IsError {
			t.Errorf("args %v deberían devolver tool error", args)
		}
	}
}
