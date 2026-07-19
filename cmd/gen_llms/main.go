// Command gen_llms generates llms.txt and llms-full.txt files. It creates an
// in-memory MCP server with libgen-mcp's three tools registered, introspects
// them via the SDK, and writes two files to the project root:
//
//   - llms.txt: concise llmstxt.org index for LLM discovery
//   - llms-full.txt: detailed companion reference with tool schemas
//
// Usage:
//
//	go run ./cmd/gen_llms/
//	go run ./cmd/gen_llms/ --check
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/jmrplens/libgen-mcp/internal/config"
	"github.com/jmrplens/libgen-mcp/internal/libgen"
	"github.com/jmrplens/libgen-mcp/internal/mirrors"
	"github.com/jmrplens/libgen-mcp/internal/tools"
)

const (
	// maxFullDescRunes caps the length of tool descriptions in llms-full.txt to
	// keep the file scannable. When a description exceeds this limit, generation
	// falls back to its first sentence; if that is still too long, the text is
	// hard-truncated at the rune boundary.
	maxFullDescRunes      = 600
	llmsFileName          = "llms.txt"
	llmsFullFileName      = "llms-full.txt"
	llmsSummaryItemFormat = "- %s: %s\n"
	llmsBoldTitleFormat   = "**%s**\n\n"
	docsSiteURL           = "https://jmrplens.github.io/libgen-mcp/"
)

func main() {
	checkOnly := flag.Bool("check", false, "validate generated llms files without writing them")
	flag.Parse()

	if err := run(*checkOnly); err != nil {
		fmt.Fprintf(os.Stderr, "failed to generate llms files: %v\n", err)
		os.Exit(1)
	}
}

// run introspects the live MCP tool catalog and regenerates llms.txt and
// llms-full.txt in the project root.
func run(checkOnly bool) error {
	rootDir, err := findProjectRoot()
	if err != nil {
		return err
	}
	version := readVersion(rootDir)

	toolList, err := listTools()
	if err != nil {
		return err
	}

	if writeErr := writeLLMSTxt(version, toolList, checkOnly); writeErr != nil {
		return writeErr
	}
	if writeErr := writeLLMSFullTxt(version, toolList, checkOnly); writeErr != nil {
		return writeErr
	}

	if checkOnly {
		fmt.Printf("Validated llms.txt and llms-full.txt\n")
		return nil
	}
	fmt.Printf("Generated llms.txt and llms-full.txt (%d tools)\n", len(toolList))
	return nil
}

// readVersion reads the VERSION file from the project root.
func readVersion(rootDir string) string {
	root, err := os.OpenRoot(rootDir)
	if err != nil {
		return "unknown"
	}
	defer func() { _ = root.Close() }()

	data, err := root.ReadFile("VERSION")
	if err != nil {
		return "unknown"
	}
	return strings.TrimSpace(string(data))
}

// newSession creates an in-memory MCP server+client session with high page size.
func newSession(setupServer func(*mcp.Server) error) (session *mcp.ClientSession, cleanup func(), err error) {
	opts := &mcp.ServerOptions{PageSize: 2000}
	server := mcp.NewServer(&mcp.Implementation{Name: "gen-llms", Version: "0.0.1"}, opts)
	if setupErr := setupServer(server); setupErr != nil {
		return nil, nil, setupErr
	}

	st, ct := mcp.NewInMemoryTransports()
	ctx := context.Background()

	serverSession, err := server.Connect(ctx, st, nil)
	if err != nil {
		return nil, nil, fmt.Errorf("server connect: %w", err)
	}

	mcpClient := mcp.NewClient(&mcp.Implementation{Name: "gen-llms-client", Version: "0.0.1"}, nil)
	session, err = mcpClient.Connect(ctx, ct, nil)
	if err != nil {
		_ = serverSession.Close()
		_ = serverSession.Wait()
		return nil, nil, fmt.Errorf("client connect: %w", err)
	}

	return session, func() {
		_ = session.Close()
		_ = serverSession.Wait()
	}, nil
}

