package main

import (
	"reflect"
	"slices"
	"strings"
	"testing"
)

// TestTypeName covers the vocabulary the generated tables use, which is the
// prose's vocabulary rather than JSON Schema's: `string[]`, not "array of
// strings", because that is what the tables beside it already said.
func TestTypeName(t *testing.T) {
	cases := []struct {
		name string
		prop map[string]any
		want string
	}{
		{name: "a plain scalar", prop: map[string]any{"type": "string"}, want: "string"},
		{name: "an array of scalars", prop: map[string]any{"type": "array", "items": map[string]any{"type": "string"}}, want: "string[]"},
		{name: "an array of objects", prop: map[string]any{"type": "array", "items": map[string]any{"type": "object"}}, want: "object[]"},
		{name: "an array with untyped items", prop: map[string]any{"type": "array", "items": map[string]any{}}, want: "array"},
		{name: "an array with no items at all", prop: map[string]any{"type": "array"}, want: "array"},
		// jsonschema-go spells an optional field as a type union with null,
		// and the null carries no information a reader wants in a table.
		{name: "nullable is not a type", prop: map[string]any{"type": []any{"string", "null"}}, want: "string"},
		{name: "a genuine union", prop: map[string]any{"type": []any{"string", "number"}}, want: "string or number"},
		// Inferred rather than declared: a schema can describe its shape
		// without naming a type.
		{name: "items imply an array", prop: map[string]any{"items": map[string]any{"type": "string"}}, want: "array"},
		{name: "properties imply an object", prop: map[string]any{"properties": map[string]any{}}, want: "object"},
		{name: "nothing at all", prop: map[string]any{}, want: "any"},
		{name: "an empty type string", prop: map[string]any{"type": ""}, want: "any"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := typeName(tc.prop); got != tc.want {
				t.Errorf("typeName(%v) = %q, want %q", tc.prop, got, tc.want)
			}
		})
	}
}

// TestEnumValuesKeepsChainOrder pins the decision that looks like an oversight
// and is not: the values are emitted in the order the schema gives them.
//
// The download tool's source enum is the enabled chain in chain order, and that
// order is the thing it tells the caller. Sorting would publish the last
// fallback first.
func TestEnumValuesKeepsChainOrder(t *testing.T) {
	chain := []any{"unpaywall", "openalex", "scihub", "libgen", "annas"}
	got := enumValues(map[string]any{"type": "string", "enum": chain})
	want := []string{"unpaywall", "openalex", "scihub", "libgen", "annas"}
	if !slices.Equal(got, want) {
		t.Errorf("enumValues = %v, want the schema's own order %v", got, want)
	}
	if slices.IsSorted(got) {
		t.Error("the fixture is sorted, so it cannot detect sorting: pick values whose order differs from alphabetical")
	}
}

// TestEnumValuesLooksInsideItems covers the array case: it is the members that
// are constrained, not the array, so the enum lives one level down.
func TestEnumValuesLooksInsideItems(t *testing.T) {
	prop := map[string]any{
		"type":  "array",
		"items": map[string]any{"type": "string", "enum": []any{"fiction", "scitech"}},
	}
	if got, want := enumValues(prop), []string{"fiction", "scitech"}; !slices.Equal(got, want) {
		t.Errorf("enumValues = %v, want %v", got, want)
	}

	for _, name := range []string{"no enum anywhere", "an empty enum"} {
		t.Run(name, func(t *testing.T) {
			bare := map[string]any{"type": "string"}
			if name == "an empty enum" {
				bare["enum"] = []any{}
			}
			if got := enumValues(bare); got != nil {
				t.Errorf("enumValues = %v, want nil", got)
			}
		})
	}
}

