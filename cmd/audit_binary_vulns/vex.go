// vex.go writes the OpenVEX document the declaration table implies, holds the
// committed copy of it to the table, and stamps the copy a release publishes.

package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"regexp"
	"slices"
	"sort"
	"strings"
	"time"
)

// The fixed parts of every document this command writes.
const (
	// vexContext is the OpenVEX specification version the documents follow.
	vexContext = "https://openvex.dev/ns/v0.2.0"

	// vexAuthor is who makes the statements: the maintainer, through the gate.
	vexAuthor = "José M. Requena Plens"

	// vexTooling names what writes the documents, so a reader can find the
	// table they are generated from.
	vexTooling = "https://github.com/jmrplens/libgen-mcp/tree/main/cmd/audit_binary_vulns"

	// vexCommittedID is the @id of the committed document, which is where it
	// lives on the default branch.
	vexCommittedID = "https://github.com/jmrplens/libgen-mcp/blob/main/.vex/libgen-mcp.openvex.json"

	// vexReleaseAsset is the name the release publishes its copy under.
	vexReleaseAsset = "libgen-mcp.openvex.json"

	// vexStatus and vexJustification are the only verdict a not-linked
	// declaration supports: the module is in the binary and the advisory's
	// packages are not.
	vexStatus        = "not_affected"
	vexJustification = "vulnerable_code_not_present"

	// vexModulePURL is the server's own main module, which Trivy reports as
	// the root of every Go binary it reads, in an image or on its own.
	vexModulePURL = golangPURLPrefix + "github.com/jmrplens/libgen-mcp/v2"

	// golangPURLPrefix opens the purl of a Go module, which is how a
	// statement names the module an advisory is filed against.
	golangPURLPrefix = "pkg:golang/"

	// vexImageName is the image's last path element, the name an OCI purl
	// carries.
	vexImageName = "libgen-mcp"

	// vexDockerHubRepo and vexGHCRRepo are the two repositories the release
	// pushes the one image index to.
	vexDockerHubRepo = "jmrplens/libgen-mcp"
	vexGHCRRepo      = "ghcr.io/jmrplens/libgen-mcp"
)

// vexDocument is an OpenVEX document, with only the fields this command
// writes. Decoding refuses any other, so a field added by hand to the
// committed copy is reported rather than carried silently.
type vexDocument struct {
	Context    string         `json:"@context"`
	ID         string         `json:"@id"`
	Author     string         `json:"author"`
	Timestamp  string         `json:"timestamp"`
	Version    int            `json:"version"`
	Tooling    string         `json:"tooling"`
	Statements []vexStatement `json:"statements"`
}

// vexStatement is one advisory declared not to affect the products.
type vexStatement struct {
	Vulnerability   vexVulnerability `json:"vulnerability"`
	Products        []vexProduct     `json:"products"`
	Status          string           `json:"status"`
	Justification   string           `json:"justification"`
	ImpactStatement string           `json:"impact_statement"`
}

// vexVulnerability names the advisory, and where it is published.
type vexVulnerability struct {
	ID   string `json:"@id"`
	Name string `json:"name"`
}

// vexProduct is one identifier the products go by, with the module the
// advisory is filed against as its subcomponent.
type vexProduct struct {
	ID            string         `json:"@id"`
	Subcomponents []vexComponent `json:"subcomponents"`
}

// vexComponent is a subcomponent, named by its purl.
type vexComponent struct {
	ID string `json:"@id"`
}

// vexRelease pins a document to one release: its version and the digest of
// the image index it pushed to both registries.
type vexRelease struct {
	version     string
	indexDigest string
}

// releaseVersion and indexDigest are the shapes a release's pins must have.
var (
	releaseVersion = regexp.MustCompile(`^\d+\.\d+\.\d+(-[0-9A-Za-z.-]+)?$`)
	indexDigest    = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)
)

// validate refuses a release pin that would write an identifier no scanner
// computes.
func (r vexRelease) validate() error {
	if !releaseVersion.MatchString(r.version) {
		return fmt.Errorf("-vex-release %q is not a version without its leading v, e.g. 2.2.0", r.version)
	}
	if !indexDigest.MatchString(r.indexDigest) {
		return fmt.Errorf("-vex-index-digest %q is not a sha256 digest", r.indexDigest)
	}
	return nil
}