// listTools builds an in-memory libgen-mcp server and returns its registered
// tools via a real MCP tools/list round-trip. Construction is offline-safe:
// config.Load and mirrors.NewManager perform no network I/O, and the client is
// never asked to make a request here.
func listTools() ([]*mcp.Tool, error) {
	cfg, err := config.Load()
	if err != nil {
		cfg = &config.Config{}
	}
	mgr, err := mirrors.NewManager(cfg)
	if err != nil {
		return nil, fmt.Errorf("create mirror manager: %w", err)
	}
	client := libgen.New(mgr, cfg)

	session, cleanup, err := newSession(func(server *mcp.Server) error {
		tools.Register(server, client, cfg)
		return nil
	})
	if err != nil {
		return nil, err
	}
	defer cleanup()

	result, err := session.ListTools(context.Background(), nil)
	if err != nil {
		return nil, fmt.Errorf("list tools: %w", err)
	}
	sort.SliceStable(result.Tools, func(i, j int) bool {
		return toolOrder(result.Tools[i].Name) < toolOrder(result.Tools[j].Name)
	})
	return result.Tools, nil
}

// toolOrder returns a stable ordinal so tools always print in the natural
// workflow order (search, then get_details, then download) regardless of the
// order the SDK lists them.
func toolOrder(name string) int {
	switch name {
	case "search":
		return 0
	case "get_details":
		return 1
	case "download":
		return 2
	default:
		return 3
	}
}

