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

// describedOutputSchema is the output schema a well-formed tool carries: an
// object whose root says what the tool returns.
func describedOutputSchema() map[string]any {
	schema := objectSchema(nil)
	schema["description"] = "What the tool returns, in a sentence long enough to be useful."
	return schema
}

// exampledInputSchema is the input schema a well-formed tool carries: an object
// with one example call in its `examples`.
func exampledInputSchema() map[string]any {
	schema := objectSchema(nil)
	schema["examples"] = []any{map[string]any{"query": "dune"}}
	return schema
}

// TestAuditMetadata_Clean verifies a well-formed tool produces no metadata
// violations.
func TestAuditMetadata_Clean(t *testing.T) {
	tool := &mcp.Tool{
		Name:         "search",
		Title:        "Search",
		Description:  "A sufficiently long description for the tool.",
		Annotations:  &mcp.ToolAnnotations{DestructiveHint: new(bool)},
		InputSchema:  exampledInputSchema(),
		OutputSchema: describedOutputSchema(),
	}
	if vs := auditMetadata(tool); len(vs) != 0 {
		t.Fatalf("auditMetadata() = %+v, want none", vs)
	}
}

// TestAuditMetadata_InputExamples pins the examples rule: no `examples` and an
// empty one are both reported, since neither shows a caller anything.
func TestAuditMetadata_InputExamples(t *testing.T) {
	testCases := []struct {
		name     string
		examples any
	}{
		{name: "absent"},
		{name: "empty", examples: []any{}},
		{name: "not an array", examples: map[string]any{"query": "dune"}},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			schema := objectSchema(nil)
			if tc.examples != nil {
				schema["examples"] = tc.examples
			}
			tool := &mcp.Tool{
				Name:         "search",
				Title:        "Search",
				Description:  "A sufficiently long description for the tool.",
				Annotations:  &mcp.ToolAnnotations{DestructiveHint: new(bool)},
				InputSchema:  schema,
				OutputSchema: describedOutputSchema(),
			}
			vs := auditMetadata(tool)
			if len(vs) != 1 || vs[0].Category != categoryInputExamples {
				t.Errorf("auditMetadata() = %+v, want exactly one %s finding", vs, categoryInputExamples)
			}
		})
	}
}