// ociPURL is the purl Trivy computes for the image in one repository, pinned
// to the index digest when there is one.
func ociPURL(repositoryURL, digest string) string {
	version := ""
	if digest != "" {
		version = "@" + strings.Replace(digest, ":", "%3A", 1)
	}
	return "pkg:oci/" + vexImageName + version + "?repository_url=" + strings.ReplaceAll(repositoryURL, "/", "%2F")
}

// dockerPURL is the purl Docker Scout uses for the image: a Docker Hub image
// by its repository, any other registry named in a qualifier, and the tag as
// the version.
func dockerPURL(tag, registry string) string {
	purl := "pkg:docker/" + vexDockerHubRepo
	if tag != "" {
		purl += "@" + tag
	}
	if registry != "" {
		purl += "?repository_url=" + registry
	}
	return purl
}

// vexProducts are the identifiers a statement is made about.
//
// Without a release they carry no version, which is what the committed
// document says: every build of the server, under every name a scanner gives
// it. A release's copy pins each one to that release, and spells the module
// version both ways, because Trivy reads it from the version the build stamps
// (2.2.0) and a reader of the module path writes it with the v.
func vexProducts(rel *vexRelease) []string {
	if rel == nil {
		return []string{
			vexModulePURL,
			ociPURL(vexGHCRRepo, ""),
			ociPURL("index.docker.io/"+vexDockerHubRepo, ""),
			dockerPURL("", ""),
			dockerPURL("", "ghcr.io"),
		}
	}
	return []string{
		vexModulePURL + "@" + rel.version,
		vexModulePURL + "@v" + rel.version,
		ociPURL(vexGHCRRepo, rel.indexDigest),
		ociPURL("index.docker.io/"+vexDockerHubRepo, rel.indexDigest),
		dockerPURL(rel.version, ""),
		dockerPURL(rel.version, "ghcr.io"),
	}
}