// writeLLMSTxt generates the concise llms.txt overview.
func writeLLMSTxt(version string, toolList []*mcp.Tool, checkOnly bool) error {
	var b strings.Builder

	b.WriteString("# libgen-mcp\n\n")
	b.WriteString("> A Model Context Protocol (MCP) server that searches and downloads books, papers, comics, magazines and standards from Library Genesis for AI assistants.\n\n")
	fmt.Fprintf(&b, "libgen-mcp v%s is a single static Go binary that runs locally via stdio or remotely via HTTP transport.\n", version)
	fmt.Fprintf(&b, "It provides exactly %d MCP tools (search, get_details, download) over the libgen.li family of mirrors. No account, token or credential is required. Cross-platform: Windows, Linux, macOS (amd64 + arm64).\n\n", len(toolList))

	b.WriteString("Quick start:\n\n")
	b.WriteString("1. Download the prebuilt binary for your platform from the Releases page (recommended — no dependencies), or use Docker (see below)\n")
	b.WriteString("2. Add the server to your AI client configuration (see below)\n")
	b.WriteString("3. Start searching — no sign-up, API key or token is needed\n\n")

	b.WriteString("Installing for an AI assistant (headless):\n\n")
	b.WriteString("If you are an AI assistant setting this up for a user from just this repository URL, write the MCP client configuration directly. No credentials are required. The recommended install is the prebuilt static binary (no dependencies): download the release asset for the user's OS and architecture from the Releases page and point `command` at its absolute path. Cursor, Claude Desktop, and Claude Code use an `mcpServers` key; VS Code and GitHub Copilot use a `servers` key (each entry also sets `\"type\": \"stdio\"`). The `mcpServers` form with the native binary:\n\n")
	b.WriteString("```json\n")
	b.WriteString("{\n")
	b.WriteString("  \"mcpServers\": {\n")
	b.WriteString("    \"libgen\": {\n")
	b.WriteString("      \"command\": \"/usr/local/bin/libgen-mcp\"\n")
	b.WriteString("    }\n")
	b.WriteString("  }\n")
	b.WriteString("}\n")
	b.WriteString("```\n\n")
	b.WriteString("For VS Code / GitHub Copilot, use the `servers` key instead of `mcpServers` and add `\"type\": \"stdio\"` to the `libgen` entry.\n\n")
	b.WriteString("Docker alternative (no download; pulls the image on first run and runs over stdio) — useful when you cannot determine the user's OS and architecture: set `command` to `docker` and `args` to `[\"run\", \"-i\", \"--rm\", \"ghcr.io/jmrplens/libgen-mcp:latest\"]`.\n\n")
	b.WriteString("Claude Code (CLI): `claude mcp add libgen -- /usr/local/bin/libgen-mcp` (native binary), or `claude mcp add libgen -- docker run -i --rm ghcr.io/jmrplens/libgen-mcp:latest` (Docker).\n\n")

	b.WriteString("Configuration (environment variables, all optional):\n\n")
	b.WriteString("- LIBGEN_MIRROR: force a specific mirror base URL, e.g. `https://libgen.li` (default: auto-discovered)\n")
	b.WriteString("- LIBGEN_MCP_DOWNLOAD_DIR: download destination directory (default: `~/Downloads`)\n")
	b.WriteString("- LIBGEN_MCP_TIMEOUT: timeout per HTTP request, e.g. `30s` (default: 30s)\n")
	b.WriteString("- LIBGEN_MCP_LOG_LEVEL: log level — debug, info, warn or error (default: info)\n")
	b.WriteString("- LIBGEN_MCP_RATE_RPS: allowed requests per second (default: 1)\n")
	b.WriteString("- LIBGEN_MCP_RATE_BURST: maximum rate-limiter burst (default: 1)\n")
	b.WriteString("- LIBGEN_MCP_MAX_DOWNLOAD_BYTES: maximum download size in bytes, 0 = no limit (default: 0)\n")
	b.WriteString("- LIBGEN_MCP_MAX_CONCURRENT_DOWNLOADS: simultaneous downloads (default: 2)\n")
	b.WriteString("- LIBGEN_MCP_RETRY_ATTEMPTS: retries per request (default: 3)\n")
	b.WriteString("- LIBGEN_MCP_UNPAYWALL_EMAIL: contact email required by the Unpaywall API for article downloads\n")
	b.WriteString("- LIBGEN_MCP_SCIHUB_HOSTS: comma-separated ordered Sci-Hub mirror hosts (bare host, no scheme)\n")
	b.WriteString("- LIBGEN_MCP_SOURCES: comma-separated enabled download sources — unpaywall, scihub, libgen, randombook (empty = all)\n\n")

	b.WriteString("Tools:\n\n")
	for _, t := range toolList {
		desc := firstSentence(t.Description)
		desc = truncateRunes(desc, 120)
		fmt.Fprintf(&b, llmsSummaryItemFormat, t.Name, desc)
	}
	b.WriteString("\n")

	b.WriteString("## Documentation\n\n")
	writeLLMSLink(&b, "Getting started", "docs/getting-started.md", "Installation and first-run guide")
	writeLLMSLink(&b, "Configuration", "docs/configuration.md", "Full environment-variable configuration reference")
	writeLLMSLink(&b, "Tools", "docs/tools.md", "Per-tool reference for search, get_details and download")
	writeLLMSLink(&b, "Architecture", "docs/architecture.md", "Internal architecture, mirror discovery and download sources")
	writeLLMSLink(&b, "Troubleshooting", "docs/troubleshooting.md", "Common setup and runtime issues")
	writeLLMSLink(&b, "Privacy policy", "PRIVACY.md", "No telemetry; requests go only to Library Genesis mirrors and article sources")
	writeLLMSLink(&b, "Headless install", "llms-install.md", "Machine-readable install guide for AI assistants")

	b.WriteString("\n## Optional\n\n")
	writeLLMSLink(&b, "Full LLM reference", llmsFullFileName, "Generated companion reference with full tool schemas")
	writeLLMSLink(&b, "Documentation site", docsSiteURL, "Rendered documentation site")

	content := b.String()
	if err := validateLLMSTxt(content); err != nil {
		return fmt.Errorf("validate llms.txt: %w", err)
	}
	if err := writeGeneratedFile(llmsFileName, content, checkOnly); err != nil {
		return fmt.Errorf("write llms.txt: %w", err)
	}
	return nil
}

func writeLLMSLink(b *strings.Builder, label, target, description string) {
	fmt.Fprintf(b, "- [%s](%s): %s\n", label, target, description)
}

