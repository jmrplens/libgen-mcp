// Surface-quality audit rules and report rendering.

package main

import (
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// minDescLen is the minimum acceptable length, in characters, for an MCP tool
// description. Anything shorter cannot meaningfully guide the model.
const minDescLen = 20

// violation records one surface-quality rule infraction.
//
// Tool is the MCP tool name that violated the rule. Category is the rule family
// used to group findings in the report (for example "description", "title", or
// "field-description"). Detail is a human-readable explanation rendered verbatim
// into the Markdown table.
type violation struct {
	Tool     string `json:"tool"`
	Category string `json:"category"`
	Detail   string `json:"detail"`
}

// auditTools runs every surface-quality rule against the tool list and returns
// the violations sorted by tool then category for a stable report.
func auditTools(toolList []*mcp.Tool) []violation {
	var vs []violation
	for _, t := range toolList {
		if t == nil {
			continue
		}
		vs = append(vs, auditMetadata(t)...)
		vs = append(vs, auditSchema(t.Name, "input", t.InputSchema)...)
		vs = append(vs, auditSchema(t.Name, "output", t.OutputSchema)...)
	}
	sort.SliceStable(vs, func(i, j int) bool {
		if vs[i].Tool != vs[j].Tool {
			return vs[i].Tool < vs[j].Tool
		}
		return vs[i].Category < vs[j].Category
	})
	return vs
}

// auditMetadata checks a tool's top-level metadata: title, annotations,
// description length, and that its input schema is an object.
func auditMetadata(t *mcp.Tool) []violation {
	var vs []violation
	if t.Title == "" {
		vs = append(vs, violation{t.Name, "title", "tool has no Title"})
	}
	if t.Annotations == nil {
		vs = append(vs, violation{t.Name, "annotations", "tool has nil Annotations"})
	}
	if len(t.Description) < minDescLen {
		vs = append(vs, violation{
			t.Name, "description",
			fmt.Sprintf("description too short (%d chars, want >= %d)", len(t.Description), minDescLen),
		})
	}
	schema, ok := t.InputSchema.(map[string]any)
	if !ok {
		vs = append(vs, violation{t.Name, "input-schema", "InputSchema is not a JSON object"})
		return vs
	}
	if typ, _ := schema["type"].(string); typ != "object" {
		vs = append(vs, violation{
			t.Name, "input-schema",
			fmt.Sprintf("InputSchema type=%q, want \"object\"", typ),
		})
	}
	return vs
}

// auditSchema walks a tool's input or output JSON Schema (as delivered to a
// client, i.e. a map[string]any) and flags every named field that lacks a
// jsonschema description and every enum that constrains nothing. kind is
// "input" or "output" and is woven into the finding detail.
//
// A nil schema is not itself a violation here: an absent OutputSchema is
// reported separately if the repo ever requires one. The walk descends through
// properties, $defs/definitions, array items, additionalProperties, and the
// allOf/anyOf/oneOf combinators so nested struct fields are covered.
func auditSchema(toolName, kind string, raw any) []violation {
	schema, ok := raw.(map[string]any)
	if !ok {
		return nil
	}
	var vs []violation
	walkSchema(schema, kind, func(category, detail string) {
		vs = append(vs, violation{toolName, category, detail})
	})
	return vs
}

// walkSchema recursively visits a JSON Schema node, reporting a missing
// description on every named property and an empty enum on any node. report is
// called once per finding with the violation category and detail.
func walkSchema(node map[string]any, kind string, report func(category, detail string)) {
	checkEnum(node, kind, report)
	walkProperties(node, kind, report)
	walkNamedChildren(node, kind, report, "$defs", "definitions")
	walkNamedChildren(node, kind, report, "allOf", "anyOf", "oneOf")
	walkChild(node, "items", kind, report)
	walkChild(node, "additionalProperties", kind, report)
}

// checkEnum reports an enum that constrains no values.
func checkEnum(node map[string]any, kind string, report func(category, detail string)) {
	enum, present := node["enum"]
	if !present {
		return
	}
	if list, isList := enum.([]any); !isList || len(list) == 0 {
		report("empty-enum", kind+" enum constrains no values")
	}
}

// walkProperties flags every named property without a description and recurses
// into each property's own schema.
func walkProperties(node map[string]any, kind string, report func(category, detail string)) {
	props, ok := node["properties"].(map[string]any)
	if !ok {
		return
	}
	for _, name := range sortedKeys(props) {
		child, isMap := props[name].(map[string]any)
		if !isMap {
			continue
		}
		if desc, _ := child["description"].(string); strings.TrimSpace(desc) == "" {
			report("field-description",
				fmt.Sprintf("%s field %q has no jsonschema description", kind, name))
		}
		walkSchema(child, kind, report)
	}
}

// walkNamedChildren recurses into schema nodes held under the given keys, each
// of which may be a map of named subschemas ($defs) or a list of subschemas
// (allOf/anyOf/oneOf).
func walkNamedChildren(node map[string]any, kind string, report func(category, detail string), keys ...string) {
	for _, key := range keys {
		switch children := node[key].(type) {
		case map[string]any:
			walkSchemaMap(children, kind, report)
		case []any:
			walkSchemaList(children, kind, report)
		}
	}
}

// walkSchemaMap recurses into each named subschema of a map-shaped block
// ($defs/definitions), visiting names in sorted order for a deterministic
// report.
func walkSchemaMap(children map[string]any, kind string, report func(category, detail string)) {
	for _, name := range sortedKeys(children) {
		if child, ok := children[name].(map[string]any); ok {
			walkSchema(child, kind, report)
		}
	}
}

// walkSchemaList recurses into each subschema of a list-shaped block
// (allOf/anyOf/oneOf).
func walkSchemaList(entries []any, kind string, report func(category, detail string)) {
	for _, entry := range entries {
		if child, ok := entry.(map[string]any); ok {
			walkSchema(child, kind, report)
		}
	}
}

// walkChild recurses into a single subschema held under key (e.g. items).
func walkChild(node map[string]any, key, kind string, report func(category, detail string)) {
	if child, ok := node[key].(map[string]any); ok {
		walkSchema(child, kind, report)
	}
}

// sortedKeys returns the keys of m in sorted order, for deterministic reports.
func sortedKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// writeJSONReport encodes the tool count and violations as a JSON document.
func writeJSONReport(w io.Writer, toolList []*mcp.Tool, violations []violation) error {
	report := struct {
		Tools      int         `json:"tools"`
		Violations int         `json:"violations"`
		Entries    []violation `json:"entries"`
	}{len(toolList), len(violations), violations}
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	if err := enc.Encode(report); err != nil {
		return fmt.Errorf("encode json: %w", err)
	}
	return nil
}

// writeMarkdownReport writes the human-readable audit report to w: a summary
// count, then a table of violations grouped by category, or a success line when
// the surface is clean.
func writeMarkdownReport(w io.Writer, toolList []*mcp.Tool, violations []violation) {
	fmt.Fprintf(w, "# MCP Tool Surface Quality Audit\n\n")
	fmt.Fprintf(w, "| Metric | Count |\n| --- | --- |\n")
	fmt.Fprintf(w, "| Tools audited | %d |\n", len(toolList))
	fmt.Fprintf(w, "| Violations | %d |\n\n", len(violations))

	if len(violations) == 0 {
		fmt.Fprintln(w, "**No violations — the tool surface meets every quality convention.**")
		return
	}

	byCategory := map[string][]violation{}
	for _, v := range violations {
		byCategory[v.Category] = append(byCategory[v.Category], v)
	}
	cats := make([]string, 0, len(byCategory))
	for c := range byCategory {
		cats = append(cats, c)
	}
	sort.Strings(cats)

	for _, cat := range cats {
		group := byCategory[cat]
		fmt.Fprintf(w, "## %s (%d)\n\n", cat, len(group))
		fmt.Fprintf(w, "| Tool | Detail |\n| --- | --- |\n")
		for _, v := range group {
			fmt.Fprintf(w, "| `%s` | %s |\n", v.Tool, v.Detail)
		}
		fmt.Fprintln(w)
	}
}