// TestEnumValuesRendersNonStrings covers a numeric enum, which results_per_page
// has: the values reach the table as the numbers a description would spell.
func TestEnumValuesRendersNonStrings(t *testing.T) {
	got := enumValues(map[string]any{"type": "integer", "enum": []any{float64(25), float64(50), float64(100)}})
	if want := []string{"25", "50", "100"}; !slices.Equal(got, want) {
		t.Errorf("enumValues = %v, want %v", got, want)
	}
}

// TestRequiredSet covers the lookup, including the shapes that are absent
// rather than empty — a schema with no required array at all is the common
// case, since every optional field carries omitempty.
func TestRequiredSet(t *testing.T) {
	got := requiredSet(map[string]any{"required": []any{"query", "limit"}})
	if !got["query"] || !got["limit"] {
		t.Errorf("requiredSet = %v, want query and limit", got)
	}
	if got["absent"] {
		t.Error("requiredSet reports a field that is not in the list")
	}
	if len(requiredSet(map[string]any{})) != 0 {
		t.Error("a schema with no required array must yield an empty set, not a nil map")
	}
	// A wrong-typed required is a malformed schema, not a panic.
	if len(requiredSet(map[string]any{"required": "query"})) != 0 {
		t.Error("a non-array required must be ignored")
	}
}

// TestJSONNamesFollowsDeclarationOrder pins the reason this command reads the
// structs at all: the round-trip loses field order, because properties decode
// into a map. The order is what the reference tables use, so it has to come
// from somewhere that still has it.
func TestJSONNamesFollowsDeclarationOrder(t *testing.T) {
	type inner struct {
		Second string `json:"second"`
		Third  string `json:"third"`
	}
	type outer struct {
		First  string `json:"first"`
		inner         // embedded: its fields are promoted at this position
		Fourth string `json:"fourth"`
		Hidden string `json:"-"`
		Bare   string
	}

	got := jsonNames(reflect.TypeFor[outer]())
	want := []string{"first", "second", "third", "fourth"}
	if !slices.Equal(got, want) {
		t.Errorf("jsonNames = %v, want %v", got, want)
	}
	if slices.Contains(got, "Hidden") || slices.Contains(got, "-") {
		t.Error("a json:\"-\" field must not appear")
	}
	if slices.Contains(got, "Bare") {
		t.Error("a field with no json tag has no name in the schema and must not appear")
	}
}

// TestJSONNamesStripsTagOptions checks that omitempty does not become part of
// the name, which is the mistake that would make every optional field mismatch.
func TestJSONNamesStripsTagOptions(t *testing.T) {
	type withOptions struct {
		Query string `json:"query,omitempty"`
	}
	got := jsonNames(reflect.TypeFor[withOptions]())
	if want := []string{"query"}; !slices.Equal(got, want) {
		t.Errorf("jsonNames = %v, want %v", got, want)
	}
}

// TestFieldsOfReportsAMismatch is the guard that gives this command its value.
//
// The order comes from the struct and the field set from the live round-trip,
// so the two can disagree — and a disagreement means the registered surface and
// the type it was inferred from have drifted, which is a bug in the server
// rather than something to paper over here. Both directions are checked,
// because only reporting one would let half the drift through.
func TestFieldsOfReportsAMismatch(t *testing.T) {
	schema := map[string]any{
		"properties": map[string]any{
			"query": map[string]any{"type": "string", "description": "the search terms"},
		},
		"required": []any{"query"},
	}

	t.Run("agreement produces rows", func(t *testing.T) {
		rows, err := fieldsOf("search", "input", schema, []string{"query"})
		if err != nil {
			t.Fatalf("fieldsOf error: %v", err)
		}
		if len(rows) != 1 || rows[0].Name != "query" {
			t.Fatalf("rows = %+v, want one row for query", rows)
		}
		if !rows[0].Required {
			t.Error("query is in the required list and the row does not say so")
		}
		if rows[0].Type != "string" {
			t.Errorf("type = %q, want string", rows[0].Type)
		}
	})

	t.Run("a field the struct declares and the schema lacks", func(t *testing.T) {
		_, err := fieldsOf("search", "input", schema, []string{"query", "ghost"})
		if err == nil {
			t.Fatal("fieldsOf accepted a field the schema does not advertise")
		}
		if !strings.Contains(err.Error(), "ghost") {
			t.Errorf("error does not name the offending field: %v", err)
		}
	})

	t.Run("a field the schema has and the struct does not", func(t *testing.T) {
		_, err := fieldsOf("search", "input", schema, []string{})
		if err == nil {
			t.Fatal("fieldsOf accepted a schema field with no counterpart in the struct")
		}
		if !strings.Contains(err.Error(), "query") {
			t.Errorf("error does not name the offending field: %v", err)
		}
	})

	t.Run("a section that is not an object", func(t *testing.T) {
		rows, err := fieldsOf("search", "output", "not a schema", nil)
		if err != nil || rows != nil {
			t.Errorf("fieldsOf(non-object) = %v, %v; want nil, nil — a tool with no output schema is not an error", rows, err)
		}
	})
}