func validateLLMSTxt(content string) error {
	lines := strings.Split(strings.ReplaceAll(content, "\r\n", "\n"), "\n")
	if len(lines) == 0 || strings.TrimSpace(lines[0]) == "" {
		return errors.New("missing H1 title")
	}
	if !strings.HasPrefix(strings.TrimSpace(lines[0]), "# ") || strings.HasPrefix(strings.TrimSpace(lines[0]), "##") {
		return fmt.Errorf("first line must be an H1 title, got %q", lines[0])
	}

	state := llmsTxtValidationState{}
	for index, rawLine := range lines[1:] {
		lineNumber := index + 2
		line := strings.TrimSpace(rawLine)
		if err := state.validateLine(lineNumber, line); err != nil {
			return err
		}
	}
	if !state.foundSummary {
		return errors.New("missing blockquote summary")
	}
	if state.inFileListSection && !state.sectionHasLink {
		return fmt.Errorf("section %q has no file links", state.currentSection)
	}
	return nil
}

type llmsTxtValidationState struct {
	foundSummary      bool
	inFileListSection bool
	currentSection    string
	sectionHasLink    bool
}

func (s *llmsTxtValidationState) validateLine(lineNumber int, line string) error {
	if line == "" {
		return nil
	}
	if strings.HasPrefix(line, "#") {
		return s.validateHeading(lineNumber, line)
	}
	if !s.inFileListSection {
		if strings.HasPrefix(line, ">") {
			s.foundSummary = true
		}
		return nil
	}
	if err := validateLLMSFileListItem(line); err != nil {
		return fmt.Errorf("line %d: %w", lineNumber, err)
	}
	s.sectionHasLink = true
	return nil
}

func (s *llmsTxtValidationState) validateHeading(lineNumber int, line string) error {
	if !strings.HasPrefix(line, "## ") || strings.HasPrefix(line, "###") {
		return fmt.Errorf("line %d: llms.txt only allows H1 plus H2 file-list sections", lineNumber)
	}
	if s.inFileListSection && !s.sectionHasLink {
		return fmt.Errorf("section %q has no file links", s.currentSection)
	}
	s.currentSection = strings.TrimSpace(strings.TrimPrefix(line, "## "))
	if s.currentSection == "" {
		return fmt.Errorf("line %d: H2 section title is empty", lineNumber)
	}
	s.inFileListSection = true
	s.sectionHasLink = false
	return nil
}

func validateLLMSFileListItem(line string) error {
	if !strings.HasPrefix(line, "- [") {
		return fmt.Errorf("file-list entries must start with a markdown link, got %q", line)
	}
	closeLabel := strings.Index(line, "](")
	if closeLabel <= len("- [") {
		return fmt.Errorf("file-list entry is missing markdown link label, got %q", line)
	}
	urlStart := closeLabel + len("](")
	urlEnd := strings.Index(line[urlStart:], ")")
	if urlEnd < 0 {
		return fmt.Errorf("file-list entry is missing markdown link target, got %q", line)
	}
	url := strings.TrimSpace(line[urlStart : urlStart+urlEnd])
	if url == "" {
		return fmt.Errorf("file-list entry has empty markdown link target, got %q", line)
	}
	remainder := strings.TrimSpace(line[urlStart+urlEnd+1:])
	if remainder != "" && !strings.HasPrefix(remainder, ":") {
		return fmt.Errorf("file-list entry notes must follow ':' after the markdown link, got %q", line)
	}
	return nil
}

func validateLLMSFullTxt(content string) error {
	lines := strings.Split(strings.ReplaceAll(content, "\r\n", "\n"), "\n")
	if len(lines) == 0 || !strings.HasPrefix(strings.TrimSpace(lines[0]), "# ") {
		return errors.New("missing H1 title")
	}
	for _, section := range []string{"## Tools", "## Configuration", "## Download sources", "## Transports", "## Install (headless)"} {
		if !strings.Contains(content, section+"\n") {
			return fmt.Errorf("missing %q section", section)
		}
	}
	return nil
}