// vexStatements are the statements the table implies: one per not-linked
// declaration, in key order. A fix-not-yet-adoptable declaration implies
// none, since the code it excuses is in the binary.
func vexStatements(declared map[string]declaration, rel *vexRelease) []vexStatement {
	keys := make([]string, 0, len(declared))
	for key, entry := range declared {
		if entry.category == categoryNotLinked {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)

	statements := make([]vexStatement, 0, len(keys))
	for _, key := range keys {
		osv, module, _ := strings.Cut(key, " ")
		products := vexProducts(rel)
		statement := vexStatement{
			Vulnerability:   vexVulnerability{ID: "https://pkg.go.dev/vuln/" + osv, Name: osv},
			Products:        make([]vexProduct, 0, len(products)),
			Status:          vexStatus,
			Justification:   vexJustification,
			ImpactStatement: declared[key].reason,
		}
		for _, id := range products {
			statement.Products = append(statement.Products, vexProduct{
				ID:            id,
				Subcomponents: []vexComponent{{ID: golangPURLPrefix + module}},
			})
		}
		statements = append(statements, statement)
	}
	return statements
}

// statementKeys names each statement by the declaration key it stands for:
// the advisory and every module its products name as a subcomponent.
func statementKeys(s vexStatement) []string {
	var keys []string
	for _, product := range s.Products {
		for _, sub := range product.Subcomponents {
			module, isGo := strings.CutPrefix(sub.ID, golangPURLPrefix)
			if module, _, _ = strings.Cut(module, "@"); !isGo || module == "" {
				continue
			}
			key := declarationKey(s.Vulnerability.Name, module)
			if !slices.Contains(keys, key) {
				keys = append(keys, key)
			}
		}
	}
	sort.Strings(keys)
	return keys
}

// vexDrift lists every way the committed document disagrees with the table,
// in a stable order: a statement no not-linked declaration stands behind, a
// not-linked declaration with no statement, and a statement whose text is not
// the one the table writes.
func vexDrift(doc vexDocument, declared map[string]declaration) []string {
	var drift []string
	if doc.Context != vexContext || doc.ID != vexCommittedID || doc.Author != vexAuthor || doc.Tooling != vexTooling {
		drift = append(drift, "the document header (@context, @id, author, tooling) is not the one this command writes")
	}
	if _, err := time.Parse(time.RFC3339, doc.Timestamp); err != nil || doc.Version < 1 {
		drift = append(drift, "the document has no RFC 3339 timestamp or no version of at least 1")
	}

	want := map[string]vexStatement{}
	for _, s := range vexStatements(declared, nil) {
		want[declarationKey(s.Vulnerability.Name, strings.TrimPrefix(s.Products[0].Subcomponents[0].ID, golangPURLPrefix))] = s
	}
	seen := map[string]bool{}
	for _, s := range doc.Statements {
		drift = append(drift, judgeStatement(s, want, declared, seen)...)
	}
	for key := range want {
		if !seen[key] {
			drift = append(drift, fmt.Sprintf("%q is declared not-linked and has no statement", key))
		}
	}
	sort.Strings(drift)
	return drift
}

// judgeStatement holds one committed statement to the one the table writes
// for it, marking the keys it covers as seen.
func judgeStatement(s vexStatement, want map[string]vexStatement, declared map[string]declaration, seen map[string]bool) []string {
	keys := statementKeys(s)
	if len(keys) != 1 {
		return []string{fmt.Sprintf("the statement on %q names %d advisory and module pairs, want exactly one", s.Vulnerability.Name, len(keys))}
	}
	key := keys[0]
	expected, ok := want[key]
	switch {
	case !ok && declared[key].category == categoryFixNotYetAdoptable:
		return []string{fmt.Sprintf("%q is declared %s, so the code is in the binary and no statement may say otherwise", key, categoryFixNotYetAdoptable)}
	case !ok:
		return []string{fmt.Sprintf("%q has a statement and no not-linked declaration behind it", key)}
	case seen[key]:
		return []string{fmt.Sprintf("%q has more than one statement", key)}
	}
	seen[key] = true
	if !jsonEqual(s, expected) {
		return []string{fmt.Sprintf("the statement on %q is not the one the declaration writes (run make gen-vex)", key)}
	}
	return nil
}

// jsonEqual compares two values as the bytes they encode to.
func jsonEqual(a, b any) bool {
	ja, errA := json.Marshal(a)
	jb, errB := json.Marshal(b)
	return errA == nil && errB == nil && bytes.Equal(ja, jb)
}

// readVEX decodes a document strictly: an unknown field, trailing data or a
// file that is not one JSON object is an error.
func readVEX(path string) (vexDocument, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return vexDocument{}, err
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	var doc vexDocument
	if decodeErr := dec.Decode(&doc); decodeErr != nil {
		return vexDocument{}, fmt.Errorf("%s: %w", path, decodeErr)
	}
	if dec.More() {
		return vexDocument{}, fmt.Errorf("%s: trailing data after the document", path)
	}
	return doc, nil
}

// encodeVEX is the document as it is written: indented, with the characters
// HTML would read left alone, and a final newline.
func encodeVEX(doc vexDocument) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(doc); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// now is the clock a written document is stamped with, behind a seam so a
// test can fix it.
var now = func() time.Time { return time.Now().UTC().Truncate(time.Second) }

// committedVEX is the document the table implies, keeping the timestamp and
// version of the one at path while its statements are unchanged, and
// advancing both when they are not: an OpenVEX version is a revision of the
// statements, not of the file.
func committedVEX(path string, declared map[string]declaration) (vexDocument, error) {
	doc := vexDocument{
		Context:    vexContext,
		ID:         vexCommittedID,
		Author:     vexAuthor,
		Timestamp:  now().Format(time.RFC3339),
		Version:    1,
		Tooling:    vexTooling,
		Statements: vexStatements(declared, nil),
	}
	old, err := readVEX(path)
	switch {
	case errors.Is(err, os.ErrNotExist):
		return doc, nil
	case err != nil:
		return vexDocument{}, err
	case jsonEqual(old.Statements, doc.Statements):
		doc.Timestamp, doc.Version = old.Timestamp, old.Version
	default:
		doc.Version = old.Version + 1
	}
	return doc, nil
}

// releaseVEX is the copy a release publishes: the committed statements, each
// product pinned to the release, under the release asset's own address.
func releaseVEX(declared map[string]declaration, rel vexRelease) vexDocument {
	return vexDocument{
		Context:    vexContext,
		ID:         "https://github.com/jmrplens/libgen-mcp/releases/download/v" + rel.version + "/" + vexReleaseAsset,
		Author:     vexAuthor,
		Timestamp:  now().Format(time.RFC3339),
		Version:    1,
		Tooling:    vexTooling,
		Statements: vexStatements(declared, &rel),
	}
}

// vexConfig is one VEX run: the committed document to check or rewrite, and
// the release copy to write, if any.
type vexConfig struct {
	check, write, out string
	release           vexRelease
	declared          map[string]declaration
}

// requested reports whether any VEX flag was given, which turns the run into a
// VEX run and away from building and scanning.
func (c vexConfig) requested() bool {
	return c.check != "" || c.write != "" || c.out != "" || c.release != (vexRelease{})
}

// validate refuses a combination of flags that says two things at once, or
// half of one.
func (c vexConfig) validate() error {
	switch {
	case c.write != "" && (c.check != "" || c.out != "" || c.release != (vexRelease{})):
		return errors.New("-vex-write rewrites the committed document and takes no other -vex flag")
	case c.write != "":
		return nil
	case c.check == "":
		return errors.New("-vex-out, -vex-release and -vex-index-digest need -vex-check: a release copy is written only from a committed document that matches the table")
	case c.out == "" && c.release != (vexRelease{}):
		return errors.New("-vex-release and -vex-index-digest pin the copy -vex-out writes, and there is no -vex-out")
	case c.out != "":
		return c.release.validate()
	}
	return nil
}

// runVEX checks, rewrites or stamps a document and returns the exit code: 0
// when the committed copy matches the table (and the release copy, if asked
// for, is written), 1 on drift, 2 when the run could not be made.
func runVEX(cfg vexConfig, stdout, stderr io.Writer) int {
	if cfg.write != "" {
		return writeCommittedVEX(cfg, stdout, stderr)
	}
	doc, err := readVEX(cfg.check)
	if err != nil {
		fmt.Fprintf(stderr, "%s: %v\n", toolName, err)
		return 2
	}
	if drift := vexDrift(doc, cfg.declared); len(drift) != 0 {
		for _, problem := range drift {
			fmt.Fprintf(stdout, "VEX DRIFT %s\n", problem)
		}
		fmt.Fprintf(stdout, "%s: FAILED: %s disagrees with the declarations in %d ways (make gen-vex rewrites it)\n", toolName, cfg.check, len(drift))
		return 1
	}
	fmt.Fprintf(stdout, "%s: %s states exactly the %d not-linked declarations\n", toolName, cfg.check, len(doc.Statements))
	if cfg.out == "" {
		return 0
	}
	return writeVEX(cfg.out, releaseVEX(cfg.declared, cfg.release), stdout, stderr)
}

// writeCommittedVEX rewrites the committed document from the table.
func writeCommittedVEX(cfg vexConfig, stdout, stderr io.Writer) int {
	doc, err := committedVEX(cfg.write, cfg.declared)
	if err != nil {
		fmt.Fprintf(stderr, "%s: %v\n", toolName, err)
		return 2
	}
	return writeVEX(cfg.write, doc, stdout, stderr)
}

// writeVEX writes one document and says where.
func writeVEX(path string, doc vexDocument, stdout, stderr io.Writer) int {
	data, err := encodeVEX(doc)
	if err == nil {
		err = os.WriteFile(path, data, 0o644) //#nosec G306 -- a public document, committed and published for anyone to read
	}
	if err != nil {
		fmt.Fprintf(stderr, "%s: %v\n", toolName, err)
		return 2
	}
	fmt.Fprintf(stdout, "%s: wrote %s (%d statements, version %d)\n", toolName, path, len(doc.Statements), doc.Version)
	return 0
}