// TestDeclaredOrderCoversTheSurface pins the map against the tools that exist:
// a tool added without an entry here would generate with no order to follow,
// and the failure would be a silently alphabetised table rather than an error.
func TestDeclaredOrderCoversTheSurface(t *testing.T) {
	for _, name := range []string{"search", "get_details", "download", "read"} {
		if _, ok := declaredOrder[name]; !ok {
			t.Errorf("declaredOrder has no entry for %q", name)
		}
	}
	if len(declaredOrder) != 4 {
		t.Errorf("declaredOrder has %d entries, want 4: a new tool needs one, and a removed tool should lose it", len(declaredOrder))
	}
	for name, pair := range declaredOrder {
		for i, typ := range pair {
			if typ.Kind() != reflect.Struct {
				t.Errorf("%s[%d] is a %s, want a struct", name, i, typ.Kind())
			}
			if len(jsonNames(typ)) == 0 {
				t.Errorf("%s[%d] (%s) yields no json names", name, i, typ)
			}
		}
	}
}

// TestRenderIsStableAndUnescaped pins two properties of the artifact rather
// than its content: it is deterministic, since a generator whose output moved
// between runs would make `make check-tool-schema` fail at random; and it does
// not HTML-escape, because an enum value containing `&` or `<` would otherwise
// reach the site as an entity nobody wrote.
func TestRenderIsStableAndUnescaped(t *testing.T) {
	doc := &document{
		Note: note,
		Tools: map[string]toolSchema{
			"search": {Input: []field{{Name: "query", Type: "string", Required: true, Enum: []string{"a & b < c"}}}},
		},
	}

	first, err := render(doc)
	if err != nil {
		t.Fatalf("render error: %v", err)
	}
	second, err := render(doc)
	if err != nil {
		t.Fatalf("render error: %v", err)
	}
	if string(first) != string(second) {
		t.Error("render is not deterministic; the freshness check would fail at random")
	}
	if strings.Contains(string(first), "\\u0026") || strings.Contains(string(first), "\\u003c") {
		t.Errorf("render HTML-escaped the description:\n%s", first)
	}
	if !strings.Contains(string(first), "a & b < c") {
		t.Errorf("the value did not survive rendering:\n%s", first)
	}
	if !strings.HasSuffix(string(first), "\n") {
		t.Error("render does not end with a newline; the committed file does")
	}
}

// TestRunCheckAcceptsTheCommittedArtifact runs the real gate against the real
// surface, which is the one assertion that ties every helper above to the file
// this repository ships.
//
// It is the same call `make check-tool-schema` makes, so a drift between the
// registered surface and site/src/data/tool-schema.json fails here first.
func TestRunCheckAcceptsTheCommittedArtifact(t *testing.T) {
	if err := run(true); err != nil {
		t.Fatalf("the committed artifact is stale or unbuildable: %v", err)
	}
}