// writeLLMSFullTxt generates the detailed llms-full.txt with tool schemas.
func writeLLMSFullTxt(version string, toolList []*mcp.Tool, checkOnly bool) error {
	var b strings.Builder

	b.WriteString("# libgen-mcp — Full Reference\n\n")
	fmt.Fprintf(&b, "> Version %s | %d tools\n\n", version, len(toolList))

	b.WriteString("## Tools\n\n")
	b.WriteString("libgen-mcp exposes three tools over the libgen.li family of mirrors. No account or token is required.\n\n")
	for _, tool := range toolList {
		writeLLMSFullTool(&b, tool)
	}

	writeLLMSFullConfiguration(&b)
	writeLLMSFullDownloadSources(&b)
	writeLLMSFullTransports(&b)
	writeLLMSFullInstall(&b)

	content := b.String()
	if err := validateLLMSFullTxt(content); err != nil {
		return fmt.Errorf("validate llms-full.txt: %w", err)
	}
	if err := writeGeneratedFile(llmsFullFileName, content, checkOnly); err != nil {
		return fmt.Errorf("write llms-full.txt: %w", err)
	}
	return nil
}

// envVarDoc documents a single environment variable for the Configuration
// section. The values mirror the defaults and validation ranges in
// internal/config/config.go (Load and Validate); they change rarely, so they are
// hardcoded here to keep the generator offline and free of side effects.
type envVarDoc struct {
	name     string
	def      string
	rangeStr string
	meaning  string
}

// configEnvVars is the authoritative Configuration reference. Keep it in sync
// with internal/config/config.go: default values come from config.Load and the
// valid ranges come from config.Config.Validate. All variables are optional and
// no credentials are required.
var configEnvVars = []envVarDoc{
	{"LIBGEN_MIRROR", "auto-discovered", "http/https URL with a host", "Force a specific mirror base URL, e.g. https://libgen.li. Empty means the mirror is auto-discovered."},
	{"LIBGEN_MCP_DOWNLOAD_DIR", "~/Downloads", "writable directory path", "Download destination directory. Created if missing; must be writable."},
	{"LIBGEN_MCP_TIMEOUT", "30s", "(0, 10m]", "Timeout per HTTP request, as a Go duration string (e.g. 30s, 2m)."},
	{"LIBGEN_MCP_LOG_LEVEL", "info", "debug, info, warn or error", "Logging verbosity."},
	{"LIBGEN_MCP_RATE_RPS", "1", "(0, 20]", "Allowed outbound requests per second."},
	{"LIBGEN_MCP_RATE_BURST", "1", "[1, 100]", "Maximum rate-limiter burst."},
	{"LIBGEN_MCP_MAX_DOWNLOAD_BYTES", "0", "[0, 53687091200] (0 = no limit, ceiling 50 GiB)", "Maximum download size in bytes."},
	{"LIBGEN_MCP_MAX_CONCURRENT_DOWNLOADS", "2", "[1, 16]", "Number of simultaneous downloads."},
	{"LIBGEN_MCP_RETRY_ATTEMPTS", "3", "[1, 10]", "Retries per request."},
	{"LIBGEN_MCP_UNPAYWALL_EMAIL", "mail@jmrp.io", "email with @ and a dotted domain", "Contact email required by the Unpaywall API for article (DOI) downloads."},
	{"LIBGEN_MCP_SCIHUB_HOSTS", "sci-hub.ee, sci-hub.se, sci-hub.st, sci-hub.ru, sci-hub.wf", "comma-separated bare hosts (no scheme, no path)", "Ordered Sci-Hub mirror hosts, tried in order until one serves an article."},
	{"LIBGEN_MCP_SOURCES", "empty (all enabled)", "comma-separated subset of: unpaywall, scihub, libgen, randombook", "Enabled/ordered download sources. Empty enables all."},
}

