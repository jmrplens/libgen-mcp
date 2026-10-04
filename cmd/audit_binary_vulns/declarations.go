// declarations.go is the table of findings a release may carry, and the rules
// an entry of it has to meet to be read at all.

package main

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// The reasons a declaration may give. Each is a claim about the finding that a
// reviewer can check, and neither says the vulnerability does not matter.
//
// There is deliberately no category for "linked but not reached": a symbol in
// the binary is what every scanner reports, and a fixed release, even on an
// older minor line, is an upgrade to make rather than an entry to write.
// GO-2026-6443 against google.golang.org/grpc was handled that way, by taking
// v1.83.2 instead of keeping v1.84.0, which no fixed release covers.
const (
	// categoryNotLinked accepts an advisory against a module the binaries
	// link when none of the packages it names is linked: what a scanner reads
	// is the module, and what is vulnerable is not in the binary. The claim is
	// not trusted as written: checkNotLinked remakes it on every run.
	categoryNotLinked = "not-linked"

	// categoryFixNotYetAdoptable accepts an advisory whose fix exists in a
	// release this repository cannot take yet: a Go point release the CI
	// images do not carry, or a module release still inside the Dependabot
	// cooldown. The entry turns stale, and fails the run, the day the upgrade
	// lands, which is what removes it.
	categoryFixNotYetAdoptable = "fix-not-yet-adoptable"
)

// categories are the reasons a declaration may give, each with what it means.
// A declaration naming anything else is reported rather than trusted, since a
// category nobody defined is an excuse nobody reviewed.
var categories = map[string]string{
	categoryNotLinked:          "the binaries link the module and none of the packages the advisory names, which every run checks against go list -deps of each release target; the reason names the packages and what would make it false",
	categoryFixNotYetAdoptable: "a fixed version exists and cannot be taken yet; the reason names the version and where its adoption is tracked",
}

// declaration is one accepted finding: why it may ship, in a category and in
// words.
type declaration struct {
	category string
	reason   string
}

// acceptedAdvisories are the findings a release may carry, each with the reason
// that is right, keyed by the advisory's id and the module it was found in,
// separated by one space: "GO-2026-5932 golang.org/x/crypto", for instance.
//
// An entry is an advisory shipped on purpose in every binary the release
// publishes, which is why each one needs a category and a reason a reviewer
// can check. The module is part of the key so the same advisory reaching the
// binaries through another module is a new finding rather than one already
// excused.
//
// The table is empty. Its one entry, that GO-2026-5932 against
// golang.org/x/crypto was not-linked, went in 2.2.1 with the module itself:
// the binaries carried x/crypto only for the ocsp package pdfcpu's signature
// code imports, and pdfcpu left when the outline reader moved to the PDF
// library the text path uses.
var acceptedAdvisories = map[string]declaration{}

// advisoryID is the shape of a Go vulnerability database id.
var advisoryID = regexp.MustCompile(`^GO-\d{4}-\d{4,}$`)

// declarationKey is how a finding is named in the declaration table.
func declarationKey(osv, module string) string {
	return osv + " " + module
}

// invalidDeclarations names every declaration that cannot be read as written,
// with what is wrong with it, in a stable order.
func invalidDeclarations(declared map[string]declaration) []string {
	var invalid []string
	for key, entry := range declared {
		fields := strings.Fields(key)
		if len(fields) != 2 || key != declarationKey(fields[0], fields[1]) || !advisoryID.MatchString(fields[0]) {
			invalid = append(invalid, fmt.Sprintf("%q: the key is not an advisory id and a module separated by one space", key))
			continue
		}
		if categories[entry.category] == "" {
			invalid = append(invalid, fmt.Sprintf("%q: the category %q is not one this command defines", key, entry.category))
			continue
		}
		if strings.TrimSpace(entry.reason) == "" {
			invalid = append(invalid, fmt.Sprintf("%q: gives no reason", key))
		}
	}
	sort.Strings(invalid)
	return invalid
}
