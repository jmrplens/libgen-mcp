// Command gen_eval_pages generates the tables on the evaluator results pages, in
// both languages, so they cannot drift from the code they describe.
//
// Two tables must match something outside the page: the scenario list, whose
// authority is cmd/eval/README.md, and the latest run, whose authority is the
// results doc a run writes with --results-doc. Both were maintained by hand and
// both drifted — a stale scenario count, malformed rows, an evidence string quoting
// a message the code no longer emitted, and a live download key published in a
// results row.
//
// Only the regions between the generated-region markers are rewritten. The prose
// around them is written by hand and left alone.
//
// Usage:
//
//	go run ./cmd/gen_eval_pages/ --results-doc eval-results.md
//	go run ./cmd/gen_eval_pages/ --check
package main