// writeLLMSFullConfiguration writes the environment-variable reference table.
func writeLLMSFullConfiguration(b *strings.Builder) {
	b.WriteString("## Configuration\n\n")
	b.WriteString("All configuration is via environment variables. Every variable is optional and no credentials are required; an unset variable uses its default. Values below mirror `internal/config/config.go` (`Load` defaults and `Validate` ranges).\n\n")
	b.WriteString("| Variable | Default | Valid range / values | Meaning |\n")
	b.WriteString("| --- | --- | --- | --- |\n")
	for _, v := range configEnvVars {
		fmt.Fprintf(b, "| `%s` | %s | %s | %s |\n", v.name, v.def, v.rangeStr, v.meaning)
	}
	b.WriteString("\n")
}

// writeLLMSFullDownloadSources documents the ordered download-source chain.
func writeLLMSFullDownloadSources(b *strings.Builder) {
	b.WriteString("## Download sources\n\n")
	b.WriteString("The `download` tool resolves a file through an ordered chain of sources. Which branch runs depends on the identifier supplied:\n\n")
	b.WriteString("- **Books (by `md5`):** tried against `libgen` (resolve the ads.php download key, then fetch from the CDN), then `randombook` as a fallback.\n")
	b.WriteString("- **Articles (by `doi`):** tried against `unpaywall` (open-access PDF, requires `LIBGEN_MCP_UNPAYWALL_EMAIL`), then `scihub` (`LIBGEN_MCP_SCIHUB_HOSTS`).\n")
	b.WriteString("- **Both `md5` and `doi` given:** article sources are tried first, then the book sources.\n\n")
	b.WriteString("`LIBGEN_MCP_SOURCES` selects and orders which sources take part; the recognized names in natural chain order are `unpaywall,scihub,libgen,randombook`. An empty value enables all.\n\n")
	b.WriteString("**Verification:** book (`md5`) downloads are MD5-verified against the requested hash (`verified:true`). DOI/article downloads are not hash-verified (`verified:false`).\n\n")
}

// writeLLMSFullTransports documents the stdio and HTTP transports.
func writeLLMSFullTransports(b *strings.Builder) {
	b.WriteString("## Transports\n\n")
	b.WriteString("- **stdio (default):** used by MCP clients (Claude Desktop, Claude Code, Cursor, VS Code, GitHub Copilot). No flags required.\n")
	b.WriteString("- **Streamable HTTP:** pass `--http host:port` (e.g. `--http :8080`) to serve over HTTP instead of stdio. The MCP handler is mounted at `/`, and a `GET /health` readiness endpoint returns HTTP 200 while the server is serving.\n\n")
}

// writeLLMSFullInstall writes a compact machine-oriented install recap that
// mirrors the guidance in writeLLMSTxt.
func writeLLMSFullInstall(b *strings.Builder) {
	b.WriteString("## Install (headless)\n\n")
	b.WriteString("No credentials are required. The recommended install is the prebuilt static binary (no dependencies): download the release asset for the user's OS and architecture from the Releases page and point `command` at its absolute path. Cursor, Claude Desktop, and Claude Code use an `mcpServers` key; VS Code and GitHub Copilot use a `servers` key (each entry also sets `\"type\": \"stdio\"`).\n\n")
	b.WriteString("Native binary (`mcpServers` form):\n\n")
	b.WriteString("```json\n")
	b.WriteString("{\n")
	b.WriteString("  \"mcpServers\": {\n")
	b.WriteString("    \"libgen\": {\n")
	b.WriteString("      \"command\": \"/usr/local/bin/libgen-mcp\"\n")
	b.WriteString("    }\n")
	b.WriteString("  }\n")
	b.WriteString("}\n")
	b.WriteString("```\n\n")
	b.WriteString("Docker alternative (no download; pulls the image on first run and runs over stdio) — useful when you cannot determine the user's OS and architecture:\n\n")
	b.WriteString("```json\n")
	b.WriteString("{\n")
	b.WriteString("  \"mcpServers\": {\n")
	b.WriteString("    \"libgen\": {\n")
	b.WriteString("      \"command\": \"docker\",\n")
	b.WriteString("      \"args\": [\"run\", \"-i\", \"--rm\", \"ghcr.io/jmrplens/libgen-mcp:latest\"]\n")
	b.WriteString("    }\n")
	b.WriteString("  }\n")
	b.WriteString("}\n")
	b.WriteString("```\n\n")
	b.WriteString("For VS Code / GitHub Copilot, use the `servers` key instead of `mcpServers` and add `\"type\": \"stdio\"` to the `libgen` entry.\n\n")
}

