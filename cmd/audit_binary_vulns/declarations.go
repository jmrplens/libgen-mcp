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
// reviewer can check, and none says the vulnerability does not matter.
const (
	// categoryNotLinked accepts an advisory against a module the binaries
	// link when none of the packages or symbols it names is linked: what a
	// scanner reads is the module, and what is vulnerable is not in the
	// binary.
	categoryNotLinked = "not-linked"

	// categoryFixNotYetAdoptable accepts an advisory whose fix exists in a
	// release this repository cannot take yet: a Go point release the CI
	// images do not carry, or a module release still inside the Dependabot
	// cooldown. The entry turns stale, and fails the run, the day the upgrade
	// lands, which is what removes it.
	categoryFixNotYetAdoptable = "fix-not-yet-adoptable"

	// categoryNotReachable accepts an advisory whose vulnerable symbols the
	// binaries do link, when the source scan (`make govulncheck`, symbol
	// grain) finds no call path from this module's code to them and no
	// release on the module's current minor line carries a fix. It is the
	// weakest of the three, which is why it asks for both halves: a fix on
	// the line the module is on is an upgrade to make, not an entry to write.
	categoryNotReachable = "not-reachable"
)

// categories are the reasons a declaration may give, each with what it means.
// A declaration naming anything else is reported rather than trusted, since a
// category nobody defined is an excuse nobody reviewed.
var categories = map[string]string{
	categoryNotLinked:          "the binaries link the module and none of the packages or symbols the advisory names, which a symbol-level scan of an unstripped build shows; the reason names the packages",
	categoryFixNotYetAdoptable: "a fixed version exists and cannot be taken yet; the reason names the version and where its adoption is tracked",
	categoryNotReachable:       "the binaries link the vulnerable symbols, the symbol-level source scan reaches none of them, and no release on the module's current minor line is fixed; the reason names the symbols, why nothing reaches them, and every fixed version that exists",
}

// declaration is one accepted finding: why it may ship, in a category and in
// words.
type declaration struct {
	category string
	reason   string
}

// acceptedAdvisories are the findings a release may carry, each with the reason
// that is right, keyed by the advisory's id and the module it was found in,
// separated by one space: "GO-2026-5932 golang.org/x/crypto".
//
// An entry is an advisory shipped on purpose in every binary the release
// publishes, which is why each one needs a category and a reason a reviewer
// can check. The module is part of the key so the same advisory reaching the
// binaries through another module is a new finding rather than one already
// excused.
var acceptedAdvisories = map[string]declaration{
	"GO-2026-5932 golang.org/x/crypto": {
		category: categoryNotLinked,
		reason: "the advisory names x/crypto's openpgp packages, and the one package of the module the binaries link is " +
			"golang.org/x/crypto/ocsp, which github.com/pdfcpu/pdfcpu/pkg/pdfcpu/sign imports and internal/extract " +
			"reaches through pdfcpu (go list -deps ./cmd/server, on every release target); the advisory has no " +
			"fixed version, so this goes stale only when pdfcpu stops importing ocsp",
	},
	"GO-2026-6443 google.golang.org/grpc": {
		category: categoryNotReachable,
		reason: "the binaries link transport.http2Server.HandleStreams, the server half of gRPC's HTTP/2 transport, but " +
			"the server uses gRPC only as the OTLP exporters' client and starts no gRPC server, and make govulncheck " +
			"reports the advisory at package grain only; the fixes are v1.82.2 and v1.83.2, older lines than the " +
			"v1.84.0 that github.com/grpc-ecosystem/grpc-gateway/v2 v2.31.0 requires, and an untagged master commit " +
			"(v1.85.0-dev), so no v1.84 release is fixed and this goes stale with the first grpc release that is",
	},
}

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
