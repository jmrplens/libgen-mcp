package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"slices"
	"sort"
	"strings"
	"unicode"

	"github.com/jmrplens/libgen-mcp/v2/cmd/internal/mcpsurface"
)

// offendingChars lists the ASCII characters gateway validators are known to
// reject in served text. Anything above U+007F is rejected as a class rather
// than listed here, for the reason the package comment gives.
var offendingChars = []rune{';'}

// fieldDescription suffixes a report location: the same field name exists on
// every listed surface, and naming it once keeps the report rows uniform.
const fieldDescription = " description"

// offender is one served string carrying a character a gateway may refuse.
type offender struct {
	surface string
	where   string
	excerpt string
}

// stdout and stderr are the report and diagnostic streams, as variables so a
// test can read what the command prints without redirecting the process.
var (
	stdout io.Writer = os.Stdout
	stderr io.Writer = os.Stderr
)

// osExit is os.Exit behind a variable, so a test can drive the command line
// main assembles and read the code it asks the process to exit with. The
// sibling audits in this repository keep the same seam.
var osExit = os.Exit

// fullStrings switches the report from excerpts to whole strings, which is the
// shape the fixing workflow needs: the whole string is what to grep for.
var fullStrings bool

func main() {
	check := flag.Bool("check", false, "exit non-zero if any offending character is served")
	full := flag.Bool("full", false, "print each offending string whole instead of a one-line excerpt")
	flag.Parse()
	fullStrings = *full

	osExit(run(*check))
}

// run performs the scan and returns the process exit code.
//
// A surface this binary cannot build is not a finding: the catalog is the one
// compiled in, and both ends of the transport are this process, so a failure
// there means the audit has nothing to say either way. It reports that on
// stderr and exits 1 whether or not -check was passed, keeping the -check exit
// reserved for what was actually found.
func run(check bool) int {
	cfg := mcpsurface.DocsConfig()

	tools, err := mcpsurface.Tools(cfg)
	if err != nil {
		fmt.Fprintln(stderr, "audit_gateway_chars: listing tools:", err)
		return 1
	}
	prompts, err := mcpsurface.Prompts(cfg)
	if err != nil {
		fmt.Fprintln(stderr, "audit_gateway_chars: listing prompts:", err)
		return 1
	}

	var found []offender
	for _, tool := range tools {
		found = append(found, scanText("tools", "tool "+tool.Name+fieldDescription, tool.Description)...)
		found = append(found, scanText("tools", "tool "+tool.Name+" title", tool.Title)...)
		found = append(found, scanSchema("tool "+tool.Name+" input schema", tool.InputSchema)...)
		found = append(found, scanSchema("tool "+tool.Name+" output schema", tool.OutputSchema)...)
	}
	for _, prompt := range prompts {
		found = append(found, scanText("prompts", "prompt "+prompt.Name+fieldDescription, prompt.Description)...)
		for _, arg := range prompt.Arguments {
			found = append(found, scanText("prompts", "prompt "+prompt.Name+" argument "+arg.Name, arg.Description)...)
		}
	}
	return report(found, check)
}

// report prints the offenders, sorted by surface then location, and returns
// the exit code: 1 when check is set and anything offends, 0 otherwise.
func report(found []offender, check bool) int {
	sort.Slice(found, func(i, j int) bool {
		if found[i].surface != found[j].surface {
			return found[i].surface < found[j].surface
		}
		return found[i].where < found[j].where
	})

	if len(found) == 0 {
		fmt.Fprintln(stdout, "gateway character audit: nothing served carries an offending character")
		return 0
	}
	for _, f := range found {
		if fullStrings {
			// Tab-separated, because the whole string is for machines and
			// greps; the padded excerpt form is for eyes.
			fmt.Fprintf(stdout, "%s\t%s\t%s\n", f.surface, f.where, f.excerpt)
			continue
		}
		fmt.Fprintf(stdout, "%-8s %-46s %s\n", f.surface, f.where, f.excerpt)
	}
	fmt.Fprintf(stdout, "gateway character audit: %d served string(s) carry an offending character\n", len(found))
	if check {
		return 1
	}
	return 0
}

// scanSchema walks a schema the way a validator does: serialized, then
// descended for the keys that carry prose. Keys like pattern or const
// legitimately contain punctuation that is not prose, and a gateway that
// rejected a regex would not be reporting this problem.
//
// Only tools carry schemas, so the surface is a constant here rather than a
// parameter: passing one would give a reader a value to go and check.
func scanSchema(where string, schema any) []offender {
	const surface = "tools"

	if schema == nil {
		return nil
	}
	raw, err := json.Marshal(schema)
	if err != nil {
		return nil
	}
	var decoded any
	if json.Unmarshal(raw, &decoded) != nil {
		return nil
	}
	var found []offender
	walkProse(decoded, func(text string) {
		found = append(found, scanText(surface, where, text)...)
	})
	return found
}

// proseKeys are the schema keys whose values are prose a gateway reads. The
// set is closed on purpose: everything else in a schema is a name, a type or a
// constraint, and holding those to an English-prose rule would report noise.
var proseKeys = map[string]bool{"description": true, "title": true}

// walkProse visits every prose string in a decoded JSON document.
func walkProse(node any, visit func(string)) {
	switch value := node.(type) {
	case map[string]any:
		for key, child := range value {
			if text, isText := child.(string); isText && proseKeys[key] {
				visit(text)
				continue
			}
			walkProse(child, visit)
		}
	case []any:
		for _, child := range value {
			walkProse(child, visit)
		}
	}
}

// scanText reports each offending character in one served string, once per
// string, with an excerpt centered on the first hit. A hit is any listed ASCII
// character or any rune above U+007F.
func scanText(surface, where, text string) []offender {
	index := -1
	for i, r := range text {
		if r > unicode.MaxASCII || slices.Contains(offendingChars, r) {
			index = i
			break
		}
	}
	if index < 0 {
		return nil
	}
	start := max(index-30, 0)
	end := min(index+30, len(text))
	excerpt := strings.ReplaceAll(text[start:end], "\n", " ")
	if fullStrings {
		excerpt = strings.ReplaceAll(text, "\n", "\\n")
	}
	return []offender{{surface: surface, where: where, excerpt: excerpt}}
}
