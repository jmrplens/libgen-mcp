// Command gen_stats counts this repository's surface and writes the numbers
// into the region README.md reserves for them.
//
// The counts were prose before: "four focused tools" and "21 download sources"
// appear in half a dozen sentences across the README and CLAUDE.md, and every
// one of them was a number somebody typed. A sentence that says four when the
// server registers five is not a stale sentence, it is a wrong one, and
// nothing reported it.
//
// So the numbers that can be counted are counted, from the same places the
// server reads them: the tools and prompts from a real registration, the
// download sources from config.KnownSources, the environment variables from
// config.KnownEnvNames, and the packages and test files by walking the tree.
// The prose around them stays hand-written — a generated page nobody reads is
// worse than a short one somebody wrote, so only the table is generated.
//
// The test counts are here rather than in a generator of their own, although
// the testing reference is a page of its own. A second command counting test
// files would be a second thing to keep fresh for the same numbers, and the
// walk that finds them is the walk this one already does.
//
// Usage:
//
//	go run ./cmd/gen_stats [--check] [--dir .]
//
// --check exits non-zero when the committed region is not what this run would
// write, naming the target that refreshes it, which is how the gate reports a
// stale README rather than rewriting it under a reader.
package main
