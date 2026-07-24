---
name: create-mcp-tool
description: "Add a new MCP tool to libgen-mcp end-to-end: input/output structs with jsonschema descriptions, a recovery-wrapped handler, Markdown rendering, tests, and docs. Use when extending the tool surface. Prefer deepening an existing tool first."
---

# Create MCP Tool — libgen-mcp

Workflow for adding a tool to libgen-mcp. The surface is deliberately small
(`search`, `get_details`, `download`, `read`). **Before adding a tool, check
whether the capability belongs on an existing one** — a new argument, a new
download source, or a new discovery provider is almost always the right move.
Add a tool only when the capability is genuinely distinct.

## Where tools live

Everything is in `internal/tools/`:

```text
internal/tools/
├── tools.go       # Register(): AddTool calls, input/output structs, schemas
├── read.go        # The read tool's handler + types (example of a fuller tool)
├── markdown.go    # mdCell / fencedBlock + Markdown renderers
├── citations.go   # BibTeX/RIS helpers used by get_details
├── elicit.go      # Client elicitation (per-call secrets)
└── *_test.go      # Table-driven tests with httptest / injected seams
```

## Step 1: Define input/output structs

Add them near the related tool in `tools.go` (or a new file in `package tools`).
**Every** field carries a `jsonschema` description; this is enforced by
`make audit-surface-quality`.

```go
// FooInput is the argument set for the foo tool.
type FooInput struct {
    MD5  string `json:"md5,omitempty" jsonschema:"file md5 hash from a search result"`
    Mode string `json:"mode,omitempty" jsonschema:"a single value: fast or thorough (default fast),enum=fast,enum=thorough"`
}

// FooOutput is the foo tool's structured result.
type FooOutput struct {
    NextSteps []string `json:"next_steps,omitempty" jsonschema:"suggested follow-up tool calls"`
    Result    string   `json:"result" jsonschema:"the resolved value"`
}
```

Rules:

- Optional fields use `,omitempty`; required fields add `,required` to the tag.
- Constrain closed sets with `enum=a,enum=b`; the surface audit fails on an
  empty enum.
- Nested output types (structs referenced by a field) need `jsonschema`
  descriptions on **their** fields too — the audit walks `$defs`.
- No vendor prefix on names; keep them short and literal.

## Step 2: Write the handler

Handlers have the signature `mcp.ToolHandlerFor[In, Out]` and return
`(*mcp.CallToolResult, Out, error)`. Build human-readable Markdown with
`markdownResult`, and pass any untrusted external text through `mdCell` /
`fencedBlock`.

```go
// fooHandler resolves a foo request against the client.
func fooHandler(c *libgen.Client, cfg *config.Config) mcp.ToolHandlerFor[FooInput, FooOutput] {
    return func(ctx context.Context, _ *mcp.CallToolRequest, in FooInput) (*mcp.CallToolResult, FooOutput, error) {
        v, err := c.Foo(ctx, in.MD5)
        if err != nil {
            return nil, FooOutput{}, fmt.Errorf("foo %s: %w", in.MD5, err)
        }
        out := FooOutput{Result: v, NextSteps: []string{"download this record by its md5"}}
        return markdownResult("**Result:** " + mdCell(v)), out, nil
    }
}
```

Error handling:

- Return a wrapped `error` (`fmt.Errorf("op: %w", err)`) for real failures.
- Do not handle panics yourself — `withRecovery` (Step 3) converts them into
  `IsError` results and meters the call.

## Step 3: Register the tool

Add an `mcp.AddTool` call inside `Register` in `tools.go`, wrapped with
`withRecovery`:

```go
mcp.AddTool(server, &mcp.Tool{
    Name:        "foo",
    Title:       "Foo a record",
    Description: "One or two sentences on what foo does, when to use it, and " +
        "what it returns. Cross-reference siblings with 'See also: search, download.'",
    Annotations: &mcp.ToolAnnotations{
        Title: "Foo a record", ReadOnlyHint: true, OpenWorldHint: &truthy,
    },
}, withRecovery("foo", fooHandler(client, cfg)))
```

- Set `ReadOnlyHint` for read-only tools; use `DestructiveHint`/`IdempotentHint`
  for writes. Set `OpenWorldHint` (`&truthy`) when the tool reaches the network.
- If the input schema needs runtime shaping (e.g. an enum restricted to enabled
  sources, like `download`), build it with `jsonschema.For[FooInput]` and set
  `InputSchema` explicitly — see `downloadInputSchema`.

## Step 4: Markdown rendering

Render results in `markdown.go` (or inline for something small). Always escape
external strings:

- Table cells → `mdCell(s)`
- Code/verbatim blocks → `fencedBlock(lang, content)`

Never interpolate an untrusted title, author, or filename into Markdown
directly.

## Step 5: Tests

Add table-driven tests in `internal/tools/`. Point the client at an `httptest`
server or inject a fake source/provider via a seam; never hit the network.

```go
func TestFooHandler_Success(t *testing.T) { /* happy path */ }
func TestFooHandler_Error(t *testing.T)   { /* wrapped error path */ }
func TestFooMarkdown_EscapesUntrusted(t *testing.T) { /* mdCell escaping */ }
```

Cover the success path, the error path, and the Markdown rendering (including
untrusted-content escaping). Every test function needs a doc comment.

## Step 6: Regenerate generated artifacts & docs

The tool surface feeds generated files and the docs site:

```bash
go run ./cmd/gen_llms/          # refresh llms.txt / llms-full.txt
```

Then update the docs (both languages — see the update-starlight-docs skill):

- `docs/tools.md` (developer reference)
- `site/src/content/docs/tools.mdx` and `site/src/content/docs/es/tools.mdx`

## Step 7: Verify

Run the full gate set:

```bash
golangci-lint fmt --diff
golangci-lint run
go vet ./...
go run ./cmd/godoc_tool/ audit --include-tests --fail-on-findings
go test ./internal/tools/ -count=1
go test -race ./...
make cover-check
make audit-surface-quality
make check-llms
make check-md-tables
make check-doc-links
```

## Validation Checklist

- [ ] Capability genuinely warrants a new tool (not a new arg/source/provider)
- [ ] Input/output structs: every field has a `jsonschema` description; enums
      constrained; nested types documented too
- [ ] Handler wrapped with `withRecovery`; errors wrapped with `%w`
- [ ] Correct annotations (read-only vs write; `OpenWorldHint` for network)
- [ ] Untrusted text escaped with `mdCell` / `fencedBlock`
- [ ] Tests cover success, error, and Markdown escaping; each has a doc comment
- [ ] `gen_llms` re-run; `docs/` + `site/` (EN & ES) updated
- [ ] Every gate in Step 7 exits 0