func writeLLMSFullTool(b *strings.Builder, tool *mcp.Tool) {
	fmt.Fprintf(b, "### %s\n\n", tool.Name)
	if tool.Title != "" {
		fmt.Fprintf(b, llmsBoldTitleFormat, tool.Title)
	}
	b.WriteString(compactToolDescription(tool.Description))
	b.WriteString("\n\n")
	writeInputSchema(b, tool.InputSchema)
	writeAnnotations(b, tool.Annotations)
	b.WriteString("\n")
}

func compactToolDescription(description string) string {
	desc := firstParagraph(description)
	if utf8.RuneCountInString(desc) <= maxFullDescRunes {
		return desc
	}
	if sentence := firstSentence(desc); sentence != "" && utf8.RuneCountInString(sentence) <= maxFullDescRunes {
		return sentence
	}
	return truncateRunes(desc, maxFullDescRunes)
}

// writeAnnotations writes tool annotation hints to the builder.
func writeAnnotations(b *strings.Builder, ann *mcp.ToolAnnotations) {
	if ann == nil {
		return
	}
	dest := false
	if ann.DestructiveHint != nil {
		dest = *ann.DestructiveHint
	}
	openWorld := true
	if ann.OpenWorldHint != nil {
		openWorld = *ann.OpenWorldHint
	}
	fmt.Fprintf(b, "Annotations: readOnly=%v, destructive=%v, idempotent=%v, openWorld=%v\n",
		ann.ReadOnlyHint, dest, ann.IdempotentHint, openWorld)
}

// writeInputSchema writes a compact representation of the tool's input schema.
func writeInputSchema(b *strings.Builder, schema any) {
	schemaMap, ok := schema.(map[string]any)
	if !ok {
		return
	}
	props, ok := schemaMap["properties"].(map[string]any)
	if !ok || len(props) == 0 {
		return
	}

	required := map[string]bool{}
	if reqList, isSlice := schemaMap["required"].([]any); isSlice {
		for _, r := range reqList {
			if s, isStr := r.(string); isStr {
				required[s] = true
			}
		}
	}

	b.WriteString("**Parameters:**\n\n")
	names := make([]string, 0, len(props))
	for name := range props {
		names = append(names, name)
	}
	sort.Strings(names)

	for _, name := range names {
		prop, isMap := props[name].(map[string]any)
		if !isMap {
			continue
		}
		typ := schemaTypeLabel(prop)
		desc, _ := prop["description"].(string)
		desc = strings.TrimSuffix(desc, ",required")
		req := ""
		if required[name] {
			req = " (required)"
		}
		if desc != "" {
			fmt.Fprintf(b, "- `%s` (%s)%s: %s\n", name, typ, req, desc)
		} else {
			fmt.Fprintf(b, "- `%s` (%s)%s\n", name, typ, req)
		}
	}
	b.WriteString("\n")
}

func schemaTypeLabel(schema map[string]any) string {
	types := schemaTypeValues(schema["type"])
	types = removeSchemaType(types, "null")
	if len(types) == 0 {
		if _, ok := schema["items"]; ok {
			return "array"
		}
		if _, ok := schema["properties"]; ok {
			return "object"
		}
		return "any"
	}
	if slices.Contains(types, "array") {
		items, _ := schema["items"].(map[string]any)
		itemType := schemaTypeLabel(items)
		if itemType == "" || itemType == "any" {
			return "array"
		}
		return "array of " + pluralSchemaType(itemType)
	}
	if len(types) == 1 {
		return types[0]
	}
	return strings.Join(types, " or ")
}

