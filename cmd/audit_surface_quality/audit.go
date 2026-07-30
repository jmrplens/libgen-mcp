// Surface-quality audit rules and report rendering.

package main

import (
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// minDescLen is the minimum acceptable length, in characters, for an MCP tool
// description. Anything shorter cannot meaningfully guide the model.
const minDescLen = 20

// Description budgets. Every tool definition is re-sent on every request, so a
// description is a fixed context cost the caller pays forever (cmd/audit_tokens
// prices the whole surface). Nothing bounded that growth: descriptions are edited
// one clause at a time and each edit looks small.
//
// The caps are deliberately loose — roughly a third above the longest text on the
// surface today — so they catch a description that ran away, not one that grew by
// a sentence.
const (
	// maxToolDescLen bounds one tool's own description.
	maxToolDescLen = 2000
	// maxFieldDescLen bounds one input/output field's description.
	maxFieldDescLen = 1000
)

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
	if len(t.Description) > maxToolDescLen {
		vs = append(vs, violation{
			t.Name, "description-budget",
			fmt.Sprintf("tool description is %d chars, over the %d-char budget; every request pays for it",
				len(t.Description), maxToolDescLen),
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
	walkSchema(schema, kind, scope{}, func(category, detail string) {
		vs = append(vs, violation{toolName, category, detail})
	})
	return vs
}

// scope names the property a schema node belongs to, so a rule about an enum can
// reach the prose that documents it.
//
// An enum frequently sits on a node with no description of its own: an array
// property carries the description and pins the constraint on its items (as
// search's topics and search_in do). Checking node["description"] alone would
// silently exempt exactly those.
type scope struct {
	// field is the property name the node hangs under, "" at a schema root.
	field string
	// description is that property's description, inherited by its subschemas.
	description string
}

// walkSchema recursively visits a JSON Schema node, reporting a missing
// description on every named property and an empty enum on any node. report is
// called once per finding with the violation category and detail. sc names the
// property the node belongs to, so an enum on an items subschema is still graded
// against the prose that documents it.
func walkSchema(node map[string]any, kind string, sc scope, report func(category, detail string)) {
	checkEnum(node, kind, sc, report)
	checkTagDirectiveLeak(node, kind, report)
	walkProperties(node, kind, report)
	walkNamedChildren(node, kind, report, "$defs", "definitions")
	walkNamedChildren(node, kind, report, "allOf", "anyOf", "oneOf")
	walkChild(node, "items", kind, sc, report)
	walkChild(node, "additionalProperties", kind, sc, report)
}

// checkEnum grades an enum: that it constrains something, that its values are
// usable, and that the prose documenting it still lists them all.
func checkEnum(node map[string]any, kind string, sc scope, report func(category, detail string)) {
	enum, present := node["enum"]
	if !present {
		return
	}
	list, isList := enum.([]any)
	if !isList || len(list) == 0 {
		report("empty-enum", kind+" enum constrains no values")
		return
	}
	values := enumValues(list)
	checkEnumValues(values, kind, sc, report)
	checkEnumDescription(values, kind, sc, report)
}

// enumValues renders an enum's values the way a model reads them. A JSON number
// arrives as a float64, and the default rendering of a large one is exponential
// notation no description would ever contain, so integral values are formatted
// as integers.
func enumValues(list []any) []string {
	out := make([]string, 0, len(list))
	for _, v := range list {
		if f, isFloat := v.(float64); isFloat {
			out = append(out, strconv.FormatFloat(f, 'f', -1, 64))
			continue
		}
		out = append(out, fmt.Sprint(v))
	}
	return out
}

// checkEnumValues reports an enum carrying a blank or repeated value.
//
// A repeat is not cosmetic: the download tool's source enum is merged from the
// per-identifier chains, and one source serves two of them — oapen resolves both
// an ISBN and a monograph DOI — so the merge deduplicates. Nothing else would
// notice if it stopped.
func checkEnumValues(values []string, kind string, sc scope, report func(category, detail string)) {
	seen := map[string]bool{}
	for _, v := range values {
		switch {
		case strings.TrimSpace(v) == "":
			report("enum-values", fmt.Sprintf("%s enum on %s carries a blank value", kind, propertyLabel(sc)))
		case seen[v]:
			report("enum-values", fmt.Sprintf("%s enum on %s lists %q twice", kind, propertyLabel(sc), v))
		}
		seen[v] = true
	}
}

// checkEnumDescription reports a value the enum accepts that the property's
// description never mentions.
//
// The two are written in different places and only one of them is generated: the
// enums are pinned from the code that validates the values (libgen.TopicNames,
// libgen.OrderNames, config.KnownSources), while the prose beside them is a
// hand-written list in a struct tag. Adding a value to the validator therefore
// ships a schema whose enum offers something the description never explains — the
// model is told the set is one thing and constrained to another.
func checkEnumDescription(values []string, kind string, sc scope, report func(category, detail string)) {
	if strings.TrimSpace(sc.description) == "" {
		return
	}
	lower := strings.ToLower(sc.description)
	for _, v := range values {
		if !strings.Contains(lower, strings.ToLower(v)) {
			report("enum-description-drift",
				fmt.Sprintf("%s enum on %s accepts %q, which its description never mentions; "+
					"the description is stale relative to the values the schema pins", kind, propertyLabel(sc), v))
		}
	}
}

// propertyLabel names the property a finding is about, for schema roots too.
func propertyLabel(sc scope) string {
	if sc.field == "" {
		return "an unnamed schema node"
	}
	return strconv.Quote(sc.field)
}

// tagDirectives are struct-tag suffixes that read like schema directives but are
// not: the jsonschema tag is assigned wholesale to the description, so anything
// of this shape ends up as literal text in front of every model instead of
// constraining anything. Checking the rendered description catches the mistake
// no matter which field grew it.
var tagDirectives = []string{"enum=", ",required"}

// checkTagDirectiveLeak reports a description carrying an unparsed struct-tag
// directive. An empty enum is only half the failure mode — a directive the tag
// parser never reads produces no enum at all, so checkEnum has nothing to flag
// and the constraint silently ships as prose.
func checkTagDirectiveLeak(node map[string]any, kind string, report func(category, detail string)) {
	desc, _ := node["description"].(string)
	for _, directive := range tagDirectives {
		if strings.Contains(desc, directive) {
			report("tag-directive-leak",
				fmt.Sprintf("%s description contains the unparsed struct-tag directive %q; "+
					"set the constraint on the schema instead", kind, directive))
		}
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
		desc, _ := child["description"].(string)
		switch {
		case strings.TrimSpace(desc) == "":
			report("field-description",
				fmt.Sprintf("%s field %q has no jsonschema description", kind, name))
		case len(desc) > maxFieldDescLen:
			report("description-budget",
				fmt.Sprintf("%s field %q has a %d-char description, over the %d-char budget",
					kind, name, len(desc), maxFieldDescLen))
		}
		walkSchema(child, kind, scope{field: name, description: desc}, report)
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
			walkSchema(child, kind, scope{}, report)
		}
	}
}

// walkSchemaList recurses into each subschema of a list-shaped block
// (allOf/anyOf/oneOf).
func walkSchemaList(entries []any, kind string, report func(category, detail string)) {
	for _, entry := range entries {
		if child, ok := entry.(map[string]any); ok {
			walkSchema(child, kind, scope{}, report)
		}
	}
}

// walkChild recurses into a single subschema held under key (e.g. items),
// carrying the owning property's scope so an enum pinned on the items still has
// the prose that documents it.
func walkChild(node map[string]any, key, kind string, sc scope, report func(category, detail string)) {
	if child, ok := node[key].(map[string]any); ok {
		walkSchema(child, kind, sc, report)
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
