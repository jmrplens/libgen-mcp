package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// objectSchema builds a minimal object JSON Schema (as a client sees it) with
// the given properties, for exercising the schema walker.
func objectSchema(props map[string]any) map[string]any {
	return map[string]any{"type": "object", "properties": props}
}

// TestAuditMetadata_Clean verifies a well-formed tool produces no metadata
// violations.
func TestAuditMetadata_Clean(t *testing.T) {
	tool := &mcp.Tool{
		Name:        "search",
		Title:       "Search",
		Description: "A sufficiently long description for the tool.",
		Annotations: &mcp.ToolAnnotations{},
		InputSchema: objectSchema(nil),
	}
	if vs := auditMetadata(tool); len(vs) != 0 {
		t.Fatalf("auditMetadata() = %+v, want none", vs)
	}
}

// TestAuditMetadata_Violations checks each metadata rule fires: missing title,
// nil annotations, short description, and a non-object input schema.
func TestAuditMetadata_Violations(t *testing.T) {
	tool := &mcp.Tool{
		Name:        "bad",
		Description: "too short",
		InputSchema: map[string]any{"type": "string"},
	}
	got := categorySet(auditMetadata(tool))
	for _, want := range []string{"title", "annotations", "description", "input-schema"} {
		if !got[want] {
			t.Errorf("auditMetadata() missing category %q; got %v", want, got)
		}
	}
}

// TestAuditMetadata_InputSchemaNotMap flags an InputSchema that is not a JSON
// object at all.
func TestAuditMetadata_InputSchemaNotMap(t *testing.T) {
	tool := &mcp.Tool{
		Name:        "bad",
		Title:       "Bad",
		Description: "A sufficiently long description for the tool.",
		Annotations: &mcp.ToolAnnotations{},
		InputSchema: "not-a-map",
	}
	if !categorySet(auditMetadata(tool))["input-schema"] {
		t.Fatal("expected input-schema violation for non-map InputSchema")
	}
}

// TestAuditSchema_MissingDescription reports a named property with no
// description, including one nested inside array items and $defs.
func TestAuditSchema_MissingDescription(t *testing.T) {
	schema := objectSchema(map[string]any{
		"documented":   map[string]any{"type": "string", "description": "ok"},
		"undocumented": map[string]any{"type": "string"},
		"list": map[string]any{
			"type":        "array",
			"description": "a list",
			"items": objectSchema(map[string]any{
				"nested": map[string]any{"type": "string"}, // missing
			}),
		},
	})
	vs := auditSchema("tool", "output", schema)
	names := detailSet(vs)
	if !hasSubstr(names, `"undocumented"`) {
		t.Errorf("expected undocumented top-level field flagged; got %v", names)
	}
	if !hasSubstr(names, `"nested"`) {
		t.Errorf("expected nested-in-items field flagged; got %v", names)
	}
	if hasSubstr(names, `"documented"`) {
		t.Errorf("documented field should not be flagged; got %v", names)
	}
}

// TestAuditSchema_Defs walks $defs and definitions blocks.
func TestAuditSchema_Defs(t *testing.T) {
	schema := map[string]any{
		"type":       "object",
		"properties": map[string]any{},
		"$defs": map[string]any{
			"Rec": objectSchema(map[string]any{
				"field": map[string]any{"type": "string"}, // missing description
			}),
		},
	}
	if len(auditSchema("tool", "input", schema)) == 0 {
		t.Fatal("expected a violation for the undocumented $defs field")
	}
}

// TestAuditSchema_EmptyEnum flags an enum that constrains no values and accepts
// a populated one.
func TestAuditSchema_EmptyEnum(t *testing.T) {
	empty := objectSchema(map[string]any{
		"mode": map[string]any{"type": "string", "description": "d", "enum": []any{}},
	})
	if !categorySet(auditSchema("tool", "input", empty))["empty-enum"] {
		t.Error("expected empty-enum violation")
	}
	full := objectSchema(map[string]any{
		"mode": map[string]any{"type": "string", "description": "d", "enum": []any{"a", "b"}},
	})
	if categorySet(auditSchema("tool", "input", full))["empty-enum"] {
		t.Error("populated enum should not be flagged")
	}
}