// TestAuditMetadata_UndeclaredDestructiveHint verifies a tool that leaves
// destructiveHint unset is reported, and that a tool with nil Annotations is
// reported once rather than twice.
func TestAuditMetadata_UndeclaredDestructiveHint(t *testing.T) {
	base := func() *mcp.Tool {
		return &mcp.Tool{
			Name:         "search",
			Title:        "Search",
			Description:  "A sufficiently long description for the tool.",
			InputSchema:  exampledInputSchema(),
			OutputSchema: describedOutputSchema(),
		}
	}

	unset := base()
	unset.Annotations = &mcp.ToolAnnotations{ReadOnlyHint: true}
	if !hasSubstr(detailSet(auditMetadata(unset)), "destructiveHint") {
		t.Errorf("auditMetadata() = %+v, want a destructiveHint finding", auditMetadata(unset))
	}

	// A nil Annotations already fails on its own; it must not also be charged
	// with omitting a field it has no room to declare.
	missing := base()
	vs := auditMetadata(missing)
	if len(vs) != 1 || vs[0].Category != "annotations" {
		t.Fatalf("auditMetadata() = %+v, want exactly one annotations finding", vs)
	}
	if hasSubstr(detailSet(vs), "destructiveHint") {
		t.Errorf("nil Annotations reported as an undeclared destructiveHint: %+v", vs)
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
		t.Run(want, func(t *testing.T) {
			if !got[want] {
				t.Errorf("auditMetadata() missing category %q; got %v", want, got)
			}
		})
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

// TestAuditSchema_TopLevelCombinator covers the rule that cost this project a
// working evaluator: a combinator at a schema's root is refused by the Anthropic
// Messages API for the whole request, so the gate has to see it.
//
// Both halves are asserted. The root is a violation, and the same keyword nested
// inside a property is not — nesting is accepted everywhere, and a gate that
// flagged it would push every future schema into a shape JSON Schema has no
// reason to avoid.
func TestAuditSchema_TopLevelCombinator(t *testing.T) {
	for _, keyword := range []string{"oneOf", "anyOf", "allOf"} {
		t.Run("root "+keyword, func(t *testing.T) {
			schema := objectSchema(map[string]any{
				"a": map[string]any{"type": "string", "description": "ok"},
			})
			schema[keyword] = []any{map[string]any{"required": []any{"a"}}}
			vs := auditSchema("tool", "input", schema)
			if !hasSubstr(detailSet(vs), keyword+" at the top level") {
				t.Errorf("a root %s was not reported; got %v", keyword, detailSet(vs))
			}
		})

		t.Run("nested "+keyword, func(t *testing.T) {
			schema := objectSchema(map[string]any{
				"a": map[string]any{
					"description": "ok",
					keyword:       []any{map[string]any{"type": "string"}, map[string]any{"type": "number"}},
				},
			})
			if vs := auditSchema("tool", "input", schema); hasSubstr(detailSet(vs), "at the top level") {
				t.Errorf("a %s nested in a property was reported; got %v", keyword, detailSet(vs))
			}
		})
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

// TestAuditSchema_EnumDescriptionDrift is the rule for the half of an enum that
// is not generated: the values are pinned from the code that validates them, the
// prose beside them is hand-written, and adding a value updates only the first.
func TestAuditSchema_EnumDescriptionDrift(t *testing.T) {
	stale := objectSchema(map[string]any{
		"topics": map[string]any{
			"type":        "array",
			"description": "collections to search: nonfiction fiction",
			// The validator gained a collection the prose never learned about.
			"items": map[string]any{"type": "string", "enum": []any{"nonfiction", "fiction", "standards"}},
		},
	})
	vs := auditSchema("search", "input", stale)
	if !categorySet(vs)["enum-description-drift"] {
		t.Fatalf("a value the description never mentions must be flagged; got %v", detailSet(vs))
	}
	if !hasSubstr(detailSet(vs), `"standards"`) {
		t.Errorf("the finding must name the undocumented value; got %v", detailSet(vs))
	}
	if hasSubstr(detailSet(vs), `"fiction"`) {
		t.Errorf("a documented value must not be flagged; got %v", detailSet(vs))
	}
}

// TestAuditSchema_EnumDescriptionFresh accepts a description that still lists
// every value, including a numeric one — a JSON number arrives as a float64 and
// must be compared as the integer a description would spell.
func TestAuditSchema_EnumDescriptionFresh(t *testing.T) {
	fresh := objectSchema(map[string]any{
		"results_per_page": map[string]any{
			"type":        "integer",
			"description": "a single number: 25 50 or 100 (default 25)",
			"enum":        []any{float64(25), float64(50), float64(100)},
		},
	})
	if vs := auditSchema("search", "input", fresh); len(vs) != 0 {
		t.Fatalf("a description listing every value must pass; got %v", detailSet(vs))
	}
}

// TestAuditSchema_EnumDescriptionAbsent leaves the drift rule silent when there
// is no description to compare against: the missing description is already
// reported by its own rule, and reporting it twice buries the first.
func TestAuditSchema_EnumDescriptionAbsent(t *testing.T) {
	schema := objectSchema(map[string]any{
		"mode": map[string]any{"type": "string", "enum": []any{"a", "b"}},
	})
	got := categorySet(auditSchema("tool", "input", schema))
	if !got["field-description"] {
		t.Error("the missing description must still be reported")
	}
	if got["enum-description-drift"] {
		t.Error("drift must not be reported on top of a missing description")
	}
}

// TestAuditSchema_EnumValues flags a repeated and a blank enum value. A repeat is
// how the download tool's merged source enum would show that its deduplication
// stopped working.
func TestAuditSchema_EnumValues(t *testing.T) {
	schema := objectSchema(map[string]any{
		"source": map[string]any{
			"type":        "string",
			"description": "one of oapen, archive or  ",
			"enum":        []any{"oapen", "archive", "oapen", " "},
		},
	})
	details := detailSet(auditSchema("download", "input", schema))
	if !hasSubstr(details, "twice") {
		t.Errorf("a repeated enum value must be flagged; got %v", details)
	}
	if !hasSubstr(details, "blank value") {
		t.Errorf("a blank enum value must be flagged; got %v", details)
	}
}

// TestAuditMetadata_DescriptionBudget flags a tool description that outgrew its
// budget, and leaves an ordinary one alone.
func TestAuditMetadata_DescriptionBudget(t *testing.T) {
	tool := &mcp.Tool{
		Name:        "search",
		Title:       "Search",
		Description: strings.Repeat("x", maxToolDescLen+1),
		Annotations: &mcp.ToolAnnotations{},
		InputSchema: objectSchema(nil),
	}
	if !categorySet(auditMetadata(tool))["description-budget"] {
		t.Fatal("an over-budget tool description must be flagged")
	}
	tool.Description = strings.Repeat("x", maxToolDescLen)
	if categorySet(auditMetadata(tool))["description-budget"] {
		t.Fatal("a description exactly at the budget must pass")
	}
}

// TestAuditSchema_FieldDescriptionBudget flags a field description that outgrew
// its budget.
func TestAuditSchema_FieldDescriptionBudget(t *testing.T) {
	schema := objectSchema(map[string]any{
		"verbose": map[string]any{"type": "string", "description": strings.Repeat("y", maxFieldDescLen+1)},
	})
	if !categorySet(auditSchema("tool", "input", schema))["description-budget"] {
		t.Fatal("an over-budget field description must be flagged")
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
		t.Run(want, func(t *testing.T) {
			if !strings.Contains(out, want) {
				t.Errorf("report missing %q; got:\n%s", want, out)
			}
		})
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
		t.Run(want, func(t *testing.T) {
			if !names[want] {
				t.Errorf("tool %q not registered; got %v", want, names)
			}
		})
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

// TestAuditSchema_ConstraintStubsInGroupBranches pins the two halves of the
// stub exemption. A required-group branch constrains a field already described
// beside it — {"pattern":"\\S"} on md5 inside anyOf is a refinement, not a
// field, and must not be reported. The discriminator is type: a real field
// always carries one, so an undescribed field inside the same branch is still
// caught.
//
// The group sits **inside a property** rather than at the schema root, which is
// the only place a combinator may now live — see
// [TestAuditSchema_TopLevelCombinator]. The fixture used to be a root anyOf,
// which is the shape this surface shipped until the Messages API refused it;
// keeping it there would test the exemption on a path no schema can take.
func TestAuditSchema_ConstraintStubsInGroupBranches(t *testing.T) {
	group := func(md5 map[string]any) map[string]any {
		return map[string]any{
			"description": "how the item is identified",
			"anyOf": []any{
				map[string]any{
					"required":   []any{"md5"},
					"properties": map[string]any{"md5": md5},
				},
			},
			"properties": map[string]any{
				"md5": map[string]any{"type": "string", "description": "the file md5"},
			},
		}
	}

	schema := objectSchema(map[string]any{"ids": group(map[string]any{"pattern": `\S`})})
	if vs := auditSchema("download", "input", schema); len(vs) != 0 {
		t.Fatalf("a constraint stub in a group branch must pass; got %v", detailSet(vs))
	}

	schema = objectSchema(map[string]any{"ids": group(map[string]any{"type": "string", "pattern": `\S`})})
	got := categorySet(auditSchema("download", "input", schema))
	if !got["field-description"] {
		t.Error("a typed, undescribed property inside a branch must still be reported")
	}
}

// TestAuditOutputRoot_ChecksWhatACallerIsToldTheyGetBack pins the rule the
// whole surface failed until it existed: a schema inferred from a Go struct
// describes every field and says nothing about the whole, so a client had a
// list of properties and no sentence to put above it.
func TestAuditOutputRoot_ChecksWhatACallerIsToldTheyGetBack(t *testing.T) {
	described := "What the tool returns, in a sentence long enough to be useful."
	testCases := []struct {
		name   string
		schema any
		want   []string
	}{
		{name: "an object with a root description", schema: describedOutputSchema(), want: nil},
		{name: "no schema at all", schema: nil, want: []string{"output-schema"}},
		{name: "not an object value", schema: "a string", want: []string{"output-schema"}},
		{name: "an object with no root description", schema: objectSchema(nil), want: []string{"output-description"}},
		{
			name:   "an object with a root description too short to say anything",
			schema: map[string]any{"type": "object", "description": "stuff"},
			want:   []string{"output-description"},
		},
		{
			name:   "a described schema of the wrong type",
			schema: map[string]any{"type": "array", "description": described},
			want:   []string{"output-schema"},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			var categories []string
			for _, v := range auditOutputRoot(&mcp.Tool{Name: "search", OutputSchema: tc.schema}) {
				categories = append(categories, v.Category)
			}
			if strings.Join(categories, ",") != strings.Join(tc.want, ",") {
				t.Errorf("auditOutputRoot() reported %v, want %v", categories, tc.want)
			}
		})
	}
}