func schemaTypeValues(raw any) []string {
	switch value := raw.(type) {
	case string:
		return []string{value}
	case []any:
		values := make([]string, 0, len(value))
		for _, item := range value {
			text, ok := item.(string)
			if ok && strings.TrimSpace(text) != "" {
				values = append(values, text)
			}
		}
		return values
	default:
		return nil
	}
}

func removeSchemaType(types []string, remove string) []string {
	filtered := types[:0]
	for _, typ := range types {
		if typ != remove {
			filtered = append(filtered, typ)
		}
	}
	return filtered
}

func pluralSchemaType(typ string) string {
	if itemType, ok := strings.CutPrefix(typ, "array of "); ok {
		return "arrays of " + itemType
	}
	switch typ {
	case "integer":
		return "integers"
	case "number":
		return "numbers"
	case "string":
		return "strings"
	case "boolean":
		return "booleans"
	case "object":
		return "objects"
	default:
		if strings.Contains(typ, " or ") {
			return "values"
		}
		return typ + "s"
	}
}

// truncateRunes truncates s to at most maxRunes runes, appending "..." if truncated.
func truncateRunes(s string, maxRunes int) string {
	if utf8.RuneCountInString(s) <= maxRunes {
		return s
	}
	var size int
	for range maxRunes {
		_, w := utf8.DecodeRuneInString(s[size:])
		size += w
	}
	return s[:size] + "..."
}

// firstParagraph returns text up to the first blank-line paragraph break (\n\n).
// Used to cut tool descriptions at a natural boundary instead of mid-sentence.
func firstParagraph(s string) string {
	s = strings.TrimSpace(s)
	if before, _, ok := strings.Cut(s, "\n\n"); ok {
		return strings.TrimSpace(before)
	}
	return s
}

// firstSentence returns text up to the first sentence-ending period or newline.
// It skips common abbreviations (e.g., i.e., etc., vs.) to avoid false splits.
func firstSentence(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	if i := findSentenceEnd(s); i >= 0 {
		return s[:i+1]
	}
	return s
}

// abbreviations that should not be treated as sentence boundaries.
var abbreviations = []string{"e.g.", "i.e.", "etc.", "vs.", "approx.", "dept.", "est.", "govt.", "incl."}

// findSentenceEnd returns the index of the first ". " that is NOT part of a
// common abbreviation, or -1 if none found.
func findSentenceEnd(s string) int {
	offset := 0
	for {
		i := strings.Index(s[offset:], ". ")
		if i < 0 {
			return -1
		}
		pos := offset + i
		isAbbrev := false
		for _, abbr := range abbreviations {
			if len(abbr) <= pos+1 && s[pos+1-len(abbr):pos+1] == abbr {
				isAbbrev = true
				break
			}
		}
		if !isAbbrev {
			return pos
		}
		offset = pos + 2
	}
}

// writeGeneratedFile writes or checks generated content in the project root.
func writeGeneratedFile(name, content string, checkOnly bool) error {
	if !isGeneratedLLMSFile(name) {
		return fmt.Errorf("unexpected generated file %q", name)
	}
	dir, err := findProjectRoot()
	if err != nil {
		return err
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return err
	}
	defer func() { _ = root.Close() }()

	if checkOnly {
		existing, readErr := root.ReadFile(name)
		if readErr != nil {
			return readErr
		}
		if normalizeLineEndings(string(existing)) != normalizeLineEndings(content) {
			return fmt.Errorf("%s is out of date; run go run ./cmd/gen_llms/", name)
		}
		return nil
	}
	return root.WriteFile(name, []byte(content), 0o644)
}

func normalizeLineEndings(s string) string {
	return strings.ReplaceAll(s, "\r\n", "\n")
}

func isGeneratedLLMSFile(name string) bool {
	switch name {
	case llmsFileName, llmsFullFileName:
		return true
	default:
		return false
	}
}

// findProjectRoot walks up from cwd looking for go.mod.
func findProjectRoot() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for {
		if _, statErr := os.Stat(filepath.Join(dir, "go.mod")); statErr == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", errors.New("could not find project root (no go.mod found)")
		}
		dir = parent
	}
}