// TestAuditSchema_NonMap returns no violations when the schema is absent or not
// a JSON object (e.g. a nil OutputSchema).
func TestAuditSchema_NonMap(t *testing.T) {
	if vs := auditSchema("tool", "output", nil); vs != nil {
		t.Errorf("nil schema: got %v, want nil", vs)
	}
}

// TestAuditTools_SortedAndSkipsNil confirms nil tools are skipped and the result
// is sorted by tool then category.
func TestAuditTools_SortedAndSkipsNil(t *testing.T) {
	list := []*mcp.Tool{
		{Name: "zeta", Description: "short", InputSchema: map[string]any{"type": "object"}},
		nil,
		{Name: "alpha", Description: "short", InputSchema: map[string]any{"type": "object"}},
	}
	vs := auditTools(list)
	if len(vs) == 0 {
		t.Fatal("expected violations")
	}
	if vs[0].Tool != "alpha" {
		t.Errorf("results not sorted by tool: first = %q", vs[0].Tool)
	}
	for _, v := range vs {
		if v.Tool == "" {
			t.Error("nil tool was not skipped")
		}
	}
}

// TestWriteMarkdownReport_Clean renders the success line when there are no
// violations.
func TestWriteMarkdownReport_Clean(t *testing.T) {
	var b bytes.Buffer
	writeMarkdownReport(&b, []*mcp.Tool{{Name: "search"}}, nil)
	if !strings.Contains(b.String(), "No violations") {
		t.Fatalf("clean report missing success line; got:\n%s", b.String())
	}
}

// TestWriteMarkdownReport_Grouped groups violations by category with a table.
func TestWriteMarkdownReport_Grouped(t *testing.T) {
	var b bytes.Buffer
	vs := []violation{
		{"search", "title", "no title"},
		{"read", "field-description", "field x missing"},
	}
	writeMarkdownReport(&b, []*mcp.Tool{{Name: "search"}, {Name: "read"}}, vs)
	out := b.String()
	for _, want := range []string{"## field-description (1)", "## title (1)", "`search`", "Tools audited | 2"} {
		if !strings.Contains(out, want) {
			t.Errorf("report missing %q; got:\n%s", want, out)
		}
	}
}

// TestWriteReport_JSON encodes a structured document via the -json path.
func TestWriteReport_JSON(t *testing.T) {
	var b bytes.Buffer
	vs := []violation{{"search", "title", "no title"}}
	if err := writeReport(&b, []*mcp.Tool{{Name: "search"}}, vs, true); err != nil {
		t.Fatalf("writeReport(json): %v", err)
	}
	var report struct {
		Tools      int         `json:"tools"`
		Violations int         `json:"violations"`
		Entries    []violation `json:"entries"`
	}
	if err := json.Unmarshal(b.Bytes(), &report); err != nil {
		t.Fatalf("unmarshal report: %v", err)
	}
	if report.Tools != 1 || report.Violations != 1 || len(report.Entries) != 1 {
		t.Fatalf("unexpected report: %+v", report)
	}
	if report.Entries[0].Category != "title" {
		t.Errorf("entry category = %q, want title", report.Entries[0].Category)
	}
}

// TestListToolsEndToEnd registers the real tool surface and asserts every tool
// and its full field set pass the audit — the same invariant the CI gate holds.
func TestListToolsEndToEnd(t *testing.T) {
	toolList, err := listTools()
	if err != nil {
		t.Fatalf("listTools: %v", err)
	}
	names := map[string]bool{}
	for _, tool := range toolList {
		names[tool.Name] = true
	}
	for _, want := range []string{"search", "get_details", "download", "read"} {
		if !names[want] {
			t.Errorf("tool %q not registered; got %v", want, names)
		}
	}
	if vs := auditTools(toolList); len(vs) != 0 {
		t.Fatalf("current tool surface has %d violations, want 0:\n%+v", len(vs), vs)
	}
}

// categorySet collapses violations to the set of categories present.
func categorySet(vs []violation) map[string]bool {
	set := map[string]bool{}
	for _, v := range vs {
		set[v.Category] = true
	}
	return set
}

// detailSet collects the detail strings of the given violations.
func detailSet(vs []violation) []string {
	out := make([]string, len(vs))
	for i, v := range vs {
		out[i] = v.Detail
	}
	return out
}

// hasSubstr reports whether any string in list contains sub.
func hasSubstr(list []string, sub string) bool {
	for _, s := range list {
		if strings.Contains(s, sub) {
			return true
		}
	}
	return false
}
