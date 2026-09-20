// Command gen_tool_schema writes the structural half of the tool surface to
// site/src/data/tool-schema.json, for the documentation site to render.
//
// The site's reference tables state, for every parameter and every returned
// field, its name, its type and whether it is required. All three are facts the
// server already knows: they come out of the registered JSON Schema. Restating
// them by hand across two locales is 89 rows nobody can check, and it had
// already drifted — download's `resolved` field is in the schema and absent from
// the table that is supposed to list every field it returns.
//
// What this command deliberately does NOT emit is the descriptions. The
// jsonschema tags are written for a model — "array of collections to search:
// nonfiction fiction articles magazines comics standards fiction_rus (omit for
// all). Use fiction for novels comics for graphic novels" — and the site's
// tables are written for a reader: "Collections to search: `nonfiction`,
// `fiction`, … Omit for all collections." Both are correct for their audience,
// and generating one from the other would replace the prose a person needs with
// the prose a model needs. Structure is generated; prose stays authored, and the
// ParamTable component fails the build if a page describes a parameter the
// schema does not have, or omits one it does.
//
// Usage:
//
//	go run ./cmd/gen_tool_schema/           # rewrite the file
//	go run ./cmd/gen_tool_schema/ --check   # fail if it is stale (CI)
package main
