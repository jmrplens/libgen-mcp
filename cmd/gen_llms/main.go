// Command gen_llms generates llms.txt and llms-full.txt files. It creates an
// in-memory MCP server with libgen-mcp's tools registered, introspects
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
	"errors"
	"flag"
	"fmt"
	"os"
	"slices"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/jmrplens/libgen-mcp/cmd/internal/mcpsurface"
	"github.com/jmrplens/libgen-mcp/internal/config"
)

const (
	// maxFullDescRunes caps the length of tool descriptions in llms-full.txt.
	// When a description exceeds it, generation falls back to its first sentence;
	// if that is still too long, the text is hard-truncated at the rune boundary.
	//
	// It sits above the longest description rather than below it on purpose. At
	// 600 this file — the one written for LLM discovery, and named "full" —
	// published "Download a file to a local directory." as the whole of the
	// download tool, dropping the source chains, resolve_only and the
	// untrusted-content warning. Four tools do not need protecting from their own
	// documentation; the cap exists to catch a runaway description, so it only has
	// to stay ahead of the real ones.
	maxFullDescRunes = 1600
	// maxSummaryDescRunes caps the one-line tool summaries in llms.txt. It is sized
	// to fit the first sentence of every current tool description whole: at 120 the
	// flagship search entry was published as "...md5 hash and downloa...", cut
	// mid-word in the index a model reads first.
	maxSummaryDescRunes   = 200
	llmsFileName          = "llms.txt"
	llmsFullFileName      = "llms-full.txt"
	llmsSummaryItemFormat = "- %s: %s\n"
	llmsBoldTitleFormat   = "**%s**\n\n"
	docsSiteURL           = "https://jmrplens.github.io/libgen-mcp/"
	repoBlobURL           = "https://github.com/jmrplens/libgen-mcp/blob/main/"
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
	promptList, err := listPrompts()
	if err != nil {
		return err
	}

	if writeErr := writeLLMSTxt(version, toolList, promptList, checkOnly); writeErr != nil {
		return writeErr
	}
	if writeErr := writeLLMSFullTxt(version, toolList, promptList, checkOnly); writeErr != nil {
		return writeErr
	}

	if checkOnly {
		fmt.Printf("Validated llms.txt and llms-full.txt\n")
		return nil
	}
	fmt.Printf("Generated llms.txt and llms-full.txt (%d tools, %d prompts)\n", len(toolList), len(promptList))
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

// newSession creates an in-memory MCP server+client session. It delegates to
// mcpsurface so this command and gen_lhm_manifest introspect the server exactly
// the same way.
func newSession(setupServer func(*mcp.Server) error) (session *mcp.ClientSession, cleanup func(), err error) {
	return mcpsurface.Session(setupServer)
}

// listTools returns the registered tools via a real tools/list round-trip,
// sorted into the natural workflow order the generated files present.
func listTools() ([]*mcp.Tool, error) {
	result, err := mcpsurface.Tools(mcpsurface.DocsConfig())
	if err != nil {
		return nil, err
	}
	sort.SliceStable(result, func(i, j int) bool {
		return toolOrder(result[i].Name) < toolOrder(result[j].Name)
	})
	return result, nil
}

// listPrompts returns the registered prompts via a real prompts/list round-trip,
// sorted by name. The prompts are half of what a client sees on connect, and
// describing them by hand here would drift the way the tool list used to.
func listPrompts() ([]*mcp.Prompt, error) {
	result, err := mcpsurface.Prompts(mcpsurface.DocsConfig())
	if err != nil {
		return nil, err
	}
	sort.SliceStable(result, func(i, j int) bool {
		return result[i].Name < result[j].Name
	})
	return result, nil
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

// toolNames returns the registered tool names as a comma-separated list, so the
// generated prose stays in sync with the actual tool set instead of hardcoding it.
func toolNames(toolList []*mcp.Tool) string {
	names := make([]string, len(toolList))
	for i, t := range toolList {
		names[i] = t.Name
	}
	return strings.Join(names, ", ")
}

// writeLLMSTxt generates the concise llms.txt overview.
func writeLLMSTxt(version string, toolList []*mcp.Tool, promptList []*mcp.Prompt, checkOnly bool) error {
	var b strings.Builder

	b.WriteString("# libgen-mcp\n\n")
	b.WriteString("> A Model Context Protocol (MCP) server that searches and downloads books, papers, comics, magazines and standards from Library Genesis for AI assistants.\n\n")
	fmt.Fprintf(&b, "libgen-mcp v%s is a single static Go binary that runs locally via stdio or remotely via HTTP transport.\n", version)
	fmt.Fprintf(&b, "It provides exactly %d MCP tools (%s) over the libgen.li family of mirrors. No account, token or credential is required. Cross-platform: Windows, Linux, macOS (amd64 + arm64).\n\n", len(toolList), toolNames(toolList))

	b.WriteString("Quick start:\n\n")
	b.WriteString("1. Download the prebuilt binary for your platform from the Releases page (recommended — no dependencies), or use Docker (see below)\n")
	b.WriteString("2. Add the server to your AI client configuration (see below)\n")
	b.WriteString("3. Start searching — no sign-up, API key or token is needed\n\n")

	b.WriteString("Installing for an AI assistant (headless):\n\n")
	b.WriteString("If you are an AI assistant setting this up for a user from just this repository URL, write the MCP client configuration directly. No credentials are required. The recommended install is the prebuilt static binary (no dependencies): download the release asset for the user's OS and architecture from the Releases page and point `command` at its absolute path. Cursor, Claude Desktop, and Claude Code use an `mcpServers` key; VS Code and GitHub Copilot use a `servers` key (each entry also sets `\"type\": \"stdio\"`). The `mcpServers` form with the native binary:\n\n")
	writeMcpServersJSON(&b, "/usr/local/bin/libgen-mcp", nil)
	b.WriteString("For VS Code / GitHub Copilot, use the `servers` key instead of `mcpServers` and add `\"type\": \"stdio\"` to the `libgen` entry.\n\n")
	b.WriteString("Docker alternative (no download; pulls the image on first run and runs over stdio) — useful when you cannot determine the user's OS and architecture: set `command` to `docker` and `args` to `[\"run\", \"-i\", \"--rm\", \"ghcr.io/jmrplens/libgen-mcp:latest\"]`.\n\n")
	b.WriteString("Claude Code (CLI): `claude mcp add libgen -- /usr/local/bin/libgen-mcp` (native binary), or `claude mcp add libgen -- docker run -i --rm ghcr.io/jmrplens/libgen-mcp:latest` (Docker).\n\n")

	// Derived from configEnvVars — the same list llms-full.txt tabulates, and the
	// one TestConfigEnvVarsCoversConfigGo holds against config.go. This summary
	// used to be written by hand and had fallen seven variables behind.
	b.WriteString("Configuration (environment variables, all optional):\n\n")
	for _, v := range configEnvVars() {
		fmt.Fprintf(&b, "- %s: %s (default: %s)\n", v.name, firstSentence(v.meaning), v.def)
	}
	b.WriteString("\n")

	b.WriteString("Tools:\n\n")
	for _, t := range toolList {
		desc := firstSentence(t.Description)
		desc = truncateRunes(desc, maxSummaryDescRunes)
		fmt.Fprintf(&b, llmsSummaryItemFormat, t.Name, desc)
	}
	b.WriteString("\n")

	writePromptSummary(&b, promptList)

	// Absolute URLs so the links resolve when llms.txt is fetched from its served
	// location (…/libgen-mcp/llms.txt), not just from the repo root. Doc pages point
	// at the rendered site; repo-only files point at their GitHub blob.
	b.WriteString("## Documentation\n\n")
	for _, p := range docPages(toolNames(toolList)) {
		writeLLMSLink(&b, p.enTitle, docsSiteURL+p.slug, p.enDesc)
	}
	writeLLMSLink(&b, "Security policy", repoBlobURL+"SECURITY.md", "Threat model and how to report a vulnerability privately")
	writeLLMSLink(&b, "Headless install", repoBlobURL+"llms-install.md", "Machine-readable install guide for AI assistants")

	// Half the site's URLs are Spanish. A single aggregate link to /es/ left them
	// invisible to anything consuming this file, which then had nine of nineteen
	// pages to work from and no way to know the rest existed.
	b.WriteString("\n## Documentación (Español)\n\n")
	for _, p := range docPages(toolNames(toolList)) {
		writeLLMSLink(&b, p.esTitle, docsSiteURL+"es/"+p.slug, p.esDesc)
	}

	b.WriteString("\n## Optional\n\n")
	writeLLMSLink(&b, "Full LLM reference", docsSiteURL+llmsFullFileName, "Generated companion reference with full tool schemas")
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
func writeLLMSFullTxt(version string, toolList []*mcp.Tool, promptList []*mcp.Prompt, checkOnly bool) error {
	var b strings.Builder

	b.WriteString("# libgen-mcp — Full Reference\n\n")
	fmt.Fprintf(&b, "> Version %s | %d tools | %d prompts\n\n", version, len(toolList), len(promptList))

	b.WriteString("## Tools\n\n")
	fmt.Fprintf(&b, "libgen-mcp exposes %d tools over the libgen.li family of mirrors. No account or token is required.\n\n", len(toolList))
	for _, tool := range toolList {
		writeLLMSFullTool(&b, tool)
	}

	writeLLMSFullPrompts(&b, promptList)
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

// docPage is one documentation page as llms.txt lists it, in both languages. The
// slug is shared: the site keeps English slugs across locales, so the ES URL is
// the EN one under /es/.
type docPage struct {
	slug    string
	enTitle string
	enDesc  string
	esTitle string
	esDesc  string
}

// docPages returns the documented pages in reading order. Both language sections
// are generated from this one list so a page cannot be added to one and forgotten
// in the other. toolNameList carries the tool names into the Tools description,
// which is derived from the live tool set rather than hardcoded.
//
//nolint:misspell // The es* fields are Spanish; misspell reads them as English.
func docPages(toolNameList string) []docPage {
	return []docPage{
		{
			"getting-started/", "Getting started", "Installation and first-run guide",
			"Primeros pasos", "Guía de instalación y primera ejecución",
		},
		{
			"configuration/", "Configuration", "Full environment-variable configuration reference",
			"Configuración", "Referencia completa de configuración por variables de entorno",
		},
		{
			"tools/", "Tools", "Per-tool reference for " + toolNameList,
			"Herramientas", "Referencia por herramienta de " + toolNameList,
		},
		{
			"architecture/", "Architecture", "Internal architecture, mirror discovery and download sources",
			"Arquitectura", "Arquitectura interna, descubrimiento de mirrors y fuentes de descarga",
		},
		{
			"sources/", "Download sources", "Per-source reference for every download source: corpus, resolve mechanics, measured traps, and keys",
			"Fuentes de descarga", "Referencia por fuente de cada fuente de descarga: corpus, mecánica de resolución, trampas medidas y claves",
		},
		{
			"how-search-works/", "How search works", "Catalog-first search, and when and how it escalates to the extra sources",
			"Cómo funciona la búsqueda", "Búsqueda con el catálogo primero, y cuándo y cómo escala a las fuentes extra",
		},
		{
			"eval-results/", "LLM eval results", "Results of driving a real model over MCP against the live site, scenario by scenario",
			"Resultados de la evaluación con LLM", "Resultados de conducir un modelo real sobre MCP contra el sitio en vivo, escenario a escenario",
		},
		{
			"troubleshooting/", "Troubleshooting", "Common setup and runtime issues",
			"Solución de problemas", "Problemas habituales de configuración y ejecución",
		},
		{
			"responsible-use/", "Responsible use", "Why the open-access providers are tried first, and what the server refuses to serve",
			"Uso responsable", "Por qué se prueban primero los proveedores de acceso abierto, y qué se niega a servir el servidor",
		},
		{
			"privacy/", "Privacy policy", "No telemetry; requests go only to the Library Genesis mirrors and the search and download sources a call invokes",
			"Política de privacidad", "Sin telemetría; las peticiones van solo a los mirrors de Library Genesis y a las fuentes que invoca cada llamada",
		},
	}
}

// writePromptSummary writes the one-line prompt index for llms.txt. Prompts are
// the other half of what a client sees on connect, and a file that lists only the
// tools understates the surface by four entries.
func writePromptSummary(b *strings.Builder, promptList []*mcp.Prompt) {
	if len(promptList) == 0 {
		return
	}
	b.WriteString("Prompts (guided workflows a client can offer by name):\n\n")
	for _, p := range promptList {
		desc := truncateRunes(firstSentence(p.Description), maxSummaryDescRunes)
		fmt.Fprintf(b, llmsSummaryItemFormat, p.Name, desc)
	}
	b.WriteString("\n")
}

// writeLLMSFullPrompts writes the full prompt reference, including each prompt's
// arguments, so a client can invoke one without a round-trip to prompts/list.
func writeLLMSFullPrompts(b *strings.Builder, promptList []*mcp.Prompt) {
	if len(promptList) == 0 {
		return
	}
	b.WriteString("## Prompts\n\n")
	fmt.Fprintf(b, "libgen-mcp registers %d prompts. Each is a guided workflow over the tools above; a client surfaces them by name.\n\n", len(promptList))
	for _, p := range promptList {
		fmt.Fprintf(b, "### %s\n\n", p.Name)
		if title := strings.TrimSpace(p.Title); title != "" {
			fmt.Fprintf(b, llmsBoldTitleFormat, title)
		}
		if desc := strings.TrimSpace(p.Description); desc != "" {
			b.WriteString(compactToolDescription(desc))
			b.WriteString("\n\n")
		}
		writePromptArguments(b, p.Arguments)
	}
}

// writePromptArguments writes one bullet per prompt argument, marking the
// required ones.
func writePromptArguments(b *strings.Builder, args []*mcp.PromptArgument) {
	if len(args) == 0 {
		b.WriteString("Arguments: none\n\n")
		return
	}
	b.WriteString("Arguments:\n\n")
	for _, a := range args {
		req := ""
		if a.Required {
			req = " (required)"
		}
		if desc := strings.TrimSpace(a.Description); desc != "" {
			fmt.Fprintf(b, "- `%s`%s: %s\n", a.Name, req, desc)
		} else {
			fmt.Fprintf(b, "- `%s`%s\n", a.Name, req)
		}
	}
	b.WriteString("\n")
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

// knownSourcesList renders config.KnownSources as a human-readable, comma-space
// separated list. Every place that names the recognized download sources derives
// them from it, so the generated reference cannot drift when a source is added or
// the chain is reordered.
func knownSourcesList() string {
	return strings.Join(config.KnownSources, ", ")
}

// boolEnvRange is the accepted-values column shared by every boolean setting,
// naming the four literals config.envBool recognizes.
const boolEnvRange = "1/true/0/false"

// configEnvVars is the authoritative Configuration reference. Keep it in sync
// with internal/config/config.go: default values come from config.Load and the
// valid ranges come from config.Config.Validate. All variables are optional and
// no credentials are required. TestConfigEnvVarsCoversConfigGo fails if
// config.go grows a LIBGEN_MCP_* variable that is missing here.
func configEnvVars() []envVarDoc {
	return []envVarDoc{
		{"LIBGEN_MIRROR", "auto-discovered", "http/https URL with a host", "Force a specific mirror base URL, e.g. https://libgen.li. Empty means the mirror is auto-discovered."},
		{"LIBGEN_MCP_DOWNLOAD_DIR", "~/Downloads", "writable directory path", "Download destination directory. Created if missing; must be writable."},
		{"LIBGEN_MCP_TIMEOUT", "30s", "(0, 10m]", "Timeout per HTTP request, as a Go duration string (e.g. 30s, 2m)."},
		{"LIBGEN_MCP_LOG_LEVEL", "info", "debug, info, warn or error", "Logging verbosity."},
		{"LIBGEN_MCP_RATE_RPS", "1", "(0, 20]", "Allowed outbound requests per second."},
		{"LIBGEN_MCP_RATE_BURST", "1", "[1, 100]", "Maximum rate-limiter burst."},
		{"LIBGEN_MCP_MAX_DOWNLOAD_BYTES", "0", "[0, 53687091200] (0 = no limit, ceiling 50 GiB)", "Maximum download size in bytes."},
		{"LIBGEN_MCP_MAX_CONCURRENT_DOWNLOADS", "2", "[1, 16]", "Number of simultaneous downloads."},
		{"LIBGEN_MCP_RETRY_ATTEMPTS", "3", "[1, 10]", "Retries per request."},
		{"LIBGEN_MCP_DOWNLOAD_START_RETRY_WAITS", "5s,5s,5s,10s,10s,10s,15s", "comma-separated Go durations; each in (0, 10m]; at most 20", "Staged waits between attempts to get a download to begin (resolve/connect/first byte); N waits = N+1 attempts (~60s by default)."},
		{"LIBGEN_MCP_DOWNLOAD_STALL_TIMEOUT", "60s", "(0, 1h]", "Progress-resetting stall window while streaming: a download is cut only if no bytes arrive for this long, so a slow-but-progressing transfer is never killed."},
		{"LIBGEN_MCP_DOWNLOAD_RETRY_EVERY_SOURCE", "false", boolEnvRange, "Give every download source the full start-retry schedule. By default the schedule is spent only on the last source that can serve the item, so a source that is down does not hold up one that is not."},
		{"LIBGEN_MCP_ALLOW_PRIVATE_ADDRESSES", "false", boolEnvRange, "Allow outbound connections to loopback, link-local, private and carrier-grade-NAT addresses. Off by default: nearly every URL the server fetches was supplied by a third party (a publisher link deposited with Crossref, a repository URL republished by an open-access index), so without the block anyone able to place a URL in such an index could aim the server at your LAN or at a cloud instance-metadata endpoint. Set it only to point LIBGEN_MIRROR at a mirror on your own network."},
		{"LIBGEN_MCP_UNPAYWALL_EMAIL", "empty (unpaywall disabled)", "empty, or an email with @ and a dotted domain", "Contact email for the Unpaywall API (article/DOI downloads). Empty disables the unpaywall source — its API rejects requests without an email — so set your own address to enable it."},
		{"LIBGEN_MCP_SCIHUB_HOSTS", "sci-hub.ee, sci-hub.se, sci-hub.st, sci-hub.ru, sci-hub.wf", "comma-separated bare hosts (no scheme, no path)", "Ordered Sci-Hub mirror hosts, tried in order until one serves an article."},
		{"LIBGEN_MCP_ANNAS_KEY", "empty (keyless IPFS)", "Anna's Archive account secret string", "Optional Anna's Archive membership key enabling the member fast-download API for books. Empty keeps the annas source keyless (IPFS only). Requires an active paid membership; an unset, expired or rejected key falls back to the keyless IPFS path."},
		{"LIBGEN_MCP_CORE_KEY", "empty (core disabled)", "empty, or a CORE (core.ac.uk) API key", "Optional API key (free registration) enabling the core open-access article source. Empty leaves core out of the chain, mirroring how an empty LIBGEN_MCP_UNPAYWALL_EMAIL disables unpaywall. The key is sent only to api.core.ac.uk, never with the resolved file URL."},
		{"LIBGEN_MCP_SOURCES", "empty (all enabled)", "comma-separated subset of: " + knownSourcesList(), "Which download sources take part. Empty enables all. The chain order is fixed (the order above); this only removes sources from it."},
		{"LIBGEN_MCP_REMOTE_DOWNLOADS", "false", boolEnvRange, "Force the download tool to always return a direct link (a resource_link + resolved object) instead of saving a file. For a hosted stdio deployment (e.g. behind mcp-proxy) whose disk the client cannot reach; --http implies it."},
		{"LIBGEN_MCP_READ_MAX_CHARS", "6000", "[500, 200000]", "Characters the read tool returns per call when a call omits max_chars."},
		{"LIBGEN_MCP_READ_DEFAULT_PAGES", "5", "[1, 200]", "PDF pages the read tool returns per call when a call omits max_pages."},
		{"LIBGEN_MCP_READ_CACHE_BYTES", "536870912 (512 MiB)", "[1048576, 53687091200]", "Total-size cap of the server-side temp cache that lets successive read pages reuse one fetch; the least recently used files past it are evicted."},
		{"LIBGEN_MCP_READ_CACHE_TTL", "10m", "[1s, 24h]", "How long an unreferenced read temp file lingers before eviction."},
		{"LIBGEN_MCP_ENRICH", "true", boolEnvRange, "Deployment kill-switch for get_details' opt-in Crossref/OpenLibrary enrichment. Default true only allows it — a call still has to pass enrich: true. Set false to forbid it entirely."},
		{"LIBGEN_MCP_CONFIRM_DOWNLOADS", "true", boolEnvRange, "Ask the user to approve each file that download writes to disk. Only ever consulted when the client advertised elicitation — one that cannot be asked is never prompted. Set false to save without prompting: the deployment-wide form of download's skip_confirmation argument and of the prompt's own \"stop asking for this session\" checkbox."},
		{"LIBGEN_MCP_EXTRA_SOURCES", "auto", "auto, always, never", "When the extra searchers (Anna's Archive, arXiv, Crossref, OpenLibrary, Project Gutenberg, dblp, PubMed, ERIC) are consulted. auto: only when the Library Genesis catalog returns nothing or fails. always: on every search, alongside the catalog. never: catalog only, even on a miss."},
	}
}

// writeLLMSFullConfiguration writes the environment-variable reference table.
func writeLLMSFullConfiguration(b *strings.Builder) {
	b.WriteString("## Configuration\n\n")
	b.WriteString("All configuration is via environment variables. Every variable is optional and no credentials are required; an unset variable uses its default. Values below mirror `internal/config/config.go` (`Load` defaults and `Validate` ranges).\n\n")
	b.WriteString("| Variable | Default | Valid range / values | Meaning |\n")
	b.WriteString("| --- | --- | --- | --- |\n")
	for _, v := range configEnvVars() {
		fmt.Fprintf(b, "| `%s` | %s | %s | %s |\n", v.name, v.def, v.rangeStr, v.meaning)
	}
	b.WriteString("\n")
}

// writeLLMSFullDownloadSources documents the ordered download-source chain.
func writeLLMSFullDownloadSources(b *strings.Builder) {
	b.WriteString("## Download sources\n\n")
	b.WriteString("The `download` tool resolves a file through an ordered chain of sources. Which branch runs depends on the identifier supplied:\n\n")
	b.WriteString("- **Books (by `md5`):** tried against `libgen` (resolve the ads.php download key, then fetch from the CDN), then `randombook`, then `annas` (keyless IPFS, or member fast-download when `LIBGEN_MCP_ANNAS_KEY` is set) as fallbacks.\n")
	b.WriteString("- **Books (by `isbn`):** the open-access book sources — `oapen` (openly licensed scholarly monographs) then `archive` (public-domain Internet Archive scans, found via OpenLibrary and served only when both OpenLibrary and archive.org report the scan as freely downloadable rather than lending-restricted).\n")
	b.WriteString("- **Articles (by `doi`):** the open-access providers first — `unpaywall` (requires `LIBGEN_MCP_UNPAYWALL_EMAIL`), then `openalex` (the same open-access index, keyless), then `europepmc` (Europe PMC open-access full text), then `biorxiv` (`10.1101` preprints), then `rfc` (`10.17487` RFCs, served as text by the RFC Editor), then `nist` (`10.6028` NIST publications), then `dagstuhl` (`10.4230` LIPIcs/OASIcs proceedings and Dagstuhl Reports), then `acl` (`10.18653/v1`/`10.3115/v1` ACL Anthology papers), then `zenodo` (`10.5281/zenodo` deposits), then `scielo` (`10.1590` SciELO Brazil articles), then `fao` (`10.4060` FAO Knowledge Repository documents), then `fatcat` (Internet Archive Scholar), then `core` (requires `LIBGEN_MCP_CORE_KEY`) — then `crossref` (the full-text link the publisher itself deposited with Crossref, probed before use; it is a publisher-link resolver, not an open-access index, so it may hold nothing an anonymous client can fetch), then `oapen` (monograph DOIs) — then the shadow-library fallbacks `scihub` (`LIBGEN_MCP_SCIHUB_HOSTS`) and `scidb` (Anna's Archive SciDB viewer).\n")
	b.WriteString("- **Both `md5` and `doi` given:** article sources are tried first, then the book sources.\n\n")
	// The recognized names come from config.KnownSources rather than a literal, so
	// this reference cannot drift when a source is added or the chain is reordered.
	fmt.Fprintf(b, "`LIBGEN_MCP_SOURCES` selects which sources take part; it never reorders them. The recognized names, in the fixed chain order, are `%s`. An empty value enables all.\n\n", strings.Join(config.KnownSources, ","))
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
	writeMcpServersJSON(b, "/usr/local/bin/libgen-mcp", nil)
	b.WriteString("Docker alternative (no download; pulls the image on first run and runs over stdio) — useful when you cannot determine the user's OS and architecture:\n\n")
	writeMcpServersJSON(b, "docker", []string{"run", "-i", "--rm", "ghcr.io/jmrplens/libgen-mcp:latest"})
	b.WriteString("For VS Code / GitHub Copilot, use the `servers` key instead of `mcpServers` and add `\"type\": \"stdio\"` to the `libgen` entry.\n\n")
}

// writeMcpServersJSON writes a fenced ```json block holding an `mcpServers`
// entry for the libgen server, followed by a blank line. When args is empty
// only a `command` field is emitted; otherwise an `args` array is appended.
// Centralizing the block keeps the generated files free of duplicated literals.
func writeMcpServersJSON(b *strings.Builder, command string, args []string) {
	b.WriteString("```json\n")
	b.WriteString("{\n")
	b.WriteString("  \"mcpServers\": {\n")
	b.WriteString("    \"libgen\": {\n")
	if len(args) == 0 {
		fmt.Fprintf(b, "      \"command\": %q\n", command)
	} else {
		fmt.Fprintf(b, "      \"command\": %q,\n", command)
		b.WriteString("      \"args\": [")
		for i, arg := range args {
			if i > 0 {
				b.WriteString(", ")
			}
			fmt.Fprintf(b, "%q", arg)
		}
		b.WriteString("]\n")
	}
	b.WriteString("    }\n")
	b.WriteString("  }\n")
	b.WriteString("}\n")
	b.WriteString("```\n\n")
}

func writeLLMSFullTool(b *strings.Builder, tool *mcp.Tool) {
	fmt.Fprintf(b, "### %s\n\n", tool.Name)
	if tool.Title != "" {
		fmt.Fprintf(b, llmsBoldTitleFormat, tool.Title)
	}
	b.WriteString(compactToolDescription(tool.Description))
	b.WriteString("\n\n")
	writeInputSchema(b, tool.InputSchema)
	writeOutputSchema(b, tool.OutputSchema)
	writeAnnotations(b, tool.Annotations)
	b.WriteString("\n")
}

func compactToolDescription(description string) string {
	desc := strings.TrimSpace(description)
	// A description that fits is reproduced whole, blank lines and all. Collapsing
	// to the first paragraph unconditionally — as this did — meant a tool that
	// structured its description into paragraphs silently lost every one after the
	// first, in the file written for LLM discovery, with nothing to signal it.
	if utf8.RuneCountInString(desc) <= maxFullDescRunes {
		return desc
	}
	// Over the cap, shed structure progressively rather than cutting mid-word.
	if para := firstParagraph(desc); utf8.RuneCountInString(para) <= maxFullDescRunes {
		return para
	}
	if sentence := firstSentence(desc); sentence != "" && utf8.RuneCountInString(sentence) <= maxFullDescRunes {
		return sentence
	}
	return truncateRunes(desc, maxFullDescRunes)
}

// writeAnnotations writes the tool annotation hints the server actually declares.
//
// A hint the server left unset is omitted rather than printed at its Go zero
// value. destructiveHint and idempotentHint are pointers precisely so "not
// stated" is distinguishable from "stated false", and search, get_details and
// read set neither — publishing "destructive=false, idempotent=false" for them
// turned an absent declaration into an asserted fact in the file written for
// models to read.
func writeAnnotations(b *strings.Builder, ann *mcp.ToolAnnotations) {
	if ann == nil {
		return
	}
	parts := []string{fmt.Sprintf("readOnly=%v", ann.ReadOnlyHint)}
	if ann.DestructiveHint != nil {
		parts = append(parts, fmt.Sprintf("destructive=%v", *ann.DestructiveHint))
	}
	if ann.IdempotentHint {
		parts = append(parts, "idempotent=true")
	}
	openWorld := true
	if ann.OpenWorldHint != nil {
		openWorld = *ann.OpenWorldHint
	}
	parts = append(parts, fmt.Sprintf("openWorld=%v", openWorld))
	fmt.Fprintf(b, "Annotations: %s\n", strings.Join(parts, ", "))
}

// writeInputSchema writes a compact representation of the tool's input schema.
func writeInputSchema(b *strings.Builder, schema any) {
	writeSchemaSection(b, "**Parameters:**", schema)
}

// writeOutputSchema writes a compact representation of the tool's output schema,
// so this file documents what a call returns and not only what it takes. Without
// it a reader could not learn that get_details hands back ready-to-paste
// citations, that search returns a separate open_access array, or that download
// reports whether the bytes were MD5-verified — the very fields that decide
// whether the tool is worth calling. It costs nothing at runtime: this is a
// static document, not part of the per-request tool definitions.
func writeOutputSchema(b *strings.Builder, schema any) {
	writeSchemaSection(b, "**Returns:**", schema)
}

// writeSchemaSection writes one titled property list from a JSON schema. It is
// the shared body of writeInputSchema and writeOutputSchema; a nil, non-object or
// property-less schema writes nothing at all, so a tool without an output schema
// simply has no Returns block.
func writeSchemaSection(b *strings.Builder, heading string, schema any) {
	schemaMap, ok := schema.(map[string]any)
	if !ok {
		return
	}
	props, ok := schemaMap["properties"].(map[string]any)
	if !ok || len(props) == 0 {
		return
	}

	required := schemaRequiredSet(schemaMap)

	b.WriteString(heading + "\n\n")
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
		writeSchemaProperty(b, name, prop, required[name])
	}
	b.WriteString("\n")
}

// schemaRequiredSet returns the set of property names listed in the schema's
// "required" array. It returns an empty set when the field is absent or malformed.
func schemaRequiredSet(schemaMap map[string]any) map[string]bool {
	required := map[string]bool{}
	reqList, ok := schemaMap["required"].([]any)
	if !ok {
		return required
	}
	for _, r := range reqList {
		if s, isStr := r.(string); isStr {
			required[s] = true
		}
	}
	return required
}

// writeSchemaProperty writes one parameter bullet describing a single schema
// property, marking it "(required)" when required and appending its description
// when present.
func writeSchemaProperty(b *strings.Builder, name string, prop map[string]any, required bool) {
	typ := schemaTypeLabel(prop)
	desc, _ := prop["description"].(string)
	desc = strings.TrimSuffix(desc, ",required")
	if allowed := schemaEnumLabel(prop); allowed != "" {
		desc = strings.TrimRight(strings.TrimSpace(desc), ".") + ". Allowed values: " + allowed
	}
	req := ""
	if required {
		req = " (required)"
	}
	if desc != "" {
		fmt.Fprintf(b, "- `%s` (%s)%s: %s\n", name, typ, req, desc)
	} else {
		fmt.Fprintf(b, "- `%s` (%s)%s\n", name, typ, req)
	}
}

// schemaEnumLabel renders a property's allowed values, reading an array
// property's constraint off its items. It returns "" when the property is
// unconstrained. Sourcing the list from the emitted schema rather than from the
// description means the generated reference states exactly what the server will
// accept, and cannot drift from it.
func schemaEnumLabel(prop map[string]any) string {
	enum, ok := prop["enum"].([]any)
	if !ok || len(enum) == 0 {
		if items, isMap := prop["items"].(map[string]any); isMap {
			return schemaEnumLabel(items)
		}
		return ""
	}
	parts := make([]string, len(enum))
	for i, v := range enum {
		parts[i] = fmt.Sprintf("%v", v)
	}
	return strings.Join(parts, ", ")
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
// It backs up to the last space so the cut lands between words: the summary line
// is the first thing a model reads about a tool, and "md5 hash and downloa..."
// reads as a broken file rather than an abbreviated one. A run of maxRunes with no
// space in it is cut where it falls, since there is no better boundary to find.
func truncateRunes(s string, maxRunes int) string {
	if utf8.RuneCountInString(s) <= maxRunes {
		return s
	}
	var size int
	for range maxRunes {
		_, w := utf8.DecodeRuneInString(s[size:])
		size += w
	}
	cut := s[:size]
	if i := strings.LastIndexByte(cut, ' '); i > 0 {
		cut = cut[:i]
	}
	return strings.TrimRight(cut, " ,;:") + "..."
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

// findProjectRoot walks up to the directory holding go.mod, so the command works
// from anywhere in the repository.
func findProjectRoot() (string, error) {
	return mcpsurface.ProjectRoot()
}
